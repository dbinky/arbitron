package layout_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type loadOnlyRepo struct {
	workflow.Repository
	load func(context.Context, workflow.Scope, string) (*workflow.Workflow, error)
}

func (r loadOnlyRepo) Load(ctx context.Context, s workflow.Scope, n string) (*workflow.Workflow, error) {
	return r.load(ctx, s, n)
}
func TestRepositoryManifests(t *testing.T) {
	ctx := context.Background()
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	s := workflow.Shared()
	wf := layouttest.SampleWorkflow()
	wf.Presentation = &workflow.Presentation{Revision: uuid.Must(uuid.NewV7()).String()}
	require.NoError(t, repo.Create(ctx, s, "saved", wf))
	source := layout.NewRepositoryManifests(repo)
	rev, ok, err := source.CurrentRevision(ctx, s, "saved")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, wf.SavedRevision(), rev)
	wf.Presentation = nil
	require.NoError(t, repo.Create(ctx, s, "legacy", wf))
	for _, name := range []string{"legacy", "gone"} {
		rev, ok, err = source.CurrentRevision(ctx, s, name)
		require.NoError(t, err)
		assert.False(t, ok)
		assert.Empty(t, rev)
	}
	boom := errors.New("disk")
	calls := 0
	source = layout.NewRepositoryManifests(loadOnlyRepo{load: func(c context.Context, got workflow.Scope, name string) (*workflow.Workflow, error) {
		calls++
		assert.Equal(t, s, got)
		assert.Equal(t, "saved", name)
		return nil, boom
	}})
	_, _, err = source.CurrentRevision(ctx, s, "saved")
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, calls)
	source = layout.NewRepositoryManifests(loadOnlyRepo{load: func(c context.Context, _ workflow.Scope, _ string) (*workflow.Workflow, error) {
		return nil, fmt.Errorf("wrapped: %w", workflow.ErrNotFound)
	}})
	_, ok, err = source.CurrentRevision(ctx, s, "gone")
	require.NoError(t, err)
	assert.False(t, ok)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	source = layout.NewRepositoryManifests(loadOnlyRepo{load: func(c context.Context, _ workflow.Scope, _ string) (*workflow.Workflow, error) { return nil, c.Err() }})
	_, _, err = source.CurrentRevision(cancelled, s, "saved")
	assert.ErrorIs(t, err, context.Canceled)
}
