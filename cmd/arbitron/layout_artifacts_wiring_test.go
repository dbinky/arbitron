package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// PR #85 Must 5: the production renderer carries the artifact lifecycle seam,
// and starting the layout stack runs the crash-leftover reconcile.
func TestLayoutStack_StartReconcilesUnreferencedCaptures(t *testing.T) {
	db := layoutE2EOpenDB(t)
	root := t.TempDir()
	cfg := layoutE2EConfig(root, 3)
	ls, err := newLayoutStack(cfg, db, filesystem.NewWorkflowRepo(root), adaptsqlite.NewPublishedWorkflowRepo(db), adaptsqlite.NewWorkflowIdentityRepo(db), layoutE2EDiscardLogger())
	require.NoError(t, err)
	t.Cleanup(ls.stop)
	require.Implements(t, (*layout.ArtifactReleaser)(nil), ls.renderer)
	require.Implements(t, (*layout.ArtifactReconciler)(nil), ls.renderer)

	dir := layoutArtifactDir(cfg)
	plant := func(rev string, age time.Duration) string {
		capture := filepath.Join(dir, rev, "1", "capture-test")
		require.NoError(t, os.MkdirAll(capture, 0o700))
		file := filepath.Join(capture, "overview.png")
		require.NoError(t, os.WriteFile(file, []byte("png"), 0o600))
		old := time.Now().Add(-age)
		for _, p := range []string{file, capture, filepath.Dir(capture), filepath.Dir(filepath.Dir(capture))} {
			require.NoError(t, os.Chtimes(p, old, old))
		}
		return file
	}
	orphan := plant("0190aaaa-orphan", 3*time.Hour)
	live := plant("0190bbbb-live", 0)

	ls.start(layoutE2EDiscardLogger())
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "0190aaaa-orphan")); return os.IsNotExist(err) }, 10*time.Second, 20*time.Millisecond)
	assert.NoFileExists(t, orphan)
	assert.FileExists(t, live, "a capture inside the grace period may belong to another live process")
}

// PR #85 Should 6: every renderer construction with NoSandbox warns about the
// residual risk; the sandboxed default stays quiet.
func TestNewLayoutRenderReviewer_WarnsWhenSandboxDisabled(t *testing.T) {
	for _, noSandbox := range []bool{true, false} {
		var buf bytes.Buffer
		cfg := renderWiringConfig(t)
		cfg.Layout.BrowserPath = filepath.Join(t.TempDir(), "no-such-chromium")
		cfg.Layout.BrowserNoSandbox = noSandbox
		_, _, closeFn, err := newLayoutRenderReviewer(cfg, slog.New(slog.NewTextHandler(&buf, nil)))
		require.NoError(t, err)
		closeFn()
		if noSandbox {
			assert.Contains(t, buf.String(), "level=WARN")
			assert.Contains(t, buf.String(), "WITHOUT the Chromium sandbox")
			assert.Contains(t, buf.String(), "credentials")
		} else {
			assert.NotContains(t, buf.String(), "WITHOUT the Chromium sandbox")
		}
	}
}
