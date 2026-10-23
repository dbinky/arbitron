package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/instance"
)

type pinRepoStub struct {
	instance.InstanceRepository
	byID  map[uuid.UUID]*instance.Instance
	fault error
	gets  int
}

func (r *pinRepoStub) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	r.gets++
	if r.fault != nil {
		return nil, r.fault
	}
	return r.byID[id], nil
}

func TestPinLookupReadsThroughRepoAndCaches(t *testing.T) {
	id := uuid.New()
	repo := &pinRepoStub{byID: map[uuid.UUID]*instance.Instance{id: {ID: id, ModelPins: map[string]string{"a": "X"}, ModelPinsVersion: 1}}}
	l := NewPinLookup(repo)
	for i := 0; i < 2; i++ {
		set, err := l.Lookup(context.Background(), id)
		if err != nil || set.Version != 1 || set.Models["a"] != "X" {
			t.Fatalf("lookup %d: %+v %v", i, set, err)
		}
	}
	if repo.gets != 1 {
		t.Fatalf("repo gets = %d, want 1 (cached)", repo.gets)
	}
	l.Release(id)
	if _, err := l.Lookup(context.Background(), id); err != nil || repo.gets != 2 {
		t.Fatalf("release should force reload: gets=%d err=%v", repo.gets, err)
	}
}

func TestPinLookupFaultIsNotCachedAndNotLegacy(t *testing.T) {
	id := uuid.New()
	repo := &pinRepoStub{fault: errors.New("db down"), byID: map[uuid.UUID]*instance.Instance{id: {ID: id, ModelPins: map[string]string{"a": "X"}, ModelPinsVersion: 1}}}
	l := NewPinLookup(repo)
	if set, err := l.Lookup(context.Background(), id); err == nil {
		t.Fatalf("expected fault, got %+v", set)
	}
	repo.fault = nil
	set, err := l.Lookup(context.Background(), id)
	if err != nil || set.Models["a"] != "X" {
		t.Fatalf("after recovery: %+v %v", set, err)
	}
}

func TestPinLookupMissingInstanceErrors(t *testing.T) {
	l := NewPinLookup(&pinRepoStub{byID: map[uuid.UUID]*instance.Instance{}})
	if _, err := l.Lookup(context.Background(), uuid.New()); err == nil {
		t.Fatal("missing instance must error, not read as legacy")
	}
}

func TestPinLookupReturnsCopyAndEvicts(t *testing.T) {
	id := uuid.New()
	repo := &pinRepoStub{byID: map[uuid.UUID]*instance.Instance{id: {ID: id, ModelPins: map[string]string{"a": "X"}, ModelPinRoutes: map[string]string{"a": "azure"}, ModelPinsVersion: 1}}}
	l := NewPinLookup(repo)
	set, err := l.Lookup(context.Background(), id)
	if err != nil || set.OpaqueRoutes["a"] != "azure" {
		t.Fatalf("missing persisted exception: %+v %v", set, err)
	}
	set.Models["a"] = "mutated"
	set.OpaqueRoutes["a"] = "mutated"
	again, _ := l.Lookup(context.Background(), id)
	if again.Models["a"] != "X" || again.OpaqueRoutes["a"] != "azure" {
		t.Fatalf("cache mutated through returned maps: %+v", again)
	}
	for i := 0; i < pinCacheCapacity+5; i++ {
		other := uuid.New()
		repo.byID[other] = &instance.Instance{ID: other, ModelPinsVersion: 1, ModelPins: map[string]string{}}
		if _, err := l.Lookup(context.Background(), other); err != nil {
			t.Fatal(err)
		}
	}
	if len(l.entries) > pinCacheCapacity {
		t.Fatalf("cache grew to %d", len(l.entries))
	}
	before := repo.gets
	if set, err := l.Lookup(context.Background(), id); err != nil || set.Models["a"] != "X" || set.OpaqueRoutes["a"] != "azure" || repo.gets != before+1 {
		t.Fatalf("evicted entry should reload from repo: %+v %v", set, err)
	}
}

func TestPinLookupCachedReflectsLookupAndRelease(t *testing.T) {
	id := uuid.New()
	repo := &pinRepoStub{byID: map[uuid.UUID]*instance.Instance{id: {ID: id, ModelPins: map[string]string{"a": "X"}, ModelPinsVersion: 1}}}
	l := NewPinLookup(repo)
	if l.Cached(id) {
		t.Fatal("cached before any lookup")
	}
	if _, err := l.Lookup(context.Background(), id); err != nil || !l.Cached(id) {
		t.Fatalf("lookup should cache: %v", err)
	}
	l.Release(id)
	if l.Cached(id) {
		t.Fatal("still cached after Release")
	}
}
