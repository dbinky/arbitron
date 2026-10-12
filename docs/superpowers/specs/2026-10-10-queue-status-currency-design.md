# Queue Status Currency — Design

Branch: `fix/status-currency`

## Intent

The dashboard queue page should describe each run **as it is now**, not as it
was at the last step change. A glance at the queue should answer, accurately:
how long has this run been going, what model/harness/provider is working on it
right now, and which branch is it working on.

### Problems being fixed

1. **Elapsed time freezes during long steps.** The cell is computed at render
   time (`now − createdAt`) but nothing re-renders it on a clock; rows only
   re-render on WebSocket events (≈ step changes). A 45-minute step shows the
   value from when it started.
2. **The Model / Backend cell is not current.**
   - *Model* comes from the node-execution aggregate's `LatestModel`: the most
     recently started execution that *recorded* a model. `llm_model` is only
     written when a node finishes, so a running node shows the previous node's
     model.
   - *Backend* comes from `nodeLabelAndBackend`, which reads `node.Config.Backend`
     and otherwise the deployment default. It ignores the profile's backend, so a
     codex-profile node displays `claude-code`.
   - Effort and provider are not produced at all.
   - The `node.started` live patch deliberately skips this cell (to avoid a
     mismatched `modelN-1 · backendN` pair).
3. **Terminal rows keep counting.** Failed/completed rows show
   `now − createdAt` instead of how long the run took; `completedAt` is already
   on the row but unused.
4. **No Git column.** There is no branch concept on the instance list; the
   branch only exists as a workflow variable (`branch`, `pr_branch`) or a path
   (`work_dir`).

### Success criteria

- The Elapsed cell of an active run advances every second with no server events.
- Failed/completed (and cancelled) rows show `completedAt − createdAt`, frozen.
- The Model cell shows the **currently running node's** configured binding as
  `model (effort) · harness · provider`, updated the moment a node starts.
  While a non-model node runs, it shows the node type (e.g. `script`).
- A **Git** column immediately after **Run** shows the run's branch/worktree.
- MCP clients authoring workflows are told the standard variable conventions.

### Assumptions (confirmed during brainstorming)

