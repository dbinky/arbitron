package engine

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/arbitron/arbitron/domain/variable"
)

// deriveGitRef returns the branch/worktree a run is working on, for the
// dashboard Git column. The first non-empty string wins, in this order:
//
//  1. the variable named by the workflow's gitVariable (dotted path allowed)
//  2. branch
//  3. pr_branch
//  4. the last path segment of work_dir
//
// Non-string values are skipped, so a mistyped or oddly-typed gitVariable falls
// through to the conventions instead of rendering garbage. Each candidate is
// passed through sanitizeGitRef, so a value that sanitizes to "" also falls
// through. "" means unknown.
func deriveGitRef(gitVariable string, vars map[string]any) string {
	if len(vars) == 0 {
		return ""
	}
	store := variable.FromSnapshot(vars)
	str := func(p string) string {
		if p == "" {
			return ""
		}
		v, ok := store.Get(p)
		if !ok {
			return ""
		}
		s, _ := v.(string)
		return s
	}
	for _, p := range []string{gitVariable, "branch", "pr_branch"} {
		if s := sanitizeGitRef(str(p)); s != "" {
			return s
		}
	}
	if wd := strings.TrimRight(str("work_dir"), "/"); wd != "" {
		if base := path.Base(wd); base != "." && base != "/" {
			return sanitizeGitRef(base)
		}
	}
	return ""
}

// maxGitRefLen caps the git ref in bytes. It rides on every enriched list row
// and node.started event, and its source variables are unbounded runtime
// values, so an oversized one must not bloat every viewer's payload.
const maxGitRefLen = 200

// sanitizeGitRef drops control characters, bidi embedding/override/isolate
// characters (U+202A–U+202E, U+2066–U+2069 — an RLO can make a ref display as
// a different string to other users) and invalid UTF-8, then truncates to at
// most maxGitRefLen bytes without splitting a rune.
func sanitizeGitRef(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if (r == utf8.RuneError && size <= 1) || unicode.IsControl(r) || isBidiControl(r) {
			continue
		}
		if b.Len()+size > maxGitRefLen {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isBidiControl(r rune) bool {
	return (r >= '\u202A' && r <= '\u202E') || (r >= '\u2066' && r <= '\u2069')
}
