# Queue Status Currency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the dashboard queue page reflect each run's current state — live-ticking elapsed, frozen terminal durations, the running node's model/effort/harness/provider, and a Git column.

**Architecture:** Two pure domain functions (`resolveNodeBinding`, `deriveGitRef`) in `domain/engine` are the single source of truth; the instance enricher (list refetch) and the engine's `node.started` event (live patch) both call them so a patch and a refetch always agree. The dashboard gains a single page-level clock and shared formatters.

**Tech Stack:** Go 1.25 (domain/adapter hexagonal), SQLite, mark3labs mcp-go; React 19 + Vite + Vitest + CSS modules.

**Spec:** `docs/superpowers/specs/2026-10-10-queue-status-currency-design.md`

## Global Constraints

- Strict TDD: failing test first, for happy path, edge cases, errors, boundaries, integration.
- `gofmt` after Go edits; `go test ./...` green after every Go task.
- Domain packages import no adapter packages.
- Never copy `ResolvedBinding.RevealKey`, `BaseURL`, or `Extras` into `NodeBinding` or any payload.
- Event/author-controlled strings render only through React text sinks (plus the plain-string `title` on the Git cell, commented).
- Dashboard gate: `cd dashboard && npx tsc --noEmit && npx vitest run`.
- Rebuild embedded SPA at the end: `make ui`, or `cd dashboard && npm run build` if `make ui` fails on lockfile drift.
- Commit messages end with `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>`.

## Review Focus

1. **Profile deleted/renamed while a run is mid-node** — the row still renders, binding shows the node type (+ legacy `Config.Model`), never an error. Test: Task 2 `TestResolveNodeBinding_UnknownProfile`.
2. **`gitVariable` or `branch` holding a non-string (number, map, null)** — skipped, next rung used. Test: Task 1 `TestDeriveGitRef_NonStringSkipped`.
3. **`completedAt` earlier than `createdAt` (clock skew) or unparseable** — duration clamps to 0, never negative/NaN. Test: Task 6 `runDuration clamps negative to 0`.
4. **Queued row (no current node, no `branch` yet)** — binding empty, Git `—`, no panic. Test: Task 3 `TestEnrich_QueuedRowEmptyBindingAndGit`.
5. **A `node.started` event missing the new fields (old server / partial payload)** — existing row values are kept, not wiped to undefined. Test: Task 8 `node.started without binding fields keeps prior values`.

---

### Task 1: `gitVariable` manifest field + `deriveGitRef`

**Files:**
- Modify: `domain/workflow/workflow.go:160-173` (add field)
- Modify: `domain/workflow/validation.go` (add `validateGitVariable`, call from `Validate`)
- Create: `domain/engine/gitref.go`
- Test: `domain/engine/gitref_test.go`, `domain/workflow/validation_gitvariable_test.go`

**Interfaces:**
- Produces: `workflow.Workflow.GitVariable string` (`json:"gitVariable,omitempty"`)
- Produces: `func deriveGitRef(gitVariable string, vars map[string]any) string` in package `engine`
- Produces: validation code `"invalid_git_variable"`, severity error

- [ ] **Step 1: Write failing tests** in `domain/engine/gitref_test.go` (table-driven `TestDeriveGitRef`):
  - `gitVariable="pr_branch"`, vars `{branch:"b", pr_branch:"p"}` → `"p"` (manifest wins)
  - `gitVariable=""`, `{branch:"feat/x", pr_branch:"p"}` → `"feat/x"`
  - `{pr_branch:"p"}` → `"p"`
  - `{work_dir:"/a/b/feat-wt"}` → `"feat-wt"`; `{work_dir:"/a/b/feat-wt/"}` → `"feat-wt"`
  - `gitVariable="init-pr-workspace.pr_branch"`, `{"init-pr-workspace": map[string]any{"pr_branch":"q"}}` → `"q"` (dotted)
  - `gitVariable="nope"`, `{branch:"b"}` → `"b"` (mistyped falls through)
  - `{branch:""}` and `nil` vars → `""`
  - `TestDeriveGitRef_NonStringSkipped`: `{branch: 42, pr_branch: "p"}` → `"p"`; `gitVariable="x"`, `{x: map[string]any{}, branch:"b"}` → `"b"`

