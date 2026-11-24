package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

const (
	// backingDirName is the directory under the workspace root that holds every
	// group snapshot's private backing repository and gather staging areas. It
	// is a sibling of the per-instance directories, never inside an author dir.
	backingDirName = ".group-backing"

	// BaselineRef names the group-start snapshot commit in a backing store.
	BaselineRef = "refs/arbitron/baseline"
	// TaskRefPrefix + <taskInstanceID> names a finalized task commit.
	TaskRefPrefix = "refs/arbitron/tasks/"
	// RollbackRefPrefix + <gatherID> names a gather's pre-gather snapshot.
	RollbackRefPrefix = "refs/arbitron/rollback/"

	// backingAttributes overrides every in-tree .gitattributes so content is
	// stored and written back byte-for-byte: no end-of-line conversion, no
	// $Id$ expansion, no clean/smudge filters, no re-encoding. Merge drivers
	// are left to the content's own attributes (union / binary are honoured;
	// unknown custom drivers fall back to git's text merge).
	backingAttributes = "* -text -eol -ident -filter -working-tree-encoding\n"
)

// WorkingArea is the git-backed engine.WorkingArea.
//
// BACKING STRATEGY (P3-A). Every group snapshot owns a private bare
// repository at <root>/.group-backing/<groupRunID>.git. Content is recorded
// with git plumbing (hash-object --no-filters, update-index --index-info,
// write-tree, commit-tree) from a Go walk of the directory, never with
// "git add" — "git add" would turn a nested author repository into a gitlink
// and lose its working-tree files. The walk captures everything the area
// holds — tracked, modified, untracked and ignored files, executable bits and
// symlinks — and excludes every ".git" entry at any depth: author repository
// metadata is not task content. Metadata is reproduced separately per task
// (metadata.go), and empty directories (which git cannot store) live in a
// per-commit manifest beside the repository.
//
// All git processes run through Runner, so no author or host configuration,
// hook, filter or credential helper ever runs. The author's repositories are
// read (copied) but never written.
type WorkingArea struct {
	root    string // absolute, symlink-resolved workspace root
	backing string // root/.group-backing
	git     *Runner
	locks   keyedMutex
	// portableGit is the checkpoint object materializer supplied by the root.
	// A missing materializer refuses resolution export.
	portableGit func(context.Context, string, string) error
	// resolutionMaxBytes bounds expanded resolution copies; nonpositive uses the default.
	resolutionMaxBytes int64
}

var _ engine.GroupWorkspace = (*WorkingArea)(nil)

// Option configures NewWorkingArea.
type Option func(*WorkingArea) error

// WithRunner makes every git process of the area go through r (tests, an
// explicit git binary, a custom timeout/output cap).
func WithRunner(r *Runner) Option {
	return func(a *WorkingArea) error {
		if r == nil {
			return errors.New("git working area: runner is required")
		}
		a.git = r
		return nil
	}
}

// NewWorkingArea returns a WorkingArea whose backing stores live under
// root/.group-backing. root is the workspace root (cfg.Workspace.Path); it is
// created if missing. Without WithRunner every git process runs through a
// default Runner (git looked up on the subprocess PATH at each use) whose
// private HOME lives at root/.group-backing/.runner.
func NewWorkingArea(root string, opts ...Option) (*WorkingArea, error) {
	if root == "" {
		return nil, errors.New("git working area: workspace root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("git working area: resolve root: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(abs, backingDirName), 0o700); err != nil {
		return nil, fmt.Errorf("git working area: create backing root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("git working area: resolve root: %w", err)
	}
	a := &WorkingArea{root: resolved, backing: filepath.Join(resolved, backingDirName)}
	for _, opt := range opts {
		if err := opt(a); err != nil {
			return nil, err
		}
	}
	if a.git == nil {
		a.git = NewRunner(RunnerConfig{StateDir: filepath.Join(a.backing, ".runner")})
	}
	return a, nil
}

// BackingDir returns the private backing repository path for a snapshot
// handle. The handle must be a group-run UUID (it is used in a path).
func (a *WorkingArea) BackingDir(handle string) (string, error) {
	id, err := uuid.Parse(handle)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("git working area: invalid snapshot handle %q", handle)
	}
	return filepath.Join(a.backing, id.String()+".git"), nil
}

// SnapshotParent records parentDir as the group's baseline and returns the
// handle (the group-run id). A previous snapshot for the same group run is
// replaced. Fails BEFORE any task exists when git is missing, an author
// repository is locked, or the area holds unsupported content.
func (a *WorkingArea) SnapshotParent(ctx context.Context, groupRunID uuid.UUID, parentDir string) (string, error) {
	handle := groupRunID.String()
	backing, err := a.BackingDir(handle)
	if err != nil {
		return "", err
	}
	unlock := a.locks.Lock(handle)
	defer unlock()

	parent, err := resolveDir(parentDir)
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	if err := removeAreaTree(backing); err != nil {
		return "", fmt.Errorf("git working area: reset backing store: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = removeAreaTree(backing)
		}
	}()
	if err := a.initBacking(ctx, backing); err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	content, err := scanContainedArea(parent, a.skipInside(parent))
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	meta, err := a.captureMetadata(ctx, parent, backing, content.GitEntries)
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	commit, err := a.commitContent(ctx, backing, parent, content, "", "arbitron: group baseline "+handle)
	if err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	if err := writeManifest(backing, commit, manifest{Root: parent, EmptyDirs: content.EmptyDirs, Metadata: meta, Permissions: content.Permissions}); err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: write manifest: %w", err)
	}
	if err := a.updateRef(ctx, backing, BaselineRef, commit); err != nil {
		return "", fmt.Errorf("git working area: snapshot parent: %w", err)
	}
	ok = true
	return handle, nil
}

