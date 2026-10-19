package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// ErrMergeInProgress reports a staging area whose previous merge has not been
// resolved and committed yet.
var ErrMergeInProgress = errors.New("a merge is already in progress in the gather staging area")

// ErrApplyTouchesMetadata reports a staged result that would replace a
// directory holding author repository metadata with a file.
var ErrApplyTouchesMetadata = errors.New("staged result would overwrite author repository metadata")

const (
	stagingBranch = "refs/heads/gather"
	conflictsFile = "arbitron-conflicts.json"
	mergeFile     = "arbitron-merge.json"
	// Git's text/binary heuristic probes the first 8000 bytes for NUL.
	binaryProbeBytes = 8000
)

var _ engine.WorkingArea = (*WorkingArea)(nil)

// StagingDir returns the staging work tree of one gather of a snapshot.
func (a *WorkingArea) StagingDir(snapshotHandle string, gatherID uuid.UUID) (string, error) {
	if _, err := a.BackingDir(snapshotHandle); err != nil {
		return "", err
	}
	if gatherID == uuid.Nil {
		return "", errors.New("git working area: gather id is required")
	}
	return filepath.Join(a.backing, snapshotHandle+"-gather-"+gatherID.String()), nil
}

// BeginGather snapshots parentDir's exact current content (dirty, untracked,
// ignored, empty dirs; author .git excluded) as refs/arbitron/rollback/<id>,
// a commit whose parent is the baseline, and prepares the staging area: an
// ordinary git repository at the staging dir, sharing the backing store's
// objects through alternates, with branch "gather" at the rollback commit.
//
// Starting staging at the rollback commit (not the baseline) means anything
// the parent area gained since the group launched is kept; every task commit
// still shares the baseline as merge base, so each merge is a true 3-way
// merge of "parent now" and "task" against "group start".
//
// Idempotent per gatherID: an existing rollback ref is never re-taken (a
// restart after a partial Apply must still roll back to pre-gather content),
// and an existing staging repository is reused as is (merge progress and
// in-flight resolutions survive).
func (a *WorkingArea) BeginGather(ctx context.Context, snapshotHandle string, gatherID uuid.UUID, parentDir string) (engine.GatherHandle, error) {
	staging, err := a.StagingDir(snapshotHandle, gatherID)
	if err != nil {
		return engine.GatherHandle{}, err
	}
	unlock := a.locks.Lock(snapshotHandle)
	defer unlock()
	backing, baseline, err := a.baseline(ctx, snapshotHandle)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	parent, err := resolveDir(parentDir)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	// The group's tasks were forked from, and diffed against, the area the
	// baseline was scanned from; only that area may receive their changes. For a
	// nested group it is the enclosing task's fork, never the outer parent.
	base, err := readManifest(backing, baseline)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	if base.Root != parent {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: target %q differs from the group's baseline area %q", parent, base.Root)
	}
	h := engine.GatherHandle{
		SnapshotHandle: snapshotHandle,
		GatherID:       gatherID,
		StagingDir:     staging,
		RollbackRef:    RollbackRefPrefix + gatherID.String(),
		ParentDir:      parent,
	}

	exists, err := a.refExists(ctx, backing, h.RollbackRef)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	if !exists {
		content, err := scanArea(parent, a.skipInside(parent))
		if err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: rollback snapshot: %w", err)
		}
		commit, err := a.commitContent(ctx, backing, parent, content, baseline, "arbitron: pre-gather "+gatherID.String())
		if err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: rollback snapshot: %w", err)
		}
		if err := writeManifest(backing, commit, manifest{Root: parent, EmptyDirs: content.EmptyDirs, Permissions: content.Permissions}); err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: rollback manifest: %w", err)
		}
		if err := a.updateRef(ctx, backing, h.RollbackRef, commit); err != nil {
			return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
		}
	}
	rollback, err := a.revParse(ctx, backing, h.RollbackRef)
	if err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: %w", err)
	}
	m, err := a.gatherManifest(ctx, backing, rollback)
	if err != nil {
		return engine.GatherHandle{}, err
	}
	if m.Root != parent {
		return engine.GatherHandle{}, errors.New("git working area: gather parent differs from rollback snapshot")
	}
	if err := a.ensureStaging(ctx, backing, staging, rollback); err != nil {
		return engine.GatherHandle{}, fmt.Errorf("git working area: begin gather: staging: %w", err)
	}
	return h, nil
}

