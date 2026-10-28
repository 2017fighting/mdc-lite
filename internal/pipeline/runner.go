package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/tasks"
)

// Runner executes one task's task-level suffix against the store: it begins a run,
// reconstructs the artifacts a rerun starts from (the files come from `task_files`
// and the number from `tasks`), records one `step_runs` row per step, applies the
// output plan, writes the ledger, and cleans up what this run no longer produces.
//
// A task run never walks a directory, and it does not re-run `parse`: the number is
// already decided and stored (a human may have typed it), so re-deriving it from the
// names would either duplicate that work or overwrite the human's answer. The parse
// artifact is reconstructed from `task_files` instead, and the suffix runs from
// `scrape` on — that is what "重跑 = 重跑任务级后缀" means once the number is a row.
type Runner struct {
	store     *tasks.Store
	pipelines map[string]config.Pipeline
	env       Environment
}

// NewRunner prepares a runner over a store and the currently loaded pipelines. A
// rerun runs against the current pipeline of that id, so a pipeline deleted since
// the batch is a run-time failure that names the missing id.
func NewRunner(st *tasks.Store, pipelines []config.Pipeline, env Environment) *Runner {
	byID := make(map[string]config.Pipeline, len(pipelines))
	for _, p := range pipelines {
		byID[p.ID] = p
	}
	return &Runner{store: st, pipelines: byID, env: env}
}

// RunTask runs one task. A run that ends failed is a successful call: the failure is
// the run's outcome, recorded in the database, not an error of the caller. The
// returned error is only for a failure to record (or to finish) the run at all.
func (r *Runner) RunTask(ctx context.Context, taskID int64) (tasks.Outcome, error) {
	task, err := r.store.Task(ctx, taskID)
	if err != nil {
		return tasks.Outcome{}, err
	}
	files, err := r.store.TaskFiles(ctx, taskID)
	if err != nil {
		return tasks.Outcome{}, err
	}
	// The previous run's ledger is read before this run starts, because cleanup is
	// "this run's products against the last run's".
	prior, err := r.priorLedger(ctx, taskID)
	if err != nil {
		return tasks.Outcome{}, err
	}

	run, err := r.store.BeginRun(ctx, taskID)
	if err != nil {
		return tasks.Outcome{}, err
	}
	finish := func(out tasks.Outcome) (tasks.Outcome, error) {
		if err := r.store.FinishRun(ctx, run.ID, out); err != nil {
			return out, err
		}
		return out, nil
	}

	p, ok := r.pipelines[task.PipelineID]
	if !ok {
		return finish(tasks.Outcome{
			Status: tasks.RunFailed,
			Error:  fmt.Sprintf("pipeline %q no longer exists: a rerun uses the current pipeline of that name", task.PipelineID),
		})
	}
	if strings.TrimSpace(task.Number) == "" {
		return finish(tasks.Outcome{
			Status: tasks.RunNeedsNumber,
			Reason: tasks.ReasonNoCandidate,
			Error:  "the task has no number yet",
		})
	}

	state := seedState(files, task.Number)
	from := suffixStartKey(p)
	if from == "" {
		// The pipeline ends at parse: there is nothing to run, and nothing to write.
		return finish(tasks.Outcome{Status: tasks.RunSucceeded, MetadataJSON: metadataJSON(state)})
	}

	var outputStepRunID int64
	opts := Options{
		From: from,
		Seed: state,
		Env:  r.env,
		OnStep: func(ctx context.Context, o StepOutcome) error {
			id, err := r.recordStep(ctx, run.ID, state, o)
			if err != nil {
				return err
			}
			if o.Type == config.StepOutput && o.Status != "failed" {
				outputStepRunID = id
			}
			return nil
		},
	}
	engine, err := Build(p, opts)
	if err != nil {
		return finish(tasks.Outcome{Status: tasks.RunFailed, Error: err.Error()})
	}
	if _, err := engine.Run(ctx); err != nil {
		return finish(tasks.Outcome{Status: tasks.RunFailed, Error: err.Error()})
	}

	md := finalMetadata(state)
	status := tasks.RunSucceeded
	if md.Partial || len(state.Partial) > 0 {
		status = tasks.RunPartial
	}

	var (
		rows      []tasks.FileRow
		deletions []Row
	)
	if state.Plan != nil && len(state.Plan.Items) > 0 && !engine.DryRun() {
		manifest, err := Apply(ctx, state.Plan)
		if err != nil {
			return finish(tasks.Outcome{Status: tasks.RunFailed, Error: err.Error()})
		}
		state.Manifest = manifest
		// Cleanup is decided before the finish transaction and performed inside it:
		// the ledger row that says "deleted" and the unlink are one fact.
		deletions = StaleRows(prior, manifest.Rows, state.Plan.Items[0].Dir)
		all := append(append([]Row(nil), manifest.Rows...), deletions...)
		for _, row := range all {
			rows = append(rows, ledgerRow(row, outputStepRunID))
		}
	}
	out := tasks.Outcome{
		Status:       status,
		MetadataJSON: metadataJSON(state),
		Rows:         rows,
	}
	if len(deletions) > 0 {
		// The transaction's context is used, which is the caller's: a cleanup that
		// fails rolls the finish back, so a task never looks finished with its old
		// files still on disk.
		out.BeforeCommit = func(ctx context.Context) error { return RemoveStale(ctx, deletions) }
	}
	return finish(out)
}

