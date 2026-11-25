package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/process"
	builtinrunner "github.com/arbitron/arbitron/adapter/runner/builtin"
	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	runnerprobe "github.com/arbitron/arbitron/adapter/runner/probe"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
)

// Exercise the real server constructor and admission sink, not a direct
// SetRequiredModels call. Profile changes must be read at each admission.
func TestWireRemoteRunners_LiveProfilesFeedAdmissionModels(t *testing.T) {
	old := version
	version = "0.2.0"
	t.Cleanup(func() { version = old })
	ds, _ := rwBoot(t, 8)
	ctx := adminCtxForLocalUser(t, ds)
	profiles := adaptsqlite.NewProfileRepo(ds.db)
	_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "requested-sonnet", Kind: provider.ProfileKindChat, Model: "claude-sonnet-4-5"})
	require.NoError(t, err)
	pool := ds.runners
	remote, err := wireRemoteRunners(remoteRunnerInputs{
		DB: ds.db, Runners: ds.cfg.Runners, ServerRoot: ds.cfg.Workspace.Path,
		Registry: pool.registry, Ledger: pool.ledger, Events: pool.recorder,
		Health: pool.monitor, Notifier: pool.notifier, Audit: ds.auditSvc,
		Builtin:        builtinrunner.New(builtinrunner.Deps{Chats: pool.innerChats, Agents: pool.innerAgents, Streams: pool.innerStreams, Process: process.NewLocalProcessRunner(), Confirmations: runnerprobe.NewConfirmationExecutor(pool.innerAgents)}),
		Logger:         slog.New(slog.DiscardHandler),
		RequiredModels: profileClaudeModels{profiles: profiles, resolver: ds.profileResolver},
	})
	require.NoError(t, err)
	admit := func(name string) []string {
		t.Helper()
		_, key, err := remote.Keys.Create(ctx, name)
		require.NoError(t, err)
		conn := hubtest.NewFakeConn()
		hello := hubtest.DarwinHello()
		hello.WorkspaceRoot = ds.cfg.Workspace.Path
		compat, err := remoteCompatTable()
		require.NoError(t, err)
		hello.HarnessVersions["claude"] = compat.Harness["claude"]
		session, err := remote.Hub.Admit(context.Background(), key, conn, hello)
		require.NoError(t, err)
		t.Cleanup(func() { remote.Hub.Disconnect(session) })
		sent := conn.Sent()
		require.NotEmpty(t, sent)
		require.Equal(t, runner.MsgAdmitted, sent[0].Kind)
		require.NotNil(t, sent[0].Admitted)
		return sent[0].Admitted.ClaudeModels
	}
	assert.Equal(t, []string{"claude-sonnet-4-5"}, admit("first"))
	_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "requested-opus", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-opus-4-1", "claude-sonnet-4-5"}, admit("second"), "admission reads the live producer rather than a boot snapshot")
}
