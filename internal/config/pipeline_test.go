package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/config"
)

// head is a pipeline's first step: a scan of one root.
const head = "steps:\n  - type: scan\n    with: {roots: [/a]}\n"

// tail is the shortest legal path from a scan to a write: filter → group →
// parse → output. Every optional step (scrape, images, nfo) is left out on
// purpose, which is itself one of the rules under test.
const filterGroup = `  - type: filter
    with:
      rules:
        - name: all
          when: {ext_in: [.mp4]}
          then: include
  - type: group
    with: {max_distance: 2, max_files_for_levenshtein: 50}
`

// afterGroup is the task-level suffix with every optional step left out.
const afterGroup = `  - type: parse
    with: {}
  - type: output
    with: {root: /o}
`

const tail = filterGroup + afterGroup

func parse(t *testing.T, body string) (config.Pipeline, error) {
	t.Helper()
	return config.ParsePipeline("/config/pipelines/main.yaml", []byte(body), config.DefaultStepRegistry(), env(nil))
}

// validPipeline is the smallest pipeline that satisfies every rule.
const validPipeline = head + tail

func TestParsePipeline_AcceptsValid(t *testing.T) {
	p, err := parse(t, validPipeline)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.ID != "main" {
		t.Errorf("id = %q, want the file name stem", p.ID)
	}
	if p.Name != "main" {
		t.Errorf("name = %q", p.Name)
	}
	if len(p.Steps) != 5 {
		t.Fatalf("steps = %d", len(p.Steps))
	}
	if p.Steps[0].OnError != config.OnErrorFail {
		t.Errorf("on_error default = %q, want fail", p.Steps[0].OnError)
	}
}

func TestParsePipeline_NameDefaultsToID(t *testing.T) {
	p, err := parse(t, validPipeline)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "main" {
		t.Errorf("name = %q, want the id", p.Name)
	}
}

func TestParsePipeline_UnknownTopLevelKey(t *testing.T) {
	_, err := parse(t, "id: main\n"+validPipeline)
	if err == nil {
		t.Fatal("an `id` key must be refused: the id is the file name")
	}
	if !strings.Contains(err.Error(), ":1:1:") || !strings.Contains(err.Error(), "id") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_UnknownStepType(t *testing.T) {
	_, err := parse(t, "steps:\n  - type: scna\n    with: {roots: [/a]}\n"+tail)
	if err == nil {
		t.Fatal("an unknown step type must be refused")
	}
	if !strings.Contains(err.Error(), "step 1 (scna)") || !strings.Contains(err.Error(), "unknown step type") {
		t.Errorf("got: %v", err)
	}
	// Line 2, column 5: the step's own mapping, not the file.
	if !strings.Contains(err.Error(), "main.yaml:2:5:") {
		t.Errorf("the error must carry file:line:col of the step: got %v", err)
	}
}

func TestParsePipeline_UnknownKeyInsideWith(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with:
      roots: [/media]
      extensoins: [.mp4]
`+tail)
	if err == nil {
		t.Fatal("a typo inside `with` must be refused, not silently swallowed")
	}
	if !strings.Contains(err.Error(), `unknown field "extensoins"`) {
		t.Errorf("got: %v", err)
	}
	// The key sits on line 5, column 7 of the document.
	if !strings.Contains(err.Error(), "main.yaml:5:7:") {
		t.Errorf("the error must point at the offending key, got %v", err)
	}
	if !strings.Contains(err.Error(), "step 1 (scan)") {
		t.Errorf("the error must name the step index and type, got %v", err)
	}
}

func TestParsePipeline_UnknownKeyInsideNestedWith(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: filter
    with:
      rules:
        - name: a
          when:
            ext_in: [.mp4]
            mni_size: 10
          then: include
`+afterGroup)
	if err == nil {
		t.Fatal("a typo nested two levels inside `with` must be refused")
	}
	if !strings.Contains(err.Error(), "mni_size") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_UnknownStepMetadataKey(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
    enabled: false
`+tail)
	if err == nil {
		t.Fatal("there is no `enabled` key on a step")
	}
	if !strings.Contains(err.Error(), "enabled") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_OnErrorEnum(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
    on_error: ignore
`+tail)
	if err == nil {
		t.Fatal("on_error must be an enum")
	}
	if !strings.Contains(err.Error(), "on_error") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_OnErrorContinueIsAccepted(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
    on_error: continue
`+tail)
	if err != nil {
		t.Fatalf("on_error: continue is legal: %v", err)
	}
}

func TestParsePipeline_DuplicateTypeNeedsName(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: scan
    with: {roots: [/b]}
`+tail)
	if err == nil {
		t.Fatal("the same type twice without a name must be refused")
	}
	if !strings.Contains(err.Error(), "needs a `name`") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_DuplicateTypeWithNamesIsFine(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    name: first
    with: {roots: [/a]}
  - type: scan
    name: second
    with: {roots: [/b]}
`+tail)
	if err != nil {
		t.Fatalf("named duplicates are legal: %v", err)
	}
}

func TestParsePipeline_TopologyNotConnected(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: parse
    with: {}
  - type: output
    with: {root: /o}
`)
	if err == nil {
		t.Fatal("parse needs Groups; nothing produces them here")
	}
	if !strings.Contains(err.Error(), "step 2 (parse)") || !strings.Contains(err.Error(), "Groups") {
		t.Errorf("got: %v", err)
	}
	if !strings.Contains(err.Error(), "main.yaml:4:5:") {
		t.Errorf("a topology error must also point at the step: got %v", err)
	}
}

