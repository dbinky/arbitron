// domain/runner/maintenance_pull_test.go
package runner_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pullRequest struct {
	Target runner.RunnerID
	P      runner.PullWorkspacePayload
}

// fakePullHub stands in for the hub's send side and can answer each request.
type fakePullHub struct {
	mu       sync.Mutex
	requests []pullRequest
	acks     []string
	offline  bool
	answer   func(pullRequest) *runner.PullReceiptPayload
	coord    *runner.WorkspacePullCoordinator
}

func (h *fakePullHub) RequestPull(ctx context.Context, target runner.RunnerID, p runner.PullWorkspacePayload) error {
	h.mu.Lock()
	if h.offline {
		h.mu.Unlock()
		return runner.ErrPullTargetOffline
	}
	req := pullRequest{Target: target, P: p}
	h.requests = append(h.requests, req)
	answer := h.answer
	h.mu.Unlock()
	if answer != nil {
		if rc := answer(req); rc != nil {
			go h.coord.Receive(context.Background(), target, *rc)
		}
	}
	return nil
}

func (h *fakePullHub) AckPullReceipt(_ context.Context, _ runner.RunnerID, opID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.acks = append(h.acks, opID)
}

func (h *fakePullHub) sent() ([]pullRequest, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]pullRequest(nil), h.requests...), append([]string(nil), h.acks...)
}

type recordingPullTokens struct {
	mu     sync.Mutex
	scopes []runner.TransferScope
}

func (t *recordingPullTokens) Issue(_ context.Context, s runner.TransferScope, ttl time.Duration) (runner.TransferToken, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scopes = append(t.scopes, s)
	return runner.TransferToken{Token: "atr_test", MaintenanceOperationID: s.MaintenanceOperationID, RunnerID: s.Fence.RunnerID, RunID: s.RunID,
		Direction: s.Direction, BaseRef: s.BaseRef, Resource: s.Resource}, nil
}

type fixedWorkspaces struct{ engine.WorkspaceManager }

func (fixedWorkspaces) EnsureInstanceDir(id uuid.UUID) (string, error) {
	return "/srv/arbitron/workspaces/instances/" + id.String(), nil
}

type pullRig struct {
	hub    *fakePullHub
	tokens *recordingPullTokens
	leases *adaptsqlite.ResourceLeaseRepo
	homes  *adaptsqlite.WorkspaceHomeRepo
	coord  *runner.WorkspacePullCoordinator
	run    uuid.UUID
	ref    runner.CheckpointRef
	claim  engine.ResourceFenceRef
}