// recordStep writes one step_runs row, and — for the scrape step — its source_runs
// rows. It returns the new step run's id; the output step's id is what every ledger
// row this run writes belongs to.
func (r *Runner) recordStep(ctx context.Context, runID int64, state *State, o StepOutcome) (int64, error) {
	summary := "{}"
	if o.Summary != nil {
		data, err := json.Marshal(o.Summary)
		if err != nil {
			return 0, fmt.Errorf("summary of the %s step: %w", o.Type, err)
		}
		summary = string(data)
	}
	stepErr := ""
	if o.Err != nil {
		stepErr = o.Err.Error()
	}
	id, err := r.store.RecordStep(ctx, tasks.Step{
		TaskRunID:   runID,
		Idx:         o.Idx,
		Type:        o.Type,
		Name:        o.Name,
		Status:      o.Status,
		MS:          o.MS,
		SummaryJSON: summary,
		Error:       stepErr,
	})
	if err != nil {
		return 0, err
	}
	if o.Type == config.StepScrape && state.ScrapeRun != nil {
		for _, a := range state.ScrapeRun.Attempts {
			if err := r.store.RecordSource(ctx, tasks.SourceRun{
				StepRunID:  id,
				SourceID:   a.ID,
				Status:     a.Status,
				Err:        a.Err,
				Reason:     a.Reason,
				FieldsJSON: fieldsJSON(a.Fields),
				MS:         a.MS,
			}); err != nil {
				return 0, err
			}
		}
	}
	return id, nil
}

// fieldsJSON is the `source_runs.fields_json` projection: the contributed field
// names, never their values (the values live in tasks.metadata_json).
func fieldsJSON(fields []field.Name) string {
	if len(fields) == 0 {
		return "{}"
	}
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = string(f)
	}
	data, err := json.Marshal(map[string]any{"fields": out})
	if err != nil {
		return "{}"
	}
	return string(data)
}

// priorLedger reads the last run's landed products: the newest finished run that
// wrote something. A run that failed before it produced anything is not "the last
// ledger" — diffing against it would leave the earlier run's stale files behind.
func (r *Runner) priorLedger(ctx context.Context, taskID int64) ([]PriorRow, error) {
	runs, err := r.store.Runs(ctx, taskID)
	if err != nil {
		return nil, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].Status == tasks.RunRunning {
			continue
		}
		rows, err := r.store.FileRows(ctx, runs[i].ID)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		out := make([]PriorRow, 0, len(rows))
		for _, row := range rows {
			out = append(out, PriorRow{
				Kind:   row.Kind,
				Role:   row.Role,
				Index:  row.Idx,
				Dst:    row.Dst,
				SHA256: row.SHA256,
				Action: row.Action,
			})
		}
		return out, nil
	}
	return nil, nil
}

