package fetch

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/2017fighting/mdc-lite/internal/source"
)

// fakeClock is the test seam: the limiter and the retry loop ask it for the time
// and ask it to wait, so a rate-limit pause or a 4-second backoff costs nothing.
type fakeClock struct {
	t     *testing.T
	now   time.Time
	slept []time.Duration
	// onSleep, when set, runs instead of advancing the clock: it is how a test
	// asserts that a wait respected the context.
	onSleep func(d time.Duration) error
}

func newFakeClock(t *testing.T) *fakeClock {
	return &fakeClock{t: t, now: time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) clock() clock {
	return clock{
		now: func() time.Time { return c.now },
		sleep: func(ctx context.Context, d time.Duration) error {
			c.slept = append(c.slept, d)
			if c.onSleep != nil {
				return c.onSleep(d)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			c.now = c.now.Add(d)
			return nil
		},
		// Jitter is deterministic here: a test asserts the documented 1s and 4s, and
		// the jitter itself is the one thing not worth asserting.
		jitter: func(d time.Duration) time.Duration { return d },
	}
}

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func TestLimiterAllowsBurstThenOnePerSecond(t *testing.T) {
	c := newFakeClock(t)
	l := newLimiter(c.clock())
	rate := budget{Rate: DefaultRate()}

	// A burst of two goes out at once: the second request waits for nothing.
	for i := 0; i < 2; i++ {
		if err := l.acquire(context.Background(), "javbus.com", rate); err != nil {
			t.Fatalf("request %d of the burst: %v", i+1, err)
		}
	}
	if len(c.slept) != 0 {
		t.Fatalf("the burst waited: %v", c.slept)
	}

	// The third waits one second: 1 req/s is the whole budget.
	if err := l.acquire(context.Background(), "javbus.com", rate); err != nil {
		t.Fatal(err)
	}
	if len(c.slept) != 1 || c.slept[0] != time.Second {
		t.Fatalf("third request waited %v, want exactly 1s", c.slept)
	}
}

func TestLimiterBucketsPerRegistrableDomain(t *testing.T) {
	c := newFakeClock(t)
	l := newLimiter(c.clock())
	rate := budget{Rate: DefaultRate()}

	// Two hosts of one site share the bucket, so the third request in total is the
	// one that waits.
	for _, key := range []string{"dmm.co.jp", "dmm.co.jp", "dmm.co.jp"} {
		if err := l.acquire(context.Background(), key, rate); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.slept) != 1 {
		t.Fatalf("waits = %v, want one (one bucket for one site)", c.slept)
	}

	// A different site starts with its own burst.
	c.slept = nil
	if err := l.acquire(context.Background(), "javbus.com", rate); err != nil {
		t.Fatal(err)
	}
	if len(c.slept) != 0 {
		t.Fatalf("a different domain waited: %v", c.slept)
	}
}

func TestCooldownFailsFastAndBlocksTheWholeDomain(t *testing.T) {
	c := newFakeClock(t)
	l := newLimiter(c.clock())
	rate := budget{Rate: DefaultRate()}

	l.cool("dmm.co.jp", rate.Cooldown)

	err := l.acquire(context.Background(), "dmm.co.jp", rate)
	var fe *Error
	if !errors.As(err, &fe) {
		t.Fatalf("acquire during a cooldown = %v, want a cooldown error", err)
	}
	if fe.Code != source.ReasonCooldown {
		t.Errorf("code = %q, want %q", fe.Code, source.ReasonCooldown)
	}
	if len(c.slept) != 0 {
		t.Fatalf("a cooldown queued instead of failing fast: %v", c.slept)
	}

	// Still inside the five minutes: still failing.
	c.advance(4 * time.Minute)
	if err := l.acquire(context.Background(), "dmm.co.jp", rate); err == nil {
		t.Fatal("a cooldown that has not expired let a request through")
	}

	// Past it, the site is forgiven.
	c.advance(61 * time.Second)
	if err := l.acquire(context.Background(), "dmm.co.jp", rate); err != nil {
		t.Fatalf("after the cooldown: %v", err)
	}
}

func TestCooldownIsNotShortenedByALaterOne(t *testing.T) {
	c := newFakeClock(t)
	l := newLimiter(c.clock())

	l.cool("javbus.com", 10*time.Minute)
	c.advance(time.Minute)
	l.cool("javbus.com", time.Minute)

	if got := l.cooling("javbus.com"); got != 9*time.Minute {
		t.Errorf("remaining cooldown = %v, want 9m (a shorter punishment must not win)", got)
	}
}

func TestCooldownSleepRespectsTheContext(t *testing.T) {
	c := newFakeClock(t)
	l := newLimiter(c.clock())
	rate := budget{Rate: DefaultRate()}

	// Spend the burst, then cancel while the third request is waiting its second.
	for i := 0; i < 2; i++ {
		if err := l.acquire(context.Background(), "javbus.com", rate); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.onSleep = func(time.Duration) error {
		cancel()
		return ctx.Err()
	}
	if err := l.acquire(ctx, "javbus.com", rate); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait = %v, want context.Canceled", err)
	}
}

func TestSourceRateOverridesTheDefaultOnlyWhenWritten(t *testing.T) {
	c := newFakeClock(t)
	l := newLimiter(c.clock())

	// A source that wrote its own rate gets it.
	fast := budget{Rate: Rate{RequestsPerSecond: 10, Burst: 1, Cooldown: time.Minute}, configured: true}
	for i := 0; i < 3; i++ {
		if err := l.acquire(context.Background(), "javdb.com", fast); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.slept) == 0 {
		t.Fatal("a burst of 1 did not wait for the second request")
	}
	for _, d := range c.slept {
		if d != 100*time.Millisecond {
			t.Fatalf("waits = %v, want 100ms (10 req/s)", c.slept)
		}
	}

	// A source that wrote nothing inherits whatever the bucket already has instead
	// of resetting it: a shared domain has one budget.
	c.slept = nil
	if err := l.acquire(context.Background(), "javdb.com", budget{Rate: DefaultRate()}); err != nil {
		t.Fatal(err)
	}
	if len(c.slept) != 1 || c.slept[0] != 100*time.Millisecond {
		t.Fatalf("waits = %v, want the configured 100ms to survive", c.slept)
	}
}
