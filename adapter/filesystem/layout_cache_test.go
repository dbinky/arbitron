package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func cacheFixture(t *testing.T) (*WorkflowRepo, *LayoutCache, layout.Destination, layout.CacheEntry) {
	t.Helper()
	r := NewWorkflowRepo(t.TempDir())
	wf := layouttest.SampleWorkflow()
	req := journalRequest(t, "demo")
	wf.ID = req.WorkflowID
	wf.Presentation = &workflow.Presentation{Revision: req.Revision, Name: req.Name, Scope: "shared", SavedAt: req.RequestedAt, Reason: req.Reason, Retained: req.Retained}
	require.NoError(t, r.Create(context.Background(), req.Scope, req.Name, wf))
	geo := layouttest.Arranged(t, wf)
	return r, NewLayoutCache(r), layout.Destination{Scope: req.Scope, Name: req.Name, WorkflowID: req.WorkflowID}, layout.CacheEntry{Format: layout.CacheFormatV1, Revision: req.Revision, Generation: 1, JobGeneration: 2, Contract: geo.Contract, Verdict: layout.VerdictNeedsAttention, Geometry: geo}
}
func TestLayoutCache_GroupJSONRoundTrip(t *testing.T) {
	for _, name := range []string{"branches", "workflows", "nested", "loop"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo, cache, d, e := cacheFixture(t)
			wf, geo, _ := layouttest.CapturedGroup(t, name)
			original, err := repo.Load(ctx, d.Scope, d.Name)
			require.NoError(t, err)
			wf.ID = original.ID
			wf.Presentation = original.Presentation
			require.NoError(t, repo.Update(ctx, d.Scope, d.Name, wf))
			e.Geometry = geo
			e.Contract = geo.Contract
			e.Verdict = layout.VerdictPassed
			before := layouttest.ManifestJSON(t, wf)
			require.NoError(t, cache.PublishCache(ctx, d, e, func(context.Context) error { return nil }))
			got, ok, err := cache.ReadCache(ctx, d.Scope, d.Name)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, e, got)
			require.Equal(t, layout.GeometryDigest(geo), layout.GeometryDigest(got.Geometry))
			bad := e
			bad.Geometry.GroupElements = nil
			require.Error(t, cache.PublishCache(ctx, d, bad, func(context.Context) error { t.Fatal("invalid group cache reached fence"); return nil }))
			after, ok, err := cache.ReadCache(ctx, d.Scope, d.Name)
			require.NoError(t, err)
			require.True(t, ok)
			require.Equal(t, got, after)
			loaded, err := repo.Load(ctx, d.Scope, d.Name)
			require.NoError(t, err)
			require.Equal(t, before, layouttest.ManifestJSON(t, loaded))
		})
	}
}

func TestLayoutCache_FencedSink(t *testing.T) {
	ctx := context.Background()
	r, c, d, e := cacheFixture(t)
	calls := 0
	fence := func(owned context.Context) error {
		calls++
		got, err := r.Load(owned, d.Scope, d.Name)
		require.NoError(t, err)
		require.Equal(t, e.Revision, got.SavedRevision())
		return nil
	}
	require.NoError(t, c.PublishCache(ctx, d, e, fence))
	require.Equal(t, 1, calls)
	got, ok, err := c.ReadCache(ctx, d.Scope, d.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, e, got)
	rejected := errors.New("stale store fence")
	require.ErrorIs(t, c.PublishCache(ctx, d, e, func(context.Context) error { return rejected }), rejected)
	after, ok, err := c.ReadCache(ctx, d.Scope, d.Name)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, got, after)
	require.Error(t, c.PublishCache(ctx, d, e, nil))
	stale := e
	stale.Revision = uuid.Must(uuid.NewV7()).String()
	require.ErrorIs(t, c.PublishCache(ctx, d, stale, fence), layout.ErrNotCurrent)
	require.NoError(t, r.Delete(ctx, d.Scope, d.Name))
	require.ErrorIs(t, c.PublishCache(ctx, d, e, fence), layout.ErrDestinationGone)
	dir, _ := r.resolveWorkflowDir(d.Scope, d.Name)
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err))
}
func TestLayoutCache_EnvelopeAndReadSettlement(t *testing.T) {
	ctx := context.Background()
	r, c, d, e := cacheFixture(t)
	variants := []layout.CacheEntry{e, e, e, e, e}
	variants[0].Format = "old"
	variants[1].JobGeneration = 0
	variants[2].Generation = 0
	variants[3].Contract.NodePaddingX++
	variants[4].Geometry.Nodes = variants[4].Geometry.Nodes[:1]
	for _, bad := range variants {
		require.Error(t, c.PublishCache(ctx, d, bad, func(context.Context) error { t.Fatal("invalid cache reached fence"); return nil }))
	}
	r.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error { return workflow.ErrMutationPending }))
	require.ErrorIs(t, c.PublishCache(ctx, d, e, func(context.Context) error { t.Fatal("unsettled cache reached fence"); return nil }), workflow.ErrMutationPending)
	r.SetReadSettler(nil)
	require.NoError(t, c.PublishCache(ctx, d, e, func(context.Context) error { return nil }))
	dir, _ := r.resolveWorkflowDir(d.Scope, d.Name)
	require.NoError(t, os.WriteFile(filepath.Join(dir, LayoutCacheFile), []byte("broken"), 0600))
	_, ok, err := c.ReadCache(ctx, d.Scope, d.Name)
	require.Error(t, err)
	require.False(t, ok)
}
