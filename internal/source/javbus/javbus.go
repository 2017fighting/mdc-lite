// Package javbus is the first real source: javbus's detail page, one request per
// number.
//
// The number *is* the address (`GET https://{domain}/{number}`), so there is no
// search leg and no candidate list. The age gate is a cookie (`dv=1`), the images
// need a Referer, and a 404 is the site's clean way of saying it has no such work.
package javbus

import (
	"bytes"
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
	"github.com/2017fighting/mdc-lite/internal/meta"
	"github.com/2017fighting/mdc-lite/internal/source"
	"github.com/2017fighting/mdc-lite/internal/source/htmlx"
)

// ID is the name configuration refers to this source by.
const ID = "javbus"

// defaultDomains is the site's own spelling of itself. `javbus.com` answers with a
// 301 to `www.javbus.com`, which the Fetcher follows; either spelling works, and the
// operator can add mirrors to `domains` without waiting for a release.
var defaultDomains = []string{"javbus.com"}

// ageGateCookie is the one cookie that opens the site. Measured: `dv=1` alone
// answers 200, while `age=verified` alone still redirects to the gate.
const ageGateCookie = "dv=1"

// declared are the ten fields javbus can contribute: everything but `plot`,
// `extrafanarts`, `poster` and `rating`, which the site does not have. `series` is
// declared too even though it is missing on most pages: the declaration is a static
// capability, and by declaring it javbus may be named in `priority[series]` without
// the config being refused.
var declared = []field.Name{
	field.Title, field.Tags, field.Releasedate, field.Actors, field.Director,
	field.Runtime, field.Series, field.Studio, field.Publisher, field.Fanart,
}

// Fetcher is the transport seam. javbus borrows the process's one Fetcher: proxy,
// rate limit, cooldown, retries and cancellation are all its business, and this
// source never retries on its own.
type Fetcher interface {
	Do(ctx context.Context, req fetch.Request) (*fetch.Response, error)
}

// Options are the four knobs `scrape.with.sources.javbus` opens: the exit, the
// ordered domains, the rate override, and the per-request timeout. Everything else
// (User-Agent, the `dv=1` cookie, the image Referer) is a constant of this package.
type Options struct {
	Proxy   string
	Domains []string
	Rate    *fetch.Rate
	Timeout time.Duration
}

// Source is javbus. It holds no per-call state, so one instance serves every task
// at once.
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

// Fields is the static capability declaration: the ten fields this site has.
func (s *Source) Fields() []field.Name {
	out := make([]field.Name, len(declared))
	copy(out, declared)
	return out
}

// Scrape reads one number's detail page.
//
// A 404 is `notfound`, positively: the site answered "no such work", which is an
// answer rather than a failure. Everything else that went wrong keeps its reason
// code, so "we could not reach it" is never written down as "it does not exist".
func (s *Source) Scrape(ctx context.Context, number string) (source.Result, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return source.Result{}, source.Fail(source.ReasonParse, errors.New("javbus: the number is empty"))
	}
	resp, err := s.get(ctx, number)
	if err != nil {
		return source.Result{}, err
	}
	if resp.Status == http.StatusNotFound {
		return source.Result{Found: false}, nil
	}
	if isAgeGate(resp) {
		// The site served its gate instead of the page: the cookie did not open the
		// door. This is a block we can recognise, not a page we failed to parse.
		return source.Result{}, source.Fail(source.ReasonBlocked,
			fmt.Errorf("javbus: %s answered the age gate instead of the page", resp.URL))
	}
	fields, err := parse(resp.Body, resp.URL)
	if err != nil {
		return source.Result{}, source.Fail(source.ReasonParse, err)
	}
	return source.Result{Found: true, Fields: fields}, nil
}

