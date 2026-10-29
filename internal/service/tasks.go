package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/pipeline"
	"github.com/2017fighting/mdc-lite/internal/scheduler"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// This file is the task side of the API: the list with its query surface, the
// detail page's four tabs, and the four things a human can do to a task (rerun,
// cancel, rerun many, fill in a number).

// The lists this file validates. They are the spec's closed sets, not whatever the
// database happens to contain.
var (
	perPageWhitelist = map[int]bool{20: true, 50: true, 100: true}
	triggerWhitelist = map[tasks.Trigger]bool{
		tasks.TriggerManual:  true,
		tasks.TriggerMonitor: true,
		tasks.TriggerWebhook: true,
	}
	taskStatusWhitelist = map[tasks.TaskStatus]bool{
		tasks.StatusQueued:      true,
		tasks.StatusRunning:     true,
		tasks.StatusSucceeded:   true,
		tasks.StatusPartial:     true,
		tasks.StatusFailed:      true,
		tasks.StatusNeedsNumber: true,
		tasks.StatusCancelled:   true,
	}
)

// DefaultPerPage is the list's page size when the client does not name one.
const DefaultPerPage = 20

// TaskQuery is `GET /api/tasks`'s query surface after the HTTP layer has parsed the
// strings. The service re-validates what it can (the closed sets, the page size):
// the query string is a secondary entrance for manual testing, so it must not be
// able to ask for something the UI cannot.
type TaskQuery struct {
	Statuses      []string
	BatchID       int64
	Trigger       string
	Number        string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Page          int
	PerPage       int
}

// TaskView is one task row plus the fields derived for display. Nothing here is
// stored: `stage`, `title`, `sources`, `missing`, `out_dir` and `file_count` are
// projections of the latest run and the metadata column, so they cannot go stale.
type TaskView struct {
	ID         int64    `json:"id"`
	Status     string   `json:"status"`
	Number     string   `json:"number"`
	BatchID    int64    `json:"batch_id"`
	PipelineID string   `json:"pipeline_id"`
	LastError  string   `json:"last_error"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	Stage      string   `json:"stage"`
	Title      string   `json:"title"`
	Sources    []string `json:"sources"`
	Missing    []string `json:"missing"`
	Partial    bool     `json:"partial"`
	Trigger    string   `json:"trigger"`
	TriggerRef string   `json:"trigger_ref"`
	FileCount  int      `json:"file_count"`
	OutDir     string   `json:"out_dir"`
	// Files is present only for a needs_number task: the待补号 panel lives in the
	// list's side bar and needs the rows without a second request.
	Files []TaskFileView `json:"files,omitempty"`
}

// TaskFileView is one input file row, with the candidate numbers parse offered for
// it (the chips of the needs-number panel).
type TaskFileView struct {
	ID             int64    `json:"id"`
	Path           string   `json:"path"`
	Kind           string   `json:"kind"`
	Role           string   `json:"role"`
	PartIndex      int      `json:"part_index"`
	FollowsPart    int      `json:"follows_part"`
	Size           int64    `json:"size"`
	MTime          string   `json:"mtime"`
	Active         bool     `json:"active"`
	AssignedNumber string   `json:"assigned_number"`
	IsPart         bool     `json:"is_part"`
	Candidates     []string `json:"candidates"`
}

// MetadataView is the field tab: the 14 source-level fields the metadata holds,
// plus who won each field, what is still missing, and whether that makes the task
// partial. `Derived` is deliberately absent (ADR: the second batch owns it).
type MetadataView struct {
	Title        string       `json:"title"`
	Plot         string       `json:"plot"`
	Extrafanarts []string     `json:"extrafanarts"`
	Tags         []string     `json:"tags"`
	Releasedate  string       `json:"releasedate"`
	Actors       []meta.Actor `json:"actors"`
	Director     []string     `json:"director"`
	Runtime      int          `json:"runtime"`
	Rating       float64      `json:"rating"`
	Series       string       `json:"series"`
	Studio       string       `json:"studio"`
	Publisher    string       `json:"publisher"`
	// Poster and Fanart are the field values the tab can show: the URL the winning
	// source offered, taken from the run's ledger. The bytes live behind the image
	// endpoint, never in this payload.
	Poster  string            `json:"poster"`
	Fanart  string            `json:"fanart"`
	Sources map[string]string `json:"sources"`
	Missing []string          `json:"missing"`
	Partial bool              `json:"partial"`
}

// StepView is one `step_runs` row. The owner columns are not repeated: they are the
// endpoint's own id.
type StepView struct {
	ID        int64           `json:"id"`
	Idx       int             `json:"idx"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	StartedAt string          `json:"started_at"`
	MS        int64           `json:"ms"`
	Summary   json.RawMessage `json:"summary"`
	Error     string          `json:"error"`
}

