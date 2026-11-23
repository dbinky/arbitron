package runneragent

import "time"

// Backoff is exponential (base * 2^attempt, capped at max) with "equal
// jitter": the result lies in [d/2, d], so a reconnect storm spreads out but
// never retries instantly.
func Backoff(attempt int, base, max time.Duration, rnd func() float64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	d := base
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return time.Duration(float64(d) * (0.5 + 0.5*rnd()))
}
