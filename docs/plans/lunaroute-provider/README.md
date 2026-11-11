# LunaRoute implementation graph and review contract

Read the spec and the phase design before the task. Task paths are stable workflow identifiers; their numbers alone are not a sufficient dependency graph.

## Execution order and external gates

1. P1-T11 **evidence preparation** is independent research, before production fixtures are finalized: write the LR-01 docs source note, collect LR-02 static facts for the decided release set from LunaRoute's model docs, and confirm the LR-03 release-id spellings (decisions doc). The test/register framework portion of P1-T11 runs after P1-T10. These are two milestones of one owner, not a cycle through implemented adapters.
2. P1-T1–T10 establish the domain rules, store and binding producers. P2-T1–T8 add discovery/chat and shared launch/diagnosis wiring.
3. P3-T5 **contract preparation** uses direct isolated harness experiments before P3-T1/T2. Final adapter observations run after P3-T3. P3-T3 alone enables Claude Code/OpenCode.
4. P4-T1 settles codex/pi runtime contracts independently of their production adapters. P4-T2/T3 implement and enable codex; P4-T4/T5 implement and feed pi; P4-T6 updates guidance; P4-T7 owns the full MCP acceptance matrix.

Live evidence verifies the decided contracts. It is not a default/no-op implementation. No task may mark an LR item verified merely because it creates a register, skips a live test, emits a candidate config key, or passes a fake runner. When live access is missing, the item stays `decided` (not `verified`) and the release gate stays closed. Contracts are fixed by the 2026-11-10 decisions below and are implemented now. No service or binary contract was live-verified by editing these plans.

The release ids (`glm-5.3`, `glm-5.3-flash`, `deepseek-4.1-flash-1017`, `claude-sonnet-5-lr1`) are decided (LR-02/LR-03). P1-T11 confirms their spelling, and P1-T7 populates the production fixed-id table. If LunaRoute spells an id differently, replace every literal consistently. Pure grammar/family tests use injected internal fixture tables and restore them. Never add a production id or a vendor mapping merely to green a test.

## Shared producers, consumers and sink proofs

| Input/component | Production producer / owner | Consumers and root proof |
|---|---|---|
| Hosted root, dialect paths, auth | LR-01 decision (docs-verified 2026-11-10) → P1-T1 endpoint helper | Repo P1-T4, probe P2-T1/T6, chat P2-T4, each agent sink P3-T3/P4-T3/T5 |
| Submitted key / prior provider | MCP connection arguments → ConnectionService → ConnectionRepo; stored provider from `GetConnectionMeta` (P1-T4) | P1-T3 rule, conditional metadata update; MCP write tests assert persisted row and rejected writes |
| Lazy key / route metadata | `ConnectionRepo.Get/Reveal` → dynamic resolver, inventory `routeBinding`, CLI binding and discovery binding; P1-T5 guards all | Probe HTTP header and each agent env; invalid foreign key produces zero requests/launches |
| Explicit backend provenance | P1-T5 `ResolvedBinding.BackendExplicit` from profile; P2-T5 executor ORs explicit node override before defaulting | Actual-call validation rejects a legacy profile relying on deployment defaults; pinned resolver preserves the flag |
| Capability facts, reasoning metadata and request conflicts | LR-02 decision: doc-sourced static facts plus live completion observations → P1-T8 `lunaroute_models` loader on shared `CapabilityRules` | Profile service/pin snapshotter P1-T10, discovery JSON P2-T6, request preflight P2-T4, Claude limits P3-T3, pi definition P4-T5 |
| Actual vendor / fixed executable identity | LR-03 decision (reviewed ids + D8-style alias exception) → P1-T6 family table and P1-T7 reviewed fixed ids | Discovery, save, pins, launch; sample same-vendor and unresolved-family safeguard rejection |
| Parsed gateway code | LR-04 decision (documented codes) → P1-T2 `LunaRouteErrorCode` → probe/chat classifier calls | P2-T1/T3/T4 tests drive recorded code via actual HTTP/SSE response to node/MCP error |
| Shared probe selector / discovery service | P2-T6 `initDomainWithModelDependencies` constructs one ProbeFactory, injects Factory.Probes, discovery, MCP deps | Registered `connection_models` HTTP sink and P3-T3 diagnosed failed agent HTTP sink; pointer equality alone is insufficient |
| Claude/OpenCode selectors and limits | LR-05/LR-06 decisions → P3-T1/T2 emitters (verified by P3-T5); `Deps.ClaudeAgentConfig.CapabilityRules` existing root feed | P3-T3 fake-binary sink plus P3-T5 real config/helper requests |
| Codex overrides / private home | LR-07 decision → P4-T2, routing/reveal narrowing P4-T3 | Root agent run validates private home, retained route, model and cleanup; all credential/error sinks covered |
| Pi definition / relocation / facts | LR-08 decision (corrected `models.json`) + LR-02 separate reasoning/limits facts → P4-T4; Factory rules injection P4-T5 | Fake pi parses complete definition before returning success; missing facts/injection fail before reveal/launch |
| Readiness | P1-T11 register schema + referenced source artifacts; P4-T7 completed MCP matrix | Gate checks exact LR set, existing evidence files, populated runtime facts and current completion artifacts; skipped tests cannot yield readiness |

