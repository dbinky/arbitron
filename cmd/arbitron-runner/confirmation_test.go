package main

import (
	"context"
	"github.com/arbitron/arbitron/adapter/claudecli"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildExecutor_ConfirmRunsThisMachinesNativeVerifier(t *testing.T) {
	root := t.TempDir()
	ws := filepath.Join(root, "ws")
	require.NoError(t, os.MkdirAll(filepath.Join(ws, "scratch"), 0o700))
	bin := filepath.Join(root, "claude")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo '2.1.330 (Claude Code)'; exit 0;; esac\ncat >/dev/null\n" + "echo \"$*\" >> '" + root + "/calls.log'\n" + "echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"OK\",\"session_id\":\"rs\",\"modelUsage\":{\"claude-opus-5-5\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	cfg := config.Config{ConfigDir: root, WorkspaceRoot: ws, ScratchRoot: filepath.Join(ws, "scratch"), ClaudeBinary: bin}
	cc := claudecli.Config{BinaryPath: bin, WorkspaceRoot: ws, Timeout: time.Minute}
	local := providersadapter.NewFactory(providersadapter.Deps{ClaudeChatConfig: cc, ClaudeAgentConfig: cc})
	exec, err := buildExecutor(cfg, local)
	require.NoError(t, err)
	d := runneragent.Dispatch{JobID: "6b1f3c1e-5d2a-4c5b-9e0f-7a8b9c0d1e2f", Generation: 1, Op: runneragent.OpConfirm, Confirmation: &runneragent.ConfirmationJob{Model: "claude-opus-5-5", Backend: "claude-code", Provider: "claude-cli"}}
	ok, reason := exec.Supports(d)
	require.True(t, ok, reason)
	res := exec.Execute(context.Background(), d, nil)
	require.True(t, res.OK, res.ErrorText)
	assert.Equal(t, &runner.ConfirmationResult{RequestedModel: "claude-opus-5-5", ExecutedModel: "claude-opus-5-5", SessionID: "rs"}, res.Confirmation)
	argv, err := os.ReadFile(filepath.Join(root, "calls.log"))
	require.NoError(t, err)
	assert.Contains(t, string(argv), "--safe-mode")
	assert.Equal(t, 1, strings.Count(string(argv), "\n"))
}
