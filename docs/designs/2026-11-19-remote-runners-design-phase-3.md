# Remote Runners — Design Phase 3: Workspace mobility and recovery

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md`
**Idea doc (context only):** `docs/ideas/remote-runners.md`
**Evidence:** `docs/evidence/remote-runners/research-transport.md` §5.2–5.3
**Depends on:** Phase 1 (ledger and fence, placement, dispatcher, `ProcessRunner`, execution status, event log) and Phase 2 (hub, liveness, remote `StepRunner`, telemetry window, keys).
**Cross-phase decisions:** see Phase 1 §0. This phase realises D2 (server copy of record, same root), D7 (restart = built-in runner loss), D9 (script bundles), D10 (stay-versus-move tiers) and drain.

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
| Workspace manager | `domain/engine/ports.go:WorkspaceManager` (:27); `adapter/filesystem/workspace.go` (:59) creates `<root>/instances/<uuid>` | The server copy stays the checkpoint of record. `InstanceDirStats` provides the size for the move cap. `RemoveInstanceDir` also triggers deletion on runners. |
| `{{workspace}}` | `domain/engine/workspace.go:prepareWorkspace` (:17); `conventions.go:WorkDirFromVars` (:30) | Unchanged value; identical on every runner (D2). |
| Group workspaces | `domain/engine/group_workspace.go:GroupWorkspace` (:26), `working_area.go:WorkingArea`; adapters `adapter/filesystem/group_workspace.go`, `adapter/git/working_area.go` | Unchanged. They operate on server copies, which are current at every step boundary. |
| Group launcher | `domain/engine/group_launcher.go` (`EnsureInstanceDir` for each branch at :294) | Each branch is a run with its own independent home and placement. |
| Script resolution | `adapter/process/executor_script.go:ScriptExecutor` (resolves via `FileReader.ResolvePath`; cwd = script folder) | The resolved folder becomes the bundle source. |
| Env construction | `adapter/process/env.go:buildEnv` (:16), `cfg.Script.EnvPassthrough` | On a remote runner, passthrough values come from the runner's own env. |
| Crash recovery | `domain/engine/engine.go:RecoverCrashedInstancesContext` (:2197), called from `cmd/arbitron/model_dependencies.go:startRuntime` (:82) | Replaced by pool-aware recovery (no blanket `SetFailed`). |
| Run re-entry | `Engine.StartInstance` (:1016) / `run()` (:1040) starting from `CurrentNodeID` | Recovery restarts the run goroutine at its current step. The dispatcher adopts or repeats the job. |
| Server read timeout | `cmd/arbitron/main.go` (:1690) `ReadTimeout: 10s` | Transfer handlers lift it per request through `http.ResponseController`. |
| Phase 1/2 seams | Ledger fence, placement function, dispatcher idempotency key, hub reconcile, remote `StepRunner`, telemetry window | Extended, not duplicated. |

## 3. Components

### 3.1 Workspace homes (`domain/runner`, ledger extension)
- **Job:** record, per run, the runner that currently holds a working copy of the workspace (its **home**) and a home generation. With no home, only the server copy exists. The built-in runner is always "at home" with the server copy.
- **Lifecycle:** each handoff bumps the generation and records a move entry (from, to, reason, bytes, time), which is the workspace's history of homes. It appends `workspace.moved` to the pool event log.
- **Read surface:** the home history is exposed on the instance and trace reads (MCP `trace_get` and the HTTP instance endpoints), so Phase 5 can render it.
- **Exactly one writer:** a step that needs the workspace runs only on its home or on the runner it is being moved to.

### 3.2 Stay-versus-move placement (extends Phase 1 placement)
- **Job:** for workspace steps, choose between the current home and the alternatives using the approved tiers (D10):
  1. **Must move:** the home is ineligible. That covers no healthy source for the group, cordoned, draining, suspect or lost, incompatible, or too little free disk.
  2. **Should move:** the home has no free slot and another eligible runner has one.
  3. **May move for load:** the home's health is degraded (memory pressure critical, or thermal state serious or worse), or the slot-utilisation gap is at least the configurable threshold (default 50 percentage points). In both cases the workspace must also be under the configurable size cap.
  4. **Otherwise stay.**
- **Process steps:** a `script` or `tool_call` has no model group and goes to the home whenever it is eligible with a free slot; otherwise it follows tiers 1 and 2.
- **Reason:** each decision records its tier and inputs as the placement reason.

### 3.3 Workspace sync (`domain/runner` service, `adapter/http` transfer endpoints, runner adapter)
- **Job:** keep the server copy authoritative and move bytes off the control channel.
  - **Manifest:** a path, size and hash listing of a workspace, computed on demand and cached by home generation.
  - **Pull:** before a workspace step on a remote runner, the runner compares its copy (if any) with the server manifest and fetches only the differing files. It reuses its copy as-is when it holds the current home generation.
  - **Push:**
    - After a workspace-changing step, the runner sends changed files and deletions.
    - The server stages them and applies them **all-or-nothing** to its copy under the ledger fence.
    - **Only then** does the dispatcher mark the step complete.
  - **Transport:**
    - Chunked parts of about 64 MB, resumable. Each part is hash-verified.
    - Separate HTTP requests under `/api/v1/runners/transfers/*`, authenticated by the runner key plus a short-lived transfer token scoped to the job and generation.
    - Per-request read deadlines. Coordination messages ride the Phase 2 WebSocket.
  - **Progress:** published as the moving-workspace execution status.
- **Built-in runner:** sync is a no-op, because it works in place.

### 3.4 Script bundles and remote process steps
- **Job:** ship what a remote process step needs, and nothing more.
  - **Script:** the server packs the resolved script directory (the workflow folder or the `@lib` scope folder; live-read at dispatch, as today) into a bundle addressed by its content hash. The runner fetches it through the transfer endpoints, caches it by hash, and runs the script with the unpacked folder as its working directory. Stdin still carries the variable snapshot.
  - **tool_call:** sends the rendered command, args and env. On a remote runner the working directory is the runner process's own, mirroring today's "inherit the server cwd".
  - **Env:** passthrough names come from server config; the **values** come from the runner's environment.
- **Runner side:** the Phase 2 job executor gains process and agent jobs. They use the same `ProcessRunner` and harness adapters linked into the binary.

### 3.5 Recovery (`domain/runner` with the engine)
- **Adoption:** the dispatcher's idempotency key (run, step, attempt) means that re-entering a step that already has a live job **adopts** it (waits for its result) instead of dispatching again.
- **Lost runner:** for each job on the runner, the dispatcher re-places the step with generation + 1, and the new home pulls the **server checkpoint**, which is clean as of the last completed step. Completed steps are preserved, and only the interrupted step repeats. This does not consume `retries`. With no eligible runner, the step waits · capacity.
- **Server restart** (replaces `RecoverCrashedInstancesContext`'s blanket `SetFailed`):
  - Remote job leases are extended by a configurable restart grace.
  - Every `running` run re-enters its current step through `StartInstance`, which leads to one of three outcomes:
    - **Remote job:** adopted when its runner reconnects within the grace. Otherwise the runner is lost and the step is re-placed.
    - **Built-in job:** treated as lost-runner recovery and repeated (D7). The built-in step's workspace is the server copy as the step left it, matching today's retry semantics.
    - **No live job:** the run continues at its step.
- **Execution status:** recovering is shown while a step waits for re-attach or re-placement. Events `job.recovered` and `job.adopted` are appended to the pool event log.

### 3.6 Fencing and stale copies
- **Job:** a runner that reconnects holding stale generations is told to stop the job (`fenced`) and delete its copy. Its pushes and results are rejected by the single ledger fence.
- **Deletion:** after a planned move, the old home is told to delete its copy once the new generation is recorded.

### 3.7 Drain and cordon (`domain/runner` service; MCP and REST)
- **Cordon (Phase 1):** stops new placements.
- **Drain (new; admin only, audited `runner.drain`):**
  1. Stop new placements.
  2. Let active steps finish.
  3. **Release** homed workspaces whose runs are between steps. The server copy is already current, so nothing is copied; the runner is told to delete its copies, and the next step places freely.
  4. **Wait** for interactive sessions; Phase 4 moves them at turn boundaries.
- **Progress:** remaining active steps, homed workspaces and sessions, plus any capacity preventing completion (for example, no other runner for the model group of a pending session).
- **Interfaces:** `runner_drain` over MCP and REST. `runner_get` includes drain progress.
- **Key revocation (Phase 2):** now also releases homes and recovers workspace steps through §3.5.

### 3.8 Workspace removal on runners
- **Job:** when a run's workspace is removed (`RemoveInstanceDir` on completion or cleanup), or its home moves, the hub tells the home runner, and any runner known to hold a fenced copy, to delete it. A runner that is offline is told on reconnect, through `hello` reconciliation.

## 4. Data flow

**A workspace step that moves:**
1. Placement applies the tiers and chooses runner R (reason recorded).
2. The ledger records the job (generation g) and the new home (home generation h + 1), and publishes moving workspace.
3. R pulls the manifest difference, then runs the step.
4. R pushes the changed files. The server stages them and applies them atomically, which passes through the fence.
5. The dispatcher completes the step, and the engine writes `NodeExecution`.
6. The old home is told to delete its copy.

**Steps that stay** on the same home skip the pull (the copy is current) and still push.

**Lost runner:** heartbeats stop, the runner goes suspect and then lost, and the dispatcher re-places the step (g + 1). The new home pulls the server checkpoint, and the step repeats. A late result from the old runner is fenced and discarded.

**Server restart:**
1. Leases are extended.
2. Runs re-enter their current step.
3. Each step is adopted when its runner's `hello` matches the ledger, or repeated (built-in, or a runner lost after the grace).

## 5. Data Provenance

| Input | Consumer | Producer |
|---|---|---|
| Workspace absolute path | Runner pull and push, executors | `domain/engine/workspace.go:prepareWorkspace` → `adapter/filesystem/workspace.go:EnsureInstanceDir`; persisted in `vars["workspace"]` |
| Workspace size | Move-for-load cap | `domain/engine/ports.go:WorkspaceManager.InstanceDirStats` (filesystem adapter) |
| Server manifest | Pull and push | **New** manifest builder over the server copy in the workspace sync service |
| Runner free disk, memory pressure, thermal state, load | Tiers 1 and 3 | Phase 2 telemetry window (the darwin thermal and pressure source is a Phase 2 **UNRESOLVED DEPENDENCY**) |
| Slot utilisation per runner | Tiers 2 and 3 | Phase 1 ledger slot accounting |
| Source health for the step's group | Tier 1 | Phase 1 registry and health monitor; Phase 2 heartbeats |
| Load threshold (50 pp), size cap, restart grace | Placement, recovery | **New** server config values |
| Script directory | Bundle builder | `domain/engine/ports.go:FileReader.ResolvePath` (as used by `adapter/process/executor_script.go`) |
| Variable snapshot for script stdin | Remote script | `variable.VariableStore.Snapshot` (as today in `ScriptExecutor`) |
| Passthrough env names | Remote process steps | `domain/config` `Script.EnvPassthrough` (`config.go:149`) |
| Passthrough env values | Remote process steps | The runner process's own environment (launchd agent env / `runner.env`) |
| Running runs and current step at boot | Recovery | `instance.Repository` listing of `running` runs (as used today by `RecoverCrashedInstancesContext`) and `Instance.CurrentNodeID` |
| Live jobs and leases at boot | Recovery | Phase 1 job ledger |
| Jobs held by a reconnecting runner | Adoption and fencing | Phase 2 `hello` held-jobs list |
| Transfer token | Transfer endpoints | **New** issuer in the workspace sync service, scoped to the job and generation |
| Archive compression | Transfer | **UNRESOLVED DEPENDENCY:** no zstd library is in `go.mod`. The plan must vet and add one, or use the standard library's gzip. |

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
- **Dispatcher completion:** the push is part of a step's completion contract. Nothing else marks a workspace step done.
- **Ledger fence:** pushes, results and deletions all pass through it.
- **Engine recovery hook:** the only engine-side change is the recovery entry point. The run loop itself is unchanged, and adoption lives in the dispatcher.
- **Phase 4 hook:** the session mover reuses home moves, pulls and drain waits.

## 7. Error handling
- **Partial uploads** are never applied. Staged parts expire.
- **A part hash mismatch** retries that part.
- **A push failure** makes a failed attempt, never success. A remote step's workspace changes are not visible on the server until the push completes.
- **A full server disk while applying** fails the step with a clear error, and the server copy stays at the previous checkpoint.
- **A pull failure** re-places the step: the runner is ineligible for this attempt, and the step moves under tier 1.
- **An over-cap workspace** never moves for load. Tiers 1 and 2 still move it.
- **A runner that misses the restart grace** goes lost, and its steps are recovered.
- **Repeated external effects** (for example a push or a comment) on recovery are disclosed as at-least-once (spec).
- **Fenced writes** are rejected centrally. A fenced runner kills its process and deletes its copy.

## 8. Testing — STRICT TDD (all five categories required)
Tests are written first for every component.
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
