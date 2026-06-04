package http

import (
	"context"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// touchFlushInterval is the default cadence at which pending bearer-token
// last-used touches are flushed to the repository in production.
const touchFlushInterval = 30 * time.Second

// touchFlusher coalesces best-effort "token last used" updates. The auth
// middleware records the most recent use of a bearer token on every request;
// rather than spawning an unbounded, unowned goroutine per request (the prior
// fire-and-forget pattern), touches are collapsed into a mutex-guarded set that
// a single owned goroutine flushes on each trigger tick. Repeated touches of the
// same token between flushes collapse to one write.
//
// The flush trigger is injected (a <-chan time.Time) so tests can drive flushes
// deterministically without time.Sleep; production passes a time.Ticker channel.
// Stop() cancels the goroutine and drains the final pending set synchronously,
// giving the server lifecycle a clean shutdown with no goroutine leak.
type touchFlusher struct {
	tokens  identity.TokenRepository
	trigger <-chan time.Time

	mu      sync.Mutex
	pending map[uuid.UUID]struct{}

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// newTouchFlusher builds and starts a flusher driven by the given trigger
// channel. The caller owns the trigger's lifetime; Stop() ends the goroutine.
func newTouchFlusher(tokens identity.TokenRepository, trigger <-chan time.Time) *touchFlusher {
	f := &touchFlusher{
		tokens:  tokens,
		trigger: trigger,
		pending: make(map[uuid.UUID]struct{}),
		stop:    make(chan struct{}),
	}
	f.wg.Add(1)
	go f.run()
	return f
}

// touch records that the given token id was used. It is non-blocking and never
// fails the request: it only stamps an in-memory set.
func (f *touchFlusher) touch(id uuid.UUID) {
	f.mu.Lock()
	f.pending[id] = struct{}{}
	f.mu.Unlock()
}

// run is the single owned goroutine. It flushes on every trigger tick and, on
// shutdown, performs one final drain before exiting.
func (f *touchFlusher) run() {
	defer f.wg.Done()
	for {
		select {
		case <-f.trigger:
			f.flush()
		case <-f.stop:
			f.flush() // final synchronous drain
			return
		}
	}
}

// flush writes a touch for every pending id, then clears the set. Each write is
// best-effort: a failed update is dropped (last-used is non-critical metadata).
func (f *touchFlusher) flush() {
	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		return
	}
	batch := f.pending
	f.pending = make(map[uuid.UUID]struct{})
	f.mu.Unlock()

	for id := range batch {
		_ = f.tokens.TouchLastUsed(context.Background(), id)
	}
}

// Stop ends the flusher goroutine and drains the final pending set
// synchronously. Idempotent.
func (f *touchFlusher) Stop() {
	f.stopOnce.Do(func() { close(f.stop) })
	f.wg.Wait()
}
