// Package filesystem provides file-based adapters for Arbitron repositories.
package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Compile-time assertion: WorkflowRepo satisfies workflow.Repository.
var _ workflow.Repository = (*WorkflowRepo)(nil)

// userScopeDirMode is the permission applied to the per-user partition root
// (and its users/ parent). It mirrors workspace.go's per-instance dir mode:
// these dirs can hold one user's private workflows and must not be group- or
// world-readable.
const userScopeDirMode os.FileMode = 0o700

// WorkflowRepo implements workflow.Repository by reading workflow
// definitions from the local filesystem. Workflows live under a scope root:
// basePath/users/<ownerID>/<name>/ for private workspaces and
// basePath/shared/<name>/ for the shared (published) space. Each workflow
// contains a workflow.json manifest.
type WorkflowRepo struct {
	basePath    string
	locks       manifestLocks
	readSettler workflow.DestinationSettler
	files       durableFiles
}

// NewWorkflowRepo creates a WorkflowRepo rooted at the given directory.
func NewWorkflowRepo(basePath string) *WorkflowRepo {
	return &WorkflowRepo{basePath: basePath}
}

// scopeRoot returns the directory that contains workflow folders for the
// given scope: basePath/users/<ownerID> for Private, basePath/shared for
// Shared. It does not create the directory.
func (r *WorkflowRepo) scopeRoot(scope workflow.Scope) (string, error) {
	switch scope.Kind {
	case workflow.ScopePrivate:
		if scope.OwnerID == uuid.Nil {
			return "", fmt.Errorf("%w: private scope requires owner", workflow.ErrInvalidName)
		}
		return filepath.Join(r.basePath, "users", scope.OwnerID.String()), nil
	case workflow.ScopeShared:
		return filepath.Join(r.basePath, "shared"), nil
	default:
		return "", fmt.Errorf("%w: unknown scope kind", workflow.ErrInvalidName)
	}
}

// ensureScopeRoot creates the scope root dir (and, for private scopes, the
// parent users/ dir) with 0o700 perms, chmodding to defeat umask relaxation,
// matching adapter/filesystem/workspace.go's security precedent.
func (r *WorkflowRepo) ensureScopeRoot(scope workflow.Scope) (string, error) {
	scopeRoot, err := r.scopeRoot(scope)
	if err != nil {
		return "", err
	}
	if err := durableMkdir(scopeRoot); err != nil {
		return "", fmt.Errorf("creating scope root: %w", err)
	}
	if scope.Kind == workflow.ScopePrivate {
		// Tighten the per-user dir and its users/ parent (MkdirAll honours umask).
		if err := os.Chmod(filepath.Dir(scopeRoot), userScopeDirMode); err != nil {
			return "", fmt.Errorf("tighten users dir: %w", err)
		}
		if err := os.Chmod(scopeRoot, userScopeDirMode); err != nil {
			return "", fmt.Errorf("tighten user dir: %w", err)
		}
	}
	return scopeRoot, nil
}

// resolveWorkflowDir validates a workflow name and returns the absolute path
// to its directory within the given scope. It rejects names that resolve to
// the scope root itself or escape it via path traversal (e.g., "../sibling",
// ".", "") including cross-scope escapes.
func (r *WorkflowRepo) resolveWorkflowDir(scope workflow.Scope, name string) (string, error) {
	scopeRoot, err := r.scopeRoot(scope)
	if err != nil {
		return "", err
	}
	root := filepath.Clean(scopeRoot) + string(os.PathSeparator)
	wfDir := filepath.Join(scopeRoot, name)
	if wfDir == filepath.Clean(scopeRoot) ||
		!strings.HasPrefix(wfDir+string(os.PathSeparator), root) {
		return "", fmt.Errorf("%w: %s", workflow.ErrInvalidName, name)
	}
	return wfDir, nil
}

func (r *WorkflowRepo) SetReadSettler(s workflow.DestinationSettler) { r.readSettler = s }

// Load reads and parses the workflow.json file from the named subdirectory.
func (r *WorkflowRepo) load(_ context.Context, scope workflow.Scope, name string) (*workflow.Workflow, error) {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		// Load historically returns ErrNotFound for invalid names.
		return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}

	manifestPath := filepath.Join(wfDir, "workflow.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
		}
		return nil, fmt.Errorf("reading workflow %q: %w", name, err)
	}

	var wf workflow.Workflow
	if err := json.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parsing workflow %q: %w", name, err)
	}

	return &wf, nil
}

