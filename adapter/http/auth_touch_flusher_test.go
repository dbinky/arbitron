package http

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// drainTouched collects every id the fakeTokenRepo recorded via TouchLastUsed,
// counting per id. It assumes the channel will be drained AFTER the flusher has
// stopped (so no further sends race the read).
func drainTouched(r *fakeTokenRepo) map[uuid.UUID]int {
	counts := map[uuid.UUID]int{}
	for {
		select {
		case id := <-r.touchedIDs:
			counts[id]++
		default:
			return counts
		}
	}
}

// Happy path: a flush trigger writes one touch per distinct pending id.
func TestTouchFlusher_FlushesOnTrigger(t *testing.T) {
	repo := newFakeTokenRepo()
	trigger := make(chan time.Time)
	f := newTouchFlusher(repo, trigger)

	id := uuid.New()
	f.touch(id)

	// Drive a deterministic flush (no time.Sleep): Stop() joins the goroutine so
	// the final drain has completed before we assert.
	trigger <- time.Time{}
	f.Stop()

	counts := drainTouched(repo)
	assert.Equal(t, 1, counts[id], "one pending id flushes to exactly one touch")
}

// Coalescing: repeated touches of the same id between flushes collapse to one
// write.
func TestTouchFlusher_CoalescesRepeatedTouches(t *testing.T) {
	repo := newFakeTokenRepo()
	trigger := make(chan time.Time)
	f := newTouchFlusher(repo, trigger)

	id := uuid.New()
	for range 10 {
		f.touch(id)
	}
	f.Stop() // final drain joins synchronously

	counts := drainTouched(repo)
	assert.Equal(t, 1, counts[id], "ten touches of one id between flushes coalesce to one write")
}

// Drain-on-Stop: pending touches recorded but never trigger-flushed are still
// written by the synchronous drain in Stop().
func TestTouchFlusher_StopDrainsSynchronously(t *testing.T) {
	repo := newFakeTokenRepo()
	trigger := make(chan time.Time)
	f := newTouchFlusher(repo, trigger)

	a, b := uuid.New(), uuid.New()
	f.touch(a)
	f.touch(b)

	f.Stop() // must drain a and b before returning

	counts := drainTouched(repo)
	assert.Equal(t, 1, counts[a])
	assert.Equal(t, 1, counts[b])
}

// Idempotent + no-leak: Stop is safe to call twice and the goroutine exits.
func TestTouchFlusher_StopIsIdempotent(t *testing.T) {
	repo := newFakeTokenRepo()
	trigger := make(chan time.Time)
	f := newTouchFlusher(repo, trigger)

	f.Stop()
	require.NotPanics(t, f.Stop, "second Stop must be a no-op")
}
