package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the run lifecycle: a task leaves the queue by starting a run, and
// the run's last moment decides the task's status. The rule is one line of the
// spec — `tasks.status ← run.status` — with one exception: a run that ends
// `interrupted` says the process died, so the task goes back to `queued` instead
// of taking that status (a task is never interrupted; it was merely unlucky).

// Outcome is what a finished run says about itself.
type Outcome struct {
	// Status is a run status: succeeded, partial, failed, needs_number,
	// interrupted or cancelled. A run cannot finish as `running`, and `queued`
	// belongs to tasks.
	Status RunStatus
	// Reason is the machine code a needs_number task carries in last_error:
	// no_candidate, ambiguous or duplicate_stem.
	Reason string
	// Error is the message written to task_runs.error; for a failed task it is
	// also what tasks.last_error shows the list page.
	Error string
	// MetadataJSON replaces `tasks.metadata_json` when it is set. It is written in
	// the same transaction as the status, so the field values the run ended with
	// and the result it reports can never disagree.
	MetadataJSON string
	// Rows is the output ledger this run produced. They are written in the same
	// transaction as the status, so a run's ledger and its result can never
	// disagree.
	Rows []FileRow
	// BeforeCommit runs inside that transaction, after the status and the rows are
	// written: it is where a run's old products are cleaned up. An error rolls the
	// whole finish back, so a failed cleanup cannot leave a task looking finished.
	//
	// A pool read inside the hook sees only committed data, so the diff a cleanup
	// wants is "Rows above" against "what the earlier run committed" — the rows this
	// run just wrote are the ones passed in. It must not start a transaction of its
	// own: the write connection is already in one.
	BeforeCommit func(context.Context) error
}

func (o Outcome) validate() error {
	switch o.Status {
	case RunSucceeded, RunPartial, RunFailed, RunNeedsNumber, RunInterrupted, RunCancelled:
	case RunRunning:
		return fmt.Errorf("%w: a finished run cannot still be running", ErrBadOutcome)
	default:
		return fmt.Errorf("%w: %q is not a run status", ErrBadOutcome, o.Status)
	}
	if o.Status == RunNeedsNumber && !isNeedsNumberReason(o.Reason) {
		return fmt.Errorf("%w: a needs_number run needs a reason (no_candidate, ambiguous, duplicate_stem), got %q",
			ErrBadOutcome, o.Reason)
	}
	return nil
}

// BeginRun opens a new run of a task: the next run_no, the task moves to running,
// and the task's paths are held again.
//
// A queued task is the usual case. A task that already ended may also start a run
// — that is what a rerun is — and taking its paths back is how the dedup gate keeps
// meaning "at most one live task per path" across the rerun. If another task has
// claimed one of those paths in the meantime, the rerun is refused instead of
// silently processing a file twice.
func (s *Store) BeginRun(ctx context.Context, taskID int64) (Run, error) {
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return Run{}, fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := readTask(ctx, tx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, ErrTaskNotFound
	}
	if err != nil {
		return Run{}, fmt.Errorf("tasks: read task %d: %w", taskID, err)
	}
	run, err := beginRun(ctx, tx, task, now)
	if err != nil {
		return Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Run{}, fmt.Errorf("tasks: commit run of task %d: %w", taskID, err)
	}
	return run, nil
}

// ClaimNext takes the oldest queued task out of the global FIFO and opens its run
// in one transaction. It is the scheduler's claim: two workers cannot both get the
// same task, and a task that is read is already `running` when this returns.
func (s *Store) ClaimNext(ctx context.Context) (Task, Run, error) {
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return Task{}, Run{}, fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE status = 'queued' ORDER BY created_at, id LIMIT 1`,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, Run{}, ErrNoQueuedTask
	}
	if err != nil {
		return Task{}, Run{}, fmt.Errorf("tasks: read the queue: %w", err)
	}
	task, err := readTask(ctx, tx, id)
	if err != nil {
		return Task{}, Run{}, fmt.Errorf("tasks: read task %d: %w", id, err)
	}
	run, err := beginRun(ctx, tx, task, now)
	if err != nil {
		return Task{}, Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, Run{}, fmt.Errorf("tasks: claim task %d: %w", id, err)
	}
	return task, run, nil
}

// beginRun is the write half of both BeginRun and ClaimNext: the status check, the
// path claim, the next run_no and the task's move to running, inside the caller's
// transaction.
func beginRun(ctx context.Context, tx *sql.Tx, task Task, now string) (Run, error) {
	if err := taskCanStart(task); err != nil {
		return Run{}, err
	}

	conflicts, err := activeConflicts(ctx, tx, task.ID)
	if err != nil {
		return Run{}, err
	}
	if len(conflicts) > 0 {
		return Run{}, fmt.Errorf("%w: %s", ErrPathOccupied, strings.Join(conflicts, ", "))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_files SET active = 1 WHERE task_id = ?`, task.ID); err != nil {
		return Run{}, fmt.Errorf("tasks: claim the paths of task %d: %w", task.ID, err)
	}

	var last int
	if err := tx.QueryRowContext(ctx,
		`SELECT coalesce(max(run_no), 0) FROM task_runs WHERE task_id = ?`, task.ID,
	).Scan(&last); err != nil {
		return Run{}, fmt.Errorf("tasks: last run of task %d: %w", task.ID, err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO task_runs (task_id, run_no, status, started_at) VALUES (?, ?, 'running', ?)`,
		task.ID, last+1, now)
	if err != nil {
		return Run{}, fmt.Errorf("tasks: insert run of task %d: %w", task.ID, err)
	}
	runID, err := res.LastInsertId()
	if err != nil {
		return Run{}, fmt.Errorf("tasks: run id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = 'running', last_error = '', updated_at = ? WHERE id = ?`, now, task.ID,
	); err != nil {
		return Run{}, fmt.Errorf("tasks: mark task %d running: %w", task.ID, err)
	}
	if err := refreshBatch(ctx, tx, task.BatchID, now); err != nil {
		return Run{}, err
	}
	started, err := parseStamp(now)
	if err != nil {
		return Run{}, err
	}
	return Run{ID: runID, TaskID: task.ID, No: last + 1, Status: RunRunning, StartedAt: started}, nil
}

