package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/scheduler"
	"github.com/2017fighting/mdc-lite/internal/store"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// env is one test's world: a real SQLite file (real migrations) and the task store
// over it. The network is not involved: the scheduler only claims rows and hands
// them to a Runner, so the Runner is the one double.
type env struct {
	raw *store.Store
	st  *tasks.Store
	ctx context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	raw, err := store.Open(ctx, filepath.Join(t.TempDir(), "mdc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	return &env{raw: raw, st: tasks.New(raw, tasks.Config{RetryLimit: 5}), ctx: ctx}
}

// enqueue creates one queued task and returns its id. Each task gets its own batch
// so the tests can create them in a known order without racing the wire format.
func (e *env) enqueue(t *testing.T, suffix string) int64 {
	t.Helper()
	res, err := e.st.CreateBatch(e.ctx, tasks.Intake{
		Trigger:    tasks.TriggerManual,
		TriggerRef: "/media/in",
		PipelineID: "main",
		Groups: []tasks.Group{{
			Number: "ABC-123",
			Files: []tasks.InputFile{{
				Path:      filepath.Join("/media/in", suffix+".mp4"),
				Kind:      tasks.KindVideo,
				Role:      tasks.RolePrimary,
				PartIndex: 1,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := e.raw.Write().QueryRowContext(e.ctx,
		`SELECT id FROM tasks WHERE batch_id = ?`, res.BatchID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) task(t *testing.T, id int64) tasks.Task {
	t.Helper()
	task, err := e.st.Task(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func (e *env) run(t *testing.T, taskID int64) tasks.Run {
	t.Helper()
	runs, err := e.st.Runs(e.ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) == 0 {
		t.Fatalf("task %d has no runs", taskID)
	}
	return runs[len(runs)-1]
}

func finishRun(st *tasks.Store, runID int64, status tasks.RunStatus) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return st.FinishRun(ctx, runID, tasks.Outcome{Status: status})
}

// testRunner is the pool's test double. It records the in-flight peak, announces
// every start, and lets each test decide how the run ends.
type testRunner struct {
	started chan tasks.Task
	run     func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error)

	mu       sync.Mutex
	inflight int
	peak     int
}

func newTestRunner(run func(context.Context, tasks.Task, tasks.Run) (tasks.Outcome, error)) *testRunner {
	return &testRunner{started: make(chan tasks.Task, 16), run: run}
}

func (r *testRunner) RunClaimed(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
	r.mu.Lock()
	r.inflight++
	if r.inflight > r.peak {
		r.peak = r.inflight
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inflight--
		r.mu.Unlock()
	}()
	r.started <- task
	return r.run(ctx, task, run)
}

func (r *testRunner) peakInflight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

func (r *testRunner) inFlight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inflight
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func start(t *testing.T, s *scheduler.Scheduler, ctx context.Context) chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := s.Run(ctx); err != nil {
			t.Errorf("scheduler.Run: %v", err)
		}
	}()
	return done
}

// TestSlotsRunAtMostConfiguredTasks is the `concurrency.tasks` nail: with two
// slots, the third task may not start until one of the first two has finished.
func TestSlotsRunAtMostConfiguredTasks(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	runner := newTestRunner(func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return tasks.Outcome{}, ctx.Err()
		}
		return tasks.Outcome{Status: tasks.RunSucceeded}, finishRun(e.st, run.ID, tasks.RunSucceeded)
	})
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: runner, Tasks: 2, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := start(t, s, ctx)

	ids := []int64{e.enqueue(t, "a"), e.enqueue(t, "b"), e.enqueue(t, "c")}
	s.Notify()

	// Two starts, then no third while both slots are held.
	for i := 0; i < 2; i++ {
		select {
		case <-runner.started:
		case <-time.After(10 * time.Second):
			t.Fatal("two tasks never started")
		}
	}
	select {
	case task := <-runner.started:
		t.Fatalf("a third task (%d) started while both slots were busy", task.ID)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	waitFor(t, "all three tasks to end", func() bool {
		for _, id := range ids {
			if !e.task(t, id).Status.Terminal() {
				return false
			}
		}
		return true
	})
	if got := runner.peakInflight(); got != 2 {
		t.Errorf("peak in-flight tasks = %d, want exactly the two configured slots", got)
	}
	cancel()
	<-done
}

// TestSlotIsFreedOnlyAfterTheFinishTransactionCommits pins the AC's ordering: the
// slot is released after RunClaimed returns, and RunClaimed only returns after
// FinishRun committed. The runner therefore finishes a run, blocks before
// returning, and a queued third task must stay unstarted until the block opens.
func TestSlotIsFreedOnlyAfterTheFinishTransactionCommits(t *testing.T) {
	e := newEnv(t)
	afterFinish := make(chan struct{})
	var once sync.Once
	runner := newTestRunner(func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
		if err := finishRun(e.st, run.ID, tasks.RunSucceeded); err != nil {
			return tasks.Outcome{}, err
		}
		once.Do(func() { <-afterFinish })
		return tasks.Outcome{Status: tasks.RunSucceeded}, nil
	})
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: runner, Tasks: 1, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := start(t, s, ctx)

	ids := []int64{e.enqueue(t, "a"), e.enqueue(t, "b")}
	s.Notify()
	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first task never started")
	}
	waitFor(t, "the first run to commit", func() bool { return e.task(t, ids[0]).Status == tasks.StatusSucceeded })
	// The first run is committed but its slot has not been released: the second
	// task must not have started.
	select {
	case task := <-runner.started:
		t.Fatalf("task %d started before the first slot was released", task.ID)
	case <-time.After(100 * time.Millisecond):
	}
	close(afterFinish)
	select {
	case task := <-runner.started:
		if task.ID != ids[1] {
			t.Fatalf("started task %d, want the FIFO's second task %d", task.ID, ids[1])
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the second task never started after the slot was released")
	}
	waitFor(t, "the second task to end", func() bool { return e.task(t, ids[1]).Status.Terminal() })
	cancel()
	<-done
}

// TestPrefixIsSingleFlight: a tick (TryPrefix) is skipped while a manual prefix
// runs; a manual prefix (Prefix) queues behind it.
func TestPrefixIsSingleFlight(t *testing.T) {
	e := newEnv(t)
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: newTestRunner(func(context.Context, tasks.Task, tasks.Run) (tasks.Outcome, error) {
		return tasks.Outcome{Status: tasks.RunSucceeded}, nil
	}), Tasks: 1, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	release := make(chan struct{})
	started := make(chan string, 2)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- s.Prefix(ctx, func(context.Context) error {
			started <- "first"
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first prefix never started")
	}

	// A tick does not wait: it is skipped, and the caller can move its watermark's
	// decision into "this round did nothing".
	err = s.TryPrefix(ctx, func(context.Context) error {
		t.Error("TryPrefix ran while the prefix slot was held")
		return nil
	})
	if !errors.Is(err, scheduler.ErrPrefixBusy) {
		t.Fatalf("TryPrefix = %v, want ErrPrefixBusy", err)
	}

	// A manual prefix queues and runs as soon as the first one releases.
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- s.Prefix(ctx, func(context.Context) error {
			started <- "second"
			return nil
		})
	}()
	select {
	case <-started:
		t.Fatal("the queued prefix started before the in-flight one finished")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the queued prefix never started")
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

// TestCancelRunningTaskEndsItCancelled: a user cancel travels on the task's own
// context, the runner recognises the cause, and the finish releases the paths.
func TestCancelRunningTaskEndsItCancelled(t *testing.T) {
	e := newEnv(t)
	runner := newTestRunner(func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
		<-ctx.Done()
		status := tasks.RunInterrupted
		if errors.Is(context.Cause(ctx), tasks.ErrCancelledByUser) {
			status = tasks.RunCancelled
		}
		return tasks.Outcome{Status: status}, finishRun(e.st, run.ID, status)
	})
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: runner, Tasks: 1, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := start(t, s, ctx)

	id := e.enqueue(t, "cancel-me")
	s.Notify()
	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the task never started")
	}

	if err := s.Cancel(ctx, id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	task := e.task(t, id)
	if task.Status != tasks.StatusCancelled {
		t.Errorf("task status = %q, want cancelled", task.Status)
	}
	files, err := e.st.TaskFiles(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Active {
			t.Errorf("%s is still active after a cancel: the path must be released", f.Path)
		}
	}
	if r := e.run(t, id); r.Status != tasks.RunCancelled {
		t.Errorf("run status = %q, want cancelled", r.Status)
	}

	// Cancelling an ended task is a conflict, not a silent success.
	if err := s.Cancel(ctx, id); !errors.Is(err, scheduler.ErrTaskTerminal) {
		t.Errorf("second Cancel = %v, want ErrTaskTerminal", err)
	}
	cancel()
	<-done
}

// TestCancelQueuedTaskReleasesItsPaths: a task that never ran is cancelled in the
// database, so a later scan can claim the same files again.
func TestCancelQueuedTaskReleasesItsPaths(t *testing.T) {
	e := newEnv(t)
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: newTestRunner(func(context.Context, tasks.Task, tasks.Run) (tasks.Outcome, error) {
		return tasks.Outcome{Status: tasks.RunSucceeded}, nil
	}), Tasks: 1})
	if err != nil {
		t.Fatal(err)
	}
	id := e.enqueue(t, "queued")
	if err := s.Cancel(e.ctx, id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if got := e.task(t, id).Status; got != tasks.StatusCancelled {
		t.Errorf("task status = %q, want cancelled", got)
	}
	files, err := e.st.TaskFiles(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Active {
			t.Errorf("%s is still active after a cancel", f.Path)
		}
	}
}

// TestShutdownInterruptsARunThatOutlivesTheGrace: the grace period is bounded, and
// the survivor is recorded `interrupted` on a context that is not the cancelled
// root. The task goes back to the queue, which is what makes the next start retry.
func TestShutdownInterruptsARunThatOutlivesTheGrace(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	runner := newTestRunner(func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
		<-ctx.Done()
		// Ignore the cancellation past the grace period, like a task stuck in a
		// non-context-aware filesystem call. The scheduler has to record it.
		<-release
		return tasks.Outcome{Status: tasks.RunInterrupted}, nil
	})
	s, err := scheduler.New(scheduler.Config{
		Store: e.st, Runner: runner, Tasks: 1, Poll: time.Hour,
		Grace: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := start(t, s, ctx)

	id := e.enqueue(t, "slow")
	s.Notify()
	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the task never started")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the scheduler did not return after its grace period")
	}
	if r := e.run(t, id); r.Status != tasks.RunInterrupted || r.FinishedAt == nil {
		t.Errorf("run = %+v, want interrupted with a finished_at", r)
	}
	if got := e.task(t, id).Status; got != tasks.StatusQueued {
		t.Errorf("task status = %q, want queued (interrupted runs are retried)", got)
	}
	close(release)
	waitFor(t, "the stuck runner to return", func() bool { return runner.inFlight() == 0 })
}

// TestRerunRefusesARunningTask: a task is one unit of work, so asking for its rerun
// while a slot has it is a conflict.
func TestRerunRefusesARunningTask(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	runner := newTestRunner(func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
		<-release
		return tasks.Outcome{Status: tasks.RunSucceeded}, finishRun(e.st, run.ID, tasks.RunSucceeded)
	})
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: runner, Tasks: 1, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := start(t, s, ctx)

	id := e.enqueue(t, "running")
	s.Notify()
	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the task never started")
	}
	if err := s.Rerun(ctx, id); !errors.Is(err, scheduler.ErrTaskRunning) {
		t.Errorf("Rerun of a running task = %v, want ErrTaskRunning", err)
	}
	close(release)
	waitFor(t, "the task to finish", func() bool { return e.task(t, id).Status.Terminal() })
	cancel()
	<-done
}

// TestRerunOfAFinishedTaskOpensANewRun: the rerun is queued (paths re-claimed) and
// the pool picks it up as normal work.
func TestRerunOfAFinishedTaskOpensANewRun(t *testing.T) {
	e := newEnv(t)
	runner := newTestRunner(func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
		return tasks.Outcome{Status: tasks.RunSucceeded}, finishRun(e.st, run.ID, tasks.RunSucceeded)
	})
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: runner, Tasks: 1, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := start(t, s, ctx)

	id := e.enqueue(t, "rerun")
	s.Notify()
	waitFor(t, "the first run to finish", func() bool { return e.task(t, id).Status == tasks.StatusSucceeded })

	if err := s.Rerun(ctx, id); err != nil {
		t.Fatalf("Rerun: %v", err)
	}
	waitFor(t, "the rerun to finish", func() bool {
		runs, err := e.st.Runs(ctx, id)
		return err == nil && len(runs) == 2 && runs[1].Status == tasks.RunSucceeded
	})
	cancel()
	<-done
}

// TestFIFOIsTheQueueOrder: the pool takes tasks in `created_at, id` order.
func TestFIFOIsTheQueueOrder(t *testing.T) {
	e := newEnv(t)
	releases := make(chan struct{})
	runner := newTestRunner(func(ctx context.Context, task tasks.Task, run tasks.Run) (tasks.Outcome, error) {
		<-releases
		return tasks.Outcome{Status: tasks.RunSucceeded}, finishRun(e.st, run.ID, tasks.RunSucceeded)
	})
	s, err := scheduler.New(scheduler.Config{Store: e.st, Runner: runner, Tasks: 1, Poll: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := start(t, s, ctx)

	var want []int64
	for _, name := range []string{"a", "b", "c"} {
		want = append(want, e.enqueue(t, name))
	}
	s.Notify()
	for i, id := range want {
		select {
		case task := <-runner.started:
			if task.ID != id {
				t.Fatalf("started task %d, want FIFO position %d = %d", task.ID, i, id)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("task %d never started", id)
		}
		// Let exactly one finish before the next can be claimed.
		releases <- struct{}{}
		waitFor(t, fmt.Sprintf("task %d to end", id), func() bool { return e.task(t, id).Status.Terminal() })
	}
	cancel()
	<-done
}