- [ ] **Step 2: Write failing validation tests** in `domain/workflow/validation_gitvariable_test.go`: `""` → no error; `"branch"`, `"init-pr-workspace.pr_branch"` → no error; `"bad var"`, `".x"`, `"x."`, `"a..b"`, `"{{branch}}"` → one error with Code `"invalid_git_variable"`. Plus JSON round-trip: unmarshal `{"gitVariable":"pr_branch"}` → field set; marshal zero value omits the key.

- [ ] **Step 3: Run** `go test ./domain/engine/ -run DeriveGitRef ./domain/workflow/ -run GitVariable` — Expected: FAIL (undefined).

- [ ] **Step 4: Implement.** `deriveGitRef` looks up each rung with `variable.FromSnapshot(vars).Get(path)` and accepts only non-empty `string`; the `work_dir` rung uses `path.Base(strings.TrimRight(v, "/"))` (reject `"."`/`"/"`). `validateGitVariable` uses `^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`.

- [ ] **Step 5: Run** the same tests and `go test ./domain/...` — Expected: PASS.

- [ ] **Step 6: Commit** `feat(workflow): gitVariable manifest field and git-ref derivation`

---

### Task 2: `resolveNodeBinding`

**Files:**
- Create: `domain/engine/node_binding.go`
- Test: `domain/engine/node_binding_test.go`

**Interfaces:**
- Consumes: `provider.ProfileResolver`, `provider.ResolvedBinding`, `provider.ErrUnknownProfile`, existing `firstNonEmpty` (`executor_agent.go`)
- Produces:
  ```go
  type NodeBinding struct { NodeType, Model, Effort, Backend, Provider string }
  func resolveNodeBinding(ctx context.Context, r provider.ProfileResolver, node workflow.Node, defaultBackend string) NodeBinding
  type NoopProfileResolver struct{} // Resolve always returns provider.ErrUnknownProfile
  ```

- [ ] **Step 1: Write failing tests** with a fake resolver (map alias → `ResolvedBinding` / error):
  - `agent_call`, profile `{Model:"gpt-6.1-sol", Effort:"high", Backend:"codex", Provider:"litellm"}` → all five fields, `NodeType:"agent_call"`
  - backend precedence: node `Backend:"opencode"` beats profile `"codex"`; profile `"codex"` beats default `"claude-code"`; both empty → default
  - `llm_call` with profile Backend `"codex"` → `Backend:""`, Model/Effort/Provider set (same for `interactive_llm`)
  - `interactive_agent` behaves like `agent_call`
  - no `model_profile`, `Config.Model:"opus"` on `agent_call` → `Model:"opus"`, `Backend:` default
  - `TestResolveNodeBinding_UnknownProfile`: resolver returns `ErrUnknownProfile`, node `Config.Model:"m"` → `{NodeType:"agent_call", Model:"m", Backend:default}`, no panic
  - generic resolver error → same degradation
  - `script`, `transform`, `conditional`, `timer_delay`, `webhook_wait`, `tool_call`, `spawn_workflow` → only `NodeType`
  - `NoopProfileResolver` → degraded binding

- [ ] **Step 2: Run** `go test ./domain/engine/ -run ResolveNodeBinding` — Expected: FAIL.

- [ ] **Step 3: Implement** in `node_binding.go`; log resolver errors with `slog.Warn` (alias + error only). Effort is `string(binding.Effort)`.

- [ ] **Step 4: Run** tests — Expected: PASS.

- [ ] **Step 5: Commit** `feat(engine): resolve a node's display binding from its profile`

---

### Task 3: Enricher emits current-node binding + Git ref

**Files:**
- Modify: `domain/instance/instance.go:205-220` (add fields)
- Modify: `domain/engine/enricher.go` (resolver collaborator, binding, gitRef, comment fix; drop backend half of `nodeLabelAndBackend` → rename `nodeLabel(node) string`)
- Modify: `domain/instance/aggregate.go`, `adapter/sqlite/node_execution_repo.go:148-205` (remove `LatestModel`)
- Modify: `adapter/http/instance_handler.go:245-283` (DTO fields)
- Modify: `cmd/arbitron/main.go:826-841` (wire resolver)
- Test: `domain/engine/enricher_unit_test.go`, `adapter/sqlite/node_execution_repo_test.go`, `adapter/http/` enriched DTO test (existing file covering `toEnrichedSummaryDTO`)

