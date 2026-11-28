package runner_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type remoteStub struct {
	got    []runner.RunnerID
	reqs   []engine.SessionExportRequest
	result engine.SessionBundle
	err    error
}

func (r *remoteStub) ExportSession(_ context.Context, id runner.RunnerID, req engine.SessionExportRequest) (engine.SessionBundle, error) {
	r.got, r.reqs = append(r.got, id), append(r.reqs, req)
	return r.result, r.err
}

func fence(runnerID string) engine.JobFenceRef {
	return engine.JobFenceRef{JobID: uuid.New(), RunnerID: runnerID, Generation: 2}
}

func TestSessionExportRouter_RoutesByReceiptRunner(t *testing.T) {
	claude := &portStub{backend: "claude-code"}
	data := []byte("remote bytes")
	remote := &remoteStub{result: engine.SessionBundle{Backend: "codex", SessionID: "t1", HarnessVersion: "0.170.0", OriginWorkDir: "/w", Data: data, Checksum: engine.BundleChecksum(data)}}
	r := runner.NewSessionExportRouter(runner.NewSessionPortabilityRegistry(map[string]engine.SessionPortability{"claude-code": claude}), remote)
	b, err := r.ExportSession(context.Background(), engine.SessionExportRequest{RunnerID: "builtin", Backend: "claude-code", SessionID: "s1", WorkDir: "/w", JobFence: fence("builtin")})
	require.NoError(t, err)
	assert.Equal(t, "s1", b.SessionID)
	assert.Equal(t, []string{"s1@/w"}, claude.calls)
	assert.Empty(t, remote.got, "the built-in never goes through the hub")
	req := engine.SessionExportRequest{RunnerID: "mac-a", Backend: "codex", SessionID: "t1", WorkDir: "/w", JobFence: fence("mac-a")}
	b, err = r.ExportSession(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, []runner.RunnerID{"mac-a"}, remote.got)
	assert.Equal(t, req, remote.reqs[0], "the hub receives the exact parent fence")
	assert.Equal(t, data, b.Data)
}

func TestSessionExportRouter_RejectsMismatchedBundleOrFence(t *testing.T) {
	data := []byte("x")
	remote := &remoteStub{}
	r := runner.NewSessionExportRouter(runner.NewSessionPortabilityRegistry(nil), remote)
	req := engine.SessionExportRequest{RunnerID: "mac-a", Backend: "codex", SessionID: "t1", WorkDir: "/w", JobFence: fence("mac-a")}
	remote.result = engine.SessionBundle{Backend: "codex", SessionID: "t1", Data: data, Checksum: engine.BundleChecksum([]byte("other"))}
	_, err := r.ExportSession(context.Background(), req)
	assert.ErrorIs(t, err, engine.ErrBundleChecksum, "a corrupted transfer is never stored")
	remote.result = engine.SessionBundle{Backend: "codex", SessionID: "someone-else", Data: data, Checksum: engine.BundleChecksum(data)}
	_, err = r.ExportSession(context.Background(), req)
	assert.Error(t, err, "the bundle must be the requested session")
	wrong := req
	wrong.JobFence = fence("mac-b")
	_, err = r.ExportSession(context.Background(), wrong)
	assert.ErrorIs(t, err, engine.ErrInvalidStepCompletion, "export runs only under the holding runner's own stream fence")
}

func TestSessionExportRouter_FailsClosedWithoutProducers(t *testing.T) {
	noHub := runner.NewSessionExportRouter(runner.NewSessionPortabilityRegistry(nil), nil)
	_, err := noHub.ExportSession(context.Background(), engine.SessionExportRequest{RunnerID: "mac-a", Backend: "codex", SessionID: "t1", JobFence: fence("mac-a")})
	assert.ErrorIs(t, err, runner.ErrRemoteSessionExportUnavailable)
	_, err = noHub.ExportSession(context.Background(), engine.SessionExportRequest{RunnerID: "builtin", Backend: "pi", SessionID: "t1", JobFence: fence("builtin")})
	assert.ErrorIs(t, err, runner.ErrNoSessionPortability)
	_, err = noHub.ExportSession(context.Background(), engine.SessionExportRequest{RunnerID: "builtin", Backend: "claude-code", SessionID: "t1"})
	assert.ErrorIs(t, err, engine.ErrInvalidStepCompletion, "no receipt, no export")
}

