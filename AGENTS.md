# AGENTS.md

## Project Orientation

Arbitron is a Go workflow orchestration service for directed graphs of LLM calls, agentic coding work, scripts, tools, wait nodes, and human-in-the-loop steps. It is intended to replace the older ralph-o-matic refinement server and feature-pipeline skill with native Arbitron workflows.

The project was previously Claude-oriented, but the current codebase is not Claude-only. The active direction is vendor abstraction: workflow nodes declare intent, while model/profile/connection resolution selects the concrete provider or harness.

Important current-state distinction: `README.md` is written as a target MVP end-state. Some parts are already implemented; some are spec-driven or in-flight. Verify against code before treating README claims as shipped behavior.

## Architecture

- `domain/` contains business logic and ports. Keep concrete adapter imports out of domain packages.
- `adapter/` contains implementations for CLI/API providers, HTTP, MCP, SQLite, filesystem, config, event bus, notifications, dashboard embedding, probes, and observability.
- `cmd/arbitron/` is the Cobra composition root and CLI entry point.
- `dashboard/` is the React/Vite dashboard source. Built assets are copied into `adapter/dashboard/dist` and embedded by Go.
- `samples/` contains bundled sample workflows. `examples/` contains worked examples.
- `docs/specs/`, `docs/designs/`, and `docs/plans/` contain much of the product and implementation history.

The intended layering is hexagonal: domain defines ports, adapters satisfy ports, and `cmd/arbitron` wires them.

## Current Implementation Notes

- Go module: `github.com/arbitron/arbitron`, Go `1.25.5`.
- Main test gate for Go: `go test ./...`.
- Dashboard test gate: from `dashboard/`, run `npx tsc --noEmit` and `npx vitest run`.
- Full local gate: `make test-all`.
- Dashboard build: `make ui`. Run this after dashboard source changes so `adapter/dashboard/dist` stays in sync with embedded assets.
- Release artifacts are built with `make dist`; installer and release tests use `bats`/`shellcheck` targets from the Makefile.

Core workflow node type constants live in `domain/workflow/workflow.go`. The code currently uses:

- `llm_call`
- `agent_call`
- `script`
- `tool_call`
- `conditional`
- `transform`
- `timer_delay`
- `webhook_wait`
- `interactive_llm`
- `interactive_agent`

Use the code vocabulary above when editing manifests, validation, dashboard behavior, or tests. Older docs may refer generically to `interactive`.

Provider abstraction is centered on:

- `domain/engine.ChatProvider`: chat/reasoning, no tool use or file mutation.
- `domain/engine.AgentProvider`: agentic mission execution inside a workspace.
- `domain/provider.Profile`: alias, kind, connection ref, model, optional agent backend, and allowed tools.
- `domain/provider.Connection`: endpoint plus encrypted credential metadata, deliberately excluding model id.

Agent backend vocabulary is defined in `domain/workflow/workflow.go`:

- `claude-code`
- `opencode`

The backend axis is separate from provider/connection. Do not add vendor-specific node kinds.

## Authorization, Visibility, and Multi-User State

The current branch has active work around workflow visibility, departments, origin stamping, intervention audit, and dashboard catalog filtering. Relevant packages include:

- `domain/identity`
- `domain/instance`
- `domain/workflow`
- `domain/audit`
- `adapter/http`
- `adapter/mcp`
- `dashboard/src/routes/WorkflowsCatalog*`
- `dashboard/src/hooks/useWorkflowsCatalog*`

Treat service-layer authorization as a boundary. Recent commits explicitly note that catalog authorization belongs in the service layer. Avoid bypassing services from handlers or MCP tools when adding read/write paths.

## Engine and Loop Behavior

The engine permits cyclic graphs. There is no general engine-level runaway guard; loop termination depends on workflow conditionals. The native ralph loop idiom is a back-edge through `agent_call -> llm_call -> transform -> conditional`.

Do not put interactive nodes in loop bodies. The interactive executor has reuse/session behavior that is not appropriate for repeated loop passes.

Instance workspaces are managed through the `WorkspaceManager` port. Agent nodes should mutate only the instance workspace, exposed to templates as `{{workspace}}`.

## Provider and Credential Safety

Connection keys must never appear in workflow JSON, logs, or dashboard payloads. Connection metadata and key reveal paths are separated in `domain/provider.ConnectionStore`.

Provider-specific validation exists for Azure and Bedrock connection metadata. Reuse the existing validation helpers instead of duplicating ad hoc checks.

Subprocess-backed providers receive credentials through process environment injection. Keep redaction and env construction tests updated when adding providers or new credential fields.

## Dashboard Notes

The dashboard uses React 19, React Router 7, Vite 8, Vitest, CSS modules, and `@xyflow/react` for graph rendering. Prefer the existing route/component/hook layout under `dashboard/src`.

When changing dashboard source:

1. Add or update focused Vitest coverage for the affected hook/component/route.
2. Run `cd dashboard && npx tsc --noEmit && npx vitest run`.
3. Run `make ui` if the embedded SPA artifact should reflect the source change.

## Working Practices

- Prefer `rg`/`rg --files` for search.
- Keep edits scoped; this repo has many spec-driven surfaces and test files.
- Preserve user or generated work in a dirty worktree. Do not revert unrelated changes.
- Add tests near the package you change. This codebase has strong package-level test coverage and often tests edge cases explicitly.
- For docs-only changes, tests may not be necessary; still check `git diff` before finishing.
- Use `gofmt` after Go edits.

## Useful Commands

```bash
go test ./...
make test
make test-ui
make test-all
make ui
make build
```

For dashboard-only work:

```bash
cd dashboard
npx tsc --noEmit
npx vitest run
npm run build
```

## Current Branch Context

At orientation time, the working branch was `dev-predeploy-testing`, clean, and ahead of `origin/dev-predeploy-testing` by two commits. Recent commits included:

- `docs(spec): loop-back edge side-routing design`
- `fix(graph): stop node-click blanking the workflow review screen`
- `docs(spec): note the service layer is the catalog authorization boundary`

Re-check `git status --short --branch` and recent history before assuming this is still true.
