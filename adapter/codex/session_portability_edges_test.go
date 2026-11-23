package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionPortability_MissingVersionOrDiskFailureFailsClosed(t *testing.T) {
	home := t.TempDir()
	installSessionRollout(t, home, "with-reasoning.jsonl", reasoningTID)
	b, err := newCodexPortability(t, home, "0.170.0").Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)
	t.Run("missing versions", func(t *testing.T) {
		missing := b
		missing.HarnessVersion = ""
		target := t.TempDir()
		_, err := newCodexPortability(t, target, "").Import(context.Background(), missing, sessionWork)
		assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
		_, err = findRollout(target, reasoningTID)
		assert.Error(t, err, "no rollout installed without version evidence")
	})
	t.Run("disk failure", func(t *testing.T) {
		target := filepath.Join(t.TempDir(), "not-a-directory")
		require.NoError(t, os.WriteFile(target, []byte("occupied"), 0600))
		_, err := newCodexPortability(t, target, "0.170.0").Import(context.Background(), b, sessionWork)
		require.Error(t, err)
		var pathErr *os.PathError
		require.ErrorAs(t, err, &pathErr, "reach the filesystem sink with a valid bundle")
		got, err := os.ReadFile(target)
		require.NoError(t, err)
		assert.Equal(t, "occupied", string(got))
	})
}

func TestSessionPortability_UnsupportedNativeStateRequiresRebuild(t *testing.T) {
	home := t.TempDir()
	raw := installSessionRollout(t, home, "with-reasoning.jsonl", reasoningTID)
	b, err := newCodexPortability(t, home, "0.170.0").Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)
	for name, extra := range map[string]string{
		"compaction record":           `{"type":"compaction","payload":{}}`,
		"escaped replacement history": `{"type":"event_msg","payload":{"replacement_\u0068istory":[]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			b.Data = append(append([]byte(nil), raw...), []byte(extra+"\n")...)
			b.Checksum = engine.BundleChecksum(b.Data)
			target := t.TempDir()
			_, err := newCodexPortability(t, target, "0.170.0").Import(context.Background(), b, sessionWork)
			assert.ErrorIs(t, err, engine.ErrBundleRequiresRebuild)
			matches, err := filepath.Glob(filepath.Join(target, "sessions", "*", "*", "*", "*.jsonl"))
			require.NoError(t, err)
			assert.Empty(t, matches)
		})
	}
}

func TestSessionPortability_ForeignHomeAndReasoningLossAreVisible(t *testing.T) {
	origin, target := t.TempDir(), t.TempDir()
	installSessionRollout(t, origin, "with-reasoning.jsonl", reasoningTID)
	b, err := newCodexPortability(t, origin, "0.170.0").Export(context.Background(), reasoningTID, sessionWork)
	require.NoError(t, err)
	importer := newCodexPortability(t, target, "0.170.0")
	_, err = importer.Import(context.Background(), b, sessionWork)
	require.NoError(t, err)
	_, got := importedRollout(t, target, reasoningTID)
	assert.Contains(t, string(got), `"creator_account_id":"acct-A"`, "never forge target account identity")
	assert.Contains(t, string(got), `"account_scoped":true`)
	assert.Contains(t, string(got), "TANGERINE")
	assert.Contains(t, string(got), `"effort":"low"`)
	assert.False(t, strings.Contains(string(got), "encrypted_content"))
	assert.True(t, importer.AlwaysLossy(), "mover must expose a loss notice, even after successful import")
}
