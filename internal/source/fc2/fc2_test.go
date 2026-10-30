package fc2

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/fixtures"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// captureDir holds the pages the live site gave, byte for byte, written by
// `mdc source test --dump`. Tests read them and never write them.
const captureDir = "testdata/captures"

func captures(t *testing.T) []source.CaptureInfo {
	t.Helper()
	rows, err := fixtures.Load(captureDir)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return rows
}

func captureBody(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(captureDir, name))
	if err != nil {
		t.Fatalf("read the capture %s: %v", name, err)
	}
	return body
}

// replay serves the captured article pages at the paths they were captured from, so
// the real source — URL building, markers, selectors, image addresses — runs against
// real bytes. It also records the requests.
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
	byID := map[string]source.CaptureInfo{}
	for _, c := range captures(t) {
		id := articleID(strings.TrimSuffix(strings.TrimPrefix(c.Path, "fc2-FC2-PPV-"), ".html"))
		byID[id] = c
	}
	r := &replay{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.Clone(context.Background()))
		r.mu.Unlock()
		if strings.HasPrefix(req.URL.Path, "/img/") {
			// The thumbnail CDN: the page's addresses resolve here in the replay.
			_, _ = w.Write([]byte("\xff\xd8\xff\xe0 image bytes"))
			return
		}
		id := strings.Trim(strings.TrimPrefix(req.URL.Path, "/article/"), "/")
		c, ok := byID[id]
		if !ok {
			http.NotFound(w, req)
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

func TestFieldsDeclaresExactlyTheEightFieldsThePageHas(t *testing.T) {
	src := New(nil, Options{})
	got := src.Fields()
	want := map[field.Name]bool{
		field.Title: true, field.Plot: true, field.Fanart: true, field.Extrafanarts: true,
		field.Tags: true, field.Releasedate: true, field.Runtime: true, field.Studio: true,
	}
	if len(got) != len(want) {
		t.Fatalf("declared %d fields (%v), want the 8 the page has", len(got), got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("declares %q, which the page does not supply", name)
		}
	}
	// A declared field that never arrives would be a capability that lies, which is
	// why the cast list is not declared at all: FC2 is an amateur marketplace.
	for _, name := range []field.Name{field.Actors, field.Poster, field.Director, field.Rating, field.Series, field.Publisher} {
		for _, declared := range got {
			if declared == name {
				t.Errorf("declares %q, which FC2 does not have", name)
			}
		}
	}
}

func TestCapturesMatchTheirManifest(t *testing.T) {
	// The fixture discipline lives in internal/fixtures so all four sources and the
	// acceptance replay answer to the same rules; what is left here is this source's
	// own claim about what its captures are.
	for _, problem := range fixtures.Problems(captureDir) {
		t.Error(problem)
	}
	for _, c := range captures(t) {
		if !strings.Contains(c.URL, "fc2.com") {
			t.Errorf("%s: url %q does not look like an fc2 capture", c.Path, c.URL)
		}
	}
}

func TestScrapeReadsTheCapturedArticle(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "FC2-PPV-4982870")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if !res.Found {
		t.Fatal("the capture is an article, so Found must be true")
	}
	f := res.Fields

	if f.Title == nil || !strings.HasPrefix(*f.Title, "【必見！！】27歳ネイリスト") {
		t.Errorf("title = %v", f.Title)
	}
	// The anti-scraping span inside the title must not survive into the name.
	if strings.Contains(*f.Title, "pxyj") {
		t.Errorf("title carries the hidden span: %q", *f.Title)
	}
	if f.Plot == nil || !strings.HasPrefix(*f.Plot, "今回は27歳のネイリストさんです") {
		t.Errorf("plot = %v, want the description without the leading number", f.Plot)
	}
	if f.Releasedate == nil || *f.Releasedate != "2026-09-25" {
		t.Errorf("releasedate = %v, want the canonical 2026-09-25", f.Releasedate)
	}
	if f.Runtime == nil || *f.Runtime != 30 {
		t.Errorf("runtime = %v, want 30 minutes from `30:16`", f.Runtime)
	}
	if f.Studio == nil || *f.Studio != "危機回避" {
		t.Errorf("studio = %v, want the seller", f.Studio)
	}
	wantTags := []string{"素人", "美乳", "個人撮影", "フェラ", "オリジナル", "美人", "スレンダー", "色白", "パイパン", "ピンク乳首"}
	if strings.Join(f.Tags, "|") != strings.Join(wantTags, "|") {
		t.Errorf("tags = %v, want %v", f.Tags, wantTags)
	}
	if f.Fanart == nil {
		t.Fatal("no fanart: the article's thumbnail is the landscape image")
	}
	// The page's addresses are protocol-relative; the source stores absolute ones.
	if !strings.HasPrefix(f.Fanart.URL, "http") || !strings.Contains(f.Fanart.URL, "contents-thumbnail2.fc2.com") {
		t.Errorf("fanart URL = %q, want an absolute thumbnail address", f.Fanart.URL)
	}
	if len(f.Extrafanarts) != 3 {
		t.Errorf("extrafanarts = %d, want the three samples", len(f.Extrafanarts))
	}
	for i, ref := range f.Extrafanarts {
		if ref.Index != i || ref.Role != field.Extrafanarts {
			t.Errorf("extrafanart %d = %+v", i, ref)
		}
		if !strings.HasPrefix(ref.URL, "http") {
			t.Errorf("extrafanart %d URL = %q, want an absolute address", i, ref.URL)
		}
	}
	if f.Actors != nil || f.Poster != nil || f.Rating != nil {
		t.Errorf("fc2 contributed a field it declares no capability for: %+v", f)
	}

	if n := len(r.seen()); n != 1 {
		t.Errorf("the site saw %d requests, want one (the number's digits are the address)", n)
	}
	if got := r.seen()[0].URL.Path; got != "/article/4982870/" {
		t.Errorf("path = %q, want the trailing digit run", got)
	}
}

