package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// GroupWorkspaceAdapter implements engine.GroupWorkspace
// (domain/engine/group_workspace.go) with plain recursive directory copies.
// It wraps instance.ErrUnsupportedSnapshotContent — the same value as
// engine.ErrUnsupportedSnapshotContent — because this package cannot import
// domain/engine (domain/engine's internal tests import this package).
//
// LAYOUT. Snapshots live under <root>/.group-snapshots/<groupRunID>/, a
// SIBLING of the WorkspaceManagerAdapter's <root>/instances/ tree — never
// inside an instance directory — so RemoveInstanceDir on the parent (or on any
// task) can never delete a snapshot that later forks or a nested group still
// need. The store and every snapshot directory are mode 0o700, matching the
// instance-directory security invariant (workspace.go).
//
// HANDLE. The handle returned by SnapshotParent is the snapshot's absolute
// directory path. ForkTask and ReleaseSnapshot accept ONLY a clean absolute
// path whose parent is exactly the snapshot store and whose base name is a
// canonical UUID, so a corrupt or hostile handle (e.g. "<store>/../instances")
// can never make them read or delete anything else.
//
// IMMUTABILITY. A snapshot is never handed to a task: its path exists only in
// the group run record (GroupRun.WorkspaceSnapshot) and is passed only to
// ForkTask / ReleaseSnapshot. Every fork is a fresh byte-for-byte copy (no
// hard links, no reflinks), so writes in a fork, in the parent, or in another
// fork cannot reach it. The snapshot keeps the source's permission bits —
// rather than being made read-only — because those bits ARE the content the
// forks must reproduce; read-only bits would add no real boundary (anything
// running as the server's OS user could chmod them back) while forcing a
// separate mode manifest.
//
// SUPPORTED CONTENT. Regular files (content + permission bits), directories
// (including empty ones, with their permission bits), independent Git metadata
// (including linked worktrees, materialized by CopyGroupGitMetadata), and
// RELATIVE symlinks that resolve inside the working area (recreated verbatim,
// so they resolve inside the fork). Everything else fails with
// instance.ErrUnsupportedSnapshotContent naming the path:
//   - an absolute symlink, or a relative one that resolves outside the area
//     (the fork would share the target with the parent);
//   - a dangling symlink containing ".." (its eventual target cannot be
//     proven to stay inside), or one that cannot be resolved (a loop);
//   - devices, sockets and named pipes.
//
// Only permission bits (0o777) are preserved; setuid/setgid/sticky bits and
// timestamps are not.
type GroupWorkspaceAdapter struct {
	snapshotsDir string
}

// groupSnapshotsDirName is the snapshot store directory under the workspace
// root. The leading dot keeps it out of the way of the flat-store migration
// (which only moves folders containing a workflow.json) and of casual listings.
const groupSnapshotsDirName = ".group-snapshots"

// snapshotDirMode is the mode of the store and of every snapshot root.
const snapshotDirMode os.FileMode = 0o700

// ErrInvalidSnapshotHandle is returned by ForkTask / ReleaseSnapshot for a
// handle that is not a snapshot of this adapter.
var ErrInvalidSnapshotHandle = errors.New("invalid group snapshot handle")

// NewGroupWorkspace creates a GroupWorkspaceAdapter rooted at root (the same
// workspace root the WorkspaceManagerAdapter uses: cfg.Workspace.Path). root
// must be non-empty; it is resolved to an absolute path. Nothing is created
// on disk until the first snapshot.
func NewGroupWorkspace(root string) (*GroupWorkspaceAdapter, error) {
	if root == "" {
		return nil, fmt.Errorf("filesystem: group workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("filesystem: resolve group workspace root: %w", err)
	}
	return &GroupWorkspaceAdapter{snapshotsDir: filepath.Join(abs, groupSnapshotsDirName)}, nil
}

// SnapshotsDir returns the absolute snapshot store directory.
func (g *GroupWorkspaceAdapter) SnapshotsDir() string { return g.snapshotsDir }

// SnapshotParent implements engine.GroupWorkspace.
//
// The copy is built in <store>/.tmp-<groupRunID> and renamed into place only
// when complete, so a crash or a rejected entry never leaves a half-built
// snapshot at the handle path. Re-snapshotting the same groupRunID (a retried
// launch that never persisted its run) replaces the previous snapshot.
func (g *GroupWorkspaceAdapter) SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error) {
	if groupRunID == uuid.Nil {
		return "", fmt.Errorf("filesystem: snapshot parent: group run id is required")
	}
	if parentDir == "" || !filepath.IsAbs(parentDir) {
		return "", fmt.Errorf("filesystem: snapshot parent: parent directory %q must be an absolute path", parentDir)
	}
	parentDir = filepath.Clean(parentDir)
	info, err := os.Stat(parentDir)
	if err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("filesystem: snapshot parent: %s is not a directory", parentDir)
	}
	if err := g.ensureStore(); err != nil {
		return "", err
	}
	realParent, err := filepath.EvalSymlinks(parentDir)
	if err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: resolve %s: %w", parentDir, err)
	}
	realStore, err := filepath.EvalSymlinks(g.snapshotsDir)
	if err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: resolve snapshot store: %w", err)
	}
	if within(realParent, realStore) || within(realStore, realParent) {
		return "", fmt.Errorf("filesystem: snapshot parent: %s overlaps the snapshot store %s", parentDir, g.snapshotsDir)
	}

	final := filepath.Join(g.snapshotsDir, groupRunID.String())
	tmp := filepath.Join(g.snapshotsDir, ".tmp-"+groupRunID.String())
	if err := removeTree(tmp); err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: clear stale build dir: %w", err)
	}
	if err := os.Mkdir(tmp, snapshotDirMode); err != nil {
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}
	if err := os.Chmod(tmp, snapshotDirMode); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}

	check := &contentCheck{root: parentDir, realRoot: realParent}
	if err := copyGroupWorkingArea(ctx, parentDir, tmp, check); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot %s: %w", parentDir, err)
	}
	if err := removeTree(final); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot parent: replace previous snapshot: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = removeTree(tmp)
		return "", fmt.Errorf("filesystem: snapshot parent: %w", err)
	}
	return final, nil
}

