package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrGroupSharedPath is returned when a task would start with a variable that
// points INTO the parent's working area. Every task gets its own fork of that
// area; a path back into the parent's mutable copy would let parallel tasks
// write the same files and defeat the isolation the group promises.
var ErrGroupSharedPath = errors.New("task variable points into the parent's working area")

// ComposeTaskVariables builds ONE task's starting variables.
//
//  1. A deep copy of the group-start snapshot, minus every reserved name: the
//     engine re-seeds "workspace" for the task's own run, so the parent's path
//     must never ride along.
//  2. Overlaid with the task's inputs, resolved by the typed resolver against
//     the START values plus workspace = taskWorkspace. A whole-value
//     "{{path}}" keeps its type; "{{workspace}}/repo" binds to the TASK's own
//     area (task-local path binding); a missing reference is an error.
//  3. When parentWorkspace is set, refused if any composed value
//     (including nested inherited values) is an absolute path equal to or
//     inside the parent's area. Arbitrary strings are not rewritten — the
//     author is told to use {{workspace}} instead.
//
// The returned map shares nothing with start or with any other task's map.
func ComposeTaskVariables(start map[string]any, task workflow.GroupTask, taskWorkspace, parentWorkspace string) (map[string]any, error) {
	for _, k := range sortedMapKeys(task.Inputs) {
		if variable.ReservedInputNames[k] {
			return nil, fmt.Errorf("task %q: %w: input %q is seeded by the engine for every task", task.Key, workflow.ErrGroupReservedInput, k)
		}
	}

	composed := variable.DeepCopyMap(start)
	if composed == nil {
		composed = map[string]any{}
	}
	for k := range variable.ReservedInputNames {
		delete(composed, k)
	}

	store := variable.NewStore()
	for k, v := range composed {
		if err := store.SetInput(k, v); err != nil {
			return nil, fmt.Errorf("task %q: seed resolution store: %w", task.Key, err)
		}
	}
	if taskWorkspace != "" {
		store.SetReserved(ReservedVarWorkspace, taskWorkspace)
	}

	resolved, err := variable.ResolveTypedMap(task.Inputs, store)
	if err != nil {
		return nil, fmt.Errorf("task %q inputs: %w", task.Key, err)
	}
	for k, v := range resolved {
		composed[k] = v
	}

	if parentWorkspace != "" {
		if err := rejectSharedPaths(task.Key, composed, parentWorkspace); err != nil {
			return nil, err
		}
	}
	return composed, nil
}

// rejectSharedPaths enforces step 3 of ComposeTaskVariables. Keys are visited
// in sorted order so the reported key is deterministic.
func rejectSharedPaths(taskKey string, composed map[string]any, parentWorkspace string) error {
	root := filepath.Clean(parentWorkspace)
	for _, k := range sortedMapKeys(composed) {
		if at, p, found := findSharedPath(composed[k], root, k); found {
			return sharedPathError(taskKey, at, p, root)
		}
	}
	return nil
}

func findSharedPath(v any, root, at string) (string, string, bool) {
	switch t := v.(type) {
	case string:
		if pathWithin(t, root) {
			return at, t, true
		}
	case map[string]any:
		for _, k := range sortedMapKeys(t) {
			if a, p, ok := findSharedPath(t[k], root, at+"."+k); ok {
				return a, p, true
			}
		}
	case []any:
		for i, e := range t {
			if a, p, ok := findSharedPath(e, root, at+"."+strconv.Itoa(i)); ok {
				return a, p, true
			}
		}
	case []string:
		for i, e := range t {
			if pathWithin(e, root) {
				return at + "." + strconv.Itoa(i), e, true
			}
		}
	case []map[string]any:
		for i, m := range t {
			if a, p, ok := findSharedPath(m, root, at+"."+strconv.Itoa(i)); ok {
				return a, p, true
			}
		}
	}
	return "", "", false
}

// pathWithin reports whether s is an absolute path equal to or below root,
// including aliases of either path. Unresolvable paths fail closed.
// Only absolute paths are considered: provenance is never inferred from an
// arbitrary relative string.
func pathWithin(s, root string) bool {
	if !filepath.IsAbs(s) {
		return false
	}
	c := filepath.Clean(s)
	if c == root || strings.HasPrefix(c, strings.TrimRight(root, string(filepath.Separator))+string(filepath.Separator)) {
		return true
	}
	realRoot, err := resolveGroupVariablePath(filepath.Clean(root))
	if err != nil {
		return true
	}
	realPath, err := resolveGroupVariablePath(c)
	if err != nil {
		return true
	}
	rel, err := filepath.Rel(realRoot, realPath)
	return err == nil && !relClimbsOut(rel)
}

// Resolve existing ancestors too: an output path may name a file or directory
// that the task will create later underneath an existing symlink alias.
func resolveGroupVariablePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if !errors.Is(err, os.ErrNotExist) || filepath.Dir(path) == path {
		return resolved, err
	}
	// A dangling link is not a missing path component: opening it could create
	// its target in the parent's area. Keep the error so pathWithin refuses it.
	if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", err
	}
	parent, err := resolveGroupVariablePath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func sharedPathError(taskKey, at, value, root string) error {
	return fmt.Errorf("%w: task %q variable %q = %q is inside the parent's working area %q; every task works in its own copy — "+
		"set it from the task's inputs relative to {{workspace}} (e.g. \"{{workspace}}/repo\") instead",
		ErrGroupSharedPath, taskKey, at, value, root)
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
