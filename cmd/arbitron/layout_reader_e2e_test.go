package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Exercise Reader through both real query consumers, using saved authoring input,
// a persisted engine snapshot and actual browser-reviewed SQLite results.
func TestLayoutReader_RealBrowserWorkflowAndInstanceParity(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "2"})
	useLayoutE2EBrowser(t, browser)
	t.Setenv("ARBITRON_LAYOUT_ARTIFACTDIR", filepath.Join(e.root, "layout-artifacts"))
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	const name = "reader-parity"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Saved reader input")))
	first := layoutE2ESavedRevision(t, e.privatePath(name))
	row := layoutRenderWaitTerminal(t, ds, first, 2*time.Minute)
	require.Equal(t, layout.StatePassed, row.State, "findings: %+v; failure: %s", row.Findings, row.FailureReason)
	cachePath := filepath.Join(filepath.Dir(e.privatePath(name)), filesystem.LayoutCacheFile)
	require.Eventually(t, func() bool {
		entry, ok, err := ds.layout.cache.ReadCache(ctx, row.Destination.Scope, name)
		return err == nil && ok && entry.Revision == first && entry.Generation == row.ResultGeneration
	}, 10*time.Second, 20*time.Millisecond)
	// Freeze job/cache writers so read-side mutations cannot hide behind worker progress.
	ds.layout.stop()
	result, err := ds.layout.store.Result(ctx, first, row.ResultGeneration)
	require.NoError(t, err)
	require.True(t, layout.HasRenderedEvidence(result.Geometry, result.Review))
	assertEvidenceOnDisk(t, e, result.Review.Evidence)
	id, _, err := ds.eng.Trigger(ctx, name, map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, first, snapshotRevision(t, ds, id))
	view, err := ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.Equal(t, layout.DisplayComplete, view.DisplayStatus)
	require.Equal(t, first, view.SavedRevision)
	require.False(t, view.Unreviewed)
	require.NotNil(t, view.Geometry)
	require.NotNil(t, view.Evidence)
	require.Len(t, view.Geometry.Nodes, 2)
	require.Len(t, view.Geometry.Edges, 1)
	require.Equal(t, layout.GeometryDigest(result.Geometry), layout.GeometryDigest(*view.Geometry))
	snapshot, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	require.Equal(t, view, snapshot, "both consumers must expose the entire same revision/status/geometry/evidence")

	// A graph-compatible authoring save retains the immutable result reference,
	// but that result is not reviewed evidence for the NEW saved revision.
	wf := layoutE2EManifest(t, e.privatePath(name))
	wf.Description = "New description, unchanged graph"
	require.NoError(t, ds.workflowSvc.Update(ctx, name, wf))
	second := layoutE2ESavedRevision(t, e.privatePath(name))
	require.NotEqual(t, first, second)
	newID, _, err := ds.eng.Trigger(ctx, name, map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, second, snapshotRevision(t, ds, newID))
	require.NoError(t, os.Remove(cachePath))
	pending, err := ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.Equal(t, second, pending.SavedRevision)
	require.Equal(t, layout.DisplayPending, pending.DisplayStatus)
	require.Equal(t, layout.ProvenanceRetainedGeometry, pending.Provenance)
	require.True(t, pending.Unreviewed)
	require.Nil(t, pending.Evidence)
	require.NotNil(t, pending.Retained)
	require.Equal(t, workflow.RetainedFromLayout, pending.Retained.Source)
	require.Equal(t, first, pending.Retained.FromRevision)
	require.Equal(t, row.ResultGeneration, pending.Retained.FromGeneration)
	require.Equal(t, view.Geometry, pending.Geometry)
	newSnapshot, err := ds.layout.query.InstanceLayout(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, pending, newSnapshot)
	manifest, err := os.ReadFile(e.privatePath(name))
	require.NoError(t, err)
	before, err := ds.layout.store.Get(ctx, second)
	require.NoError(t, err)
	again, err := ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.Equal(t, pending, again)
	after, err := ds.layout.store.Get(ctx, second)
	require.NoError(t, err)
	require.Equal(t, before, after, "Reader must not advance, retry or repair the pending row")
	raw, err := os.ReadFile(e.privatePath(name))
	require.NoError(t, err)
	require.Equal(t, manifest, raw)
	require.NoFileExists(t, cachePath, "Reader must not rebuild a deleted cache")

	// A graph-changing save while the worker is stopped: the first revision's
	// geometry no longer fits, so both consumers draw surviving positions only,
	// never the stale t1→t2 route, and leave the new node unplaced.
	changed := layoutE2EManifest(t, e.privatePath(name))
	changed.Nodes = append(changed.Nodes, workflow.Node{ID: "t3", Type: workflow.NodeTypeTransform, Label: "Inserted",
		Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "inserted", Operation: "set", Value: true}}}})
	changed.Edges = []workflow.Edge{{Source: "t1", Target: "t3"}, {Source: "t3", Target: "t2"}}
	require.NoError(t, ds.workflowSvc.Update(ctx, name, changed))
	third := layoutE2ESavedRevision(t, e.privatePath(name))
	require.NotEqual(t, second, third)
	thirdID, _, err := ds.eng.Trigger(ctx, name, map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, third, snapshotRevision(t, ds, thirdID))
	reshaped, err := ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.Equal(t, third, reshaped.SavedRevision)
	require.Equal(t, layout.DisplayPending, reshaped.DisplayStatus)
	require.Equal(t, layout.ProvenanceRetainedPositions, reshaped.Provenance)
	require.True(t, reshaped.Unreviewed)
	require.Nil(t, reshaped.Geometry, "retained geometry for the old graph must not be drawn for the new graph")
	require.Nil(t, reshaped.Contract)
	require.Nil(t, reshaped.Evidence)
	require.NotNil(t, reshaped.Retained)
	require.Equal(t, workflow.RetainedFromLayout, reshaped.Retained.Source)
	require.Equal(t, first, reshaped.Retained.FromRevision)
	survivors := map[string]workflow.NodePosition{}
	for _, n := range view.Geometry.Nodes {
		survivors[n.ID] = workflow.NodePosition{X: n.Box.X, Y: n.Box.Y}
	}
	require.Equal(t, survivors, reshaped.Retained.Positions, "surviving nodes keep their reviewed boxes; the new node has none")
	reshapedSnapshot, err := ds.layout.query.InstanceLayout(ctx, thirdID)
	require.NoError(t, err)
	require.Equal(t, reshaped, reshapedSnapshot, "the instance must show its pending revision's fallback, not legacy or the workflow's latest")
	older, err := ds.layout.query.InstanceLayout(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, pending, older, "a later save must not change an earlier pending instance's view")

	// Neither historical consumer can depend on a live definition or cache.
	require.Eventually(t, func() bool {
		inst, err := ds.instanceRepo.Get(ctx, thirdID)
		return err == nil && inst.State.IsTerminal()
	}, 10*time.Second, 20*time.Millisecond, "delete requires the triggered run to finish")
	require.NoError(t, ds.workflowSvc.Delete(ctx, name))
	for _, tc := range []struct {
		id   uuid.UUID
		want layout.View
	}{
		{id, view},
		{newID, pending},
		{thirdID, reshaped},
	} {
		got, err := ds.layout.query.InstanceLayout(ctx, tc.id)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	stored, err := ds.layout.store.Result(ctx, first, row.ResultGeneration)
	require.NoError(t, err)
	require.Equal(t, result, stored)
	require.Equal(t, 3, layoutE2ECountRows(t, ds.db, name))
	require.NoDirExists(t, filepath.Dir(e.privatePath(name)))
}
