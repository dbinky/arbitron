# LunaRoute evidence and readiness

These are reviewed source notes and test-produced observations, **not a release
approval**. `register.yaml` contains exactly LR-01 through LR-08. The operator's
2026-11-11 gateway pass verifies the service paths and Bearer contract (R1).
R2's recorded metadata now populates the embedded static capability table; the
missing-metadata escalation is cleared. R3 authorizes exactly `glm-5.3`,
`glm-5.3-flash` and `deepseek-4.1-flash`, never Claude-shaped aliases. Register
verification additionally requires the owning runtime/completion evidence;
release readiness remains pending.

## Collection and population

1. Review the LunaRoute documentation, record source title, URL, access date and
   exact supporting passages. `sources/lr01-gateway-docs.md` covers paths/auth;
   `gateway-contract.md` covers listing/error contracts. Model notes have one
   anchor per release model, with omitted facts explicitly unknown. R2 authorizes
   the recorded authenticated `/v1/models` metadata fields in
   `sources/lr02-live-models-2026-11-11.json`; membership alone is not a static
   fact or vendor-identity proof.
2. Apply reviewed facts to their **production** sources: P1-T6/T7 identity maps,
   P1-T8 `capabilities.default.yaml:lunaroute_models`, P1-T2 error-code table.
   The embedded capability records carry reviewed limits, independent reasoning,
   output-schema support and exact pi effort mappings. Temperature and other
   effort axes remain unknown. The production loader feeds these facts into
   MCP discovery and request preflight; root tests assert the resulting JSON
   and persisted over-limit failure, including zero inference requests.
   Static facts authorize no completed task/backend claim. The fixed-id table
   separately authorizes R3's three canonical ids.
3. Run live experiments with explicitly selected, reviewed models. Capture
   successful terminal content for all inference dialects, and actual
   main/helper traffic for each binary. Later P2-T8/P3-T5/P4-T1 completion
   observations alone may populate scoped task/backend support; a short run
   never proves a maximum limit or reasoning boolean.
4. Review credential-free artifacts and runtime application together. Update
   register statuses manually after review, not from a mock, launch or skip.
   `applied_to` entries are repository `file:symbol` references. The validator
   requires the file and symbol to exist, and production consistency tests
   separately compare cited facts with loaded/runtime values. File existence
   alone does not prove a contract is executed at every sink.

### Status rules and current blockers

- `decided`: a fixed contract with a decision reference; verification pending.
- `verified`: nonempty existing evidence **and** nonempty runtime application.
- `escalated`: contradiction/failed requirement, with actionable explanation
  and the decision's on-failure rule applied. Only `verified` passes release.

LR-02: R2 clears the source blocker; `model-capabilities.md` records the exact
snapshot-to-runtime mapping and nonempty source anchors. Task/backend completion
population remains with P2-T8/P3-T5/P4-T1. Required facts that a future override
omits must still fail closed, without a reasoning default or placeholder limit.
LR-03: R3's three canonical ids are executable; `deepseek-4.1-flash-1017` is a
weights name, not a served id, and `claude-*-lr*` aliases are revoked.
P1-T11's final runtime consistency gate still awaits the later backend/helper
consumers and P4-T7's completed correlated MCP matrix. Offline GREEN and a
locally skipped live rerun cannot mark those contracts ready.

## Framework and direct gateway regression

```sh
go test ./test/lunaroute -run 'Register|Observation|ReleaseGate' -count=1
go vet ./test/lunaroute
go test ./test/lunaroute -count=1 -v
```

Supply credentials privately through `ARBITRON_LUNAROUTE_E2E_KEY` (never echo or
commit them). The direct raw-HTTP experiment requires all three explicit model
inputs: `ARBITRON_LUNAROUTE_E2E_CHAT_MODEL`,
`ARBITRON_LUNAROUTE_E2E_CLAUDE_MODEL` (Messages), and
`ARBITRON_LUNAROUTE_E2E_CODEX_MODEL` (Responses). They must resolve as reviewed
ids and appear in the authoritative successful listing. No automatic selection
or fallback occurs. Then run:

```sh
go test ./test/lunaroute -run '^TestLiveGatewayContract$' -count=1 -v
```

This bypasses Arbitron adapters deliberately to verify the service contract.
It uses the fixed hosted root, Bearer only, no redirects, bounded contexts and
response sizes, validates listing ids and all three terminal responses, and
checks invalid-key 401/INVALID_API_KEY. A model-list success alone cannot pass.
A real attempted regression failure escalates LR-01 without editing constants;
missing prerequisites do not. Success writes a uniquely named
`observations/lr01-gateway-<UTC timestamp>.json`, never a verified label.

The writer rejects secrets (including escaped auth tokens and full untruncated
input), raw prompts/bodies/messages, unsafe basenames, symlinks and empty
observations **before writing**. Observations are JSON, owner-only directory
0700/files 0600, created exclusively without overwriting reviewed artifacts.
Only schema, safe routing/model ids, status and terminal/content booleans are
retained, never response text, auth values or environment dumps. A rejected
payload must be fixed at its producer, not truncated or written elsewhere.

## Isolated inference capture tooling (P2-T9)

Run the offline tooling gates (no real key or installed backend required):

```sh
go test ./test/lunaroute -run Capture -count=1
go test -race ./test/lunaroute -run Capture -count=1
```

`startInferenceCapture(t, runID, backend, syntheticConnectionHandle, key)` starts
an owner-private loopback CONNECT proxy for **only** `gw.lunaroute.com:443`.
Its temporary CA signs only that host. The live upstream is fixed HTTPS with
normal certificate verification, no ambient proxy and no redirect following.
Request headers/bodies, response bodies, records and exchange durations are
bounded. TLS/read/schema/cap failures poison the whole observation; they cannot
silently drop an unsuccessful exchange while retaining earlier successes.

For an isolated backend experiment, resolve the installed executable's absolute
path, then pass `capture.WrapBinary(t, absoluteBinary)` to the experiment's
runner. The launcher preserves argv/stdin/cwd and the production adapter's
environment; it replaces proxy/CA settings and clears conflicting bypasses.
It never writes a shared config, auth store or trust store. The launcher, CA and
private directory are removed by `Close` and test cleanup, including failed
launches. Do not disable TLS verification. Each installed runtime must actually
prove its own proxy/trust support; a fixture runtime's success proves tooling,
not Claude Code, OpenCode, Codex or Pi support. Go's macOS system verifier, for
example, does not automatically honor `SSL_CERT_FILE`.

Read `Records()` after terminal process completion; it waits for in-flight
exchanges to drain. Records contain synthetic run/request/connection handles,
method/host/path, selected and **reported** model ids, auth header **names** and
a match boolean, timestamps and terminal/failure flags. They contain no auth
values, hashes, prompts, tool arguments/results, raw response bodies or stderr.
`extractCapturedResponse` is the shared bounded JSON/SSE extractor for Messages,
chat completions and Responses (including `response.model`); an absent reported
model stays absent. A successful capture is routing evidence only: the owning
experiment must independently assert the finished tool task and workspace sink.

Helper evidence additionally requires the backend experiment's actual forced
operation/lifecycle parser. Feed its current run/backend, operation/session id,
trigger, source/version, time interval and exact observed request ids through
`CorrelateOperation`. Missing/unknown references, stale runs and self-labelled
helper categories are refused by `validateCapturedInference`. A lifecycle
fixture tests correlation mechanics only; it is not evidence that an installed
binary performed compaction, review, a subagent or another helper operation.

