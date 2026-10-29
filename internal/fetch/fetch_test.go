package fetch

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/source"
)

// testFetcher builds a Fetcher whose clock a test controls, so the rate limit and
// the retry backoff are assertions rather than wall-clock waits.
func testFetcher(t *testing.T, opts Options) (*Fetcher, *fakeClock) {
	t.Helper()
	f := New(opts)
	c := newFakeClock(t)
	f.clock = c.clock()
	f.lim = newLimiter(f.clock)
	return f, c
}

// deadURL is an address nothing listens on: a connection error the Fetcher has to
// classify and retry.
func deadURL(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + addr + "/"
}

func TestDoSendsTheBrowserUAAndThePerRequestHeaders(t *testing.T) {
	var gotUA, gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotCookie = r.UserAgent(), r.Header.Get("Cookie")
		_, _ = w.Write([]byte("page"))
	}))
	defer srv.Close()

	f, _ := testFetcher(t, Options{})
	resp, err := f.Do(context.Background(), Request{URL: srv.URL, Headers: map[string]string{"Cookie": "dv=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.Body) != "page" {
		t.Errorf("body = %q", resp.Body)
	}
	if gotUA != BrowserUA {
		t.Errorf("User-Agent = %q, want the browser constant", gotUA)
	}
	if gotCookie != "dv=1" {
		t.Errorf("Cookie = %q, want the source's own header", gotCookie)
	}
	if resp.URL != srv.URL {
		t.Errorf("response URL = %q, want the address that answered", resp.URL)
	}
}

func TestDoRetriesA5xxTwiceThenSucceeds(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f, c := testFetcher(t, Options{})
	resp, err := f.Do(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("after two failures: %v", err)
	}
	if string(resp.Body) != "ok" {
		t.Errorf("body = %q", resp.Body)
	}
	if calls.Load() != 3 {
		t.Errorf("attempts = %d, want 3 (one try plus two retries)", calls.Load())
	}
	if len(c.slept) != 2 || c.slept[0] != time.Second || c.slept[1] != 4*time.Second {
		t.Errorf("backoffs = %v, want 1s then 4s", c.slept)
	}
}

func TestDoGivesUpAfterTwoRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	f, _ := testFetcher(t, Options{})
	_, err := f.Do(context.Background(), Request{URL: srv.URL})
	if got := source.ReasonOf(err); got != source.ReasonHTTP5xx {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonHTTP5xx)
	}
	if calls.Load() != 3 {
		t.Errorf("attempts = %d, want exactly 3", calls.Load())
	}
}

func TestDoDoesNotRetryA404AndHandsItBack(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f, c := testFetcher(t, Options{})
	resp, err := f.Do(context.Background(), Request{URL: srv.URL})
	if err != nil {
		t.Fatalf("a 404 must be an answer, not an error: %v", err)
	}
	if resp.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.Status)
	}
	if calls.Load() != 1 || len(c.slept) != 0 {
		t.Errorf("attempts = %d, waits = %v: a 404 is not retried", calls.Load(), c.slept)
	}
}

func TestDoDoesNotRetryAnyOther4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	f, _ := testFetcher(t, Options{})
	_, err := f.Do(context.Background(), Request{URL: srv.URL})
	if got := source.ReasonOf(err); got != source.ReasonBlocked {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonBlocked)
	}
	if calls.Load() != 1 {
		t.Errorf("attempts = %d, want 1: a 4xx is a refusal, not a hiccup", calls.Load())
	}
}

