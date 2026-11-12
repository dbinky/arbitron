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
model stays absent. Messages streams require a start before content/deltas,
no content or repeated finish after the stop reason, and a final message stop;
pings do not count as content or completion. Reordered streams cannot publish
a completed agent observation even when tool/workspace proofs exist. A successful
capture is routing evidence only: the owning experiment must independently
assert the finished tool task and workspace sink.

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
R3's three canonical selections require exact reported spelling. Revoked
Claude-shaped aliases and the unserved DeepSeek weights spelling are rejected,
regardless of the reported vendor/model. An observation must still pass the
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
Missing prerequisites skip locally and fail in release mode. The operator
completed this experiment on 2026-11-11 and committed
`observations/lr04-discovery-chat.json`; agents do not receive the private key.
The current content-bearing smoke budget is 1024 output tokens (R4's reasoning
floor), not 64.

After genuine collection, review the observation against `gateway-contract.md`.
Apply LR-01's escalation on path/auth contradiction without editing constants;
remove any contradicted documented code from `lunaRouteDocumentedCodes`, update
the actual probe/chat sink fixtures, and escalate LR-04 in that same change.
Otherwise cite both sources in LR-04's verified evidence and its actual runtime
`applied_to` references. This task also owns populating **only** the selected
model's `tasks.chat: supported` in `capabilities.default.yaml`, citing this
observation, with source-consistency tests. Preserve every independent static
and backend fact. No capability or status change is authorized by an offline
fixture, local skip, or these instructions. R4 and the genuine committed
observation have now been applied: LR-04 is verified, its two additional codes
reach production probe/chat errors, and only `glm-5.3` chat is supported by this
artifact. The historical unavailable-model `cause_unknown` label is retained
unchanged in the artifact; production now maps `INFERENCE_MODEL_NOT_FOUND` to
model unavailable. R3 revokes that artifact's Claude-shaped selection, so future
negative reruns must select an absent canonical reviewed id, or omit the case.
Manual live MCP acceptance below is still pending; no backend readiness or
whole-release verification is claimed.

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
reported canonical identities (R3 rejects Claude-shaped aliases), a safe existing same-run
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

`liveFactory` now composes the real production Factory with its real runner,
probe selector, workspace and shared capability table (including the separate
Claude configuration feed). `runLiveMission` creates a fresh private child
workspace and runs the actual selected adapter, requiring both file/command
proof and a tool-call trace. OpenCode retains the current session established by
its verified export and emits LunaRoute tool steps as `tool_call`; other
providers retain their existing session/trace contract. The Factory fixtures
exercise both backend protocols, authenticated CONNECT/TLS inference, capability
limits, actual command-created files and safe launch-failure handling. Failure
fixtures inject a bounded offline HTTP client into the same probe composition;
no diagnostic request leaves the fixture. They are
not installed-binary contract or helper evidence.

The Factory's experiment runner also validates Claude's **main startup** from
its actual bounded JSONL stream. A `--version` query uses the same emitted
executable/env/cwd; exactly one ordered `system/init` must identify that reviewed
version, selected canonical model, actual workspace and the final successful
result's session. Missing/drifting startup refuses mission acceptance even when
the subprocess completed authenticated inference and created both files.
See [the 2.1.322 schema/producer source note](sources/lr05-startup.md).
`go test ./test/lunaroute -run TestAgentEvidenceClaudeStartup -count=1` checks
this slice. It proves startup identity and emitted launch agreement, **not**
effective limits, helper selectors, settings precedence, memory or unchanged
user/project files. Claude's remaining control/isolation and forced-helper
collectors and complete live entry point are still required. No main-only
agent/contract observation or supported/verified fact is published.

