# Remote Runners — Design Phase 3: Workspace mobility and recovery

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md`
**Idea doc (context only):** `docs/ideas/remote-runners.md`
**Evidence:** `docs/evidence/remote-runners/research-transport.md` §5.2–5.3
**Depends on:** Phase 1 (ledger and fence, placement, dispatcher, `ProcessRunner`, execution status, event log) and Phase 2 (hub, liveness, remote `StepRunner`, telemetry window, keys).
**Cross-phase decisions:** see Phase 1 §0. This phase realises D2 (immutable server checkpoints, same execution root), D7 (restart = built-in runner loss), D9 (script bundles), D10 (stay-versus-move tiers) and drain.

## 1. Purpose and scope

Let workspace steps (`agent_call`, `script`, `tool_call`) run on remote runners, keep every completed step recoverable elsewhere, and recover automatically from runner loss and server restarts. Operators get drain.

In scope:
- Workspace homes and the stay-versus-move tiers.
- Manifest-based pull and push of changed files over chunked transfer endpoints.
- Script bundles and remote process execution.
- Lost-runner failover from the checkpoint.
- Fencing and deletion of stale copies.
- Server-restart recovery: re-attach remote jobs, repeat built-in ones.
- Drain and cordon semantics with progress.
- Workspace removal on runners.
- The moving-workspace and recovering execution statuses.

Out of scope: interactive session moves (Phase 4; interactive steps stay pinned to their runner, and drain waits for them), and dashboard (Phase 5).

## 2. Existing architecture this phase extends

| Seam | Location | How this phase uses it |
|---|---|---|
| Workspace manager | `domain/engine/ports.go:WorkspaceManager` (:27); `adapter/filesystem/workspace.go` (:59) creates `<root>/instances/<uuid>` | The existing directory is mutable; this phase adds a separate immutable checkpoint of record. `InstanceDirStats` provides the size for the move cap. `RemoveInstanceDir` also triggers deletion on runners. |
| `{{workspace}}` | `domain/engine/workspace.go:prepareWorkspace` (:17); `conventions.go:WorkDirFromVars` (:30) | Unchanged value; identical on every runner (D2). |
| Group workspaces | `domain/engine/group_workspace.go:GroupWorkspace` (:26), `working_area.go:WorkingArea`; adapters `adapter/filesystem/group_workspace.go`, `adapter/git/working_area.go` | Their public behavior is preserved. Group snapshot/fork/finalize/gather mutations must acquire the same workspace ownership fence and commit checkpoints before remote placement resumes; server filesystem writes cannot bypass checkpoint generations. |
| Group launcher | `domain/engine/group_launcher.go` (`EnsureInstanceDir` for each branch at :294) | Each branch is a run with its own independent home and placement. |
| Script resolution | `adapter/process/executor_script.go:ScriptExecutor` (resolves via `FileReader.ResolvePath`; cwd = script folder) | The resolved folder becomes the bundle source. |
| Env construction | `adapter/process/env.go:buildEnv` (:16), `cfg.Script.EnvPassthrough` | On a remote runner, passthrough values come from the runner's own env. |
| Crash recovery | `domain/engine/engine.go:RecoverCrashedInstancesContext` (:2197), called from `cmd/arbitron/model_dependencies.go:startRuntime` (:82) | Replaced by pool-aware recovery (no blanket `SetFailed`). |
| Run re-entry | `Engine.StartInstance` (:1016) / `run()` (:1040) starting from `CurrentNodeID` | Recovery restarts the run goroutine at its current step. The dispatcher adopts or repeats the job. |
| Server read timeout | `cmd/arbitron/main.go` (:1690) `ReadTimeout: 10s` | Transfer handlers lift it per request through `http.ResponseController`. |
| Phase 1/2 seams | Ledger fence, placement function, dispatcher idempotency key, hub reconcile, remote `StepRunner`, telemetry window | Extended, not duplicated. |

## 3. Components

### 3.1 Workspace homes (`domain/runner`, ledger extension)
- **Job:** record, per run, the runner that currently holds a working copy of the workspace (its **home**) and a home generation. With no home, only the committed server checkpoint exists. The built-in runner has a mutable working copy and a separate immutable committed checkpoint, like remote runners.
- **Lifecycle:** each handoff bumps the generation and records a move entry (from, to, reason, bytes, time), which is the workspace's history of homes. It appends `workspace.moved` to the pool event log.
- **Read surface:** the home history is exposed on the instance and trace reads (MCP `trace_get` and the HTTP instance endpoints), so Phase 5 can render it.
- **Exactly one writer:** acquire a durable resource lease before executing, moving, restoring, gathering or deleting a workspace. Handoff fences the old writer before the target executes; job, workspace and session generations are checked together. A suspect runner is quarantined from new work but retains ownership until loss; no early handoff during the liveness grace.

### 3.2 Stay-versus-move placement (extends Phase 1 placement)
- **Job:** for workspace steps, choose between the current home and the alternatives using the approved tiers (D10):
  1. **Must move:** the home is ineligible. That covers confirmed source exhaustion/logout, draining, lost, incompatible or confirmed insufficient disk. Cordon blocks new assignments, not active steps; a suspect home receives no new assignments and waits for reconnection or loss before any move. An idle, safely fenced home may be released on cordon, but interactive pins follow Phase 4's stricter rules.
  2. **Should move:** the home has no free slot and another eligible runner has one.
  3. **May move for load:** the home's health is degraded (memory pressure critical, or thermal state serious or worse), or the slot-utilisation gap is at least the configurable threshold (default 50 percentage points). In both cases the workspace must also be under the configurable size cap.
  4. **Otherwise stay.**
- **Process steps:** a `script` or `tool_call` has no model group and goes to the home whenever it is eligible with a free slot; otherwise it follows tiers 1 and 2.
- **Reason:** each decision records its tier and inputs as the placement reason.

### 3.3 Workspace sync (`domain/runner` service, `adapter/http` transfer endpoints, runner adapter)
- **Job:** keep the committed server checkpoint authoritative and move bytes off the control channel.
  - **Manifest:** a path, size and hash listing of a workspace, computed from the immutable committed snapshot and keyed by checkpoint content/version, not merely home generation (several successful steps may share a home). Include deletions, file modes, symlinks and required Git metadata; reject unsupported file types explicitly. External linked-worktree metadata must become self-contained, never copied as dangling server paths (RR-08).
  - **Pull:** before a workspace step on a remote runner, the runner compares its copy (if any) with the server manifest and fetches only the differing files. It reuses its copy only when home generation AND committed checkpoint version match and no interrupted/failed attempt dirtied it; otherwise restore the committed snapshot first.
  - **Push:**
    - After a workspace-changing step, the runner sends changed files and deletions.
    - The server stages and hash-verifies a full next checkpoint, then publishes its immutable reference under the resource/job fence. Only the committed reference changes atomically; a multi-file in-place copy is not atomic. Server consumers restore/read the published version at safe boundaries.
    - **Only then** does the dispatcher mark the step complete.
  - **Transport:**
    - Chunked parts of about 64 MB, resumable. Each part is hash-verified.
    - Separate HTTP requests under `/api/v1/runners/transfers/*`, authenticated by the runner key plus a short-lived transfer token scoped to the job and generation.
    - Per-request read deadlines. Coordination messages ride the Phase 2 WebSocket.
  - **Progress:** published as the moving-workspace execution status.
- **Built-in runner:** executes at the existing path but snapshots into the separate checkpoint store after success. On interruption, stop/fence the old process tree and restore the last committed checkpoint before recovery. Built-in partial edits cannot mutate the checkpoint of record. The same rule applies to failed pushes and exhaustion on a remote home.
- **Completion boundary:** retain the previous checkpoint until a durable commit links the result, checkpoint reference, execution identity and next engine continuation/variables. Restart consumes a committed result without executing again; an uncommitted attempt restores its prior checkpoint. The cross-store commit/replay protocol is **UNRESOLVED DEPENDENCY RR-10**, not supplied by today's `NodeExecution` writes.

### 3.4 Script bundles and remote process steps
- **Job:** ship what a remote process step needs, and nothing more.
  - **Script:** the server packs the resolved script directory (the workflow folder or the `@lib` scope folder; live-read at dispatch, as today) into a bundle addressed by its content hash. The runner fetches it through the transfer endpoints, caches it by hash, and runs the script with the unpacked folder as its working directory. Stdin still carries the variable snapshot.
  - **tool_call:** sends the rendered command and args, resolved node env, passthrough names and the workspace reference. Set cwd to that workspace on either runner; inheriting the remote daemon cwd cannot execute workspace tools correctly. This intentionally corrects today's unset `commandSpec.Dir` to meet the spec.
  - **Env:** resolve node env templates on the server; send only those explicit values and passthrough names. Build the base allowlist and read passthrough values on the executing runner using the `buildEnv` policy, including collision rejection. Do not build/send the server's final env (it contains its HOME/PATH/passthrough values).
- **Script cwd compatibility:** bundled scripts keep their script-directory cwd and receive the canonical workspace through stdin/templates, as today; their workspace ownership and sync are identical to other workspace steps. Bundle an explicit relative script entrypoint; never run the original absolute server script path. Referenced sibling files are included; outside-bundle/server paths remain RR-09.
- **Command availability:** the runner must verify the requested interpreter/command and required local capabilities before accepting process work; a server binary path is not evidence it exists remotely (**UNRESOLVED DEPENDENCY RR-09**).
- **Runner side:** the Phase 2 job executor gains process and agent jobs. They use the same `ProcessRunner` and harness adapters linked into the binary.

### 3.5 Recovery (`domain/runner` with the engine)
- **Adoption:** the dispatcher's durable invocation/operation/retry identity (Phase 1 §3.5) means that re-entering a step that already has a live job **adopts** it (waits for its result) instead of dispatching again.
- **Lost runner:** for each job on the runner, the dispatcher re-places the step with generation + 1, and the new home pulls the **server checkpoint**, which is clean as of the last completed step. Completed steps are preserved, and only the interrupted step repeats. This does not consume `retries`. With no eligible runner, the step waits · capacity.
- **Server restart** (replaces `RecoverCrashedInstancesContext`'s blanket `SetFailed`):
  - Remote job leases are extended by a configurable restart grace.
  - Every `running` run re-enters its current step through `StartInstance`, which leads to one of three outcomes:
    - **Remote job:** adopted when its runner reconnects within the grace. Otherwise the runner is lost and the step is re-placed.
    - **Built-in job:** if uncommitted, stop/fence any surviving subprocess, restore the last committed checkpoint and repeat the interrupted operation (D7). Never repeat a committed operation just because engine continuation had not yet been persisted.
    - **No live job:** replay any committed result/continuation before creating an invocation; otherwise continue the uncommitted operation. `CurrentNodeID` alone cannot distinguish these cases.
- **Execution status:** recovering is shown while a step waits for re-attach or re-placement. Events `job.recovered` and `job.adopted` are appended to the pool event log.

### 3.6 Fencing and stale copies
- **Job:** a runner that reconnects holding stale generations is told to stop the job (`fenced`) and delete its copy. Its pushes and results are rejected by the single ledger fence.
- **Deletion:** after a planned move, the old home is told to delete its copy once the new generation is recorded.

### 3.7 Drain and cordon (`domain/runner` service; MCP and REST)
- **Cordon (Phase 1):** stops new placements.
- **Drain (new; admin only, audited `runner.drain`):**
  1. Stop new placements.
  2. Let active steps finish.
  3. **Move/release** homed workspaces at committed boundaries. Persist a fenced handoff to an eligible target and its pull, then delete the drained copy. If no target is eligible, retain a visible blocked drain item naming missing capacity; merely releasing to server storage must not report a successful drain while affected work cannot continue.
  4. **Wait** for interactive sessions; Phase 4 moves them at turn boundaries.
- **Progress:** remaining active steps, homed workspaces and sessions, plus any capacity preventing completion (for example, no other runner for the model group of a pending session).
- **Interfaces:** `runner_drain` over MCP and REST. `runner_get` includes drain progress.
- **Key revocation (Phase 2):** now also releases homes and recovers workspace steps through §3.5.

### 3.8 Workspace removal on runners
- **Job:** when a run's workspace is removed (`RemoveInstanceDir` on completion or cleanup), or its home moves, the hub tells the home runner, and any runner known to hold a fenced copy, to delete it. A runner that is offline is told on reconnect, through `hello` reconciliation.

## 4. Data flow

**A workspace step that moves:**
1. Placement applies the tiers and chooses runner R (reason recorded).
2. The ledger records the job (generation g) and a pending home handoff (home generation h + 1), fencing the old owner before target execution, and publishes moving workspace.
3. R pulls the manifest difference, then runs the step.
4. R pushes the changed files. The server stages and verifies an immutable checkpoint; it is not yet published as successful progress.
5. The executor validates output; the engine completion hook commits result/checkpoint/continuation and the trace. Only then is the step complete.
6. The old home is told to delete its copy.

**Steps that stay** skip the pull only if the copy matches the committed checkpoint and is not dirty from an interrupted attempt; every success still commits a new checkpoint.

**Lost runner:** heartbeats stop, the runner goes suspect and then lost, and the dispatcher re-places the step (g + 1). The new home pulls the server checkpoint, and the step repeats. A late result from the old runner is fenced and discarded.

**Server restart:**
1. Leases are extended.
2. Runs re-enter their current step.
3. Each step is adopted when its runner's `hello` matches the ledger, or repeated (built-in, or a runner lost after the grace).

## 5. Data Provenance

Repository verification distinguishes existing producers from required implementation. **UNRESOLVED DEPENDENCY** means the named value/capability has no implemented producer or verified source yet; a proposed component, earlier phase or research observation does not resolve it. The RR identifiers group cross-phase plan tasks; every occurrence remains open until implementation/source verification. Upstream unresolved inputs propagate to their consumers.

| Leaf input | Consumer | Verified producer or explicit dependency |
|---|---|---|
| Canonical instance execution path | Workspace dispatch | `domain/engine/workspace.go:Engine.prepareWorkspace` persists `workspace`; `adapter/filesystem/workspace.go:WorkspaceManagerAdapter.EnsureInstanceDir` creates the mutable directory. It is not an immutable checkpoint. |
| Workspace size/file count | Optional load-move cap | `adapter/filesystem/workspace.go:WorkspaceManagerAdapter.InstanceDirStats`; **UNRESOLVED DEPENDENCY RR-08:** measure the committed snapshot version for remote homes rather than a stale/dirty built-in working copy. |
| Committed checkpoint, manifest/version/hash, file content/deletions/modes/symlinks/Git metadata | Restore/pull/push, gather | **UNRESOLVED DEPENDENCY RR-08:** no immutable workspace checkpoint, manifest builder, atomic publication or self-contained Git transfer producer exists. `adapter/git/working_area.go:WorkingArea.SnapshotParent` is a group snapshot precedent, not the runner checkpoint service. Add safe transfer and restoration for built-in and remote copies. |
| Home/resource identity, lease/generation, ownership and move history | Single writer, placement, deletion | **UNRESOLVED DEPENDENCY RR-08:** implement home/resource lease and history stores extending Phase 1 ledger (RR-01); existing per-run workspaces do not provide remote ownership fencing. |
| Current machine health/free disk/load, sample age | Move tiers | **UNRESOLVED DEPENDENCY RR-06:** Phase 2 telemetry/window does not exist; retain unknown/unavailable values rather than inventing degraded or healthy state. |
| Slots, source health, exact-model eligibility and runner state | Move tiers/recovery | **UNRESOLVED DEPENDENCY RR-01/RR-02/RR-03/RR-05:** upstream ledger/inventory/health/hub producers must be implemented before mobility uses them. Suspect timestamps do not authorize immediate ownership transfer. |
| Move threshold (50 pp), size cap, disk headroom bound, restart grace, chunk size/deadlines/staging retention | Mobility policy | **UNRESOLVED DEPENDENCY RR-04:** new validated server config; defaults and unknown-metric behavior must be explicit. |
| Resolved script path/entrypoint, sibling directory and scope | Bundle builder | `adapter/process/executor_script.go:ScriptExecutor.Execute` calls `FileReader.ResolvePath`; concrete `adapter/filesystem/file_reader.go:FileReaderAdapter.ResolvePath`. **UNRESOLVED DEPENDENCY RR-09:** content-addressed bundling, relative-entrypoint rewriting and outside-bundle references are not implemented. Preserve owner-specific `@lib` resolution. |
| Variables/script stdin, rendered args and node env | Remote process request | `domain/variable/variable.go:VariableStore.Snapshot` and `Resolve`; `adapter/process/executor_script.go:ScriptExecutor.Execute`, `adapter/process/executor_toolcall.go:ToolCallExecutor.Execute`. No stdin is added to tool_call. |
| Passthrough names, controlled base env and collision policy | Process env | `domain/config/config.go:ScriptConfig.EnvPassthrough`; `adapter/process/env.go:buildEnv`; `adapter/procenv/procenv.go:BaseEnv`. **UNRESOLVED DEPENDENCY RR-09:** split descriptor construction from execution so base/passthrough values are read locally on the executing runner; operator-provided runner env is not a server snapshot. |
| Running runs/current node and persisted variables | Startup recovery | `domain/instance/ports.go:InstanceRepository.ListRunning`; `domain/engine/engine.go:Engine.RecoverCrashedInstancesContext`. Existing recovery marks runs failed and supplies no durable result replay. |
| Durable invocation/retry identity, terminal result/checkpoint and engine continuation | Adopt/replay without repeating completed work | **UNRESOLVED DEPENDENCY RR-10:** implement cross-store commit/replay and propagate persistence failures. `Engine.executeWithRetry` ignores `NodeExecution.Create` errors today; `CurrentNodeID` alone cannot prove whether a dispatched operation committed. |
| Live/terminal remote job reports and leases at boot | Adoption/fencing | **UNRESOLVED DEPENDENCY RR-01/RR-05:** Phase 1 ledger plus Phase 2 held/terminal-job reconciliation, retained results and output cursors, with startup grace. |
| Parallel branch/child workspace identity, gather/server mutations | Independent placement and safe boundaries | `domain/engine/group_launcher.go:GroupLauncher` and `domain/engine/group_workspace.go:GroupWorkspace`; `domain/engine/working_area.go:WorkingArea`. **UNRESOLVED DEPENDENCY RR-08/RR-11:** integrate their actual mutation/finalization call sites into checkpoint/resource ownership, including group staging for Phase 4. |
| Transfer/session tokens, content-addressed bundles/cache, progress and deletion inventory | Transfer endpoints/drain/cleanup | **UNRESOLVED DEPENDENCY RR-08/RR-09:** issuer, scoped authenticated handlers, resumable transfer state and runner-copy/tombstone inventory do not exist. Deletion needs a resource-generation fence and must not delete a newer copy. |
| Archive compression | Transfers | **UNRESOLVED DEPENDENCY RR-15:** no zstd dependency exists in `go.mod`; plans must vet/add one or explicitly select standard-library gzip, with round-trip fixtures. This previously unresolved choice remains open. |
| Drain state, blockers and run-visible home history | Operator APIs, Phase 5 | **UNRESOLVED DEPENDENCY RR-08/RR-16:** implement audited admin service and persistent progress/history/read projections; no current producer exists. |

### Deferred to plans
- **Composition-root wiring:**
  - Construct the workspace sync service, bundle builder and transfer endpoints.
  - Pass the sync service and home-aware placement into the dispatcher.
  - Replace the `RecoverCrashedInstancesContext` call in `startRuntime` with pool-aware recovery, ordered before the spawn and group reconcilers.
  - Hook `RemoveInstanceDir` call sites to runner deletion.
- **SQLite migrations:** home records, home-history moves and transfer sessions.
- **Mount `/api/v1/runners/transfers/*`** with runner-key auth and per-request deadlines.
- **Config:** load threshold, size cap, restart grace, transfer part size, and the staging directory for incoming pushes.
- **MCP and REST:** `runner_drain`, plus drain progress on `runner_get`, plus home history on the instance and trace reads.
- **Runner binary:** process and agent job execution, the manifest walker, and the bundle cache.

## 6. Integration seams
- **Placement function:** the tiers live behind the same function as Phase 1 placement.
- **Completion hook:** output validation plus a fenced durable result/checkpoint/continuation commit is the sole workspace-step completion contract; artifact upload alone cannot mark success.
- **Ledger fence:** pushes, results and deletions all pass through it.
- **Engine recovery/commit seam:** change startup recovery and durable invocation/result/continuation handling in the run loop. Today `executeWithRetry` ignores a `NodeExecution.Create` error and persists advancement separately; that cannot guarantee preservation of completed steps. Adoption and result replay use the same dispatcher ledger, not a second scheduler.
- **Phase 4 hook:** the session mover reuses home moves, pulls and drain waits.

## 7. Error handling
- **Partial uploads** are never applied. Staged parts expire.
- **A part hash mismatch** retries that part.
- **A push failure** makes a failed attempt, never success. A remote step's workspace changes are not visible on the server until the push completes.
- **A full server disk while applying** fails the step with a clear error, and the server copy stays at the previous checkpoint.
- **A pull failure** re-places the step: the runner is ineligible for this attempt, and the step moves under tier 1.
- **An over-cap workspace** never moves for load. Tiers 1 and 2 still move it.
- **A runner that misses the restart grace** goes lost, and its steps are recovered.
- **Repeated external effects** (for example a push or a comment) are possible on recovery. Return a persistent recovery notice in instance/trace/operator reads and events, rendered beside recovery/history in Phase 5: interrupted work may repeat external effects; exactly-once effects are not guaranteed. Key revocation confirmation carries the same disclosure (RR-16).
- **Fenced writes** are rejected centrally. A fenced runner kills its process and deletes its copy.

## 8. Testing — STRICT TDD (all five categories required)
For every component and category below: write a failing test, run it to prove the intended failure, implement the minimum change, rerun to green, then refactor while green. Use fake clocks and fault injection at ownership/commit boundaries; do not substitute post-hoc tests for STRICT TDD.
- **Happy path:** a loop of `agent_call` → `script` → `agent_call` on one remote home runs with zero pulls after the first, and a push after each step.
- **Success:**
  - An exhausted source at the home moves the workspace (tier 1), and the step runs elsewhere with the reason recorded.
  - A full home with a free alternative moves (tier 2).
  - Drain completes, with progress reported and homes released.
  - Parallel branches place independently, and gather sees all branch results.
- **Failure:**
  - When a runner is lost mid-step, the step repeats on a clean checkpoint and completed steps are preserved.
  - A stale result after failover is rejected.
  - A revoked key recovers the runner's workspace steps.
- **Error:**
  - An interrupted upload never marks the step done.
  - A transfer token for the wrong job or generation is refused.
  - A server disk-full during apply keeps the previous checkpoint.
  - A pull failure re-places the step.
- **Edge-case:**
  - On a server restart, remote jobs re-attach with no repeat, and built-in steps repeat.
  - A runner that is down for the whole restart grace becomes lost and is recovered.
  - A lib script finds its siblings on a remote runner.
  - Passthrough env values come from the runner.
  - An over-cap workspace stays put under load imbalance.
  - Deletion is delivered to a runner that was offline at removal time.
- **Chaos:** tests that kill the connection mid-transfer and mid-step.

- **Checkpoint/ownership regressions:** built-in interrupted edits roll back; two successful steps on one home publish distinct manifests; failed pushes restore the previous checkpoint before retry; suspect homes do not move before loss; two workspace writers cannot overlap; chmod/deletion/symlink/Git metadata round-trip; workspace-relative tool commands use the workspace cwd; crash after result/checkpoint commit but before engine advancement replays without repeating the completed step; dry-pool drain remains visibly blocked.
