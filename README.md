# Arbitron

Arbitron is a **workflow orchestration platform** for composing and running directed graphs of LLM calls, agentic coding work, scripts, and tools. It generalizes the pattern established by ralph-o-matic — iterative, structured-output-driven LLM loops — into a configurable system where any sequence of automated and interactive steps can be composed, executed, monitored, and shared.

Arbitron's goal is to **replace both ralph-o-matic (the refinement engine) and the dbinky-skill-set feature pipeline**: the entire feature pipeline — brainstorm → plan → implementation → refinement loop → PR review — is expressible as a single Arbitron workflow, runnable across any LLM vendor.

> **Scope of this document.** This README describes the **target MVP end-state**: the capabilities implemented today plus the vendor-abstraction layer defined in `docs/specs/feature-pipeline-replacement-mvp.md`, written in present tense as the end-state to convey what "done" looks like. The vendor-abstraction layer (the `ChatProvider`/`AgentProvider` split, `agent_call`, model profiles, the credential store, and native loops) is the in-flight MVP work; everything else (engine, queue, MCP server, dashboard, interactive nodes) is built.

---

## What Arbitron Is

A self-hostable Go service that:

- Stores **workflows** as self-contained folders (a JSON manifest plus its template/script files), authored via MCP tools from Claude Code — not through a UI.
- Executes workflows as **instances** with their own variable state, execution trace, and lifecycle.
- Runs each node on a **per-node-selectable model** from any supported vendor (Anthropic via Claude subscription, Azure AI Foundry, OpenRouter, and open-weight models), so the *same* workflow graph runs unchanged on different backends.
- Exposes everything over an **MCP server**, a **REST API**, and a **web dashboard** with live execution streaming.

---

## How It Works — Core Concepts

