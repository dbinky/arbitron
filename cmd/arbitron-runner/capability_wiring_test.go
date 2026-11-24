package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/adapter/runneragent/sources"
)

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