All new services remain domain-port based. No new workflow node kinds, subscriptions, global following catalog, recommendations, fallback, billing fields or dedicated model browser are introduced.

## Strict TDD in every task

For **each implementation change**, first add named tests with concrete assertions, run them and observe the intended failure, implement the minimum, run GREEN, then refactor and run the relevant package suite. No batch of implementation steps precedes its RED tests. Documentation/evidence tooling uses parser/schema/fixture rejection tests; live collection itself is an experiment, not a fabricated red/green test of a remote service.

Each task owns happy, success, failure, error and edge cases; the task's additions name its missing cases. Known inputs must propagate to the final sink. An intentionally empty production table does not prove a populated path works: root tests must load a nonempty, distinctive file fixture and assert its actual effect, and assert omission/mutation fails. Fakes validate endpoint, auth, model, settings, prompt/workspace and policy arguments relevant to their role; they do not return canned success after ignoring them. Integration tests assert terminal output/workspace or the credential-bearing HTTP/subprocess sink, not only an intermediate collaborator field.

Use the full exit code of `go test`/`go vet`; filtered output requires `set -o pipefail`. Run `go test ./...` once at final implementation validation. Live tests may skip locally, but release mode fails missing prerequisites or absent observations. This review edits plans only; it does not claim their future Go tests were executed.

## Resolved dependency decisions (2026-11-10)

LR-01 to LR-08 are **decided**. The binding record is
[`docs/designs/2026-11-10-lunaroute-provider-decisions.md`](../../designs/2026-11-10-lunaroute-provider-decisions.md).
Implement each contract now. The live tasks verify contracts and apply each
decision's **On failure** rule. They never invent or silently change contracts.

| ID | Producer `file:symbol` | Decided contract (summary) | Verifier / on failure |
|---|---|---|---|
| LR-01 | `domain/provider/lunaroute_endpoint.go:LunaRouteHostedRoot` (P1-T1) | Docs-verified root `https://gw.lunaroute.com`; `/v1/models`, `/v1/chat/completions`, `/v1/messages`, `/v1/responses`; `lr_` keys only; **every sink sends `Authorization: Bearer`** (Claude Code through `ANTHROPIC_AUTH_TOKEN`); `LUNAROUTE-PROJECT-ID` not sent | P1-T11 live gateway test; on failure → `escalated`, constants unchanged |
| LR-02 | `adapter/config/capabilities.default.yaml:lunaroute_models` (P1-T8, populated in P1-T11) | Release set `glm-5.3`, `glm-5.3-flash`, `deepseek-4.1-flash-1017`, `claude-sonnet-5-lr1`; static facts only from LunaRoute model docs; task/backend `supported` only from completed live runs; no placeholders | Unknown limit → model not usable on claude-code/opencode/pi; no model for a backend → `escalated` |
| LR-03 | `domain/modelidentity/version.go:lunaRouteReviewedIDs` (P1-T7) | Open-weight ids are fixed ids; reviewed aliases (`claude-sonnet-5-lr1` → `anthropic`, `alias:` target) are a D8-style pinned-only exception: no exact-version claim, vendor-family execution check, drift recorded; unreviewed aliases refused | Unconfirmed id removed from the set; no alias left → `escalated` |
| LR-04 | `domain/provider/lunaroute_classify.go:lunaRouteVerifiedCodes`, `LunaRouteErrorCode` (P1-T2) | `data[].id` listing; bare or `{"type":"error","error":{…}}` bodies; documented codes table plus new `ErrLunaRouteRequestRefused`; body code beats status; no `MODEL_NOT_FOUND`/`*_busy`; availability by re-list | P2-T8 confirms; mismatch → code removed, `escalated` |
| LR-05 | `adapter/claudecli/lunaroute_env.go:lunaRouteModelSelectorVars` (P3-T1) | Bearer via `ANTHROPIC_AUTH_TOKEN`; six selectors = binding model; hints; limits; `--setting-sources ""` on LunaRoute launches | P3-T5; ignored selector dropped; unroutable helper → enablement stops |
| LR-06 | `adapter/opencode/config.go:buildConfig` (P3-T2) | Overlay with registered model (`tool_call`, `limit`), `model`/`small_model`, `enabled_providers:["lunaroute"]`, `--pure`; unknown limits refused | P3-T5; ignored key dropped; unproven `small_model` → enablement stops |
| LR-07 | `adapter/codex/lunaroute.go:lunaRouteExecExtra` (P4-T2) | Private `CODEX_HOME`; five provider `-c` overrides plus `-c review_model=<id>`; `LUNAROUTE_API_KEY` env key; no user auth | P4-T1; login needed or traffic leaves → enablement stops |
| LR-08 | `adapter/pi/lunaroute.go:prepareLunaRoutePiLaunch` (P4-T4) | Relocation supported (`PI_CODING_AGENT_DIR`); `models.json` with `apiKey:"$LUNAROUTE_API_KEY"`, `authHeader:true`, LR-02 metadata; unknown metadata refused | P4-T1; misbehaviour → pi + LunaRoute stays refused, `escalated` |

Register statuses: `decided` (contract fixed, verification pending) →
`verified` (evidence plus `applied_to`) or `escalated`. Only `verified` passes the
release gate.