type cpStub struct {
	runner.CheckpointStore
	prepared      map[uuid.UUID]runner.CheckpointRef
	claims        map[runner.Fence]engine.ResourceFenceRef
	otherPrepared map[runner.Fence]runner.CheckpointRef
	jobs          []runner.Fence
	resources     []engine.ResourceFenceRef
	err           error
}

func (c *cpStub) PreparedFor(_ context.Context, run uuid.UUID, job runner.Fence, res engine.ResourceFenceRef) (runner.CheckpointRef, bool, error) {
	c.jobs, c.resources = append(c.jobs, job), append(c.resources, res)
	if c.err != nil {
		return runner.CheckpointRef{}, false, c.err
	}
	if claim, ok := c.claims[job]; !ok || claim != res {
		return runner.CheckpointRef{}, false, nil
	}
	if ref, ok := c.otherPrepared[job]; ok {
		return ref, ref.RunID == run, nil
	}
	ref, ok := c.prepared[run]
	return ref, ok, nil
}

func (c *cpStub) allow(rc engine.StepCompletion) {
	if c.claims == nil {
		c.claims = make(map[runner.Fence]engine.ResourceFenceRef)
	}
	if rc.Workspace != nil {
		c.claims[runner.Fence{JobID: rc.JobFence.JobID, RunnerID: runner.RunnerID(rc.JobFence.RunnerID), Generation: rc.JobFence.Generation}] = rc.Workspace.Resource
	}
}

type homesStub struct {
	runner.HomeStore
	home runner.Home
}

func (h *homesStub) GetHome(_ context.Context, run uuid.UUID) (runner.Home, error) {
	if run != h.home.RunID {
		return runner.Home{}, errors.New("test: home not found")
	}
	return h.home, nil
}

func candidate(run uuid.UUID, v int64) (runner.CheckpointRef, runner.Manifest) {
	h := strings.Repeat(string(rune('a'+v)), 64)
	return runner.CheckpointRef{RunID: run, Version: v, ManifestHash: h}, runner.Manifest{Version: v, Hash: h}
}
func wsReceipt(runnerID string, ref runner.CheckpointRef) engine.StepCompletion {
	return engine.StepCompletion{JobFence: fence(runnerID), Workspace: &engine.WorkspaceCompletion{Ref: runner.FormatTurnWorkspaceRef(ref), Resource: engine.ResourceFenceRef{Key: "run:" + ref.RunID.String(), Owner: runnerID, Generation: 1, Token: uuid.New()}}}
}

