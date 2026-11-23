package runneragent_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/runneragent"
)

func TestBackoffSequenceAndCap(t *testing.T) {
	one := func() float64 { return 1 }
	want := []time.Duration{
		500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second,
		8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		assert.Equal(t, w, runneragent.Backoff(i, 500*time.Millisecond, 30*time.Second, one), "attempt %d", i)
	}
}

func TestBackoffJitterStaysWithinHalfToFull(t *testing.T) {
	zero := func() float64 { return 0 }
	assert.Equal(t, 250*time.Millisecond, runneragent.Backoff(0, 500*time.Millisecond, 30*time.Second, zero))
	assert.Equal(t, 15*time.Second, runneragent.Backoff(40, 500*time.Millisecond, 30*time.Second, zero))
	half := func() float64 { return 0.5 }
	assert.Equal(t, 750*time.Millisecond, runneragent.Backoff(1, 500*time.Millisecond, 30*time.Second, half))
}

func TestBackoffHugeAttemptDoesNotOverflow(t *testing.T) {
	got := runneragent.Backoff(10_000, 500*time.Millisecond, 30*time.Second, func() float64 { return 1 })
	assert.Equal(t, 30*time.Second, got)
	assert.Equal(t, 500*time.Millisecond, runneragent.Backoff(-3, 500*time.Millisecond, 30*time.Second, func() float64 { return 1 }))
}
