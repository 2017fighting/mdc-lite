package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// This file is the fan-out point: one trigger's groups in, one batch plus one task
// per group out. Everything a task needs to be rerun later — its files, its
// number, why it is waiting for a human — is decided here, because after this
// point the task no longer knows about the scan that produced it.

// Intake is one trigger's input that reached the fan-out point: the batch-level
// prefix has already walked, filtered, grouped and parsed, and this is what is
// left to store.
type Intake struct {
	Trigger    Trigger
	TriggerRef string
	PipelineID string
	Groups     []Group
	// Skipped are the paths the trigger handed over that never became a group: the
	// ones filter rejected and the ones that were gone by the time the walk reached
	// them. They are counted in the batch (they are input, so they are part of
	// `total`, and they became no task, so they are part of `skipped`), which is how
	// a trigger whose input all fell out still leaves its trace.
	Skipped []SkippedPath
}

// Group is one work about to become a task: its input files and either its number
// or the reason a human has to type one.
type Group struct {
	// Files are the group's input files: the primary first, then the other parts,
	// then the companions. A group with no files is a bug in the caller.
	Files []InputFile
	// Number is the normalized 番号 parse decided. Empty when NeedsNumber says why
	// it could not.
	Number string
	// NeedsNumber is the machine code the task carries in last_error while it
	// waits for a human: no_candidate, ambiguous or duplicate_stem.
	NeedsNumber string
}

// InputFile is one file a task is made of.
type InputFile struct {
	// Path is the absolute source path. It is also the dedup gate's key: at most
	// one non-terminal task may hold it.
	Path        string
	Kind        FileKind
	Role        FileRole
	PartIndex   int
	FollowsPart int
	Size        int64
	ModTime     time.Time
}

// BatchResult is what creating a batch produced.
type BatchResult struct {
	BatchID int64
	// Created is the number of tasks made.
	Created int
	// Skipped names every path that is not part of a task: the ones the intake
	// already knew were filtered or gone, then the ones that lost the dedup gate.
	Skipped []SkippedPath
}

// SkippedPath is one path that became no part of any task, with the reason. The
// reasons are the ones the API's `skipped[]` carries: `filtered` and `not_found`
// come from the intake, `path_occupied` from the dedup gate.
type SkippedPath struct {
	Path   string
	Reason string
}

// The two reasons an intake can already know about a path before the fan-out.
const (
	ReasonFiltered = "filtered"
	ReasonNotFound = "not_found"
)