// ensureStaging creates (or keeps) the staging repository.
func (a *WorkingArea) ensureStaging(ctx context.Context, backing, staging, rollback string) error {
	gitDir := filepath.Join(staging, ".git")
	// A reused staging repository may have been edited by a resolver agent.
	if err := sealStaging(staging); err != nil {
		return err
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"rev-parse", "--verify", "-q", stagingBranch}, GitDir: gitDir}); err == nil {
		return nil
	}
	if err := removeAreaTree(staging); err != nil {
		return err
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"init", "-q", "--template=", staging}}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "objects", "info"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gitDir, "objects", "info", "alternates"),
		[]byte(filepath.Join(backing, "objects")+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(gitDir, "info", "attributes"), []byte(backingAttributes), 0o600); err != nil {
		return err
	}
	m, err := a.gatherManifest(ctx, backing, rollback)
	if err != nil {
		return err
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"checkout", "-q", "--detach", rollback}, Dir: staging}); err != nil {
		return err
	}
	if err := createEmptyDirs(staging, m.EmptyDirs); err != nil {
		return err
	}
	if err := writeManifest(gitDir, rollback, m); err != nil {
		return err
	}
	if err := restoreAreaPermissions(ctx, staging, m.Permissions); err != nil {
		return err
	}
	// Publish the branch last: a partially initialized staging tree is rebuilt.
	if _, err := a.git.Run(ctx, Command{Args: []string{"symbolic-ref", "HEAD", stagingBranch}, Dir: staging}); err != nil {
		return err
	}
	return a.updateRef(ctx, gitDir, stagingBranch, rollback)
}

