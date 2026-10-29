// Package fetch is the one transport seam of the application: every request the
// scraper makes — a metadata page or an image, for any source, in any pipeline —
// goes through a Fetcher.
//
// That is the whole point of the package. Proxy, rate limit, cooldown, retries and
// cancellation are properties of "how this process talks to the network", not of
// four parsers, so they live here once: a source says *what* it wants fetched, and
// the Fetcher decides how fast, through which exit, how many times, and how to
// report the failure.
//
// It uses net/http and a browser User-Agent and nothing else. There is no TLS
// fingerprint library and no FlareSolverr key: a site that refuses this client is
// reported as a failure with a reason code, not worked around.
package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/2017fighting/mdc-lite/internal/source"
)

// BrowserUA is the one User-Agent every request sends. It is a code constant, not
// a setting: a site that only answers browsers needs to see a browser, and there
// is exactly one right answer to "which browser".
const BrowserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// The documented defaults: a per-request timeout, and two retries (three attempts)
// for the failures that are worth retrying.
const (
	DefaultTimeout = 30 * time.Second
	DefaultRetries = 2
	// maxBodyBytes bounds one response. A metadata page is tens of KB and an image
	// is a few MB; anything past this is not a page we can use, and reading it would
	// be the whole process's memory.
	maxBodyBytes = 64 << 20
)

// retryBackoff is the pause before each retry: 1s, then 4s, plus jitter.
var retryBackoff = []time.Duration{time.Second, 4 * time.Second}

// Options are the process-level transport settings. Everything here comes from
// `config.yaml` (`proxy`) or from a documented default; the per-source overrides
// arrive per request. There is deliberately no User-Agent knob: the UA is a code
// constant, because there is exactly one right answer to "which browser".
type Options struct {
	// Proxy is the process-level exit, as a URL (`http://host:port`). Empty means a
	// direct connection: the exit is the operator's router's business, and the app
	// does not fall back to the environment.
	Proxy string
	// Timeout is one request's deadline. Zero means DefaultTimeout.
	Timeout time.Duration
	// Rate is the default budget per registrable domain. Zero fields fall back to
	// DefaultRate.
	Rate Rate
	// Retries is how many extra attempts one request gets. Zero means
	// DefaultRetries (2); -1 disables retrying. Production never sets it — the
	// number is part of the contract — but a test asserting a single attempt needs
	// a way to say so.
	Retries int
	// MaxFlight caps how many requests are in flight at once across the whole
	// process (`concurrency.requests`). Zero means no cap, which is what a test
	// wants and what a single-source build effectively has; the product sets it
	// from the one place concurrency is configured.
	MaxFlight int
}

// Request is one fetch a source asks for.
//
// It is a GET unless the source says otherwise: dmm's GraphQL endpoint is the one
// site in this build that wants a POST, and it wants a JSON body with it. The verb
// and the body live here rather than in the source so that the transport keeps its
// single job — proxy, rate limit, cooldown, retries, cancellation — for every
// request the process makes, whatever the verb.
type Request struct {
	// Method is "GET" or "POST"; empty means GET.
	Method string
	// URL is the address to try first. It must be absolute.
	URL string
	// Mirrors are alternative addresses for the same request, in the order the
	// operator wrote them. A retryable failure moves to the next unused mirror, so
	// switching mirrors and retrying share one budget: three attempts total, not
	// three per mirror.
	Mirrors []string
	// Headers are per-request headers a source must send (javbus's `Cookie: dv=1`,
	// an image's `Referer`). They are code constants inside the source.
	Headers map[string]string
	// MirrorHeaders, when it has an entry, replaces Headers for the mirror at the
	// same position: a header that names the site (dmm's `Referer:
	// https://video.{domain}/`) has to change when the address does, and only the
	// transport knows which one is being tried. Nil or too short means Headers.
	MirrorHeaders []map[string]string
	// Body is the request body, sent as-is when Method is POST.
	Body []byte
	// Proxy overrides the process-level exit for this source.
	Proxy string
	// Rate overrides the default budget and cooldown for this source.
	Rate *Rate
	// Timeout overrides one request's deadline.
	Timeout time.Duration
}

// Response is one successful answer: any 2xx, plus 404.
//
// 404 is a response rather than an error because "the site has no such work" is an
// answer a source needs to see (javbus's whole notfound judgement is this status).
// Every other refusal is an error with a reason code, so a transport failure can
// never be mistaken for "no such work".
type Response struct {
	// URL is the address that answered, after redirects. A source builds its image
	// addresses and its Referer from it, so it has to be the one that worked, not
	// the one that was asked.
	URL    string
	Status int
	Body   []byte
}

// Error is a request that produced no answer, classified into one of the six
// reason codes. The transport produces the first five; `parse` and `blocked` on a
// 200 page are the source's own to report.
type Error struct {
	// Code is the reason code (`source_runs.reason`).
	Code string
	// Status is the HTTP status that failed, when there was one.
	Status int
	// URL is the address that failed.
	URL string
	// Key is the bucket that answered or was punished (a registrable domain).
	Key string
	// CooldownFor is how much of a punishment was left when the request failed
	// without being sent.
	CooldownFor time.Duration
	// Err is the cause, when the failure was not an HTTP status.
	Err error
}