Before calling `writeObservation`, require
`validateCapturedInference(records, selectedModel, requiredCategories)` to pass.
It rejects empty traffic, mismatched auth/routes, missing helpers, mixed runs,
duplicate requests, nonterminal responses and unreported/mismatched models.
Fixed selections require exact reported spelling; reviewed aliases require the
reviewed vendor and retain observed drift. An observation must still pass the
shared credential-free JSON writer. Never publish failed/partial records as a
verified contract or populate backend support from these offline fixtures.

**Consumer wiring:** P2-T8 now feeds the production probe/chat factories' actual
HTTP responses through the shared extractor and validates nonempty current-run
chat records before publication. Its offline fixture proves the private JSON
sink, not live completion. P3-T5/P4-T1 must still supply real isolated binary
launches and forced lifecycle producers; P4-T7 must install wrappers on its
isolated PATH/Claude binary config and consume the current MCP run's correlated
records. Those binary experiments are not yet implemented. No live capture
artifact or LR verification is claimed here.
After any binary/version change, rerun its complete owning contract experiment
and the current MCP matrix, including required helpers, before reusing evidence.

## Discovery/chat adapter experiment (P2-T8)

With the key supplied privately and an explicit reviewed
`ARBITRON_LUNAROUTE_E2E_CHAT_MODEL`, run:

```sh
go test ./test/lunaroute -run '^TestLiveDiscoveryAndChat$' -count=1 -v
```

`ARBITRON_LUNAROUTE_E2E_CAPABILITIES` optionally names the deployed capability
override. The experiment loads that file through the production loader and
uses the real `ProbeFactory` and `Factory.ChatProviderFor` LunaRoute arms at the
canonical hosted root. It checks the full listing row ids, explicit membership,
terminal response/model, actual output containing pong, and invalid-key
401/INVALID_API_KEY through the real probe. No automatic model choice, fallback,
redirect, custom destination or insecure TLS is allowed. The same actual HTTP
response feeds the shared P2-T9 schema/model extractor; config and pins are not
traffic evidence. Read/cap/schema failures invalidate the whole experiment.

An optional reviewed `ARBITRON_LUNAROUTE_E2E_UNAVAILABLE_MODEL` must be absent
from a fresh successful listing and produce an actual non-success inference
response. Omission records deterministic root-fixture coverage, not a fabricated
live negative. No local grammar/preflight refusal counts as a gateway error.

Success writes owner-only `observations/lr04-discovery-chat.json` exclusively;
move an earlier reviewed artifact aside deliberately before rerunning. Only
schema, ids, status, code/type, auth header names/match booleans, token counts,
timing and terminal/pong booleans survive. Never persist raw content or messages.
Missing prerequisites skip locally and fail in release mode. This environment
has no live inputs: no artifact or supported fact has been published.

After genuine collection, review the observation against `gateway-contract.md`.
Apply LR-01's escalation on path/auth contradiction without editing constants;
remove any contradicted documented code from `lunaRouteDocumentedCodes`, update
the actual probe/chat sink fixtures, and escalate LR-04 in that same change.
Otherwise cite both sources in LR-04's verified evidence and its actual runtime
`applied_to` references. This task also owns populating **only** the selected
model's `tasks.chat: supported` in `capabilities.default.yaml`, citing this
observation, with source-consistency tests. Preserve every independent static
and backend fact. No capability or status change is authorized by an offline
fixture, local skip, or these instructions; collection/population remains an
unchecked P2-T8 milestone until real access is available.

## Manual MCP acceptance — discovery and chat (live server)

The offline root tests (`cmd/arbitron/lunaroute_*_wiring_test.go`) prove the
MCP → service → adapter wiring against a fake gateway. Live acceptance through
the real MCP tools is a manual run against a server started with a real master
key; record the outcome credential-free in
`observations/mcp-discovery-chat-<YYYY-MM-DD>.md` (supplementary manual notes,
not a JSON register observation).