// List scans subdirectories of the scope root and returns a summary for each
// valid workflow (i.e. each directory that contains a parseable workflow.json).
// Directories without a valid manifest are silently skipped. A scope root that
// does not exist yet (e.g. a user who has never created a private workflow)
// returns an empty slice with no error.
func (r *WorkflowRepo) List(ctx context.Context, scope workflow.Scope) ([]workflow.WorkflowSummary, error) {
	scopeRoot, err := r.scopeRoot(scope)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(scopeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return []workflow.WorkflowSummary{}, nil
		}
		return nil, fmt.Errorf("listing workflows: %w", err)
	}

	var summaries []workflow.WorkflowSummary
	for _, entry := range entries {
		if !entry.IsDir() || internalArtifact(entry.Name()) {
			continue
		}

		wf, err := r.Load(ctx, scope, entry.Name())
		if err != nil {
			var settlement *readSettlementError
			if errors.As(err, &settlement) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, workflow.ErrMutationPending) {
				return nil, err
			}
			// Preserve the legacy skip policy for malformed manifests.
			continue
		}

		summaries = append(summaries, workflow.WorkflowSummary{
			Name:        wf.Name,
			Description: wf.Description,
			Author:      wf.Author,
			NodeCount:   len(wf.Nodes),
		})
	}

	return summaries, nil
}

// ListScopes enumerates every workflow scope that exists on disk: the shared
// root (when present) and one private scope per users/<owner UUID> directory.
// It is the trusted, caller-independent producer for execution-path inventory:
// it finds owners the public registry never listed. Missing roots are valid and
// yield no scope. A users/ entry that is an unreadable directory, is not named
// by a non-nil UUID, or a root that cannot be read returns an error and no
// scopes, so a partial scan is never mistaken for a complete one.
func (r *WorkflowRepo) ListScopes(ctx context.Context) ([]workflow.Scope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var scopes []workflow.Scope

	shared, err := r.scopeRoot(workflow.Shared())
	if err != nil {
		return nil, err
	}
	switch info, err := os.Stat(shared); {
	case err == nil && info.IsDir():
		scopes = append(scopes, workflow.Shared())
	case err == nil:
		return nil, fmt.Errorf("listing scopes: %s is not a directory", shared)
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("listing scopes: %w", err)
	}

	users := filepath.Join(r.basePath, "users")
	entries, err := os.ReadDir(users)
	if err != nil {
		if os.IsNotExist(err) {
			return scopes, nil
		}
		return nil, fmt.Errorf("listing scopes: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		owner, err := uuid.Parse(entry.Name())
		if err != nil || owner == uuid.Nil {
			return nil, fmt.Errorf("listing scopes: malformed owner directory %q", entry.Name())
		}
		if _, err := os.ReadDir(filepath.Join(users, entry.Name())); err != nil {
			return nil, fmt.Errorf("listing scopes: owner %s: %w", owner, err)
		}
		scopes = append(scopes, workflow.Private(owner))
	}
	return scopes, nil
}

// LoadManifests returns every workflow in scope with its raw manifest bytes. It
// is the strict counterpart of List for execution-path inventory: a directory
// whose workflow.json cannot be read or parsed fails the whole scan instead of
// being skipped, so its model consumers never silently vanish. A directory with
// no manifest, a stray file and CopyTree's own staging/backup artifacts are not
// workflows and are skipped. A missing scope root yields no manifests.
func (r *WorkflowRepo) LoadManifests(ctx context.Context, scope workflow.Scope) ([]modelcatalog.WorkflowManifest, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scopeRoot, err := r.scopeRoot(scope)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(scopeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("loading manifests: %w", err)
	}

	var out []modelcatalog.WorkflowManifest
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || isCopyTreeArtifact(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(scopeRoot, entry.Name(), "workflow.json"))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("loading manifests: workflow %q: %w", entry.Name(), err)
		}
		var wf workflow.Workflow
		if err := json.Unmarshal(data, &wf); err != nil {
			return nil, fmt.Errorf("loading manifests: parsing workflow %q: %w", entry.Name(), err)
		}
		out = append(out, modelcatalog.WorkflowManifest{
			Scope: scope, Name: entry.Name(), Content: data, Workflow: &wf,
		})
	}
	return out, nil
}

