package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/adapter/runneragent/sources"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func capabilityRootRun(t *testing.T, models []string, mutate func(*runnerproto.WireChat)) stubResult {
	t.Helper()
	srv, done := stubHubWith(t, models, mutate)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	cfg := config.Config{ServerURL: srv.URL, RunnerKey: testRunnerKey, ConfigDir: home, WorkspaceRoot: filepath.Join(home, "workspaces"), ScratchRoot: filepath.Join(home, "scratch"), SpoolDir: filepath.Join(home, "spool"), Heartbeat: time.Second, ProbeInterval: time.Hour, OutputBufferFrames: 100}
	require.NoError(t, os.MkdirAll(cfg.WorkspaceRoot, 0700))
	cfg.ClaudeBinary = writeFakeClaude(t, t.TempDir())
	// Native execution proves only sonnet; opus is silently substituted.
	b, err := os.ReadFile(cfg.ClaudeBinary)
	require.NoError(t, err)
	b = []byte(strings.Replace(string(b), "echo '{\"type\":\"result\"", "if [ \"$m\" = claude-opus-5-5 ]; then m=claude-haiku-4-5; fi\necho '{\"type\":\"result\"", 1))
	require.NoError(t, os.WriteFile(cfg.ClaudeBinary, b, 0700))
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	agent, err := buildAgent(ctx, cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- agent.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(3 * time.Second):
			t.Error("agent did not stop")
		}
	}()
	select {
	case got := <-done:
		return got
	case <-ctx.Done():
		t.Fatal("no verified heartbeat/dispatch outcome reached server")
		return stubResult{}
	}
}

func TestBuildAgent_ServerRequestedModelsAreVerifiedThenAdvertised(t *testing.T) {
	for _, tc := range []struct{ name, model, rejected string }{
		{"verified", "claude-sonnet-4-5", ""},
		{"substituted", "claude-opus-5-5", "capability_not_advertised"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilityRootRun(t, []string{"claude-sonnet-4-5", "claude-opus-5-5"}, func(c *runnerproto.WireChat) { c.Model = tc.model })
			require.Len(t, got.heartbeat.Sources, 1)
			caps := got.heartbeat.Sources[0].Capabilities
			require.Len(t, caps, 2)
			fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli"})
			require.NoError(t, err)
			for _, c := range caps {
				assert.Equal(t, "claude-cli", c.Provider)
				assert.Equal(t, fp, c.EndpointFingerprint)
				assert.Equal(t, []string{"claude-sonnet-4-5"}, c.Models)
				assert.Equal(t, sources.ClaudeCodeTools, c.Tools)
				assert.True(t, c.ExecutedModel)
			}
			if tc.rejected == "" {
				assert.Empty(t, got.rejected)
				assert.True(t, got.result.OK)
			} else {
				assert.Contains(t, got.rejected, tc.rejected)
			}
		})
	}
}
func TestBuildAgent_NoRequestedModelsAdvertisesNoClaudeCapability(t *testing.T) {
	got := capabilityRootRun(t, nil, nil)
	require.Len(t, got.heartbeat.Sources, 1)
	assert.Empty(t, got.heartbeat.Sources[0].Capabilities)
	assert.Equal(t, sources.ReasonCapabilityEvidenceUnproven, got.heartbeat.Sources[0].UnavailableReason)
	assert.Contains(t, got.rejected, "capability_not_advertised")
}
func TestBuildAgent_UnadvertisedContextRejected(t *testing.T) {
	got := capabilityRootRun(t, []string{"claude-sonnet-4-5"}, func(c *runnerproto.WireChat) { c.ContextWindow = "2m" })
	assert.Contains(t, got.rejected, "capability_not_advertised")
}

func TestBuildAgent_UnprovenCapabilityRefusalReachesHello(t *testing.T) {
	srv, done := stubHub(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	cfg := config.Config{
		ServerURL: srv.URL, RunnerKey: testRunnerKey, ConfigDir: home,
		WorkspaceRoot: filepath.Join(home, "workspaces"), ScratchRoot: filepath.Join(home, "scratch"), SpoolDir: filepath.Join(home, "spool"),
		ClaudeBinary: writeFakeClaude(t, t.TempDir()), OutputBufferFrames: 64,
		Heartbeat: time.Second, ProbeInterval: time.Hour,
	}
	require.NoError(t, os.MkdirAll(cfg.WorkspaceRoot, 0o700))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	agent, err := buildAgent(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	stopped := make(chan error, 1)
	go func() { stopped <- agent.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("agent did not stop")
		}
	}()
	select {
	case got := <-done:
		require.Len(t, got.hello.Sources, 1)
		assert.Empty(t, got.hello.Sources[0].Capabilities)
		assert.Equal(t, sources.ReasonCapabilityEvidenceUnproven, got.hello.Sources[0].UnavailableReason,
			"loaded rules must reach discovery; absent rules produce a different refusal")
	case <-ctx.Done():
		t.Fatal("no hello reached server")
	}
}