- Showing the *configured* model (resolved from the node's profile) for a running
  node is acceptable. The provider-reported model (e.g. a dated id) is only
  known after completion; it remains visible per node in the execution view
  (`llmModel`), but no longer in the queue.
- Elapsed is measured from `createdAt` (includes queue wait), matching today.

## Section 1 — Current-node binding resolver (Go, domain)

New domain function in `domain/engine`:

```go
// NodeBinding is the display-level description of what a node runs on.
type NodeBinding struct {
    NodeType string // always set: "agent_call", "script", ...
    Model    string // profile.Model, else node.Config.Model (legacy), else ""
    Effort   string // profile effort; "" = default
    Backend  string // agent-capable nodes only: node > profile > deployment default
    Provider string // "claude-cli" | "litellm" | "azure-openai" | ...
}

func resolveNodeBinding(ctx context.Context, resolver provider.ProfileResolver,
    node workflow.Node, defaultBackend string) NodeBinding
```

Rules:

- **Model nodes** (`llm_call`, `agent_call`, `interactive_llm`,
  `interactive_agent`): resolve `Config.ModelProfile` through the existing
  `provider.ProfileResolver` port — the same port the executors use — so profile
  inheritance and connection lookup are not re-implemented. `Model`, `Effort`,
  `Provider` come from the `ResolvedBinding`. `Backend` follows the agent
  executor's precedence: `node.Config.Backend` > `ResolvedBinding.Backend` >
  deployment default. Chat-only nodes (`llm_call`, `interactive_llm`) carry no
  backend.
- **No `model_profile`**: fall back to `node.Config.Model` for Model, and the
  node/default backend for agent-capable nodes.
- **Every other node type**: only `NodeType` is set.
- **Degradation**: an unknown profile (`provider.ErrUnknownProfile`) or any
  resolver error yields `NodeType` plus whatever the node config holds, logged at
  warn. It never fails the list (consistent with the enricher's
  one-field-degrades-only-that-field policy).
- The backend half of `nodeLabelAndBackend` is superseded by this resolver; the
  label half stays (or is folded in) so both the enricher and engine still share
  one label rule.

Callers:

- **Enricher** (`InstanceEnricher.Enrich`): binding fields are resolved for the
  failed node on `failed` rows and the current node on active rows
  (`running`/`waiting`/`paused`/`queued`). `completed` and `cancelled` rows get
  an empty binding (render `—`) by an explicit state check. Note: today's
  `relevantNodeID` returns `CurrentNodeID` for completed rows too (its comment
  claims otherwise), which is why completed runs show "Notify Teams" in the Step
  column; the Step column keeps that behavior, only the binding gets the state
  check, and the misleading comment is corrected. The enricher gains a
  `provider.ProfileResolver` collaborator (non-nil, Null-Object default, wired
  in `cmd/arbitron` via a setter like the other enricher collaborators).
- **Engine** `node.started` publish: event data gains `model`, `effort`,
  `backend`, `provider` (alongside the existing `nodeId`, `nodeType`,
  `nodeLabel`) plus `gitRef` (Section 2). The engine does not currently hold a
  `provider.ProfileResolver` (only its executors do), so it gains one via a
  `SetProfileResolver` setter (Null-Object default), wired in `cmd/arbitron`
  with the same resolver instance the executors receive.

API row (`instance.EnrichedInstanceSummary` and its HTTP/MCP JSON):

- **Added**: `Effort`, `Provider`, `NodeType`, `GitRef`.
- **Changed meaning**: `Model` and `Backend` now describe the relevant
  (current/failed) node's configured binding, not the last recorded execution.
- **Removed**: `NodeExecutionAggregate.LatestModel` and the correlated
  subquery in `NodeExecutionRepo.AggregateByInstances` become dead and are
  deleted. `MaxAttempt` (retry count) stays.

Credential safety: `ResolvedBinding.RevealKey`, `BaseURL` and `Extras` are never
copied into `NodeBinding`; only the provider family name crosses into payloads.

## Section 2 — Git column and the `gitVariable` manifest field

### Manifest

Optional top-level field on `workflow.Workflow`:

```json
{ "name": "pr-review", "gitVariable": "pr_branch" }
```

- The value is a variable path using the same dotted syntax templates use
  (e.g. `init-pr-workspace.pr_branch`), resolved with the existing template
  variable lookup.
- Validation: must be a well-formed variable path when present. It is **not**
  required to name a declared input — `branch` and `work_dir` are runtime
  variables set by transforms.
- A `gitVariable` naming a variable that never appears is not an error; the
  fallback ladder applies.

### Resolution

One domain function, `deriveGitRef(gitVariable string, vars map[string]any) string`;
the first non-empty string wins:

1. the variable named by `gitVariable` (if set)
2. `branch`
3. `pr_branch`
4. last path segment of `work_dir`
5. `""` → renders `—`

Callers:

- **Enricher**: from the row's current `Variables` and the snapshot's
  `gitVariable`. Terminal rows use their final variables, so a completed run
  keeps showing the branch it worked on.
- **Engine** `node.started` publish: from `vars.Snapshot()`. `branch` is usually
  set a few nodes into the run by a transform; the live event fills the cell
  without waiting for a lifecycle refetch.

The pr-review sample gains `"gitVariable": "pr_branch"` to be explicit (its
`work_dir` ends in `/work`, which would be a useless fallback; `pr_branch`
already precedes it in the ladder).

### MCP authoring guidance

Today the MCP server provides no authoring guidance (`workflow_create` says only
"Create a new workflow definition from a JSON manifest"; no server
instructions). Add:

- A single Go constant holding an "Authoring workflows" guidance block, used by:
  - `server.WithInstructions(...)` on the MCP server, and
  - a one-line pointer appended to the `workflow_create` and `workflow_update`
    tool descriptions (convention summary + `gitVariable`).
- Content — standard variable conventions:

  | Name | Meaning | Kind |
  |---|---|---|
  | `repo` / `repo_url` | `owner/name` or clone URL | input |
  | `base_branch` | ref the work is cut from / targets | input |
  | `pr_number` | PR under work | input |
  | `slug` | kebab label for branch/doc naming | input |
  | `teams_webhook_url` | completion alert webhook | input |
  | `branch` | the work branch (preferred name; feeds the Git column) | runtime var |
  | `work_dir` | checkout/worktree path | runtime var |

- Also states: prefer `branch` over bespoke names (e.g. `pr_branch`); set
  `gitVariable` when the branch lives under another name; profiles are pinned
  per node via `model_profile` and there is no profile-input convention.

## Section 3 — Dashboard

### Ticking clock

- New hook `useNow(intervalMs = 1000)`: one `setInterval` for the page, returns
  `Date.now()`. Pauses while `document.visibilityState === 'hidden'` and
  re-syncs immediately on becoming visible.
- `QueueView` calls it once and threads `now` to rows (no per-row timers).
- `elapsed()` and `relativeTime()` in `utils/time.ts` take an optional
  `now: number` argument (default `Date.now()`), keeping them pure and testable.
  This also unfreezes the "relative" time mode (`updatedAt` ago).

### Elapsed stops on terminal rows

- New `runDuration(row, now): number` (ms): `completedAt − createdAt` when the
  row is terminal (`completed`, `failed`, `cancelled`) and has `completedAt`;
  otherwise `now − createdAt`.
- The Elapsed cell formats `runDuration`. The column's `sortAccessor` sorts by
  duration (so sorting Completed by Elapsed ranks by how long runs took).
- `InstanceRow.tsx` (the other `elapsed(createdAt)` caller) uses the same
  helper.

### Model cell

- Header renamed "Model / Backend" → "Model".
- One shared formatter `formatBinding(row)` used by the column and
  `RowDetailPanel`:

  | Situation | Rendering |
  |---|---|
  | agent node | `gpt-6.1-sol (high) · codex · litellm` |
  | chat node | `claude-sonnet-5 (medium) · claude-cli` |
  | default effort | `opus-5.5 · claude-code · claude-cli` |
  | non-model node | `script` |
  | completed/cancelled, or nothing known | `—` |

  Empty parts are omitted with their separator.

### Live patching

- `NodeEventData` gains `nodeType`, `model`, `effort`, `backend`, `provider`,
  `gitRef`.
- On `node.started`, `QueueView` patches `currentNodeId`, `friendlyStep`,
  `nodeType`, `model`, `effort`, `backend`, `provider`, `gitRef` together. The
  stale "patch only the Step column" rationale comment is replaced.
- `node.failed` patches the same fields it does today.
- Event values remain untrusted author-controlled strings rendered only through
  React text sinks.

### Git column

- Added to `COLUMNS` immediately after `runName`:
  `{ key: 'git', header: 'Git', accessor: r => r.gitRef || '—', sortable: true,
  densities: ['balanced', 'expanded'] }`.
- Long values truncate with CSS ellipsis; full value in a `title` attribute
  (plain branch string; comment notes the sink, mirroring the existing note).

## Testing (strict TDD)

Each unit covers happy path, edge cases, error conditions, boundaries, and
integration.

Go:

- `resolveNodeBinding`: every node type; profile hit; unknown profile; resolver
  error; no profile with legacy `Config.Model`; backend precedence node >
  profile > default; chat nodes carry no backend; no credential fields leak.
- `deriveGitRef`: each ladder rung; dotted `gitVariable`; mistyped
  `gitVariable` falls through; non-string values skipped; `work_dir` basename
  incl. trailing slash; empty vars.
- Enricher: active/failed/completed rows populate the new fields per
  `relevantNodeID`; resolver failure degrades only binding fields.
- Engine: `node.started` payload carries binding fields and `gitRef` from live
  variables.
- Workflow validation: `gitVariable` accepted/rejected forms; round-trips
  through JSON.
- Aggregate: `LatestModel` removed; `MaxAttempt` unchanged.
- MCP: server instructions and `workflow_create`/`workflow_update` descriptions
  contain the conventions and `gitVariable`.

Dashboard (Vitest):

- `useNow`: ticks; pauses when hidden; resyncs on visible; cleans up.
- `runDuration`: active, each terminal state, terminal without `completedAt`.
- `elapsed`/`relativeTime` with injected `now`.
- `formatBinding`: every row of the table above.
- `COLUMNS`: Git directly after Run; densities.
- `QueueView`: `node.started` patches the full binding + gitRef; elapsed cell
  advances under fake timers.

Gates: `go test ./...`; `cd dashboard && npx tsc --noEmit && npx vitest run`;
rebuild the embedded SPA via `make ui` (or `npm run build` if `make ui` trips on
lockfile drift).

## Out of scope

- Showing the provider-reported model in the queue.
- A profile-input convention for workflows.
- Per-row step elapsed (time in current step).
