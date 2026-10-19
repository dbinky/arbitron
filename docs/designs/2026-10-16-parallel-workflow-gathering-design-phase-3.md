# Parallel Workflow Gathering — Design Phase 3: Working-Area VCS & File Gathering

Spec: `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md`
Phases: 1 — Definition/Validation/MCP · 2 — Execution core (results only) · **3 — Working-area VCS & file gathering** · 4 — Run visibility

## Goal

Gather only eligible file changes into the immediate parent's isolated working area,
automatically resolve what can be resolved, and suspend for interactive resolution of
remaining conflicts. Continue only after gathering succeeds or returns a durable failure.
Use Phase 2's common start snapshot for both forms and both gather modes. Results-only
tasks also start with the parent's files; gathering is the optional part.

## Existing capabilities and limits

`domain/engine/ports.go:WorkspaceManager` and
`adapter/filesystem/workspace.go:WorkspaceManagerAdapter` create/remove plain directories;
they provide no snapshot, fork, diff or merge capability. Workflow scripts may create Git
repositories (for example `samples/feature-pipeline/scripts/init-workspace.sh`), but that
sample is **not** a guaranteed input to arbitrary workflows.

`InteractiveExecutor` and `conversationHandler` provide streaming and persisted conversation
history in `NodeExecution.LLMConversation`. They accept only interactive node types and
normally write session output into the node's variables. They cannot directly own a
`parallel_group` resolution session without an extension.

## Design decisions

- Git is the proposed automatic three-way merge substrate, behind a new domain working-area
  port and adapter. The spec does not require an author-created Git repository. The port
  must support ordinary directories and files outside an optional declared repository, as
  well as existing repositories, with private backing metadata where needed. That backing
  strategy is **PLANNED DEPENDENCY P3-A**, implemented by the P3-T2/T3 plan with
  shared metadata isolation from P2-T17; it is not supplied by Git sample scripts.
  These tasks must implement it; do not silently narrow the feature
  to repository-only work.
- `repoPath`, if supplied, is a templated path within the immediate parent's workspace and
  an existing-repository hint; it is optional in both gather modes. It must not determine
  whether tasks receive starting files or exclude changes elsewhere in the working area.
  Author-owned repository metadata/history must remain coherent; metadata is not merged
  as ordinary task file content.
- All-succeed gathering uses author-defined task order after every task succeeds. A failed
  group merges nothing. First-success gathering uses only the winner. Independent tasks
  never write to the parent and never become eligible after the decision.
- Task-decision data freezes in Phase 2. Merge/resolution reports are added before first
  continuation, then the final gathered output freezes too. Live task status remains
  separately inspectable after continuation.

## Components

### 1. Working-area port and adapters

Define snapshot/fork/finalize/merge/rollback/cleanup operations under `domain/engine`,
implemented by filesystem and proposed `adapter/git` capabilities:

- Snapshot the full parent working area, including uncommitted and untracked task content,
  at the common launch boundary; fork isolated writable copies before any task is enqueued.
  Preserve layout and permissions and handle independent repository metadata correctly.
  Use the Phase 2 snapshot contract rather than taking one new baseline per task.
- Before a successful task terminal transition, finalize its working area into an immutable
  mergeable ref/tree, capturing edits whether or not the task committed them. Persist the
  task's ref before terminal notification and before normal workspace removal. An empty
  diff is valid. The gatherer consumes finalized refs, never a moving task branch name.
- Merge one eligible task delta relative to the common baseline into a staging gather tree.
  Report clean/conflicted and unmerged paths. Commit/record resolved trees, then apply the
  finished result to the parent's area. Preserve an exact pre-gather rollback snapshot
  (including dirty/untracked state), not just a branch HEAD.
- Persist operation identity, current merge index, staging refs, conflict paths, applied
  marker, and rollback handle. Reconciliation can resume after a crash between file and
  database operations without applying a delta twice or losing completed resolutions.
  Parent execution and resolution must have one writer for this area.
- Retain immutable refs outside task directories until no gather/recovery needs them.
  Prune only after terminal tasks and all dependent gathers/descendants release them.

`adapter/process/runner.go:run`, `commandSpec` and process-group helpers are package-private.
They demonstrate cancellation/timeout/output bounds but cannot be called by `adapter/git`.
An exported shared runner or equivalent adapter-local implementation is **P3-D**. Git host
availability must be checked at use and reported; no remote credential is needed for
local snapshot/merge operations.

### 2. Launcher and task-terminal lifecycle integration

Extend Phase 2 preparation to record backing snapshots/refs on the group-run record and
fork the whole area, with any declared repo at the same relative path inside each task's
workspace. Do not call today's `Engine.Spawn` and fork afterward: it already enqueues.

