package codex

import (
	"path/filepath"
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
	// subcommand (push/reset/clean/checkout/...) is denied.
	if cmd == "git" && len(argv) >= 2 {
		switch argv[1] {
		case "commit", "add":
			return approvalDecision{Allow: true, Reason: "allowed: deliverable git " + argv[1]}
		}
	}

	if isInformationalCommand(argv) {
		return approvalDecision{Allow: true, Reason: "allowed: read-only/informational command " + cmd}
	}

	return approvalDecision{Allow: false, Reason: "denied: command not recognized as read-only or deliverable commit: " + cmd}
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
	return strings.HasPrefix(absTarget, absRoot+string(filepath.Separator))
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
