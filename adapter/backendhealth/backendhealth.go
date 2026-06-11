// Package backendhealth assembles the backend-scoped health entries surfaced on
// the system/health read surface (HTTP GET /system/config and the MCP
// system_config tool). It is the ADAPTER-layer seam that runs backend probes
// (today: the codex install/login readiness check) and projects them into the
// domain's transport-neutral config.RedactedBackend list.
//
// It lives here — not in domain/config — because domain/config must not import
// the adapter that shells out to a CLI (hexagonal boundary). The handlers call
// Backends after config.Redact, the same way they fill in live queue counts.
package backendhealth

import (
	"context"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/domain/config"
)

// codexBackendName is the backend id reported on the health surface. It matches
// the workflow.BackendCodex selector value so the UI/CLI can correlate a
// readiness entry with the backend a profile selects.
const codexBackendName = "codex"

// CodexEntry probes the codex backend and returns its health entry. A clean
// not-ready host (not installed / not logged in) is reported as a STATE on the
// entry, never an error; only a genuine probe fault returns an error so the
// caller can omit the entry rather than surface a misleading verdict.
//
// The runner seam is injected so this is hermetically testable; production
// callers pass nil (codex.Readiness falls back to the shared os/exec runner).
//
// SECURITY: the entry carries only the backend name and the enum state — the
// underlying codex.Readiness probe sets no OPENAI_API_KEY and returns an enum,
// never raw subprocess text, so no credential or account identifier reaches the
// health surface.
func CodexEntry(ctx context.Context, runner claudecli.CommandRunner) (config.RedactedBackend, error) {
	state, err := codex.Readiness(ctx, codex.Config{Runner: runner})
	if err != nil {
		return config.RedactedBackend{}, err
	}
	return config.RedactedBackend{Name: codexBackendName, State: string(state)}, nil
}

// Backends returns the backend-scoped health entries to attach to a
// RedactedConfig. It is BEST-EFFORT: a probe that genuinely faults (an
// infrastructure error distinct from a clean not-ready state) is dropped so a
// transient health fault never breaks the whole system_config read. The codex
// entry is always included on success, carrying ready / not-installed /
// not-logged-in so the operator can pre-flight a run.
func Backends(ctx context.Context, runner claudecli.CommandRunner) []config.RedactedBackend {
	var out []config.RedactedBackend
	if entry, err := CodexEntry(ctx, runner); err == nil {
		out = append(out, entry)
	}
	return out
}
