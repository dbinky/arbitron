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
