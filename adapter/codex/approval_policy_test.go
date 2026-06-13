package codex

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// --- withinRoot: symlink scope bypass (security HIGH) --------------------

// SECURITY / ADVERSARIAL — a write whose target is itself a symlink escaping the
// writable root must be DENIED, even though the symlink's own path is lexically
// under docs/. Without symlink resolution, `docs/link -> /etc` then a write to
// `docs/link` lands at /etc — the exact "no mutation outside docs/" bypass the
// reviewer flagged. Uses a REAL temp filesystem so the Lstat/EvalSymlinks check
// is exercised against an actual symlink, not a lexical string.
func TestWithinRoot_Security_SymlinkTargetEscapesRoot_Denied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	workDir := t.TempDir()
	docs := filepath.Join(workDir, "docs")
	require.NoError(t, os.MkdirAll(docs, 0o755))

	// outside is a sibling of docs/ (a stand-in for /etc, ../adapter, etc.).
	outside := filepath.Join(workDir, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))

	// docs/link -> ../outside : a symlink under docs/ whose target is outside it.
	link := filepath.Join(docs, "link")
	require.NoError(t, os.Symlink(outside, link))

	// (1) A write to the symlink itself: lexically under docs/, but it IS a symlink
	// escaping the root — deny.
	assert.False(t, withinRoot(workDir, "docs", link),
		"a write target that is a symlink escaping docs/ must be denied")

	// (2) A write THROUGH the symlink (docs/link/passwd) resolves to outside/passwd
	// — deny.
	assert.False(t, withinRoot(workDir, "docs", filepath.Join(link, "passwd")),
		"a write through a symlink escaping docs/ must be denied")
}

// SECURITY — a symlink that stays INSIDE the writable root is still allowed: the
// fix must reject only escaping symlinks, not every symlink (a docs/-internal
// symlink is legitimate). docs/inner -> docs/real, write docs/inner/note.md.
func TestWithinRoot_Security_SymlinkInsideRoot_Allowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	workDir := t.TempDir()
	docs := filepath.Join(workDir, "docs")
	real := filepath.Join(docs, "real")
	require.NoError(t, os.MkdirAll(real, 0o755))

	inner := filepath.Join(docs, "inner")
	require.NoError(t, os.Symlink(real, inner))

	assert.True(t, withinRoot(workDir, "docs", filepath.Join(inner, "note.md")),
		"a symlink that stays within docs/ must remain allowed")
}

// SECURITY — the docs/ root itself reached via a parent symlink must not widen
// scope: if the workDir's parent chain contains a symlink, a non-existent target
// under docs/ (the common case — codex writes a NEW file) must still be allowed
// because the resolved real docs/ dir is the container. This pins that the
// not-yet-existing-file case is not over-denied by EvalSymlinks failing on a
// missing leaf.
func TestWithinRoot_Security_NonExistentLeafUnderRealDocs_Allowed(t *testing.T) {
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "docs", "specs"), 0o755))

	// docs/specs/new-spec.md does not exist yet (codex is about to create it).
	assert.True(t, withinRoot(workDir, "docs", "docs/specs/new-spec.md"),
		"a not-yet-existing file under a real docs/ must be allowed (codex creates the deliverable)")
}

// --- decidePatch: NUL / control-char rejection (security LOW) ------------

// SECURITY — a write path containing a NUL byte is denied BEFORE the scope test,
// so the policy's notion of the path can never diverge from the kernel's
// (NUL-truncation) and the slog trace records an honest denial.
func TestDecide_Patch_Security_NulByte_Denied(t *testing.T) {
	p := newTestPolicy()
	d := p.decide(approvalRequest{WritePaths: []string{"docs/ok\x00/../../etc/passwd"}})
	assert.False(t, d.Allow, "a path containing a NUL byte must be denied")
	assert.Contains(t, strings.ToLower(d.Reason), "control", "denial reason must flag the control char")
}