// MergeTask merges one finalized task ref into the staging branch with
// "git merge --no-ff". Clean merges commit immediately. On conflict the
// unmerged index entries and conflict markers are left in the staging work
// tree and the conflicted paths are returned (and recorded so later marker
// checks know which files to inspect).
func (a *WorkingArea) MergeTask(ctx context.Context, h engine.GatherHandle, taskRef string) (engine.MergeOutcome, error) {
	backing, staging, err := a.gatherDirs(ctx, h)
	if err != nil {
		return engine.MergeOutcome{}, err
	}
	if !strings.HasPrefix(taskRef, TaskRefPrefix) {
		return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task: %q is not a finalized task ref", taskRef)
	}
	commit, err := a.revParse(ctx, backing, taskRef)
	if err != nil {
		return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task: %w", err)
	}
	gitDir := filepath.Join(staging, ".git")
	pendingPath := filepath.Join(gitDir, mergeFile)
	var pending pendingMerge
	pendingErr := readJSON(pendingPath, &pending)
	if pendingErr != nil && !errors.Is(pendingErr, os.ErrNotExist) {
		return engine.MergeOutcome{}, pendingErr
	}
	if pendingErr == nil && pending.Task != commit {
		return engine.MergeOutcome{}, ErrMergeInProgress
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"merge-base", "--is-ancestor", commit, "HEAD"}, Dir: staging}); err == nil {
		// The commit and its manifest were published before a crash in cleanup.
		if pendingErr == nil {
			if err := a.finishMerge(ctx, staging); err != nil {
				return engine.MergeOutcome{}, err
			}
		}
		return engine.MergeOutcome{Clean: true, ConflictPaths: []string{}}, nil
	} else if !IsExitCode(err, 1) {
		return engine.MergeOutcome{}, err
	}
	if pendingErr != nil {
		if _, ok := readMergeHead(gitDir); ok {
			return engine.MergeOutcome{}, ErrMergeInProgress
		}
		head, err := a.revParse(ctx, gitDir, "HEAD")
		if err != nil {
			return engine.MergeOutcome{}, err
		}
		// Read every load-bearing metadata input before changing staging.
		if _, err := a.mergeManifest(ctx, backing, staging, head, commit); err != nil {
			return engine.MergeOutcome{}, err
		}
		pending = pendingMerge{Head: head, Task: commit}
		if err := writeJSON(pendingPath, pending); err != nil {
			return engine.MergeOutcome{}, err
		}
	}
	if mergeHead, ok := readMergeHead(gitDir); ok {
		if mergeHead != commit {
			return engine.MergeOutcome{}, ErrMergeInProgress
		}
	} else {
		if err := a.writableContent(staging); err != nil {
			return engine.MergeOutcome{}, err
		}
		_, err := a.git.Run(ctx, Command{
			Args: []string{"merge", "--no-ff", "--no-commit", "--no-verify", "-q", commit}, Dir: staging,
		})
		if err != nil && !IsExitCode(err, 1) {
			return engine.MergeOutcome{}, fmt.Errorf("git working area: merge task: %w", err)
		}
		if _, ok := readMergeHead(gitDir); !ok {
			return engine.MergeOutcome{}, fmt.Errorf("git working area: merge did not start: %w", err)
		}
	}
	paths, err := a.unmergedPaths(ctx, staging)
	if err != nil {
		return engine.MergeOutcome{}, err
	}
	if !pending.Ready {
		if len(paths) > 0 {
			if err := writeJSON(filepath.Join(gitDir, conflictsFile), paths); err != nil {
				return engine.MergeOutcome{}, err
			}
		}
		if pending.Manifest == nil {
			m, err := a.mergeManifest(ctx, backing, staging, pending.Head, pending.Task)
			if err != nil {
				return engine.MergeOutcome{}, err
			}
			pending.Manifest = &m
			if err := writeJSON(pendingPath, pending); err != nil {
				return engine.MergeOutcome{}, err
			}
		}
		// Git does not remove empty directories. Remove only directories that
		// the task explicitly deleted relative to the baseline.
		baseCommit, err := a.revParse(ctx, backing, BaselineRef)
		if err != nil {
			return engine.MergeOutcome{}, err
		}
		baseManifest, err := a.gatherManifest(ctx, backing, baseCommit)
		if err != nil {
			return engine.MergeOutcome{}, err
		}
		taskManifest, err := a.gatherManifest(ctx, backing, commit)
		if err != nil {
			return engine.MergeOutcome{}, err
		}
		if err := a.writableContent(staging); err != nil {
			return engine.MergeOutcome{}, err
		}
		baseFiles, _, err := a.treeListing(ctx, backing, baseCommit)
		if err != nil {
			return engine.MergeOutcome{}, err
		}
		var deletedDirs []string
		for p := range baseManifest.Permissions {
			if _, exists := taskManifest.Permissions[p]; p != "." && !exists && !baseFiles[p] {
				deletedDirs = append(deletedDirs, p)
			}
		}
		sort.Slice(deletedDirs, func(i, j int) bool { return len(deletedDirs[i]) > len(deletedDirs[j]) })
		for _, p := range deletedDirs {
			info, err := os.Lstat(filepath.Join(staging, filepath.FromSlash(p)))
			if errors.Is(err, os.ErrNotExist) {
				delete(pending.Manifest.Permissions, p)
				continue
			}
			if err != nil {
				return engine.MergeOutcome{}, err
			}
			if info.IsDir() {
				if err := os.Remove(filepath.Join(staging, filepath.FromSlash(p))); err == nil {
					delete(pending.Manifest.Permissions, p)
				}
			}
		}
		if err := createEmptyDirs(staging, pending.Manifest.EmptyDirs); err != nil {
			return engine.MergeOutcome{}, err
		}
		if err := restoreAreaPermissions(ctx, staging, pending.Manifest.Permissions); err != nil {
			return engine.MergeOutcome{}, err
		}
		pending.Ready = true
		if err := writeJSON(pendingPath, pending); err != nil {
			return engine.MergeOutcome{}, err
		}
	}
	paths, err = a.ConflictPaths(ctx, h)
	if err != nil {
		return engine.MergeOutcome{}, err
	}
	if len(paths) == 0 {
		if err := a.CommitResolution(ctx, h, "arbitron: gather "+taskRef); err != nil {
			return engine.MergeOutcome{}, err
		}
	}
	return engine.MergeOutcome{Clean: len(paths) == 0, ConflictPaths: paths}, nil
}

