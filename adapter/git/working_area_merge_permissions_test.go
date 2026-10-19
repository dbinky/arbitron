package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chmodIn(t *testing.T, dir, rel string, mode os.FileMode) {
	t.Helper()
	require.NoError(t, os.Chmod(filepath.Join(dir, rel), mode))
}

func TestGather_PermissionDeltasUseOnlyMergedRefsInAuthorOrder(t *testing.T) {
	skipOnWindows(t)
	for _, eligible := range [][]int{{0}, {1}, {0, 1}, {}} {
		t.Run(fmtRefs(eligible), func(t *testing.T) {
			f := newGatherFixture(t, func(p string) {
				writeFile(t, p, "private/file", "base", 0o640)
				chmodIn(t, p, "private", 0o750)
				chmodIn(t, p, ".", 0o750)
			})
			// A parent-only change must survive an unchanged baseline mode in tasks.
			refs := []string{
				f.task(func(d string) { chmodIn(t, d, "private/file", 0o600) }),
				f.task(func(d string) { chmodIn(t, d, "private/file", 0o660) }),
				f.task(func(d string) { writeFile(t, d, "extra", "unchanged task modes", 0o644) }),
			}
			chmodIn(t, f.parent, "private", 0o710)
			chmodIn(t, f.parent, ".", 0o700)
			before := permissionModes(t, f.parent, []string{".", "private", "private/file"})
			h := f.begin()
			for _, i := range append(append([]int{}, eligible...), 2) {
				out, err := f.a.MergeTask(t.Context(), h, refs[i])
				require.NoError(t, err)
				require.True(t, out.Clean)
			}
			// No task workspace is a replay input; only finalized manifests are.
			for _, ref := range refs {
				commit, err := f.a.ResolveRef(t.Context(), f.handle, ref)
				require.NoError(t, err)
				backing, err := f.a.BackingDir(f.handle)
				require.NoError(t, err)
				m, err := readManifest(backing, commit)
				require.NoError(t, err)
				require.NoError(t, removeAreaTree(m.Root))
			}
			restarted, err := NewWorkingArea(f.root)
			require.NoError(t, err)
			require.NoError(t, restarted.Apply(t.Context(), h))
			want := uint32(0o640)
			if len(eligible) > 0 {
				want = []uint32{0o600, 0o660}[eligible[len(eligible)-1]]
			}
			assert.Equal(t, map[string]uint32{".": 0o700, "private": 0o710, "private/file": want}, permissionModes(t, f.parent, []string{".", "private", "private/file"}))
			require.NoError(t, restarted.Rollback(t.Context(), h))
			assert.Equal(t, before, permissionModes(t, f.parent, []string{".", "private", "private/file"}))
			assert.NotContains(t, contentOf(t, f.parent), "extra")
		})
	}
}

func fmtRefs(refs []int) string {
	if len(refs) == 0 {
		return "unchanged-modes-only"
	}
	if len(refs) == 2 {
		return "changed-modes-author-order"
	}
	if refs[0] == 0 {
		return "only-a-changed-modes"
	}
	return "only-b-changed-modes"
}

func TestGather_ResolutionCapturesChmodAndEmptyDirectories(t *testing.T) {
	skipOnWindows(t)
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "base", 0o640) })
	ref := f.task(func(d string) { writeFile(t, d, "f", "task", 0o640) })
	h := f.begin()
	_, err := f.a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	chmodIn(t, h.StagingDir, "f", 0o600) // chmod alone is invisible to git status
	mkdirIn(t, h.StagingDir, "new-empty")
	chmodIn(t, h.StagingDir, "new-empty", 0o550)
	chmodIn(t, h.StagingDir, ".", 0o750)
	require.NoError(t, f.a.CommitResolution(t.Context(), h, "operator permissions"))
	restarted, err := NewWorkingArea(f.root)
	require.NoError(t, err)
	require.NoError(t, restarted.Apply(t.Context(), h))
	assert.Equal(t, map[string]uint32{".": 0o750, "f": 0o600, "new-empty": 0o550}, permissionModes(t, f.parent, []string{".", "f", "new-empty"}))
	require.NoError(t, restarted.Rollback(t.Context(), h))
	assert.Equal(t, uint32(0o640), permissionModes(t, f.parent, []string{"f"})["f"])
	require.NoError(t, restarted.ReleaseRef(t.Context(), f.handle, h.RollbackRef))
	assert.NoDirExists(t, h.StagingDir)
}

