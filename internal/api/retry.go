package api

import (
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	backoffBase   = time.Second
	backoffMax    = 30 * time.Second
	retryAfterMax = 2 * time.Minute
)

// backoff returns the wait before the next attempt. A server-provided
// Retry-After wins (capped); otherwise exponential with full jitter.
func backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, retryAfterMax)
	}
	ceiling := min(backoffBase<<(attempt-1), backoffMax)
	// Full jitter, but never less than half the ceiling so retries stay spaced out.
	return ceiling/2 + rand.N(ceiling/2+1) //nolint:gosec // jitter, not crypto
}

// parseRetryAfter reads a Retry-After header in seconds or HTTP-date form.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}
