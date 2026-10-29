package javdb

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// captureDir holds the answers the live API gave, byte for byte, written by
// `mdc source test --dump`. Tests read them and never write them: a fixture a test
// could edit stops being evidence.
const captureDir = "testdata/captures"

// signaturePattern is the recipe's shape: a unix stamp, the salt, and the md5.
var signaturePattern = regexp.MustCompile(`^\d+\.lpw6vgqzsp\.[0-9a-f]{32}$`)

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

// replay serves the captured API answers at the addresses they were captured from:
// a search by its `q`, a detail by its id. The real source runs against real bytes —
// URL building, signature header, envelope handling and field mapping all included.
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
	r := &replay{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.Clone(context.Background()))
		r.mu.Unlock()

		path := req.URL.Path
		var name string
		switch {
		case path == "/api/v2/search":
			name = "javdb-" + req.URL.Query().Get("q") + "-search.json"
		case strings.HasPrefix(path, "/api/v4/movies/"):
			// The detail capture is named after the number, not the opaque id, so
			// find the one whose recorded URL carries this id.
			for _, c := range captures(t) {
				if strings.Contains(c.URL, path) {
					name = c.Path
				}
			}
			if name == "" {
				// SSIS-001's detail capture is the only detail in the fixture set.
				name = "javdb-SSIS-001-detail.json"
			}
		default:
			http.NotFound(w, req)
			return
		}
		body, err := os.ReadFile(filepath.Join(captureDir, name))
		if err != nil {
			// A 404 for the detail leg is the API's own "no such id".
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
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
	got := src.Fields()
	if len(got) != 14 {
		t.Fatalf("declared %d fields, want all 14 (javdb has every one)", len(got))
	}
	seen := map[field.Name]bool{}
	for _, name := range got {
		seen[name] = true
	}
	for _, name := range field.All() {
		if !seen[name] {
			t.Errorf("%q is missing from the declaration", name)
		}
	}
}

