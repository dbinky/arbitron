package sources

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// DiscoverFunc re-runs local discovery; production passes a closure over Discover.
type DiscoverFunc func(ctx context.Context) ([]Spec, map[string]string)

// NewRefreshing discovers now and again before every Probe, so installing or
// upgrading a harness, or a native Codex model-cache refresh, changes the
// advertised sources without restarting the runner.
func NewRefreshing(ctx context.Context, discover DiscoverFunc, now func() time.Time) *Inventory {
	specs, harness := discover(ctx)
	inv := New(specs, harness, now)
	inv.discover, inv.sig = discover, inventorySignature(specs, harness)
	return inv
}

// Revision increases only when rediscovery changed the source set or evidence.
func (i *Inventory) Revision() uint64 {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.rev
}

func (i *Inventory) refresh(ctx context.Context) {
	if i.discover == nil {
		return
	}
	specs, harness := i.discover(ctx)
	i.adopt(specs, harness, true)
}

func (i *Inventory) adopt(specs []Spec, harness map[string]string, bump bool) {
	sig := inventorySignature(specs, harness)
	i.mu.Lock()
	defer i.mu.Unlock()
	if sig == i.sig {
		return
	}
	health := make(map[string]runner.SourceHealth, len(i.last))
	for _, s := range i.last {
		health[string(s.Kind)+"|"+s.CredentialRef] = s.Health
	}
	last := make([]runneragent.Source, len(specs))
	for idx, sp := range specs {
		last[idx] = sp.Source
		if h, ok := health[string(sp.Source.Kind)+"|"+sp.Source.CredentialRef]; ok {
			last[idx].Health = h
		} else {
			last[idx].Health = runner.SourceHealth{Auth: "unknown"}
		}
	}
	h := make(map[string]string, len(harness))
	for k, v := range harness {
		h[k] = v
	}
	i.samples = make([]*sample, len(specs))
	i.specs, i.last, i.harness, i.sig = specs, last, h, sig
	if bump {
		i.rev++
	}
}
