package engine

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
)

// ResolveGroupRepoPath resolves a parallel_group repoPath at launch.
//
// raw is the author's value (P1-T1 GroupConfig.RepoPath), possibly templated;
// templates resolve against the PARENT's live store (vars). The result is a
// cleaned, slash-separated path relative to parentWorkspace: "" when no hint is
// declared, "." for the whole area. An absolute result is accepted only when it
// lies inside parentWorkspace (e.g. "{{workspace}}/src"). Anything that cannot
// stay inside the immediate parent's working area is ErrGroupRepoPathInvalid;
// an unresolved reference additionally wraps variable.ErrUnresolvedReference.
// This is the lexical half; the file working area's GatherPreflighter checks
// the resolved path physically (symlinks, existence, directory).
func ResolveGroupRepoPath(raw string, vars *variable.VariableStore, parentWorkspace string) (string, error) {
	if raw == "" {
		return "", nil
	}
	resolved := raw
	if strings.Contains(raw, "{{") {
		if vars == nil {
			return "", fmt.Errorf("%w: repoPath %q is templated but the parent's variables are unavailable", ErrGroupRepoPathInvalid, raw)
		}
		v, err := variable.ResolveTyped(raw, vars)
		if err != nil {
			return "", fmt.Errorf("%w: repoPath %q: %w", ErrGroupRepoPathInvalid, raw, err)
		}
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("%w: repoPath %q resolved to a %T, not a path", ErrGroupRepoPathInvalid, raw, v)
		}
		resolved = s
	}
	resolved = strings.TrimSpace(resolved)
	switch {
	case resolved == "":
		return "", fmt.Errorf("%w: repoPath %q is blank once resolved", ErrGroupRepoPathInvalid, raw)
	case strings.ContainsRune(resolved, 0):
		return "", fmt.Errorf("%w: repoPath %q contains a NUL byte", ErrGroupRepoPathInvalid, raw)
	case strings.HasPrefix(resolved, `\`):
		return "", fmt.Errorf("%w: repoPath %q must be relative to the parent's working area", ErrGroupRepoPathInvalid, raw)
	}
	slashed := filepath.ToSlash(resolved)
	for _, seg := range strings.Split(slashed, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: repoPath %q (resolved %q) contains a parent traversal",
				ErrGroupRepoPathInvalid, raw, resolved)
		}
	}
	if filepath.IsAbs(resolved) {
		root := strings.TrimSpace(parentWorkspace)
		if root == "" {
			return "", fmt.Errorf("%w: repoPath %q resolved to the absolute path %q, and the parent run has no working area to contain it",
				ErrGroupRepoPathInvalid, raw, resolved)
		}
		rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(resolved))
		if err != nil || relClimbsOut(rel) {
			return "", fmt.Errorf("%w: repoPath %q resolved to %q, outside the parent's working area %q",
				ErrGroupRepoPathInvalid, raw, resolved, root)
		}
		return filepath.ToSlash(rel), nil
	}
	return path.Clean(slashed), nil
}

// relClimbsOut reports whether a filepath.Rel result leaves its base.
func relClimbsOut(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
