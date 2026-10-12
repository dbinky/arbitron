package engine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDeriveGitRef(t *testing.T) {
	tests := []struct {
		name        string
		gitVariable string
		vars        map[string]any
		want        string
	}{
		{"manifest variable wins", "pr_branch", map[string]any{"branch": "b", "pr_branch": "p"}, "p"},
		{"branch preferred over pr_branch", "", map[string]any{"branch": "feat/x", "pr_branch": "p"}, "feat/x"},
		{"pr_branch fallback", "", map[string]any{"pr_branch": "p"}, "p"},
		{"work_dir basename", "", map[string]any{"work_dir": "/a/b/feat-wt"}, "feat-wt"},
		{"work_dir trailing slash", "", map[string]any{"work_dir": "/a/b/feat-wt/"}, "feat-wt"},
		{"work_dir root is useless", "", map[string]any{"work_dir": "/"}, ""},
		{"dotted manifest variable", "init-pr-workspace.pr_branch", map[string]any{"init-pr-workspace": map[string]any{"pr_branch": "q"}}, "q"},
		{"mistyped manifest variable falls through", "nope", map[string]any{"branch": "b"}, "b"},
		{"empty branch yields empty", "", map[string]any{"branch": ""}, ""},
		{"nil vars yields empty", "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveGitRef(tt.gitVariable, tt.vars); got != tt.want {
				t.Fatalf("deriveGitRef(%q, %v) = %q, want %q", tt.gitVariable, tt.vars, got, tt.want)
			}
		})
	}
}

func TestDeriveGitRef_NonStringSkipped(t *testing.T) {
	if got := deriveGitRef("", map[string]any{"branch": 42, "pr_branch": "p"}); got != "p" {
		t.Fatalf("numeric branch: got %q, want %q", got, "p")
	}
	if got := deriveGitRef("x", map[string]any{"x": map[string]any{}, "branch": "b"}); got != "b" {
		t.Fatalf("map gitVariable: got %q, want %q", got, "b")
	}
	if got := deriveGitRef("", map[string]any{"branch": nil, "work_dir": 7}); got != "" {
		t.Fatalf("nil/number values: got %q, want empty", got)
	}
}

func TestDeriveGitRef_OversizedBranchCapped(t *testing.T) {
	got := deriveGitRef("", map[string]any{"branch": strings.Repeat("a", 5<<20)})
	if len(got) != maxGitRefLen {
		t.Fatalf("len = %d, want %d", len(got), maxGitRefLen)
	}
}

func TestDeriveGitRef_CapIsRuneSafe(t *testing.T) {
	// "é" is 2 bytes; an odd byte prefix would split the last rune.
	got := deriveGitRef("", map[string]any{"branch": "x" + strings.Repeat("é", maxGitRefLen)})
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
	if len(got) > maxGitRefLen || len(got) < maxGitRefLen-1 {
		t.Fatalf("len = %d, want %d or %d", len(got), maxGitRefLen-1, maxGitRefLen)
	}
}

func TestDeriveGitRef_StripsControlAndBidi(t *testing.T) {
	tests := []struct{ in, want string }{
		{"feat/\u202Egnp.exe", "feat/gnp.exe"},
		{"\u2066main\u2069", "main"},
		{"a\nb\tc\x00d", "abcd"},
		{"bad\xffutf8", "badutf8"},
	}
	for _, tt := range tests {
		if got := deriveGitRef("", map[string]any{"branch": tt.in}); got != tt.want {
			t.Fatalf("deriveGitRef(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDeriveGitRef_SanitizedToEmptyFallsThrough(t *testing.T) {
	if got := deriveGitRef("", map[string]any{"branch": "\u202E\n", "pr_branch": "p"}); got != "p" {
		t.Fatalf("got %q, want %q", got, "p")
	}
}