// isCopyTreeArtifact reports whether name is a CopyTree staging (".tmp-<uuid>")
// or swap-aside (".old-<uuid>") directory, which no run ever loads.
func isCopyTreeArtifact(name string) bool {
	for _, marker := range []string{".tmp-", ".old-"} {
		if i := strings.LastIndex(name, marker); i > 0 {
			if _, err := uuid.Parse(name[i+len(marker):]); err == nil {
				return true
			}
		}
	}
	return false
}

// Create creates a new workflow directory and writes its workflow.json manifest.
// It returns an error if the workflow already exists or the name is invalid.
func (r *WorkflowRepo) create(ctx context.Context, scope workflow.Scope, name string, wf *workflow.Workflow) error {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		return err
	}

	// Fail if the directory already exists.
	if _, err := os.Stat(wfDir); err == nil {
		return fmt.Errorf("%w: %s", workflow.ErrAlreadyExists, name)
	}

	// Ensure the scope partition root exists with the correct (0o700) perms
	// before creating the per-workflow dir.
	if _, err := r.ensureScopeRoot(scope); err != nil {
		return err
	}

	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling workflow %q: %w", name, err)
	}
	staging := filepath.Join(filepath.Dir(wfDir), "."+name+".tmp-"+uuid.NewString())
	if err := os.Mkdir(staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	data = append(data, '\n')

	// Atomic write: write to a temp file then rename so a crash mid-write
	// never leaves a partial manifest visible to readers.
	manifestPath := filepath.Join(staging, "workflow.json")
	if err := r.files.write(ctx, manifestPath, data, 0o644); err != nil {
		return fmt.Errorf("committing workflow %q: %w", name, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(staging, wfDir); err != nil {
		return err
	}
	return syncDir(filepath.Dir(wfDir))
}

// Update atomically replaces the workflow.json manifest for an existing workflow.
// It returns an error if the workflow does not exist.
func (r *WorkflowRepo) update(ctx context.Context, scope workflow.Scope, name string, wf *workflow.Workflow) error {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(wfDir, "workflow.json")
	if _, err := os.Stat(manifestPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
		}
		return fmt.Errorf("checking workflow %q: %w", name, err)
	}

	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling workflow %q: %w", name, err)
	}
	data = append(data, '\n')

	// Atomic write: temp file + rename.
	if err := r.files.write(ctx, manifestPath, data, 0o644); err != nil {
		return fmt.Errorf("committing workflow %q: %w", name, err)
	}

	return nil
}

// Delete removes an entire workflow directory and all its contents.
// It returns an error if the workflow does not exist.
func (r *WorkflowRepo) delete(_ context.Context, scope workflow.Scope, name string) error {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		return err
	}

	if _, err := os.Stat(wfDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
		}
		return fmt.Errorf("checking workflow %q: %w", name, err)
	}

	if err := os.RemoveAll(wfDir); err != nil {
		return fmt.Errorf("deleting workflow %q: %w", name, err)
	}

	return syncDir(filepath.Dir(wfDir))
}

// Exists reports whether a workflow with the given name exists in scope.
func (r *WorkflowRepo) exists(_ context.Context, scope workflow.Scope, name string) (bool, error) {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		return false, err
	}
	_, statErr := os.Stat(filepath.Join(wfDir, "workflow.json"))
	if statErr == nil {
		return true, nil
	}
	if os.IsNotExist(statErr) {
		return false, nil
	}
	return false, fmt.Errorf("checking workflow %q: %w", name, statErr)
}

