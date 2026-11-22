# Remote Runners — Design Phase 4: Interactive session continuity

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md`
**Idea doc (context only):** `docs/ideas/remote-runners.md` §9
**Evidence:** `docs/evidence/remote-runners/research-session-migration.md`
**Depends on:** Phase 1 (dispatcher, decorators, classifiers, event log), Phase 2 (hub, remote `StepRunner`, protocol), Phase 3 (workspace homes, pull and push, drain, recovery, fencing).
**Cross-phase decisions:** see Phase 1 §0. This phase realises D11 (conversation rolls back; workspace stays at its last checkpoint).

## 1. Purpose and scope

Let `interactive_llm` and `interactive_agent` sessions run on any runner and keep going without the user re-entering anything. Sessions stay pinned to their runner. They move only for subscription exhaustion, logout, drain or runner loss, and each move keeps as much context as the harness allows, with a visible notice when context is lost.

In scope:
- Per-turn session checkpoints, export and import (the hybrid approach).
- Codex reasoning stripping.
- The transcript-rebuild fallback.
- Automatic resend of an interrupted turn.
- First-turn re-execution.
- The durable turn lease that replaces the in-memory guard.
- Interactive placement pinning and the session mover.
- Drain integration.
- Token streaming from remote runners.

Out of scope: guaranteed preservation of hidden reasoning across accounts (spec non-goal), and dashboard rendering (Phase 5).

## 2. Existing architecture this phase extends

| Seam | Location | How this phase uses it |
|---|---|---|
| First turn | `domain/engine/executor_interactive.go:InteractiveExecutor` (`StreamProviderFor` :128, `StreamChat` :148, record `Create` :257, `UpdateState(waiting)` :262) | Placed through the decorated stream selector. On success it exports the first checkpoint. On exhaustion the step is re-run elsewhere. |
| Later turns | `domain/engine/conversation.go:conversationHandler.HandleMessage` (:221), resume via `ChatRequest.SessionID`, `persistTurn` | Takes the durable lease, places the turn (pinned), and exports the checkpoint after success. |
| End and abandon | `conversation.go:EndSession` (:384), `AbandonSession` (:434) | Take the durable lease. |
| Overlap guard | `conversation.go` `turnInProgress sync.Map` (:86, acquired :242) | Replaced by the durable turn lease. `ErrInteractiveTurnInProgress` semantics are kept. |
| Conversation record | `instance.NodeExecution.LLMConversation` (`{sessionId, nonce, messages}`; keys in `conventions.go:21–23`), `instance.NodeExecutionRepository` | Gains the checkpoint reference, pinned runner, session generation and move history. Messages stay the source of truth. |
| Group sessions | `instance.OpenSessionRecordUpdater` (`ports.go:98`), group staging dir in `HandleMessage`; resolution sessions started by `group_resolution.go:GroupResolutionBridge.StartResolution` | Group-resolution staging becomes a named checkpointed resource keyed by `GroupResolutionIdentity`; first-turn startup, later turns, End/Abandon and recovery share the lease and mover. P4 T11 supplies portable standalone Git staging, resource-keyed transfer and committed materialization before gather verification; P4 T13 wires it. |
| Interactive events | `interactive.token`, `interactive.message`, `interactive.ended`, `interactive.error`, `interactive.history` on the event bus | Remote output is re-emitted as `interactive.token`. A new turn-restarted event and a lossy-move notice are added. |
| Claude session files | Harness `~/.claude/projects/<encoded-cwd>/<id>.jsonl` plus `<id>/` folder | Export source and import target on runners. |
| Codex rollout | `adapter/codex/rollout.go:resolveCodexHome` (:43), `findRollout` (:73) | Export source; import writes a rollout that Codex re-indexes itself. |
| Interactive codex opt-in | `cmd/arbitron/main.go` `agentStreams` map (:1359) | Unchanged: codex interactive stays behind its env opt-in. |
| Phase 3 seams | Homes, pull and push, drain waits, recovery, fence | Reused for the workspace half of a session move. |

## 3. Components

### 3.1 SessionPortability port (`domain/engine`; adapters in `adapter/claudecli`, `adapter/codex`)
- **Job:** move the harness's own session state between machines.
  - **Export:** after a successful turn, on the runner holding the session, produce a session bundle: backend, session ID, harness version, origin working directory and checksum.
    - Claude: the session `.jsonl` plus its `<session-id>/` folder.
    - Codex: the rollout file.
  - **Import:** on the target runner, install a bundle and return the session ID to resume.
    - **Claude:** bytes are never edited (an edited history can cause a 400). Identical workspace paths (D2) preserve workspace references; foreign harness-home/sidecar references refuse native import and take the visible rebuild fallback (P4 T4/T8/T9).
    - **Codex:** reasoning items are **always** stripped (see §5). A bundle containing compaction items refuses import, which leads to the rebuild fallback.
- **Executed on runners:** remote native export/upload is part of the originating stream assignment before its terminal success, under that parent fence and single slot. It is retained durably before terminal ACK; Hub.ExportSession reads the exact receipt without reserving another export job. Import is a separately fenced move operation. The built-in runner calls adapters in-process under its parent assignment. P4 T6/T12/T13 implement and test limit-one/full-limit completion and ACK→restart adoption (contracts §24).

### 3.2 Session checkpoint store (port in `domain/engine`; server filesystem adapter)
- **Job:** hold bundles keyed by (run, durable session/invocation identity, turn index), including the group-resolution attempt identity. Node ID alone can recur and must not alias a previous session. Only the bundle for the latest successful turn is kept per session (plus the one being written).
- **Bundle bytes:** travel through the Phase 3 transfer endpoints, scoped to the exact parent job/session/resource and generation. P4 T12 OpenSessionBlobs fsyncs prepared bytes and records their immutable ref in the durable inbox before ACK; P4 T13 injects the persistent store into the real hub/transfer/export graph. An in-memory blob cannot back a terminal acknowledgment.
- **Reference:** recorded in dedicated session columns with invocation/pin/generation; accepted binding and transcript remain in conversation metadata.

### 3.3 Durable turn lease (port in `domain/engine`; SQLite adapter)
- **Job:** at most one in-flight interactive operation per session, across processes and runners. It replaces `turnInProgress`.
  - Acquired by first-turn `InteractiveExecutor`/group-resolution startup, `HandleMessage`, `EndSession`, `AbandonSession` and the session mover.
  - Carries a session generation. A turn result, export or completion with a stale generation is discarded through the Phase 1 fence.
  - A concurrent submission is rejected with `ErrInteractiveTurnInProgress` (unchanged contract).
  - Renew the lease while running; expiry alone cannot permit an overlapping turn on a still-running runner; occupied expired tokens require P4 T10 proof-gated adoption/reclamation. Recovery first adopts the matching remote job or fences the old owner and increments the session generation before a new dispatch. A mover called within a turn reuses that lease token rather than acquiring it recursively.
  - Persist a **turn intent** before dispatch or acknowledging the submission: request ID, original rendered prompt/system prompt and nonce for first turns, or exact user message for later turns, resolved model constraints, prior checkpoint reference, session/job generations and operation ID. First turns, group-resolution startup, End/Abandon and moves all participate. The intent survives server restart even though the instance may be `waiting`, and is completed only with the turn commit. P4 T3 supplies the durable resend source, including immutable accepted route/tool constraints; P4 T10 owns restart consumption.

### 3.4 Interactive placement (extends Phase 1 decorators and Phase 3 placement)
- **Job:** pin each session to its runner.
  - **First turn:** placed like a workspace step (`interactive_agent`) or a session-capable chat step (`interactive_llm`). Both require a stable working directory and a recoverable session resource; `interactive_llm` cannot inherit Phase 2's disposable hermetic scratch policy. That runner becomes the pin.
  - **Later turns:** go to the pin while it is eligible.
  - **Allowed moves:** only for source exhaustion, logout, the runner draining, or runner loss. **Never for load.**
  - A full slot limit, cordon, suspect status or transient congestion does not authorize a session move. Wait on the pin (queued · slot or an explicit availability/capacity reason); a brief connection gap waits through Phase 2's grace. Exhaustion/logout/loss recovery proceeds automatically even between user turns; it cannot depend on the next user submission.
  - **Group-staging sessions:** use the same permitted move triggers and single-owner rules, with the staging resource in place of the normal instance workspace. Do not mutate parent/task directories or let gather proceed until the staged checkpoint has committed (RR-11).

### 3.5 Session mover (`domain/runner` service, called from `ConversationHandler`, `InteractiveExecutor`, drain and recovery)
- **Job:** move a session safely while holding the turn lease.
  1. Choose a target satisfying the exact accepted model/route/backend/tool requirements within the same model group (never another family).
  2. Fence the old owner and reserve the next session/resource generation under the held lease before target import/resume. Restore/pull the last committed turn checkpoint through Phase 3, discarding interrupted edits on built-in and remote runners alike.
  3. Import the latest bundle and resume. If that fails, **rebuild**: start a fresh session from the original first prompt with the same completion nonce, plus Arbitron's recorded transcript, and mark the move lossy with a visible notice ("tool details and reasoning may be lost").
  4. Record durable move state, pin/source and committed move history (from, to, reason, lossy flag) in session columns/move rows and the pool event log.
- **Between turns:** source-health, loss and drain events enqueue an idempotent move under the lease even while the run waits for human input. The next message uses the recovered pin, or waits visibly for the pending move/capacity.
- **Mid-turn** (classified exhaustion or logout, or runner lost):
  1. The partial turn is discarded. Arbitron's DB is already at a clean boundary because only successful turns are persisted.
  2. A turn-restarted event with exact invocation/request/turn and actual reserved generation tells useInteractiveSession to clear partial tokens.
  3. The mover runs and the **same user message is resent automatically**.
  4. Restore the last successful workspace and bundle on both runner types; never retain the interrupted turn's partial edits. Notify about context loss when applicable, not a different rollback policy for built-in runs.
- **First turn:** exhaustion or loss in `InteractiveExecutor` re-runs the step on another runner with a fresh session. There is no bundle yet, so the step repeats as a whole.

### 3.6 Session checkpoint after each turn
- **Job:** a turn counts as successful only after its bundle is exported and stored, and, for `interactive_agent`, after a new workspace checkpoint (Phase 3) is staged. Commit the workspace reference, session bundle reference, messages/session metadata, completed turn intent and result together under the job/resource/session fence. A crash between artifact upload and metadata commit retains the previous pair; orphan staged artifacts are reclaimed. A terminal sentinel also commits before ending/advancing the session. This applies to first turns and group-resolution turns, including their self-written `NodeExecution` records.
- This satisfies the spec's rule that successful turns preserve both workspace progress and recoverable context.

### 3.7 Token streaming
- **Job:** the remote runner streams output frames (Phase 2 sequence-numbered and replayed). The server re-emits them as the existing `interactive.token` events, using the existing client transport; clients also handle turn-restarted and persisted loss notices and discard superseded-generation tokens.

### 3.8 Drain integration (extends Phase 3 drain)
- **Job:** drain waits for each pinned session's current turn to end, then calls the mover. A session with no eligible target appears in drain progress as blocked, with the missing group.

## 4. Data flow

**A normal turn:**
1. `HandleMessage` acquires the lease (generation s).
2. Persist the accepted turn intent, then the decorated stream selector sends the turn to the dispatcher, which places it on the pin.
3. The runner resumes the session; tokens flow to `interactive.token`.
4. On success: stage the bundle and workspace (agent/staging resource), atomically commit their matching references with messages/result/intent, then release the lease and publish completion.

**An exhausted turn** (mid-turn on runner A):
1. The classified outcome reaches the dispatcher, which marks the source and hands over to the mover. The lease is still held.
2. A turn-restarted event goes out.
3. The mover picks runner B, fences A and reserves session generation s + 1. B pulls the last committed workspace and imports the matching bundle (or rebuilds and adds a notice).
4. The move record commits the new pin and generation; session execution resumes only under this ownership.
5. The same message is resent on B, and the turn completes as normal.
6. Late output from A is fenced.

**Runner lost between turns:** the loss event schedules the mover immediately; with no target, publish capacity wait even though the session is waiting for a human. Server startup scans pending turn intents and session moves in addition to running instances, adopts matching live remote turns, and automatically resends the durable message only after fencing an interrupted attempt.

## 5. Data Provenance

Repository verification below records the pre-implementation baseline: **UNRESOLVED DEPENDENCY** labels identify inputs that had no implemented producer or verified source when this design was written. The reviewed implementation-task ownership, runtime wiring and sink proofs are in `docs/plans/remote-runners/2026-11-20-contracts.md` §§19–24. A named, fully specified producer task closes a dependency for the planning gate; it does not claim that code already exists. The recorded evidence/platform decisions in §22 remain in force. The alignment review adds the explicit **UNRESOLVED DEPENDENCY RR-17** in §24: non-portable partial-clone completion has no recoverable checkpoint producer. It must be reported; upstream closure claims exclude that branch. Upstream gaps propagate to their consumers; a fallback closes a gap only when it delivers the spec outcome.

| Leaf input | Required production producer and consumer | Root wiring / final proof |
|---|---|---|
| Exact rendered first prompt/system prompt/nonce; accepted later message/request | P4 T3 `TurnIntentRepo.Persist`, handler/InteractiveExecutor/bridge acceptance; snapshot includes immutable `AcceptedTurnBinding` | P4 T13 installs stores/restorer before dispatch; restart replays exact request with changed profile fixture |
| Accepted model/provider/route/backend/tools/context/effort/pin | P4 T3 `CaptureAcceptedTurnBinding` and `adapter/provider/turn_binding.go:TurnBindingRestorer`; route metadata hash and live original-connection key access | P4 T13 same restorer on all session entry points; changed/revoked route never silently substitutes |
| Session invocation/pin/checkpoint/generation | P4 T1 migration 045 readers, P4 T6 atomic first/later turn writer, P4 T8 atomic move writer | P4 T7/T10/T14 select exact active invocation; P4 T13 loop/retry and actual trace/detail sink tests |
| Claude JSONL/sidecars and home/version | P4 T4 `claudecli.SessionPortability.Export/Import`; `sessionports` same launch env/version producer | P4 T13 links both binaries; foreign-home import or actual resume refusal takes visible automatic rebuild; no signed message rewriting |
| Codex rollout and reasoning policy | P4 T5 `codex.SessionPortability`, existing resolveCodexHome/findRollout | P4 T13 links both binaries; unconditional reasoning strip, compaction fallback, persisted notice; native resume is provisional |
| Bundle bytes/ref/checksum and transfers | P4 T6 `checkpoint.SessionStore`, P4 T12 scoped session transfer/Hub.ExportSession | P4 T13 one store/blob/token graph; corrupt/missing transfer cannot commit |
| Exact stream job/resource completion receipt | P3 StepWorkspace.BeginAttempt/FinishSuccess retained by dispatcher stream forwarder; P4 T12 remote prepared receipt | P4 T6 validates/reuses receipt; missing receipt prevents Done/commit; no inferred last-placement/Latest |
| Atomic workspace+bundle+messages+intent/result | P4 T6 `sqlite.TurnCommitRepo.CommitTurn`, P3 prepared candidate catalog/resource lease | One DB transaction checks job/resource/turn lease and publishes matching refs; P4 T13 crash/fault sink tests |
| Durable turn ownership/renewal/recovery proof | P4 T2 `TurnLeaseRepo`; P4 T10 `TurnRecoveryCoordinator` from real hub/adoption/stop/loss facts | P4 T13 exact coordinator deps; expiry alone cannot overlap; live restart adopts once before generic rerun |
| Move triggers/target eligibility | P1 monitor/outcomes/registry, P2 Hub loss/admission, P3 drain, P4 T7 pinning and P4 T10 controller | P4 T13 listener and retry worker; idle source loss moves without user input; unavailableReason is distinct from logout |
| Matching mutable workspace or group staging | P3 normal prepared checkpoints; P4 T11 `ResolutionResources`, `sqlite.ResolutionResourceRepo`, `git.WorkingArea.ExportResolution/ImportResolution` using P3 MaterializePortableGit | P4 T12 descriptors/transfers carry resource ownership; P4 T13 actual remote conflict resolution → committed materialization → gather sink |
| Resumable move state and target pin/source | P4 T8 persisted move state machine and fenced `SessionMoveRepo.CommitMove` | P4 T13 worker restores interrupted move once; cannot strand bumped generation or leak slot |
| Token/session/turn identity, restart and durable notices | P4 T9 first/later/group emitters; P4 T12 protocol/history; P5 T7 actual useInteractiveSession/InteractivePanel | P4 T13 plus P5 T8 root/browser test clears partial tokens, drops old generation and rehydrates loss notice |
| Validated TTL/retention/retry limits | P4 T1 config defaults/validation | P4 T13 loader/root uses valid config; bounded worker pass yields pending intent rather than dropping accepted message |
| Optional native account/fidelity evidence (RR-13/RR-14) | No account fingerprint is consumed by release behavior. Conservative loss notice, unconditional Codex stripping and bounded visible transcript rebuild cover unverified cases | Two-account or foreign-home fidelity research can improve native fidelity later; it does not excuse missing fallback or require a release-time human decision |

Every producer above is a required implementation task, not a claim that code already ships. Required collaborators cannot be nil/empty in production. Named root tests must fail when a producer or wiring edge is removed.

### Deferred to plans
- **Composition-root wiring:**
  - Construct the checkpoint store, the turn-lease store and the mover.
  - Inject the lease into `conversationHandler` in place of `turnInProgress`, and the mover into the conversation handler, `InteractiveExecutor`, drain and recovery.
  - Register the claudecli and codex `SessionPortability` adapters on the built-in runner and in `cmd/arbitron-runner`.
- **SQLite migrations:** the turn lease; `LLMConversation` additions (checkpoint reference, pinned runner, session generation, move history), dedicated session columns and move/resource tables in migration 045, as specified by P4 T1/T11.
- **Protocol additions:** export and import message kinds, and bundle transfer kinds over the Phase 3 endpoints.
- **New event kinds:** turn restarted and lossy-move notice, with their dashboard API types (rendered in Phase 5).
- **Config:** bundle retention, and how long the session's turn lease lasts.

## 6. Integration seams
- **`SessionPortability`:** the only place that knows harness session file layouts.
- **Turn lease:** the only overlap guard. The fence rejects stale session writes.
- **Mover:** reuses Phase 3 homes and pulls and Phase 1 placement; no parallel move path.
- **Events:** existing `interactive.*` events, so clients need no new transport.

## 7. Error handling
- **Export failure:** the turn is not recorded as successful, and the user sees a turn error. Nothing is persisted that couldn't be recovered.
- **Import or resume failure:** leads to the rebuild fallback plus a notice. If the rebuild also fails, report a visible restoration error with bounded retry/operator recovery; classify as **waiting for capacity** only when there is genuinely no eligible same-model capacity. Deterministic import/prompt failures must not masquerade as exhausted subscriptions.
- **No eligible target** (full same-model execution requirements): the session waits · capacity, naming the group and the next reset.
- **A concurrent submission** is rejected (`ErrInteractiveTurnInProgress`).
- **A stale-generation result** from the old runner is discarded.
- **A drain/external move requested during an in-flight turn** waits for its safe boundary and lease. Mid-turn recovery reuses the active holder's lease (or fences a lost holder); it must not deadlock trying to reacquire its own lease. Moves never execute beside an unfenced turn.
- **Version skew** between the bundle and the target harness refuses import and leads to the rebuild.
- **Loss notices:** every transcript rebuild, Codex reasoning-stripping move, and Claude move with unverified cross-account context retention emits a persisted notice that tool details/reasoning may be lost. Successful native import alone does not prove full fidelity. Preserve all available recorded turns; never silently summarize/truncate to fit a target. An oversized rebuild is a visible restoration error.
- **Claude sidecars:** identical workspace paths do not equal identical home/session-store paths. Absolute tool-result references must resolve on the target without editing signed message content. P4 T4 refuses incompatible paths; P4 T8/T9 visibly rebuild on import or first-resume refusal. A verified native mapping is an optional fidelity improvement.

## 8. Testing — STRICT TDD (all five categories required)
For every component and category below: write a failing test, run it to prove the intended failure, implement the minimum change, rerun to green, then refactor while green. Use fake clocks and fault injection at ownership/commit boundaries; do not substitute post-hoc tests for STRICT TDD.
- **Happy path:** a multi-turn `interactive_agent` session stays pinned to one runner, with a checkpoint after every turn.
- **Success:**
  - Exhaustion between turns moves the session, and the next turn continues with all supported available context and a loss notice whenever fidelity is reduced or unverified.
  - Mid-turn exhaustion resends the same message automatically, and the client gets a turn-restarted event.
  - Drain moves a session at its turn boundary.
- **Failure:**
  - An import failure triggers the transcript rebuild, with a visible lossy notice.
  - A runner lost mid-turn recovers the session on another runner.
  - No same-group target leads to a visible capacity wait.
- **Error:**
  - Two concurrent submissions across two server processes give exactly one rejection.
  - A stale-generation result is dropped.
  - An export failure means the turn is not persisted.
- **Edge-case:**
  - First-turn exhaustion re-runs the step with a fresh session.
  - Codex reasoning is stripped, checked against fixture rollouts.
  - A Codex compaction item forces the fallback.
  - A group-staging session moves only under the allowed recovery/drain triggers, preserving the gather staging identity and checkpoint.
  - Built-in and remote interrupted turns both restore the prior committed workspace; context-loss notices remain visible where applicable.
  - An expired lease from a crashed holder is reclaimable.

- **Durable-session regressions:** a server restart with a waiting instance and pending turn intent adopts a live remote turn or resends the exact message after fencing; first-turn recovery reuses the rendered prompt and nonce; lease renewal/expiry never overlaps turns; workspace/bundle/turn commit crashes preserve the prior matching pair; cordon/suspect/slot pressure never moves pins; idle loss triggers recovery without a new user message; group-resolution sessions recover/drain without touching parent/task directories; differing Claude home paths take a verified native path or visible fallback; successful Codex import after stripping still shows the loss notice.