**Interfaces:**
- Consumes: Task 1 `deriveGitRef`, `Workflow.GitVariable`; Task 2 `resolveNodeBinding`, `NoopProfileResolver`
- Produces: `EnrichedInstanceSummary` fields `Effort, Provider, NodeType, GitRef string`; `func (e *InstanceEnricher) SetProfileResolver(r provider.ProfileResolver)` (panics on nil, default `NoopProfileResolver{}`); JSON keys `effort`, `provider`, `nodeType`, `gitRef` (all `omitempty`)

- [ ] **Step 1: Write failing enricher tests:**
  - running row, current node `agent_call` with resolvable profile → Model/Effort/Backend/Provider/NodeType from profile
  - running row on a `script` node → `NodeType:"script"`, others empty
  - failed row → binding of `FailedNodeID` node
  - completed row with `CurrentNodeID` set → binding empty, `FriendlyStep` still the node label (unchanged behavior)
  - cancelled row → binding empty
  - `TestEnrich_QueuedRowEmptyBindingAndGit`: queued, nil current node, vars `{}` → binding empty, `GitRef:""`
  - GitRef: snapshot `"gitVariable":"pr_branch"` + vars `{pr_branch:"feat/a"}` → `"feat/a"`; completed row keeps final `branch`
  - resolver error degrades only binding; RunName/Submitter/RetryCount intact
  - `SetProfileResolver(nil)` panics

- [ ] **Step 2: Write failing adapter tests:** aggregate result has `MaxAttempt` and no model field (compile-level); HTTP DTO serializes the four new keys and omits them when empty.

- [ ] **Step 3: Run** `go test ./domain/engine/ ./adapter/sqlite/ ./adapter/http/` — Expected: FAIL.

- [ ] **Step 4: Implement.** Binding node selection: `failed` → `FailedNodeID`; `running|waiting|paused|queued` → `CurrentNodeID`; else none. Read `gitVariable` from `s.WorkflowSnapshot["gitVariable"]`. Fix the `relevantNodeID` comment to describe actual behavior. Remove the `latest_model` subquery. Wire `instanceEnricher.SetProfileResolver(profileResolver)` in `main.go`.

- [ ] **Step 5: Run** `gofmt -l ./domain ./adapter ./cmd` (empty) and `go test ./...` — Expected: PASS.

- [ ] **Step 6: Commit** `feat(enricher): show the current node's binding and git ref on queue rows`

---

### Task 4: `node.started` carries binding + gitRef

**Files:**
- Modify: `domain/engine/engine.go` (~line 912 publish, ~line 1001 failed publish keeps label only; add `profileResolver` field + `SetProfileResolver`)
- Modify: `cmd/arbitron/main.go` (near `eng.SetSpawnLinks`, wire `eng.SetProfileResolver(profileResolver)`)
- Test: `domain/engine/engine_node_started_binding_test.go`

**Interfaces:**
- Consumes: Task 1, Task 2
- Produces: `func (e *Engine) SetProfileResolver(r provider.ProfileResolver)` (nil panics, default Noop); `node.started` data keys: `nodeId`, `nodeType`, `nodeLabel`, `model`, `effort`, `backend`, `provider`, `gitRef`

- [ ] **Step 1: Write failing tests** running a two-node workflow (`transform` setting `branch:"feat/live"` → `agent_call` with fake profile) and capturing bus events:
  - first `node.started` (transform): `nodeType:"transform"`, `model:""`, `gitRef:""`
  - second `node.started` (agent): `model`, `effort`, `backend`, `provider` from profile; `gitRef:"feat/live"`
  - with no resolver wired: event still published, `model` from `Config.Model` or `""`

- [ ] **Step 2: Run** `go test ./domain/engine/ -run NodeStartedBinding` — Expected: FAIL.

- [ ] **Step 3: Implement** using `resolveNodeBinding` and `deriveGitRef(wf.GitVariable, vars.Snapshot())`.

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.