A separate, narrow `TestLiveClaudeCodeIsolation` experiment now seeds private
conflicting user/project/local settings and plaintext auth inputs. Its real
Factory guard binds bounded byte/mode snapshots to the actual emitted HOME/cwd,
refuses relocation or alternate credential env before even the version query,
and refuses changed protected inputs after the process. The final collector
requires the same successful session, actual current Messages/Bearer/model
capture, tool trace and command-created workspace proofs. It writes only
`observations/lr05-isolation.json`, after successful proxy/CA/private-workspace
cleanup. Run with the existing key and explicit `_E2E_CLAUDE_MODEL` inputs:
`go test ./test/lunaroute -run '^TestLiveClaudeCodeIsolation$' -count=1 -v`.
Missing input skips locally with readiness pending and fails in release mode.
See [the partial source/scope note](sources/lr05-isolation.md) and
`TestAgentEvidenceClaudeIsolation` for real Factory/TLS/private-JSON refusal
fixtures. The same experiment also joins the final usage's **resolved context
window** to the loaded table and actual emitted env, and the real captured
Messages **`max_tokens`** to its output fact. Its private `main_limits` projection
names the exact current request ids/session/version. Missing/drifting feeds
refuse publication despite successful files/inference. Usage `maxOutputTokens`
is a catalog default, not the env-adjusted request budget; it is not used as
output-control proof. See [the inspected producer note](sources/lr05-limits.md)
and `go test ./test/lunaroute -run 'TestAgentEvidenceClaude(MainLimitValidation|LimitsFactorySink)$' -count=1`.
Before publication, the operator entry point now also reads the cited R2
snapshot and requires exactly one selected-model record whose context/output
limits agree with those actual table/env/usage/request proofs. The final
`input_source` names that snapshot; missing, malformed, duplicate or drifting
facts and unreadable source references cannot publish. Safe projection and
successful private HOME/workspace/proxy/CA cleanup precede the exclusive write.
Run `go test ./test/lunaroute -run 'TestAgentEvidenceClaude(SourceBoundPublication|InputSourceValidation)$' -count=1`.
The synthetic source fixtures prove plumbing only, not new limits, installed
control behavior or release readiness.
The same experiment now seeds a private per-run `CLAUDE.md` nonce, protects
its bytes/modes and requires its exact source-formatted frame in the **first**
actual Messages user-text request. It publishes only safe `project_memory`
source/request-id/loaded fields alongside the existing session and workspace
proofs; later file reads, assistant echoes, missing/drifting memory or a missing
capture feed cannot substitute. Capture cleanup discards private comparison
material. See [the memory producer and unresolved settings-source concern](sources/lr05-memory.md)
and `go test ./test/lunaroute -run TestAgentEvidenceClaudeMemory -count=1`.
Source review does not establish that the installed binary preserves memory
under empty settings sources; the operator experiment must fail if it does not.
The same Factory now also consumes the selected binding's effort, the actual
emitted env and the same capability table. The Messages proxy projects the
actual `output_config.effort`; missing projection or a downgraded/absent explicit
graded level refuses the final private `main_effort` sink despite completed
inference/files. Default records only the observed value/absence, never an
inferred exact effort. Current production Claude effort facts stay unknown;
explicit levels are tested with distinctive offline facts, not invented live
support. Per-message overrides remain refused. See [the versioned source and
scope](sources/lr05-effort.md) and
`go test ./test/lunaroute -run TestAgentEvidenceClaudeEffort -count=1`.
The same experiment now also records actual Messages thinking type/budget in
`main_thinking`, with exact source/version/session/model/request ids. Explicit
`none` requires known same-table/emitted-zero-budget agreement **and** an actual
`thinking.type: disabled` field on every request. Absent/adaptive/enabled fields
or a lost capture feed cannot publish it despite completed files/inference.
Default/graded selections record observed controls only. The installed source
conditionally omits the disabled field; omission cannot prove reasoning off.
See [the source/predicate and scope note](sources/lr05-thinking.md) and
`go test ./test/lunaroute -run TestAgentEvidenceClaudeThinking -count=1`.
These fixtures establish collection/refusal, not installed thinking support;
no new live flag, support fact or register status is introduced.
The same isolated Factory now also consumes the actual JSONL synchronous
subagent lifecycle: an Agent call, foreground local-agent registration and a
matching structured completed result, with exact session/task/run/tool/model
agreement. The Claude mission explicitly permits Agent and requests a
synchronous general-purpose file verifier with explicit `model=haiku`. Its
actual Agent input must retain that selector and the emitted Haiku/subagent
selectors must equal the selected canonical id. `requested_model` records the
observed Agent input separately from the harness-reported and actual gateway
models. Missing input or a lost feed refuses publication; neither a default
subagent nor a fixed-id override proves this requested Haiku path. This is
source-backed forcing/collection, not installed selector verification or a
small-fast/other-helper claim. Missing, reordered, background,
failed, substituted or unsafe records refuse the final private
`subagent_lifecycle` sink even when authenticated main inference and both files
succeed. The lifecycle keeps its model labelled `harness_reported_model`.
The operator isolation entry point additionally joins actual current-run routing
headers to that lifecycle before publication: root-session main traffic and
separate general-purpose requests with the exact task id, selected response
model and authenticated Messages path are required. Only joined request ids
are marked `subagent`; missing/foreign/unsafe/stale feeds write no artifact.
Existing main-only lifecycle fixtures remain narrow controls; the new
Factory/subprocess/CONNECT/TLS routing fixtures exercise both exchanges and the
final private JSON sink. Neither fixture set proves installed-helper behavior.
The same publication path separates `main_limits`, `main_effort` and
`main_thinking` request ids from lifecycle-attributed `subagent_controls`.
The helper projection records each actual request's output budget, effort and
thinking type/budget, with exact task/run/tool/session/version and source refs.
Default records observed controls (including absence), not inferred support;
explicit effort and `none` retain the same-table/env/wire refusal checks.
No helper context-window or enforced gateway maximum is inferred. Missing
control/routing feeds refuse publication despite successful files/inference.
See [lifecycle sources](sources/lr05-subagent-lifecycle.md),
[request sources and join scope](sources/lr05-subagent-routing.md) and
`go test ./test/lunaroute -run 'TestAgentEvidenceClaude(Subagent|Routing)' -count=1`.
This is **not** the full Claude agent/contract entry point: keychain isolation,
effective selectors, complete thinking/effort and limit enforcement, installed
memory and helper verification remain pending. A short request establishes no gateway maximum.
No operator observation or support/status change is inferred.

