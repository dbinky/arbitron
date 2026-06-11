package codex

import (
	"errors"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrModelRequired is returned by buildArgs when the binding carries no model.
// `codex exec` routes via `-m <model>`, so a model is mandatory; an empty model
// fails closed (never a default-model run the operator did not ask for).
var ErrModelRequired = errors.New("codex: Model is required for exec -m routing")

// Flag spellings for the `codex exec` non-interactive surface.
//
// VERIFIED against codex-cli 0.137.0 (`codex exec --help`). LOCALIZED HERE so a
// binary re-verification can adjust a spelling in ONE place without touching
// buildArgs' structure (a binary change is a one-line edit), mirroring opencode
// args.go.
//
// DEVIATION FROM PLAN (recorded per Task 2's "verify with --help, record the
// version" instruction): the plan named `--yolo` for the bypass-approvals +
// bypass-sandbox posture, but codex-cli 0.137.0 `codex exec` has NO `--yolo`
// flag. The verified spelling for "skip all confirmation prompts and execute
// commands without sandboxing" is `--dangerously-bypass-approvals-and-sandbox`,
// which expresses the SAME v1 fully-open posture the plan intended. If a future
// codex adds a `--yolo` alias on `exec`, this single constant is the one-line
// change.
const (
	execSubcommand = "exec"
	// bypassFlag skips approvals AND sandboxing — the v1 fully-open posture
	// (codex's equivalent of claudecli's --dangerously-skip-permissions /
	// opencode's --allow-all-tools). codex-cli 0.137.0 spelling.
	bypassFlag = "--dangerously-bypass-approvals-and-sandbox"
	// modelFlag selects the model: `-m <model>`.
	modelFlag = "-m"
	// cdFlag sets the working directory: `--cd <dir>`. codex-cli also exposes
	// `-C`; we pin the long form for log readability.
	cdFlag = "--cd"
	// jsonFlag requests the machine-readable JSONL event stream parse.go folds.
	jsonFlag = "--json"
)

// buildArgs assembles the `codex exec` argv for an agent mission.
//
//	codex exec --dangerously-bypass-approvals-and-sandbox -m <model> --cd <WorkDir> --json
//
// The PROMPT is NOT an arg: `codex exec` reads the prompt from stdin when it is
// not supplied positionally (see provider.go, which threads mission.Prompt into
// RunSpec.Stdin). Delivering the prompt via stdin keeps mission text out of argv
// and process listings.
//
// The WorkDir is passed BOTH as `--cd <WorkDir>` and (in provider.go) as the
// process cwd (RunSpec.Dir): codex resolves its own workspace from --cd, and the
// cwd pin is the same empty-WorkDir guard the other CLI adapters enforce. The
// empty-WorkDir guard lives in provider.go.
//
// Fail-closed: an empty model returns (nil, ErrModelRequired) so the caller
// never launches a default-model run.
//
// SECURITY: only the model and working-dir strings reach argv here; the prompt
// is stdin-only and secrets are env-only (env.go) — never argv or logs.
//
// ToolScope narrowing is intentionally NOT wired into argv: `codex exec` exposes
// no per-tool allowlist flag (see buildExecArgs), so the bypass posture stays fully
// open regardless of ChatRequest/AgentMission.ToolScope. This is the permanent v1
// posture — the same posture as opencode's --allow-all-tools and claudecli's
// --dangerously-skip-permissions. See also: docs/specs/codex-backend-spec.md §8.
func buildArgs(mission engine.AgentMission, binding provider.ResolvedBinding) ([]string, error) {
	return buildExecArgs(binding.Model, mission.WorkDir)
}

// buildExecArgs is the SHARED `codex exec` argv builder consumed by both the
// batch buildArgs (AgentMission/ResolvedBinding) and the streaming StreamProvider
// (engine.ChatRequest). Centralizing the argv here keeps the batch and stream
// paths on ONE exec contract (subcommand, bypass posture, model routing, --cd,
// --json) so a flag change is a single edit, never a fork.
//
// Fail-closed: an empty model returns (nil, ErrModelRequired) so neither path
// launches a default-model run. The WorkDir is emitted verbatim as `--cd`; the
// empty-WorkDir guard is enforced by the caller (provider.go / stream_adapter.go)
// before launch.
//
// SECURITY: only the model and working-dir strings reach argv here; the prompt is
// stdin-only and secrets are env-only (env.go) — never argv or logs.
//
// ToolScope narrowing is intentionally NOT wired into argv: `codex exec` exposes
// no per-tool allowlist flag, so the bypass posture stays fully open. Per-request
// ToolScope precedence is honoured at the request boundary (the stream provider
// prefers a non-empty ChatRequest.ToolScope over its global scope), matching
// claudecli's precedence shape even though codex has no flag to emit it through.
func buildExecArgs(model, workDir string) ([]string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, ErrModelRequired
	}
	return []string{
		execSubcommand,
		bypassFlag,
		modelFlag, model,
		cdFlag, workDir,
		jsonFlag,
	}, nil
}
