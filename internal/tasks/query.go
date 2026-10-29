package tasks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the read side the REST surface needs and the state machine itself
// does not: the filtered task list, the batch dropdown, and the bulk reads that
// keep a page of tasks from costing one query per row.
//
// None of it writes. The queue, the dedup gates and the lifecycle stay in their
// own files; these are projections of rows that already exist.

// queryChunk is how many ids one IN list carries, for the same reason pathChunk
// exists: a page of 100 tasks is well inside SQLite's variable limit, and the
// chunk keeps a careless caller from building a query the driver refuses.
const queryChunk = 400

// placeholders builds `?, ?, ?` for an IN list.
func placeholders(n int) string {
	if n <= 0 {
		return "?"
	}
	return strings.Repeat("?, ", n-1) + "?"
}

// TaskFilter is the query surface of `GET /api/tasks`: the 7-value status OR, the
// batch, the trigger (through `batches`), a number substring, a created-at window,
// and the page.
//
// The sort is not a parameter. It is `created_at DESC` with the id as the tiebreak,
// fixed here because a client that could reorder the list would also have to know
// how the queue is ordered.
type TaskFilter struct {
	// Statuses is an OR over the 7 task statuses. Empty means every status.
	Statuses []TaskStatus
	// BatchID narrows to one batch. Zero means every batch.
	BatchID int64
	// Trigger narrows to one trigger by joining `batches`; no index on purpose
	// (the table is small and the filter is a menu pick, not a hot path).
	Trigger Trigger
	// Number is matched as a substring, case-insensitively. The stored number is
	// already normalized upper-case, so the caller only has to upper-case input.
	Number string
	// CreatedAfter / CreatedBefore are inclusive bounds on `created_at`. Zero means
	// unbounded.
	CreatedAfter  time.Time
	CreatedBefore time.Time
	// Page is 1-based. PerPage is the caller's whitelisted page size.
	Page, PerPage int
}

// TaskRow is a task with the trigger it came from: the list is a join, because
// "which trigger made this" is a batch fact and the task row does not carry it.
type TaskRow struct {
	Task
	Trigger    Trigger
	TriggerRef string
}

// where renders the filter's WHERE clause and its arguments. It is one function so
// the count and the page cannot disagree about what was selected.
func (f TaskFilter) where() (string, []any) {
	clauses := []string{"1 = 1"}
	var args []any
	if len(f.Statuses) > 0 {
		clauses = append(clauses, "t.status IN ("+placeholders(len(f.Statuses))+")")
		for _, st := range f.Statuses {
			args = append(args, string(st))
		}
	}
	if f.BatchID > 0 {
		clauses = append(clauses, "t.batch_id = ?")
		args = append(args, f.BatchID)
	}
	if f.Trigger != "" {
		clauses = append(clauses, "b.trigger = ?")
		args = append(args, string(f.Trigger))
	}
	if f.Number != "" {
		// instr rather than LIKE: a number is a plain substring, and `%`/`_` typed
		// into the search box must not become wildcards.
		clauses = append(clauses, "instr(upper(t.number), ?) > 0")
		args = append(args, strings.ToUpper(f.Number))
	}
	if !f.CreatedAfter.IsZero() {
		clauses = append(clauses, "t.created_at >= ?")
		args = append(args, stamp(f.CreatedAfter))
	}
	if !f.CreatedBefore.IsZero() {
		clauses = append(clauses, "t.created_at <= ?")
		args = append(args, stamp(f.CreatedBefore))
	}
	return strings.Join(clauses, " AND "), args
}

