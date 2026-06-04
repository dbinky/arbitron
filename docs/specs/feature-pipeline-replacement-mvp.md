# Feature-Pipeline Replacement — Vendor-Abstraction MVP

**Status:** Draft · **Date:** 2026-06-01 · **Type:** Platform-enabling design spec (no implementation)

## 1. Purpose & Outcome

This spec defines the **platform-enabling layer** that lets Arbitron run the feature-pipeline workflow **fully abstracted from any single LLM vendor**, while still permitting `claude -p` (a Claude subscription) as a backend for both reasoning and agentic code editing. It is the infrastructure that must exist before the feature-pipeline workflow itself can be authored as a native Arbitron workflow.

The desired outcomes:

- **Vendor abstraction.** A node's *intent* (reason vs. edit code) is decoupled from its *backend* (which model/harness fulfills it). Workflows never name a vendor.
- **Per-node model choice.** Every node may run a different model on a different provider, selected by reference, swappable in one place.
- **Local testability on a Claude subscription.** The motivating success criterion: run the entire pipeline locally, end-to-end, on a Claude subscription with **no API keys** — then switch individual nodes to Azure AI Foundry or OpenRouter by changing a single profile reference.
- **Native refinement loop.** The external ralph-o-matic server is replaced by an in-engine loop, so Arbitron itself executes the ralph refinement cycle.

The **native feature-pipeline workflow** (its node-by-node content: brainstorm → plan → ralph loop → review) is **not** specified here. It is the named *acceptance test* for this layer and will be authored in a separate spec. See the project overview's stated goal that "the entire feature pipeline … should be expressible as a single Arbitron workflow" (`docs/specs/arbitron-project-overview.md`).

That workflow content is expected to ship in **two configurations**, and this layer must support both interchangeably (same graph, different profiles):

- A **Claude-subscription configuration** — `claude -p` chat and agent nodes for real work on local projects.
- An **open-weight configuration** — non-Anthropic / open-weight models (served via OpenRouter or Azure AI Foundry) doing the same work on a more limited basis, for lower-cost end-to-end testing of the full pipeline against non-Anthropic models.

The two configurations differ only in which profiles their nodes reference (§5) — never in node kinds, edges, or templates.

## 2. Core Principle: Intent vs. Backend

The design keeps two axes strictly orthogonal:

- **Node kind = intent.** What capability the step requires.
- **Provider = backend.** Which model/harness fulfills that capability.

The backend is chosen by a **model profile** referenced from the node, never hard-coded into the node or the workflow graph. Harness choice (`claude -p` vs. `opencode`) and vendor (Anthropic / Azure AI Foundry / OpenRouter) live entirely in the provider layer. Consequence: **swapping a backend never changes a workflow.**

A corollary the design enforces: there are **no vendor-specific or harness-specific node types** (no `claudecode_call`, no `opencode_call`). One node kind per *intent*; many interchangeable backends behind it.

## 3. Node Kinds

The execution model adds one new node kind and clarifies the contract of an existing one. Existing `tool_call`, `transform`, `conditional`, and `interactive` nodes are unchanged.

- **`llm_call` — pure reasoning (no side effects).** Sends a prompt, receives text or structured output. Must not mutate files. When backed by `claude -p`, it runs in a **tool-suppressed** mode so it behaves as a plain completion.
- **`agent_call` — agentic worker (new).** A first-class node kind for tool-using work that mutates files in the instance workspace. It declares a **capability contract**: it requires an *agent-capable* profile. Binding an `agent_call` to a chat-only profile is a **configuration error caught at validation time**, before any run — preventing the silent failure mode where a tool-less model merely narrates *"I would edit the file"* and the workflow proceeds as if it had. Agent-specific configuration (working directory = the instance `{{workspace}}`, tool-permission scope, extended timeout) lives on this node kind, not on `llm_call`.

Why a distinct kind rather than reusing `llm_call`: under the current local Claude-CLI setup every `llm_call` is *silently* agentic (the `claude` binary always has tools). That is a leaky abstraction — the same node behaves with or without side effects depending only on which adapter is wired. The moment a tool-less chat adapter (Azure/OpenRouter) is wired, that ambiguity becomes a real defect. `agent_call` makes the capability requirement explicit, declared in the graph, and validatable.

## 4. Two Provider Ports

The provider layer exposes **two distinct interfaces**, separated by capability.

- **`ChatProvider` — reasoning/chat.** Accepts a prompt (or conversation) with a model selection, parameters, and an optional output schema; returns text or structured output plus token usage. Offers a streaming variant for `interactive` nodes. Performs **no tool use and no file side effects.**
  - Implementations (all required for MVP): **tool-suppressed `claude -p`** (chat via the subscription), **native Azure AI Foundry**, **native OpenRouter**.
