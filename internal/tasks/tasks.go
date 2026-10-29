// Package tasks owns the task-level state machine on top of the SQLite store:
// the fan-out that turns one batch's groups into tasks, the run lifecycle that
// carries a task's status, the two dedup gates, and the crash recovery that puts
// a killed process's work back in the queue.
//
// It knows nothing about scanning or pipelines beyond their ids. The engine's
// artifacts arrive as an Intake (see pipeline.ToIntake); everything this package
// guarantees is a row in `batches` / `tasks` / `task_runs` / `task_files` /
// `step_runs` / `file_rows`.
package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/2017fighting/mdc-lite/internal/store"
)

// timeLayout is the fixed-width RFC 3339 shape every timestamp column holds.
//
// The width is fixed on purpose: SQLite compares TEXT lexicographically, and
// RFC3339Nano's variable-length fraction sorts "…:01.5Z" before "…:01.25Z"
// because 'Z' > '2'. A fixed nine-digit fraction keeps `ORDER BY created_at` —
// which is the whole task queue — chronological.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// stamp renders t for a timestamp column.
func stamp(t time.Time) string { return t.UTC().Format(timeLayout) }

// parseStamp reads a timestamp column back. An unparseable stamp is an error
// rather than a zero time, so a hand-edited row cannot silently become 1970.
func parseStamp(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("tasks: timestamp %q: %w", s, err)
	}
	return t, nil
}