func TestScrapeReadsTheMissPageAsNotFound(t *testing.T) {
	// The miss page is HTTP 200, so the source has to recognise it by its own
	// positive marker. Reading it as "found" or as "failed" are both wrong.
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "FC2-PPV-1234567")
	if err != nil {
		t.Fatalf("a miss is an answer, not a failure: %v", err)
	}
	if res.Found {
		t.Fatal("Found must be false on the miss page")
	}
	if !res.Fields.Empty() {
		t.Errorf("a notfound answer carries fields: %+v", res.Fields)
	}
}

func TestScrapeReadsAPageWithNeitherMarkerAsParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body><p>maintenance</p></body></html>"))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{Domains: []string{srv.URL}})
	_, err := src.Scrape(context.Background(), "FC2-1")
	if got := source.ReasonOf(err); got != source.ReasonParse {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonParse)
	}
}

func TestArticleIDIsTheTrailingDigitRun(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"FC2-PPV-4982870", "4982870"},
		{"FC2PPV-4982870", "4982870"},
		{"FC2-4982870", "4982870"},
		{"FC2_PPV_4982870", "4982870"},
		{"  FC2-PPV-4982870  ", "4982870"},
		// The rule is "the trailing digit run", so a prefix that itself ends in a
		// digit still yields that digit: the point is not to fold every digit in,
		// which would make `FC2-PPV-1234567` into `21234567`.
		{"FC2-PPV", "2"},
		{"FC2", "2"},
		{"FOO-BAR", ""},
		{"", ""},
		// A non-FC2 number still ends in digits; the source is not skipped by
		// applicability rules, so this is the id it would ask about.
		{"SSIS-001", "001"},
	} {
		if got := articleID(tc.in); got != tc.want {
			t.Errorf("articleID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestScrapeFailsANumberWithNoDigitsWithoutRequesting(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	_, err := src.Scrape(context.Background(), "FOO-BAR")
	if got := source.ReasonOf(err); got != source.ReasonParse {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonParse)
	}
	if len(r.seen()) != 0 {
		t.Error("a number with no article id still made a request")
	}
	if !strings.Contains(err.Error(), "no trailing digits") {
		t.Errorf("the error does not say why: %v", err)
	}
}

func TestRuntimeMinutesReadsBothClockShapes(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"30:16", 30},
		// Whole minutes, seconds dropped — the same division dmm's seconds get.
		{"01:03:52", 63},
		{"00:00", 0},
		{"", 0},
		{"not a clock", 0},
	} {
		if got := runtimeMinutes(tc.in); got != tc.want {
			t.Errorf("runtimeMinutes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSoldDateIsCanonicalised(t *testing.T) {
	if got := soldDate("販売日 : 2026/09/25"); got != "2026-09-25" {
		t.Errorf("soldDate = %q", got)
	}
	if got := soldDate("商品ID : FC2 PPV 4982870"); got != "" {
		t.Errorf("a non-date line produced %q", got)
	}
}

func TestFetchImageUsesTheAddressAsOffered(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	data, err := src.FetchImage(context.Background(), source.ImageRef{URL: r.URL + "/img/cover.jpg"})
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("the image came back empty")
	}
	if n := len(r.seen()); n != 1 {
		t.Errorf("the CDN saw %d requests, want one", n)
	}
	if got := r.seen()[0].Header.Get("Referer"); got != "" {
		t.Errorf("the image request sent a Referer (%q); FC2's CDN wants none", got)
	}
}

func TestFetchImageRefusesAnEmptyReference(t *testing.T) {
	src := New(nil, Options{})
	if _, err := src.FetchImage(context.Background(), source.ImageRef{}); err == nil {
		t.Fatal("an ImageRef without a URL must be refused")
	}
}

func TestDumpKeepsThePageAsItCame(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	captures, err := src.Dump(context.Background(), "FC2-PPV-4982870")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(captures) != 1 {
		t.Fatalf("fc2 has one leg, so one capture; got %d", len(captures))
	}
	if string(captures[0].Body) != string(captureBody(t, "fc2-FC2-PPV-4982870.html")) {
		t.Error("the dumped body is not the page the server sent")
	}
}

func TestArticleURLAcceptsABareDomainOrAFullURL(t *testing.T) {
	if got := articleURL("adult.contents.fc2.com", "1"); got != "https://adult.contents.fc2.com/article/1/" {
		t.Errorf("bare domain = %q", got)
	}
	if got := articleURL("http://127.0.0.1:9001/", "1"); got != "http://127.0.0.1:9001/article/1/" {
		t.Errorf("full URL = %q", got)
	}
}

func TestScrapeIsSafeForConcurrentUse(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			res, err := src.Scrape(context.Background(), "FC2-PPV-4982870")
			if err != nil || !res.Found || res.Fields.Title == nil {
				errs <- fmt.Errorf("concurrent scrape: %v %+v", err, res.Fields)
				return
			}
			errs <- nil
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}