Add a terminal-finalization/retention hook **before** `Engine.run` calls `SetCompleted`,
`cleanupWorkspace`, and `OnChildTerminal`. Today's success path deletes a task's workspace
before notifying its parent, so reusing it unchanged loses the file input. The hook also
handles nested gathers and retained independent descendants. Snapshot/finalization failure
cannot publish a successful file-gather candidate without its required files; record an
error/non-success and reconcile resources. Failed/cancelled work is retained for inspection
but is never automatically eligible for partial merging.

### 3. Gatherer between `decided` and `done`

For `results_and_files`, read immutable Phase 2 decision data:

- Failed decision: report no tasks merged and route `failure` with available results.
- All-succeed success: every finalized task ref in author order.
- First-success success: the winner's finalized ref only, even while other tasks continue.

Persist `gathering` and merge progress. A clean merge proceeds; unresolved conflicts persist
`resolving_conflicts`, open a resolution session and return `IsWait`, releasing the parent
queue slot. Re-entry verifies the session and unmerged paths, records the resolved merge,
and continues. Still-conflicted completion stays waiting/reopens a fresh session; it does
not let downstream work run. Nested groups apply to their immediate parent's task copy,
whose finalized tree is later eligible for its own enclosing gather.

Before continuation, persist a report for every task (merged/not merged and reason),
conflicted paths, resolution attempts/outcomes, final success/failure and rollback status.
The final output's `outcome` follows the final route; `taskOutcome` and the stored task
decision retain the task-completion outcome. Apply changes successfully before marking
continuation ready. A merge failure rolls back
all already-applied changes from this gather and routes `failure` without partial work.

### 4. Interactive resolution bridge

Extract/reuse streaming, profile selection, sentinel protection, history persistence,
conversation guards, authorization and transport plumbing; add an explicit group-resolution
session kind/identity tied to `(groupRunID, mergeIndex, attempt)`. The group owns it, but
ordinary `InteractiveExecutor.Execute` short-circuiting must not treat an earlier group
wait trace or an earlier merge's session as completion of this session.

- Resolve the configured `resolverProfile` through `provider.ProfileResolver` and
  `InteractiveStreamSelector` with an agent-kind guard. The agent works in the staging
  resolution area associated with the parent, never in an independently running task.
- Load the embedded default prompt or workflow-relative override through scoped
  `FileReader.ReadFile` / `loadTemplate`; `workflow.Repository` has **no ReadFile method**.
  Supply persisted conflict paths, task key, baseline and merge refs, and the active area.
- Start with an autonomous attempt, then verify the actual staging index/content. If
  it is resolved, close/persist the attempt and continue automatically without operator
  end or a human-needed notification. Only remaining conflicts keep an interactive
  session open. Model claims/sentinels alone do not prove resolution. Reuse MCP
  `interactive_send_message` / `interactive_end_session`, WebSocket
  `interactive.send` / `interactive.end`, and HTTP interactive handlers, extending their
  session lookup/config gate to recognize this kind. Verify conflicts after completion.
- `conversation.go:findInteractiveNodeConfig` currently rejects a group;
  `completeSession` overwrites `vars[nodeID]` and unconditionally queues. Add group-specific
  completion dispatch: persist session completion and conditionally resume this waiting
  group without replacing its gathered output, bypassing readiness, or resurrecting a
  cancelled parent. Repeated end/sentinel callbacks must be idempotent.
- Ordinary `EndSession` means “finish and verify”, not “abandon”. Add an explicit durable
  abandon action usable through MCP/HTTP/UI; it aborts and rolls back the gather, records
  conflict details and routes `failure`. Parent cancellation aborts/rolls back and leaves
  the parent cancelled. Existing transports do not currently produce an abandon intent.
- Raise a conflict-resolution notification via `Engine.emitNotification` when human input
  is needed, using a new notification type; preserve histories and resolution links.

### 5. Cleanup and recovery

Do not prune tasks merely because a group reaches `done`: losers may still be stopping or
continuing independently, and terminal tasks may own active descendants. Release live
worktrees only after their writers stop; release pinned refs after gathering and recovery
are complete. Failed runs retain debugging content consistent with
`Engine.warnWorkspaceRetained`. Parent completion cleanup must not delete backing stores
needed by independent work. Startup reconciliation cleans interrupted launches, resumes or
rolls back interrupted gathers, and restores active resolution sessions from persisted
records. Completed runs retain outcomes/history even after worktrees are pruned.

## Data Provenance