// ConflictPaths returns the paths still conflicted in the staging area:
// unmerged index entries (the authority), plus any path of the current merge's
// recorded conflicts whose file still contains conflict markers even though
// it was marked resolved.
func (a *WorkingArea) ConflictPaths(ctx context.Context, h engine.GatherHandle) ([]string, error) {
	_, staging, err := a.gatherDirs(ctx, h)
	if err != nil {
		return nil, err
	}
	if err := a.verifyPendingMerge(ctx, staging); err != nil {
		return nil, err
	}
	paths, err := a.unmergedPaths(ctx, staging)
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range paths {
		set[p] = true
	}
	if _, inMerge := readMergeHead(filepath.Join(staging, ".git")); inMerge {
		var recorded []string
		if err := readJSON(filepath.Join(staging, ".git", conflictsFile), &recorded); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, p := range recorded {
			if err := validateAreaPath(p); err != nil {
				return nil, err
			}
			if err := checkAreaAncestors(staging, p); err != nil {
				return nil, err
			}
			if !set[p] && hasConflictMarkers(filepath.Join(staging, filepath.FromSlash(p))) {
				set[p] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// CommitResolution stages the whole staging work tree ("git add -A --force",
// so new or ignored files the resolver wrote are kept) and commits it,
// completing the in-progress merge if there is one. It refuses, changing
// nothing, while ConflictPaths is non-empty. With no merge in progress and an
// unchanged staging tree it is a no-op returning nil (safe to replay).
func (a *WorkingArea) CommitResolution(ctx context.Context, h engine.GatherHandle, message string) error {
	_, staging, err := a.gatherDirs(ctx, h)
	if err != nil {
		return err
	}
	paths, err := a.ConflictPaths(ctx, h)
	if err != nil {
		return err
	}
	if len(paths) > 0 {
		return fmt.Errorf("git working area: commit resolution: %w: %s", engine.ErrStillConflicted, strings.Join(paths, ", "))
	}
	if mergeHead, ok := readMergeHead(filepath.Join(staging, ".git")); ok {
		if _, err := a.git.Run(ctx, Command{Args: []string{"merge-base", "--is-ancestor", mergeHead, "HEAD"}, Dir: staging}); err == nil {
			if err := a.finishMerge(ctx, staging); err != nil {
				return err
			}
		} else if !IsExitCode(err, 1) {
			return err
		}
	}
	if strings.TrimSpace(message) == "" {
		message = "arbitron: resolve gather conflicts"
	}
	if _, err := a.git.Run(ctx, Command{Args: []string{"add", "-A", "--force"}, Dir: staging}); err != nil {
		return fmt.Errorf("git working area: commit resolution: %w", err)
	}
	gitDir := filepath.Join(staging, ".git")
	head, err := a.revParse(ctx, gitDir, "HEAD")
	if err != nil {
		return err
	}
	content, err := scanArea(staging, "")
	if err != nil {
		return err
	}
	m := manifest{Permissions: content.Permissions, EmptyDirs: content.EmptyDirs}
	res, err := a.git.Run(ctx, Command{Args: []string{"write-tree"}, Dir: staging})
	if err != nil {
		return err
	}
	tree := strings.TrimSpace(res.Stdout)
	mergeHead, inMerge := readMergeHead(gitDir)
	if !inMerge {
		previous, err := a.gatherManifest(ctx, gitDir, head)
		if err != nil {
			return err
		}
		res, err := a.git.Run(ctx, Command{Args: []string{"rev-parse", "HEAD^{tree}"}, Dir: staging})
		if err != nil {
			return err
		}
		if tree == strings.TrimSpace(res.Stdout) && maps.Equal(m.Permissions, previous.Permissions) && slices.Equal(m.EmptyDirs, previous.EmptyDirs) {
			return a.finishMerge(ctx, staging)
		}
	}
	args := []string{"commit-tree", tree, "-p", head, "-m", message}
	if inMerge {
		args = append(args, "-p", mergeHead)
	}
	res, err = a.git.Run(ctx, Command{Args: args, Dir: staging})
	if err != nil {
		return err
	}
	commit := strings.TrimSpace(res.Stdout)
	// Publish only after the complete metadata exists. Replaying after a crash
	// sees either the old head/in-progress merge or the new commit AND manifest.
	if err := writeManifest(gitDir, commit, m); err != nil {
		return err
	}
	if err := a.updateRef(ctx, gitDir, stagingBranch, commit); err != nil {
		return err
	}
	return a.finishMerge(ctx, staging)
}

// Apply makes h.ParentDir equal the staging branch's committed result. It
// refuses while a merge is unresolved.
func (a *WorkingArea) Apply(ctx context.Context, h engine.GatherHandle) error {
	_, staging, err := a.gatherDirs(ctx, h)
	if err != nil {
		return err
	}
	gitDir := filepath.Join(staging, ".git")
	if _, inMerge := readMergeHead(gitDir); inMerge {
		return fmt.Errorf("git working area: apply: %w", ErrMergeInProgress)
	}
	if err := a.verifyPendingMerge(ctx, staging); err != nil {
		return err
	}
	target, err := a.revParse(ctx, gitDir, stagingBranch)
	if err != nil {
		return fmt.Errorf("git working area: apply: %w", err)
	}
	m, err := a.gatherManifest(ctx, gitDir, target)
	if err != nil {
		return err
	}
	if err := a.syncArea(ctx, gitDir, target, h.ParentDir, m); err != nil {
		return fmt.Errorf("git working area: apply: %w", err)
	}
	return nil
}

// Rollback restores h.ParentDir to the content recorded by BeginGather,
// including empty directories. It does not need the staging area.
func (a *WorkingArea) Rollback(ctx context.Context, h engine.GatherHandle) error {
	backing, _, err := a.gatherDirs(ctx, h)
	if err != nil {
		return err
	}
	target, err := a.revParse(ctx, backing, h.RollbackRef)
	if err != nil {
		return fmt.Errorf("git working area: rollback: %w", err)
	}
	m, err := a.gatherManifest(ctx, backing, target)
	if err != nil {
		return fmt.Errorf("git working area: rollback: %w", err)
	}
	if err := a.syncArea(ctx, backing, target, h.ParentDir, m); err != nil {
		return fmt.Errorf("git working area: rollback: %w", err)
	}
	return nil
}

// ReleaseRef deletes one refs/arbitron/* ref. Releasing a rollback ref also
// removes that gather's staging area. Unknown handle or ref: no error.
func (a *WorkingArea) ReleaseRef(ctx context.Context, snapshotHandle, ref string) error {
	backing, err := a.BackingDir(snapshotHandle)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(ref, "refs/arbitron/") {
		return fmt.Errorf("git working area: release ref: %q is not an arbitron ref", ref)
	}
	if _, err := os.Stat(backing); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	exists, err := a.refExists(ctx, backing, ref)
	if err != nil {
		return fmt.Errorf("git working area: release ref: %w", err)
	}
	if exists {
		if _, err := a.git.Run(ctx, Command{Args: []string{"update-ref", "-d", ref}, GitDir: backing}); err != nil {
			return fmt.Errorf("git working area: release ref: %w", err)
		}
	}
	if strings.HasPrefix(ref, RollbackRefPrefix) {
		if id, err := uuid.Parse(strings.TrimPrefix(ref, RollbackRefPrefix)); err == nil {
			if staging, err := a.StagingDir(snapshotHandle, id); err == nil {
				if err := removeAreaTree(staging); err != nil {
					return fmt.Errorf("git working area: release staging: %w", err)
				}
			}
		}
	}
	return nil
}

// syncArea converges to the committed bytes and manifest, never entering .git.
// After metadata preflight, make content directories writable, delete obsolete
// paths and prune empty directories BEFORE checkout (file/directory transitions
// must not follow an old symlink). Then recreate empty dirs and restore all
// ordinary permissions, with directories/root last. Re-running after any partial
// write converges; Rollback uses the same algorithm and its original manifest.
func (a *WorkingArea) syncArea(ctx context.Context, gitDir, commit, dir string, m manifest) error {
	root, err := resolveDir(dir)
	if err != nil {
		return err
	}
	targetFiles, targetDirs, err := a.treeListing(ctx, gitDir, commit)
	if err != nil {
		return err
	}
	current, err := scanArea(root, a.skipInside(root))
	if err != nil {
		return err
	}
	permissions := make(map[string]uint32, len(m.Permissions))
	for p, mode := range m.Permissions {
		permissions[p] = mode
	}
	m.Permissions = permissions
	for _, ge := range current.GitEntries {
		for _, anc := range slashAncestors(ge.Rel) {
			if targetFiles[anc] {
				return fmt.Errorf("%w: %q would replace the directory holding %q", ErrApplyTouchesMetadata, anc, ge.Rel)
			}
			// Metadata-only directories cannot be pruned; preserve their modes.
			if _, ok := m.Permissions[anc]; !ok {
				m.Permissions[anc] = current.Permissions[anc]
			}
		}
	}
	if err := a.writableContent(root); err != nil {
		return err
	}
	var removedFrom []string
	for rel := range current.Permissions {
		if rel != "." {
			removedFrom = append(removedFrom, rel)
		}
	}
	for i := len(current.Files) - 1; i >= 0; i-- {
		f := current.Files[i]
		if targetFiles[f.Rel] {
			continue
		}
		if err := os.Remove(f.Abs); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", f.Rel, err)
		}
		removedFrom = append(removedFrom, slashAncestors(f.Rel)...)
	}
	keep := map[string]bool{}
	for _, k := range m.EmptyDirs {
		keep[k] = true
		for _, ancestor := range slashAncestors(k) {
			keep[ancestor] = true
		}
	}
	sort.Slice(removedFrom, func(i, j int) bool { return len(removedFrom[i]) > len(removedFrom[j]) })
	for _, d := range removedFrom {
		if targetDirs[d] || keep[d] {
			continue
		}
		// os.Remove only removes EMPTY directories: anything still holding a
		// file, a .git entry or a kept directory stays.
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(d)))
	}
	if err := a.checkoutCommit(ctx, gitDir, filepath.Join(gitDir, "arbitron", "tmp"), commit, root); err != nil {
		return err
	}
	if err := createEmptyDirs(root, m.EmptyDirs); err != nil {
		return err
	}
	return a.restoreGatherPermissions(ctx, root, m.Permissions)
}

// restoreGatherPermissions also handles a parent that is the workspace root:
// the private backing store is never content and must never be chmod'ed.
func (a *WorkingArea) restoreGatherPermissions(ctx context.Context, root string, permissions map[string]uint32) error {
	if a.skipInside(root) == "" {
		return restoreAreaPermissions(ctx, root, permissions)
	}
	if err := validatePermissions(permissions); err != nil {
		return err
	}
	content, err := scanArea(root, a.skipInside(root))
	if err != nil {
		return err
	}
	if len(content.Permissions) != len(permissions) {
		return errors.New("git working area: incomplete permissions manifest")
	}
	for rel := range permissions {
		if _, ok := content.Permissions[rel]; !ok {
			return fmt.Errorf("git working area: missing permissions path %q", rel)
		}
	}
	var paths []string
	for _, f := range content.Files {
		if f.Mode != modeSymlink {
			paths = append(paths, f.Rel)
		}
	}
	var dirs []string
	files := map[string]bool{}
	for _, p := range paths {
		files[p] = true
	}
	for p := range content.Permissions {
		if !files[p] {
			dirs = append(dirs, p)
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i] == "." {
			return false
		}
		if dirs[j] == "." {
			return true
		}
		return strings.Count(dirs[i], "/") > strings.Count(dirs[j], "/")
	})
	for _, p := range append(paths, dirs...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(root, filepath.FromSlash(p)), os.FileMode(permissions[p])); err != nil {
			return err
		}
	}
	return nil
}