func TestCapturesMatchTheirManifest(t *testing.T) {
	// The fixture discipline: the manifest says what the API answered, and the bytes
	// on disk have to be those bytes.
	for _, c := range captures(t) {
		body := captureBody(t, c.Path)
		if len(body) != c.Bytes {
			t.Errorf("%s: %d bytes on disk, manifest says %d", c.Path, len(body), c.Bytes)
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != c.SHA256 {
			t.Errorf("%s: sha256 %s, manifest says %s", c.Path, got, c.SHA256)
		}
		if !strings.Contains(c.URL, "javdb") && !strings.Contains(c.URL, "jdforrepam") {
			t.Errorf("%s: url %q does not look like a javdb capture", c.Path, c.URL)
		}
		if c.Captured == "" {
			t.Errorf("%s: the manifest does not say when it was captured", c.Path)
		}
		if c.Leg == "" {
			t.Errorf("%s: a two-leg capture has to name its leg", c.Path)
		}
	}
}

func TestScrapeReadsTheCapturedSearchAndDetail(t *testing.T) {
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

	if f.Title == nil || !strings.HasPrefix(*f.Title, "刺激您五感的三上悠亞") {
		t.Errorf("title = %v", f.Title)
	}
	if f.Plot == nil || !strings.Contains(*f.Plot, "三上悠亞史上最棒") {
		t.Errorf("plot = %v", f.Plot)
	}
	if f.Releasedate == nil || *f.Releasedate != "2021-02-19" {
		t.Errorf("releasedate = %v, want 2021-02-19", f.Releasedate)
	}
	if f.Runtime == nil || *f.Runtime != 150 {
		t.Errorf("runtime = %v, want 150 minutes", f.Runtime)
	}
	// The site's score is five-point; the field is ten-point.
	if f.Rating == nil || *f.Rating != 8.86 {
		t.Errorf("rating = %v, want 4.43 × 2", f.Rating)
	}
	if f.Series == nil || !strings.HasPrefix(*f.Series, "一ヶ月間の禁欲") {
		t.Errorf("series = %v", f.Series)
	}
	if f.Studio == nil || *f.Studio != "S1 NO.1 STYLE" {
		t.Errorf("studio = %v", f.Studio)
	}
	if f.Publisher != nil {
		t.Errorf("publisher = %v, want nil: this movie has no publisher", *f.Publisher)
	}
	if len(f.Director) != 1 || f.Director[0] != "苺原" {
		t.Errorf("director = %v", f.Director)
	}
	wantTags := []string{"戏剧", "美乳", "白天出轨", "美少女电影", "无码破解", "两女一男"}
	if strings.Join(f.Tags, "|") != strings.Join(wantTags, "|") {
		t.Errorf("tags = %v, want %v", f.Tags, wantTags)
	}
	// The men are kept: a co-starring work would lose half its cast otherwise.
	wantActors := []meta.Actor{
		{Name: "葵つかさ", Gender: meta.Female},
		{Name: "乙白さやか", Gender: meta.Female},
		{Name: "平田司", Gender: meta.Male},
	}
	if len(f.Actors) != len(wantActors) {
		t.Fatalf("actors = %v, want %v", f.Actors, wantActors)
	}
	for i, want := range wantActors {
		if f.Actors[i] != want {
			t.Errorf("actor %d = %v, want %v", i, f.Actors[i], want)
		}
	}
	if f.Poster == nil || !strings.Contains(f.Poster.URL, "/small_covers/") {
		t.Errorf("poster = %+v, want the app CDN's small cover", f.Poster)
	}
	if f.Fanart == nil || !strings.Contains(f.Fanart.URL, "/covers/") {
		t.Errorf("fanart = %+v, want the app CDN's cover", f.Fanart)
	}
	if len(f.Extrafanarts) != 10 {
		t.Errorf("extrafanarts = %d, want all ten samples", len(f.Extrafanarts))
	}
	for i, ref := range f.Extrafanarts {
		if ref.Role != field.Extrafanarts || ref.Index != i {
			t.Errorf("extrafanart %d = %+v, want role extrafanarts and its own index", i, ref)
		}
	}

	// Two requests: the search, then the detail. Both carry a fresh signature.
	seen := r.seen()
	if len(seen) != 2 {
		t.Fatalf("the API saw %d requests, want a search and a detail", len(seen))
	}
	if !strings.HasPrefix(seen[0].URL.Path, "/api/v2/search") {
		t.Errorf("first request = %s, want the search leg", seen[0].URL)
	}
	if seen[0].URL.Query().Get("q") != "SSIS-001" {
		t.Errorf("search q = %q", seen[0].URL.Query().Get("q"))
	}
	if !strings.HasPrefix(seen[1].URL.Path, "/api/v4/movies/ZY5eq") {
		t.Errorf("second request = %s, want the detail leg", seen[1].URL)
	}
	for i, req := range seen {
		sig := req.Header.Get("jdsignature")
		if !signaturePattern.MatchString(sig) {
			t.Errorf("request %d signature = %q, want {{unix}}.{{salt}}.{{md5}}", i, sig)
		}
	}
	if got := seen[0].UserAgent(); got != fetch.BrowserUA {
		t.Errorf("User-Agent = %q, want the shared browser UA", got)
	}
}

func TestScrapeNeverTakesTheFirstCandidate(t *testing.T) {
	// The captured search for ZZZZ-9999 answers with the near-miss ZZZM-999: the
	// exact-match rule is what keeps someone else's work off this number.
	r := serveCaptures(t)
	src := newSource(t, r)

	res, err := src.Scrape(context.Background(), "ZZZZ-9999")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if res.Found {
		t.Fatalf("Found is true for a number the API only has neighbours for: %+v", res.Fields)
	}
	if !res.Fields.Empty() {
		t.Errorf("a notfound answer carries fields: %+v", res.Fields)
	}
	if n := len(r.seen()); n != 1 {
		t.Errorf("the API saw %d requests, want just the search: a miss spends no detail leg", n)
	}
}

func TestScrapeRewritesFC2NumbersForBothLegs(t *testing.T) {
	// FC2-PPV-<digits> finds nothing; FC2-<digits> finds the work, so the rewritten
	// spelling is what the search (and the match) uses.
	for _, tc := range []struct{ in, want string }{
		{"FC2-PPV-3336019", "FC2-3336019"},
		{"FC2PPV-3336019", "FC2-3336019"},
		{"FC2_3336019", "FC2-3336019"},
		{"FC2-3336019", "FC2-3336019"},
		{"FC2-PPV", "FC2-PPV"},
		{"SSIS-001", "SSIS-001"},
	} {
		if got := searchNumber(tc.in); got != tc.want {
			t.Errorf("searchNumber(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	r := serveCaptures(t)
	src := newSource(t, r)
	if _, err := src.Scrape(context.Background(), "FC2-PPV-3336019"); err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if got := r.seen()[0].URL.Query().Get("q"); got != "FC2-3336019" {
		t.Errorf("search q = %q, want the rewritten spelling", got)
	}
}

func TestScrapeReadsARefusedRequestAsBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":0,"action":"ExpiredSignature","message":"簽名已過期","data":null}`))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{Domains: []string{srv.URL}})
	_, err := src.Scrape(context.Background(), "SSIS-001")
	if got := source.ReasonOf(err); got != source.ReasonBlocked {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonBlocked)
	}
	if !strings.Contains(err.Error(), "ExpiredSignature") {
		t.Errorf("the error does not name the site's own action: %v", err)
	}
}

func TestScrapeReadsANonJSONAnswerAsParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>maintenance</html>"))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{Domains: []string{srv.URL}})
	_, err := src.Scrape(context.Background(), "SSIS-001")
	if got := source.ReasonOf(err); got != source.ReasonParse {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonParse)
	}
}

func TestSignatureIsFreshAndFollowsTheRecipe(t *testing.T) {
	pinned := time.Unix(1793250628, 0)
	src := New(nil, Options{Domains: []string{"jdforrepam.com"}})
	src.now = func() time.Time { return pinned }

	got := src.signature()
	sum := md5.Sum([]byte("1793250628" + signKey))
	want := "1793250628." + signSalt + "." + hex.EncodeToString(sum[:])
	if got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
	// A second request at a later second gets a different signature: a cached one
	// would turn a long task into a stream of refusals.
	src.now = func() time.Time { return pinned.Add(2 * time.Second) }
	if again := src.signature(); again == got {
		t.Error("the signature did not change with the clock")
	}
}

