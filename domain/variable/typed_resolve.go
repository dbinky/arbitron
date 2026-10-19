package variable

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrUnresolvedReference is returned by ResolveTyped / ResolveTypedMap when a
// {{path}} template names a variable or node output that does not exist.
// Resolve (the string-only substitution) silently leaves such markers in
// place; the typed resolver refuses them because a parallel group task that
// started with a literal "{{typo}}" as an input would fail far from its cause.
var ErrUnresolvedReference = errors.New("unresolved variable reference")

// ResolveTyped resolves templates inside v against store, preserving types:
//
//   - a string that is exactly one "{{path}}" reference (whitespace inside the
//     braces allowed) resolves to the referenced value with its type intact
//     (map, array, number, bool, string, null). The returned value is a deep
//     copy, so mutating it never reaches the store;
//   - any other string containing references is interpolated with Resolve
//     (maps/arrays render as JSON, numbers without a trailing ".0");
//   - map[string]any, []any, []map[string]any and []string recurse element by
//     element ([]string elements stay strings: a whole reference to a
//     non-string value is rendered the way Resolve renders it);
//   - every other value is a literal and is returned as a deep copy, unchanged.
//
// A reference that does not resolve anywhere in v fails the whole call with an
// error wrapping ErrUnresolvedReference that names the missing path and, for
// nested values, the key path where it was found (e.g. "opts.list.1"). "{{}}"
// (an empty path) is not a reference and is kept literally, as Resolve does.
// A nil store resolves nothing: every reference is missing.
func ResolveTyped(v any, store *VariableStore) (any, error) {
	if store == nil {
		store = NewStore()
	}
	return resolveTypedAt(v, store, "")
}

// ResolveTypedMap applies ResolveTyped to every value of m and returns a new
// map; m itself is never modified. A nil map yields (nil, nil). Keys are
// visited in sorted order so the reported missing reference is deterministic.
func ResolveTypedMap(m map[string]any, store *VariableStore) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	if store == nil {
		store = NewStore()
	}
	return resolveTypedMapAt(m, store, "")
}

func resolveTypedAt(v any, store *VariableStore, at string) (any, error) {
	switch t := v.(type) {
	case string:
		return resolveTypedString(t, store, at)
	case map[string]any:
		if t == nil {
			return map[string]any(nil), nil
		}
		return resolveTypedMapAt(t, store, at)
	case []any:
		if t == nil {
			return []any(nil), nil
		}
		out := make([]any, len(t))
		for i, e := range t {
			r, err := resolveTypedAt(e, store, joinKeyPath(at, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case []map[string]any:
		if t == nil {
			return []map[string]any(nil), nil
		}
		out := make([]map[string]any, len(t))
		for i, m := range t {
			if m == nil {
				continue
			}
			r, err := resolveTypedMapAt(m, store, joinKeyPath(at, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case []string:
		if t == nil {
			return []string(nil), nil
		}
		out := make([]string, len(t))
		for i, s := range t {
			r, err := resolveTypedString(s, store, joinKeyPath(at, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			if rs, ok := r.(string); ok {
				out[i] = rs
			} else {
				out[i] = formatValue(r)
			}
		}
		return out, nil
	default:
		return DeepCopy(v), nil
	}
}

func resolveTypedMapAt(m map[string]any, store *VariableStore, at string) (map[string]any, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(m))
	for _, k := range keys {
		r, err := resolveTypedAt(m[k], store, joinKeyPath(at, k))
		if err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, nil
}

// resolveTypedString resolves one string value (see ResolveTyped).
func resolveTypedString(s string, store *VariableStore, at string) (any, error) {
	if path, ok := wholeReference(s); ok {
		val, found := store.Get(path)
		if !found {
			return nil, unresolvedReference(path, at)
		}
		return DeepCopy(val), nil
	}
	for _, path := range templatePaths(s) {
		if _, found := store.Get(path); !found {
			return nil, unresolvedReference(path, at)
		}
	}
	return Resolve(s, store), nil
}

// wholeReference reports whether s is exactly one "{{path}}" reference with
// nothing outside the braces, returning the trimmed path.
func wholeReference(s string) (string, bool) {
	if len(s) < 4 || !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") {
		return "", false
	}
	inner := s[2 : len(s)-2]
	if strings.Contains(inner, "{{") || strings.Contains(inner, "}}") {
		return "", false
	}
	path := strings.TrimSpace(inner)
	if path == "" {
		return "", false
	}
	return path, true
}

// templatePaths returns the trimmed, non-empty paths of every {{...}} marker
// in s, scanning exactly the way Resolve does (find the next "}}", then the
// last "{{" before it) so the two can never disagree about what a reference
// is.
func templatePaths(s string) []string {
	var paths []string
	i := 0
	for i < len(s) {
		closeRel := strings.Index(s[i:], "}}")
		if closeRel == -1 {
			break
		}
		closeAbs := i + closeRel
		openRel := strings.LastIndex(s[i:closeAbs], "{{")
		if openRel == -1 {
			i = closeAbs + 2
			continue
		}
		path := strings.TrimSpace(s[i+openRel+2 : closeAbs])
		if path != "" {
			paths = append(paths, path)
		}
		i = closeAbs + 2
	}
	return paths
}

func joinKeyPath(at, key string) string {
	if at == "" {
		return key
	}
	return at + "." + key
}

func unresolvedReference(path, at string) error {
	if at == "" {
		return fmt.Errorf("%w: {{%s}} does not name an existing variable or node output", ErrUnresolvedReference, path)
	}
	return fmt.Errorf("%w: {{%s}} (at %q) does not name an existing variable or node output", ErrUnresolvedReference, path, at)
}
