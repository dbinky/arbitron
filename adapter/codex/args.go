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
// ToolScope narrowing is intentionally NOT wired in Phase 1: the bypass posture
// is fully-open by default. Per-request ToolScope narrowing is deferred to
// Phase 2's shared agent path (mirrors the opencode/claudecli posture work).
func buildArgs(mission engine.AgentMission, binding provider.ResolvedBinding) ([]string, error) {
	model := strings.TrimSpace(binding.Model)
	if model == "" {
		return nil, ErrModelRequired
	}
	return []string{
		execSubcommand,
		bypassFlag,
		modelFlag, model,
		cdFlag, mission.WorkDir,
		jsonFlag,
	}, nil
}