// ForkTask materializes an independent writable copy of the baseline at
// taskDir (created if missing; it must be empty): every file with its mode,
// every symlink, every empty directory, and an independent reproduction of
// each author repository's metadata.
// The destination must resolve outside the private backing store.
func (a *WorkingArea) ForkTask(ctx context.Context, snapshotHandle string, taskDir string) (retErr error) {
	backing, baseline, err := a.baseline(ctx, snapshotHandle)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	taskPath, err := filepath.Abs(taskDir)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	taskPath, err = resolveForkDestination(taskPath)
	if err != nil {
		return fmt.Errorf("git working area: fork task: resolve destination: %w", err)
	}
	store, err := resolveDir(a.backing)
	if err != nil {
		return fmt.Errorf("git working area: fork task: resolve backing store: %w", err)
	}
	if within(store, taskPath) {
		return fmt.Errorf("git working area: fork task: task directory %q is inside the backing store", taskDir)
	}
	if err := os.MkdirAll(taskDir, 0o700); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	task, err := resolveDir(taskDir)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	entries, err := os.ReadDir(task)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("git working area: fork task: task directory %q is not empty", task)
	}
	info, err := os.Stat(task)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, cleanFailedFork(task, info.Mode().Perm()))
		}
	}()
	m, err := readManifest(backing, baseline)
	if err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := os.Chmod(task, info.Mode().Perm()|0o700); err != nil {
		return err
	}
	if err := a.checkoutCommit(ctx, backing, filepath.Join(backing, "arbitron", "tmp"), baseline, task); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := createEmptyDirs(task, m.EmptyDirs); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := restoreMetadata(ctx, backing, task, m); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	if err := restoreAreaPermissions(ctx, task, m.Permissions); err != nil {
		return fmt.Errorf("git working area: fork task: %w", err)
	}
	return nil
}

// Resolve aliases before MkdirAll, including ancestors of a new destination,
// so refusing a fork cannot write even an empty directory into the store.
func resolveForkDestination(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
		return resolved, err
	}
	parent, err := resolveForkDestination(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

// cleanFailedFork makes any restored read-only directories removable, without
// following symlinks. A failed fork leaves the caller's directory empty/retryable.
func cleanFailedFork(task string, originalMode os.FileMode) error {
	if err := makeAreaTreeWritable(task); err != nil {
		return fmt.Errorf("clean failed fork: %w", err)
	}
	entries, err := os.ReadDir(task)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(task, entry.Name())); err != nil {
			return err
		}
	}
	return os.Chmod(task, originalMode)
}

// makeAreaTreeWritable changes only directories owned by this private copy;
// symlinks are never followed. Git metadata can contain read-only directories.
func makeAreaTreeWritable(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.Chmod(p, 0o700)
		}
		return nil
	})
}

func removeAreaTree(root string) error {
	if err := makeAreaTreeWritable(root); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.RemoveAll(root)
}

// FinalizeTask records taskDir's full content as refs/arbitron/tasks/<id>, a
// commit whose single parent is the baseline. Edits are captured whether or
// not the task committed them in its own repository copy; the task's
// repository metadata is never recorded. First publication wins; later calls
// reuse the frozen ref, including after a crash before roster persistence.
func (a *WorkingArea) FinalizeTask(ctx context.Context, snapshotHandle string, taskInstanceID uuid.UUID, taskDir string) (string, error) {
	if taskInstanceID == uuid.Nil {
		return "", errors.New("git working area: finalize task: task instance id is required")
	}
	backing, baseline, err := a.baseline(ctx, snapshotHandle)
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	ref := TaskRefPrefix + taskInstanceID.String()
	if exists, err := a.refExists(ctx, backing, ref); err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	} else if exists {
		return ref, nil
	}
	task, err := resolveDir(taskDir)
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	content, err := scanContainedArea(task, a.skipInside(task))
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	// Give each publication its own commit and manifest even when Git trees
	// match: empty directories and permission metadata can still differ. A
	// losing publisher must never overwrite the winner's sidecar.
	message := "arbitron: task " + taskInstanceID.String() + " publication " + uuid.NewString()
	commit, err := a.commitContent(ctx, backing, task, content, baseline, message)
	if err != nil {
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	if err := writeManifest(backing, commit, manifest{Root: task, EmptyDirs: content.EmptyDirs, Permissions: content.Permissions}); err != nil {
		return "", fmt.Errorf("git working area: finalize task: write manifest: %w", err)
	}
	// Create only, atomically across adapters/processes. The initial existence
	// check alone cannot protect against concurrent publication.
	if _, err := a.git.Run(ctx, Command{Args: []string{"update-ref", ref, commit, strings.Repeat("0", len(commit))}, GitDir: backing}); err != nil {
		if exists, readErr := a.refExists(ctx, backing, ref); readErr == nil && exists {
			return ref, nil // another publisher won; reuse its immutable result
		}
		return "", fmt.Errorf("git working area: finalize task: %w", err)
	}
	return ref, nil
}