// pendingMerge keeps the pre-merge identity and intended metadata durable before
// chmod or HEAD publication. A restart never reads a mutable task workspace.
type pendingMerge struct {
	Head     string    `json:"head"`
	Task     string    `json:"task"`
	Manifest *manifest `json:"manifest,omitempty"`
	Ready    bool      `json:"ready"`
}

// A clean index does not prove that a merge completed: abort/reset/quit can
// remove MERGE_HEAD without incorporating the task. The journal must match
// either the active merge or a published commit containing BOTH parents.
// The latter permits replay after HEAD publication but before journal cleanup.
func (a *WorkingArea) verifyPendingMerge(ctx context.Context, staging string) error {
	gitDir := filepath.Join(staging, ".git")
	var pending pendingMerge
	if err := readJSON(filepath.Join(gitDir, mergeFile), &pending); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	mergeHead, inMerge := readMergeHead(gitDir)
	if inMerge && mergeHead != pending.Task {
		return engine.ErrPendingMergeChanged
	}
	head, err := a.revParse(ctx, gitDir, "HEAD")
	if err != nil {
		return err
	}
	if head == pending.Head {
		if inMerge {
			return nil
		}
		return engine.ErrPendingMergeChanged
	}
	for _, parent := range []string{pending.Head, pending.Task} {
		if _, err := a.git.Run(ctx, Command{Args: []string{"merge-base", "--is-ancestor", parent, "HEAD"}, Dir: staging}); err != nil {
			if IsExitCode(err, 1) {
				return engine.ErrPendingMergeChanged
			}
			return err
		}
	}
	return nil
}

