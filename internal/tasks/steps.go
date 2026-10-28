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
// SourceRun is one source's row in `source_runs`: how the source answered for one
// number, and which fields it actually contributed. `Reason` is one of the six
// machine codes; `Fields` is the JSON projection of the contributed field names.
type SourceRun struct {
	ID         int64
	StepRunID  int64
	SourceID   string
	Status     string
	Err        string
	Reason     string
	FieldsJSON string
	MS         int64
}

// RecordSource writes one source's trace inside the scrape step's step run. Like
// RecordStep it is a single INSERT, so a crash after it still leaves the trace of
// the sources that had already answered.
func (s *Store) RecordSource(ctx context.Context, src SourceRun) error {
	if src.StepRunID == 0 {
		return errors.New("tasks: a source run needs its step run")
	}
	if src.SourceID == "" {
		return errors.New("tasks: a source run needs a source id")
	}
	switch src.Status {
	case "ok", "notfound", "failed":
	default:
		return fmt.Errorf("tasks: source run status %q is not one of ok, notfound, failed", src.Status)
	}
	switch src.Reason {
	case "", "egress_unavailable", "timeout", "blocked", "cooldown", "http_5xx", "parse":
	default:
		return fmt.Errorf("tasks: source run reason %q is not one of the six reason codes", src.Reason)
	}
	fields := src.FieldsJSON
	if fields == "" {
		fields = "{}"
	}
	if !json.Valid([]byte(fields)) {
		return fmt.Errorf("tasks: the %s source run's fields are not JSON: %s", src.SourceID, fields)
	}
	if _, err := s.db.Write().ExecContext(ctx,
		`INSERT INTO source_runs (step_run_id, source_id, status, err, reason, fields_json, ms)
		 VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?)`,
		src.StepRunID, src.SourceID, src.Status, src.Err, src.Reason, fields, src.MS,
	); err != nil {
		return fmt.Errorf("tasks: record source %s: %w", src.SourceID, err)
	}
	return nil
}

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