// ReleaseSnapshot deletes the backing store and every staging area of the
// snapshot. An empty or unknown handle is not an error.
func (a *WorkingArea) ReleaseSnapshot(ctx context.Context, snapshotHandle string) error {
	if snapshotHandle == "" {
		return nil
	}
	backing, err := a.BackingDir(snapshotHandle)
	if err != nil {
		return err
	}
	unlock := a.locks.Lock(snapshotHandle)
	defer unlock()
	id, _ := uuid.Parse(snapshotHandle) // BackingDir already validated the handle.
	staging, err := filepath.Glob(filepath.Join(a.backing, id.String()+"-gather-*"))
	if err != nil {
		return err
	}
	for _, s := range staging {
		if err := removeAreaTree(s); err != nil {
			return fmt.Errorf("git working area: release snapshot: %w", err)
		}
	}
	if err := removeAreaTree(backing); err != nil {
		return fmt.Errorf("git working area: release snapshot: %w", err)
	}
	return nil
}

// ResolveRef returns the commit a ref of the snapshot names.
func (a *WorkingArea) ResolveRef(ctx context.Context, snapshotHandle, ref string) (string, error) {
	backing, err := a.BackingDir(snapshotHandle)
	if err != nil {
		return "", err
	}
	return a.revParse(ctx, backing, ref)
}

func (a *WorkingArea) initBacking(ctx context.Context, backing string) error {
	if _, err := a.git.Run(ctx, Command{Args: []string{"init", "--bare", "-q", "--template=", backing}}); err != nil {
		return fmt.Errorf("create backing store: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(backing, "info"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(backing, "info", "attributes"), []byte(backingAttributes), 0o600); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(backing, "arbitron", "tmp"), 0o700)
}

// baseline resolves the backing store and baseline commit of a handle.
func (a *WorkingArea) baseline(ctx context.Context, handle string) (string, string, error) {
	backing, err := a.BackingDir(handle)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Stat(backing); err != nil {
		return "", "", fmt.Errorf("unknown snapshot %q: %w", handle, err)
	}
	commit, err := a.revParse(ctx, backing, BaselineRef)
	if err != nil {
		return "", "", fmt.Errorf("snapshot %q has no baseline: %w", handle, err)
	}
	return backing, commit, nil
}

func (a *WorkingArea) revParse(ctx context.Context, gitDir, rev string) (string, error) {
	res, err := a.git.Run(ctx, Command{Args: []string{"rev-parse", "--verify", "-q", rev + "^{commit}"}, GitDir: gitDir})
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rev, err)
	}
	return strings.TrimSpace(res.Stdout), nil
}

func (a *WorkingArea) refExists(ctx context.Context, gitDir, ref string) (bool, error) {
	_, err := a.git.Run(ctx, Command{Args: []string{"show-ref", "--verify", "-q", ref}, GitDir: gitDir})
	if err == nil {
		return true, nil
	}
	if IsExitCode(err, 1) {
		return false, nil
	}
	return false, err
}

func (a *WorkingArea) updateRef(ctx context.Context, gitDir, ref, commit string) error {
	if _, err := a.git.Run(ctx, Command{Args: []string{"update-ref", ref, commit}, GitDir: gitDir}); err != nil {
		return fmt.Errorf("record %s: %w", ref, err)
	}
	return nil
}

// skipInside returns the backing root when it lies inside dir (a parent area
// that IS the workspace root), so a snapshot never captures its own store.
func (a *WorkingArea) skipInside(dir string) string {
	if within(dir, a.backing) {
		return a.backing
	}
	return ""
}

func resolveDir(dir string) (string, error) {
	if dir == "" {
		return "", errors.New("directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("directory %q: %w", dir, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", dir)
	}
	return resolved, nil
}

// keyedMutex serializes operations per snapshot handle within this process.
type keyedMutex struct {
	mu sync.Mutex
	m  map[string]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.m == nil {
		k.m = map[string]*keyedEntry{}
	}
	e := k.m[key]
	if e == nil {
		e = &keyedEntry{}
		k.m[key] = e
	}
	e.refs++
	k.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		k.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(k.m, key)
		}
		k.mu.Unlock()
	}
}
