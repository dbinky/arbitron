package layouttest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFakeJournal(t *testing.T) {
	ctx := context.Background()
	scope := workflow.Private(uuid.New())
	req := workflow.ArrangementRequest{Revision: "b", Scope: scope, Name: "old", Manifest: []byte("saved"), RequestedAt: time.Now()}
	j := layouttest.NewFakeJournal(req)
	req.Manifest[0] = 'X'
	require.True(t, j.Has("b"))
	assert.Equal(t, 1, j.Len())
	entries, err := j.Entries(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, workflow.JournalPrepared, entries[0].State)
	assert.Equal(t, "saved", string(entries[0].Request.Manifest))
	require.NoError(t, j.MarkCommitted(ctx, "b"))
	require.NoError(t, j.MarkCommitted(ctx, "b"))
	assert.Error(t, j.MarkCommitted(ctx, "unknown"))
	a := req
	a.Revision = "a"
	a.Manifest = []byte("earlier")
	a.RequestedAt = a.RequestedAt.Add(-time.Second)
	require.NoError(t, j.Record(ctx, a))
	entries, err = j.Entries(ctx)
	require.NoError(t, err)
	assert.Equal(t, "a", entries[0].Request.Revision)
	assert.Equal(t, workflow.JournalCommitted, entries[1].State)
	entries[1].Request.Manifest[0] = 'Y'
	list, _ := j.List(ctx)
	assert.Equal(t, "saved", string(list[1].Manifest))
	intent, err := j.BeginRelocation(ctx, scope, "old", workflow.Shared(), "new", uuid.New())
	require.NoError(t, err)
	assert.NotEmpty(t, intent.ID)
	intents, err := j.RelocationIntents(ctx)
	require.NoError(t, err)
	require.Len(t, intents, 1)
	assert.Equal(t, intent, intents[0])
	require.NoError(t, j.Relocate(ctx, scope, "old", workflow.Shared(), "new"))
	list, _ = j.List(ctx)
	assert.Equal(t, "new", list[0].Name)
	assert.Equal(t, workflow.Shared(), list[0].Scope)
	assert.Equal(t, "earlier", string(list[0].Manifest))
	require.NoError(t, j.EndRelocation(ctx, intent.ID))
	intents, _ = j.RelocationIntents(ctx)
	assert.Empty(t, intents)
	require.NoError(t, j.Quarantine(ctx, "a", "not committed"))
	assert.False(t, j.Has("a"))
	assert.Equal(t, 1, j.Len())
	require.NoError(t, j.Remove(ctx, "b"))
	require.NoError(t, j.Remove(ctx, "b"))
	assert.Zero(t, j.Len())
	boom := errors.New("disk")
	j.SetErr(boom)
	assert.ErrorIs(t, j.Record(ctx, req), boom)
	_, err = j.Entries(ctx)
	assert.ErrorIs(t, err, boom)
	assert.ErrorIs(t, j.MarkCommitted(ctx, "b"), boom)
	assert.ErrorIs(t, j.Remove(ctx, "b"), boom)
	assert.ErrorIs(t, j.Quarantine(ctx, "b", "bad"), boom)
	_, err = j.List(ctx)
	assert.ErrorIs(t, err, boom)
	_, err = j.BeginRelocation(ctx, scope, "old", workflow.Shared(), "new", uuid.Nil)
	assert.ErrorIs(t, err, boom)
	_, err = j.RelocationIntents(ctx)
	assert.ErrorIs(t, err, boom)
	assert.ErrorIs(t, j.Relocate(ctx, scope, "old", workflow.Shared(), "new"), boom)
	assert.ErrorIs(t, j.EndRelocation(ctx, "id"), boom)
	j.SetErr(nil)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	assert.ErrorIs(t, j.Record(cancelled, req), context.Canceled)
}
func TestFakeCommittedAndManifests(t *testing.T) {
	ctx := context.Background()
	s := layouttest.NewFakeCommittedRevisions()
	req := workflow.ArrangementRequest{Revision: "b", Manifest: []byte("exact"), RequestedAt: time.Now()}
	s.Prepare(req)
	req.Manifest[0] = 'X'
	got, err := s.CommittedRequests(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
	s.Commit("b")
	s.Commit("missing")
	a := req
	a.Revision = "a"
	a.Manifest = []byte("old")
	a.RequestedAt = a.RequestedAt.Add(-time.Second)
	s.Prepare(a)
	s.Commit("a")
	got, err = s.CommittedRequests(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].Revision)
	assert.Equal(t, "exact", string(got[1].Manifest))
	got[1].Manifest[0] = 'Z'
	again, _ := s.CommittedRequests(ctx)
	assert.Equal(t, "exact", string(again[1].Manifest))
	s.Abandon("a")
	got, _ = s.CommittedRequests(ctx)
	require.Len(t, got, 1)
	s.SetError(errors.New("offline"))
	_, err = s.CommittedRequests(ctx)
	assert.ErrorContains(t, err, "offline")
	m := layouttest.NewFakeManifests()
	m.Set(workflow.Shared(), "a", "r")
	rev, ok, err := m.CurrentRevision(ctx, workflow.Shared(), "a")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "r", rev)
	assert.Equal(t, 1, m.Calls())
	_, ok, _ = m.CurrentRevision(ctx, workflow.Private(uuid.Nil), "a")
	assert.False(t, ok)
	m.Set(workflow.Shared(), "empty", "")
	_, ok, _ = m.CurrentRevision(ctx, workflow.Shared(), "empty")
	assert.False(t, ok)
	m.Remove(workflow.Shared(), "a")
	_, ok, _ = m.CurrentRevision(ctx, workflow.Shared(), "a")
	assert.False(t, ok)
	m.SetErr(errors.New("offline"))
	_, _, err = m.CurrentRevision(ctx, workflow.Shared(), "a")
	assert.ErrorContains(t, err, "offline")
}
func TestFakeCache(t *testing.T) {
	ctx := context.Background()
	cache := layouttest.NewFakeCache()
	dest := layout.Destination{Scope: workflow.Shared(), Name: "a"}
	geo := layouttest.Arranged(t, layouttest.SampleWorkflow())
	entry := layout.CacheEntry{Format: layout.CacheFormatV1, Revision: "r", Generation: 2, JobGeneration: 3, Contract: geo.Contract, Geometry: geo}
	called := false
	require.ErrorIs(t, cache.PublishCache(ctx, dest, entry, func(context.Context) error { called = true; return layout.ErrStaleGeneration }), layout.ErrStaleGeneration)
	assert.True(t, called)
	assert.Empty(t, cache.Published(dest))
	require.NoError(t, cache.PublishCache(ctx, dest, entry, func(context.Context) error { return nil }))
	assert.Equal(t, 2, cache.PublishCalls())
	entry.Geometry.Nodes[0].Text = "mutated"
	got, ok, err := cache.ReadCache(ctx, dest.Scope, dest.Name)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, geo.Nodes[0].ID, got.Geometry.Nodes[0].ID)
	assert.NotEqual(t, "mutated", got.Geometry.Nodes[0].Text)
	history := cache.Published(dest)
	require.Len(t, history, 1)
	history[0].Geometry.Nodes[0].Text = "other"
	assert.NotEqual(t, "other", cache.Published(dest)[0].Geometry.Nodes[0].Text)
	_, ok, _ = cache.ReadCache(ctx, workflow.Private(uuid.Nil), dest.Name)
	assert.False(t, ok)
	cache.SetPublishErr(errors.New("write"))
	assert.ErrorContains(t, cache.PublishCache(ctx, dest, entry, func(context.Context) error { return nil }), "write")
	require.Len(t, cache.Published(dest), 1)
	cache.SetReadErr(errors.New("read"))
	_, _, err = cache.ReadCache(ctx, dest.Scope, dest.Name)
	assert.ErrorContains(t, err, "read")
	cache.SetReadErr(nil)
	cache.Seed(dest.Scope, "b", entry)
	_, ok, _ = cache.ReadCache(ctx, dest.Scope, "b")
	assert.True(t, ok)
}
func TestFakePortsConcurrent(t *testing.T) {
	ctx := context.Background()
	j := layouttest.NewFakeJournal()
	s := layouttest.NewFakeCommittedRevisions()
	m := layouttest.NewFakeManifests()
	cache := layouttest.NewFakeCache()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			r := workflow.ArrangementRequest{Revision: uuid.NewString(), Manifest: []byte("snapshot")}
			_ = j.Record(ctx, r)
			_ = j.MarkCommitted(ctx, r.Revision)
			_, _ = j.Entries(ctx)
			s.Prepare(r)
			s.Commit(r.Revision)
			_, _ = s.CommittedRequests(ctx)
			m.Set(workflow.Shared(), r.Revision, r.Revision)
			_, _, _ = m.CurrentRevision(ctx, workflow.Shared(), r.Revision)
			_ = m.Calls()
			cache.Seed(workflow.Shared(), r.Revision, layout.CacheEntry{})
			_, _, _ = cache.ReadCache(ctx, workflow.Shared(), r.Revision)
			_ = cache.PublishCalls()
		})
	}
	wg.Wait()
	assert.Equal(t, 12, j.Len())
}

func TestFakeJournalContract(t *testing.T) {
	layouttest.RunJournalContract(t, func(*testing.T) workflow.RevisionJournal { return layouttest.NewFakeJournal() })
}
