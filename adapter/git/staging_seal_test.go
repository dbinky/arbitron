package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// plantDrivers rewrites the staging repository's metadata as a hostile
// resolver agent could: filter and merge drivers that leave a marker file when
// run, an include, worktree config and attributes that select the drivers.
func plantDrivers(t *testing.T, staging, marker string) {
	t.Helper()
	gitDir := filepath.Join(staging, ".git")
	cmd := "touch " + marker
	evil := filepath.Join(t.TempDir(), "evil.cfg")
	require.NoError(t, os.WriteFile(evil, []byte("[filter \"inc\"]\n\tclean = "+cmd+"; cat\n"), 0o600))
	cfg, err := os.ReadFile(filepath.Join(gitDir, "config"))
	require.NoError(t, err)
	cfg = append(cfg, []byte("[include]\n\tpath = "+evil+"\n"+
		"[extensions]\n\tworktreeConfig = true\n"+
		"[filter \"pwn\"]\n\tclean = "+cmd+"; cat\n\tsmudge = "+cmd+"; cat\n\trequired = true\n"+
		"[merge \"pwn\"]\n\tdriver = "+cmd+"; false\n")...)
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config"), cfg, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "config.worktree"), []byte("[filter \"pwn\"]\n\tclean = "+cmd+"; cat\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "info", "attributes"), []byte("* filter=pwn merge=pwn\n"), 0o600))
}

func TestGather_ServerStagingStepsNeverRunResolverPlantedDrivers(t *testing.T) {
	skipOnWindows(t)
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "c.txt", "base\n", 0o644) })
	refA := f.task(func(d string) { writeFile(t, d, "c.txt", "from a\n", 0o644) })
	refB := f.task(func(d string) { writeFile(t, d, "c.txt", "from b\n", 0o644) })
	refC := f.task(func(d string) { writeFile(t, d, "c.txt", "from c\n", 0o644) })
	ctx := context.Background()
	h := f.begin()
	_, err := f.a.MergeTask(ctx, h, refA)
	require.NoError(t, err)
	out, err := f.a.MergeTask(ctx, h, refB)
	require.NoError(t, err)
	require.False(t, out.Clean)

	// The resolver's turn: resolve, mark resolved, then plant the drivers.
	writeFile(t, h.StagingDir, "c.txt", "from a and b\n", 0o644)
	author(t, h.StagingDir, "add", "c.txt")
	marker := filepath.Join(t.TempDir(), "driver-ran")
	plantDrivers(t, h.StagingDir, marker)
	// A modified file makes "git add -A" in CommitResolution clean it.
	writeFile(t, h.StagingDir, "c.txt", "from a and b, edited\n", 0o644)
	author(t, h.StagingDir, "-c", "filter.pwn.clean=cat", "add", "c.txt")

	require.NoError(t, f.a.CommitResolution(ctx, h, "resolve c.txt"))
	assert.NoFileExists(t, marker, "CommitResolution ran a planted filter")

	plantDrivers(t, h.StagingDir, marker)
	out, err = f.a.MergeTask(ctx, h, refC)
	require.NoError(t, err)
	assert.False(t, out.Clean, "the built-in text merge (not the planted driver) reports the conflict")
	assert.NoFileExists(t, marker, "MergeTask ran a planted merge driver or filter")

	cfg, err := os.ReadFile(filepath.Join(h.StagingDir, ".git", "config"))
	require.NoError(t, err)
	for _, s := range []string{"pwn", "include", "extensions"} {
		assert.NotContains(t, strings.ToLower(string(cfg)), s)
	}
	assert.NoFileExists(t, filepath.Join(h.StagingDir, ".git", "config.worktree"))
	attrs, err := os.ReadFile(filepath.Join(h.StagingDir, ".git", "info", "attributes"))
	require.NoError(t, err)
	assert.Equal(t, backingAttributes, string(attrs))
}

func TestStagingConfig_KeepsOnlyProbedFilesystemKeys(t *testing.T) {
	got := string(stagingConfig([]byte("[core]\n\tignorecase = true\n\tprecomposeunicode = false\n\thooksPath = /x\n\tbare = true\n" +
		"[CORE]\n\tignorecase = false\n[filter \"x\"]\n\tclean = rm -rf /\n")))
	assert.Equal(t, "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n"+
		"\tignorecase = true\n\tprecomposeunicode = false\n", got)
}

func TestSealStaging_RefusesANonDirectoryGitDirAndIgnoresAMissingOne(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, sealStaging(dir), "no .git: nothing to seal")
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /elsewhere\n"), 0o600))
	require.Error(t, sealStaging(dir))
}
