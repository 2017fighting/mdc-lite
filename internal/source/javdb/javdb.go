// Package javdb is the javdb APP JSON API: a search leg to find the site's opaque
// movie id, then a detail leg to read the movie itself.
//
// The website `javdb.com` refuses this kind of client at the TLS layer, but the
// application's own API answers on mirror domains (the factory default is
// `jdforrepam.com`), and the domain is the operator's to change. The API wants a
// `jdsignature` header, which is the APP's shared protocol constant and therefore a
// code constant too: a configurable signature would invite the operator to fill in
// their own and get every request refused.
//
// The number is not the address. A search answers with neighbours as well as the
// work itself (`SSIS-0011` answers with `SSIS-011` first), so the match is exact and
// positional: never "the first candidate".
package javdb

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// ID is the name configuration refers to this source by.
const ID = "javdb"

// defaultDomains is the API host, then the mirror the API advertises. The website's
// own `javdb.com` is not here: it is refused at the TLS layer from many exits, while
// the API host works, and the availability is a property of the API domain.
var defaultDomains = []string{"jdforrepam.com", "javdb580.com"}

// The signature recipe, taken from the APP: `{unix}.{salt}.{md5(unix+key)}`. These
// are the application's shared protocol constants, not deployment credentials, so
// they are code constants: a "secret" field in the config would only invite the
// operator to fill in a value that is wrong by definition.
const (
	signSalt = "lpw6vgqzsp"
	signKey  = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
)

// searchLimit is the site's own page width. It is fixed for the one search the
// source makes, so there is no paging and no cursor: one request either contains the
// work or the site does not have it.
const searchLimit = 24

// jpegMagic is what a decodable image starts with. The APP CDN returns
// `key || (jpeg XOR key)`; the site CDN returns the plain JPEG. Both arrive here.
var jpegMagic = []byte{0xFF, 0xD8, 0xFF}

// declared is the static capability declaration. javdb has all fourteen
// source-level fields: it is the widest of the four sources.
var declared = field.All()

// fc2Number rewrites an FC2 number into the spelling the site's search index uses:
// `FC2-PPV-3336019` finds nothing, `FC2-3336019` finds the work. The digits are
// required, so a bare `FC2-PPV` is not reduced to `FC2` + an empty id.
var fc2Number = regexp.MustCompile(`(?i)^FC2[-_ ]?(?:PPV[-_ ]?)?(\d+)$`)

