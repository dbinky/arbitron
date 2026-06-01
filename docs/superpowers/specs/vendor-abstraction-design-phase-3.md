# Vendor-Abstraction MVP — Design Phase 3: Native Chat Adapters & opencode Agent Backend

**Date:** 2026-06-01 · **Status:** Design approved; pending implementation plan
**Spec:** `docs/specs/feature-pipeline-replacement-mvp.md`
**Depends on:** Phase 1 (two ports + capability validation), Phase 2 (connections + resolution + credential reveal), Phase 1 §5 opencode spike finding.

## Overview

Phase 3 makes the abstraction multi-vendor. It adds two **native `ChatProvider`** implementations — Azure AI Foundry and OpenRouter (native HTTP: streaming, structured output, token usage) — and one **`AgentProvider`** implementation — **opencode** (`run`/`serve`), which routes an agent mission to any provider/model. opencode receives the connection's key and base-url **injected as environment variables into the subprocess at launch**, via a new injection path distinct from the secret-stripping `procenv` base allowlist. With a tool-less chat backend now real, `llm_call`'s tool-suppression becomes **load-bearing**, and the capability validation built in Phase 1 is proven end-to-end.

Realizes acceptance **#2** (all three chat providers), **#3** (both agent backends), and the delivery half of **#7** (env-injected credential into opencode, key absent from files and logs).

---

## 1. Native Chat Adapters — `adapter/azurefoundry`, `adapter/openrouter`

Both implement `engine.ChatProvider` (`Chat` + `StreamChat`) and consume a Phase-2 `ResolvedBinding` (base-url, extras, model, lazy `RevealKey`).

### `adapter/openrouter`

- POSTs to the OpenRouter Chat Completions endpoint (`{base-url}/chat/completions`), `Authorization: Bearer <revealed key>`.
- `Chat` → full response; `StreamChat` → SSE token stream mapped to `ChatChunk`. Structured output via the request's `OutputSchema` (response-format/JSON schema). Token usage from the response `usage` block.
- A **LiteLLM** router is *not* a separate adapter — it is just a connection whose `base-url` points at the router, reached through the OpenRouter-style OpenAI-compatible path (spec §6).

### `adapter/azurefoundry`

Handles both Azure topologies (spec §6) with **no special-casing** — the difference is entirely in the connection rows:

- **Azure OpenAI resource (GPT-family):** one resource endpoint + key shared across many **deployments**. One connection, many profiles; each profile's `Model` is the **deployment name**. The required `api-version` comes from the connection `Extras`. Request path: `{base-url}/openai/deployments/{deployment}/chat/completions?api-version=...`, `api-key: <revealed key>`.
- **Azure AI Foundry serverless / open-weight catalog (Llama, Mistral, …):** each serverless deployment has its **own** endpoint + key → one connection per model. Same adapter, different connection row.

Streaming, structured output, and token accounting are native, matching the OpenRouter adapter's contract.

### Resolver wiring

The Phase-2 resolver maps `Connection.Provider` → the concrete chat adapter: `openrouter`/`litellm` → `adapter/openrouter`; `azure-openai`/`azure-foundry` → `adapter/azurefoundry`; (empty connection) → claude-cli `ChatAdapter` from Phase 1. The `llm_call` executor is unchanged — it receives whichever `ChatProvider` the alias resolves to.

---

## 2. opencode `AgentProvider` — `adapter/opencode`

Implements `engine.AgentProvider` (`RunAgent`). Agentic editing on **non-Claude** models is reached **through opencode**, never through a native API-level tool-use loop (spec §4) — Arbitron does not reimplement a coding agent.