// ForkTask implements engine.GroupWorkspace. taskDir must be absolute and
// outside the snapshot store; if it exists it must be an EMPTY directory (a
// non-empty one would mix stale content into the task's starting copy). The
// fork root keeps taskDir's own mode — an instance directory's 0o700 — and
// everything below it mirrors the snapshot. A failed fork removes whatever it
// wrote.
func (g *GroupWorkspaceAdapter) ForkTask(ctx context.Context, snapshotHandle string, taskDir string) error {
	snap, err := g.resolveHandle(snapshotHandle)
	if err != nil {
		return fmt.Errorf("filesystem: fork task: %w", err)
	}
	if info, err := os.Stat(snap); err != nil || !info.IsDir() {
		return fmt.Errorf("filesystem: fork task: group snapshot %s does not exist (released or never taken)", snapshotHandle)
	}
	if taskDir == "" || !filepath.IsAbs(taskDir) {
		return fmt.Errorf("filesystem: fork task: task directory %q must be an absolute path", taskDir)
	}
	taskDir = filepath.Clean(taskDir)
	realTask, err := resolveWorkingPath(taskDir)
	if err != nil {
		return fmt.Errorf("filesystem: fork task: resolve task directory: %w", err)
	}
	realStore, err := filepath.EvalSymlinks(g.snapshotsDir)
	if err != nil {
		return fmt.Errorf("filesystem: fork task: resolve snapshot store: %w", err)
	}
	if within(realStore, realTask) {
		return fmt.Errorf("filesystem: fork task: task directory %s is inside the snapshot store", taskDir)
	}

	created := false
	info, err := os.Stat(taskDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(taskDir, snapshotDirMode); err != nil {
			return fmt.Errorf("filesystem: fork task: %w", err)
		}
		if err := os.Chmod(taskDir, snapshotDirMode); err != nil {
			return fmt.Errorf("filesystem: fork task: %w", err)
		}
		created = true
	case err != nil:
		return fmt.Errorf("filesystem: fork task: %w", err)
	case !info.IsDir():
		return fmt.Errorf("filesystem: fork task: %s is not a directory", taskDir)
	default:
		entries, err := os.ReadDir(taskDir)
		if err != nil {
			return fmt.Errorf("filesystem: fork task: %w", err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("filesystem: fork task: task directory %s is not empty (%d entries)", taskDir, len(entries))
		}
	}

	if err := copyGroupWorkingArea(ctx, snap, taskDir, nil); err != nil {
		if created {
			_ = removeTree(taskDir)
		} else {
			_ = removeContents(taskDir)
		}
		return fmt.Errorf("filesystem: fork task into %s: %w", taskDir, err)
	}
	return nil
}

// ReleaseSnapshot implements engine.GroupWorkspace. The empty handle (a
// parent that had no workspace) and an already-released snapshot are no-ops.
func (g *GroupWorkspaceAdapter) ReleaseSnapshot(ctx context.Context, snapshotHandle string) error {
	if snapshotHandle == "" {
		return nil
	}
	snap, err := g.resolveHandle(snapshotHandle)
	if err != nil {
		return fmt.Errorf("filesystem: release snapshot: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("filesystem: release snapshot: %w", err)
	}
	// An interrupted SnapshotParent leaves .tmp-<id>. Releasing its canonical
	// handle clears that build too; validate it before chmod/remove can follow
	// a substituted path.
	tmp := filepath.Join(g.snapshotsDir, ".tmp-"+filepath.Base(snap))
	if err := validateSnapshotDirectory(tmp); err != nil {
		return err
	}
	for _, path := range []string{tmp, snap} {
		if err := removeTree(path); err != nil {
			return fmt.Errorf("filesystem: release snapshot %s: %w", snapshotHandle, err)
		}
	}
	return nil
}

// ensureStore creates the snapshot store (0o700, umask-proof).
func (g *GroupWorkspaceAdapter) ensureStore() error {
	if err := os.MkdirAll(g.snapshotsDir, snapshotDirMode); err != nil {
		return fmt.Errorf("filesystem: create group snapshot store: %w", err)
	}
	if err := validateSnapshotDirectory(g.snapshotsDir); err != nil {
		return err
	}
	if err := os.Chmod(g.snapshotsDir, snapshotDirMode); err != nil {
		return fmt.Errorf("filesystem: tighten group snapshot store perms: %w", err)
	}
	return nil
}

// resolveHandle validates that handle names a snapshot directory of THIS
// adapter (see the HANDLE note on the type) and returns it.
func (g *GroupWorkspaceAdapter) resolveHandle(handle string) (string, error) {
	if handle == "" {
		return "", fmt.Errorf("%w: empty handle (the parent had no working area)", ErrInvalidSnapshotHandle)
	}
	if !filepath.IsAbs(handle) || filepath.Clean(handle) != handle || filepath.Dir(handle) != g.snapshotsDir {
		return "", fmt.Errorf("%w: %q is not inside %s", ErrInvalidSnapshotHandle, handle, g.snapshotsDir)
	}
	base := filepath.Base(handle)
	id, err := uuid.Parse(base)
	if err != nil || id.String() != base {
		return "", fmt.Errorf("%w: %q does not name a group run snapshot", ErrInvalidSnapshotHandle, handle)
	}
	for _, path := range []string{g.snapshotsDir, handle} {
		if err := validateSnapshotDirectory(path); err != nil {
			return "", err
		}
	}
	return handle, nil
}

// validateSnapshotDirectory refuses substituted symlinks before reading,
// chmodding or removing a snapshot or its store. Missing directories are valid
// for unknown/released handles and for a store that has not been created yet.
func validateSnapshotDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("filesystem: inspect snapshot directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a snapshot directory", ErrInvalidSnapshotHandle, path)
	}
	return nil
}

