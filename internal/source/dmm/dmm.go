// Package dmm is FANZA's video GraphQL API: a search leg that turns a number into a
// product id, then one POST that reads the product and its review summary together.
//
// The id is not the number, and it is spelled two ways: `ssis001` for the site's own
// search index, `ssis00001` for the five-wide product id of a title in the first
// thousand. Both are tried in order and the loop stops at the first hit; a candidate
// counts as a hit only when its "prefix + numeric value" identity is the asked
// number's, because `SSIS-0011` sits next to `SSIS-001` in the results.
//
// dmm is the one source whose fields are all blocked behind a Japanese exit: the API
// answers `not-available-in-your-region` from anywhere else, and the failure names
// that rather than pretending the work does not exist. Getting an exit is the
// operator's routing job; the source only reports what the API said.
package dmm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
)

// ID is the name configuration refers to this source by.
const ID = "dmm"

// defaultDomains is FANZA's own domain. `www.` is trimmed before the `api.video.` and
// `video.` hosts are built, so either spelling configures the same site.
var defaultDomains = []string{"dmm.co.jp"}

// declared is the static capability declaration: dmm has all fourteen fields.
var declared = field.All()

// paddedDigits is the width the site pads a product id to. A number already that
// wide has only one spelling and costs one search instead of two.
const paddedDigits = 5

// searchLimit is the site's own search window. It is wide because the exact work is
// not always first: the identity match is what decides, not the position.
const searchLimit = 30

// The two query texts. Values are interpolated as JSON-quoted strings rather than
// passed as GraphQL variables: the endpoint is undocumented, so a declared variable
// type would be a guess that fails server-side with a schema error.
const searchQuery = `query SearchPPV {
  legacySearchPPV(limit: %d, floor: AV, sort: SALES_RANK_SCORE, queryWord: %s) {
    result {
      contents { id }
      pageInfo { totalCount }
    }
  }
}`

// contentQuery asks for the product and its review summary in one POST. It is one
// request on purpose: a second round trip would be a second chance to fail, and the
// two answers belong to the same work.
const contentQuery = `query ContentPageData {
  ppvContent(id: %s) {
    id
    floor
    title
    description
    deliveryStartDate
    makerReleasedAt
    duration
    packageImage { largeUrl mediumUrl }
    sampleImages { largeImageUrl }
    actresses { name imageUrl }
    directors { name }
    series { name }
    maker { name }
    label { name }
    genres { name }
  }
  reviewSummary(contentId: %s) { average total }
}`

// Fetcher is the transport seam.
type Fetcher interface {
	Do(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// Options are the four knobs `scrape.with.sources.dmm` opens. `Proxy` is how the
// operator points this one source at a Japanese exit while the rest of the process
// keeps its own.
type Options struct {
	Proxy   string
	Domains []string
	Rate    *fetch.Rate
	Timeout time.Duration
}

// Source is dmm. It holds no per-call state, so one instance serves every task at
// once.
type Source struct {
	fetcher Fetcher
	opts    Options
}

// New builds the source on a Fetcher.
func New(f Fetcher, opts Options) *Source {
	if len(opts.Domains) == 0 {
		opts.Domains = defaultDomains
	}
	return &Source{fetcher: f, opts: opts}
}

// ID implements source.Source.
func (s *Source) ID() string { return ID }

// Fields is the static capability declaration: all fourteen fields.
func (s *Source) Fields() []field.Name {
	out := make([]field.Name, len(declared))
	copy(out, declared)
	return out
}

// Scrape searches the two id spellings in order, then reads the product.
func (s *Source) Scrape(ctx context.Context, number string) (source.Result, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return source.Result{}, source.Fail(source.ReasonParse, errors.New("dmm: the number is empty"))
	}
	id, found, err := s.search(ctx, number)
	if err != nil {
		return source.Result{}, err
	}
	if !found {
		return source.Result{Found: false}, nil
	}
	content, review, err := s.content(ctx, id)
	if err != nil {
		return source.Result{}, err
	}
	if content == nil {
		// A resolved id whose product is null is the site saying "not this one".
		return source.Result{Found: false}, nil
	}
	return source.Result{Found: true, Fields: fieldsOf(*content, *review)}, nil
}

// FetchImage fetches one image. The addresses point at dmm's own CDN, which wants no
// Referer and no cookie — but does need the same Japanese exit the API does.
func (s *Source) FetchImage(ctx context.Context, ref source.ImageRef) ([]byte, error) {
	if strings.TrimSpace(ref.URL) == "" {
		return nil, errors.New("dmm: the image reference has no URL")
	}
	req := s.request(ref.URL)
	req.Headers = ref.Headers
	resp, err := s.fetcher.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("dmm: image %s answered %d", resp.URL, resp.Status)
	}
	return resp.Body, nil
}

