# Arbitron

Arbitron is a self-hosted workflow engine for AI-assisted software work. A
workflow is a directed graph of steps: chat calls, coding agents, scripts,
human-in-the-loop conversations, timers, webhooks and child workflows. Each
step names the model it should run on, so one graph can mix Claude, GPT,
GLM, DeepSeek and other models, and swapping a model is a one-line change.

It ships with a complete feature pipeline (ideate → spec → design → plan →
implement → harden → refine → review) built from those parts, plus standalone
review and refinement workflows.

## Why it exists

Coding agents are good at doing work and bad at judging their own work. Given
a goal and enough tokens, a single agent will often edit the tests, weaken an
assertion, or declare victory early. Arbitron's workflows are built around a
separation of powers:

- **The model that writes never approves.** Reviews, judges and verifiers run
  on a different model *family* from the step they check. Family is the real
  vendor, so a gateway alias for an open-weight model cannot pass as Claude.
- **Deterministic gates sit between model steps.** Test gates compare against
  a recorded branch-point baseline, a tamper gate blocks a step from editing
  the tests or contracts it is judged by, and mutation testing checks that the
  new tests can actually fail.
- **Every step gets a small, focused context** instead of one long session,
  which keeps quality up and token use down.
- **Runs are durable.** A run survives restarts and quota walls, can be paused,
  retried from the failed step or rerun, and humans step in where judgment is
  needed.

## Status

