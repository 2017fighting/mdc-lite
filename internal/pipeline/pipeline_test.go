package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

const testPipeline = `name: main

steps:
  - type: scan
    with:
      roots: ["/media/from-file"]

  - type: filter
    with:
      rules:
        - name: companions
          when: {ext_in: [".srt"]}
          then: include
        - name: too-small
          when: {min_size: 100}
          then: exclude
        - name: videos
          when: {ext_in: [".mp4"]}
          then: include
        - name: everything-else
          when: {stem_matches: "^.*$"}
          then: exclude

  - type: group
    with: {}

  - type: parse
    with:
      rules:
        - {builtin: default}

  - type: output
    with: {root: "/media/library"}
`

func loadPipeline(t *testing.T, body string) config.Pipeline {
	t.Helper()
	p, err := config.ParsePipeline("/config/pipelines/main.yaml", []byte(body), config.DefaultStepRegistry(), nil)
	if err != nil {
		t.Fatalf("the test pipeline does not parse: %v", err)
	}
	return p
}

func build(t *testing.T, body string, opts Options) (*Engine, error) {
	t.Helper()
	return Build(loadPipeline(t, body), opts)
}

func TestBuild_StopsAtTheRequestedStep(t *testing.T) {
	e, err := build(t, testPipeline, Options{Until: "group"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(e.steps) != 3 {
		t.Fatalf("steps = %d, want the three batch-level steps", len(e.steps))
	}
	if got := e.steps[len(e.steps)-1].Type(); got != config.StepGroup {
		t.Errorf("last step = %q", got)
	}
}

func TestBuild_RefusesAStepWithNoImplementation(t *testing.T) {
	// Asking for the whole pipeline before the rest of the task-level suffix exists
	// has to fail at build time, naming the step, not halfway through a scan.
	_, err := build(t, testPipeline, Options{})
	if err == nil {
		t.Fatal("output has no implementation in this build")
	}
	for _, want := range []string{"step 5 (output)", "no implementation", "scan, filter, group, parse"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), "main.yaml:") {
		t.Errorf("a build error must point at the step: %v", err)
	}
}

func TestBuild_UnknownUntil(t *testing.T) {
	_, err := build(t, testPipeline, Options{Until: "scrape"})
	if err == nil || !strings.Contains(err.Error(), `no step named "scrape"`) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "scan, filter, group, parse, output") {
		t.Errorf("the error should list the steps it does have: %v", err)
	}
}

func TestBuild_UntilAcceptsAStepName(t *testing.T) {
	body := strings.Replace(testPipeline, "  - type: group\n    with: {}", "  - type: group\n    name: works\n    with: {}", 1)
	if _, err := build(t, body, Options{Until: "works"}); err != nil {
		t.Fatalf("--until takes a step's name too: %v", err)
	}
}

func TestBuild_RefusesAChainThatDoesNotConnect(t *testing.T) {
	// config's own topology check would have caught this at load time, but the
	// engine must not depend on that: it is the layer that knows which
	// implementations exist.
	p := config.Pipeline{
		ID:   "main",
		File: "main.yaml",
		Steps: []config.Step{
			{Type: config.StepScan, WithValue: &config.ScanWith{}},
			{Type: config.StepGroup, WithValue: &config.GroupWith{}},
		},
	}
	_, err := Build(p, Options{Until: "group"})
	if err == nil {
		t.Fatal("group needs Candidates and nothing produces them here")
	}
	if !strings.Contains(err.Error(), "needs Candidates") {
		t.Errorf("got %v", err)
	}
}

func TestEngineStepsMatchTheConfigContracts(t *testing.T) {
	// The artifact chain is declared twice: once as config schema (so a file is
	// refused at startup) and once by the implementations (so the engine can check
	// what it is about to run). This is the gate that keeps them agreeing.
	reg := config.DefaultStepRegistry()
	if len(constructors) == 0 {
		t.Fatal("no step is implemented")
	}
	for typ, newStep := range constructors {
		spec, ok := reg.Lookup(typ)
		if !ok {
			t.Fatalf("%s is implemented but not in the config registry", typ)
		}
		step, err := newStep(config.Step{Type: typ, WithValue: spec.NewWith()})
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if strings.Join(step.Requires(), ",") != strings.Join(spec.Requires, ",") {
			t.Errorf("%s requires %v, the config registry says %v", typ, step.Requires(), spec.Requires)
		}
		if strings.Join(step.Produces(), ",") != strings.Join(spec.Produces, ",") {
			t.Errorf("%s produces %v, the config registry says %v", typ, step.Produces(), spec.Produces)
		}
	}
}

