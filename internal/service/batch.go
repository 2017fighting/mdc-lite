package service

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/pipeline"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// This file is the manual trigger: `POST /api/tasks` walks the selected
// directories, fans the groups out into tasks, and stores the batch; and
// `GET /api/batches` is the list page's filter dropdown.

// CreateBatchRequest is `POST /api/tasks`'s body. The trigger is always `manual`
// here: a monitor and a webhook have their own entrances, and V1 opens neither.
//
// Exclude is optional and mirrors the preview's: the Scope Ledger's exclusion list
// is part of what the human chose, so a batch created from that page must walk the
// same tree the preview did. §14 documents the body as `{pipeline, roots[]}`; an
// absent exclude is exactly that.
type CreateBatchRequest struct {
	Pipeline string   `json:"pipeline"`
	Roots    []string `json:"roots"`
	Exclude  []string `json:"exclude,omitempty"`
	// Groups is the scan page's per-group selection: the primary paths of the groups
	// the human ticked. Absent means every group the walk found (a CLI trigger), so
	// the field is additive. The walk is unchanged — the same groups the preview
	// showed are the ones being filtered here, which is the parity §14 promises.
	Groups []string `json:"groups,omitempty"`
}

// SkippedPath is one path that became no part of any task, with the machine code
// the frontend maps to its own wording. The closed set is
// {path_occupied, filtered, not_found}.
type SkippedPath struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// CreateBatchResult is what the scan page shows after pressing 建任务: the batch it
// created, how many tasks that is, and which paths fell out.
type CreateBatchResult struct {
	BatchID int64         `json:"batch_id"`
	Created int           `json:"created"`
	Skipped []SkippedPath `json:"skipped"`
}

// CreateBatch walks the selection and stores one manual batch.
//
// The walk runs inside the prefix single-flight gate, so a manual trigger waits for
// an in-flight monitor walk (at most one) instead of stacking on it. The batch is
// then written in one transaction by the task store; the step traces follow it,
// each in its own transaction.
func (s *Service) CreateBatch(ctx context.Context, req CreateBatchRequest) (CreateBatchResult, error) {
	if _, err := s.taskStore(); err != nil {
		return CreateBatchResult{}, err
	}
	p, err := s.pipeline(req.Pipeline)
	if err != nil {
		return CreateBatchResult{}, err
	}
	roots, err := cleanPaths(req.Roots)
	if err != nil {
		return CreateBatchResult{}, err
	}
	if len(roots) == 0 {
		return CreateBatchResult{}, badRequest(CodeEmptySelection, "select at least one directory or file to scan")
	}
	existing, missing, err := s.checkRoots(roots)
	if err != nil {
		return CreateBatchResult{}, err
	}
	if len(existing) == 0 {
		return CreateBatchResult{}, badRequestf(CodeEmptySelection,
			"none of the selected paths exist any more: %v", missing)
	}
	exclude, err := cleanPaths(req.Exclude)
	if err != nil {
		return CreateBatchResult{}, err
	}
	if err := checkExcludes(existing, exclude); err != nil {
		return CreateBatchResult{}, err
	}

	var (
		run      *pipeline.Result
		outcomes []pipeline.StepOutcome
	)
	err = s.prefix(ctx, func(ctx context.Context) error {
		res, runErr := s.runPrefix(ctx, p, pipeline.Input{Roots: existing, Exclude: exclude}, &outcomes)
		run = res
		return runErr
	})
	if err != nil {
		return CreateBatchResult{}, &Error{Status: 500, Code: CodeInternal, Message: err.Error()}
	}

	// `trigger_ref` is the walk's first root: what the human selected. With several
	// roots there is no single one, so the first stands for the selection.
	in := pipeline.ToIntake(tasks.TriggerManual, existing[0], run)
	for _, path := range missing {
		in.Skipped = append(in.Skipped, tasks.SkippedPath{Path: path, Reason: tasks.ReasonNotFound})
	}
	if len(req.Groups) > 0 {
		in.Groups, err = selectGroups(in.Groups, req.Groups)
		if err != nil {
			return CreateBatchResult{}, err
		}
	}
	if len(in.Groups) == 0 && len(in.Skipped) == 0 {
		return CreateBatchResult{}, badRequest(CodeEmptySelection, "the selection matched no files")
	}

	stored, err := s.tasks.CreateBatch(ctx, in)
	if err != nil {
		return CreateBatchResult{}, err
	}
	s.recordBatchSteps(ctx, stored.BatchID, outcomes)
	s.notify()

	out := CreateBatchResult{BatchID: stored.BatchID, Created: stored.Created, Skipped: []SkippedPath{}}
	for _, sk := range stored.Skipped {
		out.Skipped = append(out.Skipped, SkippedPath{Path: sk.Path, Reason: sk.Reason})
	}
	return out, nil
}

