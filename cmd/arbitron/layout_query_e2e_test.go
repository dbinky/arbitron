package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
)

// bootQueryStack boots the REAL composition root (initDomain) against an empty
// workspace with a one-attempt layout budget, so arrangement jobs reach a
// terminal state quickly with or without a browser installed.
func bootQueryStack(t *testing.T) *domainStack {
	t.Helper()
	root := t.TempDir()
	workspaceDir := filepath.Join(root, "workspaces")
	require.NoError(t, os.MkdirAll(workspaceDir, 0o700))

	configPath := filepath.Join(root, "config.yaml")
	cfg := "" +
		"server:\n  host: \"127.0.0.1\"\n  port: 0\n" +
		"auth:\n  mode: \"local\"\n" +
		"database:\n  path: " + yamlQuote(filepath.Join(root, "arbitron.db")) + "\n" +
		"workspace:\n  path: " + yamlQuote(workspaceDir) + "\n" +
		"samples:\n  seed: false\n  sourcedir: \"\"\n" +
		"logging:\n  level: \"error\"\n  file: " + yamlQuote(filepath.Join(root, "arbitron.log")) + "\n" +
		"queue:\n  globalcap: 5\n  perusercap: 5\n" +
		"layout:\n  maxattempts: 1\n  artifactdir: " + yamlQuote(filepath.Join(root, "layout-artifacts")) + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(cfg), 0o600))

	ds, err := initDomainWithModelDependencies(context.Background(), configPath, offlineModelDependencies())
	require.NoError(t, err, "the composition root must boot")
	t.Cleanup(func() { ds.Close(context.Background()) })
	require.NoError(t, ds.Start(context.Background()))
	return ds
}

// queryE2EWorkflow is a two-node transform workflow that runs with no LLM.
func queryE2EWorkflow(name, label string) *workflow.Workflow {
	return &workflow.Workflow{
		Name:        name,
		Description: "layout query e2e",
		Author:      "e2e",
		Inputs:      []workflow.InputField{},
		Nodes: []workflow.Node{
			{ID: "t1", Type: workflow.NodeTypeTransform, Entrypoint: true, Label: label, Position: &workflow.NodePosition{X: 10, Y: 20},
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "done", Operation: "set", Value: true}}}},
			{ID: "t2", Type: workflow.NodeTypeTransform, Label: "Finish", Position: &workflow.NodePosition{X: 10, Y: 200},
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "finished", Operation: "set", Value: true}}}},
		},
		Edges: []workflow.Edge{{Source: "t1", Target: "t2"}},
	}
}

// manifestRevisionOnDisk reads the committed workflow.json straight from the
// workspace (the persisted SOURCE of the revision) and returns its revision.
func manifestRevisionOnDisk(t *testing.T, ds *domainStack, name string) string {
	t.Helper()
	path := filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), name, "workflow.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var wf workflow.Workflow
	require.NoError(t, json.Unmarshal(data, &wf))
	require.NotNil(t, wf.Presentation, "every authoring save must stamp presentation metadata")
	require.NotEmpty(t, wf.Presentation.Revision)
	return wf.Presentation.Revision
}

// snapshotRevision reads the revision captured in the persisted instance row's
// WorkflowSnapshot (the snapshot SOURCE, read straight from SQLite).
func snapshotRevision(t *testing.T, ds *domainStack, id uuid.UUID) string {
	t.Helper()
	inst, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	pres, ok := inst.WorkflowSnapshot["presentation"].(map[string]any)
	require.True(t, ok, "the instance snapshot must carry presentation metadata")
	rev, _ := pres["revision"].(string)
	require.NotEmpty(t, rev)
	return rev
}

// waitTerminalWorkflowView polls the production QueryService until the
// workflow's view is for revision and no longer active.
func waitTerminalWorkflowView(t *testing.T, ds *domainStack, ctx context.Context, name, revision string) layout.View {
	t.Helper()
	var v layout.View
	require.Eventually(t, func() bool {
		got, err := ds.layout.query.WorkflowLayout(ctx, name)
		if err != nil || got.SavedRevision != revision {
			return false
		}
		v = got
		return !layout.IsActive(got.DisplayStatus)
	}, 120*time.Second, 200*time.Millisecond, "revision %s must reach a terminal arrangement state", revision)
	return v
}

