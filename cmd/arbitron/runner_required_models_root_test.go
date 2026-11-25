package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runneragent"
)

// This is the admission payload's consumer seam, not an alternate model source.
// Capture even empty lists so missing root wiring fails the exact assertion.
type rrRequestedModels struct{ received chan []string }

func (v rrRequestedModels) VerifyModels(_ context.Context, models []string) <-chan struct{} {
	v.received <- append([]string(nil), models...)
	done := make(chan struct{})
	close(done)
	return done
}

func TestRemoteRunnerInstalledRoot_LiveProfilesReachAdmissionConsumer(t *testing.T) {
	st := bootRemoteRunnerStack(t, nil, false)
	ctx := adminCtxForLocalUser(t, st.ds)
	admit := func(name string, want []string) {
		t.Helper()
		plaintext, _, _ := rrCreateKey(t, st, name)
		client, err := wsclient.New(wsclient.Options{ServerURL: st.srv.URL, RunnerKey: plaintext})
		require.NoError(t, err)
		_, private, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		models := rrRequestedModels{received: make(chan []string, 1)}
		agent, err := runneragent.New(runneragent.Config{
			OS: "darwin", Arch: "arm64", ProtocolVersion: runnerproto.ProtocolVersion,
			RunnerVersion: rrReleasedVersion, HarnessVersions: map[string]string{"claude": "2.1.300 (Claude Code)"},
			WorkspaceRoot: st.workspaceDir, ScratchRoot: t.TempDir(),
			Capabilities: []string{runneragent.OpChat, runneragent.OpStream},
			Heartbeat:    time.Second, ProbeInterval: time.Minute,
			BackoffBase: 50 * time.Millisecond, BackoffCap: 200 * time.Millisecond,
		}, runneragent.Deps{
			Conn: client, Exec: newRRExecutor(t, "unused", false), Sources: rrSources{},
			Telemetry: rrTelemetry{}, Spool: runneragent.NewMemorySpool(1024),
			Identity: rrIdentity{key: private}, Models: models,
		})
		require.NoError(t, err)
		sessionCtx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); _ = agent.Run(sessionCtx) }()
		defer func() { cancel(); <-done }()
		select {
		case got := <-models.received:
			require.Equal(t, want, got)
		case <-time.After(30 * time.Second):
			t.Fatal("installed runner connect route never delivered admission models")
		}
	}
	admit("initial-models", []string{"claude-sonnet-4-5"})
	_, _, err := st.ds.profileSvc.Create(ctx, provider.Profile{Alias: "live-opus", Kind: provider.ProfileKindChat, Model: "claude-opus-4-1"})
	require.NoError(t, err)
	admit("updated-models", []string{"claude-opus-4-1", "claude-sonnet-4-5"})
}
