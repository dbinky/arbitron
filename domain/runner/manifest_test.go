// domain/runner/manifest_test.go
package runner_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManifestHash_DeterministicAndVersioned(t *testing.T) {
	es := []runner.ManifestEntry{
		{Path: "b.txt", Size: 1, Mode: 0o644, SHA256: "aa"},
		{Path: "a.txt", Size: 2, Mode: 0o755, SHA256: "bb"},
	}
	m1 := runner.NewManifest(1, es)
	m1again := runner.NewManifest(1, []runner.ManifestEntry{es[1], es[0]})
	m2 := runner.NewManifest(2, es)
	assert.Equal(t, m1.Hash, m1again.Hash, "entry order never changes the hash")
	assert.NotEqual(t, m1.Hash, m2.Hash, "identical content at a new version is a distinct manifest")
	assert.Equal(t, "a.txt", m1.Entries[0].Path, "entries are sorted")
	require.NoError(t, m1.Verify())
	m1.Entries[0].SHA256 = "tampered"
	assert.ErrorIs(t, m1.Verify(), runner.ErrCorruptCheckpoint)
	assert.Equal(t, int64(3), m2.TotalBytes())
}

func TestCleanRelPath(t *testing.T) {
	for _, ok := range []string{"a", "a/b.txt", ".git/HEAD", "dir/.hidden"} {
		got, err := runner.CleanRelPath(ok)
		require.NoError(t, err, ok)
		assert.Equal(t, ok, got)
	}
	for _, bad := range []string{"", "/abs", "../up", "a/../../x", "a//b", "a/./b", ".", "a\\b", "x\x00y"} {
		_, err := runner.CleanRelPath(bad)
		assert.Error(t, err, bad)
	}
}

func TestEntryFromInfo_TypesAndRejections(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o640))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "d"), 0o750))
	require.NoError(t, os.Symlink("f", filepath.Join(dir, "l")))
	fi, _ := os.Lstat(filepath.Join(dir, "f"))
	e, err := runner.EntryFromInfo("f", fi, "abc", "")
	require.NoError(t, err)
	assert.Equal(t, runner.ManifestEntry{Path: "f", Size: 1, Mode: 0o640, SHA256: "abc"}, e)
	di, _ := os.Lstat(filepath.Join(dir, "d"))
	e, err = runner.EntryFromInfo("d", di, "", "")
	require.NoError(t, err)
	assert.Equal(t, uint32(fs.ModeDir|0o750), e.Mode)
	li, _ := os.Lstat(filepath.Join(dir, "l"))
	e, err = runner.EntryFromInfo("l", li, "", "f")
	require.NoError(t, err)
	assert.Equal(t, "f", e.Symlink)
	assert.Equal(t, uint32(fs.ModeSymlink|0o777), e.Mode)
	_, err = runner.EntryFromInfo("p", manifestFakeInfo{mode: fs.ModeNamedPipe | 0o600}, "", "")
	assert.ErrorIs(t, err, runner.ErrUnsupportedFileType)
	_, err = runner.EntryFromInfo("s", manifestFakeInfo{mode: fs.ModeSocket | 0o600}, "", "")
	assert.ErrorIs(t, err, runner.ErrUnsupportedFileType)
	_, err = runner.EntryFromInfo("dev", manifestFakeInfo{mode: fs.ModeDevice | 0o600}, "", "")
	assert.ErrorIs(t, err, runner.ErrUnsupportedFileType)
}

func TestDiffManifests(t *testing.T) {
	from := []runner.ManifestEntry{
		{Path: "same", Size: 1, Mode: 0o644, SHA256: "s"},
		{Path: "chmod", Size: 1, Mode: 0o755, SHA256: "c"},
		{Path: "gone", Size: 1, Mode: 0o644, SHA256: "g"},
	}
	to := []runner.ManifestEntry{
		{Path: "same", Size: 1, Mode: 0o644, SHA256: "s"},
		{Path: "chmod", Size: 1, Mode: 0o644, SHA256: "c"},
		{Path: "new", Size: 2, Mode: 0o644, SHA256: "n"},
	}
	changed, deleted := runner.DiffManifests(from, to)
	paths := []string{}
	for _, c := range changed {
		paths = append(paths, c.Path)
	}
	assert.Equal(t, []string{"chmod", "new"}, paths)
	assert.Equal(t, []string{"gone"}, deleted)
	assert.True(t, runner.SameEntries(from, append([]runner.ManifestEntry{}, from[2], from[0], from[1])))
	assert.False(t, runner.SameEntries(from, to))
}

