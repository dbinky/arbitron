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
	// sandboxFlag selects codex's sandbox policy; the usability probe uses it with
	// readOnlySandbox instead of the bypass posture.
	sandboxFlag     = "--sandbox"
	readOnlySandbox = "read-only"
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
	return buildExecArgs(binding.Model, mission.WorkDir, nil)
}

// codexBackend is the effective-backend identity buildArgsWithRules passes to
// CapabilityRules.Emit when a binding carries no resolved Backend. The codex
// adapter unambiguously drives `codex exec`, so an empty Backend defaults to this.
const codexBackend = "codex"

// buildArgsWithRules assembles the `codex exec` argv with the binding's resolved
// effort appended as `-c model_reasoning_effort=<v>` (profile-editing Phase 3).
// The effort emission is computed from the capability rules so the value codex
// receives is the SAME effective (possibly floored) value the write validated. A
// default/empty effort appends nothing. An Empty (unconfigured) ruleset is a
// no-op, so it degrades to the bare buildArgs contract.
//
// Fail-closed: an empty model still returns ErrModelRequired (via buildExecArgs).
func buildArgsWithRules(mission engine.AgentMission, binding provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	extra, err := codexEffortArgs(binding, rules)
	if err != nil {
		return nil, err
	}
	return buildExecArgs(binding.Model, mission.WorkDir, extra)
}

// codexEffortConfigKey is the codex `-c` config key carrying the reasoning effort
// (`-c model_reasoning_effort=<v>`). It is the ONE place the exec flag key lives —
// codexEffortArgs wraps a grade into this form, and codexEffortGrade reads the
// grade back out of the domain emission, so neither path hard-codes the spelling.
const codexEffortConfigKey = "model_reasoning_effort"

// codexEffortGrade is the SHARED graded-effort core for both codex paths: it
// resolves the bare effective reasoning-effort value (e.g. "high", or "minimal"
// for none) for a binding via the capability rules — the SAME floor + mapping the
// exec path applies, so interactive and exec never disagree on the effective
// value. An Empty ruleset, an empty/default effort, or a backend that yields no
// graded effort returns "" (no effort).
//
// This is the value the interactive (MCP) path carries directly in the protocol
// body, and the value codexEffortArgs wraps into the exec `-c` form — so the exec
// flag FORMAT is never load-bearing for the MCP path (the previous reverse-parse
// of codexEffortArgs' output could silently floor interactive effort to default
// if the arg format changed, with green tests).
func codexEffortGrade(binding provider.ResolvedBinding, rules provider.CapabilityRules) (string, error) {
	if rules.Empty() {
		return "", nil
	}
	backend := binding.Backend
	if backend == "" {
		backend = codexBackend
	}
	effort := binding.Effort
	if effort == "" {
		effort = provider.EffortDefault
	}
	// Context is inapplicable to codex (rejected at write); pass ContextDefault so
	// Emit never trips the context-axis check and only the effort emission comes back.
	em, err := rules.Emit(backend, binding.Model, provider.ContextDefault, effort)
	if err != nil {
		return "", err
	}
	// The domain emission exposes codex effort only as the `-c <key>=<v>` arg pair;
	// extract the bare <v> here, in the ONE place that knows the emission shape, so
	// callers consume the grade rather than re-deriving it.
	return gradeFromEmissionArgs(em.Args), nil
}

// gradeFromEmissionArgs extracts the bare reasoning-effort value from a codex
// `-c model_reasoning_effort=<v>` emission arg pair, returning "" when no such
// pair is present (default/empty effort emits nothing).
func gradeFromEmissionArgs(args []string) string {
	prefix := codexEffortConfigKey + "="
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, prefix); ok {
			return v
		}
	}
	return ""
}

// codexEffortArgs computes the codex `-c model_reasoning_effort=<v>` argv addition
// for a binding by WRAPPING the shared codexEffortGrade into the exec `-c` form. A
// "" grade (Empty ruleset, empty/default effort, or a backend with no graded
// effort) returns nil (no extra args). It is shared by the batch buildArgsWithRules
// and the streaming launch so the two paths emit identical effort args.
func codexEffortArgs(binding provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	grade, err := codexEffortGrade(binding, rules)
	if err != nil {
		return nil, err
	}
	if grade == "" {
		return nil, nil
	}
	return []string{"-c", codexEffortConfigKey + "=" + grade}, nil
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
// extra carries optional `-c <key=value>` config overrides (profile-editing
// Phase 3: `-c model_reasoning_effort=<v>`) appended AFTER the model flag and
// BEFORE --cd/--json, so the effort override rides the same exec contract on both
// the batch and stream paths. A nil/empty extra reproduces the pre-Phase-3 argv
// exactly (back-compat for buildArgs and existing call sites).
func buildExecArgs(model, workDir string, extra []string) ([]string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, ErrModelRequired
	}
	args := []string{execSubcommand, bypassFlag, modelFlag, model}
	args = append(args, extra...) // e.g. -c model_reasoning_effort=medium
	args = append(args, cdFlag, workDir, jsonFlag)
	return args, nil
}

// buildProbeArgs assembles the `codex exec` argv of the usability probe: the
// mission argv with the read-only sandbox in place of the bypass posture, so a
// probe turn cannot write or run unsandboxed commands. The model and effort ride
// argv exactly as a mission's do. Fixed D10 restrictions are appended here;
// ProbeExecutedModel feeds the per-server overrides from live config enumeration.
func buildProbeArgs(binding provider.ResolvedBinding, rules provider.CapabilityRules, workDir string) ([]string, error) {
	extra, err := codexEffortArgs(binding, rules)
	if err != nil {
		return nil, err
	}
	args, err := buildExecArgs(binding.Model, workDir, extra)
	if err != nil {
		return nil, err
	}
	for i, a := range args {
		if a == bypassFlag {
			args[i] = sandboxFlag
			args = append(args[:i+1], append([]string{readOnlySandbox}, args[i+1:]...)...)
			break
		}
	}
	return append(args, probeRestrictionArgs()...), nil
}