// SourceRunView is one source's trace, including the six reason codes. They belong
// here and never in an error body.
type SourceRunView struct {
	ID        int64           `json:"id"`
	StepRunID int64           `json:"step_run_id"`
	SourceID  string          `json:"source_id"`
	Status    string          `json:"status"`
	Err       string          `json:"err"`
	Reason    string          `json:"reason,omitempty"`
	Fields    json.RawMessage `json:"fields"`
	MS        int64           `json:"ms"`
}

// FileRowView is one line of the output ledger, all columns included: the ledger is
// the record of what landed on disk.
type FileRowView struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"task_id"`
	TaskRunID int64  `json:"task_run_id"`
	StepRunID int64  `json:"step_run_id"`
	Kind      string `json:"kind"`
	Role      string `json:"role"`
	Idx       int    `json:"idx"`
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Action    string `json:"action"`
	CreatedAt string `json:"created_at"`
}

// ImageView is the ImageRef the detail page needs: which image it is, where it came
// from, and the endpoint to ask for the bytes. The bytes themselves are never in
// this payload.
type ImageView struct {
	Role string `json:"role"`
	Idx  int    `json:"idx"`
	Src  string `json:"src"`
	URL  string `json:"url"`
}

// TaskDetail is the detail page in one answer: the task row, then the four tabs.
type TaskDetail struct {
	TaskView
	Metadata   MetadataView    `json:"metadata"`
	TaskFiles  []TaskFileView  `json:"task_files"`
	StepRuns   []StepView      `json:"step_runs"`
	SourceRuns []SourceRunView `json:"source_runs"`
	FileRows   []FileRowView   `json:"file_rows"`
	Images     []ImageView     `json:"images"`
}

// ListTasks answers `GET /api/tasks`.
func (s *Service) ListTasks(ctx context.Context, q TaskQuery) (Page[TaskView], error) {
	st, err := s.taskStore()
	if err != nil {
		return Page[TaskView]{}, err
	}
	filter, err := q.filter()
	if err != nil {
		return Page[TaskView]{}, err
	}
	rows, total, err := st.ListTasks(ctx, filter)
	if err != nil {
		return Page[TaskView]{}, err
	}

	ids := make([]int64, 0, len(rows))
	batchIDs := make([]int64, 0, len(rows))
	needsNumber := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
		batchIDs = append(batchIDs, row.BatchID)
		if row.Status == tasks.StatusNeedsNumber {
			needsNumber = append(needsNumber, row.ID)
		}
	}
	latest, err := st.LatestRuns(ctx, ids)
	if err != nil {
		return Page[TaskView]{}, err
	}
	runIDs := make([]int64, 0, len(latest))
	for _, run := range latest {
		runIDs = append(runIDs, run.ID)
	}
	steps, err := st.StepsOfRuns(ctx, runIDs)
	if err != nil {
		return Page[TaskView]{}, err
	}
	ledger, err := st.FileRowsOfRuns(ctx, runIDs)
	if err != nil {
		return Page[TaskView]{}, err
	}
	fileCounts, err := st.TaskFileCounts(ctx, ids)
	if err != nil {
		return Page[TaskView]{}, err
	}
	candidates, err := s.candidatesFor(ctx, batchIDs)
	if err != nil {
		return Page[TaskView]{}, err
	}
	files := map[int64][]TaskFileView{}
	for _, id := range needsNumber {
		files[id], err = s.taskFileViews(ctx, st, id, candidates)
		if err != nil {
			return Page[TaskView]{}, err
		}
	}

	items := make([]TaskView, 0, len(rows))
	for _, row := range rows {
		view := taskView(row, latest[row.ID], steps, ledger, fileCounts[row.ID])
		if row.Status == tasks.StatusNeedsNumber {
			view.Files = files[row.ID]
		}
		items = append(items, view)
	}
	return Page[TaskView]{Items: items, Total: total, Page: filter.Page, PerPage: filter.PerPage}, nil
}

// filter validates the query and converts it for the store.
func (q TaskQuery) filter() (tasks.TaskFilter, error) {
	f := tasks.TaskFilter{
		BatchID:       q.BatchID,
		Number:        strings.TrimSpace(q.Number),
		CreatedAfter:  q.CreatedAfter,
		CreatedBefore: q.CreatedBefore,
		Page:          q.Page,
		PerPage:       q.PerPage,
	}
	for _, raw := range q.Statuses {
		status := tasks.TaskStatus(strings.TrimSpace(raw))
		if status == "" {
			continue
		}
		if !taskStatusWhitelist[status] {
			return tasks.TaskFilter{}, badRequestf(CodeBadRequest, "%q is not a task status", raw)
		}
		f.Statuses = append(f.Statuses, status)
	}
	if q.Trigger != "" {
		trigger := tasks.Trigger(q.Trigger)
		if !triggerWhitelist[trigger] {
			return tasks.TaskFilter{}, badRequestf(CodeBadRequest, "%q is not a trigger (manual, monitor, webhook)", q.Trigger)
		}
		f.Trigger = trigger
	}
	if f.Page == 0 {
		f.Page = 1
	}
	if f.Page < 1 {
		return tasks.TaskFilter{}, badRequestf(CodeBadRequest, "page must be >= 1, got %d", f.Page)
	}
	if f.PerPage == 0 {
		f.PerPage = DefaultPerPage
	}
	if !perPageWhitelist[f.PerPage] {
		return tasks.TaskFilter{}, badRequestf(CodeBadRequest, "per_page must be one of 20, 50, 100, got %d", f.PerPage)
	}
	if !f.CreatedAfter.IsZero() && !f.CreatedBefore.IsZero() && f.CreatedAfter.After(f.CreatedBefore) {
		return tasks.TaskFilter{}, badRequest(CodeBadRequest, "created_after is later than created_before")
	}
	return f, nil
}

// taskView derives one row's display fields from the latest run, its ledger and its
// metadata.
func taskView(row tasks.TaskRow, run tasks.Run, steps map[int64][]tasks.Step, ledger map[int64][]tasks.FileRow, fileCount int) TaskView {
	md := parseMetadata(row.MetadataJSON)
	view := TaskView{
		ID:         row.ID,
		Status:     string(row.Status),
		Number:     row.Number,
		BatchID:    row.BatchID,
		PipelineID: row.PipelineID,
		LastError:  row.LastError,
		CreatedAt:  timeText(row.CreatedAt),
		UpdatedAt:  timeText(row.UpdatedAt),
		Stage:      latestStage(steps[run.ID]),
		Title:      md.Title,
		Sources:    sourceIDs(md),
		Missing:    missingFields(md),
		Partial:    md.Partial,
		Trigger:    string(row.Trigger),
		TriggerRef: row.TriggerRef,
		FileCount:  fileCount,
		OutDir:     outputDir(ledger[run.ID]),
	}
	return view
}

// latestStage is the type of the last step the latest run recorded: "where the task
// is" without a status column for it.
func latestStage(steps []tasks.Step) string {
	if len(steps) == 0 {
		return ""
	}
	last := steps[0]
	for _, st := range steps {
		if st.Idx > last.Idx || (st.Idx == last.Idx && st.ID > last.ID) {
			last = st
		}
	}
	return last.Type
}

// outputDir is where the run put its products: the directory of the video row (the
// one the media server will scan), else of anything it wrote.
func outputDir(rows []tasks.FileRow) string {
	for _, row := range rows {
		if row.Action == string(pipeline.ActionDeleted) {
			continue
		}
		if row.Kind == string(pipeline.WriteVideo) {
			return filepath.Dir(row.Dst)
		}
	}
	for _, row := range rows {
		if row.Action != string(pipeline.ActionDeleted) {
			return filepath.Dir(row.Dst)
		}
	}
	return ""
}

// parseMetadata decodes the stored metadata column. A hand-edited value is reported
// as empty metadata rather than failing the page: the tab then says "nothing known"
// and the row still lists.
func parseMetadata(raw string) meta.Metadata {
	var md meta.Metadata
	if err := decodeJSON(raw, &md); err != nil {
		return meta.Metadata{}
	}
	return md
}

// sourceIDs is the distinct set of sources that won at least one field, in field
// order so the list is stable. The poster crop's "(自动裁切)" suffix is not part of
// the source's identity, so it is trimmed for this column.
func sourceIDs(md meta.Metadata) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, f := range field.All() {
		id := strings.TrimSuffix(md.Source(f), " (自动裁切)")
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// missingFields is the missing set as strings, always an array.
func missingFields(md meta.Metadata) []string {
	out := make([]string, 0, len(md.Missing))
	for _, f := range md.Missing {
		out = append(out, string(f))
	}
	return out
}

// metadataView projects the stored metadata onto the field tab. The two image
// fields have no value in the metadata column — the source offered a URL, not the
// field — so their values come from the latest run's ledger `src`. (The image
// endpoints walk back to the newest run that actually wrote bytes, which can be an
// earlier one; the tab describes the latest run, as the detail page does.)
func metadataView(md meta.Metadata, ledger []tasks.FileRow) MetadataView {
	sources := make(map[string]string, len(md.Sources))
	for f, id := range md.Sources {
		sources[string(f)] = id
	}
	return MetadataView{
		Title:        md.Title,
		Plot:         md.Plot,
		Extrafanarts: nonNilStrings(md.Extrafanarts),
		Tags:         nonNilStrings(md.Tags),
		Releasedate:  md.Releasedate,
		Actors:       nonNilActors(md.Actors),
		Director:     nonNilStrings(md.Director),
		Runtime:      md.Runtime,
		Rating:       md.Rating,
		Series:       md.Series,
		Studio:       md.Studio,
		Publisher:    md.Publisher,
		Poster:       imageSrc(ledger, "poster"),
		Fanart:       imageSrc(ledger, "fanart"),
		Sources:      sources,
		Missing:      missingFields(md),
		Partial:      md.Partial,
	}
}

// TaskDetail answers `GET /api/tasks/{id}`.
func (s *Service) TaskDetail(ctx context.Context, id int64) (TaskDetail, error) {
	st, err := s.taskStore()
	if err != nil {
		return TaskDetail{}, err
	}
	task, err := st.Task(ctx, id)
	if err != nil {
		return TaskDetail{}, taskLookupError(err)
	}
	batch, err := st.Batch(ctx, task.BatchID)
	if err != nil {
		return TaskDetail{}, err
	}
	latest, err := st.LatestRuns(ctx, []int64{id})
	if err != nil {
		return TaskDetail{}, err
	}
	run := latest[id]
	stepsByRun, err := st.StepsOfRuns(ctx, []int64{run.ID})
	if err != nil {
		return TaskDetail{}, err
	}
	ledgerByRun, err := st.FileRowsOfRuns(ctx, []int64{run.ID})
	if err != nil {
		return TaskDetail{}, err
	}
	fileCounts, err := st.TaskFileCounts(ctx, []int64{id})
	if err != nil {
		return TaskDetail{}, err
	}
	candidates, err := s.candidatesFor(ctx, []int64{task.BatchID})
	if err != nil {
		return TaskDetail{}, err
	}
	files, err := s.taskFileViews(ctx, st, id, candidates)
	if err != nil {
		return TaskDetail{}, err
	}

	stepIDs := make([]int64, 0, len(stepsByRun[run.ID]))
	for _, st := range stepsByRun[run.ID] {
		stepIDs = append(stepIDs, st.ID)
	}
	sourceRuns, err := st.SourceRunsOfSteps(ctx, stepIDs)
	if err != nil {
		return TaskDetail{}, err
	}

	md := parseMetadata(task.MetadataJSON)
	view := taskView(tasks.TaskRow{Task: task, Trigger: batch.Trigger, TriggerRef: batch.TriggerRef},
		run, stepsByRun, ledgerByRun, fileCounts[id])
	view.Files = nil // the detail's own task_files tab is the full list
	detail := TaskDetail{
		TaskView:   view,
		Metadata:   metadataView(md, ledgerByRun[run.ID]),
		TaskFiles:  files,
		StepRuns:   stepViews(stepsByRun[run.ID]),
		SourceRuns: []SourceRunView{},
		FileRows:   fileRowViews(ledgerByRun[run.ID]),
		Images:     imageViews(id, ledgerByRun[run.ID]),
	}
	for _, st := range stepsByRun[run.ID] {
		for _, src := range sourceRuns[st.ID] {
			detail.SourceRuns = append(detail.SourceRuns, SourceRunView{
				ID:        src.ID,
				StepRunID: src.StepRunID,
				SourceID:  src.SourceID,
				Status:    src.Status,
				Err:       src.Err,
				Reason:    src.Reason,
				Fields:    rawJSON(src.FieldsJSON),
				MS:        src.MS,
			})
		}
	}
	return detail, nil
}

// taskFileViews reads one task's input files and attaches the candidates parse
// offered for each path.
func (s *Service) taskFileViews(ctx context.Context, st *tasks.Store, taskID int64, candidates map[string][]string) ([]TaskFileView, error) {
	files, err := st.TaskFiles(ctx, taskID)
	if err != nil {
		return nil, err
	}
	out := make([]TaskFileView, 0, len(files))
	for _, f := range files {
		out = append(out, TaskFileView{
			ID:             f.ID,
			Path:           f.Path,
			Kind:           string(f.Kind),
			Role:           string(f.Role),
			PartIndex:      f.PartIndex,
			FollowsPart:    f.FollowsPart,
			Size:           f.Size,
			MTime:          f.ModTime,
			Active:         f.Active,
			AssignedNumber: f.AssignedNumber,
			IsPart:         f.IsPart,
			Candidates:     nonNil(candidates[f.Path]),
		})
	}
	return out, nil
}

// candidatesFor reads each batch's parse candidate projection once per request. A
// batch with no recorded parse step (a task from before the trace, or a pipeline
// without parse) contributes nothing: no chips, no error.
func (s *Service) candidatesFor(ctx context.Context, batchIDs []int64) (map[string][]string, error) {
	out := map[string][]string{}
	if len(batchIDs) == 0 || s.tasks == nil {
		return out, nil
	}
	seen := map[int64]bool{}
	for _, batchID := range batchIDs {
		if seen[batchID] {
			continue
		}
		seen[batchID] = true
		candidates, err := s.tasks.ParseCandidates(ctx, batchID)
		if errors.Is(err, tasks.ErrNoParseRun) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for path, numbers := range candidates {
			out[path] = numbers
		}
	}
	return out, nil
}

// imageSrc is where one image's bytes came from, as the ledger recorded it.
func imageSrc(rows []tasks.FileRow, role string) string {
	for _, row := range rows {
		if row.Kind == string(pipeline.WriteImage) && row.Role == role &&
			row.Action != string(pipeline.ActionDeleted) {
			return row.Src
		}
	}
	return ""
}

// stepViews projects step rows for the trace tab.
func stepViews(steps []tasks.Step) []StepView {
	out := make([]StepView, 0, len(steps))
	for _, st := range steps {
		out = append(out, StepView{
			ID:        st.ID,
			Idx:       st.Idx,
			Type:      st.Type,
			Name:      st.Name,
			Status:    st.Status,
			StartedAt: timeText(st.StartedAt),
			MS:        st.MS,
			Summary:   rawJSON(st.SummaryJSON),
			Error:     st.Error,
		})
	}
	return out
}

// fileRowViews projects the ledger tab.
func fileRowViews(rows []tasks.FileRow) []FileRowView {
	out := make([]FileRowView, 0, len(rows))
	for _, row := range rows {
		out = append(out, FileRowView{
			ID:        row.ID,
			TaskID:    row.TaskID,
			TaskRunID: row.TaskRunID,
			StepRunID: row.StepRunID,
			Kind:      row.Kind,
			Role:      row.Role,
			Idx:       row.Idx,
			Src:       row.Src,
			Dst:       row.Dst,
			Bytes:     row.Bytes,
			SHA256:    row.SHA256,
			Action:    row.Action,
			CreatedAt: timeText(row.CreatedAt),
		})
	}
	return out
}

// imageViews is the ImageRef list: one entry per landed image, with the endpoint
// that serves its bytes (the bytes are never part of this payload).
func imageViews(taskID int64, rows []tasks.FileRow) []ImageView {
	out := []ImageView{}
	for _, row := range rows {
		if row.Kind != string(pipeline.WriteImage) || row.Action == string(pipeline.ActionDeleted) {
			continue
		}
		switch row.Role {
		case pipeline.RolePoster, pipeline.RoleFanart, pipeline.RoleExtrafanart:
		default:
			continue
		}
		out = append(out, ImageView{
			Role: row.Role,
			Idx:  row.Idx,
			Src:  row.Src,
			URL:  imageURL(taskID, row.Role, row.Idx),
		})
	}
	return out
}

// RerunResult is one rerun's answer.
type RerunResult struct {
	TaskID int64  `json:"task_id"`
	Status string `json:"status"`
}

// Rerun puts a finished task back in the queue: the rerun is the task-level suffix
// starting at `parse`, and the scheduler opens the new run when a slot frees.
//
// The spec's response shape was `{task_run_id}`, which the queue makes impossible to
// answer honestly: the run row is created by the slot that claims the task, not by
// the request. The 202 therefore says `queued`, and the run id appears on the task
// as soon as one exists.
func (s *Service) Rerun(ctx context.Context, id int64) (RerunResult, error) {
	st, err := s.taskStore()
	if err != nil {
		return RerunResult{}, err
	}
	task, err := st.Task(ctx, id)
	if err != nil {
		return RerunResult{}, taskLookupError(err)
	}
	// The two states a rerun cannot act on are decided here, so the answer does not
	// depend on whether a scheduler happens to be wired: a queued task is already
	// waiting (say so, and wake the dispatcher), a needs_number task must be given a
	// number first, a running task owns its slot.
	switch task.Status {
	case tasks.StatusQueued:
		s.notify()
		return RerunResult{TaskID: id, Status: string(tasks.StatusQueued)}, nil
	case tasks.StatusNeedsNumber:
		return RerunResult{}, badRequest(CodeBadRequest, "the task is waiting for a number; submit one instead of rerunning it")
	case tasks.StatusRunning:
		return RerunResult{}, conflict(CodeTaskRunning, "the task is running; wait for it or cancel it")
	}
	if s.sched != nil {
		if err := s.sched.Rerun(ctx, id); err != nil {
			return RerunResult{}, mapTaskActionError(err)
		}
	} else if _, err := st.Requeue(ctx, id); err != nil {
		return RerunResult{}, mapTaskActionError(err)
	}
	return RerunResult{TaskID: id, Status: string(tasks.StatusQueued)}, nil
}

// BatchRerunItem is one line of a bulk rerun's answer: did this id requeue, and if
// not, which machine code says why.
type BatchRerunItem struct {
	TaskID int64  `json:"task_id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