// testTree writes the corpus the engine tests run on: one two-part work with a
// subtitle, and one file the filter keeps out.
func testTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ABC-123-cd1.mp4"), 200)
	writeFile(t, filepath.Join(root, "ABC-123-cd2.mp4"), 200)
	writeFile(t, filepath.Join(root, "ABC-123-cd1.srt"), 10)
	writeFile(t, filepath.Join(root, "notes.txt"), 10)
	return root
}

func TestRun_PrintsTheSummaryOfEveryStepItRan(t *testing.T) {
	root := testTree(t)
	e, err := build(t, testPipeline, Options{Input: Input{Roots: []string{root}}, Until: "group", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out bytes.Buffer
	res.Print(&out)
	got := out.String()

	for _, want := range []string{
		"pipeline: main",
		"until:    group   dry-run: nothing is written, nothing is stored",
		"scan:    roots=1 walked=4 files=4 skipped={}",
		"filter:  in=3 out=1 skipped={too-small: 1}",
		"group:   1 groups, 2 videos, 1 companions",
		"group 1/1: " + filepath.Join(root, "ABC-123-cd1.mp4"),
		"[cd1, confident]",
		"[cd2, confident]",
		"companion:  " + filepath.Join(root, "ABC-123-cd1.srt") + " -> part 1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestRun_PrintsEveryGroupNumberAndWhyOneNeedsAHuman(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "ABC-123-cd1.mp4"), 200)
	writeFile(t, filepath.Join(root, "ABC-123-cd2.mp4"), 200)
	writeFile(t, filepath.Join(root, "ABC-123_XYZ-456.mp4"), 200)
	writeFile(t, filepath.Join(root, "SSVR-122-cd1.mp4"), 200)
	writeFile(t, filepath.Join(root, "SSVR-123-cd2.mp4"), 200)
	writeFile(t, filepath.Join(root, "hello.mp4"), 200)
	writeFile(t, filepath.Join(root, "XYZ-999.mp4"), 200)
	writeFile(t, filepath.Join(root, "XYZ-999-U.mp4"), 200)

	e, err := build(t, testPipeline, Options{Input: Input{Roots: []string{root}}, Until: "parse", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out bytes.Buffer
	res.Print(&out)
	got := out.String()

	for _, want := range []string{
		"until:    parse   dry-run: nothing is written, nothing is stored",
		"parse:   4 groups, 6 parts, 2 resolved, 1 conflicting number (SSVR-123), 1 needs a number (no_candidate), 1 needs a number (ambiguous), 1 needs a number (duplicate_stem)",
		`number:     ABC-123 (raw "ABC-123", family generic)`,
		"number:     none (no_candidate)",
		"number:     none (ambiguous: ABC-123, XYZ-456)",
		"conflict:   SSVR-123 (another part read a different number)",
		"needs a number: duplicate_stem",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestRun_PrintsWhenThePrimaryHadNoNumber(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "SECRET-cd1.mp4"), 200)
	writeFile(t, filepath.Join(root, "SECRET-1-cd2.mp4"), 200)

	e, err := build(t, testPipeline, Options{Input: Input{Roots: []string{root}}, Until: "parse"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var out bytes.Buffer
	res.Print(&out)
	for _, want := range []string{
		"1 adopted from another part (primary had no number)",
		`number:     SECRET-001 (raw "SECRET-1", family generic; primary had no number, adopted from another part)`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
}

func TestRun_UntilFilterLeavesTheLaterArtifactsEmpty(t *testing.T) {
	root := testTree(t)
	e, err := build(t, testPipeline, Options{Input: Input{Roots: []string{root}}, Until: "filter"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.State.Candidates == nil || res.State.Groups != nil {
		t.Fatalf("stopping at filter: candidates %v, groups %v", res.State.Candidates != nil, res.State.Groups != nil)
	}
	var out bytes.Buffer
	res.Print(&out)
	if strings.Contains(out.String(), "group:") {
		t.Errorf("the group step did not run:\n%s", out.String())
	}
}

func TestRun_WritesNothingAndStoresNothing(t *testing.T) {
	root := testTree(t)
	before := snapshot(t, root)
	for _, dryRun := range []bool{true, false} {
		e, err := build(t, testPipeline, Options{Input: Input{Roots: []string{root}}, Until: "group", DryRun: dryRun})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Run(context.Background()); err != nil {
			t.Fatalf("dry-run=%v: %v", dryRun, err)
		}
		if after := snapshot(t, root); after != before {
			t.Fatalf("dry-run=%v changed the tree:\nbefore:\n%s\nafter:\n%s", dryRun, before, after)
		}
	}
}

// snapshot records what is on disk, so "wrote nothing" is a comparison rather
// than an opinion.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(path)
		b.WriteString(" ")
		if !info.IsDir() {
			b.WriteString(info.ModTime().String())
			b.WriteString(" ")
			b.WriteString(strings.Repeat("x", int(info.Size())))
		}
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
