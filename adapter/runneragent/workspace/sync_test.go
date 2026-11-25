// adapter/runneragent/workspace/sync_test.go
package workspace_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/runneragent/copies"
	"github.com/arbitron/arbitron/adapter/runneragent/transfer/transfertest"
	"github.com/arbitron/arbitron/adapter/runneragent/workspace"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const canonicalRoot = "/srv/arbitron/workspaces"

type fixture struct {
	srv       *transfertest.Server
	sync      *workspace.Syncer
	copies    *copies.FileStore
	copiesDir string
	localRoot string
	runID     uuid.UUID
	workDir   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	seed := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(seed, "a.txt"), []byte("alpha"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(seed, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(seed, "src", "b.go"), []byte("package b"), 0o644))
	runID := uuid.New()
	srv := transfertest.NewServer(t.TempDir(), runID)
	_, err := srv.Seed(ctx, seed)
	require.NoError(t, err)

	f := &fixture{srv: srv, localRoot: t.TempDir(), copiesDir: filepath.Join(t.TempDir(), "copies"), runID: runID,
		workDir: canonicalRoot + "/instances/" + runID.String()}
	f.sync = f.restart(t)
	return f
}

// restart builds a fresh Syncer over the same on-disk copy records and local
// root, as a restarted runner process would.
func (f *fixture) restart(t *testing.T) *workspace.Syncer {
	t.Helper()
	store, err := copies.NewFileStore(f.copiesDir)
	require.NoError(t, err)
	f.copies = store
	s, err := workspace.NewSyncer(workspace.SyncerDeps{WorkspaceRoot: canonicalRoot, LocalRoot: f.localRoot, Transfer: f.srv, Copies: store})
	require.NoError(t, err)
	return s
}

func TestSyncer_FirstPullFetchesAllThenReusesTheCopy(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)

	local, err := f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(f.localRoot, "instances", f.runID.String()), local)
	assert.ElementsMatch(t, []string{"a.txt", "src/b.go"}, f.srv.Fetches())
	b, err := os.ReadFile(filepath.Join(local, "src", "b.go"))
	require.NoError(t, err)
	assert.Equal(t, "package b", string(b))

	f.srv.ResetCounters()
	_, err = f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	assert.Empty(t, f.srv.Fetches(), "matching generation + version + clean copy: zero pulls")
	assert.Zero(t, f.srv.ManifestCalls(), "not even the manifest is fetched")
}

func TestSyncer_PushSendsChangesAndDeletionsThenNextPullIsFree(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	ws.PartBytes = 64
	local, err := f.sync.Pull(ctx, ws)
	require.NoError(t, err)

	require.NoError(t, f.sync.MarkDirty(ctx, ws))
	require.NoError(t, os.WriteFile(filepath.Join(local, "a.txt"), []byte(strings.Repeat("x", 3000)), 0o600))
	require.NoError(t, os.Chmod(filepath.Join(local, "a.txt"), 0o600))
	require.NoError(t, os.Remove(filepath.Join(local, "src", "b.go")))
	require.NoError(t, os.Symlink("a.txt", filepath.Join(local, "link")))

	ref, err := f.sync.Push(ctx, ws)
	require.NoError(t, err)
	assert.Equal(t, ws.CheckpointVersion+1, ref.Version)
	require.Equal(t, 1, f.srv.Commits())
	assert.Greater(t, f.srv.LastCommitParts(), 1, "a delta larger than PartBytes is split into parts")
	base, err := f.srv.LatestManifest(ctx)
	require.NoError(t, err)
	assert.Equal(t, ws.CheckpointVersion, base.Version, "the push only prepares; Latest moves in the validated commit")
	require.NoError(t, f.srv.Promote(ctx, ref))

	man, err := f.srv.LatestManifest(ctx)
	require.NoError(t, err)
	byPath := map[string]uint32{}
	for _, e := range man.Entries {
		byPath[e.Path] = e.Mode
	}
	assert.NotContains(t, byPath, "src/b.go", "the deletion reached the server checkpoint")
	assert.Equal(t, uint32(0o600), byPath["a.txt"], "chmod reached the server checkpoint")
	assert.Contains(t, byPath, "link", "the symlink reached the server checkpoint")

	next, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	f.srv.ResetCounters()
	_, err = f.sync.Pull(ctx, next)
	require.NoError(t, err)
	assert.Empty(t, f.srv.Fetches(), "after our own push is committed the copy equals the new checkpoint: zero pulls")
	assert.Zero(t, f.srv.ManifestCalls())
}