// RerunMany reruns a list of tasks, one answer per id. A bulk action is not a
// transaction: one running task must not stop the other nineteen from requeueing.
func (s *Service) RerunMany(ctx context.Context, ids []int64) []BatchRerunItem {
	out := make([]BatchRerunItem, 0, len(ids))
	for _, id := range ids {
		_, err := s.Rerun(ctx, id)
		item := BatchRerunItem{TaskID: id, OK: err == nil}
		if err != nil {
			var se *Error
			if errors.As(err, &se) {
				item.Error = se.Code
			} else {
				item.Error = CodeInternal
			}
		}
		out = append(out, item)
	}
	return out
}

// Cancel stops a task. It is also the needs-number panel's 跳过: skipping a number
// is cancelling the task, and the terminal write releases the paths.
func (s *Service) Cancel(ctx context.Context, id int64) (string, error) {
	st, err := s.taskStore()
	if err != nil {
		return "", err
	}
	if s.sched != nil {
		if err := s.sched.Cancel(ctx, id); err != nil {
			return "", mapTaskActionError(err)
		}
		return string(tasks.StatusCancelled), nil
	}
	if _, err := st.CancelTask(ctx, id); err != nil {
		return "", mapTaskActionError(err)
	}
	return string(tasks.StatusCancelled), nil
}

