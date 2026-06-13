package codex

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultWritableRoot is the single directory tree the interactive auto-policy
// lets codex write: its deliverable lives under docs/, and no source/test/config
// does, so one relative root suffices (Phase-3 may override per provider). An
// empty approvalPolicy.writableRoot defaults to this — a zero value must NOT mean
// "everywhere" (fail closed).
const defaultWritableRoot = "docs"

// approvalPolicy is the pure, transport-free auto-approval policy: it classifies
// each codex approvalRequest into allow/deny under the invisible interactive
// guardrail. It is default-DENY — only positively recognized safe actions (a
// docs/-scoped write, the deliverable commit, a read-only/informational command)
// are allowed; everything else, including new/unknown codex approval kinds, is
// denied. It holds only value config (workDir + writableRoot) and depends on
// nothing but the stdlib, so the enforcement heart is exhaustively unit-testable
// in isolation (no engine, no transport, no spawning).
type approvalPolicy struct {
	// workDir is the node WorkDir — the codex sandbox root every write path is
	// resolved relative to before the scope test.
	workDir string
	// writableRoot is the only writable subtree, relative to workDir (default
	// "docs"). Empty means the default, never "everywhere".
	writableRoot string
}

// root returns the effective writable root, defaulting an empty field to
// defaultWritableRoot so a zero-value policy never silently widens scope.
func (p approvalPolicy) root() string {
	if p.writableRoot == "" {
		return defaultWritableRoot
	}
	return p.writableRoot
}

// decide classifies one approvalRequest. Order: a patch/write is allowed iff
// EVERY target path is within workDir/writableRoot; a command-exec is allowed
// only if it is the deliverable git commit/add or a read-only/informational
// command; anything else (mutating commands, or a request that is neither a
// recognized patch nor command) is denied. Every decision carries a Reason for
// the trace. The policy never prompts a human and never defers — it is fully
// automatic and fails closed.
func (p approvalPolicy) decide(req approvalRequest) approvalDecision {
	switch {
	case req.WritePaths != nil:
		return p.decidePatch(req.WritePaths)
	case req.Command != nil:
		return p.decideCommand(req.Command)
	default:
		// Neither a patch nor a command — an unrecognized (e.g. brand-new) codex
		// approval kind. Fail closed: unknown kinds never open.
		return approvalDecision{Allow: false, Reason: "denied: unrecognized approval request (neither command nor write paths)"}
	}
}

// decidePatch allows a patch only if EVERY target path resolves inside
// workDir/writableRoot. One path outside (a sibling dir, a ..-escape, or an
// absolute landing outside) denies the whole patch (fail closed) — a multi-file
// patch is all-or-nothing so codex cannot smuggle a source edit alongside a docs
// write.
func (p approvalPolicy) decidePatch(paths []string) approvalDecision {
	root := p.root()
	if len(paths) == 0 {
		return approvalDecision{Allow: false, Reason: "denied: write request carried no target paths"}
	}
	for _, target := range paths {
		// Reject NUL / other control characters BEFORE the scope test: Go rejects a
		// NUL at the syscall, but the policy's notion of the path and the kernel's
		// can diverge on NUL-truncation, and a control char lets a write spoof the
		// slog trace. Fail closed on a non-honest path (go-security: "reject paths
		// containing .., null bytes, or symlink chains").
		if hasControlChar(target) {
			return approvalDecision{
				Allow:  false,
				Reason: "denied: write path contains a NUL/control character: " + strconv.Quote(target),
			}
		}
		if !withinRoot(p.workDir, root, target) {
			return approvalDecision{
				Allow:  false,
				Reason: "denied: write path outside writable root " + root + "/: " + target,
			}
		}
	}
	return approvalDecision{Allow: true, Reason: "allowed: all write paths within " + root + "/"}
}

// decideCommand allows the narrowly-recognized deliverable git commit (`git`
// with a `commit` or `add` subcommand) and read-only/informational commands;
// it denies everything else (build/test/install/arbitrary mutation, shell
// wrappers, and any git subcommand other than commit/add) by default.
//
// CONSERVATISM / sandbox reliance: the informational-vs-mutating test is an
// explicit allowlist of known-safe read-only verbs (git status/diff/log, ls,
// cat, etc.) PLUS the commit/add deliverable arm — when in doubt the command is
// denied. We do NOT trust a permissive "anything not obviously mutating is fine"
// rule. This means even if codex's sandbox already blocks mutating writes, the
// policy is the primary gate; the sandbox is defence-in-depth, not the basis for
// allowing an unrecognized command.
func (p approvalPolicy) decideCommand(argv []string) approvalDecision {
	if len(argv) == 0 {
		return approvalDecision{Allow: false, Reason: "denied: empty command argv"}
	}
	cmd := argv[0]

	// The deliverable git commit/add — narrowly recognized. It writes .git
	// (outside writableRoot) so it needs an explicit allow; every other git
	// subcommand (push/reset/clean/checkout/...) is denied. The commit/add arm is
	// validated argument-by-argument (NOT argv[1]-only) so it cannot stage paths
	// outside the writable root or override commit-time behaviour.
	if cmd == "git" && len(argv) >= 2 {
		switch argv[1] {
		case "commit", "add":
			return p.decideGitCommand(argv)
		}
	}

	if isInformationalCommand(argv) {
		return approvalDecision{Allow: true, Reason: "allowed: read-only/informational command " + cmd}
	}

	return approvalDecision{Allow: false, Reason: "denied: command not recognized as read-only or deliverable commit: " + cmd}
}

