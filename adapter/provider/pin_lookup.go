package provider

import (
	"container/list"
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// pinCacheCapacity bounds the in-memory pin cache. Eviction is safe: pins are
// immutable in the repository and reload on the next read.
const pinCacheCapacity = 1024

// PinLookup is the repository-backed domain PinLookup. It reads through
// InstanceRepository.Get (never a cache-only source), caches successful reads
// only, and hands out copies so callers cannot mutate a cached record.
type PinLookup struct {
	repo instance.InstanceRepository

	mu      sync.Mutex
	order   *list.List // front = most recently used; values are *pinEntry
	entries map[uuid.UUID]*list.Element
	loads   map[uuid.UUID]*pinLoad
}

// pinLoad tracks only active reads, so Release can invalidate their cache
// writes without retaining a tombstone for every completed run.
type pinLoad struct {
	readers  int
	released bool
}

type pinEntry struct {
	id  uuid.UUID
	set domprovider.PinSet
}

var _ domprovider.PinLookup = (*PinLookup)(nil)

// NewPinLookup builds a lookup over repo.
func NewPinLookup(repo instance.InstanceRepository) *PinLookup {
	if repo == nil {
		panic("provider: NewPinLookup requires an instance repository")
	}
	return &PinLookup{
		repo:    repo,
		order:   list.New(),
		entries: map[uuid.UUID]*list.Element{},
		loads:   map[uuid.UUID]*pinLoad{},
	}
}

// Lookup returns the run's pins. A repository fault is returned, not cached,
// and never degrades to an empty (legacy) record.
func (l *PinLookup) Lookup(ctx context.Context, id uuid.UUID) (domprovider.PinSet, error) {
	l.mu.Lock()
	if el, ok := l.entries[id]; ok {
		l.order.MoveToFront(el)
		set := clonePinSet(el.Value.(*pinEntry).set)
		l.mu.Unlock()
		return set, nil
	}
	load := l.loads[id]
	if load == nil {
		load = &pinLoad{}
		l.loads[id] = load
	}
	load.readers++
	l.mu.Unlock()

	inst, err := l.repo.Get(ctx, id)
	l.mu.Lock()
	defer l.mu.Unlock()
	load.readers--
	if load.readers == 0 && l.loads[id] == load {
		delete(l.loads, id)
	}
	if err != nil {
		return domprovider.PinSet{}, fmt.Errorf("pin lookup: get instance %s: %w", id, err)
	}
	if inst == nil {
		return domprovider.PinSet{}, fmt.Errorf("pin lookup: instance %s not found", id)
	}
	set := domprovider.PinSet{Version: inst.ModelPinsVersion, Models: inst.ModelPins, OpaqueRoutes: inst.ModelPinRoutes, OpaqueConnections: inst.ModelPinRouteConnections}

	if load.released {
		return clonePinSet(set), nil
	}
	if el, ok := l.entries[id]; ok {
		l.order.MoveToFront(el)
	} else {
		l.entries[id] = l.order.PushFront(&pinEntry{id: id, set: clonePinSet(set)})
		for l.order.Len() > pinCacheCapacity {
			oldest := l.order.Back()
			l.order.Remove(oldest)
			delete(l.entries, oldest.Value.(*pinEntry).id)
		}
	}
	return clonePinSet(set), nil
}

// Cached reports whether the run's pins are currently held in memory.
func (l *PinLookup) Cached(id uuid.UUID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.entries[id]
	return ok
}

// Release drops the run's cached pins and prevents earlier reads from caching
// them again. A later Lookup may reload and cache the immutable record.
func (l *PinLookup) Release(id uuid.UUID) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if load := l.loads[id]; load != nil {
		load.released = true
		delete(l.loads, id)
	}
	if el, ok := l.entries[id]; ok {
		l.order.Remove(el)
		delete(l.entries, id)
	}
}

func clonePinSet(s domprovider.PinSet) domprovider.PinSet {
	return domprovider.PinSet{Version: s.Version, Models: maps.Clone(s.Models), OpaqueRoutes: maps.Clone(s.OpaqueRoutes), OpaqueConnections: maps.Clone(s.OpaqueConnections)}
}
