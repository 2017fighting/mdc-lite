// Package pipeline is the engine: it runs a configured pipeline's steps over a
// trigger's input and hands the artifacts from one step to the next.
//
// The contract every step obeys is the artifact one: a step says which artifacts
// it requires and which it produces, and the engine refuses to start a sequence
// whose chain does not connect. In this build the batch-level prefix (scan →
// filter → group) and `parse` are implemented; the rest of the task-level suffix
// arrives step by step, and until it does, asking to run it is an error on
// startup rather than a surprise halfway through.
package pipeline

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// inputStep is a step the trigger's injected directories are meant for. Only
// `scan` takes them; every other step reads what scan and the steps after it
// produced.
type inputStep interface {
	setInput(Input)
}

// Input is what a trigger injects into a run: it overrides `scan.with.roots`
// and `scan.with.exclude` and nothing else. A trigger owns no directory
// semantics of its own, which is why the injection is this small.
type Input struct {
	Roots   []string
	Exclude []string
}

// Environment is what the task-level steps need from outside the pipeline file:
// the built-in sources `scrape` dispatches to, the directory `@file` NFO
// templates are read from, and the per-task image fetch budget.
type Environment struct {
	Sources      source.Registry
	TemplatesDir string
	// Images is `concurrency.images`: how many image fetches one task may have in
	// flight at once. Zero means no per-task cap (the Fetcher's global cap still
	// applies); the product always passes the configured number.
	Images int
}

// Options are the knobs `mdc run`, the preview and a monitor tick share.
type Options struct {
	Input Input
	// Until is the step to stop after, by type or by name. Empty runs the whole
	// pipeline.
	Until string
	// From is the first step to run, by type or by name. It exists for the rerun:
	// a task's files are read from `task_files` and its number is already decided,
	// so a rerun starts after `parse` with Seed carrying the parse artifact, and
	// never re-walks a directory.
	From string
	// Seed is the artifact set a From run starts from. Only the artifacts the
	// skipped steps would have produced need to be set.
	Seed *State
	// DryRun plans without applying. The write steps produce a Plan either way; the
	// caller is what decides not to apply it.
	DryRun bool
	// Env carries the sources and the templates directory.
	Env Environment
	// OnStep, when set, is called after every step with what the step did. It is
	// the seam the task runner uses to write `step_runs` one row at a time, which is
	// why it is not the engine that knows about the database.
	OnStep func(ctx context.Context, o StepOutcome) error
}

// StepOutcome is one step's result, as an observer sees it.
type StepOutcome struct {
	Idx    int
	Type   string
	Name   string
	Status string
	MS     int64
	// Summary is the step's summary projection (a *ScanSummary, *OutputSummary, …),
	// never an artifact.
	Summary any
	Err     error
}

// Step is one step the engine can run. Requires and Produces are artifact kinds
// from the config registry: they are the same strings a pipeline file is
// validated against, which is what keeps "connectivity" a single idea.
type Step interface {
	Type() string
	Name() string
	Requires() []string
	Produces() []string
	Run(ctx context.Context, st *State) error
}

// State carries the artifacts of one run. A step writes the ones it declares in
// Produces and reads the ones it declares in Requires; the engine has already
// checked that the latter exist by the time the step runs.
type State struct {
	Files      *FileSet
	Candidates *Candidates
	Groups     *Groups
	Parsed     *Parsed
	Scraped    *Scraped
	Images     *Images
	NfoDoc     *NfoDoc
	Plan       *Plan
	Manifest   *Manifest

	ScanRun   *ScanSummary
	FilterRun *FilterSummary
	GroupRun  *GroupSummary
	ParseRun  *ParseSummary
	ScrapeRun *ScrapeSummary
	ImagesRun *ImagesSummary
	NfoRun    *NfoSummary
	OutputRun *OutputSummary

	// Partial collects the reasons a step's failure did not stop the run
	// (`on_error: continue`). A run with any of them ends partial.
	Partial []string
}

// summaryFor returns the summary projection of one step type.
func (st *State) summaryFor(typ string) any {
	switch typ {
	case config.StepScan:
		return st.ScanRun
	case config.StepFilter:
		return st.FilterRun
	case config.StepGroup:
		return st.GroupRun
	case config.StepParse:
		return st.ParseRun
	case config.StepScrape:
		return st.ScrapeRun
	case config.StepImages:
		return st.ImagesRun
	case config.StepNfo:
		return st.NfoRun
	case config.StepOutput:
		return st.OutputRun
	default:
		return nil
	}
}