The Factory's experiment runner now queries OpenCode `debug config --pure`
with the actual adapter-emitted executable/env/cwd before launching each mission.
It validates the resolved main/helper selection, provider, destination, registered
tool/limit facts and redacted credential field against that emitted overlay;
unsafe/drifting/failed queries stop before inference. Raw debug output stays in
bounded memory. The installed 1.19.5 schema/precedence check also uses isolated,
conflicting user/project configs with a no-overlay control and unchanged-byte
assertions. See [the versioned source note](sources/lr06-effective-controls.md).
This verifies neither actual helper calls nor Bearer auth; capture still owns
those proofs. No completed-agent artifact is published by a control query.

The experiment Factory also consumes source-grounded synchronous OpenCode
`task` lifecycles at the actual parent-export seam, querying each child with the
same emitted executable/env/cwd. Its mission permits Task and explicitly asks
for a synchronous general subagent. A missing, stale, failed, background,
substituted or incomplete child refuses the mission despite valid workspace
files and main inference. See [the lifecycle source note](sources/lr06-subagent-lifecycle.md)
and `TestAgentEvidenceOpenCodeSubagent`. Protocol exports in those tests are
synthetic: they do not establish installed-helper completion or captured helper
routing, and no completed-agent artifact is published.

R2 supplies required static facts; P3 emitters/enablement and root sinks exist.
OpenCode subagent, first-conversation title and first automatic-compaction
correlation/version provenance and the source-backed automatic-compaction
threshold/continuation driver now exist offline. Both the supporting-call
experiment (`TestLiveOpencodeCompaction`) and completed OpenCode agent experiment
(`TestLiveOpencodeAgentTask`) have real operator entry points. Remaining work
includes Claude forced-helper and same-emitted-env effective-control collection
and its full live entry point. A main-only mission cannot publish a completed
agent observation. Operator runs additionally need a private key and explicit
canonical models (R6); absence of those inputs does not block offline
implementation. No completed live artifact, supported capability or verified
register label is inferred from fixtures or skips. P4-T7 will consume current
real MCP-run captures.

### Completed OpenCode agent experiment (P3-T5)

```sh
go test ./test/lunaroute -run '^TestLiveOpencodeAgentTask$' -count=1 -v
```

Supply `ARBITRON_LUNAROUTE_E2E_KEY`, an explicit reviewed
`ARBITRON_LUNAROUTE_E2E_OPENCODE_MODEL`, installed OpenCode 1.19.5 and optionally
`ARBITRON_LUNAROUTE_E2E_CAPABILITIES`. The experiment freshly lists the selected
model (no fallback), then constructs the production Factory with the real
runner, capability feeds, private conflicting config/auth snapshots, same-env
control journal and source-backed subagent/title/automatic-compaction drivers.
The narrow compaction experiment uses the same composition.

Only after exact workspace/command/tool/model proof, all four correlated request
categories, both effective-control checks, unchanged private config/auth and
readable exact helper source references and snapshot limits matching the actual
effective model definition does finalization close/remove the
proxy/CA and private experiment tree. Successful cleanup precedes writing
`observations/lr06-opencode-compaction.json` and
`observations/lr06-opencode-agent.json`; the latter references the former's
same-run/session/version controls and actual inference records. Prompts, summaries,
raw configuration, paths and credentials are not serialized. The writer is
exclusive: archive prior observations for review before collecting a new pair,
including a prior narrow-compaction artifact. A publication error is not success.