func TestSyncer_PushPreparesWithoutClaimingCommittedVersion(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	local, err := f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	require.NoError(t, f.sync.MarkDirty(ctx, ws))
	require.NoError(t, os.WriteFile(filepath.Join(local, "a.txt"), []byte("beta"), 0o644))

	ref, err := f.sync.Push(ctx, ws)
	require.NoError(t, err)
	prepared, found, err := f.srv.PreparedFor(ctx)
	require.NoError(t, err)
	require.True(t, found, "the push prepared exactly this job's candidate")
	assert.Equal(t, ref, prepared)
	latest, err := f.srv.LatestManifest(ctx)
	require.NoError(t, err)
	assert.Equal(t, ws.CheckpointVersion, latest.Version, "Latest stays on the pinned base after the push")

	st, ok, err := f.copies.Get(ctx, f.runID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ws.CheckpointVersion, st.CheckpointVersion, "the copy never records the prepared version as committed")
	assert.Equal(t, ws.ManifestHash, st.ManifestHash)
	assert.True(t, st.Dirty, "until the commit is acknowledged the copy is not a reusable committed base")
	require.NotNil(t, st.Prepared, "the prepared receipt is recorded")
	assert.Equal(t, ref, st.Prepared.Ref)
	assert.Equal(t, ws.BaseRef, st.Prepared.BaseRef)
	assert.Equal(t, ws.FenceKey, st.Prepared.FenceKey)
	assert.Equal(t, ws.FenceGeneration, st.Prepared.FenceGeneration)
	assert.Equal(t, ws.FenceToken, st.Prepared.FenceToken)

	// The simulated validated commit transaction (P3 T5) promotes the candidate.
	require.NoError(t, f.srv.Promote(ctx, ref))
	latest, err = f.srv.LatestManifest(ctx)
	require.NoError(t, err)
	assert.Equal(t, ref.Version, latest.Version, "Latest becomes the candidate only after the validated commit")

	next, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	f.srv.ResetCounters()
	_, err = f.sync.Pull(ctx, next)
	require.NoError(t, err)
	assert.Empty(t, f.srv.Fetches())
	assert.Zero(t, f.srv.ManifestCalls(), "the committed candidate is exactly the local tree: adopted with no transfer")
	st, _, err = f.copies.Get(ctx, f.runID)
	require.NoError(t, err)
	assert.Equal(t, ref.Version, st.CheckpointVersion)
	assert.Equal(t, ref.ManifestHash, st.ManifestHash)
	assert.False(t, st.Dirty)
	assert.Nil(t, st.Prepared)
}

func TestSyncer_RestartDiscardsUncommittedLocalCandidate(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	local, err := f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	require.NoError(t, f.sync.MarkDirty(ctx, ws))
	require.NoError(t, os.WriteFile(filepath.Join(local, "a.txt"), []byte("beta"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(local, "new.txt"), []byte("n"), 0o644))
	_, err = f.sync.Push(ctx, ws)
	require.NoError(t, err)

	// The server crashes, or rejects the executor output, before its commit
	// transaction: the candidate is never promoted. The runner restarts.
	restarted := f.restart(t)
	again, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	assert.Equal(t, ws.BaseRef, again.BaseRef, "the server still names the previous committed version")
	f.srv.ResetCounters()
	_, err = restarted.Pull(ctx, again)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt"}, f.srv.Fetches(), "only the file the uncommitted candidate changed is restored")
	b, err := os.ReadFile(filepath.Join(local, "a.txt"))
	require.NoError(t, err)
	assert.Equal(t, "alpha", string(b))
	_, err = os.Stat(filepath.Join(local, "new.txt"))
	assert.True(t, os.IsNotExist(err), "the uncommitted candidate's new file is discarded")

	st, ok, err := f.copies.Get(ctx, f.runID)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ws.CheckpointVersion, st.CheckpointVersion)
	assert.False(t, st.Dirty)
	assert.Nil(t, st.Prepared, "the uncommitted receipt is discarded, never adopted")
}