func TestParsePipeline_NoWriteStep(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: filter
    with:
      rules:
        - name: a
          when: {ext_in: [.mp4]}
          then: include
`)
	if err == nil {
		t.Fatal("a pipeline with no output step must be refused")
	}
	if !strings.Contains(err.Error(), "writes to disk") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_OutputRootIsRequired(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: parse
    with: {}
  - type: output
    with: {}
`)
	if err == nil {
		t.Fatal("output.with.root has no default and must be required")
	}
	if !strings.Contains(err.Error(), "root is required") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_OptionalStepsMayBeOmitted(t *testing.T) {
	// tail already omits scrape, images and nfo.
	if _, err := parse(t, validPipeline); err != nil {
		t.Fatalf("filter, group, scrape, images and nfo are all optional: %v", err)
	}
}

func TestParsePipeline_FilterRulesValidated(t *testing.T) {
	for _, tc := range []struct{ name, rules, want string }{
		{"empty", `[]`, "must not be empty"},
		{"then enum", `[{name: a, when: {ext_in: [.mp4]}, then: maybe}]`, "then"},
		{"name required", `[{when: {ext_in: [.mp4]}, then: include}]`, "name is required"},
		{"name unique", `[{name: a, when: {ext_in: [.mp4]}, then: include}, {name: a, when: {ext_in: [.mkv]}, then: exclude}]`, "used twice"},
		{"when needs a key", `[{name: a, when: {}, then: include}]`, "needs one of"},
		{"when one key", `[{name: a, when: {ext_in: [.mp4], min_size: 5}, then: include}]`, "more than one"},
		{"regex", `[{name: a, when: {stem_matches: "["}, then: include}]`, "stem_matches"},
		{"extension shape", `[{name: a, when: {ext_in: [mp4]}, then: include}]`, "must look like"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, "steps:\n  - type: scan\n    with: {roots: [/a]}\n  - type: filter\n    with:\n      rules: "+tc.rules+"\n"+afterGroup)
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParsePipeline_OutputRequiredFieldsMustBeRealFields(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: parse
    with: {}
  - type: output
    with:
      root: /o
      required_fields: [title, fanart, tittle]
`)
	if err == nil {
		t.Fatal("an unknown required field must be refused at startup")
	}
	if !strings.Contains(err.Error(), "tittle") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_TemplateVariablesChecked(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: parse
    with: {}
  - type: output
    with:
      root: /o
      dir_template: "{{ number }}"
      file_template: "{{ number }}-cd{{ part }}{{ filename }}"
`)
	if err == nil {
		t.Fatal("a template naming an unknown variable must be refused")
	}
	if !strings.Contains(err.Error(), "filename") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_LinkEnum(t *testing.T) {
	_, err := parse(t, `steps:
  - type: scan
    with: {roots: [/a]}
  - type: parse
    with: {}
  - type: output
    with: {root: /o, link: symlink}
`)
	if err == nil {
		t.Fatal("link must be an enum of hardlink/copy/move")
	}
}

func TestParsePipeline_TemplateSelector(t *testing.T) {
	for _, tc := range []struct{ sel, want string }{
		{"@my.nfo", ""},
		{"@../escape.nfo", "`..`"},
		{"@/etc/passwd", "absolute"},
		{"default", ""},
		{"{{ number }}", ""},
		{"mine", "not a builtin name"},
	} {
		body := head + filterGroup +
			"  - type: parse\n    with: {}\n" +
			"  - type: scrape\n    with: {sources: [javdb]}\n" +
			"  - type: nfo\n    with:\n      template: \"" + tc.sel + "\"\n" +
			"  - type: output\n    with: {root: /o}\n"
		_, err := parse(t, body)
		if tc.want == "" {
			if err != nil {
				t.Errorf("template %q should be accepted: %v", tc.sel, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("template %q: got %v, want mention of %q", tc.sel, err, tc.want)
		}
	}
}

func TestParsePipeline_AnchorCarrierMustBeASchemaKey(t *testing.T) {
	_, err := parse(t, `defaults: &step
  type: scan
  with: {roots: [/media]}
`+validPipeline)
	if err == nil {
		t.Fatal("`defaults` is not a top-level pipeline key, so it cannot carry an anchor")
	}
}

func TestParsePipeline_YAMLAnchorsAndMergeAreAllowed(t *testing.T) {
	_, err := parse(t, `name: anchored
steps:
  - &first
    type: scan
    name: first scan
    with: {roots: [/media]}
  - <<: *first
    name: second scan
    with: {roots: [/media/b]}
`+tail)
	if err != nil {
		t.Fatalf("merge keys inside steps are allowed: %v", err)
	}
}

func TestParsePipeline_MergeKeyDoesNotHideUnknownKeys(t *testing.T) {
	_, err := parse(t, `name: anchored
steps:
  - &first
    type: scan
    name: first scan
    with: {roots: [/media], bogus: 1}
  - <<: *first
    name: second scan
`+tail)
	if err == nil {
		t.Fatal("the anchored mapping is validated where it is written")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("got: %v", err)
	}
}

func TestParsePipeline_EnvInterpolation(t *testing.T) {
	p, err := config.ParsePipeline("/config/pipelines/main.yaml", []byte(`steps:
  - type: scan
    with: {roots: ["${MDC_TEST_ROOT}"]}
`+tail), config.DefaultStepRegistry(), env(map[string]string{"MDC_TEST_ROOT": "/media/x"}))
	if err != nil {
		t.Fatal(err)
	}
	var w config.ScanWith
	if err := p.Steps[0].With.Decode(&w); err != nil {
		t.Fatal(err)
	}
	if len(w.Roots) != 1 || w.Roots[0] != "/media/x" {
		t.Errorf("roots = %v", w.Roots)
	}
}

func TestParsePipeline_MissingEnvVarInPipelineIsRefused(t *testing.T) {
	_, err := config.ParsePipeline("/config/pipelines/main.yaml", []byte(`steps:
  - type: scan
    with: {roots: ["${MDC_DEFINITELY_UNSET}"]}
`+tail), config.DefaultStepRegistry(), env(nil))
	if err == nil {
		t.Fatal("a missing ${VAR} must be an error")
	}
	if !strings.Contains(err.Error(), "MDC_DEFINITELY_UNSET") {
		t.Errorf("got: %v", err)
	}
}

func TestLoadPipelines_OnlyTopLevelYaml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.yaml", validPipeline)
	writeFile(t, dir, "second.yml", validPipeline)
	writeFile(t, dir, "disabled.yaml.bak", "not yaml at all: [")
	writeFile(t, dir, "notes.txt", "ignore me")
	writeFile(t, dir, "nested/deep.yaml", "not: [valid")

	ps, err := config.LoadPipelines(dir, config.DefaultStepRegistry(), env(nil))
	if err != nil {
		t.Fatalf("only top-level *.yaml/*.yml are read: %v", err)
	}
	if len(ps) != 2 {
		t.Fatalf("loaded %d pipelines: %+v", len(ps), ps)
	}
	if ps[0].ID != "main" || ps[1].ID != "second" {
		t.Errorf("ids = %q %q", ps[0].ID, ps[1].ID)
	}
}

func TestLoadPipelines_RenameDisables(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.yaml", validPipeline)
	writeFile(t, dir, "broken.yaml", "steps: [{type: nope, with: {}}]")
	if _, err := config.LoadPipelines(dir, config.DefaultStepRegistry(), env(nil)); err == nil {
		t.Fatal("a broken pipeline must stop the load")
	}
	if err := os.Rename(filepath.Join(dir, "broken.yaml"), filepath.Join(dir, "broken.yaml.off")); err != nil {
		t.Fatal(err)
	}
	ps, err := config.LoadPipelines(dir, config.DefaultStepRegistry(), env(nil))
	if err != nil {
		t.Fatalf("renaming a broken file disables it: %v", err)
	}
	if len(ps) != 1 {
		t.Errorf("loaded %d", len(ps))
	}
}

func TestLoadPipelines_SymlinkToDirectoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.yaml", validPipeline)
	sub := filepath.Join(dir, "tree")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sub, filepath.Join(dir, "linked.yaml")); err != nil {
		t.Skipf("cannot create symlink here: %v", err)
	}
	_, err := config.LoadPipelines(dir, config.DefaultStepRegistry(), env(nil))
	if err == nil {
		t.Fatal("a *.yaml symlink pointing at a directory must be refused")
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("got: %v", err)
	}
}

func TestLoadPipelines_MissingDirIsEmpty(t *testing.T) {
	ps, err := config.LoadPipelines(filepath.Join(t.TempDir(), "nope"), config.DefaultStepRegistry(), env(nil))
	if err != nil {
		t.Fatalf("a missing pipelines dir is not an error: %v", err)
	}
	if len(ps) != 0 {
		t.Errorf("got %d pipelines", len(ps))
	}
}

func TestLoadPipelines_UnreferencedFileIsStillValidated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.yaml", validPipeline)
	writeFile(t, dir, "nobody-uses-this.yaml", `steps:
  - type: scan
    with: {roots: [/a], extensoins: [.mp4]}
  - type: parse
    with: {}
  - type: output
    with: {root: /o}
`)
	_, err := config.LoadPipelines(dir, config.DefaultStepRegistry(), env(nil))
	if err == nil {
		t.Fatal("every file is validated, referenced or not")
	}
	if !strings.Contains(err.Error(), "nobody-uses-this.yaml") {
		t.Errorf("got: %v", err)
	}
}

func TestPipelineString(t *testing.T) {
	p, err := parse(t, validPipeline)
	if err != nil {
		t.Fatal(err)
	}
	got := p.String()
	want := "main: scan → filter → group → parse → output"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
