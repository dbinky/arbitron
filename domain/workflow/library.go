package workflow

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	// LibraryFolder is the reserved on-disk folder holding shared library files.
	LibraryFolder = "_lib"
	// LibraryRefName is the reserved reference name used in manifests.
	LibraryRefName = "@lib"
	// LibraryPathPrefix prefixes library-relative paths in manifests.
	LibraryPathPrefix = "@lib/"
)

// ErrInvalidLibPath is wrapped by every @lib path rejection.
var ErrInvalidLibPath = errors.New("invalid library path")

// IsReservedWorkflowName reports whether name collides with the library
// folder or reference name. The check is case-insensitive.
func IsReservedWorkflowName(name string) bool {
	return strings.EqualFold(name, LibraryFolder) || strings.EqualFold(name, LibraryRefName)
}

// ParseLibPath reports whether p is an @lib path and, if so, returns its
// validated library-relative remainder. The prefix is case-sensitive. A
// non-lib path returns ("", false, nil).
func ParseLibPath(p string) (rel string, isLib bool, err error) {
	if p != LibraryRefName && !strings.HasPrefix(p, LibraryPathPrefix) {
		return "", false, nil
	}
	rel = strings.TrimPrefix(p, LibraryPathPrefix)
	if p == LibraryRefName {
		rel = ""
	}
	if err := ValidLibRel(rel); err != nil {
		return "", true, err
	}
	return rel, true, nil
}

// ValidLibRel validates a library-relative path. It rejects rather than
// normalizes: traversal, empty components, absolute paths, backslashes, NUL,
// and the reserved workflow.json / layout.json names at the top level.
func ValidLibRel(rel string) error {
	bad := func(reason string) error {
		return fmt.Errorf("%w: %s", ErrInvalidLibPath, reason)
	}
	if rel == "" {
		return bad("empty path")
	}
	if strings.HasPrefix(rel, "/") {
		return bad("absolute path")
	}
	if strings.ContainsAny(rel, "\\\x00") {
		return bad("backslash or NUL")
	}
	parts := strings.Split(rel, "/")
	for _, c := range parts {
		if c == "" || c == "." || c == ".." {
			return bad("empty, '.' or '..' component")
		}
	}
	if rel != path.Clean(rel) {
		return bad("path is not clean")
	}
	if strings.EqualFold(parts[0], "workflow.json") || strings.EqualFold(parts[0], "layout.json") {
		return bad("reserved file name")
	}
	return nil
}

// rejectReservedName returns ErrInvalidName when n collides with the shared
// library folder or its reference name, case-insensitively.
func rejectReservedName(n string) error {
	if IsReservedWorkflowName(n) {
		return fmt.Errorf("%w: %q is reserved for the shared library (use @lib)", ErrInvalidName, n)
	}
	return nil
}