| Leaf input | Consumer | Verified producer or explicit dependency |
|---|---|---|
| Mode, optional repo hint, profile alias, prompt override | Launcher / gatherer | **PLANNED DEPENDENCY P1-A** typed config; raw values are author input in `Node.Config` |
| Parent/task paths and live file bytes | Snapshot/finalizer | `domain/engine/workspace.go:Engine.prepareWorkspace` / `WorkspaceManager.EnsureInstanceDir`; files are written by existing `domain/engine/executor_agent.go:AgentCallExecutor.Execute`, `adapter/process/executor_script.go:ScriptExecutor.Execute`, and other task steps |
| Resolved optional path, identical starting boundary | Launcher | **PLANNED DEPENDENCY P2-B** snapshot/typed resolver, using `variable.Get` / `Resolve` and the parent's store; sample init scripts do not guarantee a repo |
| Full-area baselines, forks, immutable task refs including uncommitted edits, conflicts, staged result, rollback state | Gatherer / resolver | **PLANNED DEPENDENCY P3-A** working-area port/adapters, including non-repository/outside-repo support; no existing snapshot/diff/merge producer |
| Eligible tasks, ordered available results, winner, task linkage | Gatherer | **PLANNED DEPENDENCY P2-A** group-run repository/coordinator; Phase 2 defines the frozen contract |
| Final refs retained before task completion/cleanup; merge progress and reports | Gatherer / cleanup / Phase 4 | **PLANNED DEPENDENCY P3-B** terminal hook, resource retention and durable gather-state extension to **P2-A** |
| Resolver provider, model, tools | Resolution bridge | `adapter/provider/dynamic.go:DynamicProfileResolver.Resolve` implementing `domain/provider/provider.go:ProfileResolver.Resolve` and `domain/engine/ports.go:InteractiveStreamSelector.StreamProviderFor`; used by `executor_interactive.go:InteractiveExecutor.Execute` |
| Default prompt | Resolution bridge | **PLANNED DEPENDENCY P3-C** new embedded resolver asset, not currently shipped |
| Author prompt bytes | Resolution bridge | `domain/engine/ports.go:FileReader.ReadFile`, `domain/engine/template_include.go:loadTemplate`, `adapter/filesystem/file_reader.go:FileReaderAdapter.ReadFile` |
| Human messages / ordinary session end | Resolution bridge | Existing `domain/engine/conversation.go:conversationHandler.HandleMessage`, `EndSession`, reached by MCP/WS/HTTP; group-aware session gate/identity/resume and abandon intent are **PLANNED DEPENDENCY P3-C** |
| Conversation persistence / resolution session link | Gatherer / Phase 4 | Existing `domain/instance/instance.go:NodeExecution.LLMConversation`, `NodeExecutionRepository`; group-run/merge/attempt association and durable resolution outcomes are **P3-C** / **P3-B**, not an existing separate session table |
| Notifications | Operators | `domain/engine/engine.go:Engine.emitNotification`; new conflict type and invocation are **P3-C** |
| Cancellable bounded Git subprocess execution | Git adapter | **PLANNED DEPENDENCY P3-D**: `adapter/process/runner.go:run` is unexported; `adapter/procenv` provides environment helpers, not an exported runner |

## Integration and plan obligations

Implement P3-A–D and their P2 launcher/terminal/repository seams, then enable file mode.
Plans must settle the full-area backing/rollback strategy, dirty/untracked content,
repository metadata handling, crash boundaries and session ownership before claiming the
feature complete. No plan may resolve these dependencies by citing proposed interfaces as
existing producers. Use the next available migration; do not assume 019/020 is still free.

## Error handling

An invalid declared repo path, missing Git binary, unsupported snapshot content or locked
index produces actionable errors before launching tasks where possible. No declared repo
is valid and must not be rejected merely for being a plain directory. Post-launch failure
uses Phase 2's durable group failure/cancellation path. Merge/session errors abort and
restore pre-gather state; rollback failure remains visible and blocks continuation until
repaired. Parent cancellation never routes through success. Failed task groups never merge
partial work.

## Testing — STRICT TDD

Write/run each test to red, implement to green, then refactor. Use temporary real Git
repositories and plain directories for adapters, and fake ports for domain coordination.

- **Happy-path:** all-succeed merges every task in order; first-success merges only winner;
  both forms gather without requiring author commits or a pre-existing repo.
- **Success:** automatic clean/conflict resolution, interactive resolution then next merge,
  results-only copies files with no merge, nested gathering into immediate parent.
- **Failure:** failed groups merge nothing; explicit abandon rolls back earlier merges;
  still-conflicted end remains blocked; non-winning late writes never affect parent.
- **Error:** fork/finalization/merge/process failure, invalid declared path, missing Git,
  profile failure, parent cancel mid-session, rollback failure, unauthorized session action.
- **Edge-case:** empty diff, untracked/uncommitted parent and task edits, file deletion,
  rename/binary/mode changes, files outside repo hint, independent metadata for Git worktrees,
  task success cleanup cannot erase gather input, terminal ancestor retains independent
  descendants, multiple sequential conflict sessions, sentinel/end replay, restart at each
  merge/apply boundary, immutable final report, cleanup only after writers and pins release.

## Plan dependency closure

The [producer/wiring/test map](../plans/2026-10-16-parallel-workflow-gathering-implementation-alignment.md) closes
the design IDs above with concrete tasks and orders the nonnumeric prerequisites.
These are planned capabilities, not claims that implementation already exists.
