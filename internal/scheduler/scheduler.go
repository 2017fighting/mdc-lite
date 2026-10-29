// Package scheduler is the process's concurrency model: the task slots that run
// one task each, the single-flight prefix slot that keeps scan/filter/group from
// walking the same tree twice, and the two cancellation paths (a human's cancel,
// the process's SIGTERM).
//
// It is the only place that decides how many tasks run at once, so the numbers
// `concurrency.tasks` carries have exactly one reader. The queue itself is not
// here: it is `tasks.status = 'queued'`, read by `tasks.Store.ClaimNext` in the
// same transaction that starts the run.
package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// DefaultGrace is how long a SIGTERM waits for running tasks before the scheduler
// records the survivors as `interrupted`. It is a constant, not a setting: 25s
// leaves 5s of the container's default 30s terminationGracePeriodSeconds for the
// process to exit.
const DefaultGrace = 25 * time.Second

// DefaultPoll is how often the dispatcher looks at the queue without being woken.
// A task is normally woken by Notify (a batch was created, a rerun was asked, a
// slot just freed); the poll is the fallback that also picks up rows another
// command wrote.
const DefaultPoll = 500 * time.Millisecond

// finishDeadline bounds the write that records an interrupted run after the grace
// period. It uses a detached context: the process is on its way out and the root
// context is already cancelled.
const finishDeadline = 10 * time.Second

// Errors callers distinguish.
var (
	// ErrPrefixBusy is what a tick gets when a prefix run is already in flight. It
	// is not a failure: the tick skips and its watermark does not move.
	ErrPrefixBusy = errors.New("scheduler: a prefix run is already in flight")
	// ErrTaskRunning means a cancel or rerun named a task a slot is running.
	ErrTaskRunning = errors.New("scheduler: the task is running")
	// ErrTaskTerminal means a cancel named a task that already ended.
	ErrTaskTerminal = errors.New("scheduler: the task already ended")
	// ErrNotFinished means a rerun named a task that is not finished yet.
	ErrNotFinished = errors.New("scheduler: only a finished task can be rerun")
	// ErrNeedsNumber means a rerun named a task waiting for a human to type a
	// number. Supplying the number is what queues it again.
	ErrNeedsNumber = errors.New("scheduler: the task is waiting for a number")
)

// Runner runs one claimed task. The scheduler claims the task out of the queue and
// opens its run; the runner only executes it. `pipeline.Runner` implements this.
type Runner interface {
	RunClaimed(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error)
}

// Config is what a scheduler needs. Tasks is `concurrency.tasks`; the scheduler
// applies no default of its own, because the one place concurrency is configured
// is `config.yaml`.
type Config struct {
	Store  *tasks.Store
	Runner Runner
	Tasks  int
	Logger *slog.Logger
	// Poll and Grace are defaults-for-tests; zero means DefaultPoll / DefaultGrace.
	Poll  time.Duration
	Grace time.Duration
}

// Scheduler is one process's dispatcher and prefix gate. It is safe for concurrent
// use; Run must be called once.
type Scheduler struct {
	cfg    Config
	logger *slog.Logger

	// prefix is the single-flight gate of the batch-level prefix. A tick TryLocks
	// it, a manual preview or a batch creation Locks it.
	prefix sync.Mutex

	wake  chan struct{}
	slots chan struct{}

	mu      sync.Mutex
	running map[int64]*execution
	wg      sync.WaitGroup
}

// execution is one task in a slot: the run it owns, the cancel that reaches its
// context, and the signal that the slot is free again.
type execution struct {
	run    tasks.Run
	cancel context.CancelCauseFunc
	done   chan struct{}
}

// New builds a scheduler. It refuses a configuration the pool cannot honour,
// rather than starting a process that silently runs nothing.
func New(cfg Config) (*Scheduler, error) {
	if cfg.Store == nil {
		return nil, errors.New("scheduler: no store")
	}
	if cfg.Runner == nil {
		return nil, errors.New("scheduler: no runner")
	}
	if cfg.Tasks < 1 {
		return nil, errors.New("scheduler: concurrency.tasks must be >= 1")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Poll <= 0 {
		cfg.Poll = DefaultPoll
	}
	if cfg.Grace <= 0 {
		cfg.Grace = DefaultGrace
	}
	return &Scheduler{
		cfg:     cfg,
		logger:  logger,
		wake:    make(chan struct{}, 1),
		slots:   make(chan struct{}, cfg.Tasks),
		running: map[int64]*execution{},
	}, nil
}

// Run dispatches until ctx is done, then shuts down gracefully: it stops taking
// new tasks at once, waits up to Grace for the running ones, and records whatever
// is still running as `interrupted` on a detached context before returning.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.cfg.Poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.shutdown(ctx)
			return nil
		case <-s.wake:
		case <-ticker.C:
		}
		s.dispatch(ctx)
	}
}

// Notify wakes the dispatcher. Creating a batch, filling a number or asking for a
// rerun calls it; the periodic poll is only a fallback.
func (s *Scheduler) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Running reports how many tasks have a slot right now. It is for logs and tests,
// not for the API surface.
func (s *Scheduler) Running() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.running)
}

// dispatch fills the free slots with the oldest queued tasks. A slot is taken
// before the claim: a task is never marked running while it waits for a slot.
func (s *Scheduler) dispatch(ctx context.Context) {
	for ctx.Err() == nil {
		select {
		case s.slots <- struct{}{}:
		case <-ctx.Done():
			return
		default:
			// Every slot is busy; the next release wakes us.
			return
		}

		taskCtx, task, run, err := s.claim(ctx)
		if err != nil {
			<-s.slots
			if errors.Is(err, tasks.ErrNoQueuedTask) {
				return
			}
			if ctx.Err() == nil {
				s.logger.Error("the queue could not be read", "err", err)
			}
			return
		}
		s.logger.Info("task slot taken", "task", task.ID, "run", run.ID, "slots", s.cfg.Tasks)
		s.wg.Add(1)
		go s.runOne(taskCtx, task, run)
	}
}

