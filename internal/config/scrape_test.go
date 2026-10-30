package config

import (
	"strings"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/field"
)

// scrapeWith parses one pipeline whose scrape step carries the given `with` body.
func scrapeWith(t *testing.T, body string) error {
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
    with: {}
  - type: scrape
    with:
` + body + `
  - type: output
    with: {root: /media/library}
`
	_, err := ParsePipeline("main.yaml", []byte(doc), DefaultStepRegistry(), func(string) (string, bool) { return "", false })
	return err
}

func TestScrapeWithAcceptsBareIdsAndKnobObjects(t *testing.T) {
	err := scrapeWith(t, `      sources:
        - javbus
        - id: dmm
          domains: ["dmm.co.jp", "http://127.0.0.1:9001"]
          timeout: 30s
          rate: {requests_per_second: 2, burst: 4, cooldown: 90s}
      timeout: 5m`)
	if err != nil {
		t.Fatalf("a valid sources list was refused: %v", err)
	}
}

func TestScrapeWithRefusesADomainThatIsNeitherHostNorURL(t *testing.T) {
	err := scrapeWith(t, `      sources:
        - id: javbus
          domains: [""]`)
	if err == nil || !strings.Contains(err.Error(), "domains[0]") {
		t.Fatalf("an empty domain was accepted: %v", err)
	}
	for _, bad := range []string{"http://", "ftp://mirror.example"} {
		err = scrapeWith(t, `      sources:
        - id: javbus
          domains: ["`+bad+`"]`)
		if err == nil || !strings.Contains(err.Error(), "domains[0]") {
			t.Fatalf("%q was accepted as a domain: %v", bad, err)
		}
	}
}

// scrapeSettings parses one pipeline and returns its scrape step's settings.
func scrapeSettings(t *testing.T, body string) *ScrapeWith {
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
    with: {}
  - type: scrape
    with:
` + body + `
  - type: output
    with: {root: /media/library}
`
	p, err := ParsePipeline("main.yaml", []byte(doc), DefaultStepRegistry(), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	w, ok := p.Steps[4].WithValue.(*ScrapeWith)
	if !ok {
		t.Fatal("the scrape settings were not decoded")
	}
	return w
}

func TestScrapeDefaultsWriteOutEveryField(t *testing.T) {
	// The factory table is a complete replacement for the global order, so it spells
	// out all 14 fields: leaving a field implicit would make its candidate list a
	// question the operator has to guess the answer to.
	w := scrapeSettings(t, `      timeout: 5m`)
	if len(w.Priority) != len(field.All()) {
		t.Fatalf("the default priority has %d fields, want all %d", len(w.Priority), len(field.All()))
	}
	for _, name := range field.All() {
		order, ok := w.Priority[string(name)]
		if !ok {
			t.Errorf("the default priority does not name %q", name)
			continue
		}
		if len(order) == 0 {
			t.Errorf("priority.%s is empty", name)
		}
		// Every listed source declares the field: the capability check at startup
		// would refuse the factory default otherwise.
		for _, id := range order {
			if !declaresField(id, name) {
				t.Errorf("priority.%s lists %q, which does not declare that field", name, id)
			}
		}
	}
}

// declaresField is the factory table's own capability expectations, spelled out so a
// default that names a source which cannot supply the field fails here rather than at
// some operator's startup.
func declaresField(id string, name field.Name) bool {
	switch id {
	case "javdb", "dmm":
		return true
	case "javbus":
		for _, f := range []field.Name{field.Title, field.Tags, field.Releasedate, field.Actors,
			field.Director, field.Runtime, field.Series, field.Studio, field.Publisher, field.Fanart} {
			if f == name {
				return true
			}
		}
		return false
	case "fc2":
		for _, f := range []field.Name{field.Title, field.Plot, field.Fanart, field.Extrafanarts,
			field.Tags, field.Releasedate, field.Runtime, field.Studio} {
			if f == name {
				return true
			}
		}
		return false
	}
	return false
}

func TestScrapePriorityInTheFileReplacesTheFactoryTable(t *testing.T) {
	// A user who writes one field's order must not silently keep the factory's order
	// for the other thirteen: an unlisted field falls back to `sources`.
	w := scrapeSettings(t, `      priority:
        title: [javbus, dmm]`)
	if len(w.Priority) != 1 {
		t.Fatalf("priority = %v, want only the field the file names", w.Priority)
	}
	if got := strings.Join(w.Priority["title"], ","); got != "javbus,dmm" {
		t.Errorf("priority.title = %q, want the file's own order", got)
	}
}

func TestScrapeWritingSourcesDropsTheFactoryPriority(t *testing.T) {
	// The factory table describes the factory source list. A file that writes its own
	// sources and no priority means "my sources, in that order" for every field —
	// otherwise `sources: [javbus]` would schedule nothing at all.
	w := scrapeSettings(t, `      sources: [javbus, fc2]`)
	if w.Priority != nil {
		t.Errorf("priority = %v, want no factory table over a hand-written source list", w.Priority)
	}
	if got := strings.Join(w.Sources.IDs(), ","); got != "javbus,fc2" {
		t.Errorf("sources = %q", got)
	}
}

func TestScrapeRefusesADuplicateIdInAPriorityList(t *testing.T) {
	err := scrapeWith(t, `      priority:
        title: [javdb, javdb]`)
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("a repeated id was accepted: %v", err)
	}
}