func queryE2EJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// PROVENANCE PROBE: fails if initDomain never calls attachQuery (query nil) or
// if the view is not keyed by the revision the save path wrote to disk.
func TestLayoutQueryE2E_WorkflowViewKeyedByOnDiskRevision(t *testing.T) {
	ds := bootQueryStack(t)
	require.NotNil(t, ds.layout, "initDomain must build the layout stack")
	require.NotNil(t, ds.layout.query, "initDomain must attach the layout QueryService")
	assert.Same(t, ds.layout.query, ds.layoutQuery())

	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-wf", queryE2EWorkflow("query-wf", "Start")))
	rev := manifestRevisionOnDisk(t, ds, "query-wf")

	v, err := ds.layout.query.WorkflowLayout(ctx, "query-wf")
	require.NoError(t, err)
	assert.Equal(t, rev, v.SavedRevision)
	assert.NotEqual(t, layout.DisplayLegacy, v.DisplayStatus, "a stamped save is never legacy")
	row, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	var saved workflow.Workflow
	require.NoError(t, json.Unmarshal(row.Manifest, &saved))
	require.Equal(t, "Start", saved.Nodes[0].Label)
	require.Equal(t, rev, saved.SavedRevision())
	assert.Equal(t, row.JobGeneration, v.JobGeneration)
}

func TestLayoutQueryE2E_InstanceViewKeyedBySnapshotRevision(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-inst", queryE2EWorkflow("query-inst", "Start")))

	id, _, err := ds.eng.Trigger(ctx, "query-inst", map[string]any{}, "normal")
	require.NoError(t, err)
	snapRev := snapshotRevision(t, ds, id)
	assert.Equal(t, manifestRevisionOnDisk(t, ds, "query-inst"), snapRev)

	v, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, snapRev, v.SavedRevision)

	_, err = ds.layout.query.InstanceLayout(ctx, uuid.New())
	assert.Error(t, err, "an unknown instance id must not resolve")
}

// After an Update the workflow view moves to R2 while the historical instance
// stays on R1 with exactly the R1 geometry/fallback the workflow view showed.
func TestLayoutQueryE2E_InstanceStaysOnHistoricalRevisionAfterUpdate(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-hist", queryE2EWorkflow("query-hist", "Start")))
	r1 := manifestRevisionOnDisk(t, ds, "query-hist")

	id, _, err := ds.eng.Trigger(ctx, "query-hist", map[string]any{}, "normal")
	require.NoError(t, err)
	require.Equal(t, r1, snapshotRevision(t, ds, id))

	w1 := waitTerminalWorkflowView(t, ds, ctx, "query-hist", r1)
	if w1.Geometry == nil {
		require.NotNil(t, w1.Retained)
		require.Len(t, w1.Retained.Positions, 2, "fallback parity must compare actual coordinates, not two empty values")
	} else {
		require.Len(t, w1.Geometry.Nodes, 2)
	}
	i1, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, r1, i1.SavedRevision)
	assert.JSONEq(t, queryE2EJSON(t, w1.Geometry), queryE2EJSON(t, i1.Geometry), "same revision ⇒ same geometry in both views")
	assert.JSONEq(t, queryE2EJSON(t, w1.Retained), queryE2EJSON(t, i1.Retained), "same revision ⇒ same retained fallback")

	cur, err := ds.workflowSvc.Load(ctx, "query-hist")
	require.NoError(t, err)
	cur.Nodes[0].Label = "Start (renamed)"
	require.NoError(t, ds.workflowSvc.Update(ctx, "query-hist", cur))
	r2 := manifestRevisionOnDisk(t, ds, "query-hist")
	require.NotEqual(t, r1, r2)

	w2, err := ds.layout.query.WorkflowLayout(ctx, "query-hist")
	require.NoError(t, err)
	assert.Equal(t, r2, w2.SavedRevision)

	i2, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, r1, i2.SavedRevision, "a historical instance never adopts the newer revision")
	assert.JSONEq(t, queryE2EJSON(t, w1.Geometry), queryE2EJSON(t, i2.Geometry))
	assert.JSONEq(t, queryE2EJSON(t, w1.Retained), queryE2EJSON(t, i2.Retained))
}

