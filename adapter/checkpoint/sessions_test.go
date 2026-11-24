package checkpoint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bundle(text string) engine.SessionBundle {
	data := []byte(text)
	return engine.SessionBundle{Backend: "claude-code", SessionID: "sess-1", HarnessVersion: "2.1.0",
		OriginWorkDir: "/srv/ws/run-1", Data: data, Checksum: engine.BundleChecksum(data)}
}

func key(run, inv uuid.UUID, turn int) engine.SessionKey {
	return engine.SessionKey{RunID: run, InvocationID: inv, TurnIndex: turn}
}

// Happy: Put/Get round trip, readable by a second store over the same dir (restart).
func TestSessionStore_PutGetRoundTripAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSessionStore(dir, 1)
	require.NoError(t, err)
	run, inv := uuid.New(), uuid.New()
	b := bundle("turn three transcript")
	ref, err := s.Put(context.Background(), key(run, inv, 3), b)
	require.NoError(t, err)
	assert.Equal(t, "session:"+run.String()+"/"+inv.String()+"/3/"+b.Checksum, ref)

	restarted, err := NewSessionStore(dir, 1)
	require.NoError(t, err)
	got, err := restarted.Get(context.Background(), ref)
	require.NoError(t, err)
	assert.Equal(t, b, got)
}

// Success: prune keeps the committed bundle plus retain-1 older turns, removes
// the orphan of an uncommitted turn and never touches another invocation.
func TestSessionStore_PruneKeepsCommittedPairAndRetention(t *testing.T) {
	ctx := context.Background()
	run, inv, other := uuid.New(), uuid.New(), uuid.New()
	for _, retain := range []int{1, 2} {
		s, err := NewSessionStore(t.TempDir(), retain)
		require.NoError(t, err)
		refs := map[int]string{}
		for turn := 1; turn <= 3; turn++ {
			refs[turn], err = s.Put(ctx, key(run, inv, turn), bundle("turn "+string(rune('0'+turn))))
			require.NoError(t, err)
		}
		orphan, err := s.Put(ctx, key(run, inv, 4), bundle("uncommitted turn 4"))
		require.NoError(t, err)
		otherRef, err := s.Put(ctx, key(run, other, 1), bundle("other visit"))
		require.NoError(t, err)

		require.NoError(t, s.Prune(ctx, run, inv, refs[3]))
		_, err = s.Get(ctx, refs[3])
		assert.NoError(t, err, "the committed checkpoint is kept")
		_, err = s.Get(ctx, orphan)
		assert.Error(t, err, "the orphan of the crashed turn is pruned")
		_, err = s.Get(ctx, refs[2])
		if retain == 2 {
			assert.NoError(t, err, "retention 2 keeps one older turn")
		} else {
			assert.Error(t, err)
		}
		_, err = s.Get(ctx, refs[1])
		assert.Error(t, err)
		_, err = s.Get(ctx, otherRef)
		assert.NoError(t, err, "other invocations are untouched")
	}
}

// Failure: bad checksum, bad key, oversized bundle, malformed/traversal refs and a
// symlinked invocation directory are refused with nothing installed.
func TestSessionStore_RejectsBadInputsWithoutPartialInstall(t *testing.T) {
	ctx := context.Background()
	_, err := NewSessionStore(t.TempDir(), 0)
	assert.Error(t, err)
	_, err = NewSessionStore("", 1)
	assert.Error(t, err)

	dir := t.TempDir()
	s, err := NewSessionStore(dir, 1)
	require.NoError(t, err)
	b := bundle("x")
	b.Checksum = engine.BundleChecksum([]byte("y"))
	_, err = s.Put(ctx, key(uuid.New(), uuid.New(), 1), b)
	assert.ErrorIs(t, err, engine.ErrBundleChecksum)
	_, err = s.Put(ctx, key(uuid.New(), uuid.New(), 0), bundle("x"))
	assert.Error(t, err, "turn indexes start at 1")
	_, err = s.Put(ctx, key(uuid.Nil, uuid.New(), 1), bundle("x"))
	assert.Error(t, err, "run id required")

	s.SetMaxBundleBytes(4)
	_, err = s.Put(ctx, key(uuid.New(), uuid.New(), 1), bundle("too large"))
	assert.ErrorIs(t, err, ErrSessionBundleTooLarge)
	s.SetMaxBundleBytes(DefaultMaxSessionBundleBytes)

	for _, ref := range []string{"nope", "session:../../etc/passwd", "session:" + uuid.NewString() + "/" + uuid.NewString() + "/1/ZZ",
		"session:" + uuid.NewString() + "/" + uuid.NewString() + "/x/" + bundle("a").Checksum,
		"session:" + uuid.NewString() + "/" + uuid.NewString() + "/1/" + strings.ToUpper(bundle("a").Checksum)} {
		_, err := s.Get(ctx, ref)
		assert.Error(t, err, ref)
	}

	run, inv := uuid.New(), uuid.New()
	outside := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, run.String()), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, run.String(), inv.String())))
	_, err = s.Put(ctx, key(run, inv, 1), bundle("via symlink"))
	assert.Error(t, err, "a symlinked invocation directory is refused")
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing written through the symlink")
}

