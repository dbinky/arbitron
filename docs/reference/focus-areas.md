# Focus Areas Review Tracking

This document tracks the systematic review of the **Vendor-Abstraction MVP** (`docs/specs/feature-pipeline-replacement-mvp.md`) for correctness, security, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | Provider ports (`domain/engine/ports.go` — `ChatProvider`/`AgentProvider`, `Chat*`/`Agent*` types, `ChatProviderSelector`/`AgentProviderSelector`) | [x] | [x] | Done |
| 2 | claude-cli adapters (`adapter/claudecli/` — `ChatAdapter` tool-suppression, `AgentAdapter` tools, `argsPolicy`/`buildArgs`, shared `CommandRunner`, stream-json parsing) | [x] | [x] | Done |
| 3 | Profile registry & resolver (`domain/provider/provider.go`, `adapter/provider/static.go` — `Profile`, `ProfileKind`, `ProfileResolver`, `ResolvedBinding`, alias resolution) | [x] | [x] | Done |
| 4 | LLM & agent executors (`domain/engine/executor_llm.go`, `executor_agent.go` — profile resolve, provider-agnostic structured output, workspace WorkDir) | [x] | [x] | Done |
| 5 | Capability validation & loop lint (`domain/workflow/validation.go`, `lint.go` — kind-matching, connection-existence, `INTERACTIVE_IN_LOOP` advisory) | [x] | [x] | Done |
| 6 | Crypto (`domain/provider/crypto.go`, `adapter/crypto/` — AES-256-GCM, nonce-per-seal, master-key bootstrap, fail-fast gate) | [x] | [x] | Done |
| 7 | Connection store (`domain/provider/connection.go`, `adapter/sqlite/connection_repo.go`, `migrations/002_connections.*`, `queries/connections.sql` — CRUD, Reveal-only decrypt, Azure api-version rule) | [x] | [x] | Done |
| 8 | OpenRouter chat adapter (`adapter/openrouter/` — chat/completions, Bearer auth, SSE streaming, structured output, usage) | [x] | [x] | Done |
| 9 | Azure Foundry chat adapter (`adapter/azurefoundry/` — both topologies, deployment path + api-version, api-key header, serverless endpoint) | [x] | [x] | Done |
| 10 | opencode agent backend (`adapter/opencode/` — single-shot `RunAgent`, `opencodeEnv` credential injection, `buildArgs` isolation) | [x] | [x] | Done |
| 11 | Provider selection factory (`adapter/provider/factory.go`, `conn_checker.go` — provider→adapter mapping, empty→claude-cli) | [x] | [x] | Done |
| 12 | Conditional & transform executors (`domain/engine/executor_conditional.go`, `executor_transform.go` — value template-resolution, numeric compare, counter increment) | [x] | [ ] | |
| 13 | Connection management surface (`adapter/mcp/tools_connection.go` — create/update/delete/get/list, key-in-never-out, name-only logging) | [ ] | [ ] | |
| 14 | Composition root wiring (`cmd/arbitron/main.go` — adapters, resolver, selectors, checkers, fail-fast gate) | [ ] | [ ] | |
| 15 | Config & profile loading (`domain/config/config.go`, `adapter/config/loader.go`, `profiles.go` — Profiles block, ConnectionRef/Params, precedence) | [ ] | [ ] | |
| 16 | Native ralph loop example (`examples/workflows/native-ralph-loop/` — graph, templates, seed files, both bindings, termination on both exit branches) | [ ] | [ ] | |
| 17 | Acceptance harness (`test/acceptance/` — #1–#7 scenarios, hermetic seams, config-diff & secrecy assertions) | [ ] | [ ] | |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 18 | Provider ports + selection factory (port contract alignment; every provider string maps to an adapter; unknown→clear error) | [ ] | |
| 19 | Profile resolver + connection store (alias→profile→connection→binding; lazy `RevealKey` decrypts only at use; empty ConnectionRef→claude-cli no-key) | [ ] | |
| 20 | Capability validation + profile registry (kind-matching end-to-end; `agent_call`→chat fails before any run; unknown alias/connection errors) | [ ] | |
| 21 | Crypto + connection store (seal-on-write/Reveal-on-read; tampered/wrong-key → `ErrDecrypt`; no plaintext in any column) | [ ] | |
| 22 | opencode env-injection + connection resolution (credential delivered into the subprocess env; absent from logs and `workflow.json`) | [ ] | |
| 23 | Conditional executor + worked example (done-exit and `counter >= max` exit both genuinely reachable; loop terminates under the Claude binding) | [ ] | |
| 24 | Chat adapters (claudecli/openrouter/azurefoundry) + LLM executor (structured output parsed to a map uniformly regardless of backend; no double-parse) | [ ] | |
| 25 | Acceptance harness + all backends (scenarios exercise real selection/adapter/credential seams, not just unit happy paths) | [ ] | |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage across the 5 categories (happy, success, failure, error, edge)
- The code does what the design doc says, and satisfies the relevant acceptance criterion (#1–#7)
- Error handling is thorough — no silent failures (especially the `agent_call`-narrates-but-doesn't-edit failure mode)
- Module boundaries are clean (`domain/` imports no adapter; ports stay in `domain/`)

**Pass 2 (Robustness & Security):**
- Code aligns to the spirit of the spec (intent vs. backend orthogonality), not surface conformance
- Credential safety holds: keys never serialized to `workflow.json`, never logged, encrypted at rest
- The component is ready to be extended (e.g. a future provider behind the same ports)
- Edge cases handled (empty inputs, missing `{{workspace}}`, unresolved templates, malformed provider output)

### Paired Area Reviews — What To Check

Paired reviews verify two components integrate correctly:
- Data flows cleanly across the boundary (binding fields, resolved credentials, parsed output)
- Error propagation works end-to-end (resolve error → executor → instance failure)
- No assumptions in one component the other doesn't satisfy (e.g. structured output shape)
- Integration/acceptance tests cover the seams between the pair, not just unit happy paths