// Dump implements source.Dumper: every search POST it actually made, plus the
// content POST when a search found the work. The legs are named after the term they
// searched, so the fixture directory says which spelling produced which answer.
func (s *Source) Dump(ctx context.Context, number string) ([]source.Capture, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, errors.New("dmm: the number is empty")
	}
	var captures []source.Capture
	id := ""
	for _, term := range searchTerms(number) {
		resp, err := s.post(ctx, fmt.Sprintf(searchQuery, searchLimit, quote(term)))
		if err != nil {
			return captures, err
		}
		captures = append(captures, capture("search-"+term, resp))
		ids, err := searchIDs(resp)
		if err != nil {
			return captures, err
		}
		if id = exact(ids, number); id != "" {
			break
		}
	}
	if id == "" {
		return captures, nil
	}
	resp, err := s.post(ctx, contentQueryFor(id))
	if err != nil {
		return captures, err
	}
	return append(captures, capture("content", resp)), nil
}

// search runs the spelling loop: the reduced form first, then the padded one. It
// stops on the first hit; an error stops it too, because a failed leg is not a
// reason to ask the site a second question.
func (s *Source) search(ctx context.Context, number string) (string, bool, error) {
	for _, term := range searchTerms(number) {
		resp, err := s.post(ctx, fmt.Sprintf(searchQuery, searchLimit, quote(term)))
		if err != nil {
			return "", false, err
		}
		ids, err := searchIDs(resp)
		if err != nil {
			return "", false, err
		}
		if id := exact(ids, number); id != "" {
			return id, true, nil
		}
	}
	return "", false, nil
}

// content asks for the product and its review summary in one POST.
func (s *Source) content(ctx context.Context, id string) (*content, *reviewSummary, error) {
	resp, err := s.post(ctx, contentQueryFor(id))
	if err != nil {
		return nil, nil, err
	}
	data, err := payload(resp)
	if err != nil {
		return nil, nil, err
	}
	var body struct {
		PPVContent    *content       `json:"ppvContent"`
		ReviewSummary *reviewSummary `json:"reviewSummary"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, nil, source.Fail(source.ReasonParse, fmt.Errorf("dmm: the content answer is not the expected JSON: %w", err))
	}
	review := &reviewSummary{}
	if body.ReviewSummary != nil {
		review = body.ReviewSummary
	}
	return body.PPVContent, review, nil
}

// post sends one GraphQL POST. The body is `{"query": "..."}`, which is the shape the
// endpoint accepts and the only one it was measured with.
func (s *Source) post(ctx context.Context, query string) (*fetch.Response, error) {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, source.Fail(source.ReasonParse, fmt.Errorf("dmm: the query could not be encoded: %w", err))
	}
	urls := make([]string, len(s.opts.Domains))
	mirrorHeaders := make([]map[string]string, 0, len(s.opts.Domains))
	first, firstReferer := endpoints(s.opts.Domains[0])
	urls[0] = first
	for i, configured := range s.opts.Domains[1:] {
		api, referer := endpoints(configured)
		urls[i+1] = api
		// The Referer names the site the request is for, so a mirror gets its own:
		// one header set for the first address would tell a fallback domain about a
		// different site.
		mirrorHeaders = append(mirrorHeaders, headers(referer))
	}
	req := s.request(first)
	req.Mirrors = urls[1:]
	req.MirrorHeaders = mirrorHeaders
	req.Method = http.MethodPost
	req.Body = body
	req.Headers = headers(firstReferer)
	return s.fetcher.Do(ctx, req)
}

// headers are the five the API was measured with. `Origin` is deliberately absent:
// the site is not asked to accept a cross-origin request.
func headers(referer string) map[string]string {
	return map[string]string{
		"Referer":       referer,
		"Content-Type":  "application/json",
		"Accept":        "application/json",
		"Cache-Control": "no-cache",
		"Fanza-Device":  "BROWSER",
	}
}

func (s *Source) request(target string) fetch.Request {
	return fetch.Request{
		URL:     target,
		Proxy:   s.opts.Proxy,
		Rate:    s.opts.Rate,
		Timeout: s.opts.Timeout,
	}
}

// payload unwraps the GraphQL envelope. An `errors` key is the site's diagnosis of
// *our* request, so it is a failure even when the status is 200; a region refusal is
// named and told to point at a Japanese exit, because that is the only fix.
func payload(resp *fetch.Response) (json.RawMessage, error) {
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(resp.Body, &env); err != nil {
		return nil, source.Fail(source.ReasonParse, fmt.Errorf("dmm: the GraphQL answer is not JSON: %w", err))
	}
	if len(env.Errors) > 0 && string(env.Errors) != "null" {
		var errs []struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(env.Errors, &errs); err != nil {
			return nil, source.Fail(source.ReasonParse, fmt.Errorf("dmm: the API answered errors that are not readable: %w", err))
		}
		messages := make([]string, 0, len(errs))
		for _, e := range errs {
			messages = append(messages, e.Message)
		}
		joined := strings.Join(messages, "; ")
		if regionRefused(joined) {
			// The site is reachable and answering; it just refuses this exit. Saying
			// so is the difference between "fix your routing" and "the work is gone".
			return nil, source.Fail(source.ReasonBlocked, fmt.Errorf(
				"dmm: the API refused this exit because it is not in Japan (it needs a Japanese exit, e.g. via scrape.with.sources.dmm.proxy): %s", joined))
		}
		return nil, source.Fail(source.ReasonParse, fmt.Errorf("dmm: the GraphQL API answered with errors: %s", joined))
	}
	return env.Data, nil
}

// searchIDs reads the ids a search answer listed. An answer with errors is a
// failure, not an empty list: "the API refused us" must never be read as "no such
// work".
func searchIDs(resp *fetch.Response) ([]string, error) {
	data, err := payload(resp)
	if err != nil {
		return nil, err
	}
	var body struct {
		LegacySearchPPV struct {
			Result struct {
				Contents []struct {
					ID string `json:"id"`
				} `json:"contents"`
			} `json:"result"`
		} `json:"legacySearchPPV"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, source.Fail(source.ReasonParse, fmt.Errorf("dmm: the search answer is not the expected JSON: %w", err))
	}
	out := make([]string, 0, len(body.LegacySearchPPV.Result.Contents))
	for _, c := range body.LegacySearchPPV.Result.Contents {
		out = append(out, c.ID)
	}
	return out, nil
}