`TestAgentEvidenceOpenCodeCompletedAgentPublication` drives these final sinks
through protocol executables, real TLS exchanges and command-created files. It
rejects empty capture, missing controls/isolation/tool/file/source, unreadable or
misassigned source evidence, unsafe model provenance and cleanup failure with no
published pair. Malformed or model/limit-mismatched source snapshots also cannot
publish despite completed traffic/files. These fixtures prove plumbing, not installed binary contracts.
The operator run and subsequent source/support/register review remain pending;
this code never changes capability facts or register status automatically.

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
or unfinished final tool turn fails even with exit zero. Its successful final
assistant message must precede exactly one final `agent_end`; missing, premature,
duplicate or trailing lifecycle events prevent publication. Codex requires a
thread before its turn, an active turn for tool/completion events, and the same
thread's rollout session before model contexts; `codex_turn_context_intent`
explicitly denotes harness intent, **not** a gateway-reported model. Independent
nonempty main/helper request/response records are mandatory for both backends.
Pi's separate reasoning fact and both limits must be known; absent metadata is
never replaced by defaults or inferred from effort. R3 requires exact requested
and reported canonical identities for all three release models. Revoked
Claude-shaped aliases, the old DeepSeek weights spelling and fixed-id drift
cannot publish an observation, even when config and traffic otherwise agree.

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
experiment ran: private key and explicit codex/pi model selections remain
operator-only inputs. R2's required pi facts and the production adapters are now
implemented; missing access does not block offline code under R6.

The independent help entry points now run real, credential-free queries:

```sh
go test ./test/lunaroute -run '^TestLive(Codex|Pi)Help$' -count=1 -v
go test ./test/lunaroute -run '^TestBackendHelp' -count=1
```

`TestLiveCodexHelp` and `TestLivePiHelp` use the same resolved executable,
private HOME/config/workspace and bounded runner for version and help queries.
They require exact options in the actual Options section, discard raw output,
and remove private directories before returning a safe projection. They need
no live key or model and are not placeholder inference skips. Missing binaries
skip locally with readiness pending and fail in release mode. On 2026-11-12 UTC,
the installed queries passed on codex-cli `0.167.1` and Pi `1.5.2`; see
[sources/lr07-lr08-cli-help.md](sources/lr07-lr08-cli-help.md) for exact scope.
Help proves advertised CLI spellings only, **not** runtime contracts or readiness.

`TestLiveCodexLunaRoute` and `TestLivePiLunaRoute` remain unimplemented;
selecting only those names runs no tests and is **not** acceptance.
P4-T1 still owns the real conflicting-config/auth/isolation runs,
backend-specific forced lifecycle collectors, credential-free contract/run
artifacts and scoped support-fact publication when prerequisites exist. A
binary upgrade requires rerunning those experiments, not only these fixtures.
No supported capability or LR-07/LR-08 verified status is authorized by this
tooling. P4-T2–T5 have implemented and wired the production adapters offline per
R6; readiness still requires genuine observations. P4-T1's missing source-backed
helper/effective-control collectors prevent complete live entry points, not the
already implemented emitters. P4-T7 owns final MCP completion evidence.

### OpenCode captured-title correlation (partial P3-T5)

The captured experiment Factory also requires the actual first-conversation
`ensureTitle` exchange and its matching exported root-session title. The proxy
recognizes the versioned system/user wire signature with the exact mission
history, validates terminal JSON/SSE and keeps normalized title text only in
private memory. Same-run session, selected request/reported models, auth and
same-executable version must agree before the request is marked `title`.
Main traffic cannot stand in for the title request. Missing, duplicated,
substituted, unsafe or nonmatching evidence refuses publication despite valid
files and subagent completion. Close discards the private comparison material.

See [the title lifecycle source note](sources/lr06-title-lifecycle.md) and run
`go test ./test/lunaroute -run TestAgentEvidenceOpenCodeTitle -count=1`.
Factory protocol fixtures reach the final private JSON sink, not merely an
intermediate config field. They are not installed-binary observations and do
not establish support or readiness. Applicable compaction/other helpers and
Claude controls/helpers still need collectors before complete live entry points.

### OpenCode captured-compaction correlation (partial P3-T5)

