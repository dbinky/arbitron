# LunaRoute implementation graph and review contract

Read the spec and the phase design before the task. Task paths are stable workflow identifiers; their numbers alone are not a sufficient dependency graph.

## Execution order and external gates

1. P1-T11 **evidence preparation** is independent research, before production constants/fixtures are finalized: obtain LR-01 hosted/auth/dialect evidence, LR-02 model facts, and LR-03 vendor/fixed-target evidence. The test/register framework portion of P1-T11 runs after P1-T10. These are two milestones of one owner, not a cycle through implemented adapters.
2. P1-T1–T10 establish the domain rules, store and binding producers. P2-T1–T8 add discovery/chat and shared launch/diagnosis wiring.
3. P3-T5 **contract preparation** uses direct isolated harness experiments before P3-T1/T2. Final adapter observations run after P3-T3. P3-T3 alone enables Claude Code/OpenCode.
4. P4-T1 settles codex/pi runtime contracts independently of their production adapters. P4-T2/T3 implement and enable codex; P4-T4/T5 implement and feed pi; P4-T6 updates guidance; P4-T7 owns the full MCP acceptance matrix.

External evidence is a prerequisite, not a default/no-op implementation. No task may mark an LR item verified merely because it creates a register, skips a live test, emits a candidate config key, or passes a fake runner. When access/evidence is missing, preserve the unresolved item and stop the dependent production-enablement work. The document alignment review leaves LR-01–LR-08 open below; no service or binary contract was verified by editing these plans.

Candidate ids such as `glm-5.3` are illustrative test inputs, not production facts. Before downstream root/live tests, P1-T11 preparation must supply reviewed executable ids and P1-T7 must populate the production fixed-id table; replace candidate literals consistently if evidence names different ids. Pure grammar/family tests use injected internal fixture tables and restore them. Never add a production id or a vendor mapping merely to green a test.

## Shared producers, consumers and sink proofs

| Input/component | Production producer / owner | Consumers and root proof |
|---|---|---|
| Hosted root, dialect paths, auth | LR-01 authoritative evidence → P1-T1 endpoint helper | Repo P1-T4, probe P2-T1/T6, chat P2-T4, each agent sink P3-T3/P4-T3/T5 |
| Submitted key / prior provider | MCP connection arguments → ConnectionService → ConnectionRepo; stored provider from `GetConnectionMeta` (P1-T4) | P1-T3 rule, conditional metadata update; MCP write tests assert persisted row and rejected writes |
| Lazy key / route metadata | `ConnectionRepo.Get/Reveal` → dynamic resolver, inventory `routeBinding`, CLI binding and discovery binding; P1-T5 guards all | Probe HTTP header and each agent env; invalid foreign key produces zero requests/launches |
| Explicit backend provenance | P1-T5 `ResolvedBinding.BackendExplicit` from profile; P2-T5 executor ORs explicit node override before defaulting | Actual-call validation rejects a legacy profile relying on deployment defaults; pinned resolver preserves the flag |
| Capability facts, reasoning metadata and request conflicts | LR-02 source notes/completion observations → P1-T8 `lunaroute_models` loader on shared `CapabilityRules` | Profile service/pin snapshotter P1-T10, discovery JSON P2-T6, request preflight P2-T4, Claude limits P3-T3, pi definition P4-T5 |
| Actual vendor / fixed executable identity | LR-03 authoritative mapping → P1-T6 family table and P1-T7 reviewed fixed ids | Discovery, save, pins, launch; sample same-vendor and unresolved-family safeguard rejection |
| Parsed gateway code | LR-04 protocol evidence → P1-T2 `LunaRouteErrorCode` → probe/chat classifier calls | P2-T1/T3/T4 tests drive recorded code via actual HTTP/SSE response to node/MCP error |
| Shared probe selector / discovery service | P2-T6 `initDomainWithModelDependencies` constructs one ProbeFactory, injects Factory.Probes, discovery, MCP deps | Registered `connection_models` HTTP sink and P3-T3 diagnosed failed agent HTTP sink; pointer equality alone is insufficient |
| Claude/OpenCode selectors and limits | LR-05/LR-06 actual-version evidence → P3-T1/T2 emitters; `Deps.ClaudeAgentConfig.CapabilityRules` existing root feed | P3-T3 fake-binary sink plus P3-T5 real config/helper requests |
| Codex overrides / private home | LR-07 → P4-T2, routing/reveal narrowing P4-T3 | Root agent run validates private home, retained route, model and cleanup; all credential/error sinks covered |
| Pi definition / relocation / facts | LR-08 format + LR-02 separate reasoning/limits facts → P4-T4; Factory rules injection P4-T5 | Fake pi parses complete definition before returning success; missing facts/injection fail before reveal/launch |
| Readiness | P1-T11 register schema + referenced source artifacts; P4-T7 completed MCP matrix | Gate checks exact LR set, existing evidence files, populated runtime facts and current completion artifacts; skipped tests cannot yield readiness |

