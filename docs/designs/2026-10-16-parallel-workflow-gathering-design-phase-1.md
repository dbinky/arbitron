# Parallel Workflow Gathering — Design Phase 1: Group Definition, Validation & MCP Authoring

Spec: `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md`
Phases: **1 — Definition/Validation/MCP** · 2 — Execution core (results only) · 3 — Working-area VCS & file gathering · 4 — Run visibility

## Goal

Add `parallel_group` definitions and actionable validation, and let authors create,
inspect, and revise both forms, including nesting, entirely through MCP. This phase
supports authoring only: a trigger guard refuses group-bearing workflows until Phase 2
registers the results-only executor. Phase 2 retains a file-gathering guard until Phase 3.

## Design decisions and shared contract

- One `parallel_group` node represents fan-out and gather. `form` selects `branches` or
  `workflows`; every task executes as a separate child instance in Phase 2.
- The manifest remains flat. A branch task names an entry node in a closed, exclusively
  owned body. Nested group bodies belong to their enclosing task. Phase 4 renders boxes
  without adding a visual workflow editor.
- Tasks are fixed and ordered at authoring time. Each has a unique, nonempty stable key,
  a branch entry or child workflow name, and an optional deterministic templated input
  map. Runtime lists and quorum rules are outside scope.
- Both forms start with independent deep copies of the same group-start variables and
  working area. Task input overrides apply to the task's copy; `workspace` is reseeded
  by the engine. Results are explicit node outputs, never an automatic variable merge.
- `parallel_group` cannot be on a cycle because re-entry reuses a persisted group run.
  This restriction does not ban loops elsewhere or change existing spawn behavior.
  A group **may be the workflow entrypoint**; no setup node is required for results only.

## Components

### 1. Definition model — `domain/workflow`

Extend `domain/workflow/workflow.go:NodeConfig` and the node type set with
`NodeTypeParallelGroup`. Add a typed `ParseGroupConfig`, following the existing
`ParseSpawnConfig` error-collection pattern. Proposed fields:

| Setting | Values / contract |
|---|---|
| `form` | `branches` or `workflows` |
| `tasks` | Nonempty ordered list; unique task key; branch-entry ID or child workflow name; typed input map |
| `completionRule` | `all_succeed` or `first_success` |
| `unfinishedBehavior` | All succeed: `wait_for_remaining` or `cancel_unfinished`; first success: `cancel_unfinished` or `finish_independently` |
| `gatherMode` | `results` or `results_and_files` |
| `repoPath` | Optional existing-repository hint for either mode; templated path within the immediate parent's workspace |
| `resolverProfile` | Required agent-kind profile for `results_and_files` |
| Resolver prompt override | Optional workflow-relative file; otherwise Phase 3's embedded default |

New constants `GroupPortTask`, `GroupPortSuccess`, `GroupPortFailure` name ports
`task`, `success`, `failure`. Branch groups have one `task` edge per task,
matched by its unique entry target; workflow groups have none. Each group has exactly
one `success` and one `failure` continuation and no other outgoing ports. Entries must
be distinct, so repeated `task` output names never require arbitrary first-edge selection.
Only the group launcher traverses `task` edges; the parent's normal cursor routes through
`success` or `failure` (Phase 2).

### 2. Validation — `domain/workflow/validation.go`

Add config checks through `validateNodeConfigs` and a shared graph ownership pass used
by validation, structured inspection, the branch launcher, and Phase 4 layout:

- Reject missing/unknown enums, empty task lists, duplicate keys/entries, mixed task forms,
  missing nodes, extra/missing task edges, missing continuation ports, reserved input
  overrides, and incompatible completion/unfinished choices.
- Compute each scope's ordinary control-flow region by following continuation edges
  while treating nested groups atomically (do not follow their `task` edges). Compute
  branch bodies recursively from their entries. Reject overlap with the enclosing main
  path, sibling bodies, or any enclosing group node. Only the owning group's declared
  task edge may enter a body from outside; edges cannot escape to a continuation or
  sibling. Nested groups' success/failure paths remain in their enclosing body; their
  own task bodies are owned recursively. Bodies end at sinks. Defining a body simply as
  “everything reachable” is insufficient: it would absorb an escaped main-path node.
- Use `domain/workflow/lint.go:nodeOnCycle` / `buildAdjacency` to reject a group on a
  cycle. Preserve ordinary branch loops and current non-group loop semantics.