// Engine is a pipeline prepared to run: the steps are resolved, the stop point
// is fixed, and the artifact chain has already been checked.
type Engine struct {
	pipeline config.Pipeline
	opts     Options
	steps    []Step
	onError  []config.OnError
	// indexes is each step's 1-based position in the pipeline file, which is what
	// step_runs.idx records: a rerun that starts at step 5 still says "step 5".
	indexes []int
}

// Step constructors, one per implemented step type. This map is the whole list
// of what the binary can run today.
var constructors = map[string]func(config.Step, Options) (Step, error){
	config.StepScan: func(s config.Step, _ Options) (Step, error) {
		w, ok := s.WithValue.(*config.ScanWith)
		if !ok {
			return nil, fmt.Errorf("the scan settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newScan(*w), nil
	},
	config.StepFilter: func(s config.Step, _ Options) (Step, error) {
		w, ok := s.WithValue.(*config.FilterWith)
		if !ok {
			return nil, fmt.Errorf("the filter settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newFilter(*w)
	},
	config.StepGroup: func(s config.Step, _ Options) (Step, error) {
		w, ok := s.WithValue.(*config.GroupWith)
		if !ok {
			return nil, fmt.Errorf("the group settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newGroup(*w)
	},
	config.StepParse: func(s config.Step, _ Options) (Step, error) {
		w, ok := s.WithValue.(*config.ParseWith)
		if !ok {
			return nil, fmt.Errorf("the parse settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newParse(*w)
	},
	config.StepScrape: func(s config.Step, opts Options) (Step, error) {
		w, ok := s.WithValue.(*config.ScrapeWith)
		if !ok {
			return nil, fmt.Errorf("the scrape settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newScrape(*w, opts.Env.Sources, opts.Env.Images)
	},
	config.StepImages: func(s config.Step, _ Options) (Step, error) {
		w, ok := s.WithValue.(*config.ImagesWith)
		if !ok {
			return nil, fmt.Errorf("the images settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newImages(*w)
	},
	config.StepNfo: func(s config.Step, opts Options) (Step, error) {
		w, ok := s.WithValue.(*config.NfoWith)
		if !ok {
			return nil, fmt.Errorf("the nfo settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newNfo(*w, opts.Env.TemplatesDir)
	},
	config.StepOutput: func(s config.Step, opts Options) (Step, error) {
		w, ok := s.WithValue.(*config.OutputWith)
		if !ok {
			return nil, fmt.Errorf("the output settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newOutput(*w, s.OnError)
	},
}

// Build prepares a run of p. It fails, before anything touches the filesystem,
// on every reason a run cannot complete: an unknown `--until`, a step that has
// no implementation in this build, or a chain that does not connect.
func Build(p config.Pipeline, opts Options) (*Engine, error) {
	stop, err := stopIndex(p, opts.Until)
	if err != nil {
		return nil, err
	}
	start := 0
	if opts.From != "" {
		if start, err = startIndex(p, opts.From); err != nil {
			return nil, err
		}
		if start > stop {
			return nil, fmt.Errorf("%s: --from %q comes after --until %q", p.File, opts.From, opts.Until)
		}
		if opts.Seed == nil {
			return nil, fmt.Errorf("%s: starting at %q needs the artifacts of the steps before it", p.File, opts.From)
		}
	}
	e := &Engine{pipeline: p, opts: opts, onError: make([]config.OnError, 0, stop-start+1), indexes: make([]int, 0, stop-start+1)}
	available := map[string]bool{}
	for i := 0; i <= stop; i++ {
		st := p.Steps[i]
		newStep, ok := constructors[st.Type]
		if !ok {
			return nil, stepError(p, i, "%q has no implementation in this build yet (this build runs: %s)",
				st.Type, strings.Join(implementedTypes(), ", "))
		}
		step, err := newStep(st, opts)
		if err != nil {
			return nil, stepError(p, i, "%v", err)
		}
		if target, ok := step.(inputStep); ok {
			target.setInput(opts.Input)
		}
		for _, need := range step.Requires() {
			if !available[need] {
				return nil, stepError(p, i, "needs %s, which no earlier step produces (steps before it: %s)",
					need, describeSteps(p.Steps[:i]))
			}
		}
		for _, has := range step.Produces() {
			available[has] = true
		}
		if i < start {
			// The artifact is supplied by the runner instead of produced: it comes
			// out of the database, not out of the directory walk.
			continue
		}
		e.onError = append(e.onError, st.OnError)
		e.indexes = append(e.indexes, i+1)
		e.steps = append(e.steps, step)
	}
	return e, nil
}

// Run executes the steps up to and including the stop point and returns what
// they produced. It writes no files and touches no database: every artifact this
// build produces is in memory.
func (e *Engine) Run(ctx context.Context) (*Result, error) {
	state := &State{}
	if e.opts.Seed != nil {
		state = e.opts.Seed
	}
	res := &Result{
		Pipeline: e.pipeline.ID,
		File:     e.pipeline.File,
		Until:    runKey(e.steps[len(e.steps)-1]),
		DryRun:   e.opts.DryRun,
		State:    state,
	}
	for i, step := range e.steps {
		// e.indexes is the step's position in the pipeline file, not its position in
		// this run: a task-level rerun starts at `parse`, so everything a message
		// says about "which step" has to come from the pipeline, never from the loop.
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s: stopped before step %d (%s): %w", e.pipeline.ID, e.indexes[i], step.Type(), err)
		}
		started := time.Now()
		runErr := step.Run(ctx, state)
		o := StepOutcome{
			Idx:     e.indexes[i],
			Type:    step.Type(),
			Name:    step.Name(),
			Status:  "ok",
			MS:      time.Since(started).Milliseconds(),
			Summary: state.summaryFor(step.Type()),
			Err:     runErr,
		}
		if runErr != nil {
			o.Status = "failed"
			if e.onError[i] == config.OnErrorContinue {
				// `on_error: continue`: the run goes on, and its product is partial.
				// The step's failure is recorded, not swallowed.
				o.Status = "partial"
				state.Partial = append(state.Partial, fmt.Sprintf("%s: %v", step.Type(), runErr))
			}
		}
		if e.opts.OnStep != nil {
			if err := e.opts.OnStep(ctx, o); err != nil {
				return nil, err
			}
		}
		if runErr != nil && e.onError[i] != config.OnErrorContinue {
			// The step that failed is the one `e.indexes` names: indexing the loop here
			// would blame whichever step happens to sit at that offset in the file, and
			// a rerun that starts at `parse` would report a `parse: missing required
			// field(s)` that no reader could make sense of.
			return nil, stepError(e.pipeline, e.indexes[i]-1, "%v", runErr)
		}
	}
	// The seed is the run's state object, so everything a step wrote is already
	// visible through res.State: there is nothing to copy back.
	return res, nil
}

// Result is one run's outcome, ready to be printed.
type Result struct {
	Pipeline string
	File     string
	Until    string
	DryRun   bool
	State    *State
}

// stopIndex turns `--until` into the index of the last step to run. A type that
// appears more than once stops at its first occurrence, which is the only
// reading that does not need the caller to know about the second one.
func stopIndex(p config.Pipeline, until string) (int, error) {
	if len(p.Steps) == 0 {
		return 0, fmt.Errorf("%s: the pipeline has no steps", p.File)
	}
	if until == "" {
		return len(p.Steps) - 1, nil
	}
	for i, st := range p.Steps {
		if st.Type == until || (st.Name != "" && st.Name == until) {
			return i, nil
		}
	}
	keys := make([]string, len(p.Steps))
	for i, st := range p.Steps {
		keys[i] = stepKey(st)
	}
	return 0, fmt.Errorf("%s: no step named %q (steps: %s)", p.File, until, strings.Join(keys, ", "))
}

// DryRun reports whether this engine writes anything. The write steps always plan;
// the caller is what decides not to apply.
func (e *Engine) DryRun() bool { return e.opts.DryRun }

// startIndex turns `--from` into the index of the first step to run.
func startIndex(p config.Pipeline, from string) (int, error) {
	for i, st := range p.Steps {
		if st.Type == from || (st.Name != "" && st.Name == from) {
			return i, nil
		}
	}
	keys := make([]string, len(p.Steps))
	for i, st := range p.Steps {
		keys[i] = stepKey(st)
	}
	return 0, fmt.Errorf("%s: no step named %q (steps: %s)", p.File, from, strings.Join(keys, ", "))
}

// runKey is the same key for an engine step.
func runKey(s Step) string {
	if s.Name() != "" {
		return s.Type() + "(" + s.Name() + ")"
	}
	return s.Type()
}

// stepKey is how a step is addressed in a message: its type, plus the name when
// it has one because the type alone would be ambiguous.
func stepKey(st config.Step) string {
	if st.Name != "" {
		return st.Type + "(" + st.Name + ")"
	}
	return st.Type
}

// stepOrder is the spec's step order, so "what this build can run" is listed the
// way the pipeline file reads.
var stepOrder = []string{
	config.StepScan, config.StepFilter, config.StepGroup, config.StepParse,
	config.StepScrape, config.StepImages, config.StepNfo, config.StepOutput,
}

func implementedTypes() []string {
	var out []string
	for _, typ := range stepOrder {
		if _, ok := constructors[typ]; ok {
			out = append(out, typ)
		}
	}
	return out
}

func describeSteps(steps []config.Step) string {
	if len(steps) == 0 {
		return "none"
	}
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = stepKey(s)
	}
	return strings.Join(out, ", ")
}

// stepError renders the same shape the config loader uses, so a run-time refusal
// points at the same place a config refusal does.
func stepError(p config.Pipeline, idx int, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	st := p.Steps[idx]
	if st.Line == 0 {
		return fmt.Errorf("%s: step %d (%s): %s", p.File, idx+1, st.Type, msg)
	}
	return fmt.Errorf("%s:%d:%d: step %d (%s): %s", p.File, st.Line, st.Column, idx+1, st.Type, msg)
}

// Print writes the run the way `mdc run --until <step> --dry-run` shows it: the
// summary of each step that ran, then the groups themselves, so a wrong rule is
// visible in the output rather than inferred from a count.
func (r *Result) Print(w io.Writer) {
	fmt.Fprintf(w, "pipeline: %s (%s)\n", r.Pipeline, r.File)
	if r.DryRun {
		fmt.Fprintf(w, "until:    %s   dry-run: nothing is written, nothing is stored\n\n", r.Until)
	} else {
		fmt.Fprintf(w, "until:    %s\n\n", r.Until)
	}
	st := r.State
	if st.ScanRun != nil {
		fmt.Fprintf(w, "scan:    roots=%d walked=%d files=%d skipped=%s\n",
			st.ScanRun.Roots, st.ScanRun.Walked, st.ScanRun.Files, counts(st.ScanRun.Skipped))
	}
	if st.FilterRun != nil {
		fmt.Fprintf(w, "filter:  in=%d out=%d skipped=%s\n",
			st.FilterRun.In, st.FilterRun.Out, counts(st.FilterRun.Skipped))
	}
	if st.GroupRun != nil {
		fmt.Fprintf(w, "group:   %s\n", st.GroupRun.String())
	}
	if st.ParseRun != nil {
		fmt.Fprintf(w, "parse:   %s\n", st.ParseRun.String())
	}
	if st.ScrapeRun != nil {
		fmt.Fprintf(w, "scrape:  %s\n", st.ScrapeRun.String())
	}
	if st.ImagesRun != nil {
		fmt.Fprintf(w, "images:  %s\n", st.ImagesRun.String())
	}
	if st.NfoRun != nil {
		fmt.Fprintf(w, "nfo:     %s\n", st.NfoRun.String())
	}
	if st.OutputRun != nil {
		fmt.Fprintf(w, "output:  %s\n", st.OutputRun.String())
	}
	for _, reason := range st.Partial {
		fmt.Fprintf(w, "partial: %s\n", reason)
	}
	if st.Groups != nil {
		fmt.Fprintln(w)
		writeGroups(w, st.Groups, st.Parsed)
	}
}

// PrintManifest reports what applying the plan did. It is the `mdc run` view of the
// ledger a task run would have written.
func (r *Result) PrintManifest(w io.Writer, m *Manifest) {
	if m == nil {
		return
	}
	counts := map[Action]int{}
	for _, row := range m.Rows {
		counts[row.Action]++
	}
	fmt.Fprintf(w, "\nwrote:   %d created, %d overwritten, %d skipped\n",
		counts[ActionCreated], counts[ActionOverwritten], counts[ActionSkipped])
	for _, row := range m.Rows {
		fmt.Fprintf(w, "  %-10s %s\n", row.Action, row.Dst)
	}
}

func writeGroups(w io.Writer, g *Groups, parsed *Parsed) {
	resolved, unresolved := indexParsed(parsed)
	for i, grp := range g.Groups {
		fmt.Fprintf(w, "group %d/%d: %s (primary, %s)%s\n", i+1, len(g.Groups), grp.Primary.Path, grp.PrimaryStem, mergedNote(grp))
		parts := make([]string, len(grp.Parts))
		for j, p := range grp.Parts {
			parts[j] = fmt.Sprintf("%s [%s]", p.File.Path, describeMarker(p.Marker))
		}
		fmt.Fprintf(w, "  parts:      %s\n", strings.Join(parts, " | "))
		if item, ok := resolved[grp.Primary.Path]; ok {
			note := ""
			if item.PrimaryFailed {
				note = "; primary had no number, adopted from another part"
			}
			fmt.Fprintf(w, "  number:     %s (raw %q, family %s%s)\n",
				item.Number.Normalized, item.Number.Raw, item.Number.Family, note)
			if extra := conflictsOf(item); len(extra) > 0 {
				fmt.Fprintf(w, "  conflict:   %s (another part read a different number)\n", strings.Join(extra, ", "))
			}
		} else if u, ok := unresolved[grp.Primary.Path]; ok {
			fmt.Fprintf(w, "  number:     none (%s)\n", describeUnresolved(u))
		}
		for _, c := range grp.Companions {
			follows := "primary"
			if c.FollowsPart > 0 {
				follows = fmt.Sprintf("part %d", c.FollowsPart)
			}
			fmt.Fprintf(w, "  companion:  %s -> %s\n", c.File.Path, follows)
		}
	}
	for _, d := range g.Duplicates {
		names := make([]string, len(d.Files))
		for i, f := range d.Files {
			names[i] = f.Path
		}
		note := "a human decides"
		if parsed != nil {
			note = "needs a number: " + ReasonDuplicateStem
		}
		fmt.Fprintf(w, "duplicate stem: %s/%s (%d files, %s): %s\n",
			d.Dir, d.Stem, len(d.Files), note, strings.Join(names, ", "))
	}
	for _, warn := range g.Warnings {
		fmt.Fprintf(w, "like parts, not merged: %s ~ %s\n", warn.A.Path, warn.B.Path)
	}
	for _, u := range g.Unmatched {
		fmt.Fprintf(w, "unmatched companion (%s): %s\n", u.Reason, u.File.Path)
	}
	if len(g.Groups) > 0 {
		fmt.Fprintln(w)
	}
}

// indexParsed makes the parse result readable by the group it belongs to. A
// resolved group is keyed by its primary path, and so is an unresolved one —
// the primary is the first file in its list.
func indexParsed(parsed *Parsed) (map[string]ParsedGroup, map[string]Unresolved) {
	resolved := map[string]ParsedGroup{}
	unresolved := map[string]Unresolved{}
	if parsed == nil {
		return resolved, unresolved
	}
	for _, item := range parsed.Items {
		resolved[item.Group.Primary.Path] = item
	}
	for _, u := range parsed.Unresolved {
		if len(u.Files) > 0 {
			unresolved[u.Files[0].Path] = u
		}
	}
	return resolved, unresolved
}

// describeUnresolved renders why a group needs a human, with the candidates when
// there are any to offer.
func describeUnresolved(u Unresolved) string {
	if len(u.Candidates) == 0 {
		return u.Reason
	}
	return u.Reason + ": " + strings.Join(u.Candidates, ", ")
}

func mergedNote(grp Group) string {
	if grp.DistanceMerged {
		return ", merged by stem distance"
	}
	return ""
}

func describeMarker(m Marker) string {
	switch {
	case m.Text == "":
		return "no marker"
	case m.Confident:
		return m.Text + ", confident"
	default:
		return m.Text + ", conditional"
	}
}

// counts renders a set of reasons as `{a: 1, b: 2}` with the keys sorted, so two
// runs of the same tree print the same bytes.
func counts(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k, v := range m {
		if v > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s: %d", k, m[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// ScanSummary is the scan step's summary projection: how much was walked, how
// much came out, and why the rest did not.
type ScanSummary struct {
	Roots   int            `json:"roots"`
	Walked  int            `json:"walked"`
	Files   int            `json:"files"`
	Skipped map[string]int `json:"skipped"`
}

// FilterSummary is the filter step's summary projection. Skipped is keyed by
// rule name — that is what the rule's name is for.
type FilterSummary struct {
	In      int            `json:"in"`
	Out     int            `json:"out"`
	Skipped map[string]int `json:"skipped"`
}

// GroupSummary is the group step's summary projection.
type GroupSummary struct {
	Groups         int `json:"groups"`
	Videos         int `json:"videos"`
	Companions     int `json:"companions"`
	DuplicateStems int `json:"duplicate_stems"`
	DuplicateFiles int `json:"duplicate_files"`
	Unmatched      int `json:"unmatched"`
	Warnings       int `json:"warnings"`
}

// String renders the group summary as one line.
func (s GroupSummary) String() string {
	parts := []string{
		fmt.Sprintf("%d groups", s.Groups),
		fmt.Sprintf("%d videos", s.Videos),
		fmt.Sprintf("%d companions", s.Companions),
	}
	if s.DuplicateStems > 0 {
		parts = append(parts, fmt.Sprintf("%d duplicate stem(s) (%d files, needs a human)", s.DuplicateStems, s.DuplicateFiles))
	}
	if s.Unmatched > 0 {
		parts = append(parts, fmt.Sprintf("%d unmatched companion(s)", s.Unmatched))
	}
	if s.Warnings > 0 {
		parts = append(parts, fmt.Sprintf("%d look-like-parts warning(s)", s.Warnings))
	}
	return strings.Join(parts, ", ")
}

// ParseSummary is the parse step's summary projection: what got a number, what
// needs a human, and where the group's parts disagreed. Conflicts is a list of
// numbers rather than a count because the point of recording one is to show it.
type ParseSummary struct {
	Groups   int `json:"groups"`
	Parts    int `json:"parts"`
	Resolved int `json:"resolved"`
	// Unresolved is keyed by reason (no_candidate / ambiguous / duplicate_stem).
	Unresolved map[string]int `json:"unresolved"`
	// Conflicts is the sorted, distinct set of numbers other parts read that the
	// decided number was not.
	Conflicts     []string `json:"conflicts"`
	PrimaryFailed int      `json:"primary_failed"`
	// CandidateNumbers is the per-file chip list the needs-number panel shows: path
	// → the numbers that file's own stem offered. The primary also gets the group's
	// union when its own text offered nothing, so a human has something to click.
	//
	// The JSON key is `candidates` because that is what the API's groups and task
	// files call the chips; note that it is a different sense of the word than the
	// filter's Candidates, which are the files that entered processing.
	//
	// It is a projection of the unresolved sets only — a resolved group already has
	// its answer — and it stops after maxCandidateFiles entries, because a summary
	// that grows with the batch would be an artifact, not a projection.
	CandidateNumbers map[string][]string `json:"candidates"`
	// conflicts accumulates the set while the step runs; Conflicts is its sorted
	// projection, so the summary is stable across runs.
	conflicts map[string]bool
}

// maxCandidateFiles bounds how many files one parse summary records candidates
// for. A batch that needs more than this is not waiting for a human to click
// chips: the panel would be unusable long before the cap.
const maxCandidateFiles = 200

// String renders the parse summary as one line.
func (s ParseSummary) String() string {
	parts := []string{
		plural(s.Groups, "group", "groups"),
		plural(s.Parts, "part", "parts"),
		fmt.Sprintf("%d resolved", s.Resolved),
	}
	if n := len(s.Conflicts); n > 0 {
		// Conflicts is the distinct set of numbers, not a per-group tally, so the
		// count is of numbers that disagreed.
		parts = append(parts, plural(n, "conflicting number", "conflicting numbers")+
			" ("+strings.Join(s.Conflicts, ", ")+")")
	}
	if s.PrimaryFailed > 0 {
		parts = append(parts, fmt.Sprintf("%d adopted from another part (primary had no number)", s.PrimaryFailed))
	}
	for _, reason := range []string{ReasonNoCandidate, ReasonAmbiguous, ReasonDuplicateStem} {
		if s.Unresolved[reason] > 0 {
			parts = append(parts, fmt.Sprintf("%d needs a number (%s)", s.Unresolved[reason], reason))
		}
	}
	return strings.Join(parts, ", ")
}

// plural renders a count with its noun, singular when the count is one, so a line
// does not mix "1 groups" with "1 conflicting number".
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// newCounts returns an initialised reason counter: a nil map is how a summary
// ends up lying about having skipped nothing.
func newCounts() map[string]int { return map[string]int{} }