- **`AgentProvider` — agentic editing.** Accepts a mission (prompt + working directory + model); produces file mutations within the workspace, a result/summary, and step events. The agentic loop (tool calls, edits, test runs) happens **inside the harness**, not inside Arbitron.
  - Implementations (**both required for MVP**): **`claude -p`** with tools enabled (for the Claude-subscription configuration); **`opencode`** (`run` / `serve`), which routes the mission to any provider/model (for the open-weight configuration and any non-Claude agent work). Agentic editing on **non-Claude** models (Azure / OpenRouter / open-weight) is reached **through opencode**, not through native HTTP adapters — Arbitron does not build its own API-level tool-use loop.

Rationale for two ports rather than one unified provider: a single interface would force chat-only adapters to no-op the tool methods and would blur the capability validation that `agent_call` depends on. The two-port split gives a clean capability contract, native control over chat (structured output, streaming, token accounting), and confines harness usage to where agency is genuinely required.

The `interactive` node's executor is already provider-agnostic (it consumes the streaming interface and tracks a session). Its eventual backend is a Messages-API chat adapter or a Chat Completions API (dpending on model provider) that resumes via a conversation array rather than a harness session id; that adapter is **out of scope** here and noted as future work (§9).

## 5. Models, Connections & Profiles

Three orthogonal concepts are kept separate, because real provider topologies force them apart:

- **Connection (endpoint)** — *where to reach a backend and how to authenticate*. An API key is endpoint-scoped, so the key and URL are bound together here; `model-id` is **not** part of a connection. Defined in §6.
- **Model** — a `model-id` (or, for Azure, a deployment name) selected *on top of* a connection. Keeping it off the connection lets one connection serve many models, and lets the same logical model be sourced from several connections.
- **Model profile** — the single unit a node references:

```
profile = { alias, kind: chat | agent, connection-ref, model-id, default params }
```

Nodes reference a profile by **alias** (e.g. `model_profile: fast-reasoner`). Resolution maps the alias to its connection (which determines the provider and thus the concrete adapter, or harness + `provider/model` string for agent profiles), applies `model-id` on that connection, and injects the connection's credential. Properties:

- **Per-node model freedom.** Any node names any profile; this is the mechanism for "every node a different model."
- **One-line backend swap.** Repointing a profile — or repointing its connection — changes the backend for every node that uses it, without touching the workflow graph.
- **Connections are many-to-one with models, by provider topology.** A shared-key endpoint (OpenRouter, an Azure OpenAI resource) is *one* connection serving *many* profiles; a per-model endpoint (an Azure AI Foundry serverless deployment) is *one* connection serving *one* profile. The same `model-id` reached from two providers is simply two profiles over two connections.
- **Kind-matching is validated.** A node's intent must match its profile's `kind`: an `agent_call` requires an `agent` profile, an `llm_call` requires a `chat` profile. Mismatches fail validation.

## 6. Connection Store

Connections are owned by Arbitron in a **single local store** (API keys encrypted at rest), referenced by profiles via `connection-ref` (§5). A connection binds an endpoint to its credential:

```
connection = { name, provider, base-url, region/extras, api-key }
```

Because an API key authenticates against a specific endpoint, the key and URL are bound together on the connection. `model-id` is deliberately excluded so a single connection can front many models. A **local router** (e.g. LiteLLM) is just another connection whose `base-url` points at the router. Delivery rules differ by backend:

- **Subscription `claude -p` needs no connection** — it inherits the ambient Claude login. A Claude-only local run requires zero stored keys or endpoints.
- **Native chat adapters** (Azure AI Foundry, OpenRouter, LiteLLM) read their connection's base-url, key, and extras directly.
- **Subprocess harnesses** (`claude`, `opencode`) receive the connection's key and base-url **injected as environment variables into that process at launch** — this is how `opencode run --model <provider/model>` reaches the right endpoint with the right key.

**Azure's two topologies both map onto connections with no special-casing:**

- **Azure OpenAI resource** (GPT-family): one resource-level endpoint + key shared across many model **deployments** → **one connection, many profiles**, each profile's `model-id` set to its **deployment name** (the deployment name, not the canonical model name). The required `api-version` lives in the connection's `region/extras`.
- **Azure AI Foundry serverless / open-weight catalog** (Llama, Mistral, etc.): each serverless deployment has its **own** endpoint + key → **one connection per model**. This is the same shape used for the open-weight test configuration (§1).

Security requirements: keys never appear in `workflow.json`; keys are never logged; each key is scoped to the connection that owns it. The store is the one source of truth for endpoints and credentials across both native adapters and delegated harnesses.

## 7. Loop Handling — The Native Ralph Loop

Arbitron's engine already permits cyclic graphs (no acyclicity check; `transform` and `conditional` re-execute on revisit; no unique `(instance, node)` constraint). The MVP relies on this directly.

**Supported idiom (cyclic-edge pattern):**

