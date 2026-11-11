# LunaRoute evidence and readiness

These are reviewed source notes and test-produced observations, **not a release
approval**. `register.yaml` contains exactly LR-01 through LR-08. Source review
on 2026-11-11 found unpublished model metadata and two unconfirmed identities.
LR-02 is escalated; other items remain decided. No live credential was available.

## Collection and population

1. Review the LunaRoute documentation, record source title, URL, access date and
   exact supporting passages. `sources/lr01-gateway-docs.md` covers paths/auth;
   `gateway-contract.md` covers listing/error contracts. Model notes have one
   anchor per release candidate, with omitted facts explicitly unknown. Do not
   use `/v1/models` membership as identity or static capability evidence.
2. Apply reviewed facts to their **production** sources: P1-T6/T7 identity maps,
   P1-T8 `capabilities.default.yaml:lunaroute_models`, P1-T2 error-code table.
   The embedded capability records currently carry only reviewed unknowns.
   They authorize no identity, limits, reasoning, effort or backend support.
   The fixed-id table authorizes only the two source-confirmed ids. Do not
   substitute another id or expand the production map to satisfy a test.
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

LR-02: no published per-model context/output/reasoning facts exist in the
required model-doc source. Claude Code/OpenCode/Pi must refuse unknown required
metadata. No reasoning default or placeholder limit is permitted. Obtain model
documentation or an explicit source-contract decision before enabling readiness.
LR-03: `glm-5.3-flash` and `deepseek-4.1-flash-1017` remain excluded from the
executable subset. Coordinated release-set/fixture revision needs authoritative
spellings or a release-set decision; no alternatives were invented.

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

**Consumer wiring is pending:** P2-T8 must feed the actual live HTTP response
through this extractor (or its transport through the proxy); P3-T5/P4-T1 must
supply real isolated binary launches and forced lifecycle producers; P4-T7 must
install the wrappers on its isolated PATH/Claude binary config and consume the
current MCP run's nonempty correlated records. Those experiments are not yet
implemented. No live capture artifact or LR verification is claimed here.
After any binary/version change, rerun its complete owning contract experiment
and the current MCP matrix, including required helpers, before reusing evidence.

## Release enforcement

Local missing live inputs explicitly skip with readiness pending. Release mode
fails missing prerequisites. The eventual completed gate requires source facts,
all eight verified LR contracts, real runtime application, and the P4-T7
completed MCP matrix with correlated main/helper evidence:

```sh
ARBITRON_LUNAROUTE_RELEASE_GATE=1 go test ./test/lunaroute -run '^TestReleaseGate$' -count=1
ARBITRON_LUNAROUTE_RELEASE_GATE=1 go test ./cmd/arbitron -run '^TestLunaRouteReleaseAcceptance$' -count=1 -timeout 90m
```

P4-T7 owns the second test and extends the first with full matrix validation;
it is not implemented yet. Do not treat running a nonexistent test or placing
an arbitrary file at the artifact path as acceptance. Current pending/escalated
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

Negative busy/service/unknown-cause/conflict cases are deterministic offline
fault injection, labelled as such; do not induce a live outage. Live invalid
credentials and completed positive tasks are separate evidence. Retain no raw
key/body/logs in artifacts. Review server logs successfully for leakage; an
ignored read error is not a secrecy check. Final verification is `go test ./...`
and `go vet ./...`. Neither instructions nor a local skip is readiness evidence.
