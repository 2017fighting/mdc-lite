// Package fc2 is the FC2 official article page: one request per number, no search
// leg.
//
// The number's trailing digit run is the article id — not every digit, or the `2` of
// `FC2` would become part of it. The site answers its "not found" page with HTTP 200,
// so neither the status nor "no fields" can decide anything: the page says which one
// it is with a positive marker, and this source reads that marker.
package fc2

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/2017fighting/mdc-lite/internal/fetch"
	"github.com/2017fighting/mdc-lite/internal/field"
	"github.com/2017fighting/mdc-lite/internal/source"
	"github.com/2017fighting/mdc-lite/internal/source/htmlx"
)

// ID is the name configuration refers to this source by.
const ID = "fc2"

// defaultDomains is the site's own host. It needs no cookie and no Referer.
var defaultDomains = []string{"adult.contents.fc2.com"}

// declared is the eight fields the article page carries. There is no cast list —
// FC2 is an amateur marketplace — so `actors` is not declared, and a declared field
// that never arrives would be a capability that lies. `poster`, `director`,
// `rating`, `series` and `publisher` are absent for the same reason.
var declared = []field.Name{
	field.Title, field.Plot, field.Fanart, field.Extrafanarts,
	field.Tags, field.Releasedate, field.Runtime, field.Studio,
}

// The two positive markers. `header` is the article's own container; `notFound` is
// the site's miss page. Both are class names the page prints, which is why they can
// be trusted and a 200 status cannot.
const (
	hitClass      = "items_article_headerInfo"
	notFoundClass = "items_notfound_wp"
)