- Workflow task references must be visible to the caller. `WithWorkflowChecker` can
  answer existence only; it **cannot supply declared inputs**. Load the child contract
  through a scoped read capability (P1-B below). Validate explicit task input keys and
  statically known types. A required child input must be explicitly mapped or present in
  the group-start variables at runtime; optional missing inputs follow declared defaults;
  where static analysis cannot prove availability, return an actionable runtime-check
  advisory rather than rejecting valid inherited-variable use. Phase 2 rechecks values.
- Validate `repoPath` syntax/containment where statically knowable; runtime checks reject
  paths escaping the immediate parent's workspace. File gathering requires agent-kind
  `resolverProfile` and an existing override file when specified. Extend the existing
  profile/file validation passes; they do not already inspect these new fields.
- Connectivity still follows all graph edges, including `task` edges. Ownership, rather
  than reachability alone, prevents the parent from executing a branch body.

Return stable `ValidationError` codes with node/task identification and a corrective
message, collecting all errors. Exact code names and JSON tags are plan details.

### 3. Service operations and MCP

New `Service` operations upsert/get/delete a group behind existing
`domain/workflow/service.go:Service.Load`, `Service.Update`, and `adminSharedWrite`
authorization. Get is read-only; mutations load → mutate → validate → persist.

- Upsert accepts all config, task order/inputs, and continuation targets and atomically
  rewrites the group and its edges. Branch nodes can be authored through existing MCP
  `workflow_create` / `workflow_update` manifests, with `workflow_write_file` for scripts
  and prompts. A single full-manifest write can introduce bodies and groups together;
  no invalid intermediate save or manual file edit is necessary.
- Get returns ordered tasks, config, continuation targets, computed body node IDs, and
  nested groups. Delete removes the group and its incident edges, listing retained body
  nodes for reconnection/removal; reject a deletion leaving another group invalid.
- New tools `workflow_group_upsert`, `workflow_group_get`, `workflow_group_delete` in
  `adapter/mcp/tools_workflow.go:RegisterWorkflowTools` delegate to the service. Group
  validation failures use `workflow_validate`'s structured errors shape.
- `Service.Create` / `Update` and the group tools reject group-specific errors before
  registry/folder writes; existing non-group write behavior stays unchanged. Put required
  checker dependencies at the service boundary, not only in MCP. A missing checker or
  failed contract lookup must be reported, not silently skipped for a group write.
- Extend `adapter/mcp/authoring_guidance.go:WorkflowManifestHint` with forms, nesting,
  task order/inputs, rule/behavior choices, ports, gathering prerequisites, and examples.
  `workflow_validate` must receive the same scoped group checkers as write paths.

### 4. Staged execution guard

Phase 1 refuses a group-bearing snapshot before instance creation/enqueue through
`domain/engine/engine.go:Engine.TriggerWithDepartment` and any new snapshot trigger path.
Phase 2 enables `results` and continues to reject `results_and_files` until Phase 3 is
wired. Never run a files-requesting group silently as results only. Dashboard type support
must at least tolerate the new type before Phase 4's full rendering.

## Data Provenance

The IDs below name new capabilities, not existing producers. Their implementation,
wiring and production-input tests are now assigned in the
[plan dependency closure](../plans/2026-10-16-parallel-workflow-gathering-implementation-alignment.md).
A planned producer remains required work; a phase reference alone does not resolve it.

| Leaf input | Consumer | Verified producer or explicit dependency |
|---|---|---|
| Author-supplied config, task keys/order, input literals/templates, continuation and entry IDs | Service / parser | Existing `adapter/mcp/tools_workflow.go:workflowCreateHandler`, `workflowUpdateHandler` accept manifests; new focused tools/config parser: **PLANNED DEPENDENCY P1-A** |
| Existing manifest, graph, child `Workflow.Inputs` | Service / validator | `domain/workflow/service.go:Service.Load` → scoped `domain/workflow/ports.go:Repository.Load`; `adapter/filesystem/workflow_repo.go:WorkflowRepo.Load` |
| Caller identity and mutation scope | Service | `domain/workflow/service.go:caller`, `Service.Update`, `adminSharedWrite` |
| Visible child names | Validator | `domain/workflow/service.go:Service.VisibleNames` → `NameSetChecker`; `validation.go:WithWorkflowChecker` is name-only |
| Scoped child contracts inside validation and group checkers on write | Validator / service | **PLANNED DEPENDENCY P1-B**: add a scoped child-input reader and service wiring. `domain/engine/engine_spawn.go:Engine.ChildInputs` demonstrates visible runtime loading but is not currently injected into workflow validation |
| Profile kind and prompt-file existence | Validator | `adapter/provider/checker.go:ResolverChecker.ProfileKind` → `adapter/provider/dynamic.go:DynamicProfileResolver.Kind`; `adapter/filesystem/file_reader.go:FileReaderAdapter.FileExists` via `domain/workflow/validation.go:FileChecker`; existing owner binding in `adapter/mcp/tools_workflow.go:ownerBoundChecker`; new resolver-field checks/wiring belong to **P1-B** |
| Child input names/types/defaults/required flags | Validator | `domain/workflow/workflow.go:Workflow.Inputs`, `InputField`, loaded through `Service.Load`; runtime group-start values come from Phase 2 dependency **P2-B** |
| Cycle membership | Validator | `domain/workflow/lint.go:buildAdjacency`, `nodeOnCycle` |
| Typed group config, exclusive body ownership map, structured group view | Later phases | **PLANNED DEPENDENCY P1-A**: `ParseGroupConfig` and ownership analysis do not exist yet; implement together with group operations/tools |
| Trigger capability check | Trigger | **PLANNED DEPENDENCY P1-C**: new guard in `Engine.TriggerWithDepartment`, retained for file mode until Phase 3 |