func TestCheckpointRef_RoundTrip(t *testing.T) {
	id := uuid.New()
	ref := runner.CheckpointRef{RunID: id, Version: 7, ManifestHash: strings.Repeat("ab", 32)}
	got, err := runner.ParseCheckpointRef(ref.String())
	require.NoError(t, err)
	assert.Equal(t, ref, got)
	assert.Equal(t, "", runner.CheckpointRef{}.String())
	zero, err := runner.ParseCheckpointRef("")
	require.NoError(t, err)
	assert.True(t, zero.IsZero())
	hash := strings.Repeat("a", 64)
	for _, bad := range []string{
		"garbage",
		id.String() + "@0:" + hash,
		id.String() + "@x:" + hash,
		id.String() + "@1:",
		id.String() + "@1:XYZ",
		id.String() + "@1:" + strings.Repeat("A", 64),
		uuid.Nil.String() + "@1:" + hash,
		"local:" + id.String() + "@1:" + hash,
	} {
		_, err := runner.ParseCheckpointRef(bad)
		assert.Error(t, err, bad)
	}
}

func TestCheckpointCandidate_ValidateRejectsMissingEvidence(t *testing.T) {
	run := uuid.New()
	hash := strings.Repeat("c", 64)
	job := runner.Fence{JobID: uuid.New(), RunnerID: "builtin", Generation: 1}
	res := engine.ResourceFenceRef{Key: "run:" + run.String(), Owner: "builtin", Generation: 1, Token: uuid.New()}
	good := runner.CheckpointCandidate{
		Ref:  runner.CheckpointRef{RunID: run, Version: 2, ManifestHash: hash},
		Base: runner.CheckpointRef{RunID: run, Version: 1, ManifestHash: strings.Repeat("b", 64)},
		Job:  &job, Resource: res, ArchivePath: "/ck/v2.tar.gz", SizeBytes: 10,
	}
	require.NoError(t, good.ValidatePrepared())

	mutate := map[string]func(c *runner.CheckpointCandidate){
		"nil job":           func(c *runner.CheckpointCandidate) { c.Job = nil },
		"zero job id":       func(c *runner.CheckpointCandidate) { j := job; j.JobID = uuid.Nil; c.Job = &j },
		"no runner":         func(c *runner.CheckpointCandidate) { j := job; j.RunnerID = ""; c.Job = &j },
		"zero generation":   func(c *runner.CheckpointCandidate) { j := job; j.Generation = 0; c.Job = &j },
		"zero resource":     func(c *runner.CheckpointCandidate) { c.Resource = engine.ResourceFenceRef{} },
		"zero token":        func(c *runner.CheckpointCandidate) { c.Resource.Token = uuid.Nil },
		"no archive":        func(c *runner.CheckpointCandidate) { c.ArchivePath = "" },
		"bad hash":          func(c *runner.CheckpointCandidate) { c.Ref.ManifestHash = "abc" },
		"version not after": func(c *runner.CheckpointCandidate) { c.Ref.Version = 1 },
		"base other run":    func(c *runner.CheckpointCandidate) { c.Base.RunID = uuid.New() },
	}
	for name, m := range mutate {
		c := good
		m(&c)
		assert.ErrorIs(t, c.ValidatePrepared(), runner.ErrInvalidCandidate, name)
	}

	boot := runner.CheckpointCandidate{Ref: runner.CheckpointRef{RunID: run, Version: 1, ManifestHash: hash}, ArchivePath: "/ck/v1.tar.gz"}
	require.NoError(t, boot.ValidateBootstrap())
	withJob := boot
	withJob.Job = &job
	assert.ErrorIs(t, withJob.ValidateBootstrap(), runner.ErrInvalidCandidate, "bootstrap never carries a job")
	withRes := boot
	withRes.Resource = res
	assert.ErrorIs(t, withRes.ValidateBootstrap(), runner.ErrInvalidCandidate, "bootstrap never carries a resource")
}

func TestPromisorErrorIsAlsoUnsupportedGit(t *testing.T) {
	err := fmt.Errorf("%w: %w: 1 object", runner.ErrUnsupportedGit, runner.ErrPromisorObjectsMissing)
	assert.ErrorIs(t, err, runner.ErrUnsupportedGit)
	assert.ErrorIs(t, err, runner.ErrPromisorObjectsMissing)
	assert.NotErrorIs(t, runner.ErrUnsupportedGit, runner.ErrPromisorObjectsMissing, "other git causes keep the visible-failure path")
}

type manifestFakeInfo struct{ mode fs.FileMode }

func (f manifestFakeInfo) Name() string       { return "x" }
func (f manifestFakeInfo) Size() int64        { return 0 }
func (f manifestFakeInfo) Mode() fs.FileMode  { return f.mode }
func (f manifestFakeInfo) ModTime() time.Time { return time.Time{} }
func (f manifestFakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f manifestFakeInfo) Sys() any           { return nil }
