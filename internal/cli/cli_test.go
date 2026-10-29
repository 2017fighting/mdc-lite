package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/cli"
)

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const pipeline = `steps:
  - type: scan
    with: {roots: [/media]}
  - type: filter
    with:
      rules:
        - name: all
          when: {ext_in: [.mp4]}
          then: include
  - type: group
    with: {max_distance: 2, max_files_for_levenshtein: 50}
  - type: parse
    with: {rules: [{builtin: default}]}
  - type: output
    with: {root: /media/library}
`

type fixture struct {
	dir       string
	config    string
	pipelines string
}

func setup(t *testing.T, configBody string) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{
		dir:       dir,
		config:    write(t, dir, "config.yaml", configBody),
		pipelines: filepath.Join(dir, "pipelines"),
	}
	if err := os.MkdirAll(f.pipelines, 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := cli.Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestConfigCheck_PrintsPipelinesAndEffectiveKeysWithSources(t *testing.T) {
	f := setup(t, "listen: \":9999\"\nretry_limit: 7\n")
	write(t, f.pipelines, "main.yaml", pipeline)

	code, out, errOut := run(t, "config", "check", "--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut)
	}
	if !strings.Contains(out, "main") || !strings.Contains(out, "scan → filter → group → parse → output") {
		t.Errorf("the loaded pipeline is not listed:\n%s", out)
	}
	if !strings.Contains(out, ":9999") || !strings.Contains(out, "(file)") {
		t.Errorf("the effective listen value and its source are missing:\n%s", out)
	}
	if !strings.Contains(out, "retry_limit") || !strings.Contains(out, "(default)") {
		t.Errorf("defaults must be shown with their source:\n%s", out)
	}
}

func TestConfigCheck_FailsOnABrokenPipelineButStillReports(t *testing.T) {
	f := setup(t, "")
	write(t, f.pipelines, "good.yaml", pipeline)
	write(t, f.pipelines, "bad.yaml", "steps:\n  - type: scan\n    with: {roots: [/a], extensoins: [.mp4]}\n")

	code, out, errOut := run(t, "config", "check", "--config", f.config, "--pipelines-dir", f.pipelines)
	if code == 0 {
		t.Fatal("a broken pipeline must make config check exit non-zero")
	}
	if !strings.Contains(out, "good") {
		t.Errorf("the good pipeline must still be reported:\n%s", out)
	}
	if !strings.Contains(out, "extensoins") || !strings.Contains(out, "bad.yaml:") {
		t.Errorf("the broken pipeline's error must name the file and the key:\n%s\nstderr: %s", out, errOut)
	}
}

func TestConfigCheck_FailsOnABrokenSettingsFile(t *testing.T) {
	f := setup(t, "listne: \":1\"\n")
	code, _, errOut := run(t, "config", "check", "--config", f.config, "--pipelines-dir", f.pipelines)
	if code == 0 {
		t.Fatal("an unknown settings key must fail")
	}
	if !strings.Contains(errOut, "listne") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestConfigCheck_NoConfigFileIsFine(t *testing.T) {
	dir := t.TempDir()
	code, out, errOut := run(t, "config", "check", "--config", filepath.Join(dir, "nope.yaml"), "--pipelines-dir", filepath.Join(dir, "pipelines"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if !strings.Contains(out, "none found") {
		t.Errorf("out = %s", out)
	}
	if !strings.Contains(out, ":9208") {
		t.Errorf("defaults must be printed:\n%s", out)
	}
}

func TestServe_RefusesToStartOnABrokenPipeline(t *testing.T) {
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", "steps:\n  - type: nope\n    with: {}\n")

	code, _, errOut := run(t, "serve", "--config", f.config, "--pipelines-dir", f.pipelines,
		"--db-path", filepath.Join(f.dir, "mdc.db"), "--listen", "127.0.0.1:0")
	if code == 0 {
		t.Fatal("serve must refuse a bad pipeline")
	}
	if !strings.Contains(errOut, "refusing to start") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestServe_RefusesToStartOnAMissingEnvVar(t *testing.T) {
	f := setup(t, "listen: \"${MDC_TEST_UNSET_VAR}\"\n")
	write(t, f.pipelines, "main.yaml", pipeline)

	code, _, errOut := run(t, "serve", "--config", f.config, "--pipelines-dir", f.pipelines)
	if code == 0 {
		t.Fatal("a missing ${VAR} must stop the process")
	}
	if !strings.Contains(errOut, "MDC_TEST_UNSET_VAR") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestUnknownCommandExitsTwo(t *testing.T) {
	code, _, errOut := run(t, "frobnicate")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := run(t, "version")
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("version printed nothing")
	}
}

func TestFlagBeatsFile(t *testing.T) {
	f := setup(t, "listen: \":1111\"\n")
	write(t, f.pipelines, "main.yaml", pipeline)

	_, out, _ := run(t, "config", "check", "--config", f.config, "--pipelines-dir", f.pipelines, "--listen", ":2222")
	if !strings.Contains(out, ":2222") || !strings.Contains(out, "(flag)") {
		t.Errorf("the flag must win and be reported as its source:\n%s", out)
	}
}

// The pipeline file the repo ships must pass the binary's own gate; otherwise the
// example is a trap for whoever copies it.
func TestExamplePipelineIsValid(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run(t, "config", "check",
		"--config", filepath.Join(root, "examples", "config.yaml"),
		"--pipelines-dir", filepath.Join(root, "examples", "pipelines"))
	if code != 0 {
		t.Fatalf("examples/ does not pass config check:\n%s\n%s", out, errOut)
	}
	if !strings.Contains(out, "  ok    main") {
		t.Errorf("the example pipeline is not reported ok:\n%s", out)
	}
}

// Nothing in the config path writes a file: a read-only /config volume is a
// supported deployment, and a config that appears on its own is a surprise.
func TestConfigCheckWritesNoFiles(t *testing.T) {
	dir := t.TempDir()
	pipelines := filepath.Join(dir, "pipelines")
	if err := os.MkdirAll(pipelines, 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := run(t, "config", "check",
		"--config", filepath.Join(dir, "config.yaml"),
		"--pipelines-dir", pipelines)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "pipelines" {
			t.Errorf("config check created %q; it must not write anything", e.Name())
		}
	}
}

// scrapePipelineWith is a complete pipeline whose scrape step carries the given
// `with` body: it is how the source-capability check is driven from the command line.
func scrapePipelineWith(with string) string {
	return `steps:
  - type: scan
    with: {roots: [/media]}
  - type: filter
    with:
      rules:
        - name: all
          when: {ext_in: [.mp4]}
          then: include
  - type: group
    with: {max_distance: 2, max_files_for_levenshtein: 50}
  - type: parse
    with: {rules: [{builtin: default}]}
  - type: scrape
    with:
` + with + `
  - type: output
    with: {root: /media/library}
`
}

func TestConfigCheck_RefusesAPriorityNamingASourceWithoutTheField(t *testing.T) {
	// javbus has no poster. Naming it in priority.poster is refused at startup — the
	// alternative is a poster that silently never arrives, with nothing to point at.
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipelineWith(`      sources: [javbus, javdb]
      priority:
        poster: [javbus, javdb]`))

	code, out, errOut := run(t, "config", "check", "--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (stdout: %s)", code, out)
	}
	for _, want := range []string{"FAIL", "priority.poster", "javbus", "does not declare"} {
		if !strings.Contains(out, want) && !strings.Contains(errOut, want) {
			t.Errorf("the refusal does not mention %q:\n%s%s", want, out, errOut)
		}
	}
}

func TestConfigCheck_RefusesAnUnknownSourceId(t *testing.T) {
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipelineWith(`      sources: [javbu]`))

	code, out, _ := run(t, "config", "check", "--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(out, "javbu") {
		t.Errorf("the refusal does not name the typo:\n%s", out)
	}
}