func newPullRig(t *testing.T, timeout time.Duration) *pullRig {
	t.Helper()
	ctx := context.Background()
	p := hubtest.NewPool(t)
	r := &pullRig{hub: &fakePullHub{}, tokens: &recordingPullTokens{}, leases: adaptsqlite.NewResourceLeaseRepo(p.DB, "boot-test"),
		homes: adaptsqlite.NewWorkspaceHomeRepo(p.DB), run: uuid.New()}
	_, err := r.homes.HandOff(ctx, r.run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	r.ref = runner.CheckpointRef{RunID: r.run, Version: 4, ManifestHash: strings.Repeat("b", 64)}
	r.claim, err = r.leases.Claim(ctx, "run:"+r.run.String(), "drain:test", "mac-2")
	require.NoError(t, err)
	r.coord, err = runner.NewWorkspacePullCoordinator(runner.PullCoordinatorDeps{Hub: r.hub, Tokens: r.tokens, Leases: r.leases,
		Homes: r.homes, Workspaces: fixedWorkspaces{}, Clock: p.Clock.Now, Timeout: timeout})
	require.NoError(t, err)
	r.hub.coord = r.coord
	return r
}

func okReceipt(req pullRequest) *runner.PullReceiptPayload {
	return &runner.PullReceiptPayload{OperationID: req.P.OperationID, RunID: req.P.RunID, Ref: req.P.Ref, Resource: req.P.Resource, OK: true}
}

func TestPullCoordinator_RequestsTheExactScopedPullAndReturnsTheVerifiedReceipt(t *testing.T) {
	r := newPullRig(t, 5*time.Second)
	r.hub.answer = okReceipt

	rc, err := r.coord.PullCommitted(context.Background(), r.run, "mac-2", r.ref, r.claim)
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-2"), rc.RunnerID)
	assert.Equal(t, r.ref, rc.Ref)
	assert.Equal(t, r.claim, rc.Resource)
	assert.Equal(t, runner.PullOperationID(r.claim), rc.OperationID)

	reqs, _ := r.hub.sent()
	require.Len(t, reqs, 1)
	p := reqs[0].P
	assert.Equal(t, runner.RunnerID("mac-2"), reqs[0].Target)
	assert.Equal(t, rc.OperationID, p.OperationID)
	assert.Equal(t, "/srv/arbitron/workspaces/instances/"+r.run.String(), p.WorkDir)
	home, _ := r.homes.GetHome(context.Background(), r.run)
	assert.Equal(t, home.Generation+1, p.HomeGeneration, "the copy is recorded at the generation the drain handoff creates")
	assert.Equal(t, "atr_test", p.PullToken)
	require.Eventually(t, func() bool { _, a := r.hub.sent(); return len(a) == 1 && a[0] == rc.OperationID },
		2*time.Second, time.Millisecond, "the receipt is acknowledged so the runner stops replaying it")

	require.Len(t, r.tokens.scopes, 1)
	s := r.tokens.scopes[0]
	assert.Equal(t, runner.Fence{RunnerID: "mac-2"}, s.Fence, "a maintenance scope: no runner-job ID is fabricated")
	assert.Equal(t, rc.OperationID, s.MaintenanceOperationID)
	assert.Equal(t, runner.TransferPull, s.Direction)
	assert.Equal(t, r.ref.String(), s.BaseRef)
	assert.Equal(t, r.claim, s.Resource)
}

func TestPullCoordinator_FailedOrMismatchedReceiptIsAnError(t *testing.T) {
	r := newPullRig(t, 5*time.Second)
	r.hub.answer = func(req pullRequest) *runner.PullReceiptPayload {
		rc := okReceipt(req)
		rc.OK, rc.Error = false, "manifest hash mismatch"
		return rc
	}
	_, err := r.coord.PullCommitted(context.Background(), r.run, "mac-2", r.ref, r.claim)
	require.ErrorIs(t, err, runner.ErrPullFailed)
	assert.Contains(t, err.Error(), "manifest hash mismatch")

	r.hub.answer = func(req pullRequest) *runner.PullReceiptPayload {
		rc := okReceipt(req)
		rc.Ref.Version = 3 // verified a different checkpoint
		return rc
	}
	_, err = r.coord.PullCommitted(context.Background(), r.run, "mac-2", r.ref, r.claim)
	require.ErrorIs(t, err, runner.ErrPullReceiptMismatch)
}

func TestPullCoordinator_ReplayedReceiptCompletesTheWait(t *testing.T) {
	r := newPullRig(t, 5*time.Second)
	// The runner finished while the server was not waiting (a reconnect replay
	// arriving before this pass): the stored receipt completes the call with no new request.
	r.coord.Receive(context.Background(), "mac-2", runner.PullReceiptPayload{OperationID: runner.PullOperationID(r.claim),
		RunID: r.run, Ref: r.ref, Resource: r.claim, OK: true})
	rc, err := r.coord.PullCommitted(context.Background(), r.run, "mac-2", r.ref, r.claim)
	require.NoError(t, err)
	assert.Equal(t, r.ref, rc.Ref)
	reqs, _ := r.hub.sent()
	assert.Empty(t, reqs)
}

func TestPullCoordinator_ReceiptFromAnotherRunnerIsIgnored(t *testing.T) {
	r := newPullRig(t, 200*time.Millisecond)
	r.hub.answer = func(req pullRequest) *runner.PullReceiptPayload {
		go r.coord.Receive(context.Background(), "mac-3", *okReceipt(req))
		return nil
	}
	_, err := r.coord.PullCommitted(context.Background(), r.run, "mac-2", r.ref, r.claim)
	require.ErrorIs(t, err, runner.ErrPullTimeout, "only the target's receipt counts")
}

func TestPullCoordinator_OfflineTargetFencedClaimAndZeroRef(t *testing.T) {
	ctx := context.Background()
	r := newPullRig(t, 5*time.Second)
	r.hub.offline = true
	_, err := r.coord.PullCommitted(ctx, r.run, "mac-2", r.ref, r.claim)
	require.ErrorIs(t, err, runner.ErrPullTargetOffline)

	rc, err := r.coord.PullCommitted(ctx, r.run, "mac-2", runner.CheckpointRef{}, r.claim)
	require.NoError(t, err, "an empty workspace needs no transfer")
	assert.True(t, rc.Ref.IsZero())

	_, err = r.coord.PullCommitted(ctx, r.run, "mac-1", r.ref, r.claim)
	require.ErrorIs(t, err, runner.ErrPullScope, "the claim must be owned by the target")

	r.hub.offline = false
	require.NoError(t, r.leases.Release(ctx, r.claim))
	_, err = r.coord.PullCommitted(ctx, r.run, "mac-2", r.ref, r.claim)
	require.ErrorIs(t, err, runner.ErrResourceFenced)
	reqs, _ := r.hub.sent()
	assert.Empty(t, reqs, "a fenced claim never reaches the runner")
}

func TestNewWorkspacePullCoordinator_RequiresDeps(t *testing.T) {
	_, err := runner.NewWorkspacePullCoordinator(runner.PullCoordinatorDeps{})
	require.Error(t, err)
	for _, name := range []string{"Hub", "Tokens", "Leases", "Homes", "Workspaces"} {
		assert.Contains(t, err.Error(), name)
	}
	assert.False(t, errors.Is(err, runner.ErrPullFailed))
}
