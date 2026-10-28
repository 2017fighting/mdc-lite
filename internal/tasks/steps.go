package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// RecordStep writes one step's trace: a row in `step_runs` that says which step
// ran, for how long, how it ended, and — in summary_json — the counts and the first
// few items worth showing.
//
// Each call is its own transaction (a single INSERT). That is the second of the
// three transaction boundaries: a step that finished before a crash keeps its row,
// so the detail page can still say how far the run got.
//
// summary_json is a projection, never an artifact: counts and the first N items of
// something big. The media itself lives on disk and the field values live in
// tasks.metadata_json, so nothing here grows with the library.
func (s *Store) RecordStep(ctx context.Context, st Step) (int64, error) {
	if err := (Owner{BatchID: st.BatchID, TaskRunID: st.TaskRunID}).validate(); err != nil {
		return 0, err
	}
	if st.Type == "" {
		return 0, errors.New("tasks: a step run needs a step type")
	}
	if st.Status == "" {
		return 0, fmt.Errorf("tasks: the %s step run needs a status", st.Type)
	}
	summary := st.SummaryJSON
	if summary == "" {
		summary = "{}"
	}
	if !json.Valid([]byte(summary)) {
		return 0, fmt.Errorf("tasks: the %s step run's summary is not JSON: %s", st.Type, summary)
	}
	started := st.StartedAt
	if started.IsZero() {
		started = time.Now()
	}

	res, err := s.db.Write().ExecContext(ctx,
		`INSERT INTO step_runs (idx, type, name, status, started_at, ms, summary_json, error, batch_id, task_run_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, 0), NULLIF(?, 0))`,
		st.Idx, st.Type, st.Name, st.Status, stamp(started), st.MS, summary, st.Error, st.BatchID, st.TaskRunID)
	if err != nil {
		return 0, fmt.Errorf("tasks: record the %s step: %w", st.Type, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("tasks: step run id: %w", err)
	}
	return id, nil
}