// decideGitCommand adjudicates a `git add` / `git commit` deliverable command,
// inspecting EVERY argument rather than trusting argv[1] alone (the old arm did,
// letting `git add -A` stage the whole tree and `git commit -a` land tracked
// changes outside the gated docs scope):
//
//   - git add: every non-flag argument must be withinRoot, and the scope-defeating
//     `-A` / `--all` / `.` whole-tree flags are rejected outright. The `--`
//     end-of-options separator is tolerated (paths after it are still scope-checked).
//   - git commit: the broad-staging `-a` / `--all` and the arbitrary-config `-c` /
//     `--config` flags are rejected. `--no-verify` (which DISABLES hooks) is allowed;
//     all other commit flags ride through (the commit only writes .git, no path scope).
//
// argv[0]=="git" and argv[1] in {"add","commit"} are guaranteed by the caller.
func (p approvalPolicy) decideGitCommand(argv []string) approvalDecision {
	sub := argv[1]
	rest := argv[2:]
	root := p.root()

	switch sub {
	case "add":
		afterSep := false
		sawPath := false
		for _, arg := range rest {
			if arg == "--" {
				afterSep = true
				continue
			}
			if !afterSep && strings.HasPrefix(arg, "-") {
				// `-A`/`--all` (whole-tree) is the scope bypass; any other flag is also
				// rejected so an unrecognized add flag can never widen scope (fail closed).
				return approvalDecision{Allow: false, Reason: "denied: git add flag widens scope beyond " + root + "/: " + arg}
			}
			// A bare `.` (or an empty arg) stages the cwd recursively — outside docs/.
			if arg == "." || arg == "" {
				return approvalDecision{Allow: false, Reason: "denied: git add stages outside " + root + "/: " + arg}
			}
			if hasControlChar(arg) {
				return approvalDecision{Allow: false, Reason: "denied: git add path contains a NUL/control character: " + strconv.Quote(arg)}
			}
			if !withinRoot(p.workDir, root, arg) {
				return approvalDecision{Allow: false, Reason: "denied: git add path outside " + root + "/: " + arg}
			}
			sawPath = true
		}
		if !sawPath {
			// `git add` with no concrete path (e.g. only flags, or nothing) does not
			// stage a known-in-scope path — deny rather than guess at the default set.
			return approvalDecision{Allow: false, Reason: "denied: git add carried no in-scope path argument"}
		}
		return approvalDecision{Allow: true, Reason: "allowed: deliverable git add (all paths within " + root + "/)"}

	case "commit":
		for _, arg := range rest {
			switch arg {
			case "-a", "--all":
				return approvalDecision{Allow: false, Reason: "denied: git commit " + arg + " stages changes outside " + root + "/"}
			case "-c", "--config":
				return approvalDecision{Allow: false, Reason: "denied: git commit " + arg + " overrides config at commit time"}
			}
			// Reject the `-c key=val` / `--config=...` glued forms too.
			if strings.HasPrefix(arg, "-c") && len(arg) > 2 || strings.HasPrefix(arg, "--config=") {
				return approvalDecision{Allow: false, Reason: "denied: git commit config override: " + arg}
			}
		}
		return approvalDecision{Allow: true, Reason: "allowed: deliverable git commit"}
	}

	return approvalDecision{Allow: false, Reason: "denied: unrecognized git deliverable subcommand: " + sub}
}

// informationalCommands is the conservative allowlist of known read-only verbs
// the policy treats as side-effect-free. Membership is exact on argv[0]; git is
// special-cased below because only specific git subcommands are read-only.
var informationalCommands = map[string]bool{
	"ls":       true,
	"cat":      true,
	"head":     true,
	"tail":     true,
	"pwd":      true,
	"grep":     true,
	"rg":       true,
	"find":     true,
	"wc":       true,
	"echo":     true,
	"stat":     true,
	"file":     true,
	"which":    true,
	"true":     true,
	"diff":     true,
	"tree":     true,
	"readlink": true,
}

