package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildAgent_UnsupportedVersionRefusalReachesHello(t *testing.T) {
	srv, done := stubHub(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bin := filepath.Join(home, "claude")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo '2.1.100 (Claude Code)';;\nauth) echo '{\"loggedIn\":true}';;\n*) exit 1;;\nesac\n"), 0o700))
	cfg := config.Config{
		ServerURL: srv.URL, RunnerKey: testRunnerKey, ConfigDir: home,
		WorkspaceRoot: filepath.Join(home, "workspaces"), ScratchRoot: filepath.Join(home, "scratch"), SpoolDir: filepath.Join(home, "spool"),
		ClaudeBinary: bin, OutputBufferFrames: 64, Heartbeat: time.Second, ProbeInterval: time.Hour,
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
		assert.Equal(t, "2.1.100", got.hello.HarnessVersions["claude"])
		assert.Empty(t, got.hello.Sources[0].Capabilities)
		assert.Equal(t, "harness_unsupported", got.hello.Sources[0].UnavailableReason)
	case <-ctx.Done():
		t.Fatal("no hello reached server")
	}
}