- **Invocation:** `opencode run --model <provider/model> "<mission prompt>"` with `cmd.Dir = mission.WorkDir` (the instance `{{workspace}}`). The `<provider/model>` string comes from the resolved binding's `Model`.
- **Session shape:** determined by the **Phase 1 §5 spike finding**. If headless resume proved reliable, support cross-step `SessionID` via `opencode serve`/session id; if not, the contract is **single-shot mission per step** (one `opencode run` per agent node, no `SessionID` threading). Default to single-shot unless the spike cleared resume.
- **Step events / result:** parse opencode's output into `AgentResult{Summary, Steps, Tokens, SessionID}`. Token accounting is coarse (spec §9).
- **Reuses the shared `CommandRunner`** seam for unit-testability with a fake.

### Credential env-injection (new path)

The Phase-1 `procenv.BaseEnv()` allowlist deliberately **strips** secrets. opencode needs the connection's key. So this phase adds a **scoped injection** path — analogous to `adapter/process/env.go`'s per-node env, but sourcing the value from the resolved connection rather than the engine env:

- At launch, append `PROVIDER_API_KEY=<revealed key>` (and the provider-appropriate base-url var, e.g. `OPENAI_BASE_URL`/provider-specific) to the subprocess env, **on top of** `procenv.BaseEnv()`.
- The revealed key comes from `ResolvedBinding.RevealKey(ctx)` at launch time only; it is never logged and never written to a file. The injected vars exist solely in the child process environment.
- Exact var names are provider-keyed (opencode's documented env contract per provider); encapsulated in a small `opencodeEnv(binding)` helper with table tests.

This is the mechanism behind acceptance #7's "injected into the `opencode` subprocess for a non-Claude agent/chat model."

---

## 3. Capability Validation Becomes Load-Bearing

With Azure/OpenRouter chat adapters real, a chat-only backend genuinely **cannot** edit files. The Phase-1 rule — `agent_call` requires an agent profile; binding it to a chat profile fails `workflow_validate` — now prevents a concrete defect (a tool-less model narrating *"I would edit the file"*). No new validation code; this phase is where the existing rule earns its place. An `agent_call` whose profile resolves to a native chat adapter is impossible by construction, because such a profile is `kind: chat` and fails validation.

---

## 4. Testing Strategy (strict TDD, 5 scenario categories)

- **`adapter/openrouter`** (HTTP fake / httptest): `Chat` happy path; `StreamChat` SSE chunking; structured output; `usage` token parse; non-2xx error; malformed/empty body; auth header carries the revealed key; key absent from any log.
- **`adapter/azurefoundry`**: deployment-path construction (resource topology) vs. serverless endpoint; `api-version` from extras; `api-key` header; both topologies from different connection rows; streaming + structured output + usage; error/edge cases as above.
- **`adapter/opencode`** (`CommandRunner` fake): single-shot `run` happy path → `AgentResult`; `--model provider/model` arg threading; `WorkDir` set; resume arg only when the spike-enabled path is on; non-zero exit; binary-not-found; timeout/cancel; malformed output.
- **opencode env-injection** — correct provider-keyed vars appended; base allowlist preserved; revealed key present in child env but **absent from logs** (log-capture assertion); base-url var set.
- **Resolver provider→adapter mapping** — each provider string selects the right adapter; empty connection → claude-cli; unknown provider error.

## 5. Sequencing (each built test-first)

1. `adapter/openrouter` chat adapter.
2. `adapter/azurefoundry` chat adapter (both topologies).
3. opencode env-injection helper.
4. `adapter/opencode` `AgentProvider` (shape per spike finding).
5. Resolver provider→adapter wiring in `main.go`; end-to-end smoke per configuration.

## 6. Acceptance Mapping

- **#2 All three chat providers functional** — switching a node's profile from Claude to Azure/OpenRouter is a single profile-reference change and the node runs.
- **#3 Both agent backends functional** — `agent_call` runs via `claude -p` (P1) and via `opencode` on a non-Claude/open-weight model, proving the opencode path + env-injected credential.
- **#7 (delivery half)** — connection key+base-url injected into the opencode subprocess; key in neither workflow files nor logs.
