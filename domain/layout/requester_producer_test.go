package layout_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This isolated producer test is not the P2T9 durable/root recovery proof.
type existingDestinationSettler struct {
	repo  workflow.Repository
	scope workflow.Scope
	name  string
	calls int
}

func (s *existingDestinationSettler) Settle(ctx context.Context, scope workflow.Scope, name string) error {
	if scope != s.scope || name != s.name {
		return workflow.ErrInvalidName
	}
	s.calls++
	_, err := s.repo.Load(ctx, scope, name)
	return err
}
func TestRequesterConsumesActualStampedSavedInput(t *testing.T) {
	ctx := context.Background()
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	scope := workflow.Shared()
	wf := layouttest.SampleWorkflow()
	wf.Nodes[0].Position = &workflow.NodePosition{X: 37, Y: 41}
	require.NoError(t, repo.Create(ctx, scope, "sample", wf))
	prior, err := repo.Load(ctx, scope, "sample")
	require.NoError(t, err)
	wf.Nodes[0].Label = "actual saved Unicode 字🙂"
	store := layouttest.NewMemStore()
	requester := layout.NewRequester(store, 3, nil)
	journal := layouttest.NewFakeJournal()
	settler := &existingDestinationSettler{repo: repo, scope: scope, name: "sample"}
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: settler})
	require.NoError(t, err)
	req, err := stamper.Stamp(ctx, scope, "sample", wf, prior, workflow.SaveReasonUpdate)
	require.NoError(t, err)
	require.NoError(t, repo.Update(ctx, scope, "sample", wf))
	stamper.Committed(ctx, req)
	row, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	saved, err := repo.Load(ctx, scope, "sample")
	require.NoError(t, err)
	assert.Equal(t, layouttest.ManifestJSON(t, saved), row.Manifest)
	assert.Equal(t, "actual saved Unicode 字🙂", requestWorkflow(t, req).Nodes[0].Label)
	assert.Equal(t, 3, row.AttemptLimit)
	assert.Equal(t, req.Retained, row.Retained)
	assert.Equal(t, map[string]workflow.NodePosition{"start": {X: 37, Y: 41}}, row.Retained.Positions)
	assert.Equal(t, 1, settler.calls)
	assert.False(t, journal.Has(req.Revision))
	// The immutable row still contains that input after a later source mutation.
	wf.Nodes[0].Label = "later"
	require.NoError(t, repo.Update(ctx, scope, "sample", wf))
	again, err := store.Get(ctx, req.Revision)
	require.NoError(t, err)
	assert.Equal(t, row, again)
}
