package sessionports_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/adapter/sessionports"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixed(v string) sessionports.VersionFunc {
	return func(context.Context) (string, error) { return v, nil }
}

func TestVersionFuncs_ReportOnlyDetectedHarnesses(t *testing.T) {
	ctx := context.Background()
	versions := func() map[string]string { return map[string]string{"claude": " 2.1.300 (Claude Code) "} }
	v, err := sessionports.MapVersion(versions, "claude")(ctx)
	require.NoError(t, err)
	assert.Equal(t, "2.1.300 (Claude Code)", v)
	_, err = sessionports.MapVersion(versions, "codex")(ctx)
	assert.Error(t, err, "an undetected harness has no version")
}

func TestInventoryVersion_ReportOnlyDetectedHarnesses(t *testing.T) {
	ctx := context.Background()
	codexOf := func(in runner.InventoryInput) *runner.HarnessPresence { return in.Codex }
	found := func(context.Context) (runner.InventoryInput, error) {
		return runner.InventoryInput{Codex: &runner.HarnessPresence{Path: "/usr/local/bin/codex", Version: "0.139.0", Supported: true}}, nil
	}
	v, err := sessionports.InventoryVersion(found, codexOf, "codex")(ctx)
	require.NoError(t, err)
	assert.Equal(t, "0.139.0", v)
	missing := func(context.Context) (runner.InventoryInput, error) { return runner.InventoryInput{}, nil }
	_, err = sessionports.InventoryVersion(missing, codexOf, "codex")(ctx)
	assert.Error(t, err)
	broken := func(context.Context) (runner.InventoryInput, error) {
		return runner.InventoryInput{}, errors.New("codex --version: exit 2")
	}
	_, err = sessionports.InventoryVersion(broken, codexOf, "codex")(ctx)
	assert.ErrorContains(t, err, "exit 2")
}

func TestClaudeVersion_MissingBinary(t *testing.T) {
	_, err := sessionports.ClaudeVersion(nil, "")(context.Background())
	assert.Error(t, err, "no claude binary, no version")
}

func TestNew_LinksClaudeAndCodexAdapters(t *testing.T) {
	claudeHome, codexHome := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	t.Setenv("CODEX_HOME", codexHome)
	const workDir = "/srv/arbitron/workspaces/run-1"
	const claudeID = "c623adbe-4edc-468c-9256-0157d5025a02"
	const codexID = "01a1f5d2-0c2a-7a72-a9b9-1b1f7521c726"
	project := filepath.Join(claudeHome, "projects", claudecli.EncodeProjectDir(workDir))
	require.NoError(t, os.MkdirAll(project, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(project, claudeID+".jsonl"), []byte("{\"type\":\"user\",\"message\":\"hello\"}\n"), 0o600))
	raw, err := os.ReadFile(filepath.Join("..", "codex", "testdata", "sessions", "with-reasoning.jsonl"))
	require.NoError(t, err)
	sessions := filepath.Join(codexHome, "sessions", "2026", "11", "19")
	require.NoError(t, os.MkdirAll(sessions, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "rollout-2026-11-19T14-59-06-"+codexID+".jsonl"), raw, 0o600))
	reg, err := sessionports.New(fixed("2.1.300 (Claude Code)"), fixed("codex-cli 0.139.0"))
	require.NoError(t, err)
	c, ok := reg.For(workflow.BackendClaudeCode)
	require.True(t, ok)
	assert.IsType(t, &claudecli.SessionPortability{}, c)
	x, ok := reg.For(workflow.BackendCodex)
	require.True(t, ok)
	assert.IsType(t, &codex.SessionPortability{}, x)
	// Assert the bundle sink, not just the registry's constructor read-back:
	// each real adapter must receive its own nonempty version producer.
	cb, err := c.Export(context.Background(), claudeID, workDir)
	require.NoError(t, err)
	require.NoError(t, cb.Verify())
	assert.Equal(t, "2.1.300 (Claude Code)", cb.HarnessVersion)
	assert.Equal(t, workflow.BackendClaudeCode, cb.Backend)
	assert.NotEmpty(t, cb.Data)
	xb, err := x.Export(context.Background(), codexID, workDir)
	require.NoError(t, err)
	require.NoError(t, xb.Verify())
	assert.Equal(t, "codex-cli 0.139.0", xb.HarnessVersion)
	assert.Equal(t, workflow.BackendCodex, xb.Backend)
	assert.Equal(t, raw, xb.Data)
	_, lossy := x.(engine.LossyImporter)
	assert.True(t, lossy, "every codex move stays lossy (RR-14)")
	_, ok = reg.For("pi")
	assert.False(t, ok, "backends without a portability adapter rebuild from the transcript")

	_, err = sessionports.New(nil, fixed("x"))
	assert.Error(t, err)
	_, err = sessionports.New(fixed("x"), nil)
	assert.Error(t, err)
}