// resolveWorkingPath resolves existing ancestors even when the destination
// itself has not been created. This prevents aliases from bypassing containment.
func resolveWorkingPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, fs.ErrNotExist) || filepath.Dir(path) == path {
		return resolved, err
	}
	parent, err := resolveWorkingPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

// within reports whether p is root or lies below it. Both must be clean.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// contentCheck validates source entries while a SNAPSHOT is taken. Forks copy
// an already-validated snapshot and run without it.
type contentCheck struct {
	root     string // lexical parent directory (clean, absolute)
	realRoot string // the same directory with symlinks resolved
}

func unsupported(rel, detail string) error {
	return fmt.Errorf("%w: %q %s", instance.ErrUnsupportedSnapshotContent, rel, detail)
}

// symlink accepts a relative link that stays inside the working area (see the
// SUPPORTED CONTENT note on GroupWorkspaceAdapter).
func (c *contentCheck) symlink(path, rel, target string) error {
	return CheckAreaSymlink(c.root, c.realRoot, path, rel, target)
}

// CheckAreaSymlink is the group symlink containment policy shared by every
// working-area adapter (this one and adapter/git): the link at path (rel to
// the area, pointing at target) is accepted only when it is relative and
// stays inside the area rooted at root (lexically) and realRoot (resolved).
// Absolute links, links resolving outside, unresolvable links and dangling
// links containing ".." fail with instance.ErrUnsupportedSnapshotContent,
// because a copy of the area would share their target with the original.
func CheckAreaSymlink(root, realRoot, path, rel, target string) error {
	c := contentCheck{root: root, realRoot: realRoot}
	if filepath.IsAbs(target) {
		return unsupported(rel, fmt.Sprintf("is a symlink to the absolute path %q; a task copy would share it with the parent — make the link relative to a path inside the working area, or replace it with a copy", target))
	}
	if !within(c.root, filepath.Join(filepath.Dir(path), target)) {
		return unsupported(rel, fmt.Sprintf("is a symlink to %q, which points outside the working area; replace it with a copy of the target", target))
	}
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, fs.ErrNotExist) {
		for _, part := range strings.Split(filepath.ToSlash(target), "/") {
			if part == ".." {
				return unsupported(rel, fmt.Sprintf("is a dangling symlink to %q; a dangling link containing \"..\" cannot be proven to stay inside the working area — remove it or point it at an existing path", target))
			}
		}
		return nil
	}
	if err != nil {
		return unsupported(rel, fmt.Sprintf("is a symlink that cannot be resolved (%v); remove it or fix the link loop", err))
	}
	if !within(c.realRoot, resolved) {
		return unsupported(rel, fmt.Sprintf("is a symlink to %q, which resolves outside the working area (to %s); replace it with a copy of the target", target, resolved))
	}
	return nil
}

