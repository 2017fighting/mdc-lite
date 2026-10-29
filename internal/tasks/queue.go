package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the queue and the query side of the dedup gates.
//
// There is no queue table. The queue is `SELECT … WHERE status = 'queued' ORDER BY
// created_at`, which means the queue cannot disagree with the tasks it is made of:
// a task that ends, is cancelled or starts waiting for a number leaves the queue
// by the same write that changed its status.

// NextQueued returns the oldest queued task, or ErrNoQueuedTask when there is no
// work. It does not claim the task: the scheduler uses ClaimNext, which reads the
// same row and opens its run in one transaction, so a task read here is still a
// candidate for another worker until then.
//
// The order is created_at — the FIFO the spec fixes — with the id as the tiebreak
// so two tasks created in the same instant still come out in a stable order.
func (s *Store) NextQueued(ctx context.Context) (Task, error) {
	db, err := s.db.Read()
	if err != nil {
		return Task{}, err
	}
	var id int64
	err = db.QueryRowContext(ctx,
		`SELECT id FROM tasks WHERE status = 'queued' ORDER BY created_at, id LIMIT 1`,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNoQueuedTask
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: read the queue: %w", err)
	}
	t, err := readTask(ctx, db, id)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: read task %d: %w", id, err)
	}
	return t, nil
}

// CancelTask ends a task a slot is not running: a queued task a human called off,
// or a needs_number task whose number they decided to skip (skipping is
// cancelling). The terminal write releases every path, so a rescan can claim them
// again.
//
// A running task is refused: its run owns the slot and its context, so stopping it
// is the scheduler's job (cancel the context; the runner writes `cancelled`). An
// already terminal task is refused too — there is nothing left to call off.
func (s *Store) CancelTask(ctx context.Context, taskID int64) (Task, error) {
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := readTask(ctx, tx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: read task %d: %w", taskID, err)
	}
	switch {
	case task.Status == StatusRunning:
		return Task{}, ErrTaskRunning
	case task.Status.Terminal():
		return Task{}, ErrTaskTerminal
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = 'cancelled', last_error = '', updated_at = ? WHERE id = ?`, now, taskID,
	); err != nil {
		return Task{}, fmt.Errorf("tasks: cancel task %d: %w", taskID, err)
	}
	if err := releasePaths(ctx, tx, taskID); err != nil {
		return Task{}, err
	}
	if err := refreshBatch(ctx, tx, task.BatchID, now); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("tasks: commit the cancellation of task %d: %w", taskID, err)
	}
	task.Status = StatusCancelled
	if task.UpdatedAt, err = parseStamp(now); err != nil {
		return Task{}, err
	}
	return task, nil
}

// Requeue puts a finished task back in the queue for another run, taking its paths
// back in the same transaction. It is a rerun's hand-off to the scheduler: the task
// is queued exactly when it is safe to run again, and a path another active task has
// claimed in the meantime refuses the rerun instead of processing a file twice.
func (s *Store) Requeue(ctx context.Context, taskID int64) (Task, error) {
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := readTask(ctx, tx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrTaskNotFound
	}
	if err != nil {
		return Task{}, fmt.Errorf("tasks: read task %d: %w", taskID, err)
	}
	if task.Status == StatusRunning {
		return Task{}, ErrTaskRunning
	}
	if !task.Status.Terminal() {
		return Task{}, fmt.Errorf("%w: task %d is %s", ErrNotFinished, taskID, task.Status)
	}

	conflicts, err := activeConflicts(ctx, tx, taskID)
	if err != nil {
		return Task{}, err
	}
	if len(conflicts) > 0 {
		return Task{}, fmt.Errorf("%w: %s", ErrPathOccupied, strings.Join(conflicts, ", "))
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_files SET active = 1 WHERE task_id = ?`, taskID); err != nil {
		return Task{}, fmt.Errorf("tasks: reclaim the paths of task %d: %w", taskID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = 'queued', last_error = '', updated_at = ? WHERE id = ?`, now, taskID,
	); err != nil {
		return Task{}, fmt.Errorf("tasks: requeue task %d: %w", taskID, err)
	}
	if err := refreshBatch(ctx, tx, task.BatchID, now); err != nil {
		return Task{}, err
	}
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("tasks: commit the rerun of task %d: %w", taskID, err)
	}
	task.Status = StatusQueued
	if task.UpdatedAt, err = parseStamp(now); err != nil {
		return Task{}, err
	}
	return task, nil
}

// PathState is what the store knows about one path: which task has it, in what
// state, and whether that task still holds it.
type PathState struct {
	Path   string
	TaskID int64
	Status TaskStatus
	// Active is gate one: true means another task would refuse to claim this path.
	// False with a TaskID means a task processed it before and ended — the "can be
	// rerun" hint gate two gives the preview.
	Active bool
}

// pathChunk is how many paths one IN list carries. SQLite's default variable limit
// is well above this; the chunk is small enough that a preview of a large directory
// never builds a query the driver has to refuse.
const pathChunk = 400

// LookupPaths answers both dedup gates for a set of paths, without claiming
// anything: the preview needs to know which rows are occupied (gate one refuses
// those) and which paths a finished task already handled (gate two only hints that
// the path can be rerun).
//
// A path with an active row and older terminal rows reports the active one: the
// live task is the answer to "who has it now".
func (s *Store) LookupPaths(ctx context.Context, paths []string) (map[string]PathState, error) {
	out := make(map[string]PathState, len(paths))
	if len(paths) == 0 {
		return out, nil
	}
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(paths); start += pathChunk {
		chunk := paths[start:min(start+pathChunk, len(paths))]
		args := make([]any, len(chunk))
		for i, p := range chunk {
			args[i] = p
		}
		query := `SELECT f.path, f.task_id, t.status, f.active
		            FROM task_files f JOIN tasks t ON t.id = f.task_id
		           WHERE f.path IN (?` + strings.Repeat(", ?", len(chunk)-1) + `)
		           ORDER BY f.active DESC, f.task_id DESC`
		rows, err := db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("tasks: look up %d path(s): %w", len(chunk), err)
		}
		for rows.Next() {
			var st PathState
			if err := rows.Scan(&st.Path, &st.TaskID, &st.Status, &st.Active); err != nil {
				_ = rows.Close()
				return nil, err
			}
			if _, seen := out[st.Path]; !seen {
				out[st.Path] = st
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}
