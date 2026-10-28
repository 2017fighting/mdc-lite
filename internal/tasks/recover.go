package tasks

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// This file is the startup reset. A process that was killed leaves runs marked
// running and tasks that will never be picked up again, because the scheduler that
// held them is gone. Nothing resumes a half-done run — there is no cursor to resume
// from (the rerun is the task-level suffix, ADR-0010) — so the reset closes the dead
// runs, puts their tasks back in the queue, and lets the queue do the rest. The
// idempotent output step is what makes a second full run safe.

// LastErrorRetryLimit is what a task that crashed too many times says. It is a
// message rather than a machine code because there is nothing for a human to fill
// in: the task is failed, not waiting. The spec fixes this exact wording (§11,
// 崩溃恢复), so it is kept verbatim.
const LastErrorRetryLimit = "进程重启时中断（已达重跑上限）"

// Recovery is what the startup reset did.
type Recovery struct {
	// Interrupted is the number of runs that were still running.
	Interrupted int
	// Requeued is the number of tasks left in the queue for a fresh run.
	Requeued int
	// Failed is the number of tasks that had already used up `retry_limit` runs.
	Failed int
}

// Recover is the reset a restart performs, in one transaction: every run left
// running becomes interrupted, every task that is not finished goes back to the
// queue, and a task that has already used `retry_limit` runs is failed instead —
// a crash loop must not spin forever.
//
// A task waiting for a number is not touched: it is waiting for a human, not for a
// scheduler, and its paths stay held. Running Recover twice changes nothing the
// second time.
func (s *Store) Recover(ctx context.Context) (Recovery, error) {
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return Recovery{}, fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var rec Recovery

	res, err := tx.ExecContext(ctx,
		`UPDATE task_runs SET status = 'interrupted', finished_at = ? WHERE status = 'running'`, now)
	if err != nil {
		return Recovery{}, fmt.Errorf("tasks: interrupt the dead runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return Recovery{}, fmt.Errorf("tasks: interrupt the dead runs: %w", err)
	}
	rec.Interrupted = int(n)

	// The attempt count comes from the run history: interrupted runs count too,
	// which is the point — a task that keeps dying on restart is not making
	// progress.
	type attempt struct {
		id      int64
		batchID int64
	}
	var (
		requeue   []attempt
		exhausted []attempt
		batches   []int64
	)
	rows, err := tx.QueryContext(ctx,
		`SELECT t.id, t.batch_id, count(r.id)
		   FROM tasks t LEFT JOIN task_runs r ON r.task_id = t.id
		  WHERE t.status IN ('queued', 'running')
		  GROUP BY t.id, t.batch_id
		  ORDER BY t.id`)
	if err != nil {
		return Recovery{}, fmt.Errorf("tasks: read the unfinished tasks: %w", err)
	}
	for rows.Next() {
		var a attempt
		var attempts int
		if err := rows.Scan(&a.id, &a.batchID, &attempts); err != nil {
			_ = rows.Close()
			return Recovery{}, err
		}
		// A task that never started has used no attempt, so a `retry_limit: 0`
		// still lets it have its first run; only work that was actually interrupted
		// counts against the limit.
		if attempts > 0 && attempts >= s.retryLimit {
			exhausted = append(exhausted, a)
		} else {
			requeue = append(requeue, a)
		}
		if !slices.Contains(batches, a.batchID) {
			batches = append(batches, a.batchID)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Recovery{}, err
	}
	_ = rows.Close()

	for _, a := range requeue {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = 'queued', updated_at = ? WHERE id = ?`, now, a.id,
		); err != nil {
			return Recovery{}, fmt.Errorf("tasks: requeue task %d: %w", a.id, err)
		}
	}
	rec.Requeued = len(requeue)

	for _, a := range exhausted {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = 'failed', last_error = ?, updated_at = ? WHERE id = ?`,
			LastErrorRetryLimit, now, a.id,
		); err != nil {
			return Recovery{}, fmt.Errorf("tasks: fail task %d: %w", a.id, err)
		}
		if err := releasePaths(ctx, tx, a.id); err != nil {
			return Recovery{}, err
		}
	}
	rec.Failed = len(exhausted)

	for _, batchID := range batches {
		if err := refreshBatch(ctx, tx, batchID, now); err != nil {
			return Recovery{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Recovery{}, fmt.Errorf("tasks: commit the recovery: %w", err)
	}
	return rec, nil
}