- [ ] **Step 5: Commit** `feat(engine): publish current binding and git ref on node.started`

---

### Task 5: MCP authoring guidance + pr-review sample

**Files:**
- Create: `adapter/mcp/authoring_guidance.go`
- Modify: `adapter/mcp/server.go:27-29` (`server.WithInstructions(AuthoringGuidance)`)
- Modify: `adapter/mcp/tools_workflow.go:64-78` (append `WorkflowManifestHint` to both descriptions)
- Modify: `samples/pr-review/workflow.json` (add `"gitVariable": "pr_branch"`)
- Test: `adapter/mcp/authoring_guidance_test.go`

**Interfaces:**
- Produces: `const AuthoringGuidance string`, `const WorkflowManifestHint string`

- [ ] **Step 1: Write failing tests:** `AuthoringGuidance` contains each of `repo`, `repo_url`, `base_branch`, `pr_number`, `slug`, `teams_webhook_url`, `branch`, `work_dir`, `gitVariable`, `model_profile`; the `workflow_create` and `workflow_update` tools registered on a `NewServerWithDeps` server have descriptions containing `"gitVariable"` and `"conventions"`; the server's initialize result carries the instructions. `samples` tests still pass with the new field (existing `samples/samples_test.go` validates manifests).

- [ ] **Step 2: Run** `go test ./adapter/mcp/ -run Authoring` — Expected: FAIL.

- [ ] **Step 3: Implement.** `AuthoringGuidance` text = the spec's conventions table rendered as plain lines, plus: prefer `branch` over bespoke names; set `gitVariable` when the branch lives under another name (dotted paths allowed); profiles are pinned per node via `model_profile`, there is no profile-input convention. `WorkflowManifestHint` = one sentence pointing to those conventions and `gitVariable`.

- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.

- [ ] **Step 5: Commit** `feat(mcp): workflow authoring guidance with standard variable conventions`

---

### Task 6: Dashboard clock + duration helpers

**Files:**
- Modify: `dashboard/src/utils/time.ts`
- Create: `dashboard/src/hooks/useNow.ts`
- Test: `dashboard/src/utils/time.test.ts`, `dashboard/src/hooks/useNow.test.ts`

**Interfaces:**
- Produces:
  ```ts
  export function elapsed(dateStr: string, now?: number): string
  export function relativeTime(dateStr: string, now?: number): string
  export function formatDuration(ms: number): string        // the existing elapsed formatting, factored out
  export function runDuration(row: { state: string; createdAt: string; completedAt?: string }, now: number): number
  export function useNow(intervalMs?: number): number       // default 1000
  ```

- [ ] **Step 1: Write failing tests:**
  - `elapsed('2026-10-10T00:00:00Z', Date.parse('2026-10-10T01:02:00Z'))` → `'1h 2m'`; `relativeTime` with injected now → `'5m ago'`
  - `runDuration`: running → `now − createdAt`; completed/failed/cancelled with `completedAt` → `completedAt − createdAt` regardless of `now`; terminal without `completedAt` → `now − createdAt`; `runDuration clamps negative to 0` (completedAt before createdAt, and unparseable dates → 0)
  - `useNow` (fake timers): advances by 1000 per tick; no ticks while `document.visibilityState === 'hidden'`; on `visibilitychange` to visible returns fresh `Date.now()` immediately; interval cleared on unmount

- [ ] **Step 2: Run** `cd dashboard && npx vitest run src/utils/time.test.ts src/hooks/useNow.test.ts` — Expected: FAIL.

- [ ] **Step 3: Implement.**

- [ ] **Step 4: Run** the same — Expected: PASS.

- [ ] **Step 5: Commit** `feat(dashboard): page clock and run-duration helpers`

---

### Task 7: Model and Git columns, ticking Elapsed

**Files:**
- Modify: `dashboard/src/api/types.ts:46-69` (add `effort?`, `provider?`, `nodeType?`, `gitRef?`; fix `model`/`backend` doc comments)
- Create: `dashboard/src/components/formatBinding.ts`
- Modify: `dashboard/src/components/queueColumns.ts`, `QueueTableRow.tsx`, `QueueTableRow.module.css`, `RowDetailPanel.tsx:39-46`, `InstanceRow.tsx:38`, `QueueSection.tsx` (thread `now`)
- Test: `formatBinding.test.ts`, `queueColumns.test.ts`, `QueueTableRow.test.tsx`, `RowDetailPanel.test.tsx`