// ListTasks reads one page of tasks plus the total the filter matches. A page past
// the end is not an error: it returns no items and the unchanged total, which is
// what "翻到第 N 页" should mean when the list shrank under the reader.
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]TaskRow, int, error) {
	db, err := s.db.Read()
	if err != nil {
		return nil, 0, err
	}
	where, args := f.where()

	var total int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM tasks t JOIN batches b ON b.id = t.batch_id WHERE `+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("tasks: count the task list: %w", err)
	}

	page, perPage := f.Page, f.PerPage
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	pageArgs := append(append([]any(nil), args...), perPage, (page-1)*perPage)
	rows, err := db.QueryContext(ctx,
		`SELECT t.id, t.status, t.number, t.batch_id, t.pipeline_id, t.metadata_json, t.last_error,
		        t.created_at, t.updated_at, b.trigger, b.trigger_ref
		   FROM tasks t JOIN batches b ON b.id = t.batch_id
		  WHERE `+where+`
		  ORDER BY t.created_at DESC, t.id DESC
		  LIMIT ? OFFSET ?`, pageArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("tasks: read the task list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []TaskRow
	for rows.Next() {
		var (
			row                TaskRow
			createdAt, updated string
		)
		if err := rows.Scan(&row.ID, &row.Status, &row.Number, &row.BatchID, &row.PipelineID,
			&row.MetadataJSON, &row.LastError, &createdAt, &updated, &row.Trigger, &row.TriggerRef); err != nil {
			return nil, 0, err
		}
		if row.CreatedAt, err = parseStamp(createdAt); err != nil {
			return nil, 0, err
		}
		if row.UpdatedAt, err = parseStamp(updated); err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	return out, total, rows.Err()
}

// BatchCount is how many batches exist, which is the `total` of the dropdown's
// envelope: the page it carries is a limit, not the whole table.
func (s *Store) BatchCount(ctx context.Context) (int, error) {
	db, err := s.db.Read()
	if err != nil {
		return 0, err
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM batches`).Scan(&n); err != nil {
		return 0, fmt.Errorf("tasks: count the batches: %w", err)
	}
	return n, nil
}

