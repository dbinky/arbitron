package modelcatalog

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingWorkerProbe struct {
	active  atomic.Int32
	maximum atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (p *blockingWorkerProbe) Check(ctx context.Context, path ExecutionPath, e modelidentity.Entry) (UsabilityEvidence, error) {
	active := p.active.Add(1)
	defer p.active.Add(-1)
	for old := p.maximum.Load(); active > old; old = p.maximum.Load() {
		if p.maximum.CompareAndSwap(old, active) {
			break
		}
	}
	p.started <- struct{}{}
	select {
	case <-p.release:
	case <-ctx.Done():
		return UsabilityEvidence{}, ctx.Err()
	}
	return (&nativeCheckingProbe{}).Check(ctx, path, e)
}

func TestDiscoveryUsesFourBoundedProbeWorkers(t *testing.T) {
	probe := &blockingWorkerProbe{started: make(chan struct{}, 16), release: make(chan struct{})}
	done := make(chan struct{})
	var out Selection
	go func() {
		out = (&AdvanceSelector{Probe: probe}).Select(context.Background(), selectionFixture("x", nil, entry("x", "1", ""), entry("y", "2", ""), entry("z", "3", "")))
		close(done)
	}()
	var release sync.Once
	unblock := func() { release.Do(func() { close(probe.release) }) }
	defer func() { unblock(); <-done }()
	for range 4 {
		select {
		case <-probe.started:
		case <-time.After(time.Second):
			t.Fatal("probe work was serialized instead of using four workers")
		}
	}
	require.EqualValues(t, 4, probe.active.Load())
	select {
	case <-probe.started:
		t.Fatal("more than four checks in flight")
	default:
	}
	assert.EqualValues(t, 4, probe.maximum.Load())
	unblock()
	<-done
	require.Len(t, out.Evidence, 6)
	require.Len(t, out.Advances, 1)
	assert.Equal(t, "z", out.Advances[0].Model)
	assert.LessOrEqual(t, probe.maximum.Load(), int32(4))
}