All new services remain domain-port based. No new workflow node kinds, subscriptions, global following catalog, recommendations, fallback, billing fields or dedicated model browser are introduced.

## Strict TDD in every task

For **each implementation change**, first add named tests with concrete assertions, run them and observe the intended failure, implement the minimum, run GREEN, then refactor and run the relevant package suite. No batch of implementation steps precedes its RED tests. Documentation/evidence tooling uses parser/schema/fixture rejection tests; live collection itself is an experiment, not a fabricated red/green test of a remote service.

Each task owns happy, success, failure, error and edge cases; the task's additions name its missing cases. Known inputs must propagate to the final sink. An intentionally empty production table does not prove a populated path works: root tests must load a nonempty, distinctive file fixture and assert its actual effect, and assert omission/mutation fails. Fakes validate endpoint, auth, model, settings, prompt/workspace and policy arguments relevant to their role; they do not return canned success after ignoring them. Integration tests assert terminal output/workspace or the credential-bearing HTTP/subprocess sink, not only an intermediate collaborator field.

Use the full exit code of `go test`/`go vet`; filtered output requires `set -o pipefail`. Run `go test ./...` once at final implementation validation. Live tests may skip locally, but release mode fails missing prerequisites or absent observations. This review edits plans only; it does not claim their future Go tests were executed.

## Open external leaves (dependency provenance)

| ID | Missing input / consumer | Required evidence or human decision |
|---|---|---|
| LR-01 | `domain/provider/lunaroute_endpoint.go:LunaRouteHostedRoot`, auth/header and all dialect contracts | Confirm hosted destination and supported auth/path per operation from authoritative source plus successful per-dialect inference; model listing alone is insufficient. |
| LR-02 | `capabilities.default.yaml:lunaroute_models` limits, settings, reasoning and task/backend facts | Obtain authoritative values and separately scoped completion observations; decide which model per backend satisfies readiness. Unknown required pi metadata must block. |
| LR-03 | `domain/modelidentity/version.go:lunaRouteReviewedIDs`, `family.go:reviewedAliasVendors` | Establish vendor and fixed executable/response spelling of open-weight and proprietary/alias selections. If aliases are mutable, a human must resolve the existing immutable-pinning requirement without dropping the spec's alias outcome. |
| LR-04 | `domain/provider/lunaroute_classify.go:LunaRouteErrorCode` protocol envelope / verified codes | Confirm listing/error/SSE schemas and operation-specific meanings; unobserved codes remain unknown, never guessed from prose. |
| LR-05 | `adapter/claudecli/lunaroute_env.go` effective main/helper/limit controls | Verify supported installed version, all helper paths and ambient/project override precedence, or choose a supported isolation/control mechanism. |
| LR-06 | `adapter/opencode/config.go` model registration/helper/provider precedence | Verify actual custom-model metadata and routing controls; ignored keys cannot satisfy the supporting-call requirement. |
| LR-07 | `adapter/codex/lunaroute.go` Responses/auth/private-home/helper contract | Verify actual installed version, all provider overrides and project/helper isolation; recorded turn model alone is insufficient. |
| LR-08 | `adapter/pi/lunaroute.go` custom schema, relocation, required metadata/helper precedence | Verify actual pi version and per-run relocation; if unavailable, a human must resolve the required Pi outcome rather than writing shared dotfiles or removing Pi. |
