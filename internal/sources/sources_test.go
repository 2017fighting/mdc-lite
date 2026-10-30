package sources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/config"
	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/source/dmm"
	"github.com/2017fighting/mdc-lite/internal/source/fc2"
	"github.com/2017fighting/mdc-lite/internal/source/javbus"
	"github.com/2017fighting/mdc-lite/internal/source/javdb"
)

func TestBuildAssemblesTheSourcesThisBinaryHas(t *testing.T) {
	w := config.ScrapeWith{Sources: config.SourceOrder{
		{ID: "javdb"}, {ID: "dmm"}, {ID: "javbus"}, {ID: "fc2"},
	}}
	reg := Build(w, fetch.New(fetch.Options{}))
	// All four factory sources are built into this binary; the default order lists
	// exactly them.
	if got := reg.Known(); got != "dmm, fc2, javbus, javdb" {
		t.Errorf("registry = %s, want all four sources", got)
	}
	for _, id := range []string{javdb.ID, dmm.ID, javbus.ID, fc2.ID} {
		if _, ok := reg.Lookup(id); !ok {
			t.Errorf("%s is configured but not built (known: %s)", id, reg.Known())
		}
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
  - type: parse
    with: {rules: [{builtin: default}]}
  - type: group
    with: {max_distance: 2, max_files_for_levenshtein: 50}
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

// pipelineWith parses one pipeline document with the given scrape `with` body.
func pipelineWith(t *testing.T, scrapeWith string) config.Pipeline {
	t.Helper()
	doc := `steps:
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
    with: {max_distance: 2, max_files_for_levenshtein: 50}
  - type: scrape
    with:
` + scrapeWith + `
  - type: output
    with: {root: /media/library}
`
	p, err := config.ParsePipeline("main.yaml", []byte(doc), config.DefaultStepRegistry(), nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p
}

func TestValidateAcceptsTheFactoryDefaults(t *testing.T) {
	if err := Validate(pipelineWith(t, `      timeout: 5m`)); err != nil {
		t.Fatalf("the factory defaults were refused: %v", err)
	}
}

func TestValidateRefusesAPriorityFieldTheSourceCannotSupply(t *testing.T) {
	// javbus has no poster, so naming it for that field is a configuration error the
	// loader can see: the alternative is a field that is mysteriously always missing.
	err := Validate(pipelineWith(t, `      sources: [javbus, javdb]
      priority:
        poster: [javbus, javdb]`))
	if err == nil {
		t.Fatal("a source that does not declare poster was accepted in priority.poster")
	}
	for _, want := range []string{"priority.poster", "javbus", "poster"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "step 5 (scrape)") {
		t.Errorf("the error does not name the step: %v", err)
	}
}

func TestValidateRefusesAnUnknownSourceIdInTheGlobalOrder(t *testing.T) {
	err := Validate(pipelineWith(t, `      sources: [javbu]`))
	if err == nil || !strings.Contains(err.Error(), "javbu") {
		t.Fatalf("a typo'd source id was accepted: %v", err)
	}
	if !strings.Contains(err.Error(), "known:") {
		t.Errorf("the error does not say what the binary does have: %v", err)
	}
}

func TestValidateAcceptsANarrowedSourceListWithAMatchingPriority(t *testing.T) {
	// A pipeline that configures one source may order the fields it has. Whether the
	// id is configured at all is the config package's check; this one is capability.
	if err := Validate(pipelineWith(t, `      sources: [javbus]
      priority:
        title: [javbus]`)); err != nil {
		t.Fatalf("a narrowed source list was refused: %v", err)
	}
}