// FetchImage fetches one image the page offered. The bytes are returned as they
// came; the engine sniffs their type. javbus's image CDN answers 403 to a request
// without the page as its Referer, which is why the page's own URL travels in the
// ImageRef.
func (s *Source) FetchImage(ctx context.Context, ref source.ImageRef) ([]byte, error) {
	if strings.TrimSpace(ref.URL) == "" {
		return nil, errors.New("javbus: the image reference has no URL")
	}
	req := s.request(ref.URL)
	req.Headers = ref.Headers
	resp, err := s.fetcher.Do(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Status != http.StatusOK {
		// A 404 on an image is not "a transport failure" and not "no such work" of
		// the number: the image simply did not arrive, so the field is unfulfilled
		// and the next source gets its chance.
		return nil, fmt.Errorf("javbus: image %s answered %d", resp.URL, resp.Status)
	}
	return resp.Body, nil
}

// Dump implements source.Dumper: the same request `mdc source test` just made, kept
// byte for byte so it can become a fixture. javbus has one leg, so there is one
// capture and its Leg is empty.
func (s *Source) Dump(ctx context.Context, number string) ([]source.Capture, error) {
	number = strings.TrimSpace(number)
	if number == "" {
		return nil, errors.New("javbus: the number is empty")
	}
	resp, err := s.get(ctx, number)
	if err != nil {
		return nil, err
	}
	return []source.Capture{{URL: resp.URL, Status: resp.Status, Body: resp.Body}}, nil
}

// get asks for one number's page: the first domain, with the rest as mirrors. The
// age-gate cookie is the only header: the HTML itself needs no Referer.
func (s *Source) get(ctx context.Context, number string) (*fetch.Response, error) {
	urls := make([]string, len(s.opts.Domains))
	for i, domain := range s.opts.Domains {
		urls[i] = s.pageURL(domain, number)
	}
	req := s.request(urls[0])
	req.Mirrors = urls[1:]
	req.Headers = map[string]string{"Cookie": ageGateCookie}
	return s.fetcher.Do(ctx, req)
}

// request is the one place the source's four knobs are copied onto a fetch: every
// request javbus makes runs under the same exit, rate and timeout.
func (s *Source) request(url string) fetch.Request {
	return fetch.Request{
		URL:     url,
		Proxy:   s.opts.Proxy,
		Rate:    s.opts.Rate,
		Timeout: s.opts.Timeout,
	}
}

// pageURL builds `https://{domain}/{number}`. A domain may be written with a
// scheme (`http://127.0.0.1:9001`), which is how the acceptance fixtures point this
// source at a local replay of a captured page while keeping every other line —
// selectors, cookie, Referer — the production one.
func (s *Source) pageURL(domain, number string) string {
	base := strings.TrimRight(domain, "/")
	if !strings.Contains(base, "://") {
		base = "https://" + base
	}
	return base + "/" + url.PathEscape(number)
}

// isAgeGate recognises the site's interception page by its title and its path, both
// of which are positive markers: `Age Verification JavBus` at `/doc/driver-verify`.
// A generic layer cannot do this — it only knows status codes — which is exactly
// why the source reports `blocked` itself.
func isAgeGate(resp *fetch.Response) bool {
	if u, err := url.Parse(resp.URL); err == nil && strings.HasPrefix(u.Path, "/doc/driver-verify") {
		return true
	}
	return bytes.Contains(resp.Body, []byte("<title>Age Verification"))
}

// The labels the detail page prints before each value. They are Traditional
// Chinese because that is what the site writes.
const (
	labelReleaseDate = "發行日期"
	labelRuntime     = "長度"
	labelDirector    = "導演"
	labelStudio      = "製作商"
	labelPublisher   = "發行商"
	labelSeries      = "系列"
)

var runtimeDigits = regexp.MustCompile(`(\d+)`)

// parse turns a detail page into fields.
//
// The declared set is fixed, the page's set is not (series is usually absent), so
// every field here is "contribute it if the page has it". An absent field is nil,
// which is how the engine tells "this source said nothing" from "this source said
// an empty value".
func parse(body []byte, pageURL string) (source.Fields, error) {
	doc, err := htmlx.Parse(body)
	if err != nil {
		return source.Fields{}, fmt.Errorf("javbus: the page is not parseable HTML: %w", err)
	}
	var f source.Fields

	if h := htmlx.Find(doc, isTitleHeading); h != nil {
		f.Title = str(htmlx.Text(h))
	}

	info := htmlx.Find(doc, htmlx.Class("info"))
	if info != nil {
		for _, p := range htmlx.FindAll(info, htmlx.Tag("p")) {
			header := htmlx.Find(p, htmlx.Class("header"))
			if header == nil {
				continue
			}
			label := strings.TrimRight(strings.TrimSpace(htmlx.Text(header)), ":：")
			switch label {
			case labelReleaseDate:
				if d := dateOnly(strings.TrimSpace(htmlx.TextAfter(p, header))); d != "" {
					f.Releasedate = &d
				}
			case labelRuntime:
				if m := runtimeDigits.FindString(strings.TrimSpace(htmlx.TextAfter(p, header))); m != "" {
					if minutes, err := strconv.Atoi(m); err == nil {
						f.Runtime = &minutes
					}
				}
			case labelDirector:
				f.Director = htmlx.LinkText(p)
			case labelStudio:
				f.Studio = first(htmlx.LinkText(p))
			case labelPublisher:
				f.Publisher = first(htmlx.LinkText(p))
			case labelSeries:
				f.Series = first(htmlx.LinkText(p))
			}
		}
		// Tags are the genre checkboxes' labels. The actor names are also wrapped in
		// `span.genre`, so the selector needs the `label` a genre has and a star link
		// does not — otherwise every actress lands in `tags`.
		for _, span := range htmlx.FindAll(info, htmlx.Class("genre")) {
			label := htmlx.Find(span, htmlx.Tag("label"))
			if label == nil {
				continue
			}
			if a := htmlx.Find(label, htmlx.Tag("a")); a != nil {
				if name := strings.TrimSpace(htmlx.Text(a)); name != "" {
					f.Tags = append(f.Tags, name)
				}
			}
		}
	}

	// The actress list. javbus only lists women here, so every actor it names is
	// `female`; the gender is still explicit because the model's zero value is
	// `unknown`, which the NFO writes differently.
	for _, a := range actorLinks(doc) {
		if name := strings.TrimSpace(htmlx.Text(a)); name != "" {
			f.Actors = append(f.Actors, meta.Actor{Name: name, Gender: meta.Female})
		}
	}

	if a := htmlx.Find(doc, htmlx.Class("bigImage")); a != nil {
		if img := htmlx.Find(a, htmlx.Tag("img")); img != nil {
			if src := htmlx.Attr(img, "src"); src != "" {
				ref := source.ImageRef{
					Role: field.Fanart,
					URL:  absolute(pageURL, src),
					// The image CDN answers a bare request with 403; the page it belongs
					// to is the key.
					Headers: map[string]string{"Referer": pageURL},
				}
				f.Fanart = &ref
			}
		}
	}

	if f.Title == nil && f.Fanart == nil {
		// Neither the title nor the cover: this is not a usable detail page. Calling
		// it `parse` keeps it out of `notfound`, which the site never said.
		return source.Fields{}, errors.New("javbus: the page carries neither a title nor a cover")
	}
	return f, nil
}

// actorLinks returns the actresses in page order, from the waterfall's cards (the
// canonical list) or, when the page has no waterfall, from the star names above it.
func actorLinks(doc *html.Node) []*html.Node {
	var out []*html.Node
	for _, box := range htmlx.FindAll(doc, htmlx.Class("avatar-box")) {
		if span := htmlx.Find(box, htmlx.Tag("span")); span != nil {
			if name := strings.TrimSpace(htmlx.Text(span)); name != "" {
				out = append(out, span)
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, name := range htmlx.FindAll(doc, htmlx.Class("star-name")) {
		if a := htmlx.Find(name, htmlx.Tag("a")); a != nil {
			out = append(out, a)
		}
	}
	return out
}

// isTitleHeading is the page's own title: the h3 inside the content container.
func isTitleHeading(n *html.Node) bool {
	if n.Type != html.ElementNode || n.Data != "h3" {
		return false
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if htmlx.HasClass(p, "container") {
			return true
		}
	}
	return false
}

// dateOnly keeps the `YYYY-MM-DD` prefix of a date line. The site prints the date
// in that shape already; the trim is here because `releasedate` has one canonical
// form and every source produces it itself.
var datePrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)

func dateOnly(s string) string {
	if m := datePrefix.FindString(strings.TrimSpace(s)); m != "" {
		return m
	}
	return ""
}

func first(v []string) *string {
	if len(v) == 0 {
		return nil
	}
	return &v[0]
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