**Interfaces:**
- Consumes: Task 6 helpers
- Produces: `export function formatBinding(row: Pick<EnrichedInstanceSummary,'state'|'model'|'effort'|'backend'|'provider'|'nodeType'>): string`; `QueueTableRow` / `QueueSection` prop `now: number`

- [ ] **Step 1: Write failing tests:**
  - `formatBinding`: `{model:'gpt-6.1-sol', effort:'high', backend:'codex', provider:'litellm'}` → `'gpt-6.1-sol (high) · codex · litellm'`; chat `{model:'claude-sonnet-5', effort:'medium', provider:'claude-cli'}` → `'claude-sonnet-5 (medium) · claude-cli'`; no effort → `'opus-5.5 · claude-code · claude-cli'`; `{nodeType:'script'}` → `'script'`; state `completed`/`cancelled` → `'—'`; all empty → `'—'`
  - `COLUMNS` keys order starts `['workflow','runName','git', ...]`; git `header:'Git'`, densities `['balanced','expanded']`, sortable; `modelBackend` header is `'Model'`; elapsed `sortAccessor` uses `runDuration`
  - `QueueTableRow`: elapsed cell for running row renders `formatDuration(now − createdAt)` and changes when `now` prop changes; completed row ignores `now`; git cell shows `gitRef` with `title` equal to it, `'—'` when absent
  - `RowDetailPanel` renders `formatBinding` output

- [ ] **Step 2: Run** `cd dashboard && npx vitest run src/components` — Expected: FAIL.

- [ ] **Step 3: Implement.** Elapsed column `sortAccessor` needs a clock: use `runDuration(r, Date.now())` (sort happens at click time). Git cell CSS: `max-width: 18rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap`. Keep the `title`-sink comment.

- [ ] **Step 4: Run** `npx tsc --noEmit && npx vitest run` — Expected: PASS.

- [ ] **Step 5: Commit** `feat(dashboard): Model and Git columns with live elapsed`

---

### Task 8: QueueView clock + full live patch, SPA rebuild

**Files:**
- Modify: `dashboard/src/routes/QueueView.tsx` (call `useNow()`, pass `now` to sections; widen `node.started` patch)
- Modify: `dashboard/src/api/types.ts:290-293` (`NodeEventData` gains `nodeType`, `model`, `effort`, `backend`, `provider`, `gitRef`)
- Modify: `adapter/dashboard/dist/**` (rebuilt)
- Test: `dashboard/src/routes/QueueView.test.tsx`

**Interfaces:**
- Consumes: Task 6 `useNow`, Task 7 `now` prop

- [ ] **Step 1: Write failing tests** (mock WebSocket + fake timers):
  - `node.started` with full payload patches step, binding cell (`'gpt-6.1-sol (high) · codex · litellm'`) and git cell in one render
  - `node.started without binding fields keeps prior values` — payload `{nodeId, nodeLabel}` only → binding and git cells unchanged
  - advancing fake timers 5000ms with no events changes the running row's Elapsed text
  - completed row's Elapsed text unchanged after advancing timers

- [ ] **Step 2: Run** `npx vitest run src/routes/QueueView.test.tsx` — Expected: FAIL.

- [ ] **Step 3: Implement.** Patch each field with `d.x ?? prev.x` (an explicit `""` from the server does overwrite — a script node legitimately clears model). Replace the "patch only the Step column" comment with the new rationale.

- [ ] **Step 4: Run** full gates: `cd dashboard && npx tsc --noEmit && npx vitest run`; `go test ./...` — Expected: PASS.

- [ ] **Step 5: Rebuild SPA:** `make ui` (fallback `cd dashboard && npm run build`). Expected: `adapter/dashboard/dist` updated; `go test ./adapter/dashboard/...` PASS.

- [ ] **Step 6: Commit** `feat(dashboard): live-patch binding and git ref; ticking queue clock`
