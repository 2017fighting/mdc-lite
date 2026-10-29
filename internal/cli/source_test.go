package cli_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/source"
)

// scrapePipeline is a pipeline whose scrape step lists javbus with a fixture
// domain: the only thing the test changes is where the domain points, so the
// command runs the production source, cookie and selectors.
func scrapePipeline(domain string) string {
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
      sources:
        - id: javbus
          domains: ["` + domain + `"]
      timeout: 30s
  - type: output
    with: {root: /media/library}
`
}

// captureBody is the real javbus page, captured by `mdc source test --dump`.
func captureBody(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "source", "javbus", "testdata", "captures", name))
	if err != nil {
		t.Fatalf("read the capture: %v", err)
	}
	return body
}

// javbusFixture replays javbus: the captured pages, an image that insists on a
// Referer the way the real CDN does, and a record of what it was asked.
type javbusFixture struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	status   int
}

func newJavbusFixture(t *testing.T) *javbusFixture {
	t.Helper()
	f := &javbusFixture{status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.Clone(r.Context()))
		status := f.status
		f.mu.Unlock()
		switch {
		case status != http.StatusOK:
			w.WriteHeader(status)
		case strings.HasPrefix(r.URL.Path, "/pics/"):
			if r.Header.Get("Referer") == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("\xff\xd8\xff\xe0 the cover"))
		case strings.HasSuffix(r.URL.Path, "/SSIS-001"):
			_, _ = w.Write(captureBody(t, "javbus-SSIS-001.html"))
		case strings.HasSuffix(r.URL.Path, "/IPX-535"):
			_, _ = w.Write(captureBody(t, "javbus-IPX-535.html"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write(captureBody(t, "javbus-ZZZZ-9999.html"))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *javbusFixture) seen() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.requests...)
}

func TestSourceTest_PrintsEveryFieldWithItsStatus(t *testing.T) {
	fx := newJavbusFixture(t)
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipeline(fx.URL))

	code, out, errOut := run(t, "source", "test", "--id", "javbus", "--number", "SSIS-001",
		"--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\n%s", code, errOut, out)
	}
	for _, want := range []string{
		"source:  javbus",
		"number:  SSIS-001",
		// The declaration line is in spec field order, so two sources' reports line up.
		"declared: title, fanart, tags, releasedate, actors, director, runtime, series, studio, publisher",
		"title        SSIS-001 一ヶ月間の禁欲の果てに",
		"releasedate  2021-02-18",
		"runtime      150 分钟",
		"actors       葵つかさ (female), 乙白さやか (female)",
		// The declaration and the contribution are separate columns: a declared
		// field the page does not carry must not read as "not declared".
		"series       — (declared, absent on this page)",
		"plot         — (not declared by this source)",
		"fanart       " + fx.URL + "/pics/cover/83ie_b.jpg",
		"images:",
		"(14 bytes, jpg)",
		"status:  ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not contain %q:\n%s", want, out)
		}
	}

	seen := fx.seen()
	if len(seen) < 2 {
		t.Fatalf("the fixture saw %d requests, want the page and its image", len(seen))
	}
	if got := seen[0].Header.Get("Cookie"); got != "dv=1" {
		t.Errorf("Cookie = %q: the command did not go through javbus's own request path", got)
	}
	if got := seen[0].UserAgent(); !strings.Contains(got, "Mozilla/5.0") {
		t.Errorf("User-Agent = %q, want the browser UA", got)
	}
}

func TestSourceTest_ReadsA404AsNotFoundAndExitsZero(t *testing.T) {
	fx := newJavbusFixture(t)
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipeline(fx.URL))

	code, out, errOut := run(t, "source", "test", "--id", "javbus", "--number", "ZZZZ-9999",
		"--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "status:  notfound") {
		t.Errorf("a 404 did not read as notfound:\n%s", out)
	}
	if strings.Contains(out, "reason:") {
		t.Errorf("notfound was given a reason code:\n%s", out)
	}
}

func TestSourceTest_ReportsAFailureWithItsReasonAndExitsNonZero(t *testing.T) {
	fx := newJavbusFixture(t)
	fx.status = http.StatusForbidden
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipeline(fx.URL))

	code, out, errOut := run(t, "source", "test", "--id", "javbus", "--number", "SSIS-001",
		"--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 1 {
		t.Fatalf("exit %d, want 1 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "status:  failed") || !strings.Contains(out, "reason:  cooldown") {
		t.Errorf("a 403 did not read as failed/cooldown:\n%s", out)
	}
}

func TestSourceTest_DumpWritesTheCaptureAndItsManifest(t *testing.T) {
	fx := newJavbusFixture(t)
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipeline(fx.URL))
	dumpDir := filepath.Join(f.dir, "captures")

	code, out, errOut := run(t, "source", "test", "--id", "javbus", "--number", "SSIS-001",
		"--dump", "--dump-dir", dumpDir, "--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s\n%s", code, errOut, out)
	}

	body, err := os.ReadFile(filepath.Join(dumpDir, "javbus-SSIS-001.html"))
	if err != nil {
		t.Fatalf("the capture was not written: %v", err)
	}
	// Byte for byte: the fixture is what the site sent, not a re-render.
	if string(body) != string(captureBody(t, "javbus-SSIS-001.html")) {
		t.Error("the dumped page is not the page the fixture server sent")
	}

	data, err := os.ReadFile(filepath.Join(dumpDir, "fixtures.json"))
	if err != nil {
		t.Fatalf("the manifest was not written: %v", err)
	}
	var manifest []source.CaptureInfo
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("the manifest is not JSON: %v", err)
	}
	if len(manifest) != 1 {
		t.Fatalf("manifest has %d entries, want 1", len(manifest))
	}
	row := manifest[0]
	sum := sha256.Sum256(body)
	if row.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("manifest sha256 = %s, want the file's", row.SHA256)
	}
	if row.Bytes != len(body) || row.Status != http.StatusOK {
		t.Errorf("manifest row = %+v, want the bytes and status it wrote", row)
	}
	if row.URL != fx.URL+"/SSIS-001" || row.Captured == "" {
		t.Errorf("manifest row = %+v, want the URL it came from and when", row)
	}
}

func TestSourceTest_RequiresIdAndNumber(t *testing.T) {
	f := setup(t, "")
	code, _, errOut := run(t, "source", "test", "--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut, "--id and --number are required") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestSourceTest_NamesTheSourcesThisBinaryKnows(t *testing.T) {
	f := setup(t, "")
	write(t, f.pipelines, "main.yaml", scrapePipeline("https://www.javbus.com"))
	code, _, errOut := run(t, "source", "test", "--id", "javdb", "--number", "SSIS-001",
		"--config", f.config, "--pipelines-dir", f.pipelines)
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut, "javbus") {
		t.Errorf("stderr does not name what the binary does know: %s", errOut)
	}
}
