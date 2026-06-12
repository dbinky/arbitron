package codex

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// newTestPolicy builds an approvalPolicy rooted at an absolute workDir with the
// default writableRoot ("docs"), the production posture. Tests use an absolute
// workDir so the prefix test reasons about a stable, OS-correct tree.
func newTestPolicy() approvalPolicy {
	return approvalPolicy{
		workDir:      filepath.FromSlash("/repo"),
		writableRoot: "docs",
	}
}

// --- withinRoot helper: direct adversarial unit tests --------------------

func TestWithinRoot(t *testing.T) {
	workDir := filepath.FromSlash("/repo")
	root := "docs"

	cases := []struct {
		name   string
		target string
		want   bool
	}{
		// happy / success
		{"plain relative under root", "docs/specs/foo-spec.md", true},
		{"nested relative under root", "docs/superpowers/specs/a.md", true},
		{"dot-slash relative under root", "./docs/notes.md", true},
		{"absolute under root", filepath.FromSlash("/repo/docs/x.md"), true},
		{"trailing-slash dir under root", "docs/specs/", true},

		// failure / adversarial
		{"dotdot escape out of root", "docs/../domain/x.go", false},
		{"sibling source dir", "cmd/main.go", false},
		{"absolute outside repo", filepath.FromSlash("/etc/passwd"), false},
		{"absolute in repo but outside root", filepath.FromSlash("/repo/domain/x.go"), false},
		{"deep dotdot escape", "docs/a/../../secret.txt", false},
		{"dotdot escape above workDir", "../../etc/passwd", false},
		{"dotdot escape to sibling repo", "../sibling-repo/docs/x.md", false},

		// edge: a sibling whose name merely shares the root prefix must NOT match
		{"prefix-sibling not under root", "docs-archive/x.md", false},

		// edge: the root dir itself (no child) — deny, not a file write target
		{"root itself relative", "docs", false},
		{"root itself dot-slash", "./docs", false},
		{"root itself absolute", filepath.FromSlash("/repo/docs"), false},
		{"root itself trailing slash", "docs/", false},

		// edge: empty target — deny
		{"empty target", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := withinRoot(workDir, root, tc.target)
			assert.Equal(t, tc.want, got, "withinRoot(%q,%q,%q)", workDir, root, tc.target)
		})
	}
}

// EDGE — a deeper writableRoot correctly narrows the allowed subtree. All
// TestWithinRoot cases use root="docs"; this verifies the prefix-separator
// logic with a multi-level root so narrowing writableRoot can never be
// accidentally widened by the helper.
func TestWithinRoot_Edge_DeeperRoot_NarrowsSubtree(t *testing.T) {
	workDir := filepath.FromSlash("/repo")
	root := filepath.FromSlash("docs/superpowers")

	// within the narrowed subtree — allowed
	assert.True(t, withinRoot(workDir, root, "docs/superpowers/specs/a.md"),
		"docs/superpowers/specs/a.md is within docs/superpowers")

	// sibling subtree under docs/ — denied (in docs/ but outside docs/superpowers/)
	assert.False(t, withinRoot(workDir, root, "docs/specs/a.md"),
		"docs/specs is outside docs/superpowers — narrower root must deny it")

	// prefix-sibling of the deeper root — denied
	assert.False(t, withinRoot(workDir, root, "docs/superpowers-archive/x.md"),
		"prefix-sibling of docs/superpowers must not match")

	// the narrowed root itself — denied (not a writable child)
	assert.False(t, withinRoot(workDir, root, "docs/superpowers"),
		"root itself must not be within")
}

// --- decide: patch/write classification ----------------------------------

func TestDecide_Patch_HappyPath_SingleDocsFile_Allowed(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{"docs/specs/foo-spec.md"}})
	assert.True(t, d.Allow)
	assert.NotEmpty(t, d.Reason)
}

func TestDecide_Patch_Success_MultiFileAllUnderDocs_Allowed(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{
		"docs/superpowers/specs/a.md",
		"docs/superpowers/specs/b.md",
		"./docs/notes.md",
	}})
	assert.True(t, d.Allow)
	assert.NotEmpty(t, d.Reason)
}

func TestDecide_Patch_Failure_OutsideDocs_Denied(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{"domain/engine/settings.go"}})
	assert.False(t, d.Allow)
	assert.NotEmpty(t, d.Reason)
}

func TestDecide_Patch_Adversarial_DotDotEscape_Denied(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{"docs/../domain/x.go"}})
	assert.False(t, d.Allow)
	// Reason must identify the offending raw path so the slog trace is actionable.
	assert.Contains(t, d.Reason, "docs/../domain/x.go")
}

func TestDecide_Patch_Adversarial_AbsoluteOutside_Denied(t *testing.T) {
	p := newTestPolicy()
	offender := filepath.FromSlash("/etc/passwd")
	d := p.decide(approvalRequest{WritePaths: []string{offender}})
	assert.False(t, d.Allow)
	assert.Contains(t, d.Reason, offender, "denial reason must name the offending absolute path")
}