// selectGroups keeps only the groups whose primary was ticked. The scan page's
// checkboxes are per group because a work's parts must travel together; the filter
// runs after the same walk, so "the groups I saw in the preview" is what is kept.
func selectGroups(groups []tasks.Group, selected []string) ([]tasks.Group, error) {
	wanted := make(map[string]bool, len(selected))
	for _, path := range selected {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, badRequestf(CodeBadRequest, "%s: %v", path, err)
		}
		wanted[abs] = true
	}
	kept := make([]tasks.Group, 0, len(groups))
	for _, g := range groups {
		if wanted[primaryOf(g)] {
			kept = append(kept, g)
		}
	}
	if len(kept) == 0 {
		return nil, badRequest(CodeEmptySelection, "none of the selected groups is in this walk: the tree changed since the preview")
	}
	return kept, nil
}

// primaryOf is the group's primary path: the one file with the primary role.
func primaryOf(g tasks.Group) string {
	for _, f := range g.Files {
		if f.Role == tasks.RolePrimary {
			return f.Path
		}
	}
	return ""
}

// BatchView is one row of the batch dropdown. The list page only needs the id and
// a time label (`started_at`), but the counters cost nothing here and make the same
// shape usable from the command line.
type BatchView struct {
	ID          int64   `json:"id"`
	Trigger     string  `json:"trigger"`
	TriggerRef  string  `json:"trigger_ref"`
	PipelineID  string  `json:"pipeline_id"`
	Status      string  `json:"status"`
	Total       int     `json:"total"`
	Tasks       int     `json:"tasks"`
	Succeeded   int     `json:"succeeded"`
	Partial     int     `json:"partial"`
	Failed      int     `json:"failed"`
	NeedsNumber int     `json:"needs_number"`
	Skipped     int     `json:"skipped"`
	Error       string  `json:"error"`
	StartedAt   string  `json:"started_at"`
	FinishedAt  *string `json:"finished_at"`
}

// DefaultBatchLimit is the dropdown's page size: enough history to recognize the
// trigger you are looking for, not the whole table.
const DefaultBatchLimit = 20

// Batches lists the newest batches for the list page's filter.
func (s *Service) Batches(ctx context.Context, limit int) (Page[BatchView], error) {
	if limit == 0 {
		limit = DefaultBatchLimit
	}
	if limit < 1 || limit > 500 {
		return Page[BatchView]{}, badRequestf(CodeBadRequest, "limit must be between 1 and 500, got %d", limit)
	}
	st, err := s.taskStore()
	if err != nil {
		return Page[BatchView]{}, err
	}
	rows, err := st.Batches(ctx, limit)
	if err != nil {
		return Page[BatchView]{}, err
	}
	total, err := st.BatchCount(ctx)
	if err != nil {
		return Page[BatchView]{}, err
	}
	items := make([]BatchView, 0, len(rows))
	for _, b := range rows {
		items = append(items, batchView(b))
	}
	return Page[BatchView]{Items: items, Total: total, Page: 1, PerPage: limit}, nil
}

// batchView projects one batch row.
func batchView(b tasks.Batch) BatchView {
	v := BatchView{
		ID:          b.ID,
		Trigger:     string(b.Trigger),
		TriggerRef:  b.TriggerRef,
		PipelineID:  b.PipelineID,
		Status:      string(b.Status),
		Total:       b.Total,
		Tasks:       b.Tasks,
		Succeeded:   b.Succeeded,
		Partial:     b.Partial,
		Failed:      b.Failed,
		NeedsNumber: b.NeedsNumber,
		Skipped:     b.Skipped,
		Error:       b.Error,
		StartedAt:   timeText(b.StartedAt),
	}
	if b.FinishedAt != nil {
		finished := timeText(*b.FinishedAt)
		v.FinishedAt = &finished
	}
	return v
}

// timeText renders a timestamp column for JSON. It keeps the nanosecond precision
// the database stores, so a client can use the same value as a `created_after`
// bound without losing a tie.
func timeText(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
