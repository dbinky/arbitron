// Package codex implements engine.AgentProvider by driving `codex exec`
// non-interactively on the user's ChatGPT subscription (NO API key), mirroring
// the structure of adapter/opencode but WITHOUT a per-provider credential table:
// codex is single-provider and subscription-only. See args.go (the exec
// contract), env.go (the subscription env — the defining difference from
// opencode), parse.go (JSONL event folding), and version.go (binary probe).
package codex

import (
	"os"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
)

// codexHomeVar is the optional env var that relocates codex's config/auth dir
// away from the default ~/.codex. It is NOT part of the procenv base allowlist
// (which is intentionally minimal), so when the operator has set it in the
// engine's own environment we forward it explicitly here so the subprocess
// resolves the SAME ~/.codex/auth.json the user logged in with.
const codexHomeVar = "CODEX_HOME"

// codexEnv returns the controlled subprocess environment for a `codex exec`
// launch. It is the SUBSCRIPTION env: procenv.BaseEnv() (PATH-augmented,
// allowlist-filtered — so HOME/USER/LANG/... are preserved while the engine's
// own secrets are stripped) plus, when present in the parent env, CODEX_HOME.
//
// SECURITY — the defining difference from opencode's `openai` provider: codex
// authenticates via the ChatGPT subscription token in ~/.codex/auth.json (or
// $CODEX_HOME/auth.json), so this function deliberately sets NO OPENAI_API_KEY
// and NEVER calls binding.RevealKey. No secret is ever revealed, injected, or
// placed in the child env — the only auth surface is the on-disk subscription
// credential codex manages itself. (HOME is preserved by BaseEnv precisely so
// that on-disk credential resolves.)
//
// codexEnv takes no binding and cannot fail: there is no key to reveal and no
// per-provider routing to validate, so unlike opencodeEnv it returns no error.
func codexEnv() []string {
	env := procenv.BaseEnv()
	// Forward CODEX_HOME only when the operator set it in the parent env. We read
	// it directly (BaseEnv strips it as a non-allowlisted var) and append a single
	// entry so codex resolves its auth dir exactly as it does interactively.
	if home := strings.TrimSpace(os.Getenv(codexHomeVar)); home != "" {
		env = append(env, codexHomeVar+"="+os.Getenv(codexHomeVar))
	}
	return env
}
