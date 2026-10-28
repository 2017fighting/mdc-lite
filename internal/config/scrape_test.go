package config

import (
	"strings"
	"testing"
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
  - type: group
    with: {max_distance: 2, max_files_for_levenshtein: 50}
  - type: parse
    with: {rules: [{builtin: default}]}
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