// copyGroupWorkingArea keeps metadata reproduction mandatory for both the
// immutable snapshot and every task fork, with the same caller-owned rollback.
func copyGroupWorkingArea(ctx context.Context, src, dst string, check *contentCheck) error {
	if err := copyWorkingTree(ctx, src, dst, check); err != nil {
		return err
	}
	return CopyGroupGitMetadata(ctx, src, dst)
}

type dirMode struct {
	path string
	perm os.FileMode
}

// copyWorkingTree copies src's CONTENTS into the existing directory dst. Directories
// are created owner-writable and get their real permission bits only after
// everything below them is written (deepest first), so read-only source
// directories copy correctly. When check is non-nil every entry is validated
// first (snapshot); ctx is checked before every entry.
func copyWorkingTree(ctx context.Context, src, dst string, check *contentCheck) error {
	var dirs []dirMode
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Git pointers and directories are handled separately, after content.
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case mode.IsDir():
			if err := os.Mkdir(target, snapshotDirMode); err != nil {
				return err
			}
			dirs = append(dirs, dirMode{path: target, perm: mode.Perm()})
			return nil
		case mode&fs.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if check != nil {
				if err := check.symlink(path, rel, link); err != nil {
					return err
				}
			}
			return os.Symlink(link, target)
		case mode.IsRegular():
			return copyFileContent(path, target, mode.Perm())
		default:
			return unsupported(rel, fmt.Sprintf("is a special file (%s); only regular files, directories and relative symlinks can be copied into a task's working area", mode.Type()))
		}
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].perm); err != nil {
			return err
		}
	}
	return nil
}

// copyFileContent copies one regular file by CONTENT (never a hard link) and applies
// perm explicitly so the umask cannot relax or tighten it.
func copyFileContent(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, perm)
}

// makeOwnerWritable gives the owner rwx on every directory under root so a
// tree containing read-only directories can be deleted. Symlinks are never
// followed.
func makeOwnerWritable(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(path, snapshotDirMode)
		}
		return nil
	})
}

// removeTree deletes root and everything below it; a missing root is a no-op.
func removeTree(root string) error {
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err := makeOwnerWritable(root); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(root)
}

// removeContents deletes everything inside dir but keeps dir itself.
func removeContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := removeTree(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// ListSnapshots inventories published snapshots and crash-left temporary
// builds using canonical handles. If both exist, the newer timestamp protects
// an in-progress replacement from the orphan sweep's grace-period check.
func (g *GroupWorkspaceAdapter) ListSnapshots(ctx context.Context) ([]instance.GroupSnapshotInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSnapshotDirectory(g.snapshotsDir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(g.snapshotsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("filesystem: list group snapshots: %w", err)
	}
	byID := map[uuid.UUID]instance.GroupSnapshotInfo{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !e.IsDir() {
			continue
		}
		name := strings.TrimPrefix(e.Name(), ".tmp-")
		id, err := uuid.Parse(name)
		if err != nil || id == uuid.Nil || id.String() != name {
			continue
		}
		info, err := e.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("filesystem: inspect group snapshot: %w", err)
		}
		prev, exists := byID[id]
		if !exists || info.ModTime().After(prev.ModTime) {
			byID[id] = instance.GroupSnapshotInfo{GroupRunID: id, Handle: filepath.Join(g.snapshotsDir, name), ModTime: info.ModTime()}
		}
	}
	out := make([]instance.GroupSnapshotInfo, 0, len(byID))
	for _, info := range byID {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Handle < out[j].Handle })
	return out, nil
}

// HasSnapshot includes a temporary build: it too consumes disk after a crash.
func (g *GroupWorkspaceAdapter) HasSnapshot(ctx context.Context, handle string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	snap, err := g.resolveHandle(handle)
	if err != nil {
		return false, err
	}
	tmp := filepath.Join(g.snapshotsDir, ".tmp-"+filepath.Base(snap))
	if err := validateSnapshotDirectory(tmp); err != nil {
		return false, err
	}
	for _, path := range []string{snap, tmp} {
		if _, err := os.Stat(path); err == nil {
			return true, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	return false, nil
}