// CreateBatch stores one trigger as a batch and fans it out into tasks.
//
// It runs in one transaction: the batch row, every task with its files, and the
// counters can never be seen half-written. A group whose path another task still
// holds is dropped whole — the unique index on the active path is what decides
// that, not a lock — and every one of its files is reported in Skipped. An intake
// whose paths all fall out still writes a batch row (tasks=0, skipped=N): the
// trigger happened, and the history says so.
func (s *Store) CreateBatch(ctx context.Context, in Intake) (BatchResult, error) {
	if err := in.validate(); err != nil {
		return BatchResult{}, err
	}
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// `total` is every path the trigger handed over — the ones that became groups
	// and the ones that never did — `tasks` is recomputed from the rows below, and
	// `skipped` counts paths, so the row reads "given N paths, made M tasks,
	// skipped K of them".
	total := len(in.Skipped)
	for _, g := range in.Groups {
		total += len(g.Files)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO batches (trigger, trigger_ref, pipeline_id, status, total, tasks, skipped, started_at)
		 VALUES (?, ?, ?, 'running', ?, 0, 0, ?)`,
		string(in.Trigger), in.TriggerRef, in.PipelineID, total, now)
	if err != nil {
		return BatchResult{}, fmt.Errorf("tasks: insert batch: %w", err)
	}
	batchID, err := res.LastInsertId()
	if err != nil {
		return BatchResult{}, fmt.Errorf("tasks: batch id: %w", err)
	}

	// The intake's own skips come first: they are already known, and the caller's
	// slice is copied rather than appended to.
	out := BatchResult{BatchID: batchID}
	out.Skipped = append(out.Skipped, in.Skipped...)
	for _, g := range in.Groups {
		created, err := insertGroup(ctx, tx, batchID, in.PipelineID, g, now)
		if err != nil {
			return BatchResult{}, err
		}
		if created {
			out.Created++
			continue
		}
		for _, f := range g.Files {
			out.Skipped = append(out.Skipped, SkippedPath{Path: f.Path, Reason: ReasonPathOccupied})
		}
	}
	// `skipped` counts paths, not tasks, so it is the one counter the tasks
	// cannot tell us: it is the number of files the gate refused.
	if _, err := tx.ExecContext(ctx, `UPDATE batches SET skipped = ? WHERE id = ?`, len(out.Skipped), batchID); err != nil {
		return BatchResult{}, fmt.Errorf("tasks: count the skipped paths of batch %d: %w", batchID, err)
	}
	if err := refreshBatch(ctx, tx, batchID, now); err != nil {
		return BatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, fmt.Errorf("tasks: commit batch %d: %w", batchID, err)
	}
	return out, nil
}

// insertGroup writes one task with its files and reports whether it survived the
// dedup gate.
//
// The gate is the `ux_task_files_active_path` index: each file is inserted with
// `ON CONFLICT(path) WHERE active = 1 DO NOTHING`, and a refused insert means
// another live task already owns that path. The group then stands down as a whole
// — half a group is not a work — by rolling back to the savepoint taken before
// the task row, so nothing of it is left behind.
func insertGroup(ctx context.Context, tx *sql.Tx, batchID int64, pipelineID string, g Group, now string) (bool, error) {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT grp"); err != nil {
		return false, fmt.Errorf("tasks: savepoint: %w", err)
	}
	stand := func() error {
		// ROLLBACK TO leaves the savepoint in place, so it is released after.
		if _, err := tx.ExecContext(ctx, "ROLLBACK TO grp"); err != nil {
			return fmt.Errorf("tasks: roll back a refused group: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "RELEASE grp"); err != nil {
			return fmt.Errorf("tasks: release a refused group: %w", err)
		}
		return nil
	}

	status, lastError := StatusQueued, ""
	if g.NeedsNumber != "" {
		status, lastError = StatusNeedsNumber, g.NeedsNumber
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO tasks (status, number, batch_id, pipeline_id, last_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(status), g.Number, batchID, pipelineID, lastError, now, now)
	if err != nil {
		return false, fmt.Errorf("tasks: insert task in batch %d: %w", batchID, err)
	}
	taskID, err := res.LastInsertId()
	if err != nil {
		return false, fmt.Errorf("tasks: task id: %w", err)
	}

	for _, f := range g.Files {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO task_files (task_id, path, kind, role, part_index, follows_part, size, mtime, active)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
			 ON CONFLICT (path) WHERE active = 1 DO NOTHING`,
			taskID, f.Path, string(f.Kind), string(f.Role), f.PartIndex, f.FollowsPart, f.Size, stampOrEmpty(f.ModTime))
		if err != nil {
			return false, fmt.Errorf("tasks: claim %s: %w", f.Path, err)
		}
		claimed, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("tasks: claim %s: %w", f.Path, err)
		}
		if claimed == 0 {
			if err := stand(); err != nil {
				return false, err
			}
			return false, nil
		}
	}
	if _, err := tx.ExecContext(ctx, "RELEASE grp"); err != nil {
		return false, fmt.Errorf("tasks: release: %w", err)
	}
	return true, nil
}

// stampOrEmpty renders an optional mtime: a zero time means the caller did not
// read one, which is not the same as 1970 and is stored as "unknown".
func stampOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return stamp(t)
}

