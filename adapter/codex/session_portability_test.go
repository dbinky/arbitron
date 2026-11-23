package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	reasoningTID  = "01a1f5d2-0c2a-7a72-a9b9-1b1f7521c726"
	compactionTID = "01a1f5d2-0c2a-7a72-a9b9-00000000beef"
	malformedTID  = "01a1f5d2-0c2a-7a72-a9b9-0000000bad00"
	sessionWork   = "/srv/arbitron/workspaces/run-1"
)

func codexVersion(v string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return v, nil }
}

func newCodexPortability(t *testing.T, home, version string) *SessionPortability {
	t.Helper()
	p, err := NewSessionPortability(SessionPortabilityOptions{
		Env: []string{"HOME=/nonexistent", "CODEX_HOME=" + home}, HarnessVersion: codexVersion(version),
		Now: func() time.Time { return time.Date(2026, 11, 20, 8, 30, 0, 0, time.UTC) },
	})
	require.NoError(t, err)
	return p
}

// installSessionRollout copies a portability fixture into the harness session layout.
func installSessionRollout(t *testing.T, home, fixture, tid string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "sessions", fixture))
	require.NoError(t, err)
	dir := filepath.Join(home, "sessions", "2026", "11", "19")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-11-19T14-59-06-"+tid+".jsonl"), raw, 0o600))
	return raw
}

func TestCodexSessionPortability_RequiresVersionSourceAndIsAlwaysLossy(t *testing.T) {
	_, err := NewSessionPortability(SessionPortabilityOptions{})
	assert.Error(t, err)
	p := newCodexPortability(t, t.TempDir(), "0.170.0")
	var li engine.LossyImporter = p
	assert.True(t, li.AlwaysLossy(), "every Codex move strips reasoning: a notice is always required")
}

func TestCodexExport_BundlesRolloutBytes(t *testing.T) {
	home := t.TempDir()
	raw := installSessionRollout(t, home, "with-reasoning.jsonl", reasoningTID)
	b, err := newCodexPortability(t, home, "0.170.0").Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)
	assert.Equal(t, "codex", b.Backend)
	assert.Equal(t, reasoningTID, b.SessionID)
	assert.Equal(t, "0.170.0", b.HarnessVersion)
	assert.Equal(t, sessionWork, b.OriginWorkDir)
	assert.Equal(t, raw, b.Data, "export never strips; stripping happens on import")
	require.NoError(t, b.Verify())
}

func TestCodexExport_Failures(t *testing.T) {
	home := t.TempDir()
	p := newCodexPortability(t, home, "0.170.0")
	_, err := p.Export(context.Background(), reasoningTID, sessionWork)
	assert.Error(t, err, "no rollout")
	_, err = p.Export(context.Background(), "*", sessionWork)
	assert.Error(t, err, "a malformed id never widens the glob")

	// A rollout filed under one thread whose session_meta names another.
	raw, err := os.ReadFile(filepath.Join("testdata", "sessions", "with-reasoning.jsonl"))
	require.NoError(t, err)
	dir := filepath.Join(home, "sessions", "2026", "11", "19")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-x-"+compactionTID+".jsonl"), raw, 0o600))
	_, err = p.Export(context.Background(), compactionTID, sessionWork)
	assert.Error(t, err, "the rollout must belong to the requested thread")

	broken, err := NewSessionPortability(SessionPortabilityOptions{Env: []string{"CODEX_HOME=" + home},
		HarnessVersion: func(context.Context) (string, error) { return "", errors.New("codex missing") }})
	require.NoError(t, err)
	installSessionRollout(t, home, "with-reasoning.jsonl", reasoningTID)
	_, err = broken.Export(context.Background(), reasoningTID, sessionWork)
	assert.Error(t, err)
}