// Reason makes *Error one of the source package's Reasoned errors, which is how
// `source_runs.reason` gets filled without the pipeline knowing about this package.
func (e *Error) Reason() string { return e.Code }

// Unwrap exposes the cause so errors.Is keeps working.
func (e *Error) Unwrap() error { return e.Err }

func (e *Error) Error() string {
	switch {
	case e.Code == source.ReasonCooldown && e.Key != "":
		// A cooled-down request never left the process, so it has no URL to name;
		// naming the bucket is what tells the operator which site to wait for.
		if e.URL == "" {
			return fmt.Sprintf("fetch: %s is cooling down for %s", e.Key, e.CooldownFor.Round(time.Second))
		}
		return fmt.Sprintf("fetch: %s is cooling down for %s (%s)", e.Key, e.CooldownFor.Round(time.Second), e.URL)
	case e.Status != 0:
		return fmt.Sprintf("fetch: %s answered %d (%s)", e.URL, e.Status, e.Code)
	case e.Err != nil:
		return fmt.Sprintf("fetch: %s: %v", e.URL, e.Err)
	default:
		return fmt.Sprintf("fetch: %s failed (%s)", e.URL, e.Code)
	}
}

// Fetcher is one process's transport: one client cache, one rate limiter, one
// cooldown table. Create it once and hand the same instance to every source, or
// "process-wide" stops meaning anything.
//
// It is safe for concurrent use.
type Fetcher struct {
	proxy   string
	timeout time.Duration
	rate    Rate
	retries int

	lim   *limiter
	clock clock
	// flight is the process-wide in-flight cap, or nil when uncapped.
	flight chan struct{}

	mu      sync.Mutex
	clients map[string]*http.Client
	// buildErrors remembers a proxy string that cannot be a proxy, so a broken
	// config is reported once instead of re-parsed on every request.
	buildErrors map[string]error
}

// New builds the process's Fetcher.
func New(opts Options) *Fetcher {
	c := realClock()
	f := &Fetcher{
		proxy:       strings.TrimSpace(opts.Proxy),
		timeout:     opts.Timeout,
		rate:        opts.Rate.withDefaults(),
		retries:     opts.Retries,
		clock:       c,
		lim:         newLimiter(c),
		clients:     map[string]*http.Client{},
		buildErrors: map[string]error{},
	}
	if f.timeout <= 0 {
		f.timeout = DefaultTimeout
	}
	if f.retries == 0 {
		f.retries = DefaultRetries
	}
	if f.retries < 0 {
		f.retries = 0
	}
	if opts.MaxFlight > 0 {
		f.flight = make(chan struct{}, opts.MaxFlight)
	}
	return f
}

// Do fetches one request, honouring the cooldown, the rate limit, the retry budget
// and the caller's context.
//
// The order matters: the cooldown is checked before anything is sent (and fails at
// once, it never queues), the rate limit is waited out before each attempt, and a
// retryable failure moves to the next mirror with a 1s / 4s backoff. Cancellation
// is honoured at every wait.
func (f *Fetcher) Do(ctx context.Context, req Request) (*Response, error) {
	targets, err := parseTargets(req)
	if err != nil {
		return nil, err
	}
	rate := budget{Rate: f.rate}
	if req.Rate != nil {
		rate = budget{Rate: req.Rate.withDefaults(), configured: true}
	}
	timeout := f.timeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	proxy := f.proxy
	if p := strings.TrimSpace(req.Proxy); p != "" {
		proxy = p
	}
	client, err := f.client(proxy)
	if err != nil {
		return nil, err
	}
	var last *Error
	for attempt := 0; attempt <= f.retries; attempt++ {
		index := min(attempt, len(targets)-1)
		target := targets[index]
		sent := req
		if index > 0 && index-1 < len(req.MirrorHeaders) && req.MirrorHeaders[index-1] != nil {
			// The header names the site, so it has to follow the address.
			sent.Headers = req.MirrorHeaders[index-1]
		}
		key := bucketKey(target)
		if err := f.lim.acquire(ctx, key, rate); err != nil {
			return nil, err
		}
		if err := f.enter(ctx); err != nil {
			return nil, err
		}
		resp, err := f.once(ctx, client, target, sent, timeout)
		f.leave()
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			// The caller gave up: no reason code, no retry, no cooldown. The task is
			// being cancelled, which is not a fact about the site.
			return nil, ctx.Err()
		}
		var fe *Error
		if !errors.As(err, &fe) {
			return nil, err
		}
		if fe.Status == http.StatusForbidden || fe.Status == http.StatusTooManyRequests || fe.Status == http.StatusServiceUnavailable {
			f.lim.cool(key, rate.Cooldown)
		}
		last = fe
		if !retryable(fe.Code) || attempt == f.retries {
			break
		}
		if err := f.clock.sleep(ctx, f.clock.jitter(retryBackoff[min(attempt, len(retryBackoff)-1)])); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
	}
	return nil, last
}