func TestDoPunishesAndThenFailsFastForTheWholeDomain(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			var punish atomic.Bool
			punish.Store(true)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if punish.Load() {
					w.WriteHeader(status)
					return
				}
				_, _ = w.Write([]byte("ok"))
			}))
			defer srv.Close()

			f, c := testFetcher(t, Options{})
			_, err := f.Do(context.Background(), Request{URL: srv.URL})
			if got := source.ReasonOf(err); got != source.ReasonCooldown {
				t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonCooldown)
			}
			if calls.Load() != 1 {
				t.Fatalf("attempts = %d: a punishment is not retried", calls.Load())
			}

			// The next request fails without reaching the site: it does not queue.
			punish.Store(false)
			_, err = f.Do(context.Background(), Request{URL: srv.URL})
			var fe *Error
			if !errors.As(err, &fe) || fe.Code != source.ReasonCooldown || fe.CooldownFor <= 0 {
				t.Fatalf("second request = %v, want a cooldown error that names the remaining time", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("the site saw %d requests: a cooled-down domain must fail fast", calls.Load())
			}
			if len(c.slept) != 0 {
				t.Fatalf("a cooldown queued instead of failing: %v", c.slept)
			}

			// Five minutes later the site is forgiven.
			c.advance(5*time.Minute + time.Second)
			if _, err := f.Do(context.Background(), Request{URL: srv.URL}); err != nil {
				t.Fatalf("after the cooldown: %v", err)
			}
		})
	}
}

func TestMirrorSwitchSharesTheRetryBudget(t *testing.T) {
	var calls atomic.Int32
	failing := func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
	}
	a, b := failing(), failing()
	defer a.Close()
	defer b.Close()

	f, _ := testFetcher(t, Options{})
	_, err := f.Do(context.Background(), Request{URL: deadURL(t), Mirrors: []string{a.URL, b.URL}})
	if got := source.ReasonOf(err); got != source.ReasonHTTP5xx {
		t.Fatalf("reason = %q (%v)", got, err)
	}
	// One attempt on the dead address, one per mirror: the mirrors did not each get
	// their own budget of three.
	if calls.Load() != 2 {
		t.Errorf("the two mirrors saw %d requests, want 2 (one attempt each)", calls.Load())
	}
}

func TestMirrorSwitchMovesOnAfterAConnectionError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("from the mirror"))
	}))
	defer srv.Close()

	f, c := testFetcher(t, Options{})
	resp, err := f.Do(context.Background(), Request{URL: deadURL(t), Mirrors: []string{srv.URL}})
	if err != nil {
		t.Fatalf("the mirror answered, so the request should have: %v", err)
	}
	if string(resp.Body) != "from the mirror" {
		t.Errorf("body = %q", resp.Body)
	}
	if resp.URL != srv.URL {
		t.Errorf("response URL = %q, want the mirror that answered", resp.URL)
	}
	if len(c.slept) != 1 || c.slept[0] != time.Second {
		t.Errorf("backoff = %v, want the documented 1s before the mirror", c.slept)
	}
}

func TestDoClassifiesAConnectionErrorAsEgressUnavailable(t *testing.T) {
	f, _ := testFetcher(t, Options{Retries: -1})
	_, err := f.Do(context.Background(), Request{URL: deadURL(t)})
	if got := source.ReasonOf(err); got != source.ReasonEgressUnavailable {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonEgressUnavailable)
	}
}

func TestDoClassifiesATimeoutAndDoesNotRetryIt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(80 * time.Millisecond)
		_, _ = w.Write([]byte("too late"))
	}))
	defer srv.Close()

	f, _ := testFetcher(t, Options{})
	_, err := f.Do(context.Background(), Request{URL: srv.URL, Timeout: 10 * time.Millisecond})
	if got := source.ReasonOf(err); got != source.ReasonTimeout {
		t.Fatalf("reason = %q (%v), want %q", got, err, source.ReasonTimeout)
	}
	if calls.Load() != 1 {
		t.Errorf("attempts = %d: a timeout already used its budget", calls.Load())
	}
}

func TestDoHonoursCancellationDuringABackoff(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	f, c := testFetcher(t, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	c.onSleep = func(time.Duration) error {
		cancel()
		return ctx.Err()
	}
	_, err := f.Do(ctx, Request{URL: srv.URL})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request = %v, want context.Canceled", err)
	}
	if source.ReasonOf(err) != "" {
		t.Errorf("a cancellation got a reason code: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("attempts = %d: a cancelled request must not be retried", calls.Load())
	}
}

func TestDoWaitsForTheRateLimitBetweenRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f, c := testFetcher(t, Options{})
	for i := 0; i < 3; i++ {
		if _, err := f.Do(context.Background(), Request{URL: srv.URL}); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.slept) != 1 || c.slept[0] != time.Second {
		t.Fatalf("waits = %v, want one second for the third request", c.slept)
	}
}

