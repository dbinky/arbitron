package pi

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
)

// ErrModelRequired is returned when a binding carries no model. pi falls back to
// its own configured default provider/model when `--model` is absent, so an
// empty model must fail closed rather than silently run a mission on whatever pi
// happens to be pointed at.
var ErrModelRequired = errors.New("pi: Model is required for --model routing")

// Flag spellings for the pi non-interactive surface.
//
// VERIFIED against pi 0.87.0 (`pi --help`). LOCALIZED HERE so a spelling change
// in a future pi is a one-line edit rather than a hunt through the adapter.
const (
	// printFlag runs one prompt and exits instead of opening the TUI.
	printFlag = "--print"
	// modeFlag + modeJSON select the JSONL event stream parse.go folds.
	modeFlag = "--mode"
	modeJSON = "json"
	// noSessionFlag keeps a mission ephemeral. Each agent mission is an
	// independent run, and pi otherwise writes a session file into
	// ~/.pi/agent/sessions keyed by cwd — which, for arbitron's per-instance
	// workspaces, would accumulate one directory per run forever.
	noSessionFlag = "--no-session"
	// modelFlag routes the run. pi accepts "<provider>/<id>" (and an optional
	// ":<thinking>" suffix, which this adapter does NOT use — effort rides the
	// explicit --thinking flag so one code path owns the mapping).
	modelFlag = "--model"
	// thinkingFlag carries the reasoning level.
	thinkingFlag = "--thinking"
	// thinkingOff is pi's spelling for "no reasoning". arbitron spells the same
	// intent EffortNone ("none"), so the two vocabularies meet here.
	thinkingOff = "off"
	// Probe-only restrictions, verified against pi 1.3.0 (see
	// testdata/probe-restrictions.md). Tool suppression alone does not prevent
	// extension factories from executing; offline also prevents inherited
	// missing-package installation and automatic update/catalog traffic.
	noToolsFlag      = "--no-tools"
	noMCPFlag        = "--no-mcp"
	noExtensionsFlag = "--no-extensions"
	offlineFlag      = "--offline"
)

// buildArgs assembles the argv for one pi agent mission:
//
//	pi --print --mode json --no-session --model <model> [--thinking <level>]
//
// The PROMPT is NOT an arg: pi merges piped stdin into the initial prompt, and
// the adapter delivers it that way so it never appears in argv (which is
// world-readable via ps). The WORKING DIRECTORY is likewise absent: pi operates
// on the process cwd, which the runner sets from RunSpec.Dir.
//
// Fail-closed: an empty model returns ErrModelRequired and NO argv, and an
// effort value outside the axis returns provider.ErrInvalidEffort rather than
// passing an unknown level through to pi.
//
// ToolScope is deliberately NOT wired into argv even though pi exposes a
// `--tools` allowlist. pi's tool vocabulary is its own (read/write/edit/bash
// plus whatever extensions are installed), and arbitron's scope strings come
// from the claude-code vocabulary. Passing an unmapped name would produce an
// allowlist matching NOTHING — silently disabling every tool and stranding the
// agent — which is strictly worse than the fully-open posture the codex adapter
// also runs. A real mapping table belongs with a capability ruleset, not here.
func buildArgs(binding provider.ResolvedBinding) ([]string, error) {
	model := strings.TrimSpace(binding.Model)
	if model == "" {
		return nil, ErrModelRequired
	}
	thinking, err := thinkingLevel(binding.Effort)
	if err != nil {
		return nil, err
	}
	args := []string{printFlag, modeFlag, modeJSON, noSessionFlag, modelFlag, model}
	if thinking != "" {
		args = append(args, thinkingFlag, thinking)
	}
	return args, nil
}

// thinkingLevel maps an arbitron Effort onto pi's --thinking vocabulary,
// returning "" when nothing should be emitted.
//
// The two axes line up almost exactly — pi accepts off/minimal/low/medium/high/
// xhigh/max and arbitron grades low < medium < high < xhigh < max — so the
// graded levels pass through verbatim. Only the endpoints need translating:
// EffortDefault (and the empty value) emit NOTHING so the model keeps its own
// default, and EffortNone becomes pi's "off".
//
// An unrecognised value fails closed with provider.ErrInvalidEffort. pi would
// otherwise reject it at startup, costing a subprocess launch to learn what the
// axis already knows.
//
// NOTE: this is a direct axis mapping, not a capability-ruleset emission. It
// therefore does NOT floor a requested level to a model's supported ceiling the
// way the codex path does via provider.CapabilityRules. Threading pi through
// that ruleset (a new backend arm in the domain's Emit switch plus capability
// rows) is deliberately left to a follow-up; until then a level pi's chosen
// model does not support is the provider's to reject.
func thinkingLevel(e provider.Effort) (string, error) {
	switch e {
	case "", provider.EffortDefault:
		return "", nil
	case provider.EffortNone:
		return thinkingOff, nil
	case provider.EffortLow, provider.EffortMedium, provider.EffortHigh,
		provider.EffortXHigh, provider.EffortMax:
		return string(e), nil
	default:
		return "", fmt.Errorf("%w: %q", provider.ErrInvalidEffort, string(e))
	}
}
