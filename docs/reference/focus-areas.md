# Focus Areas Review Tracking

This document tracks the systematic review of the **codex agent backend**
implementation (batch `agent_call` + interactive `agent` streaming on the user's
ChatGPT/Codex subscription) for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Backend vocabulary (`domain/workflow/workflow.go` `BackendCodex` + `IsValidAgentBackend`; exact-match lowercase, fail-closed near-miss rejection; allowed-set / `NodeConfig.Backend` / validator-message doc accuracy) | [x] | [x] | done |
| 2 | Codex argv builder (`adapter/codex/args.go` — `exec --dangerously-bypass-approvals-and-sandbox -m <model> --cd <WorkDir> --json`; prompt via stdin, NEVER argv; `ErrModelRequired` on empty/whitespace model; flag spellings pinned in one const block, version-recorded) | [x] | [x] | done |
| 3 | Codex subscription env (`adapter/codex/env.go` — `procenv.BaseEnv()` + `HOME` preserved, `CODEX_HOME` forwarded only when set; NO `OPENAI_API_KEY`; never calls `binding.RevealKey`; cannot fail) | [x] | [x] | done |
| 4 | Codex event decoder (`adapter/codex/parse.go` — shared `decodeLine`/`foldEvent`; `thread.started`/`turn.started`/`item.completed`/`turn.completed`/`error`/`turn.failed`; unknown event/item kinds tolerated; `ErrParse` on malformed JSON; Summary/Steps/tokens/thread fold) | [x] | [x] | done |
| 5 | Codex version probe (`adapter/codex/version.go` — memoized `sync.Once` over the shared runner; `minCodexVersion`/`ErrIncompatibleCodex`; binary resolution PATH/StandardBinDirs) | [x] | [x] | done |
| 6 | Codex batch provider (`adapter/codex/provider.go` `RunAgent` — strict fail-closed ordering WorkDir→args→env→probe→run→parse; empty WorkDir/model ⇒ ZERO launches; runner/exit error names codex+model) | [x] | [x] | done |
| 7 | Codex streaming adapter (`adapter/codex/stream_adapter.go` `Chat`+`StreamChat` — ordered `ChatChunk` per `item.completed`; terminal chunk carries tokens/thread; `turn.failed`/`error` closes with typed error not silent Done; `ChatRequest.Model`/`ToolScope` precedence; batch/stream fold parity) | [x] | [x] | done |
| 8 | Factory backend dispatch (`adapter/provider/factory.go` `AgentProviderFor` `case BackendCodex` → `codex.New`; `Deps.CodexBinaryPath`/`CodexTimeout`; `ErrUnknownBackend` on case-variant/unknown; INVARIANT doc updated) | [x] | [x] | done |
| 9 | Interactive stream selector (`adapter/provider/interactive_stream_selector.go` — backend-keyed map + default floor; claude-code/codex/opencode-unsupported/unknown; `ErrUnsupportedAgentBackend` + `errors.Is`; chat-kind still delegates) | [x] | [x] | done |
| 10 | Codex readiness (`adapter/codex/readiness.go` — `Ready`/`NotInstalled`/`NotLoggedIn`; `codex --version` + `codex login status` by exit-status only; state-not-error for not-ready; runner-fault→error; no `OPENAI_API_KEY`, no token logging; incompatible⇒NotInstalled) | [x] | [x] | done |
| 11 | Readiness health surface (`adapter/backendhealth` + `config.RedactedConfig.Backends`; `system_config` MCP handler + HTTP `GET /system/config`; `{name, opaque state}` only; probe fault drops entry; `adapter/codex` kept out of `domain/config`) | [x] | [x] | done |
| 12 | Sample mixed-model config + composition wiring (`adapter/config/loader.go` defaults + `defaultYAMLContent` profiles: interviewer→codex/`gpt-5.5`, planner/coder→claude-code/`opus-4.8`, assessor `kind: chat`; `cmd/arbitron/main.go` `Deps.Codex*` + codex `StreamProvider` + backend→stream map + default floor; sample workflow validates) | [x] | [x] | done |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 13 | Argv + env + batch provider (§2+§3+§6 — the exec slice shares one argv/env vocabulary; prompt-via-stdin and subscription-env invariants hold through `RunAgent`) | [x] | done |
| 14 | Decoder + streaming adapter (§4+§7 — batch `RunAgent` and `StreamChat` fold an identical fixture to identical summary/session/tokens; no divergence after the shared-decoder refactor) | [x] | done |
| 15 | Factory arm + stream selector (§8+§9 — both dispatch the backend axis with the same vocabulary + default floor; codex reachable on `agent_call` AND `interactive_agent`; opencode unsupported on stream only; unknown fails closed in both) | [x] | done |
| 16 | Version probe + readiness (§5+§10 — shared binary resolution + version semantics; `minCodexVersion` failure vs readiness `NotInstalled` collapse stay consistent) | [x] | done |
| 17 | Readiness + health surface (§10+§11 — state flows `codex.Readiness` → `backendhealth` → `RedactedConfig.Backends` → both read surfaces; a probe fault drops the entry without failing the config read) | [x] | done |
| 18 | Sample config + dispatch (§12+§8+§9 — the `interviewer` codex profile resolves to `Backend == "codex"` and routes through BOTH the agent factory and the stream selector; `planner`/`coder` resolve to claude-code/`opus-4.8`) | [x] | done |
| 19 | Subscription-env security across all three paths (§3+§7+§10 — no `OPENAI_API_KEY`, `HOME` preserved, `RevealKey` never called on batch, stream, AND readiness; prompt off argv everywhere) | [ ] | pending |
| 20 | Composition root + acceptance (§12 wiring + `test/acceptance/scenario_03_*` — `Deps.Codex*`, codex stream adapter, backend→stream map, default floor all wired; the both-agent-backends scenario exercises a third backend through the fake runner; `go build ./...` green) | [ ] | pending |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage (happy / success / failure / error / edge), with the subprocess seam faked.
- The code does what the spec and phase design doc say it should.
- Error handling is thorough — no silent failures; fail-closed where security-relevant (empty input ⇒ zero launches; failure events ⇒ typed error, not silent Done).
- Module boundaries are clean (hexagonal: `adapter/codex` reuses the `claudecli.CommandRunner` seam, never redefines it; `domain/config` never imports `adapter/codex`).

**Pass 2 (Robustness & Extensibility):**
- Code aligns to the spirit of the design, not just surface conformance.
- The subscription-only credential invariant is airtight on the path under review.
- CLI coupling is pinned in one place and guarded by a test, so a codex-version bump is a one-line, test-caught change.
- The five-category coverage is meaningful (asserts behavior, not just "no error").

### Paired Area Reviews — What To Check

- The two (or three) areas agree on their shared contract (one argv/env vocabulary; one event decoder; one backend-dispatch vocabulary; one readiness state flowing to the health surface).
- The end-to-end slice behaves correctly: a codex profile drives both node types; a run-now/interactive codex mission carries the subscription env with no key; the sample mixed-model split resolves and validates.
