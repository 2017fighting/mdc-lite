package dmm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// captureDir holds the GraphQL answers the live API gave, byte for byte, written by
// `mdc source test --dump`. Tests read them and never write them.
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

// replay answers a GraphQL request with the capture whose leg the request's own
// query names: the search term for a search, the content id for the content leg. The
// real source — endpoint building, headers, the two-spelling loop, envelope handling
// — runs against the API's real bytes.
type replay struct {
	*httptest.Server
	mu       sync.Mutex
	requests []recorded
}

type recorded struct {
	header http.Header
	body   string
}

func (r *replay) seen() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.requests...)
}

var (
	queryWordPattern = regexp.MustCompile(`queryWord: "([^"]+)"`)
	contentIDPattern = regexp.MustCompile(`ppvContent\(id: "([^"]+)"\)`)
)

func serveCaptures(t *testing.T) *replay {
	t.Helper()
	byLeg := map[string]source.CaptureInfo{}
	for _, c := range captures(t) {
		byLeg[c.Leg] = c
	}
	r := &replay{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, recorded{header: req.Header.Clone(), body: string(body)})
		r.mu.Unlock()

		// The query travels JSON-escaped inside {"query": "..."}, so it is unwrapped
		// before the leg is read off it — the same way the endpoint reads it.
		var envelope struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(body, &envelope)
		var leg string
		switch {
		case queryWordPattern.MatchString(envelope.Query):
			leg = "search-" + queryWordPattern.FindStringSubmatch(envelope.Query)[1]
		case contentIDPattern.MatchString(envelope.Query):
			leg = "content"
		}
		c, ok := byLeg[leg]
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"errors":[{"message":"no fixture for this query"}]}`))
			return
		}
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

func TestFieldsDeclaresAllFourteenFields(t *testing.T) {
	src := New(nil, Options{})
	if got := len(src.Fields()); got != 14 {
		t.Fatalf("declared %d fields, want all 14 (dmm has every one)", got)
	}
}

func TestCapturesMatchTheirManifest(t *testing.T) {
	for _, c := range captures(t) {
		body := captureBody(t, c.Path)
		if len(body) != c.Bytes {
			t.Errorf("%s: %d bytes on disk, manifest says %d", c.Path, len(body), c.Bytes)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != c.SHA256 {
			t.Errorf("%s: sha256 %s, manifest says %s", c.Path, got, c.SHA256)
		}
		if !strings.Contains(c.URL, "dmm.co.jp") {
			t.Errorf("%s: url %q does not look like a dmm capture", c.Path, c.URL)
		}
		if c.Leg == "" {
			t.Errorf("%s: a GraphQL capture has to say which query produced it", c.Path)
		}
		if c.Captured == "" {
			t.Errorf("%s: the manifest does not say when it was captured", c.Path)
		}
	}
}

func TestScrapeReadsTheTwoSpellingsThenTheProduct(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if !res.Found {
		t.Fatal("the captures are a hit, so Found must be true")
	}
	f := res.Fields

	if f.Title == nil || !strings.HasPrefix(*f.Title, "一ヶ月間の禁欲の果てに") {
		t.Errorf("title = %v", f.Title)
	}
	if f.Plot == nil || !strings.HasPrefix(*f.Plot, "S1スリム美女優") {
		t.Errorf("plot = %v", f.Plot)
	}
	// The API answers RFC3339; the field's canonical form is a date.
	if f.Releasedate == nil || *f.Releasedate != "2021-02-18" {
		t.Errorf("releasedate = %v, want 2021-02-18", f.Releasedate)
	}
	// The API counts seconds; the field counts minutes.
	if f.Runtime == nil || *f.Runtime != 147 {
		t.Errorf("runtime = %v, want 8826s as 147 minutes", f.Runtime)
	}
	// The API averages five; the field is ten-point.
	if f.Rating == nil || *f.Rating != 8.44 {
		t.Errorf("rating = %v, want 4.22 × 2", f.Rating)
	}
	if f.Studio == nil || *f.Studio != "エスワン ナンバーワンスタイル" {
		t.Errorf("studio = %v, want the maker", f.Studio)
	}
	if f.Publisher == nil || *f.Publisher != "S1 NO.1 STYLE" {
		t.Errorf("publisher = %v, want the label", f.Publisher)
	}
	if f.Series != nil {
		t.Errorf("series = %v, want nil: this product has no series", *f.Series)
	}
	if len(f.Director) != 1 || f.Director[0] != "苺原" {
		t.Errorf("director = %v", f.Director)
	}
	wantTags := []string{"3P・4P", "ドラマ", "美少女", "寝取り・寝取られ・NTR", "ギリモザ", "美乳", "独占配信", "ハイビジョン"}
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
	if f.Poster == nil || !strings.HasSuffix(f.Poster.URL, "ssis00001pl.jpg") {
		t.Errorf("poster = %+v, want the package image", f.Poster)
	}
	// The landscape cover is the first sample; the samples are all kept.
	if f.Fanart == nil || !strings.HasSuffix(f.Fanart.URL, "ssis00001jp-1.jpg") {
		t.Errorf("fanart = %+v, want the first sample", f.Fanart)
	}
	if len(f.Extrafanarts) != 10 {
		t.Errorf("extrafanarts = %d, want all ten samples", len(f.Extrafanarts))
	}
	for i, ref := range f.Extrafanarts {
		if ref.Role != field.Extrafanarts || ref.Index != i {
			t.Errorf("extrafanart %d = %+v, want role extrafanarts and its own index", i, ref)
		}
	}

	// Three requests: the reduced spelling, the padded spelling, then one content
	// POST that carries both the product and its review summary.
	seen := r.seen()
	if len(seen) != 3 {
		t.Fatalf("the API saw %d requests, want two searches and one content", len(seen))
	}
	if got := queryWord(t, seen[0]); got != "ssis001" {
		t.Errorf("first search term = %q, want the reduced spelling first", got)
	}
	if got := queryWord(t, seen[1]); got != "ssis00001" {
		t.Errorf("second search term = %q, want the padded spelling", got)
	}
	last := queryOf(t, seen[2])
	if !strings.Contains(last, `ppvContent(id: "ssis00001")`) {
		t.Errorf("third request does not read the product: %s", last)
	}
	if !strings.Contains(last, "reviewSummary(contentId:") {
		t.Error("the review summary is not in the same POST as the product")
	}
	for i, req := range seen {
		if got := req.header.Get("Content-Type"); got != "application/json" {
			t.Errorf("request %d Content-Type = %q", i, got)
		}
		if got := req.header.Get("Fanza-Device"); got != "BROWSER" {
			t.Errorf("request %d Fanza-Device = %q", i, got)
		}
		if got := req.header.Get("Referer"); !strings.HasSuffix(got, "/") {
			t.Errorf("request %d Referer = %q", i, got)
		}
		if got := req.header.Get("Origin"); got != "" {
			t.Errorf("request %d sent an Origin (%q); the source sends none", i, got)
		}
	}
}

// queryOf unwraps the {"query": "..."} envelope the source sends.
func queryOf(t *testing.T, rec recorded) string {
	t.Helper()
	var envelope struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(rec.body), &envelope); err != nil {
		t.Fatalf("the request body is not a GraphQL envelope: %v", err)
	}
	return envelope.Query
}

// queryWord reads the search term out of one request.
func queryWord(t *testing.T, rec recorded) string {
	t.Helper()
	m := queryWordPattern.FindStringSubmatch(queryOf(t, rec))
	if m == nil {
		return ""
	}
	return m[1]
}

func TestScrapeReadsZeroCandidatesAsNotFound(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "ZZZZ-9999")
	if err != nil {
		t.Fatalf("a zero-candidate search is an answer, not a failure: %v", err)
	}
	if res.Found {
		t.Fatal("Found is true for a number the API has no candidate for")
	}
	if n := len(r.seen()); n != 2 {
		t.Errorf("the API saw %d requests, want both spellings and no content leg", n)
	}
}

func TestSearchTermsTryTheReducedSpellingThenThePaddedOne(t *testing.T) {
	for _, tc := range []struct {
		number string
		want   []string
	}{
		{"SSIS-001", []string{"ssis001", "ssis00001"}},
		{"IPX-535", []string{"ipx535", "ipx00535"}},
		{"SSIS-00129", []string{"ssis00129"}},
		{"FC2-PPV-1234567", []string{"fc2ppv1234567"}},
		{"ZZZZ-9999", []string{"zzzz9999", "zzzz09999"}},
		{"NO-DIGITS", []string{"nodigits"}},
	} {
		if got := strings.Join(searchTerms(tc.number), ","); got != strings.Join(tc.want, ",") {
			t.Errorf("searchTerms(%q) = %s, want %s", tc.number, got, strings.Join(tc.want, ","))
		}
	}
}

func TestSameWorkComparesIdentityNotSpelling(t *testing.T) {
	for _, tc := range []struct {
		id, number string
		want       bool
	}{
		{"ssis00129", "SSIS-00129", true},
		{"ssis00001", "SSIS-001", true},
		{"ssis001", "SSIS-0011", false},
		{"ssis00001", "SSIS-002", false},
		{"nothing-numeric", "nothing-numeric", true},
	} {
		if got := sameWork(tc.id, tc.number); got != tc.want {
			t.Errorf("sameWork(%q, %q) = %v, want %v", tc.id, tc.number, got, tc.want)
		}
	}
}

func TestScrapeNamesTheJapaneseExitWhenTheRegionIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"not-available-in-your-region"}]}`))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{Domains: []string{srv.URL}})
	_, err := src.Scrape(context.Background(), "SSIS-001")
	if got := source.ReasonOf(err); got != source.ReasonBlocked {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonBlocked)
	}
	for _, want := range []string{"Japan", "proxy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

func TestScrapeReadsOtherGraphQLErrorsAsParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errors":[{"message":"Cannot query field \"nope\""}]}`))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{Domains: []string{srv.URL}})
	_, err := src.Scrape(context.Background(), "SSIS-001")
	if got := source.ReasonOf(err); got != source.ReasonParse {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonParse)
	}
	if strings.Contains(err.Error(), "Japan") {
		t.Errorf("an ordinary schema error was blamed on the exit: %v", err)
	}
}

func TestScrapeReadsANullProductAsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if queryWordPattern.Match(body) {
			_, _ = w.Write([]byte(`{"data":{"legacySearchPPV":{"result":{"contents":[{"id":"ssis00001"}],"pageInfo":{"totalCount":1}}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ppvContent":null,"reviewSummary":null}}`))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{Domains: []string{srv.URL}})
	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("a null product is `no such work`, not a failure: %v", err)
	}
	if res.Found {
		t.Fatal("Found is true for a null product")
	}
}

func TestDateOfPrefersTheDeliveryDate(t *testing.T) {
	for _, tc := range []struct{ delivery, maker, want string }{
		{"2021-02-18T01:00:00Z", "2021-02-18T15:00:00Z", "2021-02-18"},
		{"", "2021-02-18T15:00:00Z", "2021-02-18"},
		{"2021-07-16T00:00:00Z", "", "2021-07-16"},
		{"2021-07-16", "", "2021-07-16"},
		{"", "", ""},
		{"garbage", "", ""},
	} {
		if got := dateOf(tc.delivery, tc.maker); got != tc.want {
			t.Errorf("dateOf(%q, %q) = %q, want %q", tc.delivery, tc.maker, got, tc.want)
		}
	}
}

func TestEndpointsBuildTheApiAndItsReferer(t *testing.T) {
	api, referer := endpoints("dmm.co.jp")
	if api != "https://api.video.dmm.co.jp/graphql" {
		t.Errorf("api = %q", api)
	}
	if referer != "https://video.dmm.co.jp/" {
		t.Errorf("referer = %q", referer)
	}
	api, referer = endpoints("www.dmm.com")
	if api != "https://api.video.dmm.com/graphql" || referer != "https://video.dmm.com/" {
		t.Errorf("a configured www. host produced %q / %q", api, referer)
	}
	// A full URL is a captured-page replay taking over this source.
	api, referer = endpoints("http://127.0.0.1:9001/")
	if api != "http://127.0.0.1:9001/graphql" || referer != "http://127.0.0.1:9001/" {
		t.Errorf("a full URL produced %q / %q", api, referer)
	}
}

func TestFetchImageUsesTheAddressAsOffered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("\xff\xd8\xff\xe0 poster"))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{})
	data, err := src.FetchImage(context.Background(), source.ImageRef{URL: srv.URL + "/poster.jpg"})
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("the image came back empty")
	}
}