- **Workflow** — a reusable definition stored as a folder: one JSON manifest describing a directed graph of **nodes** and **edges**, plus accompanying files (markdown templates, scripts).
- **Workflow Instance** — a single execution. Has its own variable state, execution trace, and lifecycle (queued, running, waiting, completed, failed, cancelled, paused). An instance snapshots the workflow definition at creation time — later edits don't affect running instances.
- **Node** — a step in the graph. Each node declares an **intent** (its kind) and references a **model profile** for its backend. See [Node Kinds](#node-kinds).
- **Edge** — a simple, unconditional wire defining graph topology. Edges carry no logic; all routing happens inside nodes (specifically `conditional` nodes). Edges may form **cycles** (see [Loops](#loops--the-native-refinement-cycle)).
- **Instance Variables** — a key-value store living for the instance's duration, initialized from the workflow's input schema. Nodes read variables via `{{substitution}}` in templates and write variables via their structured output. A reserved `{{workspace}}` variable provides a per-instance working directory.
- **Structured Output** — every execution node emits JSON on completion, stored in instance state and available to downstream nodes (conditionals evaluate it; templates substitute it).

---

## Node Kinds

The execution model separates a node's **intent** (its kind) from its **backend** (which model/harness fulfills it). The backend is chosen by a referenced model profile, never hard-coded into the node. There are **no vendor- or harness-specific node kinds** — one kind per intent, many interchangeable backends behind it.

**Execution nodes** (do work, produce output):

| Kind | Purpose |
|---|---|
| `llm_call` | **Pure reasoning, no side effects.** Sends a templated prompt, returns text or structured output. When backed by `claude -p`, runs tool-suppressed so it behaves as a plain completion. |
| `agent_call` | **Agentic worker that edits files** in the instance `{{workspace}}`. Declares a capability contract requiring an *agent-capable* profile; binding it to a chat-only profile fails validation. The agentic loop runs inside the harness (`claude -p` with tools, or `opencode`). |
| `script` | Executes a script (shell, Python, JS, etc.) and collects structured output from stdout. |
| `tool_call` | Invokes a CLI tool or API and collects structured output. |
| `interactive` | An LLM call requiring human participation via an embedded conversational interface. The workflow pauses (state `waiting`), notifies the user, and resumes when the user replies. Multi-turn, with conversation history and a completion sentinel. |

**Flow-control nodes** (route and shape execution):

| Kind | Purpose |
|---|---|
| `conditional` | Evaluates conditions against instance state and routes to one of several named outputs (with a default). The only place branching logic lives. |
| `transform` | Lightweight variable manipulation (set, increment, concatenate) with no external execution. |

**Wait nodes** (pause execution without consuming a concurrency slot):

| Kind | Purpose |
|---|---|
| `timer_delay` | Pauses for a configured duration. |
| `webhook_wait` | Pauses until an external system hits a specific webhook endpoint. |

> **Not in the MVP:** `fan-out` / `fan-in` (parallel branches) are designed but deferred to a later phase. The MVP runs work sequentially, iterating dynamic task lists one at a time via loops.

---

## Vendor Abstraction — Models, Providers, Credentials

The MVP makes Arbitron **fully abstracted from any single LLM vendor** while still allowing a local `claude -p` (Claude subscription) backend for both reasoning and agentic editing.

### Two provider ports

- **`ChatProvider`** — reasoning/chat. Prompt (or conversation) + model + params + optional output schema → text or structured output + token usage. Streaming variant for `interactive`. No tools, no file side effects. Implementations: **tool-suppressed `claude -p`**, **native Azure AI Foundry**, **native OpenRouter**.
- **`AgentProvider`** — agentic editing. Mission (prompt + workdir + model) → file mutations in the workspace + result summary + step events. The tool-use loop runs inside the harness. Implementations: **`claude -p`** (tools on) and **`opencode`** (`run`/`serve`), which routes the mission to any provider/model. Non-Claude agentic editing (Azure, OpenRouter, open-weight) goes through opencode; Arbitron does not build its own API-level tool-use loop.

### Model Profile Registry

A **model profile** binds intent to backend:

```
profile = { alias, kind: chat | agent, provider, model-id, credential-ref, default params }
```

Nodes reference a profile by **alias** (e.g. `model_profile: fast-reasoner`). Resolution maps the alias to a concrete adapter (or harness + `provider/model` string) and a credential. This gives **per-node model freedom**, a **one-line backend swap** for every node using an alias, and **kind-matching validation** (an `agent_call` requires an `agent` profile; an `llm_call` requires a `chat` profile).

### Credential Store

A single local **encrypted** store of named secrets, referenced by profiles via `credential-ref`:

- Subscription `claude -p` needs **no** entry (it inherits the ambient Claude login) — a Claude-only local run requires zero stored keys.
- Native chat adapters (Azure, OpenRouter) read their key directly.
- Subprocess harnesses (`claude`, `opencode`) receive the required key **injected as an environment variable at launch** — this is how `opencode run --model <provider/model>` obtains the right key.

Keys never appear in `workflow.json` and are never logged.

---

## Loops — The Native Refinement Cycle

Arbitron's engine permits **cyclic graphs**. A loop is expressed with the **cyclic-edge pattern**: a back-edge forms the loop, a `transform` increments a counter, and a `conditional` checks *(done? OR counter ≥ max)* to either loop or exit. `interactive` nodes are excluded from loop bodies (they cache per `(instance, node)`).

This lets Arbitron run the **ralph refinement loop natively**, replacing the external ralph-o-matic server:

```
agent_call (edit toward focus areas)
   → llm_call (assess: tests / remaining gaps)
   → transform (increment counter)
   → conditional (loop back, or exit)
```

`RALPH.md`, `focus-areas`, and `gaps` are ordinary files in the instance `{{workspace}}` that the agent reads and rewrites each pass. **Termination is the author's `conditional`** — the MVP engine adds no runaway guard (a future backstop will cap max node-executions / wall-clock).

---

## Execution Model & Instance Lifecycle

- **Lifecycle states:** `queued → running → (waiting) → completed`, with `paused`, `failed`, and `cancelled`. `waiting` and `paused` do not count against concurrency caps.
- **Queue & concurrency:** global and per-user concurrency caps with priority (high / normal / low); the queue drains as capacity frees.
- **Error handling:** per-node and per-workflow behaviors — `halt`, `retry` (N times with delay), `skip`, or `route` to an error-handling edge.
- **Control:** instances can be paused, resumed, cancelled, retried (with variable edits), and rerun (fresh run from a prior one).
- **Crash recovery:** running instances are re-enqueued on startup.
- **Variable history:** each node execution snapshots variable state for replay and debugging; full traces record inputs, outputs, timing, errors, model, and token usage.

---

## Interfaces

- **MCP server** — the primary interface, used from Claude Code to author and operate workflows. Covers workflow management (list/get/create/update/delete/validate, file read/write), instance control (trigger/pause/resume/cancel/retry/rerun/priority), trace & variable inspection, system & queue config, notifications, event streaming (subscribe to instance and system events), and interactive sessions (send message, get history, end session).
- **REST API** — HTTP wrapping of the engine for non-MCP clients.
- **Web dashboard** — React UI showing the live workflow graph, instance state, traces, streaming LLM/agent output, and notifications. Served as embedded static assets.
- **Event streaming** — real-time node-progress and token events over WebSocket.
- **Notifications** — in-app notifications (with external transports as a future extension).

---

## The Feature Pipeline (Flagship Workflow)

The motivating acceptance test for the MVP is the native feature pipeline, expressed as one Arbitron workflow:

```
brainstorm-spec (interactive) → write-spec (script)
  → brainstorm-design (interactive) → write-designs (script)
  → [conditional: spec-only?] → stop | continue
  → generate-plan (llm_call) → generate-ralph-files (llm_call)
  → native ralph loop { agent_call → llm_call(assess) → transform → conditional }
  → pr-review (multi-persona) → done
```

The brainstorming process is copied **native** into the interactive nodes' orchestration/template files (no dependency on an external `Skill` tool), so it runs on any adapter. The pipeline ships in **two interchangeable configurations** that differ only in which profiles their nodes reference:

- **Claude-subscription configuration** — `claude -p` chat and agent nodes, for real work on local projects.
- **Open-weight configuration** — non-Anthropic / open-weight models (served via OpenRouter or Azure AI Foundry, agentic work via opencode), for lower-cost end-to-end testing of the full pipeline.

A separate multi-persona **PR review** workflow (Architect → 10x Engineer → Security → Manager, with optional auto-implement) already exists under `workspaces/pr-review/`.

---

## Architecture

Hexagonal (ports & adapters):

- **`domain/`** — pure business logic: `engine` (graph walker + node executors), `workflow` (definitions + validation), `instance` (lifecycle + repositories), `queue` (manager, scheduler, caps, priority), `variable` (store + `{{substitution}}`), `config`, `notification`. Ports (e.g. `ChatProvider`, `AgentProvider`, repositories) are defined here.
- **`adapter/`** — implementations: `claudecli` & `anthropic` & (Azure/OpenRouter) LLM backends, `opencode` agent harness, `mcp` (MCP server + tools), `http` (REST + dashboard host), `filesystem` (workflow/file storage), `sqlite` (instance/exec/snapshot/queue repos via sqlc), `eventbus`, `notification`, `observability` (logging + OTel), `process`/`procenv` (script & tool execution + environment), `config`, `dashboard`.
- **`cmd/arbitron/`** — CLI entry point (cobra) that wires domain + adapters.
- **`workspaces/`** — example workflow folders. **`plugin/skills/`** — Claude Code skills wrapping the MCP tools. **`docs/specs/`** — design specs, including `feature-pipeline-replacement-mvp.md`.

---

## Tech Stack

- **Language:** Go 1.25
- **Persistence:** SQLite (pure-Go `modernc.org/sqlite`) with **sqlc**-generated queries
- **HTTP router:** chi v5 · **WebSocket:** nhooyr.io/websocket
- **Config:** koanf (YAML / env / confmap) · **CLI:** cobra
- **MCP:** mark3labs/mcp-go · **JSON Schema:** santhosh-tekuri/jsonschema
- **Observability:** OpenTelemetry (tracing) + structured logging
- **LLM backends:** `claude -p` (subscription), Azure AI Foundry, OpenRouter; agentic editing via `claude -p` and `opencode`
- **Dashboard:** React (embedded build)

---

## Scope Boundaries

**In the MVP end-state:** the full node-kind set above (incl. `agent_call`), the `ChatProvider`/`AgentProvider` abstraction with Claude/Azure/OpenRouter chat backends and `claude -p`/`opencode` agent backends, the model-profile registry, the credential store, native cyclic loops, the MCP server, REST API, dashboard, streaming, notifications, queue/caps/priority, and the native feature pipeline in both configurations.

**Deferred (post-MVP):** `fan-out`/`fan-in` parallelism; a dedicated loop primitive and engine-level runaway guard; multi-user auth and private workspaces; scheduled (cron) and inbound webhook *triggers* (the `webhook_wait` node exists, but trigger registration does not); external notification transports (email/Teams/Slack); a native API-level tool-use agent loop (agency is delegated to harnesses).
