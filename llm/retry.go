package llm

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Retry policy for the OpenAI-compatible client.
//
// Adapted from github.com/owainlewis/neo, internal/llm/retry —
// Copyright (c) 2024 Neo Contributors, MIT licensed. Trimmed to the
// single-provider case and kept unexported: it is an implementation
// detail of openAIClient, not a seam. Promote it to its own package if a
// second Client implementation ever needs it.

const (
	// maxRetryDelay caps any single backoff, including one a server asks
	// for via Retry-After. Waiting longer than this inside a Run is worse
	// than failing: the loop has a RunTimeout to respect and the caller
	// would rather see the 429 than stall.
	maxRetryDelay = 30 * time.Second

	// defaultMaxRetries is the number of retries after the initial
	// attempt, so up to four requests total.
	defaultMaxRetries = 3

	// defaultRetryBaseDelay is the first backoff; each subsequent
	// attempt doubles it.
	defaultRetryBaseDelay = 500 * time.Millisecond
)

// retryAfter carries a parsed Retry-After hint. Present distinguishes an
// explicit "retry immediately" hint (Delay 0) from a missing or
// unparseable header, which falls back to exponential backoff.
type retryAfter struct {
	Delay   time.Duration
	Present bool
}

func absentRetryAfter() retryAfter { return retryAfter{} }

// isRetryableStatus reports whether a status is worth another attempt:
// 429 (rate limited) and 5xx (upstream trouble). Every 4xx other than
// 429 is a request the server will reject identically next time.
func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// parseRetryAfterHeader reads both Retry-After forms — delay-seconds and
// an HTTP-date — relative to now. Anything else is Absent, and a date
// already in the past becomes a zero delay rather than a negative one.
func parseRetryAfterHeader(value string, now time.Time) retryAfter {
	value = strings.TrimSpace(value)
	if value == "" {
		return absentRetryAfter()
	}
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds < 0 {
			return absentRetryAfter()
		}
		if seconds > int64(maxRetryDelay/time.Second) {
			return retryAfter{Delay: maxRetryDelay, Present: true}
		}
		return retryAfter{Delay: time.Duration(seconds) * time.Second, Present: true}
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return absentRetryAfter()
	}
	delay := when.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return retryAfter{Delay: capRetryDelay(delay), Present: true}
}

type jitterFunc func(max time.Duration) time.Duration

// retryDelay is how long to wait before attempt+1. A server-supplied
// Retry-After wins outright — it knows when its own window reopens, and
// adding jitter on top would only push the retry past it.
func retryDelay(base time.Duration, attempt int, hint retryAfter) time.Duration {
	return retryDelayWithJitter(base, attempt, hint, randomJitter)
}

func retryDelayWithJitter(base time.Duration, attempt int, hint retryAfter, jitter jitterFunc) time.Duration {
	if hint.Present {
		return capRetryDelay(hint.Delay)
	}
	return backoffDelayWithJitter(base, attempt, jitter)
}

// backoffDelayWithJitter adds up to 50% of the delay on top of it, so
// concurrent Runs that hit the same rate limit spread out instead of
// retrying in lockstep. The jitter is clamped so the total still
// respects maxRetryDelay.
func backoffDelayWithJitter(base time.Duration, attempt int, jitter jitterFunc) time.Duration {
	d := exponentialDelay(base, attempt)
	if jitter == nil || d >= maxRetryDelay {
		return d
	}
	span := d / 2
	if remaining := maxRetryDelay - d; span > remaining {
		span = remaining
	}
	if span <= 0 {
		return d
	}
	j := jitter(span)
	if j < 0 {
		j = 0
	}
	if j > span {
		j = span
	}
	return capRetryDelay(d + j)
}

func exponentialDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		base = defaultRetryBaseDelay
	}
	if attempt < 0 {
		attempt = 0
	}
	d := base
	for range attempt {
		// Doubling past half the cap would overflow into it anyway.
		if d >= maxRetryDelay/2 {
			return maxRetryDelay
		}
		d *= 2
	}
	return capRetryDelay(d)
}

func capRetryDelay(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > maxRetryDelay {
		return maxRetryDelay
	}
	return d
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(max) + 1))
}

// sleepCtx waits for d, or returns early with the context's error if the
// Run is cancelled or times out mid-backoff.
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