func TestFetchImageRefusesAnEmptyReference(t *testing.T) {
	src := New(nil, Options{})
	if _, err := src.FetchImage(context.Background(), source.ImageRef{}); err == nil {
		t.Fatal("an ImageRef without a URL must be refused")
	}
}

func TestDumpKeepsEveryLegItAsked(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	captures, err := src.Dump(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	want := []string{"search-ssis001", "search-ssis00001", "content"}
	if len(captures) != len(want) {
		t.Fatalf("dump = %d captures, want %d", len(captures), len(want))
	}
	for i, leg := range want {
		if captures[i].Leg != leg {
			t.Errorf("capture %d leg = %q, want %q", i, captures[i].Leg, leg)
		}
	}
	if string(captures[2].Body) != string(captureBody(t, "dmm-SSIS-001-content.json")) {
		t.Error("the dumped content is not the answer the server sent")
	}
}

func TestScrapeSendsEachMirrorItsOwnReferer(t *testing.T) {
	// The Referer names the site the request is for, so the fallback domain must get
	// its own rather than the first domain's.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	healthy := serveCaptures(t)

	f := fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}, Retries: 1})
	src := New(f, Options{Domains: []string{broken.URL, healthy.URL}})

	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("the mirror answered, so the scrape should have: %v", err)
	}
	if !res.Found {
		t.Fatal("the mirror's answer was not read")
	}
	seen := healthy.seen()
	if len(seen) == 0 {
		t.Fatal("the mirror was never asked")
	}
	if got, want := seen[0].header.Get("Referer"), healthy.URL+"/"; got != want {
		t.Errorf("mirror Referer = %q, want its own %q", got, want)
	}
}