// NumberFill is one file's human-typed number in the panel's submission.
type NumberFill struct {
	Path           string `json:"path"`
	AssignedNumber string `json:"assigned_number"`
	IsPart         bool   `json:"is_part"`
}

// NumbersResult is the numbers endpoint's answer: the task went back into the queue
// and a new run starts at `parse`.
type NumbersResult struct {
	TaskID int64  `json:"task_id"`
	Status string `json:"status"`
}

// FillNumbers stores a human's numbers and requeues the task.
//
// The typed number runs through the same normalization as a file name, so `ssis-1`
// and a file's `SSIS-1` are one work. Every video must be numbered in one submit;
// `numbers_incomplete` carries the paths that were left empty, which is what the
// panel highlights.
func (s *Service) FillNumbers(ctx context.Context, id int64, fills []NumberFill) (NumbersResult, error) {
	st, err := s.taskStore()
	if err != nil {
		return NumbersResult{}, err
	}
	converted := make([]tasks.Fill, 0, len(fills))
	seen := make(map[string]bool, len(fills))
	for _, f := range fills {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			return NumbersResult{}, badRequest(CodeBadRequest, "a number was submitted without a path")
		}
		if seen[path] {
			return NumbersResult{}, badRequestf(CodeBadRequest, "%s was submitted twice", path)
		}
		seen[path] = true
		// The typed number is normalized exactly like a name from disk, so `ssis-1`
		// and `SSIS-1` are one work and the task's number matches its file.
		number := pipeline.NormalizeNumber(strings.TrimSpace(f.AssignedNumber))
		converted = append(converted, tasks.Fill{Path: path, Number: number, IsPart: f.IsPart})
	}
	if err := st.AssignNumbers(ctx, id, converted); err != nil {
		return NumbersResult{}, mapNumbersError(err)
	}
	s.notify()
	return NumbersResult{TaskID: id, Status: string(tasks.StatusQueued)}, nil
}

