# LunaRoute backend enablement checklist

Gate: `domain/provider/lunaroute_validate.go:lunaRouteEnabledBackends`. A backend
may be added to that map only when **every** box in its section below is ticked
with a committed record (an observation file or a source note under
`docs/evidence/lunaroute-provider/`) that names the version it was observed on.
Constructor tests, offline fixtures and documentation checks do **not** tick a
box: they prove the code emits a shape, not that the deployed binary honours it.
Until a section is complete, the backend stays refused at the factory and the
interactive path, and the existing refusal tests
(`TestProductionR7CompletionApplication`, the `LunaRouteBackendEnabled`
assertions and the gate refusal cases in `adapter/provider` and `cmd/arbitron`)
keep passing.

Status as of this branch: **codex** and **pi** enabled (R7, 2026-11-12);
**claude-code** and **opencode** disabled (LR-05, LR-06 deferred).

## Applies to every backend

- [ ] **Deployed versions.** The versions operators actually receive — the
  `Dockerfile` pins and the installer defaults, not only the binaries inspected
  during development — are recorded, and the contract was re-run on those exact
  versions. A route-specific minimum (like `minLunaRouteCodexVersion` /
  `minLunaRoutePiVersion`) is added and tested to refuse an older binary before
  any credential-bearing launch.
- [ ] **Live routing and identity provenance.** A live run through the gateway
  shows which model executed, and the evidence is persisted with a correct
  `engine.ModelEvidence.Kind` (`reported_execution` only when the call itself
  reported the identity; otherwise `routing_intent`).
- [ ] **Credential and project isolation.** With a conflicting and an absent
  operator login/config, the launch uses only the private home or agent
  directory, project-level configuration and extensions do not load, the key
  appears in neither argv nor files, and protected inputs are unchanged and the
  private directory is cleaned.
- [ ] **Existing-provider regressions.** Subscription and non-LunaRoute launches
  of the same backend are unchanged: same argv, same environment, same version
  floor.

## codex (enabled)

- [x] Contract record: `observations/lr07-codex-config-controls.json`
  (codex-cli 0.167.2). Enforced floor: `minLunaRouteCodexVersion` = 0.167.2.
- [x] Manual tool task: `observations/operator-manual-agent-tasks-2026-11-12.md`.
- [ ] Re-verify on the version operators install. The `Dockerfile` does not
  install codex; record the version a deployment uses before relying on a floor
  lower than the one above.
- [ ] Gateway response evidence. `codex_turn_context` is `routing_intent`; it
  does not show what the gateway executed.

## pi (enabled)

- [x] Contract record: `observations/lr08-pi-model-controls.json` (pi 1.5.2).
  Enforced floor: `minLunaRoutePiVersion` = 1.5.2.
- [x] Manual tool task: `observations/operator-manual-agent-tasks-2026-11-12.md`.
- [ ] Re-verify on the version operators install. The `Dockerfile` does not
  install pi.

## claude-code (disabled)

- [ ] Re-run the LR-05 credential/config isolation contract on the version
  deployed. The `Dockerfile` pins Claude Code 2.1.170; the LR-05 source facts
  were inspected on 2.1.322. Either align the pin and the evidence or record
  that the pinned version satisfies each control.
- [ ] Add a claude-code LunaRoute minimum from that record and test refusal
  before launch.
- [ ] Live routing/identity pass, isolation pass and existing-provider
  regression pass (shared items above).
- [ ] Then, and only then, add `workflow.BackendClaudeCode` to
  `lunaRouteEnabledBackends` and flip the refusal tests deliberately.

## opencode (disabled)

- [ ] Re-run the LR-06 contract on the deployed version. The `Dockerfile` pins
  OpenCode 1.19.3; the LR-06 source/config/isolation evidence is for 1.19.5.
- [ ] **SDK provenance.** Establish whether the emitted
  `npm: "@ai-sdk/openai-compatible"` provider resolves to a bundled, pinned SDK
  or may resolve a package at runtime. The config string alone does not show
  where the code comes from. If runtime resolution is possible, pin it or keep
  the route refused.
- [ ] Establish whether the session export's `modelID`/`providerID` reflects the
  gateway response; until then its evidence stays `routing_intent`.
- [ ] Live routing/identity pass, isolation pass and existing-provider
  regression pass (shared items above), a minimum with refusal-before-launch
  coverage, then add the backend to `lunaRouteEnabledBackends`.

## Out of scope here (tracked separately)

The shell installer's unversioned npm defaults, mutable action tags in the
installer CI, and pi/tool subprocess credential inheritance under the existing
open execution posture are pre-existing debt, tracked in the PR #92 review
ledgers, not enablement criteria for this gate.