// exact picks the candidate that is the work asked for. It walks every candidate:
// `SSIS-001` sits next to `SSIS-0011`, and the search order is not evidence.
func exact(ids []string, number string) string {
	for _, id := range ids {
		if id != "" && sameWork(id, number) {
			return id
		}
	}
	return ""
}

// searchTerms is the id spellings to try, most specific first: the reduced number,
// then the five-wide zero-padded one. A number whose tail is not digits, or which is
// already five wide, has only the reduced spelling.
func searchTerms(number string) []string {
	reduced := strings.ToLower(source.NumberKey(number))
	prefix, value, ok := identity(number)
	if !ok {
		return []string{reduced}
	}
	digits := len(reduced) - len(prefix)
	if digits >= paddedDigits {
		return []string{reduced}
	}
	padded := prefix + fmt.Sprintf("%0*d", paddedDigits, atoiOr(value, 0))
	if padded == reduced {
		return []string{reduced}
	}
	return []string{reduced, padded}
}

// sameWork compares an id against a number by identity: the prefix plus the numeric
// value with leading zeros gone. `ssis00001` and `SSIS-001` are the same title and
// neither string is the other, so comparing the spellings is how a match gets missed.
func sameWork(id, number string) bool {
	idPrefix, idValue, idOK := identity(id)
	numPrefix, numValue, numOK := identity(number)
	if !idOK || !numOK {
		// Nothing numeric to compare: an exact spelling match is all there is.
		return strings.EqualFold(strings.TrimSpace(id), strings.TrimSpace(number))
	}
	return idPrefix == numPrefix && idValue == numValue
}

// identity splits a number into its lower-cased family prefix and its trailing digit
// run with the leading zeros removed.
func identity(number string) (string, string, bool) {
	key := strings.ToLower(source.NumberKey(number))
	end := len(key)
	for end > 0 && isDigit(key[end-1]) {
		end--
	}
	if end == len(key) {
		return "", "", false
	}
	digits := strings.TrimLeft(key[end:], "0")
	if digits == "" {
		digits = "0"
	}
	return key[:end], digits, true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func atoiOr(s string, fallback int) int {
	n := 0
	if s == "" {
		return fallback
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return fallback
		}
		n = n*10 + int(s[i]-'0')
	}
	return n
}

// quote JSON-quotes a value for interpolation into a query text.
func quote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// contentQueryFor builds the content query for one id.
func contentQueryFor(id string) string {
	return fmt.Sprintf(contentQuery, quote(id), quote(id))
}

// endpoints turns one configured domain into the GraphQL address and the Referer it
// wants. A domain written as a full URL is a captured-page replay taking over this
// source: both legs then live on that one origin, which is what `api.video.<domain>`
// and `video.<domain>` are in production.
func endpoints(configured string) (string, string) {
	base := strings.TrimRight(configured, "/")
	if strings.Contains(base, "://") {
		return base + "/graphql", base + "/"
	}
	host := strings.TrimPrefix(base, "www.")
	return "https://api.video." + host + "/graphql", "https://video." + host + "/"
}