// seedState reconstructs the parse artifact from the task's stored files. It is the
// whole reason a rerun is fast: no walk, no rules, just the rows the fan-out wrote.
func seedState(files []tasks.TaskFile, number string) *State {
	grp := Group{}
	if len(files) > 0 {
		grp.Dir = filepath.Dir(files[0].Path)
	}
	for _, f := range files {
		ref := FileRef{
			Path: f.Path,
			Size: f.Size,
			Ext:  strings.ToLower(filepath.Ext(f.Path)),
			Kind: KindVideo,
		}
		if f.Role == tasks.RoleCompanion {
			ref.Kind = KindCompanion
			base := stemOf(f.Path)
			lang, withoutLang := splitLanguageToken(base)
			grp.Companions = append(grp.Companions, Companion{
				File:        ref,
				Stem:        withoutLang,
				Lang:        lang,
				FollowsPart: f.FollowsPart,
			})
			continue
		}
		grp.Parts = append(grp.Parts, Part{File: ref, Marker: Marker{Index: f.PartIndex}})
	}
	sort.SliceStable(grp.Parts, func(a, b int) bool { return grp.Parts[a].Index() < grp.Parts[b].Index() })
	if len(grp.Parts) > 0 {
		grp.Primary = grp.Parts[0].File
		grp.PrimaryStem = stemOf(grp.Parts[0].File.Path)
	}
	parsed := &Parsed{Items: []ParsedGroup{{
		Group:  grp,
		Number: Number{Raw: number, Normalized: number},
	}}}
	return &State{Parsed: parsed}
}

// suffixStartKey is the first step of the task-level suffix: the one after `parse`,
// or — in a pipeline without a parse step — the first step that is not batch-level.
func suffixStartKey(p config.Pipeline) string {
	for i, st := range p.Steps {
		if st.Type != config.StepParse {
			continue
		}
		if i+1 >= len(p.Steps) {
			return ""
		}
		return stepAddress(p.Steps[i+1])
	}
	for _, st := range p.Steps {
		switch st.Type {
		case config.StepScan, config.StepFilter, config.StepGroup:
			continue
		}
		return stepAddress(st)
	}
	return ""
}

// stepAddress is how a step is named to `--from` / `--until`: its own name when it
// has one (a repeated type needs it), its type otherwise.
func stepAddress(st config.Step) string {
	if st.Name != "" {
		return st.Name
	}
	return st.Type
}

// finalMetadata is the metadata the run ended with: the output plan's when there is
// one (it carries the poster crop), else the images', the scrape's.
func finalMetadata(state *State) meta.Metadata {
	switch {
	case state.Plan != nil && len(state.Plan.Items) > 0:
		return state.Plan.Items[0].Metadata
	case state.Images != nil && len(state.Images.Items) > 0:
		return state.Images.Items[0].Metadata
	case state.Scraped != nil && len(state.Scraped.Items) > 0:
		return state.Scraped.Items[0].Metadata
	default:
		return meta.Metadata{}
	}
}

// metadataJSON is the `tasks.metadata_json` payload.
func metadataJSON(state *State) string {
	data, err := json.Marshal(finalMetadata(state))
	if err != nil {
		return "{}"
	}
	return string(data)
}

// ledgerRow converts one applied write into a `file_rows` row.
func ledgerRow(row Row, stepRunID int64) tasks.FileRow {
	return tasks.FileRow{
		StepRunID: stepRunID,
		Kind:      row.Kind,
		Role:      row.Role,
		Idx:       row.Index,
		Src:       row.Src,
		Dst:       row.Dst,
		Bytes:     row.Bytes,
		SHA256:    row.SHA256,
		Action:    string(row.Action),
	}
}
