package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// rawReadSettleTimeout bounds the lock wait and settlement of one raw read; the
// engine.FileReader port carries no context.
const rawReadSettleTimeout = 30 * time.Second

// FileReaderAdapter implements engine.FileReader by reading files from the
// local filesystem. Files are located relative to a workflow's subdirectory
// under a scope root: basePath/users/<owner>/<workflowName>/<relativePath>
// (private) with a fallback to basePath/shared/<workflowName>/<relativePath>
// (shared). When owner is uuid.Nil only the shared space is searched.
type FileReaderAdapter struct {
	basePath string
	// repo, when set, gates reads of reserved artifacts (workflow.json,
	// layout.json) behind the repository's read settler so a raw read never
	// observes a staged or uncommitted tree.
	repo *WorkflowRepo
}

// SetSettledRepository routes reserved-artifact reads through repo's lock and
// read settler. The settler is looked up per read, so it may be installed on
// the repository after this call.
func (r *FileReaderAdapter) SetSettledRepository(repo *WorkflowRepo) { r.repo = repo }

// settled runs fn under the destination lock after settlement when the path is
// a reserved artifact and a settler is wired; otherwise it just runs fn.
func (r *FileReaderAdapter) settled(scope workflow.Scope, name, relativePath string, fn func() error) error {
	if r.repo == nil || r.repo.readSettler == nil || !reservedWorkflowFile(relativePath) {
		return fn()
	}
	ctx, cancel := context.WithTimeout(context.Background(), rawReadSettleTimeout)
	defer cancel()
	return r.repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: name}}, func(owned context.Context) error {
		if err := r.repo.readSettler.Settle(owned, scope, name); err != nil {
			return err
		}
		return fn()
	})
}

// NewFileReader creates a FileReaderAdapter rooted at the given directory.
func NewFileReader(basePath string) *FileReaderAdapter {
	return &FileReaderAdapter{basePath: basePath}
}

// containmentRoot returns the absolute root that file paths must stay within.
// Using filepath.Clean + separator ensures the prefix check works on all OSes.
func (r *FileReaderAdapter) containmentRoot() string {
	return filepath.Clean(r.basePath) + string(os.PathSeparator)
}

// candidateRoots returns the ordered scope roots a read should try for the
// given owner: the owner's private partition first (when owner is non-nil),
// then the shared space.
func (r *FileReaderAdapter) candidateRoots(owner uuid.UUID) []readRoot {
	var roots []readRoot
	if owner != uuid.Nil {
		roots = append(roots, readRoot{filepath.Join(r.basePath, "users", owner.String()), workflow.Private(owner)})
	}
	roots = append(roots, readRoot{filepath.Join(r.basePath, "shared"), workflow.Shared()})
	return roots
}

// readRoot pairs a scope directory with the workflow scope it represents.
type readRoot struct {
	dir   string
	scope workflow.Scope
}

// safeJoin builds the full path under the given scope root and checks that it
// is (a) inside basePath and (b) inside the specific workflow subdirectory.
// The two-level check prevents both workspace-root escapes (e.g.
// workflowName="../../etc") and cross-workflow reads (e.g.
// relativePath="../other-wf/secret.txt").
func (r *FileReaderAdapter) safeJoin(scopeRoot, workflowName, relativePath string) (string, error) {
	// Ordinary files intentionally skip settlement, so reject internal trees
	// before either probing or reading them. Require a single component to
	// prevent an alternate spelling from bypassing the artifact-name check.
	if workflowName == "" || workflowName == "." || workflowName == ".." || strings.ContainsAny(workflowName, "/\\\x00") || internalArtifact(workflowName) {
		return "", fmt.Errorf("%w: %s", workflow.ErrInvalidName, workflowName)
	}
	// Non-lib paths never reach the library folder through a workflow name.
	if workflow.IsReservedWorkflowName(workflowName) {
		return "", fmt.Errorf("%w: %q is reserved for the shared library (use @lib)", workflow.ErrInvalidName, workflowName)
	}
	wfDir := filepath.Join(scopeRoot, workflowName)
	full := filepath.Join(wfDir, relativePath)
	// Must stay inside the workspace root (catches bad workflowName).
	if !strings.HasPrefix(full, r.containmentRoot()) {
		return "", fmt.Errorf("path %q escapes workspace root", relativePath)
	}
	// Must stay inside the specific workflow directory (catches bad relativePath).
	wfRoot := filepath.Clean(wfDir) + string(os.PathSeparator)
	if !strings.HasPrefix(full, wfRoot) {
		return "", fmt.Errorf("path %q escapes workflow directory", relativePath)
	}
	return full, nil
}

