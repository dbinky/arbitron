package main

import "strings"

// codexInteractiveVerifiedEnv is the env var that opts a deployment INTO arming
// the codex interactive provider (`codex exec` / `codex exec resume`) against a
// real codex binary. It gates production wiring of the codex interactive_agent
// backend behind an explicit operator opt-in.
//
// SECURITY POSTURE (verified against codex-cli 0.139, PR #39 Must Fix #1): the
// approval subsystem is deleted, so codex's sandbox is the SOLE containment. The
// adapter pins `-s workspace-write` plus two -c overrides on BOTH the fresh and
// resume argv:
//   - sandbox_workspace_write.writable_roots=["<WorkDir>"] — confines writes to the
//     per-instance worktree (a BARE -s workspace-write was probed to also permit
//     writes to the cwd PARENT chain and /tmp, i.e. cross-instance writes).
//   - sandbox_workspace_write.network_access=false — pins egress off.
//
// RESIDUAL, operator must know before arming in a SHARED deployment:
//   - The writable_roots pin re-asserts on resume via -c (resume accepts -c, rejects
//     -s/--cd), but it confines against the resume PROCESS cwd; the engine round-trips
//     the same WorkDir per instance, so resume cwd == origin WorkDir holds. On a
//     resume whose process cwd diverges from the originating WorkDir the pin would
//     confine against the wrong root — a structural assumption, tested in the adapter.
//   - workspace-write does NOT confine READS: the turn can read any host-readable file
//     (HOME dotfiles, ~/.codex). interactive_agent transcripts may therefore contain
//     host secrets and must be access-controlled. See docs/specs/codex-backend-spec.md.
const codexInteractiveVerifiedEnv = "ARBITRON_CODEX_INTERACTIVE_VERIFIED"

// codexInteractiveVerified reports whether the operator has explicitly opted in
// to the codex interactive provider by setting codexInteractiveVerifiedEnv to a
// truthy value. It DEFAULTS OFF: an empty/unset/false value means the provider is
// not registered in the interactive stream map, so the selector fails closed for
// the codex backend (ErrUnsupportedAgentBackend) and an unverified build can never
// arm the unverified protocol against a live binary. Only "1"/"true"/"yes"/"on"
// (case-insensitive) enable it.
func codexInteractiveVerified(envVal string) bool {
	switch strings.ToLower(strings.TrimSpace(envVal)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}