func TestDoRejectsANonHTTPURL(t *testing.T) {
	f, _ := testFetcher(t, Options{})
	if _, err := f.Do(context.Background(), Request{URL: "ftp://example.com/x"}); err == nil {
		t.Fatal("a non-http URL must be refused before anything is sent")
	}
}

func TestProxyIsANoopForLocalTestsAndRefusedWhenMalformed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f, _ := testFetcher(t, Options{})
	if _, err := f.Do(context.Background(), Request{URL: srv.URL, Proxy: "socks5h://"}); err == nil {
		t.Fatal("a proxy without a host must be refused")
	}
}

func TestDoCapsTheRequestsInFlight(t *testing.T) {
	// `concurrency.requests` is a process-wide cap, not a per-source one: it lives on
	// the one seam every request crosses, so no step or source can exceed it.
	var inflight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inflight.Add(-1)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	f := New(Options{
		Rate:      Rate{RequestsPerSecond: 1000, Burst: 100},
		MaxFlight: 2,
	})
	var done atomic.Int32
	for i := 0; i < 6; i++ {
		go func() {
			if _, err := f.Do(context.Background(), Request{URL: srv.URL + "/x"}); err == nil {
				done.Add(1)
			}
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for done.Load() < 6 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := done.Load(); got != 6 {
		t.Fatalf("%d of 6 requests finished; the cap blocked one forever", got)
	}
	if got := peak.Load(); got > 2 {
		t.Errorf("peak in-flight = %d, want the cap of 2 never exceeded", got)
	}
	if got := peak.Load(); got < 2 {
		t.Errorf("peak in-flight = %d, want the cap to be used, not throttled to one", got)
	}
}

func TestMirrorHeadersFollowTheMirror(t *testing.T) {
	// A header that names the site has to change when the address does: the mirror
	// switch happens in here, so this is the only place that can do it.
	var seen string
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Referer")
		_, _ = w.Write([]byte("ok"))
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer first.Close()

	f, _ := testFetcher(t, Options{Rate: Rate{RequestsPerSecond: 1000, Burst: 100}, Retries: 1})
	resp, err := f.Do(context.Background(), Request{
		URL:           first.URL,
		Mirrors:       []string{second.URL},
		Headers:       map[string]string{"Referer": "https://video.first/"},
		MirrorHeaders: []map[string]string{{"Referer": "https://video.second/"}},
	})
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("status = %d, want the mirror's 200", resp.Status)
	}
	if seen != "https://video.second/" {
		t.Errorf("mirror Referer = %q, want the mirror's own site", seen)
	}
}

// TestNewUsesTheSuppliedClock is the fake-clock seam: the limiter must read the
// time and wait through the injected clock, so a test can assert a rate-limit pause
// without spending it. A real sleep here would make the assertion slow and the
// mechanism unobservable.
func TestNewUsesTheSuppliedClock(t *testing.T) {
	var slept []time.Duration
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	f := New(Options{
		Rate: Rate{RequestsPerSecond: 1, Burst: 1},
		Clock: &Clock{
			Now:    func() time.Time { return fixed },
			Sleep:  func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
			Jitter: func(d time.Duration) time.Duration { return d },
		},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	for i := 0; i < 2; i++ {
		if _, err := f.Do(context.Background(), Request{URL: srv.URL}); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if len(slept) != 1 {
		t.Fatalf("sleeps = %v, want exactly the second request's rate-limit wait", slept)
	}
	if slept[0] < time.Second {
		t.Errorf("rate-limit wait = %s, want at least one second at 1 req/s", slept[0])
	}
}
