package tasks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// This file is the number a human types. A task that went needs_number is holding
// its paths and waiting; the panel writes one number per file and the task goes
// back into the queue, where the scheduler picks it up for a new run that starts at
// `parse` (ADR-0010: a rerun is the task-level suffix).

// Fill is one file's human-typed number.
type Fill struct {
	Path string
	// Number is the normalized 番号 — the same normalization parse applies to a file
	// name, so a typed `ssis-1` and a file's `SSIS-1` land on the same task. The
	// caller normalizes; this package stores what it is given.
	Number string
	// IsPart is the panel's "same work, a part of it" tick: positive evidence where
	// the file name had none.
	IsPart bool
}

// AssignNumbers stores the numbers a human typed and puts the task back in the
// queue.
//
// Every video must be numbered in one submit — a half-filled task would run with a
// number for one part and nothing for the next; a companion may be left empty,
// because a subtitle follows its video and has no number of its own. The task's
// number is the primary's. The paths stay held (active=1) throughout: the task was
// never terminal, so a rescan must not build a second task over the same files.
func (s *Store) AssignNumbers(ctx context.Context, taskID int64, fills []Fill) error {
	now := stamp(time.Now())
	tx, err := s.db.Write().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tasks: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	task, err := readTask(ctx, tx, taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	if err != nil {
		return fmt.Errorf("tasks: read task %d: %w", taskID, err)
	}
	if task.Status != StatusNeedsNumber {
		return fmt.Errorf("%w: task %d is %s", ErrNotWaitingForNumber, taskID, task.Status)
	}

	files, err := readTaskFiles(ctx, tx, taskID)
	if err != nil {
		return err
	}
	byPath := make(map[string]Fill, len(fills))
	for _, f := range fills {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			return errors.New("tasks: a number was submitted without a path")
		}
		if _, dup := byPath[path]; dup {
			return fmt.Errorf("tasks: %s was submitted twice", path)
		}
		f.Number = strings.TrimSpace(f.Number)
		byPath[path] = f
	}

	known := make(map[string]bool, len(files))
	var missing []string
	for _, f := range files {
		known[f.Path] = true
		if f.Kind != KindVideo {
			// A companion has no number to type; whatever the panel sent for it is
			// stored, but never demanded.
			continue
		}
		if byPath[f.Path].Number == "" {
			missing = append(missing, f.Path)
		}
	}
	var unknown []string
	for path := range byPath {
		if !known[path] {
			unknown = append(unknown, path)
		}
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return fmt.Errorf("%w: %s", ErrUnknownPath, strings.Join(unknown, ", "))
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return &IncompleteNumbersError{Missing: missing}
	}

	primary := ""
	for _, f := range files {
		fill, ok := byPath[f.Path]
		if ok {
			if _, err := tx.ExecContext(ctx,
				`UPDATE task_files SET assigned_number = ?, is_part = ? WHERE id = ?`,
				fill.Number, fill.IsPart, f.ID,
			); err != nil {
				return fmt.Errorf("tasks: store the number of %s: %w", f.Path, err)
			}
		}
		if f.Role == RolePrimary {
			primary = fill.Number
		}
	}
	if primary == "" {
		return fmt.Errorf("tasks: task %d has no numbered primary", taskID)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET number = ?, status = 'queued', last_error = '', updated_at = ? WHERE id = ?`,
		primary, now, taskID,
	); err != nil {
		return fmt.Errorf("tasks: requeue task %d: %w", taskID, err)
	}
	if err := refreshBatch(ctx, tx, task.BatchID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tasks: commit the numbers of task %d: %w", taskID, err)
	}
	return nil
}
