package javbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// captureDir holds the pages fetched from the live site, byte for byte, by
// `mdc source test --dump`. Tests read them and never write them: a fixture that a
// test could edit stops being evidence.
const captureDir = "testdata/captures"

func captures(t *testing.T) []source.CaptureInfo {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(captureDir, "fixtures.json"))
	if err != nil {
		t.Fatalf("read the capture manifest: %v", err)
	}
	var out []source.CaptureInfo
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse the capture manifest: %v", err)
	}
	return out
}

func captureBody(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(captureDir, name))
	if err != nil {
		t.Fatalf("read the capture %s: %v", name, err)
	}
	return body
}

// replay serves the captured pages at the paths they were captured from, so the
// real source — URL building, cookie, selectors — runs against real bytes.
//
// It also records the requests: the headers are part of the contract (the age-gate
// cookie on the page, the Referer on the image), and a test that only checked the
// parsed fields would not notice either going missing.
type replay struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
}

func (r *replay) seen() []*http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*http.Request(nil), r.requests...)
}

func serveCaptures(t *testing.T) *replay {
	t.Helper()
	byPath := map[string]source.CaptureInfo{}
	for _, c := range captures(t) {
		byPath[c.Path] = c
	}
	r := &replay{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.Clone(context.Background()))
		r.mu.Unlock()
		// `/pics/...` is an image: the source's own FetchImage asks for it.
		if strings.HasPrefix(req.URL.Path, "/pics/") {
			if req.Header.Get("Referer") == "" {
				// The live CDN answers 403 without one; the replay must too, or a test
				// could pass while the product gets a poster-shaped 403 page.
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("\xff\xd8\xff\xe0 cover bytes"))
			return
		}
		name := fmt.Sprintf("javbus-%s.html", strings.TrimPrefix(req.URL.Path, "/"))
		c, ok := byPath[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(c.Status)
		_, _ = w.Write(captureBody(t, c.Path))
	}))
	t.Cleanup(r.Close)
	return r
}

func newSource(t *testing.T, r *replay) *Source {
	t.Helper()
	f := fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}})
	return New(f, Options{Domains: []string{r.URL}})
}

func TestFieldsDeclaresExactlyTheTenFieldsTheSiteHas(t *testing.T) {
	src := New(nil, Options{})
	got := src.Fields()
	want := map[field.Name]bool{
		field.Title: true, field.Tags: true, field.Releasedate: true, field.Actors: true,
		field.Director: true, field.Runtime: true, field.Series: true, field.Studio: true,
		field.Publisher: true, field.Fanart: true,
	}
	if len(got) != 10 {
		t.Fatalf("declared %d fields (%v), want the 10 javbus has", len(got), got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("declares %q, which the site does not supply", name)
		}
	}
	// The four the site does not have must not be declared: `priority[poster]` with
	// javbus listed is a configuration the loader has to be able to refuse.
	for _, name := range []field.Name{field.Plot, field.Extrafanarts, field.Poster, field.Rating} {
		for _, declared := range got {
			if declared == name {
				t.Errorf("declares %q, which javbus does not have", name)
			}
		}
	}
}

