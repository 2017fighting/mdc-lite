package fetch

import (
	"context"
	"sync"
	"time"

	"github.com/2017fighting/mdc-lite/internal/source"
)

// Rate is one domain's request budget: how fast requests may go out, how many may
// go at once, and how long a punishment lasts.
//
// The process has one default (one request per second, burst two, five minutes of
// cooldown); a source overrides it with `scrape.with.sources.<id>.rate`, because
// the site that punishes a burst is not the same for all four sources.
type Rate struct {
	RequestsPerSecond float64
	Burst             int
	Cooldown          time.Duration
}

// DefaultRate is the documented default: 1 req/s with a burst of 2, and a 5 min
// punishment for 403/429/503.
func DefaultRate() Rate {
	return Rate{RequestsPerSecond: 1, Burst: 2, Cooldown: 5 * time.Minute}
}

// withDefaults fills in the parts a config left out. The three fields are
// independent so `rate: {cooldown: 10m}` keeps the default speed.
func (r Rate) withDefaults() Rate {
	d := DefaultRate()
	if r.RequestsPerSecond <= 0 {
		r.RequestsPerSecond = d.RequestsPerSecond
	}
	if r.Burst < 1 {
		r.Burst = d.Burst
	}
	if r.Cooldown <= 0 {
		r.Cooldown = d.Cooldown
	}
	return r
}

// budget is one request's numbers plus where they came from: a configured rate is
// allowed to adopt a shared domain's bucket, a defaulted one is not. The two travel
// together because the flag is meaningless without the numbers beside it.
type budget struct {
	Rate
	configured bool
}

// limiter holds the process-wide rate and cooldown state, keyed by registrable
// domain. It is safe for concurrent use, and it is the same object for metadata
// and images: a site counts both.
//
// The state is in memory only. A restart forgets every cooldown, which is what
// "状态只在内存（重启即忘）" asks for: a punishment is a fact about this process's
// behaviour, not something to persist.
type limiter struct {
	clock   clock
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	// tokens is the current allowance. It may go negative: a negative value is a
	// reservation for a future slot, which is how concurrent callers queue up in
	// the right order instead of all waking up together.
	tokens float64
	last   time.Time
	rate   float64
	burst  float64

	cooldownUntil time.Time
}

func newLimiter(c clock) *limiter {
	return &limiter{clock: c, buckets: map[string]*bucket{}}
}

// acquire blocks until one request to key may be sent. A cooled-down key fails at
// once — it does not queue — because a cooldown means "this site is punishing us
// for five minutes", and waiting five minutes inside a task is worse than failing
// the source and moving on.
//
// A configured budget adopts the bucket's numbers; a defaulted one accepts whatever
// the bucket already has, so one source's default request does not reset another
// source's configured budget for a domain the two share.
func (l *limiter) acquire(ctx context.Context, key string, budget budget) error {
	at, cooling := l.reserve(l.clock.now(), key, budget)
	if cooling > 0 {
		return &Error{Code: source.ReasonCooldown, CooldownFor: cooling, Key: key}
	}
	if d := at.Sub(l.clock.now()); d > 0 {
		if err := l.clock.sleep(ctx, d); err != nil {
			return err
		}
		// Another goroutine may have earned a 403 while this one was waiting.
		if cooling := l.cooling(key); cooling > 0 {
			return &Error{Code: source.ReasonCooldown, CooldownFor: cooling, Key: key}
		}
	}
	return nil
}

// reserve takes one token and returns when the request may go out, or the
// remaining cooldown when the key is being punished.
func (l *limiter) reserve(now time.Time, key string, budget budget) (time.Time, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	bk, ok := l.buckets[key]
	if !ok {
		bk = &bucket{tokens: float64(budget.Burst), last: now, rate: budget.RequestsPerSecond, burst: float64(budget.Burst)}
		l.buckets[key] = bk
	}
	if budget.configured {
		bk.rate, bk.burst = budget.RequestsPerSecond, float64(budget.Burst)
		if bk.tokens > bk.burst {
			bk.tokens = bk.burst
		}
	}
	if now.Before(bk.cooldownUntil) {
		return time.Time{}, bk.cooldownUntil.Sub(now)
	}
	if elapsed := now.Sub(bk.last); elapsed > 0 {
		bk.tokens = min(bk.burst, bk.tokens+elapsed.Seconds()*bk.rate)
	}
	bk.last = now
	bk.tokens--
	if bk.tokens >= 0 {
		return now, 0
	}
	deficit := -bk.tokens
	return now.Add(time.Duration(deficit / bk.rate * float64(time.Second))), 0
}

// cool punishes key until now+d. A later punishment does not shorten an earlier
// one: the site is already cross.
func (l *limiter) cool(key string, d time.Duration) {
	if d <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		def := DefaultRate()
		b = &bucket{tokens: float64(def.Burst), last: l.clock.now(), rate: def.RequestsPerSecond, burst: float64(def.Burst)}
		l.buckets[key] = b
	}
	if until := l.clock.now().Add(d); until.After(b.cooldownUntil) {
		b.cooldownUntil = until
	}
}

// cooling reports how much of key's cooldown is left, or 0.
func (l *limiter) cooling(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok {
		return 0
	}
	if d := b.cooldownUntil.Sub(l.clock.now()); d > 0 {
		return d
	}
	return 0
}
