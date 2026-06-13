package main

import "strings"

// codexInteractiveVerifiedEnv is the env var that opts a deployment INTO arming
// the codex interactive (mcp-server) provider against a real codex binary. It
// gates the production wiring of an SPIKE-unverified protocol: the read-only +
// every-mutation-gated posture rides four // SPIKE arg keys that fail OPEN if
// codex ignores them (PR #36 security HIGH), so until the protocol is verified
// against a live codex-cli 0.139 the provider must NOT be selectable.
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