Arbitron is pre-1.0 and maintained by one person. It runs on **macOS and
Linux** (Windows is not supported). Interfaces and workflow manifests may still
change between releases. See [Releases](https://github.com/dbinky/arbitron/releases).

## Install

One command installs the latest release as a background service, checksum- and
signature-verifies the binary, writes the config, and health-checks the
server:

```bash
curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
```

The installer covers three setups: macOS local, Linux local, and a Linux
shared server. Pin a version with `ARBITRON_VERSION=x.y.z`, and script every
prompt for unattended installs. See [docs/install/](docs/install/README.md),
plus the guides for [upgrading](docs/install/upgrading.md),
[security](docs/install/security.md) and
[tunnels and TLS](docs/install/tunnels-and-tls.md).

### Build from source

Requires Go 1.25 and, for the dashboard, Node.js.

```bash
git clone https://github.com/dbinky/arbitron.git
cd arbitron
make ui      # build the dashboard and embed it
make build   # produces ./arbitron (CGO_ENABLED=0, pure-Go SQLite)
./arbitron serve
```

### What the agents need

Arbitron drives agent harnesses that you install and log in to yourself:
[Claude Code](https://code.claude.com), and optionally
[Codex](https://developers.openai.com/codex), [pi](https://www.npmjs.com/package/@earendil-works/pi-coding-agent)
and [opencode](https://opencode.ai). Workflows that touch GitHub need `git` and
an authenticated `gh`. The bundled `harness-update` workflow keeps the
harnesses current.

## Quick start

1. Start the server: `arbitron serve` (the installer does this for you). The
   dashboard is at `http://localhost:8080`, the REST API under `/api/v1`.
2. Connect Claude Code (or any MCP client) to the MCP server:

   ```bash
   claude mcp add --transport http arbitron http://localhost:8080/mcp
   ```

   `arbitron mcp` serves the same tools over stdio.
3. Ask your agent to list the workflows, then trigger one, for example
   `commit-review` with a repo, a commit SHA and a reviewer model. Watch it run
   in the dashboard.

The bundled workflows are seeded on first start. They reference model
profiles by alias; each sample's `PROFILES.md` lists the profiles it expects.

## Concepts

- **Workflow** — a folder with a JSON manifest (nodes and edges) plus the
  prompt templates and scripts it uses. Authored through MCP tools and
  validated on save; the dashboard renders the graph.
- **Instance** — one run of a workflow, with its own variables, workspace,
  trace and lifecycle (`queued`, `running`, `waiting`, `paused`, `completed`,
  `failed`, `cancelled`). An instance snapshots its manifest when it starts.
- **Variables** — inputs plus each node's output. Templates read them with
  `{{node.field}}`; `{{workspace}}` is the run's private working directory.
- **Edges** are plain wires; all branching happens in `conditional` nodes.
  Graphs may contain cycles, which is how loops are built.
- **`@lib`** — a shared library of prompts and scripts that any workflow can
  reference as `@lib/<path>`.

### Node types

| Type | What it does |
|---|---|
| `llm_call` | A chat call with no tools: templated prompt in, text or JSON out. |
| `agent_call` | A coding agent that works inside the run's workspace. |
| `interactive_llm` / `interactive_agent` | A multi-turn conversation with a person, in the dashboard. The run waits for replies. |
| `script` | Runs a script; the run's variables arrive as JSON on stdin, JSON output on stdout becomes the node's output. |
| `tool_call` | Runs a CLI tool and collects its output. |
| `conditional` | Routes to one named branch based on the run's variables. |
| `transform` | Sets or updates variables without calling anything. |
| `timer_delay` / `webhook_wait` | Waits for a duration or an inbound webhook without holding a concurrency slot. |
| `spawn_workflow` | Starts a child workflow, either fire-and-forget or waiting for its result. |
| `parallel_group` | Runs branches or child workflows in parallel and gathers their results. |

Every node can retry, halt, skip or route on error.

## Models, connections and profiles

Nodes never name a vendor. They reference a **profile** by alias:

- A **connection** is an endpoint plus its credential (encrypted at rest,
  never returned by any API, never written to workflow files or logs).
- A **profile** picks a model on a connection, its kind (`chat` or `agent`),
  the agent backend, effort and context size, and the tools an agent may use.

Chat runs on the Claude CLI (subscription), the Anthropic API, Azure OpenAI
and Azure AI Foundry, Amazon Bedrock, OpenAI-compatible endpoints (OpenRouter,
LiteLLM, OpenAI, Ollama and Ollama Cloud) and the LunaRoute gateway. Agent
work runs on four backends: **claude-code**, **codex**, **pi** and
**opencode**. Arbitron checks which model actually ran, so a pinned profile
cannot silently fall back to another model.

LunaRoute supports chat and the codex and pi agent backends; see
[docs/setup/lunaroute.md](docs/setup/lunaroute.md).

## Bundled workflows

| Workflow | Purpose |
|---|---|
| `feature-pipeline` | Composes the stage workflows below, recording progress in `.arbitron/pipeline/<slug>.json` so a run can resume where it stopped. |
| `ideate`, `spec`, `design`, `plan` | Interactive idea, spec and design sessions, then unattended alignment and TDD task plans with a dependency gate. |
| `implement` | Implements the plan one task per pass, each pass checked by the tamper gate and a judge from another model family. |
| `harden` | Mutation-tests the changed code and strengthens the tests until a score gate passes. |
| `ralph-loop` | A refinement loop: a review plan seeded by one model and critiqued by another, then repeated review-and-fix passes with role-routed, cross-family model pairs. |
| `pr-review` | A multi-persona PR review (architect, engineer, security, manager), with optional auto-fix and verification. |
| `commit-review` | An adversarial review of a single commit by the model you choose, read-only through `gh`. |
| `harness-update` | Updates the agent harnesses; scheduled daily. |

## Interfaces

- **MCP server** — the main interface: authoring, running and inspecting
  workflows, profiles, connections, schedules, notifications and interactive
  sessions.
- **Dashboard** — React app embedded in the binary: workflow graphs with
  automatic layout, live run views with streamed agent output, the run queue,
  interactive sessions and admin settings.
- **REST API and WebSocket events** under `/api/v1`.
- **Schedules** — cron triggers for any workflow.
- **Notifications** — in-app, optionally forwarded to a Microsoft Teams channel.

## Users and access

Out of the box Arbitron runs in **local mode**: one user, no login, listening
on localhost. For shared servers it supports **edge** authentication behind
Cloudflare Access and **org** mode with OIDC single sign-on, admin and user
roles, departments, and private, department or public workflow visibility.
See [docs/install/security.md](docs/install/security.md) and the
[agent security model](docs/agent-security-model.md).

## Architecture

Hexagonal (ports and adapters), in Go:

- `domain/` — engine, workflows and validation, instances, queue, variables,
  providers and identity. Defines the ports; imports no adapters.
- `adapter/` — implementations: LLM providers and agent harnesses, MCP, HTTP,
  SQLite (via sqlc), filesystem storage, auth, notifications, observability
  (OpenTelemetry), the embedded dashboard.
- `cmd/arbitron/` — the CLI and composition root.
- `dashboard/` — React 19 + Vite source for the embedded UI.
- `samples/` — bundled workflows; `samples/_lib/` seeds the shared library.
- `docs/` — install and setup guides, plus the specs, designs and plans the
  project was built from.

## Development

```bash
go test ./...                                    # Go tests
cd dashboard && npx tsc --noEmit && npx vitest run   # dashboard tests
make test-all                                    # everything CI runs
```

[AGENTS.md](AGENTS.md) is the orientation guide for contributors and coding
agents. See [CONTRIBUTING.md](CONTRIBUTING.md) to get involved and
[SECURITY.md](SECURITY.md) to report a vulnerability.

## License

Licensed under the [Apache License, Version 2.0](LICENSE).
Copyright 2026 Lets All Play Together, LLC.