// Batches reads the newest batches, newest first, for the list page's filter
// dropdown. There is no batch view: this is the only reader, and the time label it
// feeds is `started_at`.
func (s *Store) Batches(ctx context.Context, limit int) ([]Batch, error) {
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, trigger, trigger_ref, pipeline_id, status, total, tasks, succeeded,
		        partial, failed, needs_number, skipped, error, started_at, finished_at
		   FROM batches ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("tasks: read the batch list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Batch
	for rows.Next() {
		var (
			b        Batch
			started  string
			finished sql.NullString
		)
		if err := rows.Scan(&b.ID, &b.Trigger, &b.TriggerRef, &b.PipelineID, &b.Status, &b.Total,
			&b.Tasks, &b.Succeeded, &b.Partial, &b.Failed, &b.NeedsNumber, &b.Skipped, &b.Error,
			&started, &finished); err != nil {
			return nil, err
		}
		if b.StartedAt, err = parseStamp(started); err != nil {
			return nil, err
		}
		if b.FinishedAt, err = nullStamp(finished); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if out == nil {
		out = []Batch{}
	}
	return out, rows.Err()
}

// LatestRuns reads the newest run of each named task. The list's `stage` and
// `out_dir` are derived from the latest run only, so reading the history would be
// reading more than the page shows.
func (s *Store) LatestRuns(ctx context.Context, taskIDs []int64) (map[int64]Run, error) {
	out := make(map[int64]Run, len(taskIDs))
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(taskIDs); start += queryChunk {
		chunk := taskIDs[start:min(start+queryChunk, len(taskIDs))]
		args := int64Args(chunk)
		rows, err := db.QueryContext(ctx,
			`SELECT r.id, r.task_id, r.run_no, r.status, r.started_at, r.finished_at, r.error
			   FROM task_runs r
			   JOIN (SELECT task_id, max(run_no) AS n FROM task_runs WHERE task_id IN (`+placeholders(len(chunk))+`)
			          GROUP BY task_id) m ON m.task_id = r.task_id AND m.n = r.run_no`,
			args...)
		if err != nil {
			return nil, fmt.Errorf("tasks: read the latest runs: %w", err)
		}
		if err := scanRuns(rows, func(r Run) { out[r.TaskID] = r }); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// StepsOfRuns reads the step runs of several runs at once, keyed by run. The
// detail page shows only the latest run's trace; the list only needs the last step
// type it contains.
func (s *Store) StepsOfRuns(ctx context.Context, runIDs []int64) (map[int64][]Step, error) {
	out := make(map[int64][]Step, len(runIDs))
	if len(runIDs) == 0 {
		return out, nil
	}
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(runIDs); start += queryChunk {
		chunk := runIDs[start:min(start+queryChunk, len(runIDs))]
		rows, err := db.QueryContext(ctx,
			`SELECT id, idx, type, name, status, started_at, ms, summary_json, error, batch_id, task_run_id
			   FROM step_runs WHERE task_run_id IN (`+placeholders(len(chunk))+`)
			  ORDER BY task_run_id, idx, id`, int64Args(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("tasks: read the step runs of %d run(s): %w", len(chunk), err)
		}
		if err := scanSteps(rows, func(st Step) { out[st.TaskRunID] = append(out[st.TaskRunID], st) }); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// FileRowsOfRuns reads the output ledger of several runs at once, keyed by run.
func (s *Store) FileRowsOfRuns(ctx context.Context, runIDs []int64) (map[int64][]FileRow, error) {
	out := make(map[int64][]FileRow, len(runIDs))
	if len(runIDs) == 0 {
		return out, nil
	}
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(runIDs); start += queryChunk {
		chunk := runIDs[start:min(start+queryChunk, len(runIDs))]
		rows, err := db.QueryContext(ctx,
			`SELECT id, task_id, task_run_id, step_run_id, kind, role, idx, src, dst, bytes, sha256, action, created_at
			   FROM file_rows WHERE task_run_id IN (`+placeholders(len(chunk))+`)
			  ORDER BY task_run_id, id`, int64Args(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("tasks: read the ledger of %d run(s): %w", len(chunk), err)
		}
		if err := scanFileRows(rows, func(r FileRow) { out[r.TaskRunID] = append(out[r.TaskRunID], r) }); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SourceRunsOfSteps reads the per-source traces of several step runs at once, keyed
// by step run.
func (s *Store) SourceRunsOfSteps(ctx context.Context, stepIDs []int64) (map[int64][]SourceRun, error) {
	out := make(map[int64][]SourceRun, len(stepIDs))
	if len(stepIDs) == 0 {
		return out, nil
	}
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(stepIDs); start += queryChunk {
		chunk := stepIDs[start:min(start+queryChunk, len(stepIDs))]
		rows, err := db.QueryContext(ctx,
			`SELECT id, step_run_id, source_id, status, err, reason, fields_json, ms
			   FROM source_runs WHERE step_run_id IN (`+placeholders(len(chunk))+`)
			  ORDER BY step_run_id, id`, int64Args(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("tasks: read the source runs of %d step(s): %w", len(chunk), err)
		}
		if err := scanSourceRuns(rows, func(src SourceRun) { out[src.StepRunID] = append(out[src.StepRunID], src) }); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// TaskFileCounts counts each task's input files in one query, so a page of 100
// tasks does not cost 100 counts.
func (s *Store) TaskFileCounts(ctx context.Context, taskIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	db, err := s.db.Read()
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(taskIDs); start += queryChunk {
		chunk := taskIDs[start:min(start+queryChunk, len(taskIDs))]
		rows, err := db.QueryContext(ctx,
			`SELECT task_id, count(*) FROM task_files WHERE task_id IN (`+placeholders(len(chunk))+`) GROUP BY task_id`,
			int64Args(chunk)...)
		if err != nil {
			return nil, fmt.Errorf("tasks: count the files of %d task(s): %w", len(chunk), err)
		}
		for rows.Next() {
			var id int64
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				_ = rows.Close()
				return nil, err
			}
			out[id] = n
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		_ = rows.Close()
	}
	return out, nil
}

// int64Args turns an id list into query arguments.
func int64Args(ids []int64) []any {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// scanRuns reads run rows and reports each one.
func scanRuns(rows *sql.Rows, each func(Run)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r        Run
			started  string
			finished sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.TaskID, &r.No, &r.Status, &started, &finished, &r.Error); err != nil {
			return err
		}
		var err error
		if r.StartedAt, err = parseStamp(started); err != nil {
			return err
		}
		if r.FinishedAt, err = nullStamp(finished); err != nil {
			return err
		}
		each(r)
	}
	return rows.Err()
}

// scanSteps reads step run rows and reports each one.
func scanSteps(rows *sql.Rows, each func(Step)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			st                 Step
			started            string
			batchID, taskRunID sql.NullInt64
		)
		if err := rows.Scan(&st.ID, &st.Idx, &st.Type, &st.Name, &st.Status, &started, &st.MS,
			&st.SummaryJSON, &st.Error, &batchID, &taskRunID); err != nil {
			return err
		}
		st.BatchID, st.TaskRunID = batchID.Int64, taskRunID.Int64
		var err error
		if st.StartedAt, err = parseStamp(started); err != nil {
			return err
		}
		each(st)
	}
	return rows.Err()
}

// scanFileRows reads ledger rows and reports each one.
func scanFileRows(rows *sql.Rows, each func(FileRow)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			r       FileRow
			created string
		)
		if err := rows.Scan(&r.ID, &r.TaskID, &r.TaskRunID, &r.StepRunID, &r.Kind, &r.Role, &r.Idx,
			&r.Src, &r.Dst, &r.Bytes, &r.SHA256, &r.Action, &created); err != nil {
			return err
		}
		var err error
		if r.CreatedAt, err = parseStamp(created); err != nil {
			return err
		}
		each(r)
	}
	return rows.Err()
}

// scanSourceRuns reads source trace rows and reports each one. `reason` is
// nullable: a source that did not fail has none.
func scanSourceRuns(rows *sql.Rows, each func(SourceRun)) error {
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			src    SourceRun
			reason sql.NullString
		)
		if err := rows.Scan(&src.ID, &src.StepRunID, &src.SourceID, &src.Status, &src.Err,
			&reason, &src.FieldsJSON, &src.MS); err != nil {
			return err
		}
		src.Reason = reason.String
		each(src)
	}
	return rows.Err()
}

// ErrNoParseRun means the batch has no recorded parse step, so no candidate list
// can be offered to the number panel. It is not a failure: a task created before
// the trace existed (or by a pipeline without parse) simply has no chips.
var ErrNoParseRun = errors.New("tasks: the batch has no recorded parse step")

// ParseCandidates reads the parse step of one batch and returns its per-file
// candidate projection: path → the numbers that file's own stem offered.
//
// It reads the newest parse step run of the batch, because a batch's prefix runs
// once per trigger and the latest trace is the one the fan-out used.
func (s *Store) ParseCandidates(ctx context.Context, batchID int64) (map[string][]string, error) {
	steps, err := s.Steps(ctx, Owner{BatchID: batchID})
	if err != nil {
		return nil, err
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Type != "parse" {
			continue
		}
		return DecodeParseCandidates(steps[i].SummaryJSON)
	}
	return nil, ErrNoParseRun
}

// DecodeParseCandidates pulls the `candidates` projection out of a parse step's
// summary JSON. A malformed summary is an error rather than an empty map: silently
// showing no chips would look like "the parse found nothing".
func DecodeParseCandidates(summaryJSON string) (map[string][]string, error) {
	var summary struct {
		Candidates map[string][]string `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
		return nil, fmt.Errorf("tasks: the parse summary is not JSON: %w", err)
	}
	if summary.Candidates == nil {
		return map[string][]string{}, nil
	}
	return summary.Candidates, nil
}
