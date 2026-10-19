package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFinalizeTask_ConcurrentSameTreeCannotRewritePublishedManifest(t *testing.T) {
	skipOnWindows(t)
	a, root := newTestArea(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parent := mkdirIn(t, root, "instances/p")
	writeFile(t, parent, "f", "same tree", 0o600)
	handle, err := a.SnapshotParent(ctx, uuid.New(), parent)
	require.NoError(t, err)
	winnerDir, loserDir := filepath.Join(root, "instances/winner"), filepath.Join(root, "instances/loser")
	require.NoError(t, a.ForkTask(ctx, handle, winnerDir))
	require.NoError(t, a.ForkTask(ctx, handle, loserDir))
	require.NoError(t, os.Mkdir(filepath.Join(winnerDir, "winner-empty"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(loserDir, "loser-empty"), 0o700))
	bin, err := a.git.Resolve()
	require.NoError(t, err)
	gate, release := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "release")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	// Equal trees, task IDs and timestamps would otherwise share a commit's
	// sidecar even though their empty directories differ. Pause the loser
	// after its initial ref check, before it can write that sidecar.
	base := "#!/bin/sh\nexport GIT_AUTHOR_DATE='2026-10-16T00:00:00Z'\nexport GIT_COMMITTER_DATE='2026-10-16T00:00:00Z'\n"
	paused := "for arg do\n if [ \"$arg\" = commit-tree ]; then\n touch " + quote(gate) + "\n while [ ! -e " + quote(release) + " ]; do sleep 0.01; done\n fi\ndone\n"
	makeArea := func(name, pause string) *WorkingArea {
		wrapper := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(wrapper, []byte(base+pause+"exec "+quote(bin)+" \"$@\"\n"), 0o700))
		area, err := NewWorkingArea(root, WithRunner(NewRunner(RunnerConfig{GitPath: wrapper, StateDir: t.TempDir()})))
		require.NoError(t, err)
		return area
	}
	winner, loser := makeArea("winner-git", ""), makeArea("loser-git", paused)
	id := uuid.New()
	done := make(chan struct{})
	var loserErr error
	go func() {
		defer close(done)
		_, loserErr = loser.FinalizeTask(ctx, handle, id, loserDir)
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { _, err := os.Stat(gate); return err == nil }, 3*time.Second, 10*time.Millisecond)
	ref, err := winner.FinalizeTask(ctx, handle, id, winnerDir)
	require.NoError(t, err)
	commit := gitOut(t, a, handle, "rev-parse", ref)
	backing, err := a.BackingDir(handle)
	require.NoError(t, err)
	before, err := os.ReadFile(manifestPath(backing, commit))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(release, nil, 0o600))
	<-done
	require.NoError(t, loserErr)
	assert.Equal(t, commit, gitOut(t, a, handle, "rev-parse", ref))
	after, err := os.ReadFile(manifestPath(backing, commit))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "a losing publication cannot alter a frozen commit's manifest")
}