// refreshBatch recomputes a batch's counters and status from its tasks, so the
// batch row is always a projection of the tasks and never an independently
// maintained tally that can drift.
//
// A task waiting for a number closes its batch as partial: the batch has nothing
// left to do, and a human is the reason. Filling the number moves the task back to
// queued and this function puts the batch back to running. A batch with no tasks
// at all (every path lost the dedup gate) is succeeded: nothing ran, nothing
// failed.
func refreshBatch(ctx context.Context, tx *sql.Tx, batchID int64, now string) error {
	var (
		tasks, succeeded, partial, failed, needsNumber, cancelled, pending int
	)
	err := tx.QueryRowContext(ctx,
		`SELECT count(*),
		        coalesce(sum(status = 'succeeded'), 0),
		        coalesce(sum(status = 'partial'), 0),
		        coalesce(sum(status = 'failed'), 0),
		        coalesce(sum(status = 'needs_number'), 0),
		        coalesce(sum(status = 'cancelled'), 0),
		        coalesce(sum(status IN ('queued', 'running')), 0)
		   FROM tasks WHERE batch_id = ?`, batchID,
	).Scan(&tasks, &succeeded, &partial, &failed, &needsNumber, &cancelled, &pending)
	if err != nil {
		return fmt.Errorf("tasks: count tasks of batch %d: %w", batchID, err)
	}

	status := BatchSucceeded
	var finished any = now
	switch {
	case pending > 0:
		status, finished = BatchRunning, nil
	case failed > 0:
		status = BatchFailed
	case tasks > 0 && cancelled == tasks:
		// Every task was cancelled by a human: nothing failed, nothing is pending,
		// and calling it "partial" would hide that the whole batch was called off.
		status = BatchCancelled
	case partial > 0 || needsNumber > 0 || cancelled > 0:
		status = BatchPartial
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE batches
		    SET tasks = ?, succeeded = ?, partial = ?, failed = ?, needs_number = ?, status = ?, finished_at = ?
		  WHERE id = ?`,
		tasks, succeeded, partial, failed, needsNumber, string(status), finished, batchID,
	); err != nil {
		return fmt.Errorf("tasks: update batch %d: %w", batchID, err)
	}
	return nil
}

// validate refuses an intake the store cannot make sense of. These are caller
// bugs, not user errors: a group with no primary, or with both a number and a
// reason for having none, would leave a task the rest of the system cannot read.
func (in Intake) validate() error {
	switch in.Trigger {
	case TriggerManual, TriggerMonitor, TriggerWebhook:
	default:
		return fmt.Errorf("tasks: unknown trigger %q", in.Trigger)
	}
	if in.PipelineID == "" {
		return errors.New("tasks: intake has no pipeline id")
	}
	if len(in.Groups) == 0 && len(in.Skipped) == 0 {
		// No paths at all, no batch: the API answers 400 rather than storing a row
		// that says a trigger did nothing. Paths that were filtered out are still
		// paths, so they do make a batch.
		return errors.New("tasks: intake has no paths")
	}
	for i, sk := range in.Skipped {
		if sk.Path == "" {
			return fmt.Errorf("tasks: skipped path %d has no path", i+1)
		}
		switch sk.Reason {
		case ReasonFiltered, ReasonNotFound:
		default:
			return fmt.Errorf("tasks: skipped path %s: %q is not filtered or not_found", sk.Path, sk.Reason)
		}
	}
	for i, g := range in.Groups {
		if err := g.validate(); err != nil {
			return fmt.Errorf("tasks: group %d: %w", i+1, err)
		}
	}
	return nil
}

func (g Group) validate() error {
	if len(g.Files) == 0 {
		return errors.New("no files")
	}
	if g.Number != "" && g.NeedsNumber != "" {
		return fmt.Errorf("has both number %q and needs_number reason %q", g.Number, g.NeedsNumber)
	}
	if g.NeedsNumber != "" && !isNeedsNumberReason(g.NeedsNumber) {
		return fmt.Errorf("needs_number reason %q is not one of no_candidate, ambiguous, duplicate_stem", g.NeedsNumber)
	}
	primaries, seen := 0, map[string]bool{}
	for _, f := range g.Files {
		if f.Path == "" {
			return errors.New("a file has no path")
		}
		if seen[f.Path] {
			return fmt.Errorf("%s appears twice", f.Path)
		}
		seen[f.Path] = true
		switch f.Kind {
		case KindVideo, KindCompanion:
		default:
			return fmt.Errorf("%s: unknown kind %q", f.Path, f.Kind)
		}
		switch f.Role {
		case RolePrimary:
			primaries++
			if f.Kind != KindVideo {
				return fmt.Errorf("%s: only a video can be the primary", f.Path)
			}
		case RolePart:
			if f.Kind != KindVideo {
				return fmt.Errorf("%s: only a video can be a part", f.Path)
			}
		case RoleCompanion:
			if f.Kind != KindCompanion {
				return fmt.Errorf("%s: a companion role needs the companion kind", f.Path)
			}
		default:
			return fmt.Errorf("%s: unknown role %q", f.Path, f.Role)
		}
	}
	if primaries != 1 {
		return fmt.Errorf("has %d primaries, want exactly 1", primaries)
	}
	return nil
}
