# Focus Areas Review Tracking

This document tracks the systematic review of the **Multi-Provider LLM Adapters** feature (`docs/specs/llm-providers-spec.md`) for correctness, credential secrecy, fail-closed behavior, and robustness.

Each **single focus area** requires **2 full review passes** before being considered done.
Each **paired focus area** requires **1 full review pass** before being considered done.

A review pass is only checked when the checklist (in `RALPH.md`) produces a passing result for that area.

---

## Table 1: Single Area Reviews (2 passes each)

| # | Focus Area | Pass 1 | Pass 2 | Status |
|---|---|---|---|---|
| 1 | awssig SigV4 signer (`adapter/awssig/sigv4.go` — canonical request, string-to-sign, signing-key, injectable clock, golden signature, X-Amz-Security-Token) | [x] | [x] | Done |
| 2 | awssig sealed credentials (`adapter/awssig/credentials.go` — ParseCredential bare/JSON-composite, BuildCredential round-trip, ErrMissingSecret, no-leak) | [x] | [x] | Done |
| 3 | anthropicapi chat adapter (`adapter/anthropicapi/{provider,request,response}.go` — /v1/messages, x-api-key fail-closed, max_tokens default, forced-tool structured output, SSE StreamChat) | [x] | [x] | Done |
| 4 | bedrock chat adapter (`adapter/bedrock/{provider,endpoint,request,response}.go` — Converse, regional endpoint derivation, signing via awssig, streaming, guards before reveal) | [x] | [x] | Done |
| 5 | Factory chat routing (`adapter/provider/factory.go` — openai/ollama/anthropic-api/bedrock arms, provider normalization, ErrUnknownProvider, Deps wiring) | [x] | [x] | Done |
| 6 | opencode args + provider-id + posture (`adapter/opencode/{args,provider_id,posture}.go` — run -m contract, model de-prefix, ToolScope/AllowedTools precedence, open-by-default) | [x] | [x] | Done |
| 7 | opencode env + run + version probe (`adapter/opencode/{env,env_providers,version,provider}.go` — auth-env table, bedrock AWS quartet via awssig, keyless allowlist, no-partial-env, fail-closed) | [x] | [x] | Done |
| 8 | probe adapters (`adapter/probe/*.go` — OpenAI-compat /models, ollama /api/tags fallback, anthropic /v1/models, bedrock ListFoundationModels, claude-cli reachability, error classification) | [x] | [x] | Done |
| 9 | ConnectionProbe domain port (`domain/provider/probe.go` — ConnectionProbe, ProbeResult, ProbeSelector, ErrNoProbeForProvider; dispatch on Provider only) | [x] | [x] | Done |
| 10 | connection validate CLI (`cmd/arbitron/connection.go` — runConnectionValidate core, connection-name binding via Get+Reveal, output shapes, exit codes, no-secret-print) | [x] | [x] | Done |
| 11 | url_policy ollama loopback + bedrock ValidateConnection (`domain/provider/{url_policy,connection}.go` — provider-scoped loopback carve-out, bedrock region/access-key rule) | [x] | [x] | Done |
| 12 | Config + composition root (`domain/config/config.go`, `adapter/config/loader.go`, `cmd/arbitron/main.go` — AgentAllowAllTools default-true, YAML doc, Factory Deps wiring, connection cmd registration) | [x] | [x] | Done |
| 13 | Credential secrecy (cross-cutting — key/secret/session-token never in logs/errors/argv across all adapters + CLI; scrubControl + bounded readers + no-leak tests) | [x] | [x] | Done |
| 14 | Open-by-default posture & fail-closed (cross-cutting — skip-permissions + broad tools default, operator restriction narrows, unsupported provider fails closed, kind guard intact) | [x] | [x] | Done |

---

## Table 2: Paired Area Reviews (1 pass each)

| # | Pair | Pass | Status |
|---|---|---|---|
| 15 | awssig signer + bedrock adapter (signature applied to Converse request; region/service correct; security-token header) | [x] | Done |
| 16 | awssig credentials + opencode env (sealed composite → AWS_ACCESS_KEY_ID/SECRET/SESSION/REGION quartet; bare-string path) | [x] | Done |
| 17 | awssig signer + bedrock probe (ListFoundationModels signed on control-plane host; injected clock; endpoint seam) | [x] | Done |
| 18 | Factory routing + chat adapters (ResolvedBinding → correct concrete provider per string; Deps threaded; unknown → error) | [x] | Done |
| 19 | opencode posture + config knob (AgentAllowAllTools → Deps.OpencodeAllowAllTools → Config.AllowAllTools → emitted argv; AllowedTools overrides) | [x] | Done |
| 20 | url_policy ollama allowance + Factory ollama routing (a localhost ollama connection is both creatable and routable end-to-end; non-ollama localhost still rejected) | [x] | Done |
| 21 | anthropicapi adapter + anthropic probe (anthropic-version pinned in lockstep; x-api-key header parity) | [x] | Done |
| 22 | ConnectionProbe port + validate CLI (ProbeSelector dispatch from a connection-name binding; result/error rendering; exit codes) | [x] | Done |
| 23 | Credential secrecy + mixed-provider acceptance (`test/acceptance/scenario_08` — keyed secrets never logged across the ollama→anthropic→openai run) | [x] | Done |
| 24 | Config wiring + composition root (cfg.LLM → Factory Deps → all adapters; connection cmd built from the same stack as serve) | [x] | Done |

---

## Review Guidance

### Single Area Reviews — What To Check

**Pass 1 (Correctness & Coverage):**
- All public functions have test coverage across the five categories (happy / success / failure / error / edge).
- The code does what the design doc says it should (per-phase design in `docs/superpowers/specs/`).
- Error handling is thorough — no silent failures; configuration faults vs transient faults are distinguished.
- Module boundaries are clean (no leaking types across boundaries; `adapter/awssig` stays a stdlib-only leaf).

**Pass 2 (Robustness & Security):**
- Code aligns to the spirit of the design, not just surface conformance.
- Credential secrecy holds on every error path; bounded readers cap hostile bodies; control chars are scrubbed.
- Open-by-default posture and fail-closed behavior are correct; operator restriction actually narrows.
- Edge cases handled (empty model, missing region/key, malformed JSON/SSE, trailing slashes, session-token present/absent, keyless paths).

### Paired Area Reviews — What To Check

- Data (a `ResolvedBinding`, a sealed credential, a config value) flows cleanly across the boundary.
- Error propagation works end-to-end and never leaks a secret.
- No assumptions in one component that the other doesn't satisfy (provider-string vocabulary, anthropic-version lockstep, signer service/region).
- Integration tests cover the seam, not just unit-level happy paths.
