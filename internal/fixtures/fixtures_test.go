package fixtures

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtures lays out a capture directory: the manifest plus the files the
// manifest claims. `extra` is written to disk without a manifest row, which is the
// shape of "somebody dropped a fixture in and forgot to record where it came from".
func writeFixtures(t *testing.T, rows []map[string]any, body, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range body {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	for name, content := range extra {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write stray file %s: %v", name, err)
		}
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return dir
}

func captureRow(name, body string) map[string]any {
	sum := sha256.Sum256([]byte(body))
	return map[string]any{
		"path": name, "url": "https://example.test/x", "status": 200,
		"bytes": len(body), "captured": "2026-10-29T00:00:00Z",
		"sha256": hex.EncodeToString(sum[:]),
	}
}

func TestAVerbatimCaptureDirectoryHasNoProblems(t *testing.T) {
	dir := writeFixtures(t,
		[]map[string]any{captureRow("site-ABC-1.html", "<html>real page</html>")},
		map[string]string{"site-ABC-1.html": "<html>real page</html>"}, nil)

	if problems := Problems(dir); len(problems) != 0 {
		t.Fatalf("problems = %v, want none", problems)
	}
}

// The reason the manifest exists: a fixture somebody edited to make a test pass has
// to be visible as such, and the check has to name the file.
func TestAHandEditedCaptureIsAProblem(t *testing.T) {
	dir := writeFixtures(t,
		[]map[string]any{captureRow("site-ABC-1.html", "<html>real page</html>")},
		map[string]string{"site-ABC-1.html": "<html>edited page</html>"}, nil)

	problems := Problems(dir)
	if len(problems) == 0 {
		t.Fatal("an edited capture was accepted")
	}
	if !strings.Contains(strings.Join(problems, "\n"), "site-ABC-1.html") {
		t.Errorf("problems = %v, want the edited file named", problems)
	}
}

func TestAFileWithoutAManifestRowIsAProblem(t *testing.T) {
	dir := writeFixtures(t,
		[]map[string]any{captureRow("site-ABC-1.html", "page")},
		map[string]string{"site-ABC-1.html": "page"},
		map[string]string{"site-ABC-2.html": "unrecorded"})

	problems := Problems(dir)
	if !strings.Contains(strings.Join(problems, "\n"), "site-ABC-2.html") {
		t.Errorf("problems = %v, want the unrecorded file named", problems)
	}
}

func TestARowWithoutAFileIsAProblem(t *testing.T) {
	dir := writeFixtures(t,
		[]map[string]any{captureRow("site-ABC-1.html", "page")}, nil, nil)

	problems := Problems(dir)
	if !strings.Contains(strings.Join(problems, "\n"), "site-ABC-1.html") {
		t.Errorf("problems = %v, want the missing file named", problems)
	}
}

// A capture is a point-in-time answer, so the row has to say when, where and what
// the site said. A row missing one of those is not evidence of anything.
func TestACaptureRowMustSayWhereItCameFrom(t *testing.T) {
	for _, key := range []string{"url", "status", "captured", "bytes", "sha256"} {
		t.Run(key, func(t *testing.T) {
			row := captureRow("site-ABC-1.html", "page")
			delete(row, key)
			dir := writeFixtures(t, []map[string]any{row},
				map[string]string{"site-ABC-1.html": "page"}, nil)
			problems := Problems(dir)
			if !strings.Contains(strings.Join(problems, "\n"), key) {
				t.Errorf("problems = %v, want %q named", problems, key)
			}
		})
	}
}

// A constructed fixture is allowed, but only for two reasons, and it must say why.
// The set is closed on purpose: "I could not reach the site" is not a reason to
// invent a page, and widening this list is a decision, not an accident.
func TestAConstructedFixtureNeedsAReasonFromTheClosedSet(t *testing.T) {
	body := "<html>made up</html>"
	sum := sha256.Sum256([]byte(body))
	row := map[string]any{
		"path": "site-ABC-1.html", "bytes": len(body),
		"sha256": hex.EncodeToString(sum[:]),
	}
	files := map[string]string{"site-ABC-1.html": body}

	for _, reason := range []string{ReasonDefectReproduction, ReasonParserShape} {
		t.Run(reason, func(t *testing.T) {
			r := map[string]any{"reason": reason, "note": "why this file exists"}
			for k, v := range row {
				r[k] = v
			}
			dir := writeFixtures(t, []map[string]any{r}, files, nil)
			if problems := Problems(dir); len(problems) != 0 {
				t.Fatalf("problems = %v, want none", problems)
			}
		})
	}

	t.Run("unknown-reason", func(t *testing.T) {
		r := map[string]any{"reason": "convenience", "note": "the site was slow"}
		for k, v := range row {
			r[k] = v
		}
		dir := writeFixtures(t, []map[string]any{r}, files, nil)
		problems := strings.Join(Problems(dir), "\n")
		if !strings.Contains(problems, "convenience") {
			t.Errorf("problems = %v, want the unknown reason named", problems)
		}
	})

	t.Run("no-note", func(t *testing.T) {
		r := map[string]any{"reason": ReasonParserShape}
		for k, v := range row {
			r[k] = v
		}
		dir := writeFixtures(t, []map[string]any{r}, files, nil)
		if problems := Problems(dir); len(problems) == 0 {
			t.Fatal("a constructed fixture without a note was accepted")
		}
	})
}

// The repository's own capture directories are the point of this package: if this
// fails, one of the four sources has a fixture that no longer matches its manifest
// or a constructed page with a made-up reason.
func TestTheRepositoriesCapturesHoldUp(t *testing.T) {
	for _, id := range []string{"javbus", "javdb", "fc2", "dmm"} {
		t.Run(id, func(t *testing.T) {
			dir := filepath.Join("..", "source", id, "testdata", "captures")
			for _, p := range Problems(dir) {
				t.Error(p)
			}
		})
	}
}

func TestLoadReadsTheManifestInAStableOrder(t *testing.T) {
	dir := writeFixtures(t,
		[]map[string]any{
			captureRow("b.html", "b"),
			captureRow("a.html", "a"),
		},
		map[string]string{"a.html": "a", "b.html": "b"}, nil)

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0].Path != "a.html" || got[1].Path != "b.html" {
		t.Fatalf("load = %v, want a.html then b.html", got)
	}
}