// content is the product payload, cut down to what this build reads.
type content struct {
	Title             string `json:"title"`
	Description       string `json:"description"`
	DeliveryStartDate string `json:"deliveryStartDate"`
	MakerReleasedAt   string `json:"makerReleasedAt"`
	Duration          int    `json:"duration"`

	PackageImage *struct {
		LargeURL  string `json:"largeUrl"`
		MediumURL string `json:"mediumUrl"`
	} `json:"packageImage"`
	SampleImages []struct {
		LargeImageURL string `json:"largeImageUrl"`
	} `json:"sampleImages"`
	Actresses []struct {
		Name string `json:"name"`
	} `json:"actresses"`
	Directors []struct {
		Name string `json:"name"`
	} `json:"directors"`
	Series *struct {
		Name string `json:"name"`
	} `json:"series"`
	Maker *struct {
		Name string `json:"name"`
	} `json:"maker"`
	Label *struct {
		Name string `json:"name"`
	} `json:"label"`
	Genres []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

// reviewSummary is the site's five-point review average.
type reviewSummary struct {
	Average float64 `json:"average"`
	Total   int     `json:"total"`
}

// fieldsOf maps the product onto the 14 source-level fields.
//
// The site's unit and scale conversions happen here: `runtime` leaves as minutes
// (the API counts seconds), `rating` as a 0–10 number (the API averages five), and
// `releasedate` as `YYYY-MM-DD` (the API answers RFC3339). A rating of 0 is left
// unset, because 0 on this scale means "not rated", not "rated zero".
func fieldsOf(c content, r reviewSummary) source.Fields {
	var f source.Fields
	f.Title = str(c.Title)
	f.Plot = str(c.Description)
	f.Releasedate = str(dateOf(c.DeliveryStartDate, c.MakerReleasedAt))
	if c.Duration > 0 {
		minutes := c.Duration / 60
		if minutes > 0 {
			f.Runtime = &minutes
		}
	}
	if r.Average > 0 {
		rating := r.Average * 2
		f.Rating = &rating
	}
	for _, g := range c.Genres {
		if name := strings.TrimSpace(g.Name); name != "" {
			f.Tags = append(f.Tags, name)
		}
	}
	for _, a := range c.Actresses {
		if name := strings.TrimSpace(a.Name); name != "" {
			// The API carries no gender; every name it lists here is an actress.
			f.Actors = append(f.Actors, meta.Actor{Name: name, Gender: meta.Female})
		}
	}
	for _, d := range c.Directors {
		if name := strings.TrimSpace(d.Name); name != "" {
			f.Director = append(f.Director, name)
		}
	}
	if c.Series != nil {
		f.Series = str(c.Series.Name)
	}
	if c.Maker != nil {
		f.Studio = str(c.Maker.Name)
	}
	if c.Label != nil {
		f.Publisher = str(c.Label.Name)
	}

	poster := ""
	if c.PackageImage != nil {
		poster = strings.TrimSpace(c.PackageImage.LargeURL)
	}
	if poster != "" {
		f.Poster = &source.ImageRef{Role: field.Poster, URL: poster}
	}
	var samples []string
	for _, s := range c.SampleImages {
		if u := strings.TrimSpace(s.LargeImageURL); u != "" {
			samples = append(samples, u)
		}
	}
	// The landscape cover is the first sample; with no samples the package image is
	// the only landscape the site has.
	fanart := poster
	if len(samples) > 0 {
		fanart = samples[0]
	}
	if fanart != "" {
		f.Fanart = &source.ImageRef{Role: field.Fanart, URL: fanart}
	}
	for i, u := range samples {
		f.Extrafanarts = append(f.Extrafanarts, source.ImageRef{Role: field.Extrafanarts, Index: i, URL: u})
	}
	return f
}

// dateOf turns the product's dates into `YYYY-MM-DD`. The delivery date wins over
// the maker's release date, which is the earlier of the two and the one the site
// considers the publication.
func dateOf(delivery, maker string) string {
	for _, raw := range []string{delivery, maker} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.UTC().Format("2006-01-02")
		}
		if len(raw) >= len("2006-01-02") {
			if _, err := time.Parse("2006-01-02", raw[:len("2006-01-02")]); err == nil {
				return raw[:len("2006-01-02")]
			}
		}
	}
	return ""
}

// regionRefused recognises the API's region refusal. The message is the site's, and
// it has been seen in both spellings.
func regionRefused(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "not-available-in-your-region") ||
		strings.Contains(lower, "not available in your region")
}

func capture(leg string, resp *fetch.Response) source.Capture {
	return source.Capture{Leg: leg, URL: resp.URL, Status: resp.Status, Body: resp.Body}
}

func str(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}