// enter takes one of the process's in-flight slots, waiting for one or for the
// caller to give up. The slot is taken *after* the domain bucket: a request queued on
// its domain must not hold a global slot while it waits.
func (f *Fetcher) enter(ctx context.Context) error {
	if f.flight == nil {
		return nil
	}
	select {
	case f.flight <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Fetcher) leave() {
	if f.flight == nil {
		return
	}
	select {
	case <-f.flight:
	default:
	}
}

// retryable is the closed list of failures worth another attempt: a connection
// that did not come up (dial error, TLS EOF, a reset) and a 5xx. A 404 is an
// answer, another 4xx is a refusal, a timeout already used its budget, and a
// cooldown is a punishment — retrying any of those makes it worse.
func retryable(code string) bool {
	return code == source.ReasonEgressUnavailable || code == source.ReasonHTTP5xx
}

// once performs one attempt: no retry loop, no cooldown bookkeeping. It returns a
// Response for 2xx and 404, and a classified *Error for everything else.
func (f *Fetcher) once(ctx context.Context, client *http.Client, target *url.URL, request Request, timeout time.Duration) (*Response, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	method := request.Method
	if method == "" {
		method = http.MethodGet
	}
	var payload io.Reader
	if len(request.Body) > 0 {
		payload = bytes.NewReader(request.Body)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, target.String(), payload)
	if err != nil {
		return nil, &Error{Code: source.ReasonParse, URL: target.String(), Err: err}
	}
	req.Header.Set("User-Agent", BrowserUA)
	for k, v := range request.Headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, f.classify(ctx, target, err)
	}
	defer func() {
		// The body is read before this runs; a close error on a body we finished
		// reading has nothing left to tell us.
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, f.classify(ctx, target, err)
	}
	if len(body) > maxBodyBytes {
		return nil, &Error{Code: source.ReasonParse, Status: resp.StatusCode, URL: target.String(),
			Err: fmt.Errorf("the answer is larger than %d bytes", maxBodyBytes)}
	}
	final := target.String()
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	out := &Response{URL: final, Status: resp.StatusCode, Body: body}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode == http.StatusNotFound:
		return out, nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable:
		// One punishment for all three: they are the site saying "you, slower".
		return nil, &Error{Code: source.ReasonCooldown, Status: resp.StatusCode, URL: final}
	case resp.StatusCode >= 500:
		return nil, &Error{Code: source.ReasonHTTP5xx, Status: resp.StatusCode, URL: final}
	default:
		// Any other 4xx: the site refused this request outright. 404 is handled above
		// as an answer and 403/429/503 as a punishment, so what is left is a hard "no"
		// with no page to inspect — `blocked` is the honest bucket of the six, because
		// `parse` would claim we read a page and found it unreadable.
		return nil, &Error{Code: source.ReasonBlocked, Status: resp.StatusCode, URL: final}
	}
}

// classify turns a transport error into a reason code. The caller's own
// cancellation is returned as-is, because that is not a fact about the site.
func (f *Fetcher) classify(ctx context.Context, target *url.URL, err error) error {
	if ctx.Err() != nil {
		return err
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return &Error{Code: source.ReasonTimeout, URL: target.String(), Err: err}
	}
	// A dial error, a TLS handshake cut short, a reset: from here they are one
	// fact — the exit to this site does not work.
	return &Error{Code: source.ReasonEgressUnavailable, URL: target.String(), Err: err}
}

// client returns the HTTP client for a proxy, building it once. An empty proxy is a
// direct connection: the process does not read HTTP_PROXY from the environment,
// because "the exit is the router's job" is only true if the app has no second
// opinion about it.
func (f *Fetcher) client(proxy string) (*http.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.clients[proxy]; ok {
		return c, nil
	}
	if err, ok := f.buildErrors[proxy]; ok {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			f.buildErrors[proxy] = fmt.Errorf("proxy %q: %w", proxy, err)
			return nil, f.buildErrors[proxy]
		}
		if u.Scheme == "" || u.Host == "" {
			f.buildErrors[proxy] = fmt.Errorf("proxy %q needs a scheme and a host", proxy)
			return nil, f.buildErrors[proxy]
		}
		tr.Proxy = http.ProxyURL(u)
	}
	c := &http.Client{Transport: tr}
	f.clients[proxy] = c
	return c, nil
}

// parseTargets parses the request's address and mirrors into one ordered list.
func parseTargets(req Request) ([]*url.URL, error) {
	raw := append([]string{req.URL}, req.Mirrors...)
	out := make([]*url.URL, 0, len(raw))
	for _, s := range raw {
		u, err := url.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("fetch: %q: %w", s, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("fetch: %q is not an http(s) URL", s)
		}
		if u.Host == "" {
			return nil, fmt.Errorf("fetch: %q has no host", s)
		}
		out = append(out, u)
	}
	return out, nil
}
