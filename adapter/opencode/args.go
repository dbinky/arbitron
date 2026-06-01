package opencode

import "github.com/arbitron/arbitron/domain/engine"

// outputFormat is the headless output format requested from opencode. The
// installed v0.0.55 binary supports "text" and "json"; we use "text" because
// this build's JSON shape is unstable across versions and the single-shot
// contract only needs the final assistant text. parseOutput is tolerant either
// way.
const outputFormat = "text"

// buildArgs assembles the `opencode` CLI arguments for a single-shot headless
// mission.
//
// INSTALLED-BINARY CONTRACT (opencode v0.0.55, the older Go variant):
//
//	opencode -p "<prompt>" -c <cwd> -q -f text
//
// This build has NO `run` subcommand and NO `--model`/`--resume`/`--session`
// flag, so:
//   - the prompt is passed via -p (headless mode), NOT as a `run` positional;
//   - the WorkDir is passed via -c (the {{workspace}} stand-in);
//   - -q hides the spinner; -f selects the output format;
//   - NO resume/session arg is emitted — this is a single-shot mission per step
//     per the spike finding (spec §9 fallback). SessionID is intentionally
//     unused for the opencode backend.
//
// All arg-building is isolated here so a future opencode build that DOES expose
// `--model <provider/model>` / provider routing can be swapped in by editing
// only this function (and re-verifying against that binary).
func buildArgs(mission engine.AgentMission) []string {
	args := []string{
		"-p", mission.Prompt,
		"-c", mission.WorkDir,
		"-q",
		"-f", outputFormat,
	}
	// NOTE: mission.ToolScope and mission.SessionID are intentionally NOT
	// threaded — the installed binary exposes neither a per-tool permission
	// flag nor a session/resume flag in headless mode. A future build with
	// those surfaces should map them here.
	return args
}
