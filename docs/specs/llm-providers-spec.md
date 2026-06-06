# LLM Provider Adapters — Product Specification

**Status:** Draft
**Slug:** `llm-providers`
**Date:** 2026-06-05

## Summary

Arbitron workflows drive Large Language Models through configurable **providers**.
Today, Claude (via the `claude -p` CLI subscription) works, alongside HTTP chat
access to OpenRouter and Azure AI Foundry and a headless agent path. This feature
expands provider coverage to **seven provider families** and makes every one of them
selectable on **both** node tiers — non-tool-using "chat" nodes and tool-using
"agent" nodes — so operators can build and run **mixed-provider workflows** where
different nodes use different providers (for example, a cheap local model drafts and
a frontier model reviews).

## Goals

- Let a workflow author point any LLM node at any supported provider by selecting a
  **model profile**, and mix providers freely across nodes in a single workflow.
- Guarantee that **every** provider works on chat / non-tool-using nodes.
- Enable agent / tool-using nodes across **all** providers by default, failing closed
  with a clear error only where the underlying runner genuinely cannot drive a
  provider with tools.
- Let an operator **validate** a provider connection (credentials + reachability) and
  **list its models** before committing a workflow run to it.
- Keep all credentials in the existing encrypted connection store — never in
  plaintext workflow files, logs, or error messages.

## Non-Goals

- A graphical connection-management UI. Connections are configured through the
  existing configuration mechanism.
- Automatic model discovery or routing/optimization logic (cost-based routing,
  automatic failover between providers). Workflow authors choose providers explicitly
  per node.
- Validating or curating which model names are "good"; the operator chooses models.
- Fine-grained, per-tool human-in-the-loop approval flows. Agent execution is
  open-by-default (see Default Execution Posture).

## Personas

- **Operator** — configures provider connections and credentials, sets execution
  posture, and runs the Arbitron server. Trusted; has access to the host.
- **Workflow author** — builds workflows and selects, per node, which model profile
  (and therefore which provider/model) to use.

## Provider Catalog

Eight selectable connection types across seven provider families:

| Provider | Authentication | Endpoint |
|---|---|---|
| **Claude CLI** (existing) | Subscription — no stored key | local `claude -p` |
| **Anthropic API** | API key | Anthropic-hosted |
| **OpenAI API** | API key | OpenAI-hosted |
| **OpenRouter** (existing) | API key | OpenRouter-hosted |
| **Azure AI Foundry** (existing) | Endpoint + API key | operator's Azure endpoint |
| **Amazon Bedrock** | Stored AWS access key id + secret access key (+ optional session token) + region | AWS regional endpoint |
| **Ollama — cloud (ollama.com)** | API key | ollama.com |
| **Ollama — local** | None | operator's base URL (e.g. `http://localhost:11434`) |

Notes:

- **Ollama** appears as two distinct connections of the same family: a keyless local
  connection with a custom base URL, and a keyed cloud (ollama.com) connection.
- **Local Ollama** and **Claude CLI subscription** are the keyless paths and need no
  stored secret.

## Connections & Credentials

- Credentials live in the existing **encrypted connection store**. A workflow file
  never contains a secret; it references a **model profile alias**.
- A **connection** records: the provider family, an optional base URL (for local
  Ollama, Azure, or self-hosted endpoints), the secret(s), and non-secret provider
  **extras** (Bedrock **region** and optional **session token**, Azure endpoint, etc.).
- **Amazon Bedrock** authenticates with **stored AWS credentials**: access key id +
  secret access key, an optional session token, and a region. These are held in the
  encrypted store like any other secret.
- A **model profile** binds an alias → connection + model + kind (`chat` or `agent`).
  Workflow nodes reference the alias via their `model_profile`.

## Node Tiers & Capability

Two node tiers consume providers:

- **Chat / non-tool-using** — `llm_call`, `interactive_llm`.
- **Agent / tool-using** — `agent_call`, `interactive_agent`.