func TestCapturesMatchTheirManifest(t *testing.T) {
	// The fixture discipline: the manifest says what the site answered, and the
	// bytes on disk have to be those bytes. A capture somebody hand-edited to make a
	// test pass would otherwise be invisible.
	for _, c := range captures(t) {
		body := captureBody(t, c.Path)
		if len(body) != c.Bytes {
			t.Errorf("%s: %d bytes on disk, manifest says %d", c.Path, len(body), c.Bytes)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != c.SHA256 {
			t.Errorf("%s: sha256 %s, manifest says %s", c.Path, got, c.SHA256)
		}
		if !strings.Contains(c.URL, "javbus") {
			t.Errorf("%s: url %q does not look like a javbus capture", c.Path, c.URL)
		}
		if c.Captured == "" {
			t.Errorf("%s: the manifest does not say when it was captured", c.Path)
		}
	}
}

func TestScrapeFromARealCapture(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if !res.Found {
		t.Fatal("the capture is a detail page, so Found must be true")
	}
	f := res.Fields

	if f.Title == nil || !strings.HasPrefix(*f.Title, "SSIS-001 一ヶ月間の禁欲の果てに") {
		t.Errorf("title = %v, want the page's h3", f.Title)
	}
	if f.Releasedate == nil || *f.Releasedate != "2021-02-18" {
		t.Errorf("releasedate = %v, want 2021-02-18", f.Releasedate)
	}
	if f.Runtime == nil || *f.Runtime != 150 {
		t.Errorf("runtime = %v, want 150 minutes from `150分鐘`", f.Runtime)
	}
	if len(f.Director) != 1 || f.Director[0] != "苺原" {
		t.Errorf("director = %v", f.Director)
	}
	if f.Studio == nil || *f.Studio != "エスワン ナンバーワンスタイル" {
		t.Errorf("studio = %v", f.Studio)
	}
	if f.Publisher == nil || *f.Publisher != "S1 NO.1 STYLE" {
		t.Errorf("publisher = %v", f.Publisher)
	}
	if f.Series != nil {
		t.Errorf("series = %v, want nil: this page has no series line", *f.Series)
	}
	// Tags are the genre labels only. The actresses are also wrapped in
	// `span.genre`, so a looser selector would land them here.
	wantTags := []string{"多P", "美少女", "乳房", "薄馬賽克", "高畫質", "出軌", "戲劇", "DMM獨家"}
	if strings.Join(f.Tags, "|") != strings.Join(wantTags, "|") {
		t.Errorf("tags = %v, want %v", f.Tags, wantTags)
	}
	wantActors := []meta.Actor{{Name: "葵つかさ", Gender: meta.Female}, {Name: "乙白さやか", Gender: meta.Female}}
	if len(f.Actors) != len(wantActors) {
		t.Fatalf("actors = %v, want %v", f.Actors, wantActors)
	}
	for i, want := range wantActors {
		if f.Actors[i] != want {
			t.Errorf("actor %d = %v, want %v", i, f.Actors[i], want)
		}
	}
	if f.Fanart == nil {
		t.Fatal("no fanart: javbus's only image field is the big cover")
	}
	if !strings.HasSuffix(f.Fanart.URL, "/pics/cover/83ie_b.jpg") {
		t.Errorf("fanart URL = %q, want the page's absolute cover address", f.Fanart.URL)
	}
	if f.Fanart.Role != field.Fanart {
		t.Errorf("fanart role = %q", f.Fanart.Role)
	}
	if got := f.Fanart.Headers["Referer"]; !strings.HasSuffix(got, "/SSIS-001") {
		t.Errorf("image Referer = %q, want the page it belongs to", got)
	}
	// The four fields javbus does not have stay nil, which is what "this source has
	// nothing to say about poster" looks like downstream.
	if f.Plot != nil || f.Poster != nil || f.Rating != nil || len(f.Extrafanarts) != 0 {
		t.Errorf("javbus contributed a field it declares no capability for: %+v", f)
	}

	// The page request carries the age-gate cookie and no Referer: the HTML does not
	// need one.
	seen := r.seen()
	if len(seen) != 1 {
		t.Fatalf("the site saw %d requests, want one (the number is the address)", len(seen))
	}
	if got := seen[0].Header.Get("Cookie"); got != "dv=1" {
		t.Errorf("Cookie = %q, want the age gate's dv=1", got)
	}
	if got := seen[0].UserAgent(); got != fetch.BrowserUA {
		t.Errorf("User-Agent = %q, want the shared browser UA", got)
	}
}

func TestScrapeContributesSeriesWhenThePageHasIt(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "IPX-535")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if res.Fields.Series == nil {
		t.Fatal("series is nil, but this captured page carries a 系列 line")
	}
	if *res.Fields.Series != "いきなりSEX えっ？今ここでですか？" {
		t.Errorf("series = %q", *res.Fields.Series)
	}
}

func TestScrapeReadsA404AsNotFound(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "ZZZZ-9999")
	if err != nil {
		t.Fatalf("a 404 is an answer, not a failure: %v", err)
	}
	if res.Found {
		t.Fatal("Found must be false on a 404")
	}
	if !res.Fields.Empty() {
		t.Errorf("a notfound answer carries fields: %+v", res.Fields)
	}
}

func TestScrapeReportsTheAgeGateAsBlocked(t *testing.T) {
	// The gate is what the site answers a request without the cookie: a 302 to
	// /doc/driver-verify whose page is a 200 with the gate's title. It is not
	// notfound and not parse — the source recognises it and says so.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/doc/driver-verify" {
			_, _ = w.Write([]byte("<html><head><title>Age Verification JavBus</title></head><body>gate</body></html>"))
			return
		}
		http.Redirect(w, r, "/doc/driver-verify?referer="+r.URL.Path, http.StatusFound)
	}))
	defer srv.Close()

	f := fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}})
	src := New(f, Options{Domains: []string{srv.URL}})
	_, err := src.Scrape(context.Background(), "SSIS-001")
	if got := source.ReasonOf(err); got != source.ReasonBlocked {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonBlocked)
	}
}

func TestScrapeReportsAnUnreadablePageAsParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body><p>maintenance</p></body></html>"))
	}))
	defer srv.Close()

	f := fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}})
	src := New(f, Options{Domains: []string{srv.URL}})
	_, err := src.Scrape(context.Background(), "SSIS-001")
	if got := source.ReasonOf(err); got != source.ReasonParse {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonParse)
	}
}