// Retry through the composition root: a stale expected revision conflicts; the
// current revision is retried (or refused as already passed) by the real store.
func TestLayoutQueryE2E_RetryWorkflowThroughCompositionRoot(t *testing.T) {
	ds := bootQueryStack(t)
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.workflowSvc.Create(ctx, "query-retry", queryE2EWorkflow("query-retry", "Start")))
	rev := manifestRevisionOnDisk(t, ds, "query-retry")

	_, err := ds.layout.query.RetryWorkflow(ctx, "query-retry", "not-the-revision")
	assert.ErrorIs(t, err, layout.ErrRevisionConflict)

	v := waitTerminalWorkflowView(t, ds, ctx, "query-retry", rev)
	ack, err := ds.layout.query.RetryWorkflow(ctx, "query-retry", rev)
	if v.State == layout.StatePassed {
		assert.ErrorIs(t, err, layout.ErrRetryConflict, "a passed revision is not retryable")
		return
	}
	require.NoError(t, err)
	assert.Equal(t, rev, ack.SavedRevision)
	assert.Equal(t, v.JobGeneration+1, ack.JobGeneration)
}

func TestLayoutQueryE2E_HistoricalMissingRowAfterOwnershipMove(t *testing.T) {
	browser := requireBrowserE2E(t)
	e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
	useLayoutE2EBrowser(t, browser)
	ds, _ := e.boot(t)
	ctx := adminCtxForLocalUser(t, ds)
	name := "query-moved"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Start")))
	rev := manifestRevisionOnDisk(t, ds, name)
	workflowID := layoutE2EManifest(t, e.privatePath(name)).ID
	id, _, err := ds.eng.Trigger(ctx, name, map[string]any{}, "normal")
	require.NoError(t, err)
	original := waitTerminalWorkflowView(t, ds, ctx, name, rev)
	require.Equal(t, layout.StatePassed, original.State)
	require.NotNil(t, original.Geometry)
	owner := uuid.New()
	require.NoError(t, ds.workflowSvc.TransferOwnership(ctx, name, owner))
	movedCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleAdmin})
	// Exercise the documented journal-less recovery window with a real snapshot.
	// Results are removed first to respect the store's foreign key.
	_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision = ?", rev)
	require.NoError(t, err)
	_, err = ds.db.Exec("DELETE FROM layout_revisions WHERE revision = ?", rev)
	require.NoError(t, err)
	_, err = ds.layout.query.WorkflowLayout(ctx, name)
	require.ErrorIs(t, err, workflow.ErrNotFound, "the historical caller cannot see the moved private definition")
	ack, err := ds.layout.query.RetryInstance(ctx, id, rev)
	require.NoError(t, err)
	require.Equal(t, rev, ack.SavedRevision)
	// With no row and no committed journal envelope there is no relocation
	// evidence: the row is rebuilt from the snapshot's own locator, and the
	// cache CAS below refuses to recreate that folder.
	rebuilt, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	require.Equal(t, layout.Destination{Scope: workflow.Private(localDefaultUser), Name: name, WorkflowID: workflowID}, rebuilt.Destination)
	moved := waitTerminalWorkflowView(t, ds, movedCtx, name, rev)
	require.Equal(t, layout.StatePassed, moved.State)
	require.NotNil(t, moved.Geometry)
	snapshot, err := ds.layout.query.InstanceLayout(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, snapshot.Geometry)
	assert.Equal(t, layout.GeometryDigest(*moved.Geometry), layout.GeometryDigest(*snapshot.Geometry))
	assert.Equal(t, rev, snapshot.SavedRevision)
	require.NoDirExists(t, filepath.Dir(e.privatePath(name)), "old-locator cache CAS must not recreate the original folder")
	movedPath := filepath.Join(e.workspaceDir, "users", owner.String(), name, "workflow.json")
	require.FileExists(t, movedPath)

	require.NoError(t, ds.workflowSvc.Delete(movedCtx, name))
	_, err = ds.db.Exec("DELETE FROM layout_results WHERE revision = ?", rev)
	require.NoError(t, err)
	_, err = ds.db.Exec("DELETE FROM layout_revisions WHERE revision = ?", rev)
	require.NoError(t, err)
	_, err = ds.layout.query.RetryInstance(ctx, id, rev)
	require.NoError(t, err, "authorized historical retry survives deletion as well as relocation")
	require.Eventually(t, func() bool {
		got, err := ds.layout.query.InstanceLayout(ctx, id)
		return err == nil && got.State == layout.StatePassed && got.Geometry != nil
	}, 120*time.Second, 200*time.Millisecond)
	require.NoDirExists(t, filepath.Dir(e.privatePath(name)))
	require.NoDirExists(t, filepath.Dir(movedPath))
}

func TestLayoutQueryE2E_LayoutQueryNilStack(t *testing.T) {
	var ds *domainStack
	require.Nil(t, ds.layoutQuery())
	require.Nil(t, (&domainStack{}).layoutQuery())
}