// mapNumbersError turns the store's failures into the API's answers. In particular
// an incomplete submission is a 422 with the paths, not a 400 with a sentence.
func mapNumbersError(err error) error {
	var incomplete *tasks.IncompleteNumbersError
	if errors.As(err, &incomplete) {
		return &Error{
			Status:  422,
			Code:    CodeNumbersIncomplete,
			Message: "every video needs a number before the task can run",
			Detail:  map[string]any{"missing_paths": incomplete.Missing},
		}
	}
	switch {
	case errors.Is(err, tasks.ErrTaskNotFound):
		return notFound("no such task")
	case errors.Is(err, tasks.ErrUnknownPath):
		return badRequest(CodeBadRequest, err.Error())
	case errors.Is(err, tasks.ErrNotWaitingForNumber):
		return badRequest(CodeBadRequest, err.Error())
	}
	return err
}

// taskLookupError maps the store's "no such task" onto the API's 404.
func taskLookupError(err error) error {
	if errors.Is(err, tasks.ErrTaskNotFound) {
		return notFound("no such task")
	}
	return err
}

// mapTaskActionError maps the scheduler's and the store's action refusals onto the
// two 409 codes, and a missing task onto 404.
func mapTaskActionError(err error) error {
	switch {
	case errors.Is(err, tasks.ErrTaskNotFound):
		return notFound("no such task")
	case errors.Is(err, scheduler.ErrTaskRunning), errors.Is(err, tasks.ErrTaskRunning):
		return conflict(CodeTaskRunning, "the task is running; wait for it or cancel it")
	case errors.Is(err, scheduler.ErrTaskTerminal), errors.Is(err, tasks.ErrTaskTerminal):
		return conflict(CodeTaskTerminal, "the task already ended")
	case errors.Is(err, scheduler.ErrNeedsNumber):
		return badRequest(CodeBadRequest, "the task is waiting for a number; submit one instead of rerunning it")
	case errors.Is(err, scheduler.ErrNotFinished), errors.Is(err, tasks.ErrNotFinished):
		return badRequest(CodeBadRequest, err.Error())
	case errors.Is(err, tasks.ErrPathOccupied):
		// Not a 409: the task is idle, another live task simply holds one of its
		// paths (the "可重跑" hint gate). The paths are named in the message.
		return badRequest(CodeBadRequest, err.Error())
	}
	return err
}

// rawJSON passes a stored JSON column through as a JSON value. An unreadable column
// becomes `{}` so one hand-edited row cannot break the whole response.
func rawJSON(raw string) json.RawMessage {
	if raw == "" || !json.Valid([]byte(raw)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(raw)
}

// nonNilStrings keeps a list from serialising as null.
func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// nonNilActors is nonNilStrings for the actor list.
func nonNilActors(in []meta.Actor) []meta.Actor {
	if in == nil {
		return []meta.Actor{}
	}
	return in
}