func TestSyncer_DirtyCopyIsRestoredByFetchingOnlyDifferences(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	local, err := f.sync.Pull(ctx, ws)
	require.NoError(t, err)

	// An interrupted attempt edits a file and leaves junk, then never pushes.
	require.NoError(t, f.sync.MarkDirty(ctx, ws))
	require.NoError(t, os.WriteFile(filepath.Join(local, "a.txt"), []byte("partial"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(local, "junk.tmp"), []byte("j"), 0o644))

	f.srv.ResetCounters()
	_, err = f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt"}, f.srv.Fetches(), "only the differing file is fetched")
	_, err = os.Stat(filepath.Join(local, "junk.tmp"))
	assert.True(t, os.IsNotExist(err), "files absent from the checkpoint are removed")
	b, _ := os.ReadFile(filepath.Join(local, "a.txt"))
	assert.Equal(t, "alpha", string(b), "the clean committed content is restored")
}

func TestSyncer_NewHomeGenerationReverifies(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	_, err = f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	f.srv.ResetCounters()
	ws.HomeGeneration = 2
	_, err = f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	assert.Equal(t, 1, f.srv.ManifestCalls(), "a different home generation re-verifies against the server manifest")
	assert.Empty(t, f.srv.Fetches(), "identical content is not re-downloaded")
}

func TestSyncer_InterruptedUploadNeverPublishes(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	ws.PartBytes = 64
	local, err := f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	require.NoError(t, f.sync.MarkDirty(ctx, ws))
	require.NoError(t, os.WriteFile(filepath.Join(local, "a.txt"), []byte(strings.Repeat("y", 2000)), 0o644))

	f.srv.FailPart(1, 100) // part 1 always fails
	_, err = f.sync.Push(ctx, ws)
	require.Error(t, err, "a dropped part fails the push; it is never reported as done")
	assert.Zero(t, f.srv.Commits())
	_, found, err := f.srv.PreparedFor(ctx)
	require.NoError(t, err)
	assert.False(t, found, "nothing is even prepared")
	latest, err := f.srv.LatestManifest(ctx)
	require.NoError(t, err)
	assert.Equal(t, ws.CheckpointVersion, latest.Version, "the server keeps the previous checkpoint")

	f.srv.ResetCounters()
	_, err = f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	assert.Equal(t, []string{"a.txt"}, f.srv.Fetches(), "the copy stayed dirty, so the next pull restores it")
}

func TestSyncer_RetriesAFailedPart(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	ws.PartBytes = 64
	local, err := f.sync.Pull(ctx, ws)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(local, "a.txt"), []byte(strings.Repeat("z", 2000)), 0o644))
	f.srv.FailPart(1, 1) // part 1 fails once
	_, err = f.sync.Push(ctx, ws)
	require.NoError(t, err, "a single dropped part is retried")
	assert.Equal(t, 1, f.srv.Commits())
}

func TestSyncer_RejectsWorkDirOutsideTheWorkspaceRoot(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, "/etc", 1)
	require.NoError(t, err)
	_, err = f.sync.Pull(ctx, ws)
	require.ErrorIs(t, err, workspace.ErrOutsideWorkspaceRoot)
	_, err = f.sync.LocalPath(canonicalRoot + "/../escape")
	require.ErrorIs(t, err, workspace.ErrOutsideWorkspaceRoot)
	_, err = f.sync.LocalPath(canonicalRoot)
	require.ErrorIs(t, err, workspace.ErrOutsideWorkspaceRoot, "the root itself is never a run workspace")
}

func TestSyncer_RefusesAManifestOtherThanTheAssignedOne(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	// A self-consistent job whose pinned base is not what the server serves.
	ws.ManifestHash = strings.Repeat("0", 64)
	ws.BaseRef = fmt.Sprintf("%s@%d:%s", ws.RunID, ws.CheckpointVersion, ws.ManifestHash)
	_, err = f.sync.Pull(ctx, ws)
	require.ErrorIs(t, err, workspace.ErrPullVerifyFailed)
}

func TestSyncer_RefusesAJobWithoutItsPinnedBaseOrClaim(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	ws, err := f.srv.Plan(ctx, f.workDir, 1)
	require.NoError(t, err)
	bad := ws
	bad.FenceToken = ""
	_, err = f.sync.Pull(ctx, bad)
	require.Error(t, err)
	assert.Zero(t, f.srv.ManifestCalls(), "incomplete scope is refused before any transfer")
	_, err = f.sync.Push(ctx, bad)
	require.Error(t, err)
	assert.Zero(t, f.srv.Commits())
}