// Error: a write failure (read-only invocation dir standing in for disk full)
// leaves the previously stored bundle readable and no temp file behind; a
// tampered data file is detected on Get.
func TestSessionStore_WriteFailureKeepsPriorBundleAndTamperIsDetected(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewSessionStore(dir, 1)
	require.NoError(t, err)
	run, inv := uuid.New(), uuid.New()
	prior, err := s.Put(ctx, key(run, inv, 1), bundle("committed turn 1"))
	require.NoError(t, err)
	invDir := filepath.Join(dir, run.String(), inv.String())
	require.NoError(t, os.Chmod(invDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(invDir, 0o700) })

	_, err = s.Put(ctx, key(run, inv, 2), bundle("turn 2"))
	require.Error(t, err)
	got, err := s.Get(ctx, prior)
	require.NoError(t, err, "the prior committed bundle is intact")
	assert.Equal(t, []byte("committed turn 1"), got.Data)
	require.NoError(t, os.Chmod(invDir, 0o700))
	entries, err := os.ReadDir(invDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), ".tmp-"), "no temp file left: %s", e.Name())
	}

	matches, err := filepath.Glob(filepath.Join(invDir, "*.data"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
	require.NoError(t, os.WriteFile(matches[0], []byte("tampered"), 0o600))
	_, err = s.Get(ctx, prior)
	assert.ErrorIs(t, err, engine.ErrBundleChecksum)
}

// Edge: prune refuses an unknown or foreign keepRef (deletes nothing); startup GC
// keeps live (committed or in-flight) refs and young orphans, reclaims only
// expired orphans and stale temp files.
func TestSessionStore_PruneRefusalAndGarbageCollection(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewSessionStore(dir, 1)
	require.NoError(t, err)
	run, inv := uuid.New(), uuid.New()
	ref, err := s.Put(ctx, key(run, inv, 1), bundle("one"))
	require.NoError(t, err)
	assert.Error(t, s.Prune(ctx, run, inv, strings.Replace(ref, "/1/", "/2/", 1)), "never prune when the committed bundle is not stored")
	assert.Error(t, s.Prune(ctx, uuid.New(), inv, ref), "keepRef must belong to the pruned invocation")
	_, err = s.Get(ctx, ref)
	assert.NoError(t, err, "a refused prune deletes nothing")

	inFlight, err := s.Put(ctx, key(run, inv, 2), bundle("in flight"))
	require.NoError(t, err)
	orphan, err := s.Put(ctx, key(run, uuid.New(), 1), bundle("orphan"))
	require.NoError(t, err)
	young, err := s.Put(ctx, key(run, uuid.New(), 1), bundle("young orphan"))
	require.NoError(t, err)
	tmp := filepath.Join(dir, run.String(), inv.String(), ".tmp-stale")
	require.NoError(t, os.WriteFile(tmp, []byte("x"), 0o600))
	old := time.Now().Add(-2 * time.Hour)
	for _, r := range []string{ref, inFlight, orphan} {
		p, _ := s.refPaths(t, r)
		require.NoError(t, os.Chtimes(p, old, old))
	}
	require.NoError(t, os.Chtimes(tmp, old, old))

	live := map[string]bool{ref: true, inFlight: true}
	n, err := s.CollectGarbage(ctx, func(r string) bool { return live[r] }, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 1, n, "exactly the expired orphan bundle is reclaimed")
	for _, r := range []string{ref, inFlight, young} {
		_, err := s.Get(ctx, r)
		assert.NoError(t, err, r)
	}
	_, err = s.Get(ctx, orphan)
	assert.Error(t, err)
	_, err = os.Stat(tmp)
	assert.True(t, os.IsNotExist(err), "stale temp file reclaimed")
}

// refPaths is a test helper exposing the meta path of a ref.
func (s *SessionStore) refPaths(t *testing.T, ref string) (meta, data string) {
	t.Helper()
	r, err := parseSessionRef(ref)
	require.NoError(t, err)
	d, m := s.paths(r)
	return m, d
}