1. `connection_create` `{name: "lunaroute-main", provider: "lunaroute", base_url: "", api_key: "<lr_ key>"}` → created with `base_url` = `https://gw.lunaroute.com`.
2. `connection_create` with an `sk-…` key → refused with the `lr_` guidance, no key echoed.
3. `connection_models` `{name: "lunaroute-main"}` → listed ids, `family`, explicit unknowns and availability note. Record ids/families, not credentials.
4. `profile_create` `{alias: "lr-chat", kind: "chat", connection_ref: "lunaroute-main", model: "<listed reviewed id>"}` → created. Listing alone does not authorize an unreviewed id.
5. `workflow_create` a one-node workflow (`llm_call`, `model_profile: "lr-chat"`, template asking for pong); `instance_trigger`; poll `instance_get` until completed and assert the output. A launch is not success.
6. `connection_update` with an invalid `lr_` key; `connection_models` → credential rejection; re-trigger → node fails with classified credential rejection. Restore the real key privately.
7. If a reviewed id is genuinely absent, save it explicitly (availability is not checked at save), then trigger → classified non-success/explicit uncertainty, no substitution. Otherwise record this live negative as not applicable and cite deterministic fixtures.
8. Inspect responses, logs and observations for credential leakage. Assert log reads succeeded; never copy keys or raw request/response content into notes.

Phase 4 Task 7 extends this to the completed release matrix on all four agent
backends, with actual main/helper capture. This procedure is not itself live
evidence and cannot flip a register status.

## Claude Code / OpenCode evidence tooling (P3-T5)

The independent preparation helpers and validator can be checked offline:

```sh
go test ./test/lunaroute -run 'AgentEvidence|Observation' -count=1
go test -race ./test/lunaroute -run AgentEvidence -count=1
```

`liveModel` requires an explicitly supplied, reviewed id via `os.LookupEnv`;
missing input skips using the calling test locally and fails in release mode.
`liveBinary`, bounded/safe `versionOf`, `liveRules` (the production capability
loader) and guarded `liveBinding` share the later backend experiments' inputs.
There is no recommended/default model. Raw version/process stderr is never
printed. `runCaptured` invokes the real runner with bounded context/output,
drains and waits, and retains raw lifecycle lines only in memory for parsers.

`collectAgentEvidence` requires current-run completed main **and** correlated
helper records, the backend's actual dialect, matching session/version, nonempty
reported identities (including permitted alias drift), a safe existing same-run
configuration identity projection, a tool-call trace, and both actual workspace
sinks. The primary file is exactly `LUNAROUTE-OK`; `wc -c` must produce `12` in
the second file. Both must be regular files written within the run interval;
pre-existing or symlinked proofs are refused. The owning launch must first call
`requireEmptyAgentWorkspace`. No summary text or caller-supplied success boolean
substitutes for these checks. `publishAgentEvidence` validates everything before
using the existing owner-private, exclusive JSON writer; rejection writes no
partial artifact. Captured request/response models survive into the JSON sink,
while summaries, prompts, command contents, credentials and stderr do not.

The subprocess/CONNECT/TLS fixture tests feed those sinks with actual traffic
and command-created files. Their simulated lifecycle and configuration identity
projection prove validation plumbing **only**, not effective Claude/OpenCode
controls, installed binary proxy support, or an actual helper operation. The
projection is not a replacement for the full same-emitted-env/config inspection
required by the real experiments. A backend-specific lifecycle parser must
produce each applicable helper category from a forced operation, not a label.

A version-only local query on 2026-11-11 UTC returned Claude Code `2.1.322`
and OpenCode `1.19.5` under a minimal credential-free environment. This is a
binary inventory, not a contract/helper observation or readiness evidence.

Direct live experiments remain blocked on the key, explicit models and required
documented static limits; production Factory/mission integration remains blocked
on P3-T1/T2/T3. No empty placeholder live test, contract observation, completed
agent artifact, supported capability or verified register label is shipped.
P3-T5 still owns those experiments, the production helpers and scoped population
when prerequisites exist. P4-T7 will consume current real MCP-run captures.