// Fetcher is the transport seam: proxy, rate limit, cooldown, retries and
// cancellation are all the Fetcher's business, and this source never retries.
type Fetcher interface {
	Do(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// Options are the four knobs `scrape.with.sources.javdb` opens.
type Options struct {
	Proxy   string
	Domains []string
	Rate    *fetch.Rate
	Timeout time.Duration
}

// Source is javdb. It holds no per-call state, so one instance serves every task at
// once.
type Source struct {
	fetcher Fetcher
	opts    Options
	// now is the clock the signature is stamped from. It is a field only so a test
	// can pin the timestamp; production leaves it alone.
	now func() time.Time
}

// New builds the source on a Fetcher.
func New(f Fetcher, opts Options) *Source {
	if len(opts.Domains) == 0 {
		opts.Domains = defaultDomains
	}
	return &Source{fetcher: f, opts: opts, now: time.Now}
}

// ID implements source.Source.
func (s *Source) ID() string { return ID }

// Fields is the static capability declaration: all fourteen fields.
func (s *Source) Fields() []field.Name {
	out := make([]field.Name, len(declared))
	copy(out, declared)
	return out
}

// Scrape asks the search leg, matches the work exactly, then reads the detail leg.
func (s *Source) Scrape(ctx context.Context, number string) (source.Result, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return source.Result{}, source.Fail(source.ReasonParse, errors.New("javdb: the number is empty"))
	}
	want := searchNumber(number)
	id, found, err := s.search(ctx, want)
	if err != nil {
		return source.Result{}, err
	}
	if !found {
		return source.Result{Found: false}, nil
	}
	movie, err := s.detail(ctx, id)
	if err != nil {
		return source.Result{}, err
	}
	if movie == nil {
		return source.Result{Found: false}, nil
	}
	return source.Result{Found: true, Fields: fieldsOf(*movie)}, nil
}

// FetchImage fetches one image and decodes it. The decoder stays inside the source
// because the encoding is the site's own; the engine only sniffs the magic bytes.
func (s *Source) FetchImage(ctx context.Context, ref source.ImageRef) ([]byte, error) {
	if strings.TrimSpace(ref.URL) == "" {
		return nil, errors.New("javdb: the image reference has no URL")
	}
	body, err := s.get(ctx, []string{ref.URL}, ref.Headers)
	if err != nil {
		return nil, err
	}
	return decodeArtifact(body)
}

// Dump implements source.Dumper: the search leg, then the detail leg when the search
// found the work. A search that finds nothing is still a fixture: it is the page
// whose exact-match rule has to be tested.
func (s *Source) Dump(ctx context.Context, number string) ([]source.Capture, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, errors.New("javdb: the number is empty")
	}
	want := searchNumber(number)
	searchURLs := s.urls(func(base string) string { return s.searchURL(base, want) })
	resp, err := s.fetch(ctx, searchURLs, nil)
	if err != nil {
		return nil, err
	}
	captures := []source.Capture{{Leg: "search", URL: resp.URL, Status: resp.Status, Body: resp.Body}}
	id, found, err := s.matchSearch(resp, want)
	if err != nil || !found {
		return captures, err
	}
	detailURLs := s.urls(func(base string) string { return s.detailURL(base, id) })
	resp, err = s.fetch(ctx, detailURLs, nil)
	if err != nil {
		return captures, err
	}
	return append(captures, source.Capture{Leg: "detail", URL: resp.URL, Status: resp.Status, Body: resp.Body}), nil
}

// search runs the search leg and returns the id of the exact match.
func (s *Source) search(ctx context.Context, want string) (string, bool, error) {
	urls := s.urls(func(base string) string { return s.searchURL(base, want) })
	resp, err := s.fetch(ctx, urls, nil)
	if err != nil {
		return "", false, err
	}
	return s.matchSearch(resp, want)
}

// matchSearch reads a search answer and picks the candidate whose number is the
// work asked for. Every candidate is compared — the site's ordering is not evidence,
// and two searches for the same number can reorder the neighbours.
func (s *Source) matchSearch(resp *fetch.Response, wanted string) (string, bool, error) {
	data, err := s.data(resp, "search")
	if err != nil {
		return "", false, err
	}
	if len(data) == 0 {
		return "", false, nil
	}
	var payload struct {
		Movies []struct {
			ID     string `json:"id"`
			Number string `json:"number"`
		} `json:"movies"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", false, source.Fail(source.ReasonParse, fmt.Errorf("javdb: the search answer is not the expected JSON: %w", err))
	}
	for _, m := range payload.Movies {
		if m.ID == "" || m.Number == "" {
			continue
		}
		if source.NumberKey(m.Number) == source.NumberKey(wanted) {
			return m.ID, true, nil
		}
	}
	return "", false, nil
}

// detail runs the detail leg and returns the movie, or nil for a 404.
func (s *Source) detail(ctx context.Context, id string) (*movie, error) {
	urls := s.urls(func(base string) string { return s.detailURL(base, id) })
	resp, err := s.fetch(ctx, urls, nil)
	if err != nil {
		return nil, err
	}
	data, err := s.data(resp, "detail")
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var payload struct {
		Movie *movie `json:"movie"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, source.Fail(source.ReasonParse, fmt.Errorf("javdb: the detail answer is not the expected JSON: %w", err))
	}
	return payload.Movie, nil
}