// WriteFile writes content to a file at relativePath inside the named workflow
// directory, creating intermediate directories as needed.
func (r *WorkflowRepo) writeFile(ctx context.Context, scope workflow.Scope, name string, relativePath string, content []byte) error {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		return err
	}

	// Verify workflow directory exists.
	if _, err := os.Stat(wfDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
		}
		return fmt.Errorf("checking workflow %q: %w", name, err)
	}

	// Path-traversal check on relativePath against the workflow directory.
	wfRoot := filepath.Clean(wfDir) + string(os.PathSeparator)
	targetPath := filepath.Join(wfDir, relativePath)
	if !strings.HasPrefix(targetPath, wfRoot) {
		return fmt.Errorf("%w: %s", workflow.ErrInvalidFilePath, relativePath)
	}

	// Protect the manifest: workflow.json is only ever written through Create
	// and Update, which use type-safe structs and atomic writes.
	// Allowing raw bytes through WriteFile would bypass schema validation and could
	// corrupt the workflow directory or leave it in an unloadable state.
	if reservedWorkflowFile(relativePath) {
		return fmt.Errorf("%w: workflow artifact is reserved", workflow.ErrInvalidFilePath)
	}

	// Create intermediate directories.
	dir := filepath.Dir(targetPath)
	if err := durableMkdir(dir); err != nil {
		return fmt.Errorf("creating directories for %q in workflow %q: %w", relativePath, name, err)
	}

	// Atomic write: write to a temp file then rename so a crash mid-write
	// never leaves an existing file in a truncated state.
	if err := r.files.write(ctx, targetPath, content, 0o644); err != nil {
		return fmt.Errorf("committing file %q in workflow %q: %w", relativePath, name, err)
	}

	return nil
}

// DeleteFile removes a single file from inside the named workflow directory.
func (r *WorkflowRepo) deleteFile(_ context.Context, scope workflow.Scope, name string, relativePath string) error {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		return err
	}

	// Verify workflow directory exists.
	if _, err := os.Stat(wfDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
		}
		return fmt.Errorf("checking workflow %q: %w", name, err)
	}

	// Path-traversal check on relativePath against the workflow directory.
	wfRoot := filepath.Clean(wfDir) + string(os.PathSeparator)
	targetPath := filepath.Join(wfDir, relativePath)
	if !strings.HasPrefix(targetPath, wfRoot) {
		return fmt.Errorf("%w: %s", workflow.ErrInvalidFilePath, relativePath)
	}

	// Protect presentation and manifest artifacts from the file API.
	if reservedWorkflowFile(relativePath) {
		return fmt.Errorf("%w: workflow artifact is reserved", workflow.ErrInvalidFilePath)
	}

	if err := os.Remove(targetPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s in workflow %s", workflow.ErrFileNotFound, relativePath, name)
		}
		return fmt.Errorf("deleting file %q in workflow %q: %w", relativePath, name, err)
	}

	return syncDir(filepath.Dir(targetPath))
}

// ListFiles returns all file paths inside the named workflow directory,
// excluding workflow.json and .tmp crash artifacts. Paths use forward slashes
// and are relative to the workflow directory.
func (r *WorkflowRepo) listFiles(_ context.Context, scope workflow.Scope, name string) ([]string, error) {
	wfDir, err := r.resolveWorkflowDir(scope, name)
	if err != nil {
		return nil, err
	}

	// Verify workflow directory exists.
	if _, err := os.Stat(wfDir); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
		}
		return nil, fmt.Errorf("checking workflow %q: %w", name, err)
	}

	var files []string
	err = filepath.WalkDir(wfDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != wfDir && (internalArtifact(d.Name()) || strings.EqualFold(d.Name(), LayoutCacheFile)) {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(wfDir, path)
		if err != nil {
			return err
		}

		// Use forward slashes regardless of OS.
		rel = filepath.ToSlash(rel)

		// Exclude the manifest file.
		if rel == "workflow.json" || rel == LayoutCacheFile {
			return nil
		}

		// Exclude .tmp crash artifacts left by interrupted atomic writes.
		if strings.HasSuffix(rel, ".tmp") {
			return nil
		}

		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing files in workflow %q: %w", name, err)
	}

	return files, nil
}

// pathExists reports whether a path exists on disk.
func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// copyDirRecursive copies the directory tree at src into dst, creating
// directories 0o755 and files 0o644 and skipping .tmp crash artifacts.
func copyDirRecursive(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		sPath := filepath.Join(src, e.Name())
		dPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDirRecursive(sPath, dPath); err != nil {
				return err
			}
			continue
		}
		// Skip interrupted-atomic-write artifacts so a copy never carries them.
		if strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		if err := copyFile(sPath, dPath); err != nil {
			return err
		}
	}
	return nil
}

