package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ntTurnReq(f *lcFixture, w *engine.WorkspaceCompletion) engine.TurnWorkspaceRequest {
	return engine.TurnWorkspaceRequest{RunID: f.run, RunnerID: string(runner.BuiltInRunnerID), WorkDir: "/ws/run", Completion: engine.StepCompletion{JobFence: engine.JobFenceRef{JobID: uuid.New(), RunnerID: string(runner.BuiltInRunnerID), Generation: 1}, Workspace: w}}
}
func TestTurnWorkspace_NonPortableReusesTheLocalCheckpointReceipt(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(2)
	local := runner.LocalCheckpointRef{RunID: f.run, Version: 3, ManifestHash: lcHash(3)}.String()
	w := &engine.WorkspaceCompletion{NonPortable: true, LocalRef: local, Resource: f.claim}
	staged, err := runner.NewTurnWorkspaceCheckpointer(f.catalog, f.homes).StageTurn(context.Background(), ntTurnReq(f, w))
	require.NoError(t, err)
	assert.Equal(t, *w, staged.Completion)
	assert.Equal(t, local, staged.Ref)
	assert.EqualValues(t, 3, staged.Version)
	assert.Empty(t, f.local.staged)
}
func TestTurnWorkspace_NonPortableRequiresTheBuiltinJobAndResource(t *testing.T) {
	f := newLCFixture(t)
	f.nonPortable(2)
	local := runner.LocalCheckpointRef{RunID: f.run, Version: 3, ManifestHash: lcHash(3)}.String()
	for name, mutate := range map[string]func(*engine.TurnWorkspaceRequest){
		"job runner":     func(req *engine.TurnWorkspaceRequest) { req.Completion.JobFence.RunnerID = "mac-a" },
		"resource owner": func(req *engine.TurnWorkspaceRequest) { req.Completion.Workspace.Resource.Owner = "mac-a" },
		"missing job":    func(req *engine.TurnWorkspaceRequest) { req.Completion.JobFence.JobID = uuid.Nil },
		"missing claim":  func(req *engine.TurnWorkspaceRequest) { req.Completion.Workspace.Resource.Token = uuid.Nil },
	} {
		t.Run(name, func(t *testing.T) {
			req := ntTurnReq(f, &engine.WorkspaceCompletion{NonPortable: true, LocalRef: local, Resource: f.claim})
			mutate(&req)
			_, err := runner.NewTurnWorkspaceCheckpointer(f.catalog, f.homes).StageTurn(context.Background(), req)
			require.ErrorIs(t, err, engine.ErrInvalidStepCompletion)
		})
	}
}

func TestTurnWorkspace_NonPortableReceiptIsExact(t *testing.T) {
	ctx := context.Background()
	f := newLCFixture(t)
	f.nonPortable(2)
	next := runner.LocalCheckpointRef{RunID: f.run, Version: 3, ManifestHash: lcHash(3)}.String()
	stale := runner.LocalCheckpointRef{RunID: f.run, Version: 2, ManifestHash: lcHash(2)}.String()
	other := runner.LocalCheckpointRef{RunID: uuid.New(), Version: 3, ManifestHash: lcHash(3)}.String()
	cp := runner.NewTurnWorkspaceCheckpointer(f.catalog, f.homes)
	for name, w := range map[string]*engine.WorkspaceCompletion{
		"non-portable without LocalRef":   {NonPortable: true, Resource: f.claim},
		"LocalRef on a portable receipt":  {LocalRef: next, Resource: f.claim},
		"non-portable with a catalog ref": {NonPortable: true, LocalRef: next, Ref: f.run.String() + "@3:" + lcHash(3), Resource: f.claim},
		"stale local version":             {NonPortable: true, LocalRef: stale, Resource: f.claim},
		"another run's local checkpoint":  {NonPortable: true, LocalRef: other, Resource: f.claim},
	} {
		_, err := cp.StageTurn(ctx, ntTurnReq(f, w))
		require.ErrorIs(t, err, engine.ErrInvalidStepCompletion, name)
	}
	remote := ntTurnReq(f, &engine.WorkspaceCompletion{NonPortable: true, LocalRef: next, Resource: f.claim})
	remote.RunnerID = "mac-1"
	_, err := cp.StageTurn(ctx, remote)
	require.ErrorIs(t, err, runner.ErrHomeOwned)
	g := newLCFixture(t)
	gl := runner.LocalCheckpointRef{RunID: g.run, Version: 1, ManifestHash: lcHash(1)}.String()
	_, err = runner.NewTurnWorkspaceCheckpointer(g.catalog, g.homes).StageTurn(ctx, ntTurnReq(g, &engine.WorkspaceCompletion{NonPortable: true, LocalRef: gl, Resource: g.claim}))
	require.ErrorIs(t, err, engine.ErrInvalidStepCompletion)
}