// SECURITY — other control characters (e.g. newline, ESC) in a write path are
// also rejected up front, keeping the trace honest and defeating injection of
// log-spoofing path strings.
func TestDecide_Patch_Security_ControlChar_Denied(t *testing.T) {
	p := newTestPolicy()
	for _, target := range []string{"docs/a\nb.md", "docs/a\x1bb.md", "docs/a\tb.md"} {
		d := p.decide(approvalRequest{WritePaths: []string{target}})
		assert.Falsef(t, d.Allow, "a path containing a control char must be denied: %q", target)
	}
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
		{"git", "commit", "--no-verify", "-m", "deliverable"}, // --no-verify is fine (it DISABLES hooks)
		{"git", "add", "docs/spec.md"},
		{"git", "add", "docs/specs/a.md", "docs/specs/b.md"}, // every path under docs/
		{"git", "add", "--", "docs/spec.md"},                 // the `--` end-of-flags separator
	} {
		d := p.decide(approvalRequest{Command: argv})
		assert.Truef(t, d.Allow, "expected allow for commit-shape %v", argv)
		assert.NotEmpty(t, d.Reason)
	}
}

// SECURITY / ADVERSARIAL — the git allow arm must not trust argv[1] alone.
//   - `git add` must validate EVERY non-flag path is within docs/ and reject the
//     scope-defeating `-A` / `--all` / `.` forms.
//   - `git commit` must reject `-a`/`--all` (stages tracked changes outside the
//     gated docs scope) and `-c`/`--config` (arbitrary config override at commit).
//
// Each of these passed the old argv[1]-only check and would have been allowed.
func TestDecide_Command_Security_GitArm_BypassAttempts_Denied(t *testing.T) {
	p := newTestPolicy()
	for _, argv := range [][]string{
		{"git", "add", "-A"},                           // stages the whole tree, incl. non-docs
		{"git", "add", "--all"},                        // long form of -A
		{"git", "add", "."},                            // stages cwd recursively
		{"git", "add", "../adapter/codex/provider.go"}, // explicit out-of-docs path
		{"git", "add", "docs/ok.md", "cmd/main.go"},    // one good path, one escaping
		{"git", "add", "-A", "docs/ok.md"},             // -A alongside a scoped path
		{"git", "commit", "-a", "-m", "x"},             // -a stages tracked changes broadly
		{"git", "commit", "--all", "-m", "x"},          // long form of -a
		{"git", "commit", "-c", "core.editor=cmd"},     // -c config override
		{"git", "commit", "--config", "x=y"},           // --config override
	} {
		d := p.decide(approvalRequest{Command: argv})
		assert.Falsef(t, d.Allow, "git arm must deny scope/hook bypass: %v", argv)
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

// --- SECURITY: approvalSummary log-safety --------------------------------

// SECURITY — approvalSummary produces the "summary" field in the slog trace
// emitted by tracingApprover. It must NEVER include req.Reason (codex's
// human-readable explanation for the request, which may contain prompt-derived
// content — user intent, partial transcript, sensitive context). Only structural
// facts (argv or paths) are safe to log. This test verifies all three branches.
func TestApprovalSummary_NeverLogsRequestReason(t *testing.T) {
	secret := "the user asked me to implement the login button"

	// command branch — only the argv, never req.Reason
	got := approvalSummary(approvalRequest{
		Command: []string{"git", "status"},
		Reason:  secret,
	})
	assert.Contains(t, got, "git status", "summary must identify the command argv")
	assert.NotContains(t, got, secret, "summary must never include req.Reason for a command request")

	// write branch — only the paths, never req.Reason
	got = approvalSummary(approvalRequest{
		WritePaths: []string{"docs/spec.md"},
		Reason:     secret,
	})
	assert.Contains(t, got, "docs/spec.md", "summary must identify the write paths")
	assert.NotContains(t, got, secret, "summary must never include req.Reason for a write request")

	// unrecognized branch — fallback string only, never req.Reason
	got = approvalSummary(approvalRequest{Reason: secret})
	assert.NotContains(t, got, secret, "summary must never include req.Reason for an unrecognized request")
}
