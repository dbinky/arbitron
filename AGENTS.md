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
- `spawn_workflow`

`spawn_workflow` starts a CHILD workflow instance from within a parent workflow.
It is ONE node type with two modes (`spawnMode`): `fire_and_forget` (the parent
continues along a single unnamed edge) and `wait_for_child` (the parent suspends
and resumes through its required `success` / `failure` ports). Do not add a
separate node type per mode.

When an awaited child finishes, its final variables (inputs plus node outputs,
minus the reserved `workspace`) come back as the spawn node's `childOutput` on
BOTH ports, e.g. `{{spawn-1.childOutput.review.verdict}}`. The node output also
carries `childInstanceId`, `childWorkflow`, `spawnMode`, and `childState`.

A spawn node may set an explicit `inputs` map (child input name to a literal or
`{{template}}` against the parent's variables). Mapped values are passed as
given and always win. The startup-value LLM runs only for required child inputs
that are unmapped and have no default; child defaults are passed explicitly, and
optional inputs are never LLM-filled. A mapped reference that cannot be resolved
fails closed (the spawn fails rather than passing an empty value). A fully mapped
spawn makes zero LLM calls and needs no `model_profile`. The node output carries
`inputSources` (input name to `mapped`, `default`, or `prepared`; names only,
never values); it is readable downstream (`{{spawn-1.inputSources.topic}}`) for
`fire_and_forget` spawns, and trace-only (first execution record) for awaited
spawns. A string in `inputs` that is exactly one `{{path}}` keeps the
referenced value's type; any other templated string is interpolated to a
string. Mapping parent `{{workspace}}` paths into a child is discouraged:
the child has its own workspace.

Use the code vocabulary above when editing manifests, validation, dashboard behavior, or tests. Older docs may refer generically to `interactive`.

Provider abstraction is centered on:

- `domain/engine.ChatProvider`: chat/reasoning, no tool use or file mutation.
- `domain/engine.AgentProvider`: agentic mission execution inside a workspace.
- `domain/provider.Profile`: alias, kind, connection ref, model, optional agent backend, and allowed tools.
- `domain/provider.Connection`: endpoint plus encrypted credential metadata, deliberately excluding model id.

Agent backend vocabulary is defined in `domain/workflow/workflow.go`:

- `claude-code`
- `opencode`
- `codex`
- `pi`

The backend axis is separate from provider/connection. Do not add vendor-specific node kinds. For cross-family rules, family is the model vendor (after stripping provider prefixes), not the backend.

## Pipeline stage workflows

The feature pipeline is split into stage workflows under `samples/`: `ideate`, `spec`, `design`, `plan`, `implement` and `harden`, plus `ralph-loop` and `pr-review`, which are the parent's refine and review stages. The parent `feature-pipeline` composes them with `spawn_workflow`. The older monolithic pipeline still ships, unchanged, as `legacy-feature-pipeline`; pre-release builds called the parent `feature-pipeline-next`, and pipeline manifests stamped with that name still resume.

- **Manifests.** Stage progress and outputs are recorded in `.arbitron/pipeline/<slug>.json` in the work repo; resume reads them to decide which stages to skip or adopt.
- **Tamper gate.** Stages that must not edit their own tests or contracts use the shared `@lib` tamper gate: the `tamper-gate` script, the `tamper-judge` prompt, and the `tamper-verdict` script. `harden`, `implement` and `ralph-loop` wire it. Reference the files as `@lib/...`. `_lib` files are seeded into the shared library at boot (once; admin edits are never overwritten) and live-read at run time.
- **Spawns.** Every `spawn_workflow` in the parent is fully mapped: each child input is passed explicitly through `inputs`, and each needed result is read from `childOutput`. Nothing is inherited implicitly.

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

Do not put an **awaited** `spawn_workflow` node (`spawnMode: wait_for_child`) in a loop body, for the same underlying reason. Its resumption short-circuit cannot distinguish a re-entry-after-wait from a back-edge revisit: both arrive at the node with a terminal link already recorded for (parent, node), so a revisit reads the PREVIOUS child's outcome and routes immediately instead of spawning a new child.

A **fire-and-forget** `spawn_workflow` node is loop-safe and carries no such restriction: it has no short-circuit, so each revisit spawns a new child, consistent with the documented re-execution semantics of loop revisits.

Instance workspaces are managed through the `WorkspaceManager` port. Agent nodes should mutate only the instance workspace, exposed to templates as `{{workspace}}`.

## Shared library (@lib)

`@lib` is a reusable file library (prompts, templates, scripts) that any workflow can reference. Source lives in `samples/_lib/` and is seeded into the shared library at boot, per file and create-if-absent (see `samples/_lib/README.md`).

- **Roots and precedence.** Two roots: the run owner's private `_lib` and the shared `_lib`. A reference resolves private first, then shared, keyed by the run OWNER (not the viewer or editor). A private file therefore shadows the shared file of the same path for your own runs only.
- **Where it works.** Use `@lib/<path>` in `templateFile`, `scriptFile`, `orchestrationFile`, `resolverPromptFile`, and in includes.
- **Include rule.** A relative include inside a lib file resolves against the lib root, not the including workflow. `./x` and `../` are rejected inside the lib; write `prompts/x.md`, not `./x.md`.
- **Naming.** The `@lib/` prefix is case-sensitive. `_lib` and `@lib` are reserved (case-insensitively) and can never be a workflow name. Paths are validated strictly: no traversal, absolute paths, backslashes or empty components, and no top-level `workflow.json`/`layout.json`. Containment and symlinks fail closed.
- **Live reads.** Lib files are read at run time, not snapshotted into an instance. A broken lib edit breaks every workflow that references it, including running instances. Lib scripts need tests and should change via PRs, not by hand on a live server.
- **MCP.** Use `workflow_list_files`, `workflow_read_file`, `workflow_write_file` and `workflow_delete_file` with `name: "@lib"` and an optional `scope` (`private` or `shared`). Without `scope`, write and delete act on the private library, a read is an effective read (private then shared), and a list ignores `scope` and returns both. Writing or deleting in `shared` requires admin and is audited (`lib.edit_shared`). The MCP write size limit also applies to lib files. `scope` is only valid with `name: "@lib"`.
- **Publish gotcha.** A published (shared) workflow resolves `@lib/...` for each run owner, so put the lib files in `shared` BEFORE publishing a workflow that uses them; files only in your private lib will not be found by other users.
- **Script gotcha: cwd.** A lib script runs with its cwd inside `_lib`, possibly the SHARED one. Scripts must write via absolute paths taken from stdin (e.g. `work_dir`), never relative paths.
- **Script gotcha: siblings.** `$(dirname "$0")` finds sibling files only in the same scope as the script; there is no fallback to the shared library.

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
