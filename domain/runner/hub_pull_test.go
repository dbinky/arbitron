// domain/runner/hub_pull_test.go
package runner_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubPull_MissingReceiverDoesNotDiscardReceipt(t *testing.T) {
	ctx := context.Background()
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	key := p.SeedRunner(t, "mac-2", 2)
	conn := hubtest.NewFakeConn()
	session, err := h.Admit(ctx, key, conn, hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	rc := runner.PullReceiptPayload{OperationID: "pull:unwired"}
	require.ErrorIs(t, h.Handle(ctx, session, runner.Inbound{Kind: runner.MsgPullReceipt, PullReceipt: &rc}), runner.ErrDrainUnavailable)
	assert.Zero(t, conn.Count(runner.MsgPullReceiptAck, uuid.Nil), "missing wiring must not erase the runner's replay evidence")
}

func TestHubPull_RequestReceiptAndReplayReachTheCoordinator(t *testing.T) {
	ctx := context.Background()
	p := hubtest.NewPool(t)
	h := p.NewHub(hubtest.HubConfig())
	key := p.SeedRunner(t, "mac-2", 2)
	leases := adaptsqlite.NewResourceLeaseRepo(p.DB, "boot-test")
	homes := adaptsqlite.NewWorkspaceHomeRepo(p.DB)
	tokens := runner.NewTransferTokenService(adaptsqlite.NewTransferSessionRepo(p.DB), p.Ledger, leases, p.Clock.Now)
	coord, err := runner.NewWorkspacePullCoordinator(runner.PullCoordinatorDeps{Hub: h, Tokens: tokens, Leases: leases, Homes: homes,
		Workspaces: fixedWorkspaces{}, Timeout: 5 * time.Second})
	require.NoError(t, err)
	h.SetPullReceiver(coord)

	run := uuid.New()
	ref := runner.CheckpointRef{RunID: run, Version: 2, ManifestHash: strings.Repeat("c", 64)}
	claim, err := leases.Claim(ctx, "run:"+run.String(), "drain:test", "mac-2")
	require.NoError(t, err)

	_, err = coord.PullCommitted(ctx, run, "mac-2", ref, claim)
	require.ErrorIs(t, err, runner.ErrPullTargetOffline, "an offline target fails fast")

	conn := hubtest.NewFakeConn()
	_, err = h.Admit(ctx, key, conn, hubtest.DarwinHello(hubtest.AnthropicSource()))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := coord.PullCommitted(ctx, run, "mac-2", ref, claim); done <- err }()

	req := conn.Await(t, runner.MsgPullWorkspace, uuid.Nil)
	require.NotNil(t, req.PullWorkspace)
	assert.Equal(t, ref, req.PullWorkspace.Ref)
	// The transmitted token must authorize the exact persisted maintenance scope,
	// not just be a nonempty placeholder supplied by a test issuer.
	token, err := tokens.Validate(ctx, req.PullWorkspace.PullToken, "mac-2")
	require.NoError(t, err)
	assert.Equal(t, ref.String(), token.BaseRef)
	assert.Equal(t, claim, token.Resource)
	assert.Equal(t, req.PullWorkspace.OperationID, token.MaintenanceOperationID)
	assert.Equal(t, runner.TransferPull, token.Direction)
	assert.Equal(t, run, token.RunID)
	_, err = tokens.Validate(ctx, req.PullWorkspace.PullToken, "mac-1")
	require.ErrorIs(t, err, runner.ErrTransferScope)
	rc := runner.PullReceiptPayload{OperationID: req.PullWorkspace.OperationID, RunID: run, Ref: ref, Resource: claim, OK: true}

	// The socket drops before the receipt; the runner replays it in its next hello.
	conn2 := hubtest.NewFakeConn()
	hello := hubtest.DarwinHello(hubtest.AnthropicSource())
	hello.PullReceipts = []runner.PullReceiptPayload{rc}
	s2, err := h.Admit(ctx, key, conn2, hello)
	require.NoError(t, err)
	require.NoError(t, <-done)
	ack := conn2.Await(t, runner.MsgPullReceiptAck, uuid.Nil)
	assert.Equal(t, rc.OperationID, ack.PullReceiptAck)

	// A live receipt on the current session is acknowledged too, and a receipt without a payload is malformed.
	require.NoError(t, h.Handle(ctx, s2, runner.Inbound{Kind: runner.MsgPullReceipt, PullReceipt: &rc}))
	assert.Equal(t, 2, conn2.Count(runner.MsgPullReceiptAck, uuid.Nil))
	require.ErrorIs(t, h.Handle(ctx, s2, runner.Inbound{Kind: runner.MsgPullReceipt}), runner.ErrMalformedMessage)
}