func TestDecodeArtifactTurnsTheAppEncodingBackIntoAJPEG(t *testing.T) {
	plain := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'j', 'p', 'e', 'g'}
	if got, err := decodeArtifact(plain); err != nil || string(got) != string(plain) {
		t.Fatalf("a plain JPEG was not passed through: %v (%v)", got, err)
	}

	key := byte(0x89)
	encoded := []byte{key}
	for _, b := range plain {
		encoded = append(encoded, b^key)
	}
	if bytes, err := decodeArtifact(encoded); err != nil || string(bytes) != string(plain) {
		t.Fatalf("the XOR body did not decode: %v (%v)", bytes, err)
	}

	if _, err := decodeArtifact(nil); err == nil {
		t.Error("an empty body decoded to something")
	}
	if _, err := decodeArtifact([]byte("definitely not an image")); err == nil {
		t.Error("a body that decodes to garbage was accepted as an image")
	}
}

func TestFetchImageDecodesTheAppCDN(t *testing.T) {
	plain := []byte{0xFF, 0xD8, 0xFF, 0xE0, 'p', 'i', 'x'}
	key := byte(0x3E)
	encoded := []byte{key}
	for _, b := range plain {
		encoded = append(encoded, b^key)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(encoded)
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{})
	got, err := src.FetchImage(context.Background(), source.ImageRef{URL: srv.URL + "/covers/x.jpg"})
	if err != nil {
		t.Fatalf("fetch image: %v", err)
	}
	if string(got) != string(plain) {
		t.Errorf("decoded %v, want the JPEG bytes", got)
	}
}

func TestFetchImageRefusesAnEmptyReference(t *testing.T) {
	src := New(nil, Options{})
	if _, err := src.FetchImage(context.Background(), source.ImageRef{}); err == nil {
		t.Fatal("an ImageRef without a URL must be refused")
	}
}

func TestScrapeRejectsAnEmptyNumberWithoutRequesting(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)
	if _, err := src.Scrape(context.Background(), "  "); err == nil {
		t.Fatal("an empty number must be refused")
	}
	if len(r.seen()) != 0 {
		t.Error("an empty number still made a request")
	}
}

func TestDumpKeepsBothLegs(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	captures, err := src.Dump(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(captures) != 2 {
		t.Fatalf("dump = %d captures, want a search and a detail", len(captures))
	}
	if captures[0].Leg != "search" || captures[1].Leg != "detail" {
		t.Errorf("legs = %q, %q", captures[0].Leg, captures[1].Leg)
	}
	// Byte for byte: the fixture is the answer, not a re-rendered version of it.
	if string(captures[0].Body) != string(captureBody(t, "javdb-SSIS-001-search.json")) {
		t.Error("the dumped search is not the answer the server sent")
	}
	if string(captures[1].Body) != string(captureBody(t, "javdb-SSIS-001-detail.json")) {
		t.Error("the dumped detail is not the answer the server sent")
	}
}

func TestDumpKeepsAMissAsItsSearchLeg(t *testing.T) {
	r := serveCaptures(t)
	src := newSource(t, r)

	captures, err := src.Dump(context.Background(), "ZZZZ-9999")
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if len(captures) != 1 || captures[0].Leg != "search" {
		t.Fatalf("dump = %+v, want just the search leg", captures)
	}
}

func TestExtrafanartsAreIndexedAcrossTheOnesThatExist(t *testing.T) {
	// The site's array can have a hole in it. The index is the still's position among
	// the stills that exist, so the output writes fanart1/fanart2 — not fanart1/fanart3.
	search := `{"success":1,"action":null,"message":null,"data":{"movies":[{"id":"X","number":"SSIS-001"}]}}`
	detail := `{"success":1,"action":null,"message":null,"data":{"movie":{"id":"X","number":"SSIS-001","title":"a title",` +
		`"preview_images":[{"large_url":"https://cdn/0.jpg"},{"large_url":""},{"large_url":"https://cdn/2.jpg"}]}}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v2/search") {
			_, _ = w.Write([]byte(search))
			return
		}
		_, _ = w.Write([]byte(detail))
	}))
	defer srv.Close()

	src := New(fetch.New(fetch.Options{Rate: fetch.Rate{RequestsPerSecond: 1000, Burst: 100}}), Options{Domains: []string{srv.URL}})
	res, err := src.Scrape(context.Background(), "SSIS-001")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	refs := res.Fields.Extrafanarts
	if len(refs) != 2 {
		t.Fatalf("extrafanarts = %d, want the two that have an address", len(refs))
	}
	for i, ref := range refs {
		if ref.Index != i {
			t.Errorf("extrafanart %d has index %d, want the position among the ones that exist", i, ref.Index)
		}
	}
	if !strings.HasSuffix(refs[1].URL, "/2.jpg") {
		t.Errorf("second still = %q", refs[1].URL)
	}
}