// nullStamp reads a nullable timestamp column.
func nullStamp(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid || ns.String == "" {
		return nil, nil
	}
	t, err := parseStamp(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Trigger is how a batch came to exist.
type Trigger string

// The three trigger values. `webhook` is reserved: V1 opens no endpoint for it,
// but a row written tomorrow must validate against today's column.
const (
	TriggerManual  Trigger = "manual"
	TriggerMonitor Trigger = "monitor"
	TriggerWebhook Trigger = "webhook"
)

// TaskStatus is a task's current state (the 7 values of `tasks.status`).
//
// `queued` belongs to tasks only: a run is started by the scheduler taking a task
// out of the queue, so a run is never queued.
type TaskStatus string

// The seven task statuses.
const (
	StatusQueued      TaskStatus = "queued"
	StatusRunning     TaskStatus = "running"
	StatusSucceeded   TaskStatus = "succeeded"
	StatusPartial     TaskStatus = "partial"
	StatusFailed      TaskStatus = "failed"
	StatusNeedsNumber TaskStatus = "needs_number"
	StatusCancelled   TaskStatus = "cancelled"
)

// Terminal reports whether the status releases the task's paths: everything but
// queued, running and needs_number is an end state. A needs_number task is still
// holding its paths, which is why "terminal" is not "not queued or running".
func (s TaskStatus) Terminal() bool {
	switch s {
	case StatusSucceeded, StatusPartial, StatusFailed, StatusCancelled:
		return true
	case StatusQueued, StatusRunning, StatusNeedsNumber:
		return false
	default:
		return false
	}
}

// RunStatus is one execution's outcome (the 7 values of `task_runs.status`).
//
// `interrupted` belongs to runs only: it says "the process died while this run was
// going", which is a fact about a run, never about the task — the task goes back
// to queued and tries again.
type RunStatus string

// The seven run statuses: the task statuses, plus interrupted instead of queued.
const (
	RunRunning     RunStatus = "running"
	RunSucceeded   RunStatus = "succeeded"
	RunPartial     RunStatus = "partial"
	RunFailed      RunStatus = "failed"
	RunNeedsNumber RunStatus = "needs_number"
	RunInterrupted RunStatus = "interrupted"
	RunCancelled   RunStatus = "cancelled"
)

// BatchStatus is the batch-level projection (the 5 values of `batches.status`).
type BatchStatus string

// The five batch statuses. A batch is never queued: it exists because a trigger
// already started it.
const (
	BatchRunning   BatchStatus = "running"
	BatchSucceeded BatchStatus = "succeeded"
	BatchPartial   BatchStatus = "partial"
	BatchFailed    BatchStatus = "failed"
	BatchCancelled BatchStatus = "cancelled"
)

// FileKind is what an input file is in the pipeline's terms.
type FileKind string

// The two input file kinds.
const (
	KindVideo     FileKind = "video"
	KindCompanion FileKind = "companion"
)

// FileRole is a file's part in its group.
type FileRole string

// The three input roles. Exactly one file per group is the primary: it decides the
// task's number.
const (
	RolePrimary   FileRole = "primary"
	RolePart      FileRole = "part"
	RoleCompanion FileRole = "companion"
)

// ReasonPathOccupied is why a path did not become part of a task: another task
// still holds it (dedup gate one). It is the reason code the batch's `skipped`
// counter and the API's `skipped[].path` carry.
const ReasonPathOccupied = "path_occupied"

// The three machine codes a needs_number task stores in `tasks.last_error`. They
// are the same domain as the engine's Unresolved reasons; the panel maps them to
// Chinese locally.
const (
	ReasonNoCandidate   = "no_candidate"
	ReasonAmbiguous     = "ambiguous"
	ReasonDuplicateStem = "duplicate_stem"
)

// isNeedsNumberReason reports whether code is one of the three待补号 reasons.
func isNeedsNumberReason(code string) bool {
	switch code {
	case ReasonNoCandidate, ReasonAmbiguous, ReasonDuplicateStem:
		return true
	default:
		return false
	}
}

// Config is the process-level settings this package needs.
type Config struct {
	// RetryLimit is how many runs a task may have before a restart fails it
	// instead of requeueing it (`retry_limit`, whose documented default is 5 — this
	// package applies no default of its own, so the caller passes the configured
	// value). A task that already used that many attempts is not interrupted again:
	// it is failed, so a crash loop cannot spin forever. Zero means no retry at
	// all: the first interrupted run fails the task.
	RetryLimit int
}

// Store is the task-level state machine over one database.
type Store struct {
	db         *store.Store
	retryLimit int
}

// New builds the state machine over an open store.
func New(s *store.Store, cfg Config) *Store {
	return &Store{db: s, retryLimit: cfg.RetryLimit}
}

// Errors callers distinguish. Everything else is a real failure and is wrapped.
var (
	// ErrNoQueuedTask means the global FIFO has nothing left to hand out.
	ErrNoQueuedTask = errors.New("tasks: no queued task")
	// ErrTaskNotFound means the task id does not exist.
	ErrTaskNotFound = errors.New("tasks: no such task")
	// ErrRunNotFound means the run id does not exist.
	ErrRunNotFound = errors.New("tasks: no such run")
	// ErrRunFinished means the run already ended: a run has one ending, because a
	// second one would append its ledger twice.
	ErrRunFinished = errors.New("tasks: the run is already finished")
	// ErrNotQueued means the task cannot start a run now: it is running, or it is
	// waiting for a human to type a number.
	ErrNotQueued = errors.New("tasks: the task is not waiting in the queue")
	// ErrTaskRunning means the caller tried to cancel or rerun a task a slot is
	// already running. A task is the cancellation unit, so only its own context
	// can stop it.
	ErrTaskRunning = errors.New("tasks: the task is running")
	// ErrTaskTerminal means the task already ended, so there is nothing to cancel.
	ErrTaskTerminal = errors.New("tasks: the task already ended")
	// ErrNotFinished means a rerun was asked of a task that has not ended yet.
	// Rerun is the task-level suffix of a finished task (ADR-0010); a queued task
	// is already waiting for its first run.
	ErrNotFinished = errors.New("tasks: the task has not finished yet")
	// ErrNotWaitingForNumber means numbers were submitted for a task that is not
	// in needs_number.
	ErrNotWaitingForNumber = errors.New("tasks: the task is not waiting for a number")
	// ErrPathOccupied means a path is held by another active task, so the task
	// cannot take it back (a rerun after the path was claimed again).
	ErrPathOccupied = errors.New("tasks: the path is held by another active task")
	// ErrUnknownPath means a submitted number names a path that is not one of the
	// task's files.
	ErrUnknownPath = errors.New("tasks: the path is not one of the task's files")
	// ErrStepOwner means a step run was given neither or both owners.
	ErrStepOwner = errors.New("tasks: a step run belongs to exactly one batch or one task run")
	// ErrBadOutcome means a finish carried a status a finished run cannot have, or
	// a needs_number task was closed without one of the three reason codes.
	ErrBadOutcome = errors.New("tasks: the run outcome is not valid")
)

// IncompleteNumbersError says which files still have no number. The API maps it
// to 422 numbers_incomplete with detail.missing_paths[].
type IncompleteNumbersError struct {
	// Missing is the sorted set of paths still empty.
	Missing []string
}

func (e *IncompleteNumbersError) Error() string {
	return fmt.Sprintf("tasks: %d file(s) still have no number: %v", len(e.Missing), e.Missing)
}

// Batch is one trigger's row, counters included.
type Batch struct {
	ID          int64
	Trigger     Trigger
	TriggerRef  string
	PipelineID  string
	Status      BatchStatus
	Total       int
	Tasks       int
	Succeeded   int
	Partial     int
	Failed      int
	NeedsNumber int
	Skipped     int
	Error       string
	StartedAt   time.Time
	FinishedAt  *time.Time
}

// Task is one group's row.
type Task struct {
	ID           int64
	Status       TaskStatus
	Number       string
	BatchID      int64
	PipelineID   string
	MetadataJSON string
	LastError    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TaskFile is one input file row (`task_files`): what this task processes, and
// whether it still holds the path.
type TaskFile struct {
	ID             int64
	TaskID         int64
	Path           string
	Kind           FileKind
	Role           FileRole
	PartIndex      int
	FollowsPart    int
	Size           int64
	ModTime        string
	Active         bool
	AssignedNumber string
	IsPart         bool
}

// Run is one execution of a task (`task_runs`).
type Run struct {
	ID         int64
	TaskID     int64
	No         int
	Status     RunStatus
	StartedAt  time.Time
	FinishedAt *time.Time
	Error      string
}

// ErrCancelledByUser is the cause a running task's context is cancelled with when
// a human cancels it. Any other ending of that context (a deadline, the process
// shutting down) is an interruption: the run is a victim, not a decision.
//
// It is a context cause rather than a field on the run because cancellation is one
// context path all the way down to the HTTP request; the only place that has to
// tell the two endings apart is the code that writes the run's final status.
var ErrCancelledByUser = errors.New("tasks: cancelled by the user")

// Owner is who a step run belongs to: a batch (the prefix steps) or one task run
// (the suffix steps). Exactly one id is set.
type Owner struct {
	BatchID   int64
	TaskRunID int64
}

// validate enforces "exactly one of the two": a step run with no owner is an
// orphan, and one with both would have to be read twice over.
func (o Owner) validate() error {
	if (o.BatchID == 0) == (o.TaskRunID == 0) {
		return ErrStepOwner
	}
	return nil
}

// Step is one step run (`step_runs`): the摘要 projection of one step's execution,
// never its artifacts.
type Step struct {
	ID          int64
	BatchID     int64
	TaskRunID   int64
	Idx         int
	Type        string
	Name        string
	Status      string
	StartedAt   time.Time
	MS          int64
	SummaryJSON string
	Error       string
}

// FileRow is one line of the output ledger (`file_rows`): what a run wrote, where,
// and whether anything changed.
type FileRow struct {
	ID        int64
	TaskID    int64
	TaskRunID int64
	StepRunID int64
	Kind      string
	Role      string
	Idx       int
	Src       string
	Dst       string
	Bytes     int64
	SHA256    string
	Action    string
	CreatedAt time.Time
}

// Batch reads one batch row.
func (s *Store) Batch(ctx context.Context, id int64) (Batch, error) {
	db, err := s.db.Read()
	if err != nil {
		return Batch{}, err
	}
	var (
		b        Batch
		finished sql.NullString
		started  string
	)
	err = db.QueryRowContext(ctx,
		`SELECT id, trigger, trigger_ref, pipeline_id, status, total, tasks, succeeded,
		        partial, failed, needs_number, skipped, error, started_at, finished_at
		   FROM batches WHERE id = ?`, id,
	).Scan(&b.ID, &b.Trigger, &b.TriggerRef, &b.PipelineID, &b.Status, &b.Total, &b.Tasks,
		&b.Succeeded, &b.Partial, &b.Failed, &b.NeedsNumber, &b.Skipped, &b.Error, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Batch{}, fmt.Errorf("tasks: no such batch %d", id)
	}
	if err != nil {
		return Batch{}, fmt.Errorf("tasks: read batch %d: %w", id, err)
	}
	if b.StartedAt, err = parseStamp(started); err != nil {
		return Batch{}, err
	}
	if b.FinishedAt, err = nullStamp(finished); err != nil {
		return Batch{}, err
	}
	return b, nil
}

// Task reads one task row.
func (s *Store) Task(ctx context.Context, id int64) (Task, error) {
	db, err := s.db.Read()
	if err != nil {
		return Task{}, err
	}
	t, err := readTask(ctx, db, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// queryer is the part of *sql.DB and *sql.Tx a read needs, so a read can run
// inside a transaction as well as on the read pool.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readTask(ctx context.Context, q queryer, id int64) (Task, error) {
	var (
		t                  Task
		createdAt, updated string
	)
	err := q.QueryRowContext(ctx,
		`SELECT id, status, number, batch_id, pipeline_id, metadata_json, last_error, created_at, updated_at
		   FROM tasks WHERE id = ?`, id,
	).Scan(&t.ID, &t.Status, &t.Number, &t.BatchID, &t.PipelineID, &t.MetadataJSON,
		&t.LastError, &createdAt, &updated)
	if err != nil {
		return Task{}, err
	}
	if t.CreatedAt, err = parseStamp(createdAt); err != nil {
		return Task{}, err
	}
	if t.UpdatedAt, err = parseStamp(updated); err != nil {
		return Task{}, err
	}
	return t, nil
}

// TaskFiles reads a task's input files in the order the pipeline reads them:
// parts by index, companions last.
func (s *Store) TaskFiles(ctx context.Context, taskID int64) ([]TaskFile, error) {
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	return readTaskFiles(ctx, db, taskID)
}

func readTaskFiles(ctx context.Context, q queryer, taskID int64) ([]TaskFile, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, task_id, path, kind, role, part_index, follows_part, size, mtime,
		        active, assigned_number, is_part
		   FROM task_files WHERE task_id = ?
		  ORDER BY CASE role WHEN 'primary' THEN 0 WHEN 'part' THEN 1 ELSE 2 END, part_index, path`,
		taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: read files of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []TaskFile
	for rows.Next() {
		var f TaskFile
		if err := rows.Scan(&f.ID, &f.TaskID, &f.Path, &f.Kind, &f.Role, &f.PartIndex, &f.FollowsPart,
			&f.Size, &f.ModTime, &f.Active, &f.AssignedNumber, &f.IsPart); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Runs reads a task's runs oldest first. History is kept, so a rerun's previous
// attempts are still here.
func (s *Store) Runs(ctx context.Context, taskID int64) ([]Run, error) {
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, task_id, run_no, status, started_at, finished_at, error
		   FROM task_runs WHERE task_id = ? ORDER BY run_no`, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: read runs of task %d: %w", taskID, err)
	}
	var out []Run
	err = scanRuns(rows, func(r Run) { out = append(out, r) })
	return out, err
}

// Steps reads the step runs of one owner, in pipeline order.
func (s *Store) Steps(ctx context.Context, o Owner) ([]Step, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	column, id := "batch_id", o.BatchID
	if o.TaskRunID != 0 {
		column, id = "task_run_id", o.TaskRunID
	}
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, idx, type, name, status, started_at, ms, summary_json, error, batch_id, task_run_id
		   FROM step_runs WHERE `+column+` = ? ORDER BY idx, id`, id)
	if err != nil {
		return nil, fmt.Errorf("tasks: read step runs: %w", err)
	}
	var out []Step
	err = scanSteps(rows, func(st Step) { out = append(out, st) })
	return out, err
}

// FileRows reads the output ledger of one run.
func (s *Store) FileRows(ctx context.Context, taskRunID int64) ([]FileRow, error) {
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, task_id, task_run_id, step_run_id, kind, role, idx, src, dst, bytes, sha256, action, created_at
		   FROM file_rows WHERE task_run_id = ? ORDER BY id`, taskRunID)
	if err != nil {
		return nil, fmt.Errorf("tasks: read %d's ledger: %w", taskRunID, err)
	}
	var out []FileRow
	err = scanFileRows(rows, func(r FileRow) { out = append(out, r) })
	return out, err
}