// data unwraps the API's envelope, which is where the site puts its diagnosis: a
// missing signature answers HTTP 200 with `success: 0` and `action:
// "ParameterInvalid"`, while an unknown id answers 404. "The site refused the
// request" is `blocked`, not `notfound`, so it can never be written down as "no such
// work".
func (s *Source) data(resp *fetch.Response, leg string) (json.RawMessage, error) {
	if resp.Status == http.StatusNotFound {
		return nil, nil
	}
	var env struct {
		Success int             `json:"success"`
		Action  string          `json:"action"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, source.Fail(source.ReasonParse, fmt.Errorf("javdb: the %s answer is not JSON: %w", leg, err))
	}
	if env.Success != 1 {
		return nil, source.Fail(source.ReasonBlocked, fmt.Errorf(
			"javdb: the API refused the %s request: action %q, message %q", leg, env.Action, env.Message))
	}
	return env.Data, nil
}

// get performs one request. The signature is computed here, per request, because it
// expires: a cached one would turn a long task into a stream of refusals.
func (s *Source) get(ctx context.Context, urls []string, headers map[string]string) ([]byte, error) {
	resp, err := s.fetch(ctx, urls, headers)
	if err != nil {
		return nil, err
	}
	if resp.Status == http.StatusNotFound {
		// An image that does not exist is not "no such work" of the number and not a
		// transport failure: the field simply did not arrive, so the next source gets
		// its chance.
		return nil, fmt.Errorf("javdb: the image %s does not exist", resp.URL)
	}
	return resp.Body, nil
}

// fetch sends one request with the signature, over the first domain with the rest as
// mirrors.
func (s *Source) fetch(ctx context.Context, urls []string, headers map[string]string) (*fetch.Response, error) {
	if len(urls) == 0 {
		return nil, errors.New("javdb: no domain to ask")
	}
	h := map[string]string{"jdsignature": s.signature()}
	for k, v := range headers {
		h[k] = v
	}
	return s.fetcher.Do(ctx, fetch.Request{
		URL:     urls[0],
		Mirrors: urls[1:],
		Headers: h,
		Proxy:   s.opts.Proxy,
		Rate:    s.opts.Rate,
		Timeout: s.opts.Timeout,
	})
}

// signature is `{unix}.{salt}.{md5(unix+key)}`, fresh for every request.
func (s *Source) signature() string {
	unix := strconv.FormatInt(s.now().Unix(), 10)
	sum := md5.Sum([]byte(unix + signKey))
	return unix + "." + signSalt + "." + hex.EncodeToString(sum[:])
}

// urls builds one address per configured domain, in order, so the Fetcher can move
// to the next mirror when one is refused.
func (s *Source) urls(build func(base string) string) []string {
	out := make([]string, 0, len(s.opts.Domains))
	for _, domain := range s.opts.Domains {
		out = append(out, build(base(domain)))
	}
	return out
}

func (s *Source) searchURL(base, number string) string {
	q := url.Values{}
	q.Set("q", number)
	q.Set("from_recent", "false")
	q.Set("type", "movie")
	q.Set("movie_type", "all")
	q.Set("movie_sort_by", "relevance")
	q.Set("movie_filter_by", "all")
	q.Set("page", "1")
	q.Set("limit", strconv.Itoa(searchLimit))
	return base + "/api/v2/search?" + q.Encode()
}

func (s *Source) detailURL(base, id string) string {
	return base + "/api/v4/movies/" + url.PathEscape(id) + "?from_rankings=false"
}

// base turns a configured domain into an origin. A bare host means https; a full URL
// is how a captured-page replay takes over this source while every other line —
// selector, header, signature — stays the production one.
func base(domain string) string {
	base := strings.TrimRight(domain, "/")
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return base
}

// searchNumber is the spelling the search index knows: FC2 is rewritten, everything
// else passes through.
func searchNumber(number string) string {
	number = strings.TrimSpace(number)
	if parts := fc2Number.FindStringSubmatch(number); parts != nil {
		return "FC2-" + parts[1]
	}
	return number
}

// movie is the detail payload, cut down to the fields this build reads. Nullable
// strings stay strings: json decodes null into "" and the field mapping treats ""
// as "not supplied".
type movie struct {
	ID            string `json:"id"`
	Number        string `json:"number"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	ThumbURL      string `json:"thumb_url"`
	CoverURL      string `json:"cover_url"`
	Duration      int    `json:"duration"`
	Score         string `json:"score"`
	ReleaseDate   string `json:"release_date"`
	MakerName     string `json:"maker_name"`
	DirectorName  string `json:"director_name"`
	PublisherName string `json:"publisher_name"`
	SeriesName    string `json:"series_name"`

	Tags []struct {
		Name string `json:"name"`
	} `json:"tags"`
	Actors []struct {
		Name   string `json:"name"`
		Gender int    `json:"gender"`
	} `json:"actors"`
	PreviewImages []struct {
		ThumbURL string `json:"thumb_url"`
		LargeURL string `json:"large_url"`
	} `json:"preview_images"`
}

// fieldsOf maps the site's keys onto the 14 source-level fields. The site's own
// normalisation is done here, not downstream: `rating` leaves as a 0–10 number,
// `releasedate` as `YYYY-MM-DD`, and the actor genders are translated while the men
// are kept (a co-starring work would lose half its cast otherwise).
func fieldsOf(m movie) source.Fields {
	var f source.Fields
	f.Title = str(m.Title)
	f.Plot = str(m.Summary)
	f.Releasedate = str(dateOnly(m.ReleaseDate))
	f.Series = str(m.SeriesName)
	f.Studio = str(m.MakerName)
	f.Publisher = str(m.PublisherName)
	if m.Duration > 0 {
		f.Runtime = &m.Duration
	}
	if score, err := strconv.ParseFloat(strings.TrimSpace(m.Score), 64); err == nil && score > 0 {
		// The site's score is five-point; the field is ten-point (ADR-0016).
		rating := score * 2
		f.Rating = &rating
	}
	for _, t := range m.Tags {
		if name := strings.TrimSpace(t.Name); name != "" {
			f.Tags = append(f.Tags, name)
		}
	}
	if name := strings.TrimSpace(m.DirectorName); name != "" {
		f.Director = []string{name}
	}
	for _, a := range m.Actors {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		f.Actors = append(f.Actors, meta.Actor{Name: name, Gender: gender(a.Gender)})
	}
	if ref := imageRef(field.Poster, 0, m.ThumbURL); ref != nil {
		f.Poster = ref
	}
	if ref := imageRef(field.Fanart, 0, m.CoverURL); ref != nil {
		f.Fanart = ref
	}
	// The index is the image's position among the stills that exist, not its position
	// in the site's array: an empty entry must not leave a gap, or the output would
	// write `fanart1`, `fanart3`.
	for _, p := range m.PreviewImages {
		if ref := imageRef(field.Extrafanarts, len(f.Extrafanarts), p.LargeURL); ref != nil {
			f.Extrafanarts = append(f.Extrafanarts, *ref)
		}
	}
	return f
}

// gender translates the site's actor gender: 0 is female, 1 is male, anything else
// is explicitly `unknown` rather than a guess.
func gender(code int) meta.Gender {
	switch code {
	case 0:
		return meta.Female
	case 1:
		return meta.Male
	default:
		return meta.Unknown
	}
}

// imageRef builds one address. No headers: the APP CDN does not ask for a Referer,
// and its bytes are decoded by FetchImage.
func imageRef(role field.Name, index int, rawURL string) *source.ImageRef {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	return &source.ImageRef{Role: role, Index: index, URL: rawURL}
}

var datePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

func dateOnly(s string) string {
	return datePrefix.FindString(strings.TrimSpace(s))
}

func str(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// decodeArtifact turns the APP CDN's encoding back into a JPEG. The format is
// `key || (jpeg XOR key)` — the first byte is the key and the rest is XORed with it,
// which is an encoding, not a secret. A body that already starts with the JPEG magic
// passes through, because some addresses answer from the site CDN, which does not
// encode. Anything that does not decode to a JPEG is refused rather than handed
// downstream as a cover.
func decodeArtifact(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return nil, errors.New("javdb: the image answer is empty")
	}
	if bytes.HasPrefix(body, jpegMagic) {
		return body, nil
	}
	key := body[0]
	decoded := make([]byte, len(body)-1)
	for i := 1; i < len(body); i++ {
		decoded[i-1] = body[i] ^ key
	}
	if !bytes.HasPrefix(decoded, jpegMagic) {
		return nil, errors.New("javdb: the image answer is neither a JPEG nor a decodable javdb image")
	}
	return decoded, nil
}