// GUARDRAIL — the canonical "codex tries to edit source mid-interview" scenario.
// A multi-file patch with even one path outside docs/ is denied in full; the
// Reason must identify the source file that triggered the denial so the operator
// can audit it via slog.
func TestDecide_Patch_Adversarial_MixedMultiFile_DeniedWholesale(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{
		"docs/a.md",
		"cmd/main.go",
	}})
	assert.False(t, d.Allow)
	assert.Contains(t, d.Reason, "cmd/main.go", "denial reason must name the offending path, not the allowed one")
}

func TestDecide_Patch_Edge_RootItself_Denied(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{"docs"}})
	assert.False(t, d.Allow)
	assert.NotEmpty(t, d.Reason)
}

func TestDecide_Patch_Edge_EmptyPathInList_Denied(t *testing.T) {
	p := newTestPolicy()
	// An empty path element resolves to workDir itself — outside docs/ → deny.
	d := p.decide(approvalRequest{WritePaths: []string{"docs/a.md", ""}})
	assert.False(t, d.Allow)
	assert.NotEmpty(t, d.Reason)
}

// FAIL-CLOSED — a non-nil but empty WritePaths slice must be denied, not
// vacuously allowed. Without the len==0 guard, range over []string{} iterates
// zero times and the trailing "all paths within root" return would fire.
func TestDecide_Patch_Edge_EmptyWritePathsSlice_Denied(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{}})
	assert.False(t, d.Allow)
	assert.Contains(t, d.Reason, "no target paths")
}

// --- decide: command exec classification ----------------------------------

func TestDecide_Command_HappyPath_Informational_Allowed(t *testing.T) {
	p := newTestPolicy()
	for _, argv := range [][]string{
		{"git", "status"},
		{"git", "diff"},
		{"git", "log", "--oneline"},
		{"ls", "-la"},
		{"cat", "docs/spec.md"},
		{"grep", "-r", "foo", "."},
		{"pwd"},
	} {
		d := p.decide(approvalRequest{Command: argv})
		assert.Truef(t, d.Allow, "expected allow for informational %v", argv)
		assert.NotEmpty(t, d.Reason)
	}
}

func TestDecide_Command_Success_DeliverableCommit_Allowed(t *testing.T) {
	p := newTestPolicy()
	for _, argv := range [][]string{
		{"git", "commit", "-m", "deliverable"},
		{"git", "add", "docs/spec.md"},
		{"git", "add", "-A"},
	} {
		d := p.decide(approvalRequest{Command: argv})
		assert.Truef(t, d.Allow, "expected allow for commit-shape %v", argv)
		assert.NotEmpty(t, d.Reason)
	}
}

func TestDecide_Command_Failure_Mutating_Denied(t *testing.T) {
	p := newTestPolicy()
	for _, argv := range [][]string{
		{"make", "build"},
		{"npm", "run", "build"},
		{"go", "build", "./..."},
		{"go", "test", "./..."},
		{"npm", "install"},
		{"rm", "-rf", "docs"},
		{"sh", "-c", "git status"}, // shell wrapper — not recognized, deny
		{"git", "push"},            // git but not commit/add — deny
		{"git", "reset", "--hard"}, // mutating git — deny
	} {
		d := p.decide(approvalRequest{Command: argv})
		assert.Falsef(t, d.Allow, "expected deny for mutating/unknown %v", argv)
		assert.NotEmpty(t, d.Reason)
	}
}

func TestDecide_Command_Edge_EmptyArgv_Denied(t *testing.T) {
	p := newTestPolicy()
	// Command non-nil but empty — unrecognized, fail closed.
	d := p.decide(approvalRequest{Command: []string{}})
	assert.False(t, d.Allow)
	assert.NotEmpty(t, d.Reason)
}

// --- decide: unrecognized requests ----------------------------------------

func TestDecide_Edge_NeitherCommandNorWritePaths_Denied(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{Reason: "some new codex approval kind"})
	assert.False(t, d.Allow)
	assert.NotEmpty(t, d.Reason)
}

// --- decide: writableRoot defaulting --------------------------------------

func TestDecide_Patch_DefaultsWritableRootToDocs(t *testing.T) {
	// An empty writableRoot must default to "docs", not allow-everything.
	p := approvalPolicy{workDir: filepath.FromSlash("/repo")}
	allowed := p.decide(approvalRequest{WritePaths: []string{"docs/x.md"}})
	assert.True(t, allowed.Allow, "docs path must be allowed under defaulted root")
	denied := p.decide(approvalRequest{WritePaths: []string{"src/x.go"}})
	assert.False(t, denied.Allow, "non-docs path must be denied under defaulted root")
}
