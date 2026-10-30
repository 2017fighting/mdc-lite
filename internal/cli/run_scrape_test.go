package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRun_ScrapeReachesTheBuiltInSource proves the product wiring: `mdc run`
// builds the registry from the pipeline's `scrape.with.sources`, so the real
// javbus source answers a real run — the fixture server is only standing in for
// the site, not for the source.
func TestRun_ScrapeReachesTheBuiltInSource(t *testing.T) {
	fx := newJavbusFixture(t)
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipeline(fx.URL))

	media := filepath.Join(f.dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "SSIS-001.mp4"), []byte("not a real video"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := run(t, "run", "--pipeline", "main", "--roots", media, "--until", "scrape",
		"--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "scrape:  1 number, 1 images") {
		t.Errorf("the scrape step did not report the number and its cover:\n%s", out)
	}
	var asked bool
	for _, r := range fx.seen() {
		if strings.HasSuffix(r.URL.Path, "/SSIS-001") {
			asked = true
			if got := r.Header.Get("Cookie"); got != "dv=1" {
				t.Errorf("Cookie = %q: the run did not use javbus's own request", got)
			}
		}
	}
	if !asked {
		t.Error("the run never asked the source for the number")
	}
}

// TestRun_ScrapeFallsBackAcrossTheFourSources proves the field-level fallback end to
// end through the real command: javdb answers nothing, javbus answers the fields it
// has, and the run keeps going — one site being down is not the task's failure.
func TestRun_ScrapeFallsBackAcrossSources(t *testing.T) {
	fx := newJavbusFixture(t)
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", `steps:
  - type: scan
    with: {roots: [/media]}
  - type: filter
    with:
      rules:
        - name: all
          when: {ext_in: [.mp4]}
          then: include
  - type: parse
    with: {rules: [{builtin: default}]}
  - type: group
    with: {}
  - type: scrape
    with:
      sources:
        - id: javdb
          domains: ["http://no-such-host.invalid"]
        - id: javbus
          domains: ["`+fx.URL+`"]
      timeout: 30s
  - type: output
    with: {root: /media/library}
`)

	media := filepath.Join(f.dir, "media")
	if err := os.MkdirAll(media, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(media, "SSIS-001.mp4"), []byte("not a real video"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := run(t, "run", "--pipeline", "main", "--roots", media, "--until", "scrape",
		"--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "1 source(s) failed") {
		t.Errorf("the summary does not report the failed source:\n%s", out)
	}
	if !strings.Contains(out, "scrape:  1 number, 1 images") {
		t.Errorf("the fallback source's image did not land:\n%s", out)
	}
}