// taskCanStart refuses the two states a run cannot start from: it is already
// running, or it is waiting for a human. Everything else may start, including a
// terminal task (that is a rerun).
func taskCanStart(task Task) error {
	switch task.Status {
	case StatusRunning, StatusNeedsNumber:
		return fmt.Errorf("%w: task %d is %s", ErrNotQueued, task.ID, task.Status)
	default:
		return nil
	}
}

// activeConflicts lists the task's paths that another active task holds. Gate one
// is the unique index; this is only what makes the refusal readable.
func activeConflicts(ctx context.Context, tx *sql.Tx, taskID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT DISTINCT f.path
		   FROM task_files f
		   JOIN task_files o ON o.path = f.path AND o.active = 1 AND o.task_id <> f.task_id
		  WHERE f.task_id = ?
		  ORDER BY f.path`, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: check the paths of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		out = append(out, path)
	}
	return out, rows.Err()
}

// FinishRun closes a run and moves the task with it, in one transaction: the run
// row, the task's status, the paths it releases (or keeps), the ledger it wrote and
// the batch's counters are one fact, so they are one commit.
//
// The `active` obligation lives here because this is the only place a task
// changes state: a terminal task releases every path it holds, and a task that is
// queued, running or waiting for a number keeps them.
func (s *Store) FinishRun(ctx context.Context, runID int64, out Outcome) error {
	if err := out.validate(); err != nil {
		return err
	}
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		taskID, batchID int64
		runStatus       RunStatus
	)
	err = tx.QueryRowContext(ctx,
		`SELECT t.id, t.batch_id, r.status FROM task_runs r JOIN tasks t ON t.id = r.task_id WHERE r.id = ?`, runID,
	).Scan(&taskID, &batchID, &runStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRunNotFound
	}
	if err != nil {
		return fmt.Errorf("tasks: read run %d: %w", runID, err)
	}
	if runStatus != RunRunning {
		// Finishing twice would append the ledger twice and could pull a task back
		// out of a terminal state, so a run has exactly one ending.
		return fmt.Errorf("%w: run %d is %s", ErrRunFinished, runID, runStatus)
	}

	// The run's own error column carries the message, not the needs_number code:
	// codes belong in tasks.last_error, where the panel reads them.
	if _, err := tx.ExecContext(ctx,
		`UPDATE task_runs SET status = ?, finished_at = ?, error = ? WHERE id = ?`,
		string(out.Status), now, out.Error, runID,
	); err != nil {
		return fmt.Errorf("tasks: finish run %d: %w", runID, err)
	}

	status := TaskStatus(out.Status)
	if out.Status == RunInterrupted {
		status = StatusQueued
	}
	lastError := ""
	switch out.Status {
	case RunFailed:
		lastError = out.Error
	case RunNeedsNumber:
		lastError = out.Reason
	}
	// A run that has nothing new to say about the fields keeps the ones the task
	// already had: a failed rerun must not erase what the last good run collected.
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = ?, last_error = ?, metadata_json = coalesce(nullif(?, ''), metadata_json), updated_at = ? WHERE id = ?`,
		string(status), lastError, out.MetadataJSON, now, taskID,
	); err != nil {
		return fmt.Errorf("tasks: move task %d to %s: %w", taskID, status, err)
	}
	if status.Terminal() {
		if err := releasePaths(ctx, tx, taskID); err != nil {
			return err
		}
	}

	for _, row := range out.Rows {
		if err := insertFileRow(ctx, tx, taskID, runID, row, now); err != nil {
			return err
		}
	}
	if out.BeforeCommit != nil {
		if err := out.BeforeCommit(ctx); err != nil {
			return err
		}
	}
	if err := refreshBatch(ctx, tx, batchID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tasks: commit run %d: %w", runID, err)
	}
	return nil
}

// releasePaths clears the task's hold on its paths: the one write that makes the
// 活跃占用 invariant true when a task reaches a terminal status.
func releasePaths(ctx context.Context, tx *sql.Tx, taskID int64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE task_files SET active = 0 WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("tasks: release the paths of task %d: %w", taskID, err)
	}
	return nil
}

// insertFileRow appends one line to the output ledger. The run's ids are the
// function's, not the caller's: a ledger row can only ever belong to the run that
// is being finished.
func insertFileRow(ctx context.Context, tx *sql.Tx, taskID, runID int64, row FileRow, now string) error {
	if row.StepRunID == 0 {
		return fmt.Errorf("tasks: ledger row %s has no step run", row.Dst)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO file_rows (task_id, task_run_id, step_run_id, kind, role, idx, src, dst, bytes, sha256, action, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, runID, row.StepRunID, row.Kind, row.Role, row.Idx, row.Src, row.Dst, row.Bytes, row.SHA256, row.Action, now,
	); err != nil {
		return fmt.Errorf("tasks: write the ledger row for %s: %w", row.Dst, err)
	}
	return nil
}