func TestGather_ReadOnlyDirectoriesAndSymlinks(t *testing.T) {
	skipOnWindows(t)
	// The link stays inside the area (out-of-area links are refused at the
	// snapshot); permission restore must still never chmod through it.
	f := newGatherFixture(t, func(p string) {
		writeFile(t, p, "locked/file", "base", 0o440)
		require.NoError(t, os.Symlink("locked/file", filepath.Join(p, "link")))
		chmodIn(t, p, "locked", 0o550)
		chmodIn(t, p, ".", 0o550)
	})
	t.Cleanup(func() { _ = makeAreaTreeWritable(f.root) })
	ref := f.task(func(d string) {
		chmodIn(t, d, ".", 0o750)
		chmodIn(t, d, "locked", 0o750)
		chmodIn(t, d, "locked/file", 0o640)
		writeFile(t, d, "locked/file", "task", 0o640)
		chmodIn(t, d, "locked", 0o550)
		chmodIn(t, d, ".", 0o550)
	})
	h := f.begin()
	t.Cleanup(func() { _ = makeAreaTreeWritable(h.StagingDir) })
	_, err := f.a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(t.Context(), h))
	assert.Equal(t, "file:0644:task", contentOf(t, f.parent)["locked/file"])
	assert.Equal(t, map[string]uint32{".": 0o550, "locked": 0o550, "locked/file": 0o640}, permissionModes(t, f.parent, []string{".", "locked", "locked/file"}))
	require.NoError(t, f.a.Rollback(t.Context(), h))
	assert.Equal(t, map[string]uint32{".": 0o550, "locked": 0o550, "locked/file": 0o440}, permissionModes(t, f.parent, []string{".", "locked", "locked/file"}))
	target, err := os.Readlink(filepath.Join(f.parent, "link"))
	require.NoError(t, err)
	assert.Equal(t, "locked/file", target)
}

func TestGather_MissingManifestsFailWithoutTouchingParent(t *testing.T) {
	for _, boundary := range []string{"baseline", "task", "task-incomplete", "apply", "apply-incomplete", "rollback"} {
		t.Run(boundary, func(t *testing.T) {
			f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "base", 0o640) })
			ref := f.task(func(d string) { writeFile(t, d, "f", "task", 0o600) })
			h := f.begin()
			backing, err := f.a.BackingDir(f.handle)
			require.NoError(t, err)
			dir, rev := backing, BaselineRef
			if boundary == "task" || boundary == "task-incomplete" {
				rev = ref
			}
			if boundary == "apply" || boundary == "apply-incomplete" {
				_, err = f.a.MergeTask(t.Context(), h, ref)
				require.NoError(t, err)
				dir, rev = filepath.Join(h.StagingDir, ".git"), "HEAD"
			}
			if boundary == "rollback" {
				rev = h.RollbackRef
			}
			commit, err := f.a.revParse(t.Context(), dir, rev)
			require.NoError(t, err)
			if boundary == "task-incomplete" || boundary == "apply-incomplete" {
				m, err := readManifest(dir, commit)
				require.NoError(t, err)
				delete(m.Permissions, "f")
				require.NoError(t, writeManifest(dir, commit, m))
			} else {
				require.NoError(t, os.Remove(manifestPath(dir, commit)))
			}
			before := contentOf(t, f.parent)
			switch boundary {
			case "apply", "apply-incomplete":
				err = f.a.Apply(t.Context(), h)
			case "rollback":
				err = f.a.Rollback(t.Context(), h)
			default:
				_, err = f.a.MergeTask(t.Context(), h, ref)
			}
			require.Error(t, err)
			assert.Equal(t, before, contentOf(t, f.parent))
			assert.Equal(t, uint32(0o640), permissionModes(t, f.parent, []string{"f"})["f"])
		})
	}
}