// gitReadOnlySubcommands lists the git subcommands that only inspect (never
// mutate) the repo. `git` with any other subcommand falls through to deny (the
// commit/add deliverable arm is handled separately in decideCommand).
var gitReadOnlySubcommands = map[string]bool{
	"status":    true,
	"diff":      true,
	"log":       true,
	"show":      true,
	"branch":    true,
	"rev-parse": true,
	"ls-files":  true,
	"blame":     true,
	"describe":  true,
}

// isInformationalCommand reports whether argv is a known side-effect-free,
// read-only command. It is intentionally conservative: only an exact allowlist
// hit (or a read-only git subcommand) qualifies — anything else is treated as
// potentially mutating and left for the caller to deny.
func isInformationalCommand(argv []string) bool {
	cmd := argv[0]
	if cmd == "git" {
		return len(argv) >= 2 && gitReadOnlySubcommands[argv[1]]
	}
	return informationalCommands[cmd]
}

// withinRoot reports whether target resolves strictly inside workDir/root. Both
// the root and the target are resolved relative to workDir and filepath.Clean'd,
// then compared: target is "within" only if it is a proper descendant of root
// (root itself, and any ..-escape or absolute path landing outside, is NOT
// within — fail closed). The trailing-separator on the prefix prevents a sibling
// whose name merely shares the prefix (docs-archive vs docs) from matching.
//
// SECURITY: a lexical prefix test alone is bypassable via symlink — `docs/link ->
// /etc` then a write to `docs/link/x` is lexically under docs/ but lands outside
// it. So before the prefix compare we (1) deny a target that is ITSELF an
// existing symlink, and (2) resolve the symlinks in the existing portion of both
// root and target (filepath.EvalSymlinks) and re-check containment on the real
// paths. A not-yet-existing leaf (codex creating a new deliverable) is fine: we
// resolve the deepest existing ancestor and re-join the missing tail, so a brand
// new docs/specs/new.md still verifies against the REAL docs/.
func withinRoot(workDir, root, target string) bool {
	if target == "" {
		return false
	}
	absRoot := resolve(workDir, root)
	absTarget := resolve(workDir, target)
	if absTarget == absRoot {
		// The root directory itself is not a writable child.
		return false
	}
	if !strings.HasPrefix(absTarget, absRoot+string(filepath.Separator)) {
		return false
	}

	// SECURITY: a target that is itself an existing symlink escapes the lexical
	// check — deny it outright (the realized write lands wherever the link points,
	// not at the docs/-prefixed string).
	if isSymlink(absTarget) {
		return false
	}

	// Re-check containment on the symlink-resolved real paths so a symlink anywhere
	// in either parent chain cannot smuggle the write outside the deliverable root.
	realRoot, ok := evalExisting(absRoot)
	if !ok {
		return false
	}
	realTarget, ok := evalExisting(absTarget)
	if !ok {
		return false
	}
	if realTarget == realRoot {
		return false
	}
	return strings.HasPrefix(realTarget, realRoot+string(filepath.Separator))
}

// isSymlink reports whether p is an existing symlink (os.Lstat does NOT follow
// the final component). A non-existent path or a stat error is treated as "not a
// symlink" — the parent-chain resolution in withinRoot still guards the
// containment, and a brand-new deliverable file legitimately does not exist yet.
func isSymlink(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// evalExisting resolves the symlinks in the existing portion of p and re-joins
// any not-yet-existing tail, returning the real absolute path. It walks up to the
// deepest existing ancestor (so a new file under a real docs/ resolves against the
// real docs/), filepath.EvalSymlinks's that ancestor, and re-appends the missing
// segments. It returns (real, false) only when EvalSymlinks fails for a reason
// other than non-existence — fail closed.
func evalExisting(p string) (string, bool) {
	missing := ""
	cur := p
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if missing == "" {
				return resolved, true
			}
			return filepath.Join(resolved, missing), true
		}
		if !os.IsNotExist(err) {
			// A real error (permission, loop, ...) — fail closed.
			return "", false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Reached the filesystem root without finding an existing ancestor.
			return p, true
		}
		missing = filepath.Join(filepath.Base(cur), missing)
		cur = parent
	}
}

// hasControlChar reports whether s contains a NUL or any other ASCII control
// character (< 0x20 or 0x7f). Such a character in a write path is rejected before
// the scope test: it has no legitimate place in a deliverable path, and it lets a
// write diverge the policy's path from the kernel's (NUL truncation) or spoof the
// slog trace (newline/ESC). Fail closed on a non-honest path.
func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// resolve makes p absolute against workDir (a relative p is joined onto workDir;
// an absolute p is taken as-is) and cleans it, collapsing any `.`/`..` segments
// so the prefix test in withinRoot reasons about a normalized path. filepath.Join
// already cleans; filepath.Clean handles the absolute branch.
func resolve(workDir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(workDir, p)
}
