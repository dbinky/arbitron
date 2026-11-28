package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/adapter/checkpoint"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingPort struct {
	exports int
	bundle  engine.SessionBundle
}

func (p *countingPort) Export(_ context.Context, sid, dir string) (engine.SessionBundle, error) {
	p.exports++
	if sid != p.bundle.SessionID || dir != p.bundle.OriginWorkDir {
		return engine.SessionBundle{}, engine.ErrInvalidStepCompletion
	}
	return p.bundle, nil
}
func (p *countingPort) Import(context.Context, engine.SessionBundle, string) (string, error) {
	panic("export never imports")
}
func sessBundle(sid string) engine.SessionBundle {
	data := []byte("native session " + sid)
	return engine.SessionBundle{Backend: "claude-code", SessionID: sid, HarnessVersion: "2.1.0", OriginWorkDir: "/w", Data: data, Checksum: engine.BundleChecksum(data)}
}
func TestTurnCheckpoint_LimitOneExportsAndCommitsWithOneAssignment(t *testing.T) {
	f := newTurnCommitFixture(t)
	ctx := context.Background()
	port := &countingPort{bundle: sessBundle("s2")}
	_, err := f.db.DB.Exec(`UPDATE runners SET step_limit=1 WHERE id=?`, string(f.job.RunnerID))
	require.NoError(t, err)
	router := runner.NewSessionExportRouter(runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"claude-code": port}), nil)
	store, err := checkpoint.NewSessionStore(t.TempDir(), 1)
	require.NoError(t, err)
	rc := f.receipt()
	artifacts := checkpoint.NewStore(t.TempDir(), checkpoint.WithCatalog(NewCheckpointCatalogRepo(f.db.DB)))
	workspace, err := runner.NewTurnWorkspaceCheckpointer(artifacts, NewWorkspaceHomeRepo(f.db.DB)).StageTurn(ctx, engine.TurnWorkspaceRequest{
		RunID: f.inst.ID, RunnerID: rc.JobFence.RunnerID, WorkDir: "/w", Completion: rc,
	})
	require.NoError(t, err)
	assert.EqualValues(t, 1, f.homeVersion(t), "inspection cannot publish the prepared candidate")
	assert.Equal(t, "prepared", f.candidateState(t, 2))
	b, err := router.ExportSession(ctx, engine.SessionExportRequest{RunnerID: rc.JobFence.RunnerID, Backend: "claude-code", SessionID: "s2", WorkDir: "/w", JobFence: rc.JobFence, Resource: &rc.Workspace.Resource})
	require.NoError(t, err)
	assert.Equal(t, 1, f.lf.slots(t), "export used the parent assignment at limit one")
	ref, err := store.Put(ctx, engine.SessionKey{RunID: f.inst.ID, InvocationID: f.inv, TurnIndex: 2}, b)
	require.NoError(t, err)
	c := f.turn()
	c.Completions[0].Workspace = &workspace.Completion
	c.Execution.LLMConversation[engine.ConversationKeyWorkspaceCheckpointRef] = workspace.Ref
	c.CheckpointRef = ref
	require.NoError(t, f.repo.CommitTurn(ctx, c))
	var jobs int
	require.NoError(t, f.db.DB.QueryRow(`SELECT COUNT(*) FROM runner_jobs WHERE run_id = ?`, f.inst.ID.String()).Scan(&jobs))
	assert.Equal(t, 1, jobs, "zero independently reserved export jobs")
	assert.Equal(t, 1, port.exports)
	assert.Zero(t, f.lf.slots(t))
	got := f.stored(t)
	assert.Equal(t, ref, *got.SessionCheckpointRef)
	assert.Equal(t, f.wsRef(2), got.LLMConversation[engine.ConversationKeyWorkspaceCheckpointRef])
	bundle, err := store.Get(ctx, *got.SessionCheckpointRef)
	require.NoError(t, err)
	assert.Equal(t, b.Data, bundle.Data, "committed ref resolves the real stored bytes")
}
func TestTurnCheckpoint_WrongParentBundleRejected(t *testing.T) {
	f := newTurnCommitFixture(t)
	ctx := context.Background()
	port := &countingPort{bundle: sessBundle("s2")}
	router := runner.NewSessionExportRouter(runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"claude-code": port}), nil)
	rc := f.receipt()
	other := rc.JobFence
	other.RunnerID = "mac-z"
	_, err := router.ExportSession(ctx, engine.SessionExportRequest{RunnerID: rc.JobFence.RunnerID, Backend: "claude-code", SessionID: "s2", WorkDir: "/w", JobFence: other})
	require.ErrorIs(t, err, engine.ErrInvalidStepCompletion)
	assert.Zero(t, port.exports)
	c := f.turn()
	c.Completions[0].JobFence.JobID = uuid.New()
	require.ErrorIs(t, f.repo.CommitTurn(ctx, c), engine.ErrTurnFenced)
	f.assertPriorPair(t)
}
