package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

// A real status-store deadline must not turn a stamped save into a legacy or
// successful layout, discard its captured positions, or repair it during a read.
func TestLayoutQueryE2E_StatusDeadlinePreservesFallback(t *testing.T) {
	ds := bootQueryStack(t)
	ds.layout.stop()
	ctx := adminCtxForLocalUser(t, ds)
	const name = "reader-deadline"
	require.NoError(t, ds.workflowSvc.Create(ctx, name, queryE2EWorkflow(name, "Saved before timeout")))
	rev := manifestRevisionOnDisk(t, ds, name)
	before, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	manifestPath := filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), name, "workflow.json")
	manifest, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	// Holding the only connection forces the real SQL status query to wait for
	// its context. No timing assumption about query or browser speed is needed.
	ds.db.SetMaxOpenConns(1)
	conn, err := ds.db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	deadline, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	view, err := ds.layout.query.WorkflowLayout(deadline, name)
	require.NoError(t, err, "an authorised manifest read retains its fallback when status expires")
	require.ErrorIs(t, deadline.Err(), context.DeadlineExceeded)
	require.Equal(t, rev, view.SavedRevision)
	require.Equal(t, layout.DisplayUnavailable, view.DisplayStatus)
	require.False(t, view.Available)
	require.True(t, view.Unreviewed)
	require.Nil(t, view.Evidence)
	require.Nil(t, view.Geometry)
	require.Equal(t, "stored status unavailable", view.AvailabilityError, "storage detail is logged, never served")
	require.Equal(t, layout.ProvenanceRetainedPositions, view.Provenance)
	require.NotNil(t, view.Retained)
	require.Equal(t, map[string]workflow.NodePosition{
		"t1": {X: 10, Y: 20}, "t2": {X: 10, Y: 200},
	}, view.Retained.Positions)

	require.NoError(t, conn.Close())
	after, err := ds.layout.store.Get(ctx, rev)
	require.NoError(t, err)
	require.Equal(t, before, after, "the expired read must not change status or generations")
	raw, err := os.ReadFile(manifestPath)
	require.NoError(t, err)
	require.Equal(t, manifest, raw)
	recovered, err := ds.layout.query.WorkflowLayout(ctx, name)
	require.NoError(t, err)
	require.True(t, recovered.Available)
	require.Equal(t, layout.DisplayPending, recovered.DisplayStatus)
	require.Equal(t, view.Retained, recovered.Retained)
}