func (a *WorkingArea) finishMerge(ctx context.Context, staging string) error {
	gitDir := filepath.Join(staging, ".git")
	if _, ok := readMergeHead(gitDir); ok {
		if _, err := a.git.Run(ctx, Command{Args: []string{"merge", "--quit"}, Dir: staging}); err != nil {
			return err
		}
	}
	for _, name := range []string{conflictsFile, mergeFile} {
		if err := os.Remove(filepath.Join(gitDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// writableContent grants temporary directory access for byte synchronization,
// never following symlinks or entering any author metadata (including .git files).
func (a *WorkingArea) writableContent(root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != root && (isGitMetadataName(d.Name()) || p == a.skipInside(root)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			return os.Chmod(p, info.Mode().Perm()|0o700)
		}
		return nil
	})
}

// mergeManifest combines only task metadata deltas relative to the baseline.
// Unchanged task modes cannot clobber current parent modes. Git remains the
// authority for content/executable conflicts; ordinary bits resolve in task order.
func (a *WorkingArea) mergeManifest(ctx context.Context, backing, staging, head, task string) (manifest, error) {
	baseline, err := a.revParse(ctx, backing, BaselineRef)
	if err != nil {
		return manifest{}, err
	}
	base, err := a.gatherManifest(ctx, backing, baseline)
	if err != nil {
		return manifest{}, err
	}
	taskManifest, err := a.gatherManifest(ctx, backing, task)
	if err != nil {
		return manifest{}, err
	}
	previous, err := a.gatherManifest(ctx, filepath.Join(staging, ".git"), head)
	if err != nil {
		return manifest{}, err
	}
	content, err := scanArea(staging, "")
	if err != nil {
		return manifest{}, err
	}
	// Keep existing empty dirs and apply only task additions/deletions.
	empty := map[string]bool{}
	baseEmpty := map[string]bool{}
	taskEmpty := map[string]bool{}
	for _, p := range previous.EmptyDirs {
		empty[p] = true
	}
	for _, p := range base.EmptyDirs {
		baseEmpty[p] = true
	}
	for _, p := range taskManifest.EmptyDirs {
		taskEmpty[p] = true
		if !baseEmpty[p] {
			empty[p] = true
		}
	}
	for p := range baseEmpty {
		if !taskEmpty[p] {
			delete(empty, p)
		}
	}
	files := map[string]bool{}
	for _, f := range content.Files {
		files[f.Rel] = true
	}
	for p := range empty {
		blocked := files[p]
		for _, ancestor := range slashAncestors(p) {
			blocked = blocked || files[ancestor]
		}
		if blocked {
			delete(empty, p)
			continue
		}
		if _, ok := content.Permissions[p]; !ok {
			content.Permissions[p] = taskManifest.Permissions[p]
		}
		for _, ancestor := range slashAncestors(p) {
			if _, ok := content.Permissions[ancestor]; !ok {
				content.Permissions[ancestor] = taskManifest.Permissions[ancestor]
			}
		}
	}
	for rel, actual := range content.Permissions {
		mode, ok := previous.Permissions[rel]
		if !ok {
			mode = actual
		}
		if changed, present := taskManifest.Permissions[rel]; present {
			if original, existed := base.Permissions[rel]; !existed || original != changed {
				mode = changed
			}
		}
		if files[rel] && (mode&0o111 != 0) != (actual&0o111 != 0) {
			// Git decides the executable class, not the ordinary per-user bits.
			mode = mode&^0o111 | actual&0o111
		}
		content.Permissions[rel] = mode
	}
	m := manifest{Permissions: content.Permissions, EmptyDirs: []string{}}
	for p := range empty {
		m.EmptyDirs = append(m.EmptyDirs, p)
	}
	sort.Strings(m.EmptyDirs)
	return m, nil
}

// gatherManifest requires permissions for every ordinary path in the immutable
// tree and its empty directories. An incomplete input fails before mutation.
func (a *WorkingArea) gatherManifest(ctx context.Context, gitDir, commit string) (manifest, error) {
	m, err := readManifest(gitDir, commit)
	if err != nil {
		return manifest{}, err
	}
	res, err := a.git.Run(ctx, Command{Args: []string{"ls-tree", "-r", "-z", "--full-tree", commit}, GitDir: gitDir})
	if err != nil {
		return manifest{}, err
	}
	expected := map[string]bool{".": true}
	for _, record := range strings.Split(res.Stdout, "\x00") {
		if record == "" {
			continue
		}
		info, p, ok := strings.Cut(record, "\t")
		if !ok {
			return manifest{}, errors.New("git working area: malformed tree listing")
		}
		if err := validateAreaPath(p); err != nil {
			return manifest{}, err
		}
		fields := strings.Fields(info)
		if len(fields) != 3 {
			return manifest{}, errors.New("git working area: malformed tree entry")
		}
		switch fields[0] {
		case modeFile, modeExec:
			expected[p] = true
		case modeSymlink:
		default:
			return manifest{}, fmt.Errorf("git working area: unsupported tree mode %s", fields[0])
		}
		for _, ancestor := range slashAncestors(p) {
			expected[ancestor] = true
		}
	}
	for _, p := range m.EmptyDirs {
		if err := validateAreaPath(p); err != nil {
			return manifest{}, err
		}
		expected[p] = true
		for _, ancestor := range slashAncestors(p) {
			expected[ancestor] = true
		}
	}
	if len(expected) != len(m.Permissions) {
		return manifest{}, errors.New("git working area: incomplete permissions manifest")
	}
	for p := range expected {
		if _, ok := m.Permissions[p]; !ok {
			return manifest{}, fmt.Errorf("git working area: missing permissions for %q", p)
		}
	}
	return m, nil
}

// treeListing returns the commit's file paths and every directory they need.
func (a *WorkingArea) treeListing(ctx context.Context, gitDir, commit string) (map[string]bool, map[string]bool, error) {
	res, err := a.git.Run(ctx, Command{Args: []string{"ls-tree", "-r", "-z", "--full-tree", "--name-only", commit}, GitDir: gitDir})
	if err != nil {
		return nil, nil, fmt.Errorf("list %s: %w", commit, err)
	}
	files := map[string]bool{}
	dirs := map[string]bool{}
	for _, p := range strings.Split(res.Stdout, "\x00") {
		if p == "" {
			continue
		}
		files[p] = true
		for _, d := range slashAncestors(p) {
			dirs[d] = true
		}
	}
	return files, dirs, nil
}

func (a *WorkingArea) unmergedPaths(ctx context.Context, staging string) ([]string, error) {
	res, err := a.git.Run(ctx, Command{Args: []string{"ls-files", "-u", "-z"}, Dir: staging})
	if err != nil {
		return nil, fmt.Errorf("git working area: list conflicts: %w", err)
	}
	set := map[string]bool{}
	for _, rec := range strings.Split(res.Stdout, "\x00") {
		if _, p, ok := strings.Cut(rec, "\t"); ok && p != "" {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// gatherDirs validates a handle against this area and returns its backing
// and staging directories.
func (a *WorkingArea) gatherDirs(ctx context.Context, h engine.GatherHandle) (string, string, error) {
	backing, err := a.BackingDir(h.SnapshotHandle)
	if err != nil {
		return "", "", err
	}
	staging, err := a.StagingDir(h.SnapshotHandle, h.GatherID)
	if err != nil {
		return "", "", err
	}
	if h.RollbackRef != RollbackRefPrefix+h.GatherID.String() {
		return "", "", fmt.Errorf("git working area: handle rollback ref does not belong to gather %s", h.GatherID)
	}
	if h.StagingDir != staging {
		return "", "", fmt.Errorf("git working area: gather handle staging dir %q does not belong to this area", h.StagingDir)
	}
	// The rollback manifest is the durable authority for the immediate parent.
	// It remains available even if the staging repository was removed.
	id, err := a.revParse(ctx, backing, h.RollbackRef)
	if err != nil {
		return "", "", err
	}
	m, err := a.gatherManifest(ctx, backing, id)
	if err != nil {
		return "", "", err
	}
	parent, err := resolveDir(h.ParentDir)
	if err != nil {
		return "", "", err
	}
	if parent != m.Root {
		return "", "", errors.New("git working area: gather parent differs from rollback snapshot")
	}
	// Every staging operation starts here, after any resolver turn: restore
	// the engine-owned config and attributes before git runs in staging.
	if err := sealStaging(staging); err != nil {
		return "", "", fmt.Errorf("git working area: %w", err)
	}
	return backing, staging, nil
}

func readMergeHead(gitDir string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(gitDir, "MERGE_HEAD"))
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(firstLine(string(raw))), true
}

// hasConflictMarkers reports whether a text file still holds both an opening
// and a closing conflict marker line. Binary and missing files report false:
// for them the unmerged index entry is the only signal.
func hasConflictMarkers(p string) bool {
	info, err := os.Lstat(p)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	head := raw
	if len(head) > binaryProbeBytes {
		head = head[:binaryProbeBytes]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return false
	}
	var open, closed bool
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 64*1024), len(raw)+1)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "<<<<<<<") {
			open = true
		}
		if open && strings.HasPrefix(line, ">>>>>>>") {
			closed = true
		}
	}
	return open && closed
}

func writeJSON(p string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "gather-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

func readJSON(p string, v any) error {
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