## Codex / Pi independent evidence tooling (P4-T1)

Run the deterministic tooling checks with:

```sh
go test ./test/lunaroute -run 'BackendEvidence|Captured' -count=1
go test -race ./test/lunaroute -run 'BackendEvidence|Captured' -count=1
```

`runCaptured(t,ctx,bin,args,env,cwd,stdin)` delegates to the existing bounded
real runner (now named `runCapturedLines` for line-oriented consumers). Its
caller supplies explicit env/cwd; it never inherits ambient provider auth.
Raw stdout/rollout bytes remain in memory and all launch/read/exit/timeout
errors return fixed safe text, never stderr. `seedConflictingHome` seeds only a
test-private HOME and workspace with conflicting codex/pi config and auth.
Protected user/project trees are compared by bytes, paths and modes; symlinks,
read failures, oversized trees and unexpected writes are refused. No shared
`~/.codex` or `~/.pi` files are touched.

`collectBackendEvidence` requires bounded, valid JSONL, a correlated session,
nonempty model records, successful final completion and tool-call evidence.
Pi tool-use intermediate messages are allowed, but an error, abort, length stop
or unfinished final tool turn fails even with exit zero. Codex needs its same
thread's rollout plus terminal turn completion; `codex_turn_context_intent`
explicitly denotes harness intent, **not** a gateway-reported model. Independent
nonempty main/helper request/response records are mandatory for both backends.
Pi's separate reasoning fact and both limits must be known; absent metadata is
never replaced by defaults or inferred from effort. Reviewed alias drift stays
visible in the reported identities.

The validator reads the exact `ok` proof from `lunaroute-ok.txt` and the derived
byte count `2` from command-generated `listing.txt`, rejects precreated/symlinked
files, checks the existing same-run configuration identity reference and
protected home snapshots, and refuses publication before private-dir cleanup.
This identity projection alone is not evidence that runtime overrides took
effect. Only after validation does the shared private/exclusive writer persist
sanitized fields; no prompts, command output, auth values, raw config, stderr
or subscription snapshots enter JSON.

Offline protocol subprocess fixtures use the real wrapper, CONNECT/TLS proxy,
nonce stdin/cwd and credential-bearing HTTP requests, then create both workspace
files with a command. Tests inspect the final JSON sink and missing-feed
failures. Their synthetic compaction lifecycle and model/config output prove
plumbing **only**, not installed codex/pi controls or helper routing. Both
present/absent subscription-file fixtures remain distinct from real auth
independence experiments.

Version-only inventory on 2026-11-11 UTC returned codex-cli `0.167.0` and pi
`1.5.1` under a temporary HOME and credential-free environment. No direct live
experiment ran: key, explicit codex/pi models and documented required pi facts
are absent. The planned `TestLiveCodexHelp`, `TestLiveCodexLunaRoute`,
`TestLivePiHelp` and `TestLivePiLunaRoute` experiments are not implemented as
placeholder skips; selecting those names currently runs no tests and is **not**
acceptance. P4-T1 still owns the real conflicting-config/auth/isolation runs,
backend-specific forced lifecycle collectors, credential-free contract/run
artifacts and scoped support-fact publication when prerequisites exist. A
binary upgrade requires rerunning those experiments, not only these fixtures.
No supported capability, LR-07/LR-08 verified status or enablement is authorized
by this tooling; P4-T2/T4 implement adapters, P4-T3/T5 enable them only after
genuine contract observations, and P4-T7 owns final MCP completion evidence.

## Release enforcement

Local missing live inputs explicitly skip with readiness pending. Release mode
fails missing prerequisites. The eventual completed gate requires source facts,
all eight verified LR contracts, real runtime application, and the P4-T7
completed MCP matrix with correlated main/helper evidence:

```sh
ARBITRON_LUNAROUTE_RELEASE_GATE=1 go test ./test/lunaroute -run '^TestReleaseGate$' -count=1
ARBITRON_LUNAROUTE_RELEASE_GATE=1 go test ./cmd/arbitron -run '^TestLunaRouteReleaseAcceptance$' -count=1 -timeout 90m
```

P4-T7 owns the second test and the first gate's full matrix-content extension;
neither is implemented yet. Its independent node/workspace tooling is described
below, not a completed release validator. Do not treat running a nonexistent
test or placing an arbitrary file at the artifact path as acceptance. Current pending/escalated
register entries already fail the first command. Before release also run the
direct gateway regression in release mode; it must not skip.

The matrix must include chat output, an actually executed reviewed proprietary
alias with actual vendor evidence/drift, and successful tool tasks on all four
backends (file + derived command artifact + tool trace). Pins/profile saves or
process exits alone are not completions. Current MCP-run evidence must cover
main and helper requests. Acceptance env vocabulary is shared:
`ARBITRON_LUNAROUTE_E2E_CAPABILITIES`, `_E2E_CHAT_MODEL`, `_E2E_ALIAS_MODEL`,
`_E2E_CLAUDE_MODEL`, `_E2E_CODEX_MODEL`, `_E2E_OPENCODE_MODEL`, `_E2E_PI_MODEL`,
`_E2E_UNAVAILABLE_MODEL` (all with the `ARBITRON_LUNAROUTE` prefix), and
`ARBITRON_LUNAROUTE_ACCEPTANCE=1` for local opt-in MCP runs.

### Partial MCP acceptance tooling (P4-T7)

`cmd/arbitron/lunaroute_acceptance_live_test.go` currently supplies stable
primitives only: exact `{id, queued}` trigger parsing, context-bounded polling
(up to 15 minutes), persisted-node/output/model-proof validation, bounded
workspace proof reads and successful bounded server-log reads with credential
scanning before truncation. `triggerViaMCP` and `waitLunaRouteTerminal` use the
registered production MCP handlers. Polling rejects failed/cancelled or
mismatched instances; its fetch callback must honor the supplied context.
SQLite's second-precision node timestamps are handled without allowing proof
files from a later second. Missing, unreadable, symlinked, stale or oversized
files cannot pass. Chat uses the actual response model; agent traces require
nonempty `model_evidence` and a persisted `tool_call`, preserve permitted alias
drift and reject unsafe identity fields. `codex_turn_context` remains explicitly
harness intent, not gateway response evidence.

`go test ./cmd/arbitron -run 'LunaRouteAcceptanceEvidence' -count=1` exercises
these boundaries. Its offline real-MCP chat case loads a distinctive capability
file, creates/discovers/selects through MCP, triggers and polls via MCP, checks
the actual authenticated HTTP sink, then reads the persisted node and real
server log. Separate node parser fixtures cover all four planned backend
source labels; a real local command derives their second workspace artifact.
Those synthetic traces are **not** installed-backend evidence. No artifact is
published by this partial tooling, and no new release acceptance test is
present to masquerade as a live skip.

Full matrix collection/validation, current-run P2-T9 captures plus genuine
forced-helper lifecycle/session/version correlation, live binary configuration,
metadata/runtime-source checks, and the final register gate extension remain
blocked on the owning P3/P4 producers and live prerequisites. Missing inputs
are not accepted as a no-op; Steps 1–5 of P4-T7 stay unchecked. No
`observations/mcp-release-acceptance.json` is fabricated and no register label
or supported capability changes.

Negative busy/service/unknown-cause/conflict cases are deterministic offline
fault injection, labelled as such; do not induce a live outage. Live invalid
credentials and completed positive tasks are separate evidence. Retain no raw
key/body/logs in artifacts. Review server logs successfully for leakage; an
ignored read error is not a secrecy check. Final verification is `go test ./...`
and `go vet ./...`. Neither instructions nor a local skip is readiness evidence.