## Integration and plan obligations

P1-A supplies the same typed config and ownership analysis to Phases 2–4. P1-B supplies
mandatory scoped validation on MCP and service writes. P1-C prevents staged execution
from promising unsupported semantics. Preserve these dependencies until implemented;
checkers being optional for older nodes is not evidence group inputs have a producer.

## Error handling

Repository and authorization failures follow existing `mapLifecycleError` conventions.
Group errors block writes without partial persistence. Read-only get never persists.
Unavailable contract/profile/file checks produce actionable feedback. Non-group workflows
retain existing write behavior and existing single-child/fire-and-forget definitions.

## Spec coverage across phases

| Spec requirement / outcome | Owning phase(s) |
|---|---|
| Fixed ordered branch/workflow tasks; repeated workflows; nesting; all settings through MCP | 1 definition/authoring, 2 execution, 4 relationships |
| Independent starting variables and working areas; existing execution limits | 2 snapshot/launcher/queue, 3 backing integration |
| All-succeed wait/cancel failure behavior and available results | 2 coordinator and both-port output |
| First-success winner, individual failures, cancel/independent behavior | 2 durable terminal order and decision |
| Ordered origins/results, completed/unfinished/cancelled distinction, explicit reconciliation | 2 frozen outcome; 4 separate live inspection |
| Exactly one continuation; no late result or file mutation | 2 claims/freeze, 3 eligibility/readiness, 4 live-versus-frozen display |
| Optional all-success or winner-only file gathering; no failed partial work | 3 full-area gatherer |
| Automatic resolution, interactive pause/resume, immediate-parent nested gathering | 3 working-area/session bridge |
| Workflow fan-out/gathers/nested child relationships | 4 static graph and task cards |
| Queue live view: every active node/state, gather dependencies, independent work | 4 aggregate read/refresh/queue integration |
| Completed outcomes, gathered results and merge-resolution history | 2/3 durable records, 4 inspection |
| Existing single-child awaiting/results, fire-and-forget, full parent cancellation | 2 compatibility and descendant traversal; 3 gather cancellation |

## Testing — STRICT TDD

For each unit, write and run a failing test first, implement the minimum change, run it
to green, then refactor while keeping tests green. Cover all five categories:

- **Happy-path:** create both forms through MCP, inspect in author order, revise all
  settings, and validate without manual definition edits.
- **Success:** nested groups, repeated child workflow with distinct inputs, atomic manifest
  creation of bodies plus groups, deletion with retained-node feedback, entrypoint group.
- **Failure:** every config/port/ownership error; external incoming body edge; escaped
  continuation absorbed by naive reachability; sibling overlap; group cycle; wrong profile
  kind; unknown explicit input; invalid literal type; reserved override; escaping repo path.
- **Error:** failed/invisible child lookup, missing checker wiring, repository failures,
  unauthorized get/write, and rejected writes leaving registry and folder unchanged.
- **Edge-case:** one task/sink, deep nesting, inherited required input checked at runtime,
  unique entries with identical task port names, legacy non-group write compatibility,
  Phase 1 trigger refusal and Phase 2 file-mode refusal until Phase 3.

## Plan dependency closure

The [producer/wiring/test map](../plans/2026-10-16-parallel-workflow-gathering-implementation-alignment.md) closes
the design IDs above with concrete tasks and orders the nonnumeric prerequisites.
These are planned capabilities, not claims that implementation already exists.