1. A **back-edge** from a downstream node to an earlier node forms the loop.
2. A **`transform`** node increments an iteration counter.
3. A **`conditional`** node evaluates *(done? OR counter ≥ max)* and routes either back into the loop or out to the exit.

**Constraints:**

- **`interactive` nodes must not appear in a loop body.** The interactive executor short-circuits on a prior succeeded `(instance, node)` execution and would return its first answer on every subsequent pass. Loop bodies use only `agent_call` / `llm_call` / `transform` / `conditional` / `tool_call`.
- **Termination is entirely the author's `conditional`.** Per the MVP decision, the engine adds **no** runaway guard. A malformed loop can run indefinitely; this is an accepted, documented risk (§9).

**The native ralph loop** is then expressible as: `agent_call` (edit toward the focus areas) → `llm_call` (assess: run/inspect tests and remaining gaps) → `transform` (increment counter) → `conditional` (loop or exit). The `RALPH.md`, `focus-areas`, and `gaps` documents are ordinary files in the instance `{{workspace}}` that the agent reads and rewrites each pass. This removes the dependency on the external ralph-o-matic server.

## 8. Acceptance Criteria

The MVP is complete when all of the following hold:

1. **Local Claude run.** The feature-pipeline-replacement workflow runs end-to-end locally on a Claude subscription with **no API keys** — `llm_call` via tool-suppressed `claude -p`, `agent_call` via `claude -p` with tools.
2. **All three chat providers functional.** Switching a node from a Claude profile to an Azure AI Foundry or OpenRouter profile is a single profile-reference change and the node runs correctly. Claude, Azure AI Foundry, and OpenRouter all work as chat backends in the MVP.
3. **Both agent backends functional.** An `agent_call` executes end-to-end via **`claude -p`** (Claude configuration) *and* via **`opencode`** on a non-Claude / open-weight model (open-weight configuration), proving the opencode `AgentProvider` path and its env-injected credential.
4. **Configuration interchangeability.** The same workflow graph runs under both the Claude-subscription and open-weight configurations by changing only profile references — no edits to node kinds, edges, or templates.
5. **Native loop.** A workflow loop iterates multiple times and exits on its `conditional`, with the iteration counter persisted in instance variables and visible in the trace.
6. **Capability validation.** An `agent_call` bound to a chat-only profile fails `workflow_validate` with a clear error, before any execution.
7. **Connection safety & delivery.** A connection's base-url and key are resolvable from a profile, injected into the `opencode` subprocess for a non-Claude agent/chat model, and the key appears in **neither** the workflow files **nor** any log output. A multi-region case (two Azure connections, different endpoints/keys) resolves each node to the correct endpoint.

## 9. Non-Goals, Known Risks & Future Work

**Non-goals (explicitly out of scope for this MVP):**

- **Fan-out / fan-in (parallelism).** Deferred to a future release (formerly Phase 8) per the project overview. The native loop iterates a dynamic task list one-at-a-time; this is slower, not blocked.
- **A dedicated loop/iteration primitive.** The cyclic-edge pattern is the supported idiom; a first-class loop construct is not built.
- **The feature-pipeline workflow content.** Authored separately; this spec delivers only the enabling layer.
- **A native API-level tool-use agent loop.** Agency is delegated to harnesses (`claude -p`, `opencode`); Arbitron does not reimplement a coding agent.
- **Multi-user / auth changes.** Unaffected by this spec.

**Known risks & future work:**

- **No engine runaway guard (accepted MVP risk).** Loop termination depends solely on the author's `conditional`. Planned future backstop: an engine-level cap on max node-executions and/or wall-clock per instance.
- **OpenCode headless session-resume rough edges** (upstream issues #2404, #13851). Now on the **MVP critical path**, since opencode is a required agent backend (§4, §8-3). Affects only `agent_call`; chat backends are unaffected. Must be de-risked early with a small unattended spike (`opencode run` editing files headlessly in a `{{workspace}}`); if headless resume proves unreliable, the fallback is single-shot `opencode run` per agent step without cross-step session resume.
- **Coarser cost accounting for `agent_call`.** Token/cost visibility for a subprocess harness is less precise than for native chat calls; acceptable for MVP.
- **`interactive` node future backend.** Long-term, `interactive` should run on a Messages-API chat adapter that resumes via a conversation array (reintroducing the messages path removed in the PR #8 cleanup) rather than a harness session id. Not required for this MVP.

**Rejected alternatives:**

- **Single unified `ModelProvider` port.** Forces chat adapters to no-op tool methods and muddies capability validation. Rejected in favor of two ports.
- **Harness-centric (no native remote chat adapters).** Route all chat through `claude -p` / `opencode` and reach remote models via opencode's provider routing. Minimizes Arbitron code but gives up native streaming/structured-output control for chat and contradicts the decision to ship native Azure/OpenRouter chat adapters. Rejected.