// claim is the critical section that makes a running task always cancellable: the
// task is claimed, its context is created and both are registered under the same
// lock, so Cancel can never see `running` in the database without finding the
// cancel function in the map.
func (s *Scheduler) claim(ctx context.Context) (context.Context, tasks.Task, tasks.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, run, err := s.cfg.Store.ClaimNext(ctx)
	if err != nil {
		return nil, tasks.Task{}, tasks.Run{}, err
	}
	taskCtx, cancel := context.WithCancelCause(ctx)
	s.running[task.ID] = &execution{run: run, cancel: cancel, done: make(chan struct{})}
	return taskCtx, task, run, nil
}

// runOne is one slot's work: run the task, then free the slot only after the run's
// ending has been committed (RunClaimed finishes the run before it returns).
func (s *Scheduler) runOne(ctx context.Context, task tasks.Task, run tasks.Run) {
	defer s.wg.Done()

	s.mu.Lock()
	ex := s.running[task.ID]
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.running, task.ID)
		s.mu.Unlock()
		ex.cancel(nil)
		close(ex.done)
		<-s.slots
		s.Notify()
	}()

	out, err := s.cfg.Runner.RunClaimed(ctx, task, run)
	switch {
	case err != nil:
		s.logger.Error("task run could not be recorded", "task", task.ID, "run", run.ID, "err", err)
	default:
		s.logger.Info("task finished", "task", task.ID, "run", run.ID, "status", out.Status)
	}
}

// Cancel stops one task. A queued or needs_number task is cancelled in the database
// (which releases its paths); a running task is stopped through the same context
// the steps and the Fetcher already honour, and the call returns after its run has
// been finished as `cancelled`.
func (s *Scheduler) Cancel(ctx context.Context, taskID int64) error {
	for {
		task, err := s.cfg.Store.Task(ctx, taskID)
		if err != nil {
			return err
		}
		switch {
		case task.Status == tasks.StatusRunning:
			s.mu.Lock()
			ex := s.running[taskID]
			s.mu.Unlock()
			if ex == nil {
				// The claim transaction committed but this scheduler does not own the
				// task (single replica makes this a bug, not a race); do not pretend
				// to have cancelled it.
				return ErrTaskRunning
			}
			ex.cancel(tasks.ErrCancelledByUser)
			select {
			case <-ex.done:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		case task.Status.Terminal():
			return ErrTaskTerminal
		default:
			_, err := s.cfg.Store.CancelTask(ctx, taskID)
			if errors.Is(err, tasks.ErrTaskRunning) {
				// The dispatcher claimed it between the read and the write. Fall
				// through to the running branch on the next read.
				continue
			}
			if err == nil {
				s.Notify()
			}
			return err
		}
	}
}

// Rerun puts a finished task back in the queue. The scheduler picks it up in its
// normal FIFO order; a task a slot is running is refused (a run cannot be duplicated
// and reopened), and a needs_number task must be given its number first.
func (s *Scheduler) Rerun(ctx context.Context, taskID int64) error {
	task, err := s.cfg.Store.Task(ctx, taskID)
	if err != nil {
		return err
	}
	switch task.Status {
	case tasks.StatusRunning:
		return ErrTaskRunning
	case tasks.StatusNeedsNumber:
		return ErrNeedsNumber
	case tasks.StatusQueued:
		s.Notify()
		return nil
	}
	if _, err := s.cfg.Store.Requeue(ctx, taskID); err != nil {
		if errors.Is(err, tasks.ErrTaskRunning) {
			return ErrTaskRunning
		}
		if errors.Is(err, tasks.ErrNotFinished) {
			return ErrNotFinished
		}
		return err
	}
	s.Notify()
	return nil
}

// Prefix runs the batch-level prefix with the blocking lock: a manual preview or a
// batch creation waits for the walk in flight (at most one), it never stacks.
func (s *Scheduler) Prefix(ctx context.Context, fn func(context.Context) error) error {
	s.prefix.Lock()
	defer s.prefix.Unlock()
	return fn(ctx)
}

// TryPrefix runs the batch-level prefix only when it is free. A monitor tick uses
// this: when another walk is in flight the tick is skipped and its watermark does
// not advance.
func (s *Scheduler) TryPrefix(ctx context.Context, fn func(context.Context) error) error {
	if !s.prefix.TryLock() {
		s.logger.Info("prefix slot is busy; skipping this tick")
		return ErrPrefixBusy
	}
	defer s.prefix.Unlock()
	return fn(ctx)
}

// shutdown stops the dispatcher (ctx is already done), waits up to Grace for the
// running tasks, then records the survivors as interrupted. The interrupt write is
// detached from the cancelled root context and bounded by a short timeout, because
// otherwise the state that says "this run was interrupted" could not be written.
func (s *Scheduler) shutdown(root context.Context) {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(s.cfg.Grace):
	}

	s.mu.Lock()
	exs := make([]*execution, 0, len(s.running))
	for _, ex := range s.running {
		exs = append(exs, ex)
	}
	s.mu.Unlock()
	if len(exs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(root), finishDeadline)
	defer cancel()
	for _, ex := range exs {
		err := s.cfg.Store.FinishRun(ctx, ex.run.ID, tasks.Outcome{Status: tasks.RunInterrupted})
		if err != nil && !errors.Is(err, tasks.ErrRunFinished) {
			s.logger.Error("a run that outlived the grace period could not be marked interrupted",
				"run", ex.run.ID, "err", err)
		}
	}
}