// Fetcher is the transport seam.
type Fetcher interface {
	Do(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// Options are the four knobs `scrape.with.sources.fc2` opens.
type Options struct {
	Proxy   string
	Domains []string
	Rate    *fetch.Rate
	Timeout time.Duration
}

// Source is fc2. It holds no per-call state, so one instance serves every task at
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

// Fields is the static capability declaration.
func (s *Source) Fields() []field.Name {
	out := make([]field.Name, len(declared))
	copy(out, declared)
	return out
}

// Scrape reads one article page.
//
// The id has to come from the number: a number with no trailing digits has no
// article, and asking `/article//` would be asking the site a different question. A
// number that cannot name an article is `parse` — the input is unreadable, and no
// request was made.
func (s *Source) Scrape(ctx context.Context, number string) (source.Result, error) {
	id := articleID(number)
	if id == "" {
		return source.Result{}, source.Fail(source.ReasonParse,
			fmt.Errorf("fc2: the number %q has no trailing digits, so it names no article", strings.TrimSpace(number)))
	}
	resp, err := s.get(ctx, id)
	if err != nil {
		return source.Result{}, err
	}
	doc, err := htmlx.Parse(resp.Body)
	if err != nil {
		return source.Result{}, source.Fail(source.ReasonParse, fmt.Errorf("fc2: the page is not parseable HTML: %w", err))
	}
	if htmlx.Find(doc, htmlx.Class(notFoundClass)) != nil {
		// The site's own positive miss marker, on a 200. This is the answer, not a
		// failure: the capture of this page is what the source test keeps.
		return source.Result{Found: false}, nil
	}
	if htmlx.Find(doc, htmlx.Class(hitClass)) == nil {
		// Neither marker: the page changed shape, or this is an interception page we
		// cannot name. Calling it `notfound` would be a lie about the site.
		return source.Result{}, source.Fail(source.ReasonParse,
			fmt.Errorf("fc2: %s carries neither the article header nor the not-found marker", resp.URL))
	}
	return source.Result{Found: true, Fields: parse(doc, resp.URL)}, nil
}

// FetchImage fetches one image. FC2's thumbnail CDN answers a bare request; the
// addresses are protocol-relative, so the stored address already carries the scheme.
func (s *Source) FetchImage(ctx context.Context, ref source.ImageRef) ([]byte, error) {
	if strings.TrimSpace(ref.URL) == "" {
		return nil, errors.New("fc2: the image reference has no URL")
	}
	req := s.request(ref.URL)
	req.Headers = ref.Headers
	resp, err := s.fetcher.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		return nil, fmt.Errorf("fc2: image %s answered %d", resp.URL, resp.Status)
	}
	return resp.Body, nil
}

// Dump implements source.Dumper. fc2 has one leg.
func (s *Source) Dump(ctx context.Context, number string) ([]source.Capture, error) {
	id := articleID(number)
	if id == "" {
		return nil, fmt.Errorf("fc2: the number %q has no trailing digits, so there is nothing to dump", strings.TrimSpace(number))
	}
	resp, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	return []source.Capture{{URL: resp.URL, Status: resp.Status, Body: resp.Body}}, nil
}

// get asks for one article page: the first domain, with the rest as mirrors.
func (s *Source) get(ctx context.Context, id string) (*fetch.Response, error) {
	urls := make([]string, len(s.opts.Domains))
	for i, domain := range s.opts.Domains {
		urls[i] = articleURL(domain, id)
	}
	req := s.request(urls[0])
	req.Mirrors = urls[1:]
	return s.fetcher.Do(ctx, req)
}

func (s *Source) request(target string) fetch.Request {
	return fetch.Request{
		URL:     target,
		Proxy:   s.opts.Proxy,
		Rate:    s.opts.Rate,
		Timeout: s.opts.Timeout,
	}
}

// articleID is the number's trailing digit run. Taking every digit would fold the
// `2` of `FC2` into the id; taking the first would read the prefix's.
func articleID(number string) string {
	s := strings.TrimSpace(number)
	end := len(s)
	for end > 0 && !isDigit(s[end-1]) {
		end--
	}
	start := end
	for start > 0 && isDigit(s[start-1]) {
		start--
	}
	return s[start:end]
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// articleURL builds `https://{domain}/article/{id}/`. A domain written with a scheme
// is a captured-page replay taking over this source.
func articleURL(domain, id string) string {
	base := strings.TrimRight(domain, "/")
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return base + "/article/" + url.PathEscape(id) + "/"
}

// parse reads the article. Every field is "contribute it if the page has it": FC2
// pages differ in what they show, and a missing field is not a broken page.
func parse(doc *html.Node, pageURL string) source.Fields {
	var f source.Fields
	header := htmlx.Find(doc, htmlx.Class(hitClass))
	if header == nil {
		return f
	}

	if h := htmlx.Find(header, htmlx.Tag("h3")); h != nil {
		// The title carries an invisible anti-scraping span; removing it is how the
		// title is read as the title it prints.
		htmlx.Remove(h, hidden)
		f.Title = str(htmlx.Text(h))
	}
	if a := htmlx.Find(htmlx.Find(header, htmlx.Class("items_article_writer")), htmlx.Tag("a")); a != nil {
		f.Studio = str(htmlx.Text(a))
	}
	for _, a := range htmlx.FindAll(htmlx.Find(header, htmlx.Class("items_article_TagArea")), htmlx.Class("tag")) {
		if name := strings.TrimSpace(htmlx.Text(a)); name != "" {
			f.Tags = append(f.Tags, name)
		}
	}
	for _, p := range htmlx.FindAll(header, htmlx.Tag("p")) {
		text := strings.TrimSpace(htmlx.Text(p))
		if date := soldDate(text); date != "" {
			f.Releasedate = &date
		}
	}

	if img := htmlx.Find(htmlx.Find(doc, htmlx.Class("items_article_MainitemThumb")), htmlx.Tag("img")); img != nil {
		if src := htmlx.Attr(img, "src"); src != "" {
			f.Fanart = ref(field.Fanart, 0, absolute(pageURL, src))
		}
	}
	if p := htmlx.Find(htmlx.Find(doc, htmlx.Class("items_article_MainitemThumb")), htmlx.Class("items_article_info")); p != nil {
		if minutes := runtimeMinutes(htmlx.Text(p)); minutes > 0 {
			f.Runtime = &minutes
		}
	}
	for _, a := range htmlx.FindAll(htmlx.Find(doc, htmlx.Class("items_article_SampleImages")), htmlx.Tag("a")) {
		if href := htmlx.Attr(a, "href"); href != "" {
			// The index counts the stills that exist, so a link without an address
			// cannot leave a gap in the output's `fanart{n}` sequence.
			f.Extrafanarts = append(f.Extrafanarts, source.ImageRef{
				Role: field.Extrafanarts, Index: len(f.Extrafanarts), URL: absolute(pageURL, href),
			})
		}
	}
	if metaDesc := htmlx.Find(doc, func(n *html.Node) bool {
		return n.Type == html.ElementNode && n.Data == "meta" && htmlx.Attr(n, "name") == "description"
	}); metaDesc != nil {
		f.Plot = str(stripNumberPrefix(htmlx.Attr(metaDesc, "content")))
	}
	return f
}

// hidden reports the spans a page uses to poison a copied title: their text is not
// part of what the page shows.
func hidden(n *html.Node) bool {
	if n.Type != html.ElementNode || n.Data != "span" {
		return false
	}
	style := strings.ReplaceAll(htmlx.Attr(n, "style"), " ", "")
	return strings.Contains(style, "display:none") ||
		strings.Contains(style, "zoom:0.01") ||
		strings.Contains(style, "overflow:hidden")
}

var (
	soldDatePattern = regexp.MustCompile(`販売日\s*[:：]\s*(\d{4})/(\d{1,2})/(\d{1,2})`)
	clockPattern    = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2}))?$`)
	numberPrefix    = regexp.MustCompile(`^FC2[-_ ]?(?:PPV[-_ ]?)?\d+\s+`)
)

// soldDate turns `販売日 : 2026/09/25` into the canonical `2026-09-25`.
func soldDate(text string) string {
	m := soldDatePattern.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	month, _ := strconv.Atoi(m[2])
	day, _ := strconv.Atoi(m[3])
	return fmt.Sprintf("%s-%02d-%02d", m[1], month, day)
}

// runtimeMinutes turns the page's `30:16` / `01:03:52` into minutes. The site prints
// either MM:SS or HH:MM:SS, and the field is whole minutes, so the seconds are
// dropped — the same integer division dmm's seconds count gets, so the two sources
// agree about what "runtime" means.
func runtimeMinutes(text string) int {
	m := clockPattern.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0
	}
	first, _ := strconv.Atoi(m[1])
	second, _ := strconv.Atoi(m[2])
	seconds := first*60 + second
	if m[3] != "" {
		third, _ := strconv.Atoi(m[3])
		seconds = first*3600 + second*60 + third
	}
	return seconds / 60
}

// stripNumberPrefix removes the leading number token from the page description,
// which begins with the work's own number. A description that does not begin with
// one is left alone: a plot is not a place to guess.
func stripNumberPrefix(text string) string {
	text = strings.TrimSpace(text)
	if numberPrefix.MatchString(text) {
		return strings.TrimSpace(numberPrefix.ReplaceAllString(text, ""))
	}
	return text
}

func ref(role field.Name, index int, rawURL string) *source.ImageRef {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	return &source.ImageRef{Role: role, Index: index, URL: rawURL}
}

func str(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

func absolute(pageURL, ref string) string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return ref
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}
