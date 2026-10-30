package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runFixture sets up a config, one pipeline and one media directory holding a
// two-part work, and returns the arguments `mdc run` needs to look at them.
type runFixture struct {
	fixture
	media string
}

func setupRun(t *testing.T) runFixture {
	t.Helper()
	f := setup(t, "")
	media := filepath.Join(f.dir, "media")
	for name, size := range map[string]int{
		"ABC-123-cd1.mp4":  200,
		"ABC-123-cd2.mp4":  200,
		"ABC-123-cd1.srt":  10,
		"notes.txt":        10,
		"ABC-456.mp4.part": 10,
	} {
		p := filepath.Join(media, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(t, f.pipelines, "main.yaml", `steps:
  - type: scan
    with: {roots: [/media]}
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
  - type: parse
    with:
      rules:
        - {builtin: default}
  - type: group
    with: {}
  - type: output
    with: {root: /media/library}
`)
	return runFixture{fixture: f, media: media}
}

func (f runFixture) args(extra ...string) []string {
	return append([]string{
		"run",
		"--config", f.config,
		"--pipelines-dir", f.pipelines,
	}, extra...)
}

func TestRun_PrintsTheBatchPrefixSummary(t *testing.T) {
	f := setupRun(t)
	code, out, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media, "--until", "group", "--dry-run")...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	for _, want := range []string{
		"pipeline: main",
		"dry-run: nothing is written, nothing is stored",
		"scan:",
		"files=4",
		"partial: 1",
		"filter:  in=3 out=1",
		"group:   1 group, 2 videos, 1 companion",
		"ABC-123-cd1.mp4 [cd1, confident]",
		"-> part 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}

func TestRun_WritesNoFileAndNoDatabase(t *testing.T) {
	f := setupRun(t)
	before := tree(t, f.dir)
	code, out, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media, "--until", "group")...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	if after := tree(t, f.dir); after != before {
		t.Fatalf("mdc run changed the tree:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "mdc.db")); !os.IsNotExist(err) {
		t.Errorf("mdc run touched the database: %v", err)
	}
}

func TestRun_InjectedRootsBeatThePipelineFile(t *testing.T) {
	f := setupRun(t)
	code, out, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media, "--until", "scan")...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "scan:") || strings.Contains(out, "filter:") {
		t.Errorf("--until scan prints the scan and stops:\n%s", out)
	}
	// The pipeline file says /media; the injected root is the temp directory.
	if !strings.Contains(out, "roots=1") {
		t.Errorf("one root was asked for:\n%s", out)
	}
	if strings.Contains(out, "files=0") {
		t.Errorf("the injected root was not the one walked:\n%s", out)
	}
	// A root that cannot exist proves the injection is what scan reads: the file's
	// own /media would have been walkable.
	missing := filepath.Join(f.dir, "does-not-exist")
	code, _, errOut = run(t, f.args("--pipeline", "main", "--roots", missing, "--until", "scan")...)
	if code == 0 {
		t.Fatalf("the injected root must be the one used; %s does not exist", missing)
	}
	if !strings.Contains(errOut, missing) {
		t.Errorf("the error should name the missing root: %s", errOut)
	}
}

func TestRun_RootsAcceptsACommaSeparatedList(t *testing.T) {
	f := setupRun(t)
	other := filepath.Join(f.dir, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "XYZ-999.mp4"), make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media+","+other, "--until", "scan")...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "roots=2") || !strings.Contains(out, "files=5") {
		t.Errorf("both roots should have been walked:\n%s", out)
	}
}

func TestRun_FailsWhenARequiredFieldNeverArrived(t *testing.T) {
	// This pipeline has no scrape step, so no source ever supplies anything: title
	// and fanart are both absent and the default required-fields gate refuses.
	f := setupRun(t)
	code, _, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media)...)
	if code != 1 {
		t.Fatalf("exit %d, want 1: %s", code, errOut)
	}
	for _, want := range []string{"step 5 (output)", "missing required field(s)", "title", "fanart", "required_fields"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("error %q does not mention %q", errOut, want)
		}
	}
}

func TestRun_WritesTheOutputDirectoryUnlessDryRun(t *testing.T) {
	f := setupRun(t)
	library := filepath.Join(f.dir, "library")
	// A pipeline that needs no scraped field, so the whole chain can run without a
	// source: parse → output.
	write(t, f.pipelines, "main.yaml", `steps:
  - type: scan
    with: {roots: [/media]}
  - type: filter
    with:
      rules:
        - name: companions
          when: {ext_in: [".srt"]}
          then: include
        - name: videos
          when: {ext_in: [".mp4"]}
          then: include
  - type: parse
    with:
      rules:
        - {builtin: default}
  - type: group
    with: {}
  - type: output
    with:
      root: "`+library+`"
      required_fields: []
`)
	code, out, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media)...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "wrote:") {
		t.Errorf("the run should report what it wrote: %s", out)
	}
	for _, name := range []string{"ABC-123-cd1.mp4", "ABC-123-cd2.mp4", "ABC-123-cd1.srt"} {
		if _, err := os.Stat(filepath.Join(library, "ABC-123", name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRun_UntilGroupPrintsTheNumberOfEveryWork(t *testing.T) {
	f := setupRun(t)
	code, out, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media, "--until", "group", "--dry-run")...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	for _, want := range []string{
		"until:    group   dry-run: nothing is written, nothing is stored",
		"parse:   3 files, 3 parts, 3 resolved",
		"group:   1 group, 2 videos, 1 companion",
		`number:     ABC-123 (raw "ABC-123", family generic)`, // the primary's reading
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not contain %q:\n%s", want, out)
		}
	}
}

func TestRun_UntilGroupSaysWhyAFileNeedsANumber(t *testing.T) {
	f := setupRun(t)
	// A file whose name offers two numbers is not a work: it never becomes a group,
	// and `mdc run` names it as the one file waiting for a human.
	if err := os.WriteFile(filepath.Join(f.media, "XYZ-999_ABC-123.mp4"), make([]byte, 200), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media, "--until", "group", "--dry-run")...)
	if code != 0 {
		t.Fatalf("exit %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "needs a number (ambiguous: ABC-123, XYZ-999): ") {
		t.Errorf("the unresolved file must name its reason and its candidates:\n%s", out)
	}
}

func TestRun_UnknownPipeline(t *testing.T) {
	f := setupRun(t)
	code, _, errOut := run(t, f.args("--pipeline", "nope", "--roots", f.media)...)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, `no pipeline "nope"`) || !strings.Contains(errOut, "main") {
		t.Errorf("the error should name the unknown id and the known ones: %s", errOut)
	}
}

func TestRun_PipelineFlagIsRequired(t *testing.T) {
	f := setupRun(t)
	code, _, errOut := run(t, f.args("--roots", f.media)...)
	if code != 2 {
		t.Fatalf("exit %d, want the usage code", code)
	}
	if !strings.Contains(errOut, "--pipeline is required") {
		t.Errorf("got %s", errOut)
	}
}

func TestRun_RefusesABrokenPipelineDirectory(t *testing.T) {
	f := setupRun(t)
	write(t, f.pipelines, "broken.yaml", "steps:\n  - type: nope\n    with: {}\n")
	code, _, errOut := run(t, f.args("--pipeline", "main", "--roots", f.media, "--until", "group")...)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "broken.yaml") {
		t.Errorf("the broken file must be named: %s", errOut)
	}
}

// tree records a directory's contents, so "wrote nothing" is a comparison rather
// than an opinion.
func tree(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		b.WriteString(path)
		b.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