| Provider | Chat (non-tool) | Agent (tool-using) |
|---|---|---|
| Claude CLI | ✅ | ✅ |
| Anthropic API | ✅ | ✅ |
| OpenAI API | ✅ | ✅ |
| OpenRouter | ✅ | ✅ |
| Azure AI Foundry | ✅ | ✅ |
| Amazon Bedrock | ✅ | ✅ |
| Ollama (cloud) | ✅ | ✅ |
| Ollama (local) | ✅ | ✅ |

- **Chat is guaranteed for all eight connection types.**
- **Agent / tool-using support is enabled for every provider by default.** The system
  attempts tool-use across all providers. It fails closed — with a clear, actionable
  error naming the provider and node — only where the underlying runner genuinely
  cannot drive that provider with tools. It never silently degrades to a tool-less run
  when an agent node was requested.
- The **chat-vs-agent kind guard remains in force**: a node declared as chat can never
  be handed agent tools, and vice-versa. This structural protection is independent of
  the execution posture below and is not relaxed.

## Default Execution Posture (open-by-default)

This is a deliberate, explicit stance for a multi-provider **experimentation harness**,
chosen to minimize friction:

- **Permission-skipping is ON by default** for agent nodes across every provider
  (e.g. `--dangerously-skip-permissions` and each runner's equivalent).
- **The agent tool set is broadly open by default** — agent nodes receive wide tool
  access unless explicitly narrowed.
- **Restriction is the operator's responsibility.** To tighten behavior, the operator
  explicitly edits configuration to (a) disable permission-skipping and/or (b) supply
  a narrower allowed-tools allowlist on a profile. Absent such configuration, behavior
  is broadly open.

This consciously trades safe-by-default for open-by-default. It is appropriate because
the operator is trusted and has host access, and because frictionless agent execution
is the priority for experimentation. Secrets are still never exposed in errors or logs,
and the kind guard still prevents chat→agent escalation.

> **Security amendment (allow-all is opt-in, CLOSED by default).** For the
> opencode-driven (non-claude) agent providers, the allow-all tool posture is
> gated behind `llm.agentallowalltools`, which **ships `false` (closed)**.
> Granting allow-all to a credential-bearing, tool-using subprocess fed
> model-influenced prompts is a lethal-trifecta / RCE-equivalent posture, so it
> is an explicit operator opt-in (`agentallowalltools: true`) rather than the
> out-of-the-box behavior. Per-profile `allowedtools` narrows tool access
> independently of this switch.

## Connection Validation & Model Listing

An operator can validate any configured connection on demand, outside of a workflow run:

- **Validation** confirms that credentials authenticate and the endpoint is reachable.
- **Model listing** returns the provider's available models **where the provider exposes
  that capability**. Providers that do not expose a model list (e.g. the Claude CLI
  subscription) report reachability only and say so explicitly.
- Results are reported clearly: success (with the model list where available), or a
  specific failure — invalid credentials, unreachable endpoint, region/configuration
  error.

This is the intended onboarding path: configure a connection → validate it → reference
it from a node.

## Failure Visibility

Every provider failure surfaces as a **node-level error** the workflow author can act on,
distinguishing:

- **Configuration faults** — missing or invalid credentials, unknown/unavailable model,
  unreachable endpoint, or an agent node requested against a provider the runner cannot
  drive with tools.
- **Transient faults** — timeouts, rate limits, upstream 5xx responses.

Errors name the provider and the affected node and **never leak secrets**.

## Success Criteria

1. **Mixed-provider workflow runs end-to-end.** A workflow using different providers on
   different nodes — e.g. local-Ollama drafter → Anthropic-API reviewer → OpenAI-API
   summarizer — completes successfully.
2. **Universal chat.** All eight connection types execute a chat / non-tool-using node
   successfully against a live or faked endpoint.
3. **Open-by-default agents.** Agent / tool-using nodes succeed across all providers with
   permission-skipping on and a broad tool set by default; where a runner genuinely
   cannot drive a provider with tools, the node fails closed with a clear error.
4. **Operator restriction works.** Disabling permission-skipping and/or supplying a
   narrower allowed-tools allowlist on a profile measurably narrows agent behavior.
5. **Validation.** Each connection type can be validated; model listing returns results
   where the provider supports it and reports reachability-only where it does not.
6. **No secret leakage.** No secret appears in workflow files, logs, or error messages.

## Open Questions

_None at this time._