// withExisting settles each candidate before checking existence or selecting a
// scope. The probe and optional read share the settlement lock, including when
// a crash left the destination absent between swap renames.
func (r *FileReaderAdapter) withExisting(owner uuid.UUID, workflowName, relativePath string, read func(string) error) (string, error) {
	if rel, isLib, err := workflow.ParseLibPath(relativePath); isLib {
		if err != nil {
			return "", err
		}
		return r.withExistingLib(owner, rel, read)
	}
	var lastErr error
	for _, root := range r.candidateRoots(owner) {
		full, err := r.safeJoin(root.dir, workflowName, relativePath)
		if err != nil {
			lastErr = err
			continue
		}
		found := false
		err = r.settled(root.scope, workflowName, relativePath, func() error {
			if _, err := os.Stat(full); err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			found = true
			if read != nil {
				return read(full)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		if found {
			return full, nil
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", os.ErrNotExist
}

// ReadFile reads the content of a file within the named workflow's directory,
// searching the owner's private partition first then the shared space.
func (r *FileReaderAdapter) ReadFile(owner uuid.UUID, workflowName, relativePath string) ([]byte, error) {
	var data []byte
	_, err := r.withExisting(owner, workflowName, relativePath, func(full string) (readErr error) {
		data, readErr = os.ReadFile(full)
		return readErr
	})
	if err != nil {
		return nil, fmt.Errorf("reading file %q in workflow %q: %w", relativePath, workflowName, err)
	}
	return data, nil
}

// ResolvePath returns the absolute path to a file within the named workflow's
// directory, searching the owner's private partition first then the shared
// space. It returns an error if the file does not exist in any scope.
func (r *FileReaderAdapter) ResolvePath(owner uuid.UUID, workflowName, relativePath string) (string, error) {
	full, err := r.withExisting(owner, workflowName, relativePath, nil)
	if err != nil {
		return "", fmt.Errorf("resolving path %q in workflow %q: %w", relativePath, workflowName, err)
	}
	return full, nil
}

// FileExists reports whether a file exists within the named workflow's
// directory in any of the owner's visible scopes.
func (r *FileReaderAdapter) FileExists(owner uuid.UUID, workflowName, relativePath string) bool {
	_, err := r.withExisting(owner, workflowName, relativePath, nil)
	return err == nil
}

// withExistingLib resolves a validated library-relative path against the
// owner's private library then the shared library, ignoring the workflow. The
// first root holding the file wins; a containment failure in any root is an
// error rather than a reason to try the next one. Library reads take no
// settlement lock because _lib holds no manifest.
func (r *FileReaderAdapter) withExistingLib(owner uuid.UUID, rel string, read func(string) error) (string, error) {
	for _, root := range r.candidateRoots(owner) {
		real, found, err := resolveLibCandidate(root.dir, rel)
		if err != nil {
			return "", err
		}
		if !found {
			continue
		}
		if read != nil {
			if err := read(real); err != nil {
				return "", err
			}
		}
		return real, nil
	}
	return "", os.ErrNotExist
}

// validAdapterLibRel applies the domain library-path rules plus the adapter's
// own internal-artifact names, which are never library content.
func validAdapterLibRel(rel string) error {
	if err := workflow.ValidLibRel(rel); err != nil {
		return err
	}
	if reservedWorkflowFile(rel) {
		return fmt.Errorf("%w: reserved file name", workflow.ErrInvalidLibPath)
	}
	return nil
}

// resolveLibCandidate resolves rel inside scopeRoot's library folder and
// returns the file's real path. found is false, with a nil error, only when
// the library folder or the file is absent, so the caller may try the next
// scope. A library folder that is a symlink or not a directory, a link that
// resolves outside the library's real path, and a dangling link all fail
// closed with ErrInvalidLibPath.
func resolveLibCandidate(scopeRoot, rel string) (real string, found bool, err error) {
	if err := validAdapterLibRel(rel); err != nil {
		return "", false, err
	}
	libDir := filepath.Join(scopeRoot, workflow.LibraryFolder)
	info, err := os.Lstat(libDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("%w: library folder %q: %v", workflow.ErrInvalidLibPath, libDir, err)
	}
	// Lstat reports a symlinked folder as a link, never as a directory.
	if !info.IsDir() {
		return "", false, fmt.Errorf("%w: library folder %q is not a plain directory", workflow.ErrInvalidLibPath, libDir)
	}
	libRoot := filepath.Clean(libDir) + string(os.PathSeparator)
	full := filepath.Join(libDir, filepath.FromSlash(rel))
	if !strings.HasPrefix(full, libRoot) {
		return "", false, fmt.Errorf("%w: %q escapes the library", workflow.ErrInvalidLibPath, rel)
	}
	realLib, err := filepath.EvalSymlinks(libDir)
	if err != nil {
		return "", false, err
	}
	if _, err := os.Lstat(full); err != nil {
		if os.IsNotExist(err) {
			// Lstat follows intermediate components, so a missing leaf may
			// sit behind a dangling or escaping directory link.
			if err := checkLibAncestors(libDir, realLib, rel); err != nil {
				return "", false, err
			}
			return "", false, nil
		}
		return "", false, fmt.Errorf("%w: %q: %v", workflow.ErrInvalidLibPath, rel, err)
	}
	real, err = filepath.EvalSymlinks(full)
	if err != nil {
		// The entry exists but does not resolve: a dangling link. Formatted
		// with %v so it never reads as os.ErrNotExist and falls through.
		return "", false, fmt.Errorf("%w: %q does not resolve: %v", workflow.ErrInvalidLibPath, rel, err)
	}
	if !insideLib(real, realLib) {
		return "", false, fmt.Errorf("%w: %q escapes the library", workflow.ErrInvalidLibPath, rel)
	}
	return real, true, nil
}

// insideLib reports whether real is realLib or sits beneath it.
func insideLib(real, realLib string) bool {
	return real == realLib || strings.HasPrefix(real, realLib+string(os.PathSeparator))
}

// checkLibAncestors walks rel's existing ancestors under libDir for a path
// whose leaf is missing. Each existing ancestor must resolve inside realLib;
// a dangling or escaping link fails closed so the miss never falls through to
// the next scope or reveals what exists outside the library.
func checkLibAncestors(libDir, realLib, rel string) error {
	cur := libDir
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		if _, err := os.Lstat(cur); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("%w: %q: %v", workflow.ErrInvalidLibPath, rel, err)
		}
		real, err := filepath.EvalSymlinks(cur)
		if err != nil {
			return fmt.Errorf("%w: %q does not resolve: %v", workflow.ErrInvalidLibPath, rel, err)
		}
		if !insideLib(real, realLib) {
			return fmt.Errorf("%w: %q escapes the library", workflow.ErrInvalidLibPath, rel)
		}
	}
	return nil
}