func TestTurnWorkspaceCheckpointer_ReusesExactPreparedCandidate(t *testing.T) {
	run := uuid.New()
	ref, _ := candidate(run, 3)
	cp := &cpStub{prepared: map[uuid.UUID]runner.CheckpointRef{run: ref}}
	w := runner.NewTurnWorkspaceCheckpointer(cp, &homesStub{home: runner.Home{RunID: run, CheckpointVersion: 2}})
	for _, runnerID := range []string{"builtin", "mac-a"} {
		rc := wsReceipt(runnerID, ref)
		cp.allow(rc)
		// A different assignment really has a newer prepared upload.
		newer := wsReceipt(runnerID, ref)
		cp.allow(newer)
		v4, _ := candidate(run, 4)
		cp.otherPrepared = map[runner.Fence]runner.CheckpointRef{{JobID: newer.JobFence.JobID, RunnerID: runner.RunnerID(runnerID), Generation: newer.JobFence.Generation}: v4}
		s, err := w.StageTurn(context.Background(), engine.TurnWorkspaceRequest{RunID: run, RunnerID: runnerID, WorkDir: "/srv/ws/run", Completion: rc})
		require.NoError(t, err, runnerID)
		assert.EqualValues(t, 3, s.Version, "never the newer upload v4")
		assert.Equal(t, run.String()+"@3:"+ref.ManifestHash, s.Ref)
		assert.Equal(t, *rc.Workspace, s.Completion, "the exact dispatcher receipt reaches CommitTurn")
		assert.Equal(t, runner.Fence{JobID: rc.JobFence.JobID, RunnerID: runner.RunnerID(runnerID), Generation: rc.JobFence.Generation}, cp.jobs[len(cp.jobs)-1])
		assert.Equal(t, rc.Workspace.Resource, cp.resources[len(cp.resources)-1])
	}
	parsed, err := runner.ParseTurnWorkspaceRef(runner.FormatTurnWorkspaceRef(ref))
	require.NoError(t, err)
	assert.Equal(t, ref, parsed)
}

func TestTurnWorkspaceCheckpointer_RejectsAnythingButTheCurrentReceipt(t *testing.T) {
	run := uuid.New()
	ref, _ := candidate(run, 3)
	old, _ := candidate(run, 2)
	cp := &cpStub{prepared: map[uuid.UUID]runner.CheckpointRef{run: ref}}
	w := runner.NewTurnWorkspaceCheckpointer(cp, &homesStub{home: runner.Home{RunID: run, CheckpointVersion: 2}})
	stage := func(rc engine.StepCompletion, runnerID string) error {
		_, err := w.StageTurn(context.Background(), engine.TurnWorkspaceRequest{RunID: run, RunnerID: runnerID, WorkDir: "/w", Completion: rc})
		return err
	}
	foreignOwner := wsReceipt("mac-a", ref)
	foreignOwner.Workspace.Resource.Owner = "mac-b"
	foreignJob := wsReceipt("mac-a", ref)
	foreignJob.JobFence.RunnerID = "mac-b"
	otherRun := wsReceipt("mac-a", runner.CheckpointRef{RunID: uuid.New(), Version: 3, ManifestHash: ref.ManifestHash})
	badHash := wsReceipt("mac-a", runner.CheckpointRef{RunID: run, Version: 3, ManifestHash: strings.Repeat("f", 64)})
	noWorkspace := engine.StepCompletion{JobFence: fence("mac-a")}
	for name, rc := range map[string]engine.StepCompletion{
		"resource owner": foreignOwner, "job runner": foreignJob, "other run": otherRun, "manifest hash": badHash,
		"missing workspace receipt": noWorkspace, "old candidate": wsReceipt("mac-a", old),
	} {
		cp.allow(rc)
		assert.ErrorIs(t, stage(rc, "mac-a"), engine.ErrInvalidStepCompletion, name)
	}
	legitimate := wsReceipt("mac-a", ref)
	cp.allow(legitimate)
	forged := legitimate
	forged.JobFence.JobID = uuid.New()
	assert.ErrorIs(t, stage(forged, "mac-a"), engine.ErrInvalidStepCompletion, "another job has no prepared candidate")
	forged = legitimate
	wcopy := *legitimate.Workspace
	wcopy.Resource.Token = uuid.New()
	forged.Workspace = &wcopy
	assert.ErrorIs(t, stage(forged, "mac-a"), engine.ErrInvalidStepCompletion, "another claim has no prepared candidate")
	delete(cp.prepared, run)
	assert.ErrorIs(t, stage(wsReceipt("mac-a", ref), "mac-a"), engine.ErrInvalidStepCompletion, "no prepared candidate under this fence")
	cp.err = errors.New("io error")
	assert.Error(t, stage(wsReceipt("mac-a", ref), "mac-a"), "store errors propagate")
}
