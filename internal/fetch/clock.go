package fetch

import (
	"context"
	"math/rand/v2"
	"time"
)

// clock is the only place the Fetcher learns the time and waits. It exists so a
// test can assert a rate-limit pause or a retry backoff without spending the real
// seconds: the limiter and the retry loop both read the clock through this struct
// and never call time.Now or time.Sleep themselves.
type clock struct {
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error
	jitter func(d time.Duration) time.Duration
}

func realClock() clock {
	return clock{
		now:    time.Now,
		sleep:  sleepCtx,
		jitter: addJitter,
	}
}

// sleepCtx waits d, and gives up the moment ctx is done: a rate-limit pause or a
// retry backoff is never allowed to outlive the caller's cancellation.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// addJitter spreads a backoff by up to a quarter of its length, so several tasks
// that hit the same 5xx do not come back in lockstep.
func addJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return d + rand.N(d/4+1)
}
