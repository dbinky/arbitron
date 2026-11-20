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
| Group sessions | `instance.OpenSessionRecordUpdater` (`ports.go:98`), group staging dir in `HandleMessage`; resolution sessions started by `group_resolution.go:GroupResolutionBridge.StartResolution` | Sessions in a server-local staging directory (including group-resolution sessions) stay pinned to the built-in runner. |
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
    - **Claude:** bytes are never edited (an edited history can cause a 400). Identical workspace paths (D2) keep embedded paths valid.
    - **Codex:** reasoning items are **always** stripped (see §5). A bundle containing compaction items refuses import, which leads to the rebuild fallback.
- **Executed on runners:** remote runners carry export and import as protocol message kinds; the built-in runner calls the adapters in-process.

### 3.2 Session checkpoint store (port in `domain/engine`; server filesystem adapter)
- **Job:** hold bundles keyed by (run, step, turn index). Only the bundle for the latest successful turn is kept per session (plus the one being written).
- **Bundle bytes:** travel through the Phase 3 transfer endpoints, scoped to the job and generation.
- **Reference:** recorded in `LLMConversation` together with the session's pinned runner and session generation.

### 3.3 Durable turn lease (port in `domain/engine`; SQLite adapter)
- **Job:** at most one in-flight interactive operation per session, across processes and runners. It replaces `turnInProgress`.
  - Acquired by `HandleMessage`, `EndSession`, `AbandonSession` and the session mover.
  - Carries a session generation. A turn result, export or completion with a stale generation is discarded through the Phase 1 fence.
  - A concurrent submission is rejected with `ErrInteractiveTurnInProgress` (unchanged contract).
  - A crashed holder's lease expires, so a restart never leaves a session locked.

### 3.4 Interactive placement (extends Phase 1 decorators and Phase 3 placement)
- **Job:** pin each session to its runner.
  - **First turn:** placed like a workspace step (`interactive_agent`) or a chat step (`interactive_llm`, which is not hermetic because the harness session lives in its working directory). That runner becomes the pin.
  - **Later turns:** go to the pin while it is eligible.
  - **Allowed moves:** only for source exhaustion, logout, the runner draining, or runner loss. **Never for load.**
  - **Group-staging sessions:** pinned to the built-in runner, because their working directory is server-local.

### 3.5 Session mover (`domain/runner` service, called from `ConversationHandler`, `InteractiveExecutor`, drain and recovery)
- **Job:** move a session safely while holding the turn lease.
  1. Choose a target runner with the same model group (never another family).
  2. Move the workspace using Phase 3: a pull from the server checkpoint, which is clean after a remote turn and holds the partial edits after a built-in turn (D11).
  3. Import the latest bundle and resume. If that fails, **rebuild**: start a fresh session from the original first prompt with the same completion nonce, plus Arbitron's recorded transcript, and mark the move lossy with a visible notice ("tool details and reasoning may be lost").
  4. Bump the session generation and record the move (from, to, reason, lossy flag) in `LLMConversation` and the pool event log.
- **Between turns:** the next message's placement finds the pin ineligible, so the mover runs and the message is sent on the new runner.
- **Mid-turn** (classified exhaustion or logout, or runner lost):
  1. The partial turn is discarded. Arbitron's DB is already at a clean boundary because only successful turns are persisted.
  2. A turn-restarted event tells clients to clear partial tokens.
  3. The mover runs and the **same user message is resent automatically**.
  4. A notice states whether the workspace kept the interrupted turn's partial edits (built-in) or not (remote).
- **First turn:** exhaustion or loss in `InteractiveExecutor` re-runs the step on another runner with a fresh session. There is no bundle yet, so the step repeats as a whole.

### 3.6 Session checkpoint after each turn
- **Job:** a turn counts as successful only after its bundle is exported and stored, and, for `interactive_agent`, after the workspace push (Phase 3) has landed. Only then is the turn persisted (`persistTurn` / `UpdateIfSessionOpen`).
- This satisfies the spec's rule that successful turns preserve both workspace progress and recoverable context.

### 3.7 Token streaming
- **Job:** the remote runner streams output frames (Phase 2 sequence-numbered and replayed). The server re-emits them as the existing `interactive.token` events, so dashboard and MCP clients are unchanged.

