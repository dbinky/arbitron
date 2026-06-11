# Phase 3 / Task 2 — Sample mixed-model feature-pipeline config

**Design:** `docs/superpowers/specs/codex-backend-design-phase-3.md`
**Depends on:** Phase 1 (codex backend), Phase 2 (interactive streaming on
codex) — the sample is only meaningful once codex runs both node types.

## Goal
Ship the spec's worked example as runnable config: feature-pipeline's
`interviewer` runs on `gpt-5.5` via codex; `planner`/`coder` on `opus-4.8` via
claude-code.

## Files
- the feature-pipeline profile definitions. Locate where the sample profiles
  live — the deployment-config profile list (`adapter/config` defaults /
  `defaultYAMLContent`) and/or `samples/feature-pipeline/`. Edit the four
  profiles:

  | Profile      | Backend       | Model      | Kind  |
  | ------------ | ------------- | ---------- | ----- |
  | `interviewer`| `codex`       | `gpt-5.5`  | agent |
  | `planner`    | `claude-code` | `opus-4.8` | agent |
  | `coder`      | `claude-code` | `opus-4.8` | agent |
  | `assessor`   | `claude-code` | (unchanged)| agent |

- `samples/feature-pipeline/workflow.json` is UNCHANGED (nodes already reference
  these profile aliases; only profile definitions change).
- add/extend a config or sample loader test.

## TDD steps
1. **RED→GREEN:** a test loads the updated profiles and asserts:
   - `interviewer` resolves to `Backend == "codex"`, `Model == "gpt-5.5"`,
     `Kind == agent`.
   - `planner` and `coder` resolve to `Backend == "claude-code"`,
     `Model == "opus-4.8"`.
2. **validation:** the sample feature-pipeline workflow still passes
   `workflow_validate` with the new profile backends (codex is a valid agent
   backend after Phase 1 Task 1).

## Acceptance
- `go test ./adapter/config/...` (and any sample-loading test) passes.
- The sample workflow validates; profiles resolve to the intended
  backend/model split.
