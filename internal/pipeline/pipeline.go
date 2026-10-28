// Package pipeline is the engine: it runs a configured pipeline's steps over a
// trigger's input and hands the artifacts from one step to the next.
//
// The contract every step obeys is the artifact one: a step says which artifacts
// it requires and which it produces, and the engine refuses to start a sequence
// whose chain does not connect. In this build the batch-level prefix (scan →
// filter → group) is implemented; the task-level suffix arrives step by step,
// and until it does, asking to run it is an error on startup rather than a
// surprise halfway through.
package pipeline

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/2017fighting/mdc-lite/internal/config"
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

// Options are the knobs `mdc run`, the preview and a monitor tick share.
type Options struct {
	Input Input
	// Until is the step to stop after, by type or by name. Empty runs the whole
	// pipeline.
	Until string
	// DryRun plans without applying. The batch-level prefix writes nothing, so on
	// its own it changes nothing yet; it is plumbed through so that the task-level
	// steps can honour it without a second flag appearing.
	DryRun bool
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

	ScanRun   *ScanSummary
	FilterRun *FilterSummary
	GroupRun  *GroupSummary
}

// Engine is a pipeline prepared to run: the steps are resolved, the stop point
// is fixed, and the artifact chain has already been checked.
type Engine struct {
	pipeline config.Pipeline
	opts     Options
	steps    []Step
}

// Step constructors, one per implemented step type. This map is the whole list
// of what the binary can run today.
var constructors = map[string]func(config.Step) (Step, error){
	config.StepScan: func(s config.Step) (Step, error) {
		w, ok := s.WithValue.(*config.ScanWith)
		if !ok {
			return nil, fmt.Errorf("the scan settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newScan(*w), nil
	},
	config.StepFilter: func(s config.Step) (Step, error) {
		w, ok := s.WithValue.(*config.FilterWith)
		if !ok {
			return nil, fmt.Errorf("the filter settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newFilter(*w)
	},
	config.StepGroup: func(s config.Step) (Step, error) {
		w, ok := s.WithValue.(*config.GroupWith)
		if !ok {
			return nil, fmt.Errorf("the group settings were not decoded (was the pipeline loaded through config.LoadPipelineFile?)")
		}
		return newGroup(*w)
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
	e := &Engine{pipeline: p, opts: opts}
	available := map[string]bool{}
	for i := 0; i <= stop; i++ {
		st := p.Steps[i]
		newStep, ok := constructors[st.Type]
		if !ok {
			return nil, stepError(p, i, "%q has no implementation in this build yet (this build runs the batch-level prefix: %s)",
				st.Type, strings.Join(implementedTypes(), ", "))
		}
		step, err := newStep(st)
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
		e.steps = append(e.steps, step)
	}
	return e, nil
}

// Run executes the steps up to and including the stop point and returns what
// they produced. It writes no files and touches no database: every artifact this
// build produces is in memory.
func (e *Engine) Run(ctx context.Context) (*Result, error) {
	res := &Result{
		Pipeline: e.pipeline.ID,
		File:     e.pipeline.File,
		Until:    runKey(e.steps[len(e.steps)-1]),
		DryRun:   e.opts.DryRun,
		State:    &State{},
	}
	for i, step := range e.steps {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%s: stopped before step %d (%s): %w", e.pipeline.ID, i+1, step.Type(), err)
		}
		if err := step.Run(ctx, res.State); err != nil {
			return nil, stepError(e.pipeline, i, "%v", err)
		}
	}
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
		fmt.Fprintf(w, "group:   %s\n\n", st.GroupRun.String())
	}
	if st.Groups != nil {
		writeGroups(w, st.Groups)
	}
}

func writeGroups(w io.Writer, g *Groups) {
	for i, grp := range g.Groups {
		fmt.Fprintf(w, "group %d/%d: %s (primary, %s)%s\n", i+1, len(g.Groups), grp.Primary.Path, grp.PrimaryStem, mergedNote(grp))
		parts := make([]string, len(grp.Parts))
		for j, p := range grp.Parts {
			parts[j] = fmt.Sprintf("%s [%s]", p.File.Path, describeMarker(p.Marker))
		}
		fmt.Fprintf(w, "  parts:      %s\n", strings.Join(parts, " | "))
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
		fmt.Fprintf(w, "duplicate stem: %s/%s (%d files, a human decides): %s\n",
			d.Dir, d.Stem, len(d.Files), strings.Join(names, ", "))
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
	Roots   int
	Walked  int
	Files   int
	Skipped map[string]int
}

// FilterSummary is the filter step's summary projection. Skipped is keyed by
// rule name — that is what the rule's name is for.
type FilterSummary struct {
	In      int
	Out     int
	Skipped map[string]int
}

// GroupSummary is the group step's summary projection.
type GroupSummary struct {
	Groups         int
	Videos         int
	Companions     int
	DuplicateStems int
	DuplicateFiles int
	Unmatched      int
	Warnings       int
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

// newCounts returns an initialised reason counter: a nil map is how a summary
// ends up lying about having skipped nothing.
func newCounts() map[string]int { return map[string]int{} }
