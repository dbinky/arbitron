package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func importedRollout(t *testing.T, home, tid string) (string, []byte) {
	t.Helper()
	path, err := findRollout(home, tid)
	require.NoError(t, err, "the imported rollout lands where codex (and the verifier) glob for it")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return path, b
}

func TestCodexImport_AlwaysStripsReasoning(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	raw := installSessionRollout(t, homeA, "with-reasoning.jsonl", reasoningTID)
	b, err := newCodexPortability(t, homeA, "0.170.0").Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)

	sid, err := newCodexPortability(t, homeB, "0.170.0").Import(context.Background(), b, sessionWork)
	require.NoError(t, err)
	assert.Equal(t, reasoningTID, sid)
	path, got := importedRollout(t, homeB, reasoningTID)
	assert.Equal(t, filepath.Join(homeB, "sessions", "2026", "11", "20"), filepath.Dir(path), "dated by the importer's clock")
	assert.True(t, strings.HasPrefix(filepath.Base(path), "rollout-2026-11-20T08-30-00-"))

	assert.NotContains(t, string(got), `"type":"reasoning"`, "reasoning items are dropped whole")
	assert.NotContains(t, string(got), "encrypted_content", "no org-bound encrypted content survives anywhere")
	assert.Contains(t, string(got), "TANGERINE", "visible history is preserved")
	assert.Contains(t, string(got), `"call_id":"call_1"`, "non-reasoning items stay, minus encrypted_content")
	assert.Equal(t, countLines(raw)-1, countLines(got), "exactly one reasoning record was removed")

	ev, err := readRollout(path)
	require.NoError(t, err)
	assert.Equal(t, reasoningTID, ev.ThreadID)
	assert.Equal(t, []string{"gpt-5.6-luna"}, ev.TurnModels)
}

func TestCodexImport_SameHomeAlsoStrips(t *testing.T) {
	home := t.TempDir()
	installSessionRollout(t, home, "with-reasoning.jsonl", reasoningTID)
	p := newCodexPortability(t, home, "0.170.0")
	b, err := p.Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)
	_, err = p.Import(context.Background(), b, sessionWork)
	require.NoError(t, err)
	matches, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*-"+reasoningTID+".jsonl"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "an existing rollout for the thread is replaced, never duplicated")
	assert.Equal(t, filepath.Join(home, "sessions", "2026", "11", "19"), filepath.Dir(matches[0]))
	got, err := os.ReadFile(matches[0])
	require.NoError(t, err)
	assert.NotContains(t, string(got), "encrypted_content", "stripping is unconditional (no account fingerprint, RR-14)")
}

func TestCodexImport_CompactionRequiresRebuild(t *testing.T) {
	homeA, homeB := t.TempDir(), t.TempDir()
	installSessionRollout(t, homeA, "with-compaction.jsonl", compactionTID)
	b, err := newCodexPortability(t, homeA, "0.170.0").Export(context.Background(), compactionTID, sessionWork)
	require.NoError(t, err)
	_, err = newCodexPortability(t, homeB, "0.170.0").Import(context.Background(), b, sessionWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
	matches, _ := filepath.Glob(filepath.Join(homeB, "sessions", "*", "*", "*", "*.jsonl"))
	assert.Empty(t, matches, "nothing is written on refusal")
}

func TestCodexImport_VersionSkewAndWorkDirRequireRebuild(t *testing.T) {
	homeA := t.TempDir()
	installSessionRollout(t, homeA, "with-reasoning.jsonl", reasoningTID)
	b, err := newCodexPortability(t, homeA, "0.170.0").Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)
	_, err = newCodexPortability(t, t.TempDir(), "0.171.0").Import(context.Background(), b, sessionWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild, "version skew")
	_, err = newCodexPortability(t, t.TempDir(), "0.170.0").Import(context.Background(), b, "/srv/arbitron/workspaces/run-9")
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild, "identical workspace paths only")
}

func TestCodexImport_CorruptOrMalformedRequiresRebuild(t *testing.T) {
	home := t.TempDir()
	installSessionRollout(t, home, "with-reasoning.jsonl", reasoningTID)
	b, err := newCodexPortability(t, home, "0.170.0").Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)
	corrupt := b
	corrupt.Data = append([]byte{}, b.Data...)
	corrupt.Data[0] = 'X'
	_, err = newCodexPortability(t, t.TempDir(), "0.170.0").Import(context.Background(), corrupt, sessionWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
	assert.ErrorIs(t, err, engine.ErrBundleChecksum)

	raw, err := os.ReadFile(filepath.Join("testdata", "sessions", "malformed.jsonl"))
	require.NoError(t, err)
	bad := engine.SessionBundle{Backend: "codex", SessionID: malformedTID, HarnessVersion: "0.170.0", OriginWorkDir: sessionWork,
		Data: raw, Checksum: engine.BundleChecksum(raw)}
	_, err = newCodexPortability(t, t.TempDir(), "0.170.0").Import(context.Background(), bad, sessionWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)

	wrongThread := b
	wrongThread.SessionID = compactionTID
	_, err = newCodexPortability(t, t.TempDir(), "0.170.0").Import(context.Background(), wrongThread, sessionWork)
	assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild, "session_meta must name the bundle's thread")

	claude := b
	claude.Backend = "claude-code"
	_, err = newCodexPortability(t, t.TempDir(), "0.170.0").Import(context.Background(), claude, sessionWork)
	assert.Error(t, err)
}

func countLines(b []byte) int {
	n := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) > 0 {
			n++
		}
	}
	return n
}