func TestScrapeRefusesAnEmptySourceIdInAPriorityList(t *testing.T) {
	err := scrapeWith(t, `      priority:
        title: [""]`)
	if err == nil || !strings.Contains(err.Error(), "empty source id") {
		t.Fatalf("an empty id was accepted: %v", err)
	}
}

func TestScrapeRefusesAnEmptyPriorityList(t *testing.T) {
	err := scrapeWith(t, `      priority:
        title: []`)
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("an empty list was accepted: %v", err)
	}
}

func TestScrapeRefusesAPriorityIdThatIsNotAConfiguredSource(t *testing.T) {
	// An id the pipeline did not configure would be dropped at run time and the field
	// would silently lose a candidate, so it is refused instead.
	err := scrapeWith(t, `      sources: [javbus]
      priority:
        title: [javbus, dmm]`)
	if err == nil || !strings.Contains(err.Error(), "not in sources") {
		t.Fatalf("a priority id outside sources was accepted: %v", err)
	}
}

// scrapeSettingsYAML parses a scrape `with` body written as raw YAML (anchors and
// merge keys included, which the indented helper cannot express).
func scrapeSettingsYAML(t *testing.T, withYAML string) *ScrapeWith {
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
    with: {}
  - type: scrape
    with:
` + withYAML + `
  - type: output
    with: {root: /media/library}
`
	p, err := ParsePipeline("main.yaml", []byte(doc), DefaultStepRegistry(), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return p.Steps[4].WithValue.(*ScrapeWith)
}

func TestScrapeASourceListFromAMergeKeyDropsTheFactoryPriority(t *testing.T) {
	// `<<: *anchor` is a supported YAML shape, so "the file wrote its own sources"
	// has to be true for a key the anchor carried too. Otherwise the factory table
	// survives, names sources the file never configured, and the capability check
	// refuses a pipeline that is perfectly consistent.
	w := scrapeSettingsYAML(t, `      <<: &own
        sources: [javbus]`)
	if w.Priority != nil {
		t.Fatalf("priority = %v, want no factory table over a merged source list", w.Priority)
	}
	if got := strings.Join(w.Sources.IDs(), ","); got != "javbus" {
		t.Errorf("sources = %q", got)
	}
}

func TestScrapeAPriorityFromAMergeKeyReplacesTheFactoryTable(t *testing.T) {
	w := scrapeSettingsYAML(t, `      <<: &order
        priority:
          title: [javdb, dmm]`)
	if len(w.Priority) != 1 {
		t.Fatalf("priority = %v, want only the field the merged anchor names", w.Priority)
	}
	if got := strings.Join(w.Priority["title"], ","); got != "javdb,dmm" {
		t.Errorf("priority.title = %q", got)
	}
}
