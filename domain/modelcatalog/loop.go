package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// RefreshInterval is the cadence of the scheduled catalog refresh.
const RefreshInterval = 4 * time.Hour

// ErrRefreshBusy lets overlapping callers retry without joining an active scan.
var ErrRefreshBusy = errors.New("modelcatalog: refresh already in progress")

// RefreshCoordinator is the single-flight gate in front of RefreshService: the
// startup pass, the four-hour tick and the admin tool share one in-flight
// cycle. A caller arriving mid-cycle gets ErrRefreshBusy. The cycle runs on
// the coordinator's own context, so its caller giving up neither cancels it
// nor leaves it running past Close.
type RefreshCoordinator struct {
	run    func(context.Context) (RefreshReport, error)
	base   context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	closed bool
	flight *refreshFlight
}

type refreshFlight struct {
	done   chan struct{}
	report RefreshReport
	err    error
}

func NewRefreshCoordinator(run func(context.Context) (RefreshReport, error)) *RefreshCoordinator {
	base, cancel := context.WithCancel(context.Background())
	return &RefreshCoordinator{run: run, base: base, cancel: cancel}
}

func (c *RefreshCoordinator) Refresh(ctx context.Context) (RefreshReport, error) {
	return c.admit(ctx, c.run)
}

// Do admits bootstrap work through the same slot as scheduled/manual refresh.
// Work must not recursively call Do or Refresh.
func (c *RefreshCoordinator) Do(ctx context.Context, run func(context.Context) error) error {
	if run == nil {
		return errNilCollaborator
	}
	_, err := c.admit(ctx, func(ctx context.Context) (RefreshReport, error) {
		return RefreshReport{}, run(ctx)
	})
	return err
}

func (c *RefreshCoordinator) admit(ctx context.Context, run func(context.Context) (RefreshReport, error)) (RefreshReport, error) {
	if err := ctx.Err(); err != nil {
		return RefreshReport{}, err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return RefreshReport{}, ErrRefreshClosed
	}
	if c.flight != nil {
		c.mu.Unlock()
		return RefreshReport{}, ErrRefreshBusy
	}
	f := &refreshFlight{done: make(chan struct{})}
	c.flight = f
	c.wg.Add(1)
	go c.exec(f, run)
	c.mu.Unlock()
	select {
	case <-f.done:
		return f.report, f.err
	case <-ctx.Done():
		return RefreshReport{}, ctx.Err()
	}
}

func (c *RefreshCoordinator) exec(f *refreshFlight, run func(context.Context) (RefreshReport, error)) {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			f.report, f.err = RefreshReport{}, fmt.Errorf("modelcatalog: refresh panicked: %v", r)
		}
		c.mu.Lock()
		c.flight = nil
		c.mu.Unlock()
		close(f.done)
	}()
	f.report, f.err = run(c.base)
}

// Close refuses new cycles, cancels the one in flight and waits for it.
func (c *RefreshCoordinator) Close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.cancel()
	c.wg.Wait()
}

// RefreshLoop runs one cycle per interval until its context ends. A failed
// cycle is logged and the loop continues. Startup runs only publication
// recovery synchronously (RefreshService.Recover through the coordinator); the
// loop owns discovery, probes and announcement delivery.
type RefreshLoop struct {
	Refresh  func(context.Context) (RefreshReport, error)
	Interval time.Duration
	// NewTicker is the clock seam; the default is a time.Ticker.
	NewTicker func(time.Duration) (<-chan time.Time, func())
	Logger    *slog.Logger
	// Immediate runs one cycle as soon as RunScheduled starts, before the first
	// tick. Serving entrypoints use it so discovery begins right after startup
	// without startup waiting on it.
	Immediate bool
}

// RunScheduled runs the scheduled refresh until ctx ends: an optional immediate
// first cycle, then one per interval. Startup performs only publication
// recovery synchronously; discovery always runs here, in the background.
func (l *RefreshLoop) RunScheduled(ctx context.Context) {
	interval := l.Interval
	if interval <= 0 {
		interval = RefreshInterval
	}
	newTicker := l.NewTicker
	if newTicker == nil {
		newTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	ticks, stop := newTicker(interval)
	defer stop()
	if l.Immediate && ctx.Err() == nil {
		l.refreshOnce(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			l.refreshOnce(ctx)
		}
	}
}

func (l *RefreshLoop) refreshOnce(ctx context.Context) {
	logger := l.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if _, err := l.Refresh(ctx); err != nil && ctx.Err() == nil {
		logger.Warn("model catalog refresh failed; keeping working models", "error", err)
	}
}
