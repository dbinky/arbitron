package filesystem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Compile-time assertion: LibraryStore satisfies workflow.LibraryStore.
var _ workflow.LibraryStore = (*LibraryStore)(nil)

// libFileMode is the permission of every file the store writes, matching
// workflow-scoped files.
const libFileMode os.FileMode = 0o644

// LibraryStore implements workflow.LibraryStore over each scope root's _lib
// folder: basePath/users/<ownerID>/_lib (private) and basePath/shared/_lib
// (shared). Reads follow the run-time reader's containment rules. Writes never
// follow a symlink: a link at the library folder, at any ancestor, or at the
// target is refused rather than replaced, and the store offers no way to
// create one.
type LibraryStore struct {
	basePath string
	files    durableFiles
}

// NewLibraryStore creates a LibraryStore rooted at the given directory.
func NewLibraryStore(basePath string) *LibraryStore {
	return &LibraryStore{basePath: basePath}
}

// ListFiles returns the scope's library files, sorted and slash-separated.
// Internal-artifact trees, reserved names, write temp files, and links that
// dangle, escape the library, or name a directory are omitted.
func (s *LibraryStore) ListFiles(ctx context.Context, scope workflow.Scope) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scopeRoot, err := scopeRootDir(s.basePath, scope)
	if err != nil {
		return nil, err
	}
	libDir := filepath.Join(scopeRoot, workflow.LibraryFolder)
	info, err := os.Lstat(libDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("listing library: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: library folder %q is not a plain directory", workflow.ErrInvalidLibPath, libDir)
	}
	realLib, err := filepath.EvalSymlinks(libDir)
	if err != nil {
		return nil, fmt.Errorf("listing library: %w", err)
	}

	files := []string{}
	err = filepath.WalkDir(libDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == libDir {
			return nil
		}
		rel, err := filepath.Rel(libDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if reservedWorkflowFile(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if validAdapterLibRel(rel) != nil || writeTempArtifact(d.Name()) {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			// WalkDir never descends a link; report it only when it names a
			// regular file inside the library.
			real, err := filepath.EvalSymlinks(path)
			if err != nil || !insideLib(real, realLib) {
				return nil
			}
			target, err := os.Stat(real)
			if err != nil || !target.Mode().IsRegular() {
				return nil
			}
		} else if !d.Type().IsRegular() {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing library: %w", err)
	}
	sort.Strings(files)
	return files, nil
}

// ReadFile reads rel from exactly the given scope's library.
func (s *LibraryStore) ReadFile(ctx context.Context, scope workflow.Scope, rel string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scopeRoot, err := scopeRootDir(s.basePath, scope)
	if err != nil {
		return nil, err
	}
	real, found, err := resolveLibCandidate(scopeRoot, rel)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("%w: library file %s", workflow.ErrFileNotFound, rel)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return nil, fmt.Errorf("reading library file %q: %w", rel, err)
	}
	return data, nil
}

// WriteFile atomically replaces rel, creating the library and any parent
// directories on demand. A private library is created inside the owner's
// partition with partition permissions.
func (s *LibraryStore) WriteFile(ctx context.Context, scope workflow.Scope, rel string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if hasControlChar(rel) {
		return fmt.Errorf("%w: control character in path", workflow.ErrInvalidLibPath)
	}
	if writeTempArtifact(filepath.Base(rel)) {
		return fmt.Errorf("%w: reserved temporary file name", workflow.ErrInvalidLibPath)
	}
	scopeRoot, err := scopeRootDir(s.basePath, scope)
	if err != nil {
		return err
	}
	// Validation and containment of what already exists, shared with reads.
	if _, _, err := resolveLibCandidate(scopeRoot, rel); err != nil {
		return err
	}
	if _, err := ensureScopeRootDir(s.basePath, scope); err != nil {
		return err
	}
	target, err := ensureLibParents(scopeRoot, rel)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %q is not a regular file", workflow.ErrInvalidLibPath, rel)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("writing library file %q: %w", rel, err)
	}
	if err := s.files.write(ctx, target, content, libFileMode); err != nil {
		return fmt.Errorf("writing library file %q: %w", rel, err)
	}
	return nil
}

// DeleteFile removes rel from the given scope's library. A link inside the
// library is removed itself; its target is left alone.
func (s *LibraryStore) DeleteFile(ctx context.Context, scope workflow.Scope, rel string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	scopeRoot, err := scopeRootDir(s.basePath, scope)
	if err != nil {
		return err
	}
	_, found, err := resolveLibCandidate(scopeRoot, rel)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: library file %s", workflow.ErrFileNotFound, rel)
	}
	full := filepath.Join(scopeRoot, workflow.LibraryFolder, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil {
		return fmt.Errorf("deleting library file %q: %w", rel, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: %q is a directory", workflow.ErrInvalidLibPath, rel)
	}
	if err := os.Remove(full); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: library file %s", workflow.ErrFileNotFound, rel)
		}
		return fmt.Errorf("deleting library file %q: %w", rel, err)
	}
	return syncDir(filepath.Dir(full))
}

// ensureLibParents creates the library folder and rel's parent directories,
// refusing any existing component that is a symlink or not a directory, and
// returns the target file path.
func ensureLibParents(scopeRoot, rel string) (string, error) {
	cur := filepath.Join(scopeRoot, workflow.LibraryFolder)
	parts := strings.Split(rel, "/")
	for i := 0; ; i++ {
		info, err := os.Lstat(cur)
		switch {
		case os.IsNotExist(err):
			if err := durableMkdir(cur); err != nil {
				return "", fmt.Errorf("creating library directory for %q: %w", rel, err)
			}
		case err != nil:
			return "", fmt.Errorf("%w: %q: %v", workflow.ErrInvalidLibPath, rel, err)
		case !info.IsDir():
			// Lstat reports a symlinked directory as a link, never a dir.
			return "", fmt.Errorf("%w: %q crosses a symlink or file", workflow.ErrInvalidLibPath, rel)
		}
		if i == len(parts)-1 {
			return filepath.Join(cur, parts[i]), nil
		}
		cur = filepath.Join(cur, parts[i])
	}
}

// hasControlChar reports whether p contains an ASCII control character.
func hasControlChar(p string) bool {
	return strings.ContainsFunc(p, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// writeTempArtifact reports whether name is a temp file left by an
// interrupted durableFiles.write.
func writeTempArtifact(name string) bool {
	return strings.HasPrefix(name, ".write-") && strings.HasSuffix(name, ".tmp")
}