func TestGather_InterruptedApplyThenRestartRestoresBytesAndModes(t *testing.T) {
	skipOnWindows(t)
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "interrupted"}[interrupted], func(t *testing.T) {
			f := newGatherFixture(t, func(p string) {
				writeFile(t, p, "dir/f", "original", 0o640)
				chmodIn(t, p, "dir", 0o550)
				chmodIn(t, p, ".", 0o750)
			})
			t.Cleanup(func() { _ = makeAreaTreeWritable(f.parent) })
			ref := f.task(func(d string) {
				chmodIn(t, d, "dir", 0o750)
				writeFile(t, d, "dir/f", "task", 0o600)
				chmodIn(t, d, ".", 0o700)
			})
			h := f.begin()
			_, err := f.a.MergeTask(t.Context(), h, ref)
			require.NoError(t, err)
			before := contentOf(t, f.parent)
			modes := permissionModes(t, f.parent, []string{".", "dir", "dir/f"})
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			gate := filepath.Join(t.TempDir(), "started")
			wrapper := filepath.Join(t.TempDir(), "git")
			tail := "exit 1\n"
			if interrupted {
				tail = "sleep 30\nexit 1\n"
			}
			script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = checkout-index ]; then\n chmod 700 . dir\n printf partial > dir/f\n chmod 600 dir/f\n touch '" + gate + "'\n " + tail + " fi\ndone\nexec '" + realGit + "' \"$@\"\n"
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o755))
			f.a.git = NewRunner(RunnerConfig{GitPath: wrapper, StateDir: t.TempDir()})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- f.a.Apply(ctx, h) }()
			require.Eventually(t, func() bool { _, err := os.Stat(gate); return err == nil }, 5*time.Second, 10*time.Millisecond)
			if interrupted {
				cancel()
			}
			select {
			case err := <-result:
				require.Error(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("apply did not stop")
			}
			restarted, err := NewWorkingArea(f.root)
			require.NoError(t, err)
			_, err = restarted.BeginGather(t.Context(), f.handle, h.GatherID, f.parent)
			require.NoError(t, err)
			require.NoError(t, restarted.Rollback(t.Context(), h))
			assert.Equal(t, before, contentOf(t, f.parent))
			assert.Equal(t, modes, permissionModes(t, f.parent, []string{".", "dir", "dir/f"}))
			require.NoError(t, restarted.Apply(t.Context(), h))
			assert.Equal(t, map[string]uint32{".": 0o700, "dir": 0o750, "dir/f": 0o600}, permissionModes(t, f.parent, []string{".", "dir", "dir/f"}))
		})
	}
}

func TestGather_PermissionRestoreFailureDoesNotChmodSymlinkTarget(t *testing.T) {
	skipOnWindows(t)
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "base", 0o640) })
	ref := f.task(func(d string) { writeFile(t, d, "f", "task", 0o600) })
	h := f.begin()
	_, err := f.a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	outside := t.TempDir()
	writeFile(t, outside, "secret", "outside", 0o600)
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	wrapper := filepath.Join(t.TempDir(), "git")
	// Replace the freshly checked-out regular file just before chmod restoration.
	// Restoration must fail closed, not chmod through an unexpected symlink.
	script := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = checkout-index ]; then\n '" + realGit + "' \"$@\" || exit $?\n rm f\n ln -s '" + filepath.Join(outside, "secret") + "' f\n exit 0\n fi\ndone\nexec '" + realGit + "' \"$@\"\n"
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o755))
	f.a.git = NewRunner(RunnerConfig{GitPath: wrapper, StateDir: t.TempDir()})
	require.Error(t, f.a.Apply(t.Context(), h))
	assert.Equal(t, uint32(0o600), permissionModes(t, outside, []string{"secret"})["secret"])
	restarted, err := NewWorkingArea(f.root)
	require.NoError(t, err)
	require.NoError(t, restarted.Rollback(t.Context(), h))
	assert.Equal(t, "file:0644:base", contentOf(t, f.parent)["f"])
	assert.Equal(t, uint32(0o640), permissionModes(t, f.parent, []string{"f"})["f"])
}

func TestGather_NestedImmediateParentPermissions(t *testing.T) {
	skipOnWindows(t)
	f := newGatherFixture(t, func(p string) { writeFile(t, p, "f", "root", 0o640) })
	taskDir := filepath.Join(f.root, "instances/outer-task")
	require.NoError(t, f.a.ForkTask(t.Context(), f.handle, taskDir))
	innerHandle, err := f.a.SnapshotParent(t.Context(), uuid.New(), taskDir)
	require.NoError(t, err)
	innerTask := filepath.Join(f.root, "instances/inner-task")
	require.NoError(t, f.a.ForkTask(t.Context(), innerHandle, innerTask))
	writeFile(t, innerTask, "f", "inner", 0o600)
	ref, err := f.a.FinalizeTask(t.Context(), innerHandle, uuid.New(), innerTask)
	require.NoError(t, err)
	h, err := f.a.BeginGather(t.Context(), innerHandle, uuid.New(), taskDir)
	require.NoError(t, err)
	_, err = f.a.MergeTask(t.Context(), h, ref)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(t.Context(), h))
	assert.Equal(t, "file:0644:root", contentOf(t, f.parent)["f"])
	assert.Equal(t, uint32(0o640), permissionModes(t, f.parent, []string{"f"})["f"])
	outerRef, err := f.a.FinalizeTask(t.Context(), f.handle, uuid.New(), taskDir)
	require.NoError(t, err)
	outer := f.begin()
	_, err = f.a.MergeTask(t.Context(), outer, outerRef)
	require.NoError(t, err)
	require.NoError(t, f.a.Apply(t.Context(), outer))
	assert.Equal(t, "file:0644:inner", contentOf(t, f.parent)["f"])
	assert.Equal(t, uint32(0o600), permissionModes(t, f.parent, []string{"f"})["f"])
}