The captured Factory requires a first automatic-compaction exchange in addition
to main, synchronous subagent and title evidence. The proxy recognizes the
versioned system/prompt/template signature and exact mission history, validates
terminal JSON/SSE and retains bounded summary text privately. The actual
adapter-requested same-env export must contain its ordered current automatic
compaction user/part and successful selected-model summary. Exact root session,
request interval, exported text and executable version must agree before that
request is labelled `compaction`. Missing/duplicate/foreign/stale/unsafe or
incomplete evidence prevents final publication despite completed workspace
proofs and the other helper exchanges. Close clears private summary material.

See [the compaction lifecycle source note](sources/lr06-compaction-lifecycle.md)
and run `go test ./test/lunaroute -run TestAgentEvidenceOpenCodeCompaction -count=1`.
The Factory/proxy/subprocess fixtures inspect final private JSON and actual
command-created files. They synthesize the lifecycle and are not evidence that
the installed binary performed compaction. The forcing driver now reads actual
warm-up usage, registers R2's input limit without changing context/output facts,
validates the effective reserve/input/auto controls, and continues the exact
session before the adapter's normal export/request joins. A successful final
post-compaction turn is mandatory. The installed offline debug test proves
schema acceptance only. No supported fact, register status or live artifact
follows from fixtures.

Operator supporting-call run (private inputs supplied outside the repo):

```sh
go test ./test/lunaroute -run '^TestLiveOpencodeCompaction$' -count=1 -v
```

Requires `ARBITRON_LUNAROUTE_E2E_KEY`, explicit canonical
`ARBITRON_LUNAROUTE_E2E_OPENCODE_MODEL`, installed OpenCode 1.19.5 and optionally
`ARBITRON_LUNAROUTE_E2E_CAPABILITIES`. Local missing inputs explicitly skip;
release mode fails them. The actual Factory/runner uses private HOME, conflicting
user/project seeds and the hosted-only capture wrapper. Only after real tool
proofs, correlated main/subagent/title/compaction traffic, unchanged seeds and
successful cleanup does it write `observations/lr06-opencode-compaction.json`.
The artifact now includes bounded effective-control projections from both actual
same-emitted-env debug queries (initial launch and exact-session continuation),
not reconstructed configuration or caller-set success booleans. The collector
requires matching model/limits, executable/workspace, ordered current query and
inference times, and the resumed session. It checks real workspace/tool/model
proofs before cleanup; absent, extra, drifting or stale controls prevent any
publication. Executable/cwd stay private, and raw debug/config/auth text is never
serialized. Run `go test ./test/lunaroute -run TestAgentEvidenceOpenCodeControlsToSupportingObservation -count=1`
for the real Factory/subprocess/TLS/private-file sink fixtures. Those remain
synthetic protocol tests, not installed-backend verification.

The same experiment now seeds conflicting private user/project config and
provider auth and snapshots their bounded bytes/modes. Both real emitted
launches must use those protected HOME/project roots, checked before even the
debug query runs. Publication additionally requires the complete user config
tree, project config and auth file to remain unchanged, with no missing input,
new config entry, symlink or relocation override. Only safe isolation booleans
and the [versioned source reference](sources/lr06-isolation.md) enter the artifact;
paths and snapshot/auth bytes stay private. The installed inference-free schema
check verifies that both seed layers actually load without the overlay and that
the overlay restores selected controls without changing the protected inputs.
Run `go test ./test/lunaroute -run 'TestAgentEvidenceOpenCode(Isolation|InstalledIsolationSeeds)' -count=1`.
This is not a live contract observation.

This supporting artifact does not replace the full LR-06 contract/agent
milestones or populate capabilities. Remaining complete helper/control
collection, full agent entry points and operator observations stay pending.

### OpenCode captured-subagent correlation (partial P3-T5)

`liveCapturedFactoryWithClient` feeds the actual P2-T9 proxy into the existing
Factory/control/subagent collectors. It joins safe session/parent request
headers and completed gateway model responses to validated parent/child exports,
task intervals and a bounded same-executable/env/cwd version query. Only exact
child requests are marked `subagent`; parent traffic alone cannot pass.
`TestAgentEvidenceOpenCodeSubagentCorrelatedFactoryPublication` checks the final
private JSON sink through real Factory → subprocess → CONNECT/TLS → workspace
proofs. See `sources/lr06-subagent-lifecycle.md` for versioned header sources.

The protocol exports are synthetic and establish tooling only. Installed-binary
collection, the other OpenCode helper categories and Claude controls/helpers
still precede full live entry points and any support/status publication. No new
live observation is claimed and no release gate is opened by these tests.

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
