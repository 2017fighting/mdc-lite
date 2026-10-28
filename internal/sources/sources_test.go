package sources

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/source/javbus"
)

func TestBuildAssemblesTheSourcesThisBinaryHas(t *testing.T) {
	w := config.ScrapeWith{Sources: config.SourceOrder{
		{ID: "javdb"}, {ID: "dmm"}, {ID: "javbus"}, {ID: "fc2"},
	}}
	reg := Build(w, fetch.New(fetch.Options{}))
	if _, ok := reg.Lookup(javbus.ID); !ok {
		t.Fatalf("javbus is configured but not built (known: %s)", reg.Known())
	}
	// The three sources of the next ticket are configured in the default order and
	// are not built yet. They must be skipped rather than refused: the factory
	// config lists all four, and a missing one must not stop the present one from
	// running.
	if got := reg.Known(); got != "javbus" {
		t.Errorf("registry = %s, want only javbus", got)
	}
}

func TestBuildIgnoresASourceThatIsNotConfigured(t *testing.T) {
	reg := Build(config.ScrapeWith{Sources: config.SourceOrder{{ID: "javbus"}}}, fetch.New(fetch.Options{}))
	if len(reg) != 1 {
		t.Errorf("registry has %d sources, want what the config listed", len(reg))
	}
}

func TestJavbusOptionsCarryTheFourKnobs(t *testing.T) {
	w := config.SourceWith{
		Proxy:   "http://proxy.local:3128",
		Domains: []string{"mirror.example", "javbus.com"},
		Timeout: config.Duration(3 * time.Second),
		Rate:    &config.Rate{RequestsPerSecond: 2, Burst: 3, Cooldown: config.Duration(time.Minute)},
	}
	opts := javbusOptions(w)
	if opts.Proxy != w.Proxy {
		t.Errorf("proxy = %q", opts.Proxy)
	}
	if len(opts.Domains) != 2 || opts.Domains[0] != "mirror.example" {
		t.Errorf("domains = %v, want the configured order kept", opts.Domains)
	}
	if opts.Timeout != 3*time.Second {
		t.Errorf("timeout = %v", opts.Timeout)
	}
	if opts.Rate == nil || opts.Rate.RequestsPerSecond != 2 || opts.Rate.Burst != 3 || opts.Rate.Cooldown != time.Minute {
		t.Errorf("rate = %+v", opts.Rate)
	}
}

func TestJavbusOptionsLeaveTheUnsetKnobsToTheFetchDefaults(t *testing.T) {
	opts := javbusOptions(config.SourceWith{})
	if opts.Proxy != "" || len(opts.Domains) != 0 || opts.Rate != nil || opts.Timeout != 0 {
		t.Errorf("an empty config produced %+v, want every knob left unset", opts)
	}
}

func TestScrapeWithFallsBackToTheDocumentedDefaults(t *testing.T) {
	p := config.Pipeline{ID: "preview", File: "preview.yaml", Steps: []config.Step{{Type: config.StepScan}}}
	w := ScrapeWith(p)
	if len(w.Sources) != 4 || w.Sources.IDs()[0] != "javdb" || time.Duration(w.Timeout) != 5*time.Minute {
		t.Errorf("defaults = %+v, want the four-source order and a 5m budget", w)
	}
}

func TestScrapeWithReadsThePipelinesOwnStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.yaml")
	body := `steps:
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
      sources:
        - id: javbus
          domains: ["mirror.example", "javbus.com"]
          timeout: 12s
          rate: {requests_per_second: 2, burst: 4, cooldown: 90s}
      timeout: 2m
  - type: output
    with: {root: /media/library}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := config.LoadPipelineFile(path, config.DefaultStepRegistry(), nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	w := ScrapeWith(p)
	if time.Duration(w.Timeout) != 2*time.Minute {
		t.Errorf("timeout = %v, want the pipeline's 2m", w.Timeout)
	}
	knobs := w.Sources.Knobs()
	got, ok := knobs["javbus"]
	if !ok {
		t.Fatalf("the per-source knobs were lost: %+v", w.Sources)
	}
	if len(got.Domains) != 2 || got.Domains[0] != "mirror.example" {
		t.Errorf("domains = %v", got.Domains)
	}
	if time.Duration(got.Timeout) != 12*time.Second {
		t.Errorf("per-source timeout = %v", got.Timeout)
	}
	if got.Rate == nil || got.Rate.RequestsPerSecond != 2 || time.Duration(got.Rate.Cooldown) != 90*time.Second {
		t.Errorf("rate = %+v", got.Rate)
	}
}