// copyFile copies a single file's bytes from src to dst with mode 0o644.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// isTreeCopyArtifact recognizes only generated staging/backup names, not ordinary
// dot-prefixed workflow names that were already supported by the repository.
func isTreeCopyArtifact(name string) bool {
	if !strings.HasPrefix(name, ".") {
		return false
	}
	for _, marker := range []string{".tmp-", ".old-"} {
		if i := strings.LastIndex(name, marker); i >= 0 {
			if _, err := uuid.Parse(name[i+len(marker):]); err == nil {
				return true
			}
		}
	}
	return false
}

// CopyTreeStamped stamps a hidden staging copy before replacing the visible
// destination. The caller supplies final identity and operation-wide lock context.
func (r *WorkflowRepo) CopyTreeStamped(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	if stamp == nil {
		return fmt.Errorf("workflow: stamped copy requires a stamp callback")
	}
	return r.copyTree(ctx, src, srcName, dst, dstName, overwrite, stamp)
}

// CopyTree atomically copies an entire workflow folder from src scope/name to
// dst scope/name. It stages the copy in a temp dir under the destination scope
// root and renames it into place, so a destination folder never appears
// half-written. Both endpoints are resolved through resolveWorkflowDir, which
// pins each side to its scope root (no cross-partition escape).
func (r *WorkflowRepo) CopyTree(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	return r.copyTree(ctx, src, srcName, dst, dstName, overwrite, nil)
}

func (r *WorkflowRepo) copyTree(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	return r.WithMutations(ctx, []workflow.MutationTarget{{Scope: src, Name: srcName}, {Scope: dst, Name: dstName}}, func(owned context.Context) error {
		return r.copyTreeLocked(owned, src, srcName, dst, dstName, overwrite, stamp)
	})
}

func (r *WorkflowRepo) copyTreeLocked(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	srcDir, err := r.resolveWorkflowDir(src, srcName)
	if err != nil {
		return err
	}
	if ok, _ := pathExists(filepath.Join(srcDir, "workflow.json")); !ok {
		return fmt.Errorf("%w: %s", workflow.ErrNotFound, srcName)
	}

	if _, err := r.ensureScopeRoot(dst); err != nil {
		return err
	}
	dstDir, err := r.resolveWorkflowDir(dst, dstName)
	if err != nil {
		return err
	}
	if ok, _ := pathExists(dstDir); ok && !overwrite {
		return fmt.Errorf("%w: %s", workflow.ErrAlreadyExists, dstName)
	}

	// Stage into a temp dir under the same scope root, then rename into place.
	staging := filepath.Join(filepath.Dir(dstDir), "."+filepath.Base(dstDir)+".tmp-"+uuid.NewString())
	if err := copyDirRecursive(srcDir, staging); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("staging copy of %q: %w", srcName, err)
	}

	defer os.RemoveAll(staging)
	if stamp != nil {
		manifest := filepath.Join(staging, "workflow.json")
		data, err := os.ReadFile(manifest)
		if err != nil {
			return err
		}
		var wf workflow.Workflow
		if err := json.Unmarshal(data, &wf); err != nil {
			return err
		}
		wf.Name = dstName
		if err := stamp(&wf); err != nil {
			return err
		}
		data, err = json.MarshalIndent(&wf, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(manifest, append(data, '\n'), 0o644); err != nil {
			return err
		}
	}
	if err := syncTree(ctx, staging); err != nil {
		return err
	}

	if overwrite {
		// Swap the old tree aside; the caller's TreeCommitter retains the durable
		// backup through the publication transaction.
		backup := filepath.Join(filepath.Dir(dstDir), "."+filepath.Base(dstDir)+".old-"+uuid.NewString())
		hadOld := false
		if ok, _ := pathExists(dstDir); ok {
			if err := os.Rename(dstDir, backup); err != nil {
				_ = os.RemoveAll(staging)
				return fmt.Errorf("swap aside %q: %w", dstName, err)
			}
			hadOld = true
		}
		if err := os.Rename(staging, dstDir); err != nil {
			if hadOld {
				err = errors.Join(err, os.Rename(backup, dstDir))
			}
			return fmt.Errorf("commit %q: %w", dstName, err)
		}
		if err := syncDir(filepath.Dir(dstDir)); err != nil {
			return err
		}
		if hadOld {
			if err := os.RemoveAll(backup); err != nil {
				return err
			}
		}
		return syncDir(filepath.Dir(dstDir))
	}

	if err := os.Rename(staging, dstDir); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("commit %q: %w", dstName, err)
	}
	return syncDir(filepath.Dir(dstDir))
}