### 3.8 Drain integration (extends Phase 3 drain)
- **Job:** drain waits for each pinned session's current turn to end, then calls the mover. A session with no eligible target appears in drain progress as blocked, with the missing group.

## 4. Data flow

**A normal turn:**
1. `HandleMessage` acquires the lease (generation s).
2. The decorated stream selector sends the turn to the dispatcher, which places it on the pin.
3. The runner resumes the session; tokens flow to `interactive.token`.
4. On success: export the bundle, push the workspace (agent), store the bundle reference, persist the turn, and release the lease.

**An exhausted turn** (mid-turn on runner A):
1. The classified outcome reaches the dispatcher, which marks the source and hands over to the mover. The lease is still held.
2. A turn-restarted event goes out.
3. The mover picks runner B. B pulls the workspace from the server checkpoint and imports the bundle (or rebuilds and adds a notice).
4. The session generation becomes s + 1.
5. The same message is resent on B, and the turn completes as normal.
6. Late output from A is fenced.

**Runner lost between turns:** the next message's placement finds the pin lost, so the mover runs and then the message is sent.

## 5. Data Provenance

| Input | Consumer | Producer |
|---|---|---|
| Session ID, nonce, messages | Mover, rebuild fallback | `instance.NodeExecution.LLMConversation`, written by `executor_interactive.go` (:257) and `conversation.go:persistTurn` |
| Original first prompt | Rebuild fallback | First user message in `LLMConversation.messages` (written by `InteractiveExecutor`) |
| Claude session files | Export | Claude Code harness on the runner (`~/.claude/projects/...`), located by session ID and working directory |
| Codex rollout file | Export | Codex harness on the runner; located by `adapter/codex/rollout.go:findRollout` |
| Harness version for a bundle | Import compatibility | Phase 2 runner harness version probe |
| Exhausted or logged-out classification mid-turn | Mover trigger | Phase 1 classifiers in `adapter/claudecli` and `adapter/codex` |
| Runner lost or draining | Mover trigger | Phase 2 hub liveness; Phase 3 drain |
| Workspace state for the target | Mover | Phase 3 server checkpoint copy and pull |
| Same-account check (to skip Codex reasoning stripping) | Import | **UNRESOLVED DEPENDENCY:** no per-source account fingerprint producer exists. The design strips Codex reasoning on **every** move (safe, slightly lossy) until one exists. |
| Whether Codex checks its stored account ID on resume; how much Claude context is lost across accounts | Import behaviour | **UNRESOLVED DEPENDENCY:** unverified (the research had one account per vendor). The plan must include a two-account verification. The rebuild fallback covers a refusal meanwhile. |
| Turn lease and session generation | Lease, fence | **New** durable lease store (this phase) |

### Deferred to plans
- **Composition-root wiring:**
  - Construct the checkpoint store, the turn-lease store and the mover.
  - Inject the lease into `conversationHandler` in place of `turnInProgress`, and the mover into the conversation handler, `InteractiveExecutor`, drain and recovery.
  - Register the claudecli and codex `SessionPortability` adapters on the built-in runner and in `cmd/arbitron-runner`.
- **SQLite migrations:** the turn lease; `LLMConversation` additions (checkpoint reference, pinned runner, session generation, move history), as JSON fields or columns, chosen in the plan.
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
- **Import or resume failure:** leads to the rebuild fallback plus a notice. If the rebuild also fails, the session **waits for capacity** with its reason. It never fails silently.
- **No eligible target** (same model group): the session waits · capacity, naming the group and the next reset.
- **A concurrent submission** is rejected (`ErrInteractiveTurnInProgress`).
- **A stale-generation result** from the old runner is discarded.
- **A move requested during an in-flight turn** waits for the lease. Moves never run concurrently with a turn.
- **Version skew** between the bundle and the target harness refuses import and leads to the rebuild.

## 8. Testing — STRICT TDD (all five categories required)
Tests are written first for every component.
- **Happy path:** a multi-turn `interactive_agent` session stays pinned to one runner, with a checkpoint after every turn.
- **Success:**
  - Exhaustion between turns moves the session, and the next turn continues with full context.
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
  - A group-staging session never moves.
  - The workspace notice differs for built-in and remote interrupted turns.
  - An expired lease from a crashed holder is reclaimable.