func TestScrapeDoesNotWriteATransportFailureDownAsNotFound(t *testing.T) {
	// "We could not reach it" must never read as "it does not exist": this is the
	// one confusion the six reason codes exist to prevent.
	// A reserved, never-resolving host: "the exit does not work" has to be a
	// failure the test can create on any network. A closed local port is not one —
	// some networks drop the packet instead of refusing it, and then the request
	// times out instead of failing fast.
	src := New(fetch.New(fetch.Options{Retries: -1}), Options{Domains: []string{"http://no-such-host.invalid"}})

	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err == nil {
		t.Fatalf("scrape against a dead port answered ok: %+v", res)
	}
	if res.Found {
		t.Error("Found must not be true when the fetch failed")
	}
	if source.ReasonOf(err) != source.ReasonEgressUnavailable {
		t.Errorf("reason = %q, want %q", source.ReasonOf(err), source.ReasonEgressUnavailable)
	}
}

func TestFetchImageSendsTheRefererTheCdnRequires(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatal(err)
	}
	data, err := src.FetchImage(context.Background(), *res.Fields.Fanart)
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("the image came back empty")
	}
	seen := r.seen()
	if len(seen) != 2 {
		t.Fatalf("the site saw %d requests, want the page and then the image", len(seen))
	}
	if got := seen[1].Header.Get("Referer"); !strings.HasSuffix(got, "/SSIS-001") {
		t.Errorf("image Referer = %q, want the page it belongs to", got)
	}
	if got := seen[1].Header.Get("Cookie"); got != "" {
		t.Errorf("the image request sent the age-gate cookie (%q); only the page needs it", got)
	}
}

func TestScrapeSwitchesToTheSecondDomain(t *testing.T) {
	r := serveCaptures(t)
	f := fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}})
	src := New(f, Options{Domains: []string{"http://no-such-host.invalid", r.URL}})

	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("the mirror answered, so the scrape should have: %v", err)
	}
	if !res.Found || res.Fields.Title == nil {
		t.Errorf("the mirror's page was not parsed: %+v", res.Fields)
	}
	// The image addresses must follow the domain that actually answered, or the
	// operator's mirror would end up with images from the original site.
	if !strings.HasPrefix(res.Fields.Fanart.URL, r.URL) {
		t.Errorf("fanart URL = %q, want it built on the mirror that answered", res.Fields.Fanart.URL)
	}
}

func TestDumpReturnsTheRawAnswerThroughTheSameFetcher(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	captures, err := src.Dump(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("javbus has one leg, so one capture; got %d", len(captures))
	}
	capture := captures[0]
	if capture.Status != 200 {
		t.Errorf("status = %d, want 200", capture.Status)
	}
	if !strings.HasPrefix(capture.URL, r.URL+"/SSIS-001") {
		t.Errorf("captured URL = %q, want the address that answered", capture.URL)
	}
	// Byte for byte: the fixture is the page, not a re-rendered version of it.
	if string(capture.Body) != string(captureBody(t, "javbus-SSIS-001.html")) {
		t.Error("the dumped body is not the page the server sent")
	}
}

func TestDumpKeepsANotfoundPage(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	captures, err := src.Dump(context.Background(), "ZZZZ-9999")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("javbus has one leg, so one capture; got %d", len(captures))
	}
	capture := captures[0]
	if capture.Status != http.StatusNotFound {
		t.Errorf("status = %d, want the 404 kept as it came", capture.Status)
	}
}

func TestScrapeIsSafeForConcurrentUse(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := src.Scrape(context.Background(), "SSIS-001")
			if err != nil || !res.Found || res.Fields.Title == nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d of 8 concurrent scrapes failed: a Source must keep no per-call state", failures.Load())
	}
}

func TestFetchImageRefusesAnEmptyReference(t *testing.T) {
	src := New(nil, Options{})
	if _, err := src.FetchImage(context.Background(), source.ImageRef{}); err == nil {
		t.Fatal("an ImageRef without a URL must be refused")
	}
}

func TestPageURLAcceptsABareDomainOrAFullURL(t *testing.T) {
	src := New(nil, Options{})
	if got := src.pageURL("javbus.com", "SSIS-001"); got != "https://javbus.com/SSIS-001" {
		t.Errorf("bare domain = %q", got)
	}
	if got := src.pageURL("http://127.0.0.1:9001/", "SSIS-001"); got != "http://127.0.0.1:9001/SSIS-001" {
		t.Errorf("full URL = %q", got)
	}
}

func TestAgeGateMarkerIsNotATitleMatcherBug(t *testing.T) {
	// A page that merely mentions the gate in prose is not the gate: the marker is
	// the title, not the word.
	resp := &fetch.Response{URL: "https://www.javbus.com/SSIS-001", Body: []byte("<title>SSIS-001 something - JavBus</title>")}
	if isAgeGate(resp) {
		t.Error("an ordinary page was read as the age gate")
	}
	resp = &fetch.Response{URL: "https://www.javbus.com/doc/driver-verify?referer=x", Body: []byte("<html></html>")}
	if !isAgeGate(resp) {
		t.Error("the gate's own path was not recognised")
	}
}

func TestScrapeRejectsAnEmptyNumberWithoutRequesting(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)
	_, err := src.Scrape(context.Background(), "  ")
	if err == nil {
		t.Fatal("an empty number must be refused")
	}
	if len(r.seen()) != 0 {
		t.Error("an empty number still made a request")
	}
}
