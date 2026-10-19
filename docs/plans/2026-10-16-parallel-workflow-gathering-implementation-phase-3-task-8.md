# Parallel Workflow Gathering — Phase 3 Task 8: Resolution session bridge

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Open, persist, gate and complete a group conflict-resolution session through the existing interactive machinery: an autonomous first agent attempt in the gather's staging directory, a session identity tied to `(groupRunID, mergeIndex, attempt)`, a conversation gate that admits a `parallel_group` node only while its open session is the active attempt, and a completion path that records the attempt outcome and wakes the waiting parent instead of writing node variables.
**Architecture:** New `domain/engine/group_resolution.go` holds the session identity vocabulary, the `GroupResolutionCompleter`/`ResolutionNotifier`/`GroupSessionCloser` ports and `*GroupResolutionBridge` (implements P3-T6's `GroupResolutionStarter`, the completer, and the closer). `domain/engine/conversation.go` is extended (full replacement below) so `loadAndValidate` recognises an open group session, `HandleMessage` streams into it with `WorkDir` = the persisted staging dir, and `completeSession` dispatches group sessions to the completer (no `vars[nodeID]` write, no unconditional queueing). A new `notification.TypeConflictResolution` is emitted through an exported `Engine.EmitNotification`; migration 020 admits the new type in the notifications CHECK.
**Tech Stack:** Go 1.25.5, existing `domain/engine` interactive plumbing (`sentinel.go`, `kind_guard.go`, `template_include.go`), SQLite migration (hand-written SQL), testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Unresolved conflicts pause gathering and open an interactive resolution session ... Downstream work proceeds only after the conflicts are resolved"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §4 "Interactive resolution bridge" (all bullets), Data Provenance rows "Resolver provider, model, tools", "Human messages / ordinary session end", "Conversation persistence / resolution session link", "Notifications".

## Automatic attempt versus human notification

P3-T6 owns authoritative post-turn `WorkingArea.ConflictPaths` verification. Extend
`GroupResolutionStarter` here with `NotifyResolutionRequired(ctx context.Context,
ref instance.ResolutionSessionRef) error` alongside its existing `StartResolution` and
`CloseSession`; P3-T6 declares this exact method and calls it only on the nonempty-path
branch of `startSession`, after persisting `ActiveSession`. Implement it on
`GroupResolutionBridge`: load the exact run/session via `ref`, verify that it is still
open/current, then invoke `ResolutionNotifier.EmitNotification` with the owning parent
and conflict type. Missing/wrong run/session is an error; the constructor panics on a nil notifier
or any other required collaborator. Keep per-attempt notification delivery idempotent in the persisted conversation
with `notificationSent: true` written after sending (crash replay may redeliver one
notification, never lose the session). P3-T10 retries this method for an open restored
session missing that marker. This is the single notification producer.

Write/run red tests before this change: StartResolution alone persists history but sends
no human notification; `NotifyResolutionRequired` with the returned active session sends
one, replay sends none, wrong identity sends none, and storage/notifier wiring errors are
visible. Update the existing first-turn notification assertion accordingly. A resolved
first turn must close without human action; do not reuse ordinary InteractiveExecutor's
operator-only first-turn completion policy for gathering. P3-T11 boots `initDomain` and
asserts a real automatic repair reaches a downstream persisted file-read variable with
zero `conflict_resolution` notifications; the unresolved fixture still notifies and waits.


## Session-closer port used by recovery

`GroupSessionCloser` includes both `CloseSession(ctx, ref, reason) error` and
`NotifyResolutionRequired(ctx, ref) error`; the latter is mandatory for retrying human
notification after a crash. P3-T10's `recordingCloser` records the full notification ref
and validates it. The real `*GroupResolutionBridge` implements both and P3-T11 injects it
into the reconciler. There is no optional interface assertion or silent missing producer.


**Interfaces**
- Consumes:
  - Existing: `domain/engine/conversation.go:1-593` (whole file, replaced below), `domain/engine/executor_interactive.go:68-75` (short-circuit), `domain/engine/sentinel.go:46` `extractSentinelResult`, `sentinel.go:106` `sentinelInstruction(nonce string, schema any) string`, `domain/engine/kind_guard.go:48` `assertBindingKind(nodeID string, nodeType workflow.NodeType, bindingKind provider.ProfileKind) error`, `domain/engine/conversation.go:110` `publishInteractiveMessage`, `domain/engine/ports.go:78` `InteractiveStreamSelector`, `ports.go:214` `QueueManager`, `domain/provider/provider.go:112` `ProfileResolver`, `domain/engine/engine.go:1245` `(*Engine).emitNotification`, `domain/notification/notification.go:16-26` (Type consts), `domain/engine/conventions.go:20-24` (`ConversationKey*`), `domain/instance/instance.go:19,25` (`ErrNotInteractiveWait`, `ErrInteractiveTurnInProgress`), `domain/engine/authz.go:14,32` (`canView`, `canIntervene`).
  - P1-T1: `workflow.NodeTypeParallelGroup`, `workflow.NodeConfig.Group`.
  - P2-T1/P3-T4: `instance.GroupRun`, `GroupRunRepository` (`GetByParentNode`, `SetGatherReport`), `GatherReport`, `ResolutionAttempt`, `ResolutionSessionRef`, `instance.NodeAddressedWaker` (P2-T4: `SetQueuedIfWaitingAtNode(ctx, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error)`).
  - P2-T8 (`domain/engine/group_events.go`): `EventTypeGroupConflict`.
  - P3-T6: `GroupResolutionStarter`, `ResolutionRequest`, `GatherHandleFromReport`, `findAttempt`, `copyGatherReport`, `ResolutionOutcomeAbandoned`; test helpers `put`, `snapshot`, `update` on `memGroupRuns` (`group_gather_fakes_test.go`). `memGroupRuns` / `newMemGroupRuns` / `callCount` themselves are P2-T8's shared fake (`domain/engine/group_fakes_test.go`), with P3-T4's validating `SetGatherReport`.
  - P3-T7: `LoadGroupResolverPrompt`, `GroupResolverPromptData`; test fake `ownerScopedFileReader`.
- Produces (exact):
  - `notification.TypeConflictResolution notification.Type = "conflict_resolution"`
  - `func (e *Engine) EmitNotification(ctx context.Context, instanceID uuid.UUID, workflowName string, notifType notification.Type)`
  - `type GroupResolutionCompleter interface { OnResolutionSessionEnded(ctx context.Context, instanceID uuid.UUID, nodeID string, abandoned bool) error }` (contract)
  - `type ResolutionNotifier interface { EmitNotification(ctx context.Context, instanceID uuid.UUID, workflowName string, notifType notification.Type) }` (`*Engine` satisfies)
  - `type GroupSessionCloser interface { CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error; NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error }` (P3-T10 reconciler)
  - `type GroupResolutionBridge struct{...}`; `func NewGroupResolutionBridge(groups instance.GroupRunRepository, instances instance.InstanceRepository, nodeExecs instance.NodeExecutionRepository, resolver provider.ProfileResolver, selector InteractiveStreamSelector, files FileReader, notifier ResolutionNotifier, bus EventBus, waker instance.NodeAddressedWaker, queueMgr QueueManager) *GroupResolutionBridge` with methods `StartResolution`, `OnResolutionSessionEnded`, `CloseSession`, `NotifyResolutionRequired`.
  - Session identity: `const ConversationKeyKind = "kind"`, `ConversationKindGroupResolution = "group_resolution"`, `ConversationKeyGroupRunID = "groupRunId"`, `ConversationKeyMergeIndex = "mergeIndex"`, `ConversationKeyAttempt = "attempt"`, `ConversationKeyTaskKey = "taskKey"`, `ConversationKeyConflictPaths = "conflictPaths"`, `ConversationKeyEndedAt = "endedAt"`, `ConversationKeyEndReason = "endReason"`; `type GroupResolutionIdentity struct { GroupRunID uuid.UUID; MergeIndex int; Attempt int; TaskKey string; Open bool }`; `func GroupResolutionIdentityOf(conv map[string]any) (GroupResolutionIdentity, bool)`; `func IsOpenGroupResolutionSession(conv map[string]any) bool` (P3-T9 WS history).
  - On `*conversationHandler` (concrete, reached by type assertion like `SetInterventions`): `func (h *conversationHandler) SetGroupResolution(groups instance.GroupRunRepository, completer GroupResolutionCompleter)`.
  - Session row contract: the session is a `NodeExecution` on the PARENT at the group node with `Status = succeeded`, `Attempt = (max existing attempt for that node) + 1` (so every existing `GetByInstanceAndNode` reader — `ORDER BY attempt DESC LIMIT 1`, `adapter/sqlite/queries/node_executions.sql:14` — returns the newest session: MCP `interactive_get_history`, WS history), and `LLMConversation = {"kind":"group_resolution","groupRunId","mergeIndex","attempt","taskKey","conflictPaths","sessionId","nonce","messages"[, "endedAt","endReason"]}`. Side effect: the queue row's retry count (`enricher.go:226`, max attempt) counts resolution sessions as attempts of the group node.
  - Test fakes reused BY NAME by P3-T9/P3-T10: `recordingNotifier`, `fakeNodeWaker`, `recordingCompleter`, consts `groupResolverProfile`, `groupResolverModel`, `groupStagingDir`.

**Wiring & data provenance**
- Composition root: **P3-T11** (`cmd/arbitron/main.go`): `groupResolution := engine.NewGroupResolutionBridge(groupRunRepo, instanceRepo, nodeExecRepo, profileResolver, interactiveStreamSel, fileReader, eng, bus, queueRepo, queueMgr)`; `conversationHandler.(interface{ SetGroupResolution(...) }).SetGroupResolution(groupRunRepo, groupResolution)`; the bridge is the `GroupResolutionStarter` passed to `engine.NewGroupGatherer`.
- Producers of every input:
  - `run`, `parent`, `req` — `FileGatherer.startSession` (P3-T6), from the run loaded by the executor (P2-T11/P3-T10) and the persisted `GatherReport` attempt.
  - `run.Config["resolverProfile"]` / `["resolverPromptFile"]` — `GroupLauncher.Launch` (P2-T8) snapshot of `workflow.GroupConfig` (P1-T1), validated at authoring time by P1-T3 (`GROUP_RESOLVER_PROFILE_KIND`, `GROUP_MISSING_PROMPT_FILE`).
  - `req.Handle.StagingDir` — `WorkingArea.BeginGather` (P3-T3), persisted in `GatherReport.Handle` by P3-T6; read back by the conversation gate via `GatherHandleFromReport`.
  - `resolver` — `adapter/provider/dynamic.go:DynamicProfileResolver` (`profileResolver`, main.go:705); `selector` — `providersadapter.NewInteractiveStreamSelector` (`interactiveStreamSel`, main.go:991); `files` — `fileReader` (main.go:449); `notifier` — `eng` (main.go:808) via `Engine.EmitNotification` → `notifRepo` (main.go:886) + `externalNotifier` (main.go:916); `waker` — `*sqlite.QueueRepo` (`queueRepo`, main.go:805; `SetQueuedIfWaitingAtNode` added by P2-T4); `queueMgr` — main.go:840; `nodeExecs` / `instances` — main.go:372-374.
  - Human turns / ends — `adapter/mcp/tools_interactive.go`, `adapter/http/interactive_handler.go`, `adapter/http/stream_handler.go` (existing) → `ConversationHandler` (this task's gate).
- Provenance probes: `TestGroupResolutionBridge_StartResolution_RunsAutonomousAttemptInStagingDir` (fails if `WorkDir` is not the persisted staging dir, if the session is not persisted on the parent's group node, or if the notification sink receives nothing), `TestConversationHandler_Group_UnwiredHandlerKeepsGroupsNonInteractive` (fails if the gate opens without the composition-root setter), `TestGroupResolutionBridge_OnEnded_WakesWaitingParentAtNode` (asserts the SINK: parent row moved to `queued`).

### Task 8: Resolution session bridge
**Files:**
- Modify: `domain/notification/notification.go:16-26` (add `TypeConflictResolution`)
- Modify: `adapter/sqlite/migrations/020_group_gather.up.sql` (created by P3-T4 — APPEND the notifications rebuild at the end)
- Modify: `adapter/sqlite/migrations/020_group_gather.down.sql` (created by P3-T4 — PREPEND the revert at the very top)
- Create: `domain/engine/group_resolution.go`
- Modify: `domain/engine/conversation.go:1-593` (full replacement)
- Modify: `domain/engine/executor_interactive.go:68-75` (short-circuit guard)
- Test:   `adapter/sqlite/notification_conflict_type_test.go` (package `sqlite`)
- Test:   `domain/engine/group_resolution_test.go` (package `engine_test`)
- Test:   `domain/engine/conversation_group_test.go` (package `engine_test`)
- Test:   `domain/engine/executor_interactive_group_test.go` (package `engine_test`)

Migration ownership (settled with P3-T4): `notifications.type` has a CHECK constraint (`adapter/sqlite/migrations/013_schedules.up.sql:38-42`, the last migration to touch `notifications`; 014–019 do not) that rejects any new type; Phase 3 owns only migration 020. P3-T4 creates `020_group_gather.{up,down}.sql` with only its two `ALTER TABLE … ADD COLUMN` statements (up) and `SELECT 1;` (down) and explicitly does not touch `notifications`; THIS task is the single owner of the `notifications` rebuild — it APPENDS the rebuild to `020_group_gather.up.sql` and PREPENDS its revert to `020_group_gather.down.sql`. The rebuild runs inside `RunMigrations`' per-file transaction with `PRAGMA foreign_keys=ON` (`adapter/sqlite/db.go:39`); that is safe because no table has a FOREIGN KEY to `notifications` and no trigger/view references it (`grep -rn "REFERENCES notifications\|CREATE TRIGGER\|CREATE VIEW" adapter/sqlite/migrations/` is empty). `instances` is never rebuilt.

#### 8.1 Notification type + CHECK admission

- [x] Step 1: Write the failing persistence test. Create `adapter/sqlite/notification_conflict_type_test.go`:

```go
package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The conflict-resolution notification (P3-T8) must pass the notifications
// CHECK constraint and round-trip with its instance and workflow name.
func TestNotificationRepo_ConflictResolution_Persists(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()
	instID := seedInstance(t, db, owner, "waiting")

	n := &notification.Notification{ID: uuid.New(), InstanceID: instID, Type: notification.TypeConflictResolution}
	require.NoError(t, repo.Create(ctx, n))

	got, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, notification.TypeConflictResolution, got[0].Type)
	assert.Equal(t, instID, got[0].InstanceID)
	assert.Equal(t, "wf", got[0].WorkflowName)
}

// Every pre-existing type still passes after the rebuild.
func TestNotificationRepo_ConflictResolution_RebuildKeepsOtherTypes(t *testing.T) {
	repo, db, owner := newNotifRepo(t)
	ctx := context.Background()
	instID := seedInstance(t, db, owner, "failed")
	for _, typ := range []notification.Type{
		notification.TypeNeedsInteraction, notification.TypeFailed, notification.TypeCompleted, notification.TypeConflictResolution,
	} {
		require.NoErrorf(t, repo.Create(ctx, &notification.Notification{ID: uuid.New(), InstanceID: instID, Type: typ}), "type %q", typ)
	}
	for _, typ := range []notification.Type{notification.TypeScheduleSkipped, notification.TypeScheduleMissed} {
		require.NoErrorf(t, repo.Create(ctx, &notification.Notification{ID: uuid.New(), WorkflowName: "sched", Type: typ}), "type %q", typ)
	}
	all, err := repo.List(ctx, notification.ListFilters{})
	require.NoError(t, err)
	assert.Len(t, all, 6)
}
```

- [x] Step 2: Run it; confirm it fails.

```bash
go test ./adapter/sqlite/ -run 'TestNotificationRepo_ConflictResolution' -count=1
```
Expected:
```
adapter/sqlite/notification_conflict_type_test.go:...: undefined: notification.TypeConflictResolution
FAIL	github.com/arbitron/arbitron/adapter/sqlite [build failed]
```

- [x] Step 3: Add the type. In `domain/notification/notification.go`, replace lines 16-26 (the `const (...)` block) with:

```go
const (
	TypeNeedsInteraction Type = "needs_interaction"
	TypeFailed           Type = "failed"
	TypeCompleted        Type = "completed"
	// TypeScheduleSkipped / TypeScheduleMissed are raised by the schedule
	// evaluator (org-readiness Phase 3). They have NO instance: a scheduled run
	// did not happen (collision skip, or downtime miss). The notifications table
	// stores them with a NULL instance_id and the workflow name denormalized.
	TypeScheduleSkipped Type = "schedule_skipped"
	TypeScheduleMissed  Type = "schedule_missed"
	// TypeConflictResolution is raised when a parallel group's file gather hit
	// merge conflicts and opened a resolution session that needs a human
	// (parallel-workflow-gathering Phase 3). It is tied to the PARENT instance.
	TypeConflictResolution Type = "conflict_resolution"
)
```

- [x] Step 4: Run the test again; it now compiles but the CHECK rejects the row.

```bash
go test ./adapter/sqlite/ -run 'TestNotificationRepo_ConflictResolution' -count=1
```
Expected:
```
--- FAIL: TestNotificationRepo_ConflictResolution_Persists (0.0Xs)
    notification_conflict_type_test.go:...: Error: Received unexpected error: ... CHECK constraint failed ...
FAIL
```

- [x] Step 5: Admit the type in migration 020. APPEND to the END of `adapter/sqlite/migrations/020_group_gather.up.sql`:

```sql

-- P3-T8: admit the conflict_resolution notification type. SQLite cannot alter a
-- CHECK constraint in place, so the notifications table is rebuilt with its
-- data preserved, exactly as migration 013 did. Safe inside the migration
-- transaction with foreign_keys=ON only because nothing references
-- notifications (see the warning in 013_schedules.up.sql).
CREATE TABLE notifications_new (
    id            TEXT PRIMARY KEY,
    instance_id   TEXT REFERENCES instances(id),
    type          TEXT NOT NULL CHECK (type IN (
                      'needs_interaction', 'failed', 'completed',
                      'schedule_skipped', 'schedule_missed',
                      'conflict_resolution')),
    workflow_name TEXT NOT NULL DEFAULT '',
    read          INTEGER NOT NULL DEFAULT 0,
    dismissed     INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

INSERT INTO notifications_new (id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at)
SELECT id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at FROM notifications;

DROP TABLE notifications;
ALTER TABLE notifications_new RENAME TO notifications;

CREATE INDEX IF NOT EXISTS idx_notifications_active
    ON notifications (read, dismissed, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_instance
    ON notifications (instance_id);
```

PREPEND to the VERY TOP of `adapter/sqlite/migrations/020_group_gather.down.sql` (before P3-T4's statements):

```sql
-- Revert P3-T8: restore the migration-013 notifications CHECK. Rows of the
-- conflict_resolution type cannot be represented downgraded and are dropped.
CREATE TABLE notifications_old (
    id            TEXT PRIMARY KEY,
    instance_id   TEXT REFERENCES instances(id),
    type          TEXT NOT NULL CHECK (type IN (
                      'needs_interaction', 'failed', 'completed',
                      'schedule_skipped', 'schedule_missed')),
    workflow_name TEXT NOT NULL DEFAULT '',
    read          INTEGER NOT NULL DEFAULT 0,
    dismissed     INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ'))
);

INSERT INTO notifications_old (id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at)
SELECT id, instance_id, type, workflow_name, read, dismissed, created_at, updated_at
FROM notifications
WHERE type <> 'conflict_resolution';

DROP TABLE notifications;
ALTER TABLE notifications_old RENAME TO notifications;

CREATE INDEX IF NOT EXISTS idx_notifications_active
    ON notifications (read, dismissed, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_notifications_instance
    ON notifications (instance_id);

```

- [x] Step 6: Run the sqlite package.

```bash
go test ./adapter/sqlite/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/adapter/sqlite	...
```

- [x] Step 7: Commit.

  Notification slice red/green: undefined type → CHECK rejection → whole SQLite package green. Self-review in order: (1) only the specified type/migration changes; (2) tests assert actual repository round trips with nonempty instance/workflow data and all existing types; (3) matched migration 013's schema/indexes and safe-rebuild pattern, checked no references to notifications, formatted and checked diff. Runtime notification production remains part of the bridge and P3-T11 completion gate below.

```bash
git add domain/notification/notification.go adapter/sqlite/migrations/020_group_gather.up.sql adapter/sqlite/migrations/020_group_gather.down.sql adapter/sqlite/notification_conflict_type_test.go
git commit -m "feat(notification): conflict_resolution notification type admitted by migration 020" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### 8.2 The bridge (start, complete, close) and Engine.EmitNotification

- [x] Step 8: Write the failing bridge tests. Create `domain/engine/group_resolution_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	groupResolverProfile = "group-resolver"
	groupResolverModel   = "resolver-model"
	groupStagingDir      = "/ws/.group-backing/run/staging/gather-1"
)

// recordingNotifier is an engine.ResolutionNotifier that records what it was asked to raise.
type recordedNotification struct {
	instanceID   uuid.UUID
	workflowName string
	typ          notification.Type
}

type recordingNotifier struct {
	mu  sync.Mutex
	got []recordedNotification
}

var _ engine.ResolutionNotifier = (*recordingNotifier)(nil)

func (n *recordingNotifier) EmitNotification(_ context.Context, instanceID uuid.UUID, workflowName string, t notification.Type) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.got = append(n.got, recordedNotification{instanceID: instanceID, workflowName: workflowName, typ: t})
}

func (n *recordingNotifier) all() []recordedNotification {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]recordedNotification(nil), n.got...)
}

// fakeNodeWaker mirrors QueueRepo.SetQueuedIfWaitingAtNode against a
// mockInstanceRepo: it re-queues ONLY an instance that is waiting AT nodeID.
type wakeCall struct {
	id     uuid.UUID
	nodeID string
}

type fakeNodeWaker struct {
	mu        sync.Mutex
	instances *mockInstanceRepo
	calls     []wakeCall
	err       error
}

var _ instance.NodeAddressedWaker = (*fakeNodeWaker)(nil)

func (w *fakeNodeWaker) SetQueuedIfWaitingAtNode(_ context.Context, id uuid.UUID, nodeID string, queuedAt time.Time) (bool, error) {
	w.mu.Lock()
	w.calls = append(w.calls, wakeCall{id: id, nodeID: nodeID})
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return false, err
	}
	w.instances.mu.Lock()
	defer w.instances.mu.Unlock()
	inst, ok := w.instances.instances[id]
	if !ok || inst.State != instance.StateWaiting || inst.CurrentNodeID == nil || *inst.CurrentNodeID != nodeID {
		return false, nil
	}
	inst.State = instance.StateQueued
	q := queuedAt
	inst.QueuedAt = &q
	return true, nil
}

func (w *fakeNodeWaker) wakeCalls() []wakeCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]wakeCall(nil), w.calls...)
}

// recordingCompleter is an engine.GroupResolutionCompleter that records calls.
type completerCall struct {
	instanceID uuid.UUID
	nodeID     string
	abandoned  bool
}

type recordingCompleter struct {
	mu    sync.Mutex
	calls []completerCall
	err   error
}

var _ engine.GroupResolutionCompleter = (*recordingCompleter)(nil)

func (c *recordingCompleter) OnResolutionSessionEnded(_ context.Context, instanceID uuid.UUID, nodeID string, abandoned bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, completerCall{instanceID: instanceID, nodeID: nodeID, abandoned: abandoned})
	return c.err
}

func (c *recordingCompleter) all() []completerCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]completerCall(nil), c.calls...)
}

func groupResolverResolver() *fakeResolver {
	return newFakeResolver(
		provider.Profile{Alias: groupResolverProfile, Kind: provider.ProfileKindAgent, Model: groupResolverModel, AllowedTools: []string{"Read", "Edit"}},
		provider.Profile{Alias: "chatty", Kind: provider.ProfileKindChat, Model: "chat-model"},
	)
}

func stagingHandleMap(gatherID uuid.UUID) map[string]any {
	return map[string]any{
		"snapshotHandle": "snap-1", "gatherId": gatherID.String(), "stagingDir": groupStagingDir,
		"rollbackRef": "refs/arbitron/rollback/x", "parentDir": "/ws/parent",
	}
}

type bridgeFixture struct {
	t         *testing.T
	groups    *memGroupRuns
	instances *mockInstanceRepo
	nodeExecs *mockNodeExecutionRepo
	agent     *mockStreamingChatProvider
	chat      *mockStreamingChatProvider
	selector  *fakeStreamSelector
	files     *ownerScopedFileReader
	notifier  *recordingNotifier
	bus       *mockEventBus
	waker     *fakeNodeWaker
	queue     *mockQueueManager
	bridge    *engine.GroupResolutionBridge
	runID     uuid.UUID
	parentID  uuid.UUID
	ownerID   uuid.UUID
	handle    engine.GatherHandle
	waitTrace uuid.UUID
}

// newBridgeFixture: a parent RUNNING at group node g1 (the gatherer runs
// inside the parent's executor), its wait trace (attempt 1), and a run in
// resolving_conflicts whose report holds attempt 1 of merge 1 (task b).
func newBridgeFixture(t *testing.T) *bridgeFixture {
	t.Helper()
	f := &bridgeFixture{
		t: t, groups: newMemGroupRuns(), instances: newMockInstanceRepo(), nodeExecs: newMockNodeExecutionRepo(),
		agent: newMockStreamingChatProvider(), chat: newMockStreamingChatProvider(),
		files: newOwnerScopedFileReader(), notifier: &recordingNotifier{}, bus: newMockEventBus(), queue: newMockQueueManager(),
		parentID: uuid.New(), ownerID: uuid.New(),
	}
	f.selector = &fakeStreamSelector{chatProv: f.chat, agentProv: f.agent}
	f.waker = &fakeNodeWaker{instances: f.instances}
	node := "g1"
	f.instances.instances[f.parentID] = &instance.Instance{
		ID: f.parentID, OwnerID: f.ownerID, WorkflowName: "wf", State: instance.StateRunning,
		CurrentNodeID: &node, Variables: map[string]any{"workspace": "/ws/parent"},
	}
	gatherID := uuid.New()
	f.handle = engine.GatherHandle{SnapshotHandle: "snap-1", GatherID: gatherID, StagingDir: groupStagingDir, RollbackRef: "refs/arbitron/rollback/x", ParentDir: "/ws/parent"}
	run := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: f.parentID, NodeID: "g1", Attempt: 1,
		CompletionRule: workflow.GroupRuleAllSucceed, GatherMode: workflow.GroupGatherResultsAndFiles,
		Config:            map[string]any{"resolverProfile": groupResolverProfile},
		WorkspaceSnapshot: "snap-1", State: instance.GroupRunResolvingConflicts,
		Gather: &instance.GatherReport{
			Mode: workflow.GroupGatherResultsAndFiles, State: "resolving", MergeIndex: 1,
			Handle: stagingHandleMap(gatherID),
			Tasks:  []instance.GatherTaskReport{{Key: "a", Merged: true, Reason: "merged cleanly"}, {Key: "b", ConflictPaths: []string{"shared.txt"}}},
			Attempts: []instance.ResolutionAttempt{
				{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"shared.txt"}, StartedAt: time.Now().UTC()},
			},
		},
	}
	f.runID = run.ID
	f.groups.put(run)
	f.waitTrace = uuid.New()
	require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
		ID: f.waitTrace, InstanceID: f.parentID, NodeID: "g1", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"groupRunId": run.ID.String(), "state": "waiting"}, StartedAt: time.Now(),
	}))
	f.bridge = engine.NewGroupResolutionBridge(f.groups, f.instances, f.nodeExecs, groupResolverResolver(), f.selector, f.files, f.notifier, f.bus, f.waker, f.queue)
	return f
}

func (f *bridgeFixture) parent() *instance.Instance {
	f.t.Helper()
	p, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(f.t, err)
	return p
}

func (f *bridgeFixture) request(attempt int) engine.ResolutionRequest {
	return engine.ResolutionRequest{Handle: f.handle, MergeIndex: 1, Attempt: attempt, TaskKey: "b", TaskRef: "refs/arbitron/tasks/b", ConflictPaths: []string{"shared.txt"}}
}

func (f *bridgeFixture) start(attempt int) (*instance.ResolutionSessionRef, error) {
	return f.bridge.StartResolution(context.Background(), f.groups.snapshot(f.runID), f.parent(), f.request(attempt))
}

// sessionRows returns the parent's group_resolution rows at g1 in creation order.
func (f *bridgeFixture) sessionRows() []instance.NodeExecution {
	execs, err := f.nodeExecs.ListByInstance(context.Background(), f.parentID)
	require.NoError(f.t, err)
	var out []instance.NodeExecution
	for _, e := range execs {
		if e.NodeID == "g1" && e.LLMConversation != nil && e.LLMConversation["kind"] == engine.ConversationKindGroupResolution {
			out = append(out, e)
		}
	}
	return out
}

func (f *bridgeFixture) setActiveSession() {
	f.groups.update(f.runID, func(r *instance.GroupRun) {
		r.Gather.ActiveSession = &instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: f.parentID, NodeID: "g1"}
	})
}

func (f *bridgeFixture) setParentState(s instance.State, node string) {
	f.instances.mu.Lock()
	defer f.instances.mu.Unlock()
	f.instances.instances[f.parentID].State = s
	f.instances.instances[f.parentID].CurrentNodeID = &node
}

// --- StartResolution --------------------------------------------------------

func TestGroupResolutionBridge_StartResolution_RunsAutonomousAttemptInStagingDir(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"Resolved ", "shared.txt."}, "agent-sess-1")

	ref, err := f.start(1)
	require.NoError(t, err)
	assert.Equal(t, instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: f.parentID, NodeID: "g1"}, *ref)

	reqs := f.agent.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, groupStagingDir, reqs[0].WorkDir, "the agent works in the gather's STAGING dir")
	assert.Equal(t, groupResolverModel, reqs[0].Model)
	assert.Equal(t, []string{"Read", "Edit"}, reqs[0].ToolScope)
	assert.Empty(t, reqs[0].SessionID, "the autonomous attempt starts a fresh provider session")
	assert.Contains(t, reqs[0].Prompt, "task `b`")
	assert.Contains(t, reqs[0].Prompt, "- shared.txt")
	assert.Contains(t, reqs[0].Prompt, "`MERGE_HEAD`")
	assert.Contains(t, reqs[0].Prompt, "<arbitron:result:")
	assert.Empty(t, f.chat.getCapturedReqs(), "the tool-suppressed chat stream is never used")
	assert.Equal(t, provider.ProfileKindAgent, f.selector.gotKind)

	rows := f.sessionRows()
	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, f.parentID, row.InstanceID, "persisted on the PARENT")
	assert.Equal(t, 2, row.Attempt, "one past the group node's newest row (the wait trace)")
	assert.Equal(t, instance.NodeStatusSucceeded, row.Status)
	conv := row.LLMConversation
	assert.Equal(t, f.runID.String(), conv["groupRunId"])
	assert.EqualValues(t, 1, conv["mergeIndex"])
	assert.EqualValues(t, 1, conv["attempt"])
	assert.Equal(t, "b", conv["taskKey"])
	assert.Equal(t, "agent-sess-1", conv["sessionId"])
	nonce, _ := conv["nonce"].(string)
	require.NotEmpty(t, nonce)
	assert.Contains(t, reqs[0].Prompt, engine.SentinelOpenTag(nonce), "the persisted nonce scopes the sentinel the agent was told to emit")
	msgs := extractMessages(t, conv)
	require.Len(t, msgs, 2)
	assert.Equal(t, "Resolved shared.txt.", msgs[1]["content"])
	ident, ok := engine.GroupResolutionIdentityOf(conv)
	require.True(t, ok)
	assert.True(t, ident.Open)

	assert.Empty(t, f.notifier.all(), "the first turn alone does not require a human")
	f.setActiveSession()
	require.NoError(t, f.bridge.NotifyResolutionRequired(context.Background(), *ref))
	require.NoError(t, f.bridge.NotifyResolutionRequired(context.Background(), *ref))
	assert.Equal(t, []recordedNotification{{instanceID: f.parentID, workflowName: "wf", typ: notification.TypeConflictResolution}}, f.notifier.all())
	conflicts := f.bus.eventsOfType(f.parentID, engine.EventTypeGroupConflict)
	require.Len(t, conflicts, 1)
	data := conflicts[0].Data.(map[string]any)
	assert.Equal(t, f.runID.String(), data["groupRunId"])
	assert.Equal(t, "g1", data["nodeId"])
	assert.Equal(t, "b", data["taskKey"])
	assert.Equal(t, 1, data["attempt"])
	assert.Len(t, f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveToken), 2)
	assert.Len(t, f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveMessage), 1)

	trace := f.nodeExecs.executions[0]
	assert.Equal(t, f.waitTrace, trace.ID)
	assert.Nil(t, trace.LLMConversation, "the wait trace is never rewritten")
}

func TestGroupResolutionBridge_StartResolution_FreshAttemptGetsANewRow(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"try"}, "s")

	_, err := f.start(1)
	require.NoError(t, err)
	_, err = f.start(2)
	require.NoError(t, err)

	rows := f.sessionRows()
	require.Len(t, rows, 2)
	assert.Equal(t, 2, rows[0].Attempt)
	assert.Equal(t, 3, rows[1].Attempt)
	assert.EqualValues(t, 2, rows[1].LLMConversation["attempt"])
}

func TestGroupResolutionBridge_StartResolution_ChatProfileFailsClosed(t *testing.T) {
	f := newBridgeFixture(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = "chatty" })

	_, err := f.start(1)
	require.Error(t, err)
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.Empty(t, f.agent.getCapturedReqs())
	assert.Empty(t, f.chat.getCapturedReqs())
	assert.Empty(t, f.sessionRows())
	assert.Empty(t, f.notifier.all())
}

func TestGroupResolutionBridge_StartResolution_ProfileErrors(t *testing.T) {
	t.Run("unknown profile", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = "nope" })
		_, err := f.start(1)
		assert.ErrorIs(t, err, provider.ErrUnknownProfile)
	})
	t.Run("no profile configured", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.groups.update(f.runID, func(r *instance.GroupRun) { delete(r.Config, "resolverProfile") })
		_, err := f.start(1)
		assert.ErrorContains(t, err, "has no resolverProfile")
	})
}

func TestGroupResolutionBridge_StartResolution_StreamFailuresPersistNothing(t *testing.T) {
	t.Run("stream error", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			ch := make(chan engine.ChatChunk, 1)
			ch <- engine.ChatChunk{Error: errors.New("claude exited 1")}
			close(ch)
			return ch, nil
		}
		_, err := f.start(1)
		assert.ErrorContains(t, err, "claude exited 1")
		assert.Empty(t, f.sessionRows())
		assert.Empty(t, f.notifier.all())
		assert.Empty(t, f.bus.eventsOfType(f.parentID, engine.EventTypeGroupConflict))
	})
	t.Run("stream closes without completion", func(t *testing.T) {
		f := newBridgeFixture(t)
		f.agent.streamCallFn = func(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
			ch := make(chan engine.ChatChunk, 1)
			ch <- engine.ChatChunk{Token: "partial"}
			close(ch)
			return ch, nil
		}
		_, err := f.start(1)
		assert.ErrorContains(t, err, "stream closed without completion")
		assert.Empty(t, f.sessionRows())
	})
}

func TestGroupResolutionBridge_StartResolution_OverridePromptReadUnderParentOwner(t *testing.T) {
	f := newBridgeFixture(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverPromptFile"] = "prompts/resolve.md" })
	f.files.put(f.ownerID, "wf", "prompts/resolve.md", "CUSTOM resolve {{taskKey}} in {{workDir}}")
	f.agent.streamCallFn = cannedStreamWithSession([]string{"ok"}, "s")

	_, err := f.start(1)
	require.NoError(t, err)
	reqs := f.agent.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(reqs[0].Prompt, "CUSTOM resolve b in "+groupStagingDir))
	assert.Equal(t, []string{ownerFileKey(f.ownerID, "wf", "prompts/resolve.md")}, f.files.readKeys())
}

func TestGroupResolutionBridge_StartResolution_MissingOverrideStartsNothing(t *testing.T) {
	f := newBridgeFixture(t)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverPromptFile"] = "prompts/missing.md" })

	_, err := f.start(1)
	assert.ErrorIs(t, err, engine.ErrGroupResolverPrompt)
	assert.Empty(t, f.agent.getCapturedReqs())
}

func TestGroupResolutionBridge_StartResolution_RejectsIncompleteRequests(t *testing.T) {
	f := newBridgeFixture(t)
	req := f.request(1)
	req.Handle.StagingDir = ""
	_, err := f.bridge.StartResolution(context.Background(), f.groups.snapshot(f.runID), f.parent(), req)
	assert.ErrorContains(t, err, "no staging directory")

	req = f.request(1)
	req.ConflictPaths = nil
	_, err = f.bridge.StartResolution(context.Background(), f.groups.snapshot(f.runID), f.parent(), req)
	assert.ErrorContains(t, err, "no conflicted paths")

	_, err = f.bridge.StartResolution(context.Background(), nil, f.parent(), f.request(1))
	assert.Error(t, err)
}

// --- OnResolutionSessionEnded (the completer) -------------------------------

func TestGroupResolutionBridge_OnEnded_WakesWaitingParentAtNode(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.parentID, "g1", false))

	rep := f.groups.snapshot(f.runID).Gather
	require.NotNil(t, rep.Attempts[0].EndedAt)
	assert.Equal(t, "", rep.Attempts[0].Outcome, "an ordinary end leaves the outcome to the gatherer's verification")
	assert.Equal(t, []wakeCall{{id: f.parentID, nodeID: "g1"}}, f.waker.wakeCalls())
	assert.Equal(t, instance.StateQueued, f.parent().State, "SINK: the parent row is re-queued")
	assert.Equal(t, 1, f.queue.notifyCount())
}

func TestGroupResolutionBridge_OnEnded_AbandonRecordsOutcome(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.parentID, "g1", true))
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, f.groups.snapshot(f.runID).Gather.Attempts[0].Outcome)
	assert.Equal(t, instance.StateQueued, f.parent().State)
}

func TestGroupResolutionBridge_OnEnded_IsIdempotent(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")
	ctx := context.Background()

	require.NoError(t, f.bridge.OnResolutionSessionEnded(ctx, f.parentID, "g1", false))
	first := f.groups.snapshot(f.runID).Gather.Attempts[0].EndedAt
	reports := f.groups.callCount("SetGatherReport")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(ctx, f.parentID, "g1", true))
	assert.Equal(t, first, f.groups.snapshot(f.runID).Gather.Attempts[0].EndedAt)
	assert.Equal(t, "", f.groups.snapshot(f.runID).Gather.Attempts[0].Outcome, "a repeated (even abandoning) end cannot rewrite an ended attempt")
	assert.Equal(t, reports, f.groups.callCount("SetGatherReport"))
	assert.Len(t, f.waker.wakeCalls(), 1)
}

func TestGroupResolutionBridge_OnEnded_NeverWakesACancelledParent(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateCancelled, "g1")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.parentID, "g1", true))
	assert.NotNil(t, f.groups.snapshot(f.runID).Gather.Attempts[0].EndedAt, "the outcome is still recorded")
	assert.Empty(t, f.waker.wakeCalls())
	assert.Equal(t, instance.StateCancelled, f.parent().State)
	assert.Zero(t, f.queue.notifyCount())
}

func TestGroupResolutionBridge_OnEnded_ParentElsewhereIsNotWoken(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "later-node")

	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.parentID, "g1", false))
	assert.Equal(t, instance.StateWaiting, f.parent().State)
	assert.Zero(t, f.queue.notifyCount())
}

func TestGroupResolutionBridge_OnEnded_NoRunOrNoActiveSessionIsANoOp(t *testing.T) {
	f := newBridgeFixture(t)
	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), f.parentID, "g1", false), "no active session")
	require.NoError(t, f.bridge.OnResolutionSessionEnded(context.Background(), uuid.New(), "g1", false), "no run")
	assert.Zero(t, f.groups.callCount("SetGatherReport"))
	assert.Empty(t, f.waker.wakeCalls())
}

func TestGroupResolutionBridge_OnEnded_WakerErrorSurfaces(t *testing.T) {
	f := newBridgeFixture(t)
	f.setActiveSession()
	f.setParentState(instance.StateWaiting, "g1")
	f.waker.err = errors.New("database is locked")

	err := f.bridge.OnResolutionSessionEnded(context.Background(), f.parentID, "g1", false)
	assert.ErrorContains(t, err, "database is locked")
}

// --- CloseSession -----------------------------------------------------------

func TestGroupResolutionBridge_CloseSession_MarksTheRowEndedOnce(t *testing.T) {
	f := newBridgeFixture(t)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"ok"}, "s")
	ref, err := f.start(1)
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, f.bridge.CloseSession(ctx, *ref, "cancelled"))
	row := f.sessionRows()[0]
	assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation))
	assert.Equal(t, "cancelled", row.LLMConversation["endReason"])
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	assert.Equal(t, "cancelled", ended[0].Data.(map[string]any)["reason"])

	require.NoError(t, f.bridge.CloseSession(ctx, *ref, "error"), "closing an ended session is a no-op")
	assert.Equal(t, "cancelled", f.sessionRows()[0].LLMConversation["endReason"])
	assert.Len(t, f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded), 1)

	require.NoError(t, f.bridge.CloseSession(ctx, instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 9, Attempt: 9, InstanceID: f.parentID, NodeID: "g1"}, "x"),
		"an unknown session is a no-op")
}

// --- identity helpers + Engine.EmitNotification ----------------------------

func TestGroupResolutionIdentityOf(t *testing.T) {
	id := uuid.New()
	conv := map[string]any{"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": float64(2), "attempt": float64(3), "taskKey": "b"}
	got, ok := engine.GroupResolutionIdentityOf(conv)
	require.True(t, ok)
	assert.Equal(t, engine.GroupResolutionIdentity{GroupRunID: id, MergeIndex: 2, Attempt: 3, TaskKey: "b", Open: true}, got)

	conv["endedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	got, ok = engine.GroupResolutionIdentityOf(conv)
	require.True(t, ok)
	assert.False(t, got.Open)
	assert.False(t, engine.IsOpenGroupResolutionSession(conv))

	for name, bad := range map[string]map[string]any{
		"nil":             nil,
		"ordinary":        {"nonce": "n", "messages": []any{}},
		"bad run id":      {"kind": "group_resolution", "groupRunId": "nope", "mergeIndex": 0, "attempt": 1},
		"missing attempt": {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": 0},
		"fractional":      {"kind": "group_resolution", "groupRunId": id.String(), "mergeIndex": 0.5, "attempt": 1},
	} {
		_, ok := engine.GroupResolutionIdentityOf(bad)
		assert.False(t, ok, name)
	}
}

func TestEngine_EmitNotification_CreatesAndPublishes(t *testing.T) {
	bus := newMockEventBus()
	repo := newMockNotificationRepo()
	eng := engine.NewEngine(nil, newMockInstanceRepo(), newMockNodeExecutionRepo(), newMockSnapshotRepo(), newMockUserRepo(),
		map[workflow.NodeType]engine.NodeExecutor{}, bus, newMockQueueManager(), nil, "")
	eng.SetNotifications(repo)
	id := uuid.New()

	eng.EmitNotification(context.Background(), id, "wf", notification.TypeConflictResolution)

	require.Len(t, repo.created, 1)
	assert.Equal(t, notification.TypeConflictResolution, repo.created[0].Type)
	assert.Equal(t, id, repo.created[0].InstanceID)
	assert.Equal(t, "wf", repo.created[0].WorkflowName)
	assert.Len(t, bus.eventsOfType(id, "notification.created"), 1)
}
```

- [x] Step 9: Run them; confirm they fail.

```bash
go test ./domain/engine/ -run 'TestGroupResolutionBridge|TestGroupResolutionIdentityOf|TestEngine_EmitNotification' -count=1
```
Expected (compile failure):
```
domain/engine/group_resolution_test.go:...: undefined: engine.ResolutionNotifier
domain/engine/group_resolution_test.go:...: undefined: engine.GroupResolutionCompleter
domain/engine/group_resolution_test.go:...: undefined: engine.GroupResolutionBridge
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 10: Implement. Create `domain/engine/group_resolution.go`:

```go
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Group conflict-resolution session identity, carried inside the session's
// NodeExecution.LLMConversation next to the ordinary sessionId/nonce/messages
// keys. The identity (groupRunId, mergeIndex, attempt) is what distinguishes
// THIS session from the group's wait trace and from an earlier merge's (or an
// earlier attempt's) session on the same node.
const (
	ConversationKeyKind             = "kind"
	ConversationKindGroupResolution = "group_resolution"
	ConversationKeyGroupRunID       = "groupRunId"
	ConversationKeyMergeIndex       = "mergeIndex"
	ConversationKeyAttempt          = "attempt"
	ConversationKeyTaskKey          = "taskKey"
	ConversationKeyConflictPaths    = "conflictPaths"
	ConversationKeyEndedAt          = "endedAt"
	ConversationKeyEndReason        = "endReason"
)

// GroupResolutionIdentity is the decoded identity of a resolution session.
type GroupResolutionIdentity struct {
	GroupRunID uuid.UUID
	MergeIndex int
	Attempt    int
	TaskKey    string
	Open       bool // no endedAt recorded yet
}

// GroupResolutionIdentityOf decodes conv's group-resolution identity. ok is
// false for ordinary interactive conversations and malformed identities.
// Numbers may arrive as float64 (JSON-decoded from storage) or int.
func GroupResolutionIdentityOf(conv map[string]any) (GroupResolutionIdentity, bool) {
	if !isGroupResolutionConversation(conv) {
		return GroupResolutionIdentity{}, false
	}
	rawID, _ := conv[ConversationKeyGroupRunID].(string)
	runID, err := uuid.Parse(rawID)
	if err != nil {
		return GroupResolutionIdentity{}, false
	}
	mergeIndex, ok := intValue(conv[ConversationKeyMergeIndex])
	if !ok {
		return GroupResolutionIdentity{}, false
	}
	attempt, ok := intValue(conv[ConversationKeyAttempt])
	if !ok {
		return GroupResolutionIdentity{}, false
	}
	taskKey, _ := conv[ConversationKeyTaskKey].(string)
	_, ended := conv[ConversationKeyEndedAt]
	return GroupResolutionIdentity{GroupRunID: runID, MergeIndex: mergeIndex, Attempt: attempt, TaskKey: taskKey, Open: !ended}, true
}

// IsOpenGroupResolutionSession reports whether conv is a group conflict-
// resolution session that has not ended.
func IsOpenGroupResolutionSession(conv map[string]any) bool {
	id, ok := GroupResolutionIdentityOf(conv)
	return ok && id.Open
}

func isGroupResolutionConversation(conv map[string]any) bool {
	kind, _ := conv[ConversationKeyKind].(string)
	return kind == ConversationKindGroupResolution
}

func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	default:
		return 0, false
	}
}

// closeGroupSessionConversation returns a copy of conv marked ended.
func closeGroupSessionConversation(conv map[string]any, reason string, at time.Time) map[string]any {
	out := make(map[string]any, len(conv)+2)
	for k, v := range conv {
		out[k] = v
	}
	out[ConversationKeyEndedAt] = at.UTC().Format(time.RFC3339Nano)
	out[ConversationKeyEndReason] = reason
	return out
}

// GroupResolutionCompleter receives the end of a group conflict-resolution
// session from the conversation handler (end, sentinel completion, abandon).
type GroupResolutionCompleter interface {
	OnResolutionSessionEnded(ctx context.Context, instanceID uuid.UUID, nodeID string, abandoned bool) error
}

// ResolutionNotifier raises an operator notification. *Engine satisfies it
// through EmitNotification (notification repo + event + external notifier).
type ResolutionNotifier interface {
	EmitNotification(ctx context.Context, instanceID uuid.UUID, workflowName string, notifType notification.Type)
}

// GroupSessionCloser marks a resolution session's record ended from outside a
// conversation (the reconciler, when a parent was cancelled mid-session).
type GroupSessionCloser interface {
	CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error
	NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error
}

// EmitNotification raises a notification for instanceID through the engine's
// wired notification repository, event bus and external notifier. It is the
// exported face of emitNotification for collaborators outside the run loop.
func (e *Engine) EmitNotification(ctx context.Context, instanceID uuid.UUID, workflowName string, notifType notification.Type) {
	e.emitNotification(ctx, instanceID, workflowName, notifType)
}

// groupResolutionOutputSchema is the shape of the sentinel completion block a
// resolution agent emits when the operator confirms.
var groupResolutionOutputSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary":       map[string]any{"type": "string"},
		"resolvedPaths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
	"required": []any{"summary"},
}

// GroupResolutionBridge opens group conflict-resolution sessions and receives
// their completion. It implements GroupResolutionStarter (for the gatherer),
// GroupResolutionCompleter (for the conversation handler) and
// GroupSessionCloser (for the reconciler).
type GroupResolutionBridge struct {
	groups    instance.GroupRunRepository
	instances instance.InstanceRepository
	nodeExecs instance.NodeExecutionRepository
	resolver  provider.ProfileResolver
	selector  InteractiveStreamSelector
	files     FileReader
	notifier  ResolutionNotifier
	bus       EventBus
	waker     instance.NodeAddressedWaker
	queueMgr  QueueManager
	now       func() time.Time
}

var (
	_ GroupResolutionStarter   = (*GroupResolutionBridge)(nil)
	_ GroupResolutionCompleter = (*GroupResolutionBridge)(nil)
	_ GroupSessionCloser       = (*GroupResolutionBridge)(nil)
)

// NewGroupResolutionBridge constructs the bridge.
func NewGroupResolutionBridge(
	groups instance.GroupRunRepository,
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	resolver provider.ProfileResolver,
	selector InteractiveStreamSelector,
	files FileReader,
	notifier ResolutionNotifier,
	bus EventBus,
	waker instance.NodeAddressedWaker,
	queueMgr QueueManager,
) *GroupResolutionBridge {
	if groups == nil || instances == nil || nodeExecs == nil || resolver == nil || selector == nil || files == nil || notifier == nil || bus == nil || waker == nil || queueMgr == nil {
		panic("engine: group resolution requires every collaborator")
	}
	return &GroupResolutionBridge{
		groups: groups, instances: instances, nodeExecs: nodeExecs, resolver: resolver, selector: selector,
		files: files, notifier: notifier, bus: bus, waker: waker, queueMgr: queueMgr, now: time.Now,
	}
}

// StartResolution runs the AUTONOMOUS first turn of a resolution session in
// the gather's staging directory, persists the session on the parent at the
// group node and publishes group.conflict. P3-T6 verifies the staging index
// after this first turn: a resolved merge continues automatically; only a
// still-conflicted merge needs operator end/abandon. The sentinel alone never
// proves that file conflicts were resolved.
func (b *GroupResolutionBridge) StartResolution(ctx context.Context, run *instance.GroupRun, parent *instance.Instance, req ResolutionRequest) (*instance.ResolutionSessionRef, error) {
	if run == nil || parent == nil {
		return nil, errors.New("group resolution: run and parent instance are required")
	}
	if strings.TrimSpace(req.Handle.StagingDir) == "" {
		return nil, fmt.Errorf("group resolution %q: run %s: gather has no staging directory", run.NodeID, run.ID)
	}
	if len(req.ConflictPaths) == 0 {
		return nil, fmt.Errorf("group resolution %q: run %s: no conflicted paths to resolve", run.NodeID, run.ID)
	}
	alias := groupConfigString(run.Config, "resolverProfile")
	if alias == "" {
		return nil, fmt.Errorf("group resolution: parallel_group %q has no resolverProfile", run.NodeID)
	}
	binding, err := b.resolver.Resolve(ctx, alias)
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: resolve profile %q: %w", run.NodeID, alias, err)
	}
	// SECURITY: resolution edits files, so it runs with the interactive_agent
	// posture; a chat-kind resolverProfile fails closed before any stream starts.
	if err := assertBindingKind(run.NodeID, workflow.NodeTypeInteractiveAgent, binding.Kind); err != nil {
		return nil, fmt.Errorf("group resolution %q: resolverProfile %q: %w", run.NodeID, alias, err)
	}
	streamProvider, err := b.selector.StreamProviderFor(binding)
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: select stream provider: %w", run.NodeID, err)
	}
	prompt, err := LoadGroupResolverPrompt(b.files, parent.OwnerID, parent.WorkflowName,
		groupConfigString(run.Config, "resolverPromptFile"), GroupResolverPromptData{
			TaskKey:       req.TaskKey,
			// The snapshot handle is a group-run id, not a revision the agent can
			// inspect, and the backing refs are not visible in the staging repo
			// (P3-T3 shares objects only). During the conflicted merge MERGE_HEAD
			// is the task commit, whose single parent is the baseline (P3-T2
			// FinalizeTask: commit-tree -p <baseline>), so MERGE_HEAD^ names the
			// common starting point in {{workDir}}.
			BaselineRef:   "MERGE_HEAD^",
			MergeRef:      "MERGE_HEAD",
			ConflictPaths: req.ConflictPaths,
			WorkDir:       req.Handle.StagingDir,
			Attempt:       req.Attempt,
		})
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: %w", run.NodeID, err)
	}

	nonce := uuid.NewString()
	userMessage := prompt + sentinelInstruction(nonce, groupResolutionOutputSchema)
	startedAt := b.now()
	stream, err := streamProvider.StreamChat(ctx, ChatRequest{
		Prompt:    userMessage,
		Model:     binding.Model,
		WorkDir:   req.Handle.StagingDir,
		ToolScope: binding.AllowedTools,
	})
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: stream call: %w", run.NodeID, err)
	}
	var response strings.Builder
	var tokensIn, tokensOut int
	var sessionID string
	var sawDone bool
	for chunk := range stream {
		if chunk.Error != nil {
			return nil, fmt.Errorf("group resolution %q: stream error: %w", run.NodeID, chunk.Error)
		}
		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}
		if chunk.Token != "" {
			response.WriteString(chunk.Token)
			b.bus.Publish(parent.ID, Event{Type: EventTypeInteractiveToken, Timestamp: time.Now(), Data: map[string]any{"token": chunk.Token}})
		}
		if chunk.Done {
			tokensIn, tokensOut = chunk.TokensIn, chunk.TokensOut
			sawDone = true
			break
		}
	}
	if !sawDone {
		return nil, fmt.Errorf("group resolution %q: stream closed without completion", run.NodeID)
	}
	fullResponse := response.String()

	attemptRow, err := nextNodeAttempt(ctx, b.nodeExecs, parent.ID, run.NodeID)
	if err != nil {
		return nil, fmt.Errorf("group resolution %q: number session record: %w", run.NodeID, err)
	}
	paths := make([]any, 0, len(req.ConflictPaths))
	for _, p := range req.ConflictPaths {
		paths = append(paths, p)
	}
	conversation := map[string]any{
		ConversationKeyKind:          ConversationKindGroupResolution,
		ConversationKeyGroupRunID:    run.ID.String(),
		ConversationKeyMergeIndex:    req.MergeIndex,
		ConversationKeyAttempt:       req.Attempt,
		ConversationKeyTaskKey:       req.TaskKey,
		ConversationKeyConflictPaths: paths,
		ConversationKeySessionID:     sessionID,
		ConversationKeyNonce:         nonce,
		ConversationKeyMessages: []map[string]any{
			{"role": "user", "content": userMessage},
			{"role": "assistant", "content": fullResponse},
		},
	}
	completedAt := b.now()
	model := binding.Model
	exec := &instance.NodeExecution{
		ID:              uuid.New(),
		InstanceID:      parent.ID,
		NodeID:          run.NodeID,
		Attempt:         attemptRow,
		Status:          instance.NodeStatusSucceeded,
		LLMConversation: conversation,
		LLMModel:        &model,
		LLMTokensIn:     &tokensIn,
		LLMTokensOut:    &tokensOut,
		StartedAt:       startedAt,
		CompletedAt:     &completedAt,
	}
	// Unlike InteractiveExecutor (log-only), a failed write is fatal here: the
	// record IS the session's identity, and a session nobody can address
	// would strand the parent.
	if err := b.nodeExecs.Create(ctx, exec); err != nil {
		return nil, fmt.Errorf("group resolution %q: persist session: %w", run.NodeID, err)
	}

	publishInteractiveMessage(b.bus, parent.ID, "assistant", fullResponse)
	b.bus.Publish(parent.ID, Event{
		Type:      EventTypeGroupConflict,
		Timestamp: time.Now(),
		Data: map[string]any{
			"groupRunId":    run.ID.String(),
			"nodeId":        run.NodeID,
			"taskKey":       req.TaskKey,
			"mergeIndex":    req.MergeIndex,
			"attempt":       req.Attempt,
			"conflictPaths": append([]string(nil), req.ConflictPaths...),
		},
	})
	return &instance.ResolutionSessionRef{
		GroupRunID: run.ID,
		MergeIndex: req.MergeIndex,
		Attempt:    req.Attempt,
		InstanceID: parent.ID,
		NodeID:     run.NodeID,
	}, nil
}

// NotifyResolutionRequired is called only after the gatherer verifies that
// the first agent turn left conflicts and persists the session identity.
func (b *GroupResolutionBridge) NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error {
	run, err := b.groups.Get(ctx, ref.GroupRunID)
	if err != nil { return fmt.Errorf("load resolution notification run: %w", err) }
	if run.Gather == nil || run.Gather.ActiveSession == nil || *run.Gather.ActiveSession != ref || run.ParentInstanceID != ref.InstanceID || run.NodeID != ref.NodeID {
		return errors.New("resolution notification does not name the active session")
	}
	exec, err := findGroupSessionExec(ctx, b.nodeExecs, ref)
	if err != nil { return err }
	if exec == nil || !IsOpenGroupResolutionSession(exec.LLMConversation) {
		return errors.New("resolution notification session is not open")
	}
	if sent, _ := exec.LLMConversation["notificationSent"].(bool); sent { return nil }
	parent, err := b.instances.Get(ctx, ref.InstanceID)
	if err != nil { return err }
	b.notifier.EmitNotification(ctx, parent.ID, parent.WorkflowName, notification.TypeConflictResolution)
	exec.LLMConversation["notificationSent"] = true
	return b.nodeExecs.Update(ctx, exec)
}

// OnResolutionSessionEnded records the end of the ACTIVE attempt (outcome
// "abandoned" when abandoned, otherwise left for the gatherer to verify) and
// wakes the parent ONLY if it is still waiting at this node — never a
// cancelled, terminal or already-claimed parent. An attempt that already
// ended makes the call a no-op, so repeated end/sentinel callbacks are safe.
func (b *GroupResolutionBridge) OnResolutionSessionEnded(ctx context.Context, instanceID uuid.UUID, nodeID string, abandoned bool) error {
	run, err := b.groups.GetByParentNode(ctx, instanceID, nodeID)
	if err != nil {
		return fmt.Errorf("group resolution: load group run for %s/%s: %w", instanceID, nodeID, err)
	}
	if run == nil || run.Gather == nil || run.Gather.ActiveSession == nil {
		return nil
	}
	active := *run.Gather.ActiveSession
	report := copyGatherReport(*run.Gather)
	att := findAttempt(&report, active.MergeIndex, active.Attempt)
	if att == nil || att.EndedAt != nil {
		return nil
	}
	now := b.now().UTC()
	att.EndedAt = &now
	if abandoned {
		att.Outcome = ResolutionOutcomeAbandoned
	}
	if err := b.groups.SetGatherReport(ctx, run.ID, report); err != nil {
		return fmt.Errorf("group resolution: record session end for run %s: %w", run.ID, err)
	}
	parent, err := b.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("group resolution: load parent %s: %w", instanceID, err)
	}
	if parent.State != instance.StateWaiting {
		return nil
	}
	woke, err := b.waker.SetQueuedIfWaitingAtNode(ctx, instanceID, nodeID, now)
	if err != nil {
		return fmt.Errorf("group resolution: wake parent %s at %s: %w", instanceID, nodeID, err)
	}
	if woke && b.queueMgr != nil {
		b.queueMgr.NotifyCapacityFreed(ctx)
	}
	return nil
}

// CloseSession marks the record of ref ended with reason and publishes
// interactive.ended. Unknown or already-ended sessions are a no-op.
func (b *GroupResolutionBridge) CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error {
	exec, err := findGroupSessionExec(ctx, b.nodeExecs, ref)
	if err != nil {
		return fmt.Errorf("group resolution: find session %d/%d of run %s: %w", ref.MergeIndex, ref.Attempt, ref.GroupRunID, err)
	}
	if exec == nil || !IsOpenGroupResolutionSession(exec.LLMConversation) {
		return nil
	}
	exec.LLMConversation = closeGroupSessionConversation(exec.LLMConversation, reason, b.now())
	if err := b.nodeExecs.Update(ctx, exec); err != nil {
		return fmt.Errorf("group resolution: close session %d/%d of run %s: %w", ref.MergeIndex, ref.Attempt, ref.GroupRunID, err)
	}
	b.bus.Publish(ref.InstanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data:      map[string]any{"reason": reason, "sessionKind": ConversationKindGroupResolution},
	})
	return nil
}

// latestNodeExecution returns the newest record for (instanceID, nodeID): the
// highest Attempt, ties going to the later record in repository order (the
// SQLite repository lists by started_at). (nil, nil) when there is none.
func latestNodeExecution(ctx context.Context, repo instance.NodeExecutionRepository, instanceID uuid.UUID, nodeID string) (*instance.NodeExecution, error) {
	execs, err := repo.ListByInstance(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	var best *instance.NodeExecution
	for i := range execs {
		if execs[i].NodeID != nodeID {
			continue
		}
		if best == nil || execs[i].Attempt >= best.Attempt {
			cp := execs[i]
			best = &cp
		}
	}
	return best, nil
}

// nextNodeAttempt numbers a new record one past the node's newest record, so
// GetByInstanceAndNode (ORDER BY attempt DESC LIMIT 1) returns it.
func nextNodeAttempt(ctx context.Context, repo instance.NodeExecutionRepository, instanceID uuid.UUID, nodeID string) (int, error) {
	latest, err := latestNodeExecution(ctx, repo, instanceID, nodeID)
	if err != nil {
		return 0, err
	}
	if latest == nil {
		return 1, nil
	}
	return latest.Attempt + 1, nil
}

// findGroupSessionExec returns the session record whose identity equals ref.
func findGroupSessionExec(ctx context.Context, repo instance.NodeExecutionRepository, ref instance.ResolutionSessionRef) (*instance.NodeExecution, error) {
	execs, err := repo.ListByInstance(ctx, ref.InstanceID)
	if err != nil {
		return nil, err
	}
	for i := range execs {
		if execs[i].NodeID != ref.NodeID {
			continue
		}
		id, ok := GroupResolutionIdentityOf(execs[i].LLMConversation)
		if ok && id.GroupRunID == ref.GroupRunID && id.MergeIndex == ref.MergeIndex && id.Attempt == ref.Attempt {
			cp := execs[i]
			return &cp, nil
		}
	}
	return nil, nil
}

func groupConfigString(cfg map[string]any, key string) string {
	s, _ := cfg[key].(string)
	return strings.TrimSpace(s)
}
```

- [x] Step 11: Run the bridge tests; they pass.

```bash
go test ./domain/engine/ -run 'TestGroupResolutionBridge|TestGroupResolutionIdentityOf|TestEngine_EmitNotification' -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 12: Commit.

  Bridge red/green confirmed missing types, then additional failing tests exposed replay launching twice, invalid notification state admission and mutation of the durable notification marker before a failed write. Fixed with full-ref replay lookup, resolving-state check and copy-before-write. Self-review in order: (1) kept exact bridge ports and autonomous/no-human-notification contract, honored P3-T6's idempotent starter; (2) assertions cover persisted session identity/history, exact staging/model/tools/prompt, notification marker retry and parent queued-state sink; (3) checked neighboring gather/conversation/repository patterns, formatted, reran focused tests. Production-root wiring is still blocked on P3-T10/T11 (recorded at the final completion gate).

```bash
git add domain/engine/group_resolution.go domain/engine/group_resolution_test.go
git commit -m "feat(engine): group conflict-resolution bridge (autonomous attempt, session identity, completion wake)" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

#### 8.3 Conversation gate and group completion dispatch

- [x] Step 13: Write the failing conversation tests. Create `domain/engine/conversation_group_test.go`:

```go
package engine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groupResolutionSetter is the composition-root seam (concrete type only).
type groupResolutionSetter interface {
	SetGroupResolution(instance.GroupRunRepository, engine.GroupResolutionCompleter)
}

type groupConvoFixture struct {
	t          *testing.T
	instances  *mockInstanceRepo
	nodeExecs  *mockNodeExecutionRepo
	groups     *memGroupRuns
	agent      *mockStreamingChatProvider
	chat       *mockStreamingChatProvider
	bus        *mockEventBus
	queue      *mockQueueManager
	completer  *recordingCompleter
	handler    engine.ConversationHandler
	parentID   uuid.UUID
	runID      uuid.UUID
	waitTrace  uuid.UUID
	sessionRow uuid.UUID
}

func groupWorkflowSnapshot(t *testing.T) map[string]any {
	t.Helper()
	raw := `{
	  "name": "wf",
	  "nodes": [
	    {"id": "g1", "type": "parallel_group", "config": {"group": {
	      "form": "branches",
	      "tasks": [{"key": "a", "entry": "a1"}, {"key": "b", "entry": "b1"}],
	      "completionRule": "all_succeed", "unfinishedBehavior": "wait_for_remaining",
	      "gatherMode": "results_and_files", "resolverProfile": "group-resolver"}}},
	    {"id": "a1", "type": "transform", "config": {}},
	    {"id": "b1", "type": "transform", "config": {}}
	  ],
	  "edges": []
	}`
	var snap map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &snap))
	return snap
}

// newGroupConvoFixture: a parent WAITING at group node g1, the group's wait
// trace (attempt 1) and — when withSession — the open session record of merge
// 1 / attempt 1 (attempt 2), shaped exactly as the bridge persists it after a
// JSON round trip. wire=false leaves the handler as Phase 2 built it.
func newGroupConvoFixture(t *testing.T, wire, withSession bool) *groupConvoFixture {
	t.Helper()
	f := &groupConvoFixture{
		t: t, instances: newMockInstanceRepo(), nodeExecs: newMockNodeExecutionRepo(), groups: newMemGroupRuns(),
		agent: newMockStreamingChatProvider(), chat: newMockStreamingChatProvider(), bus: newMockEventBus(),
		queue: newMockQueueManager(), completer: &recordingCompleter{}, parentID: uuid.New(),
	}
	node := "g1"
	f.instances.instances[f.parentID] = &instance.Instance{
		ID: f.parentID, OwnerID: convoTestOwner, WorkflowName: "wf", State: instance.StateWaiting,
		CurrentNodeID: &node, WorkflowSnapshot: groupWorkflowSnapshot(t),
		Variables: map[string]any{"workspace": "/ws/parent", "before": "kept"},
	}
	gatherID := uuid.New()
	run := &instance.GroupRun{
		ID: uuid.New(), ParentInstanceID: f.parentID, NodeID: "g1", Attempt: 1,
		CompletionRule: workflow.GroupRuleAllSucceed, GatherMode: workflow.GroupGatherResultsAndFiles,
		Config: map[string]any{"resolverProfile": groupResolverProfile}, State: instance.GroupRunResolvingConflicts,
		Gather: &instance.GatherReport{
			Mode: workflow.GroupGatherResultsAndFiles, State: "resolving", MergeIndex: 1,
			Handle:   stagingHandleMap(gatherID),
			Attempts: []instance.ResolutionAttempt{{MergeIndex: 1, Attempt: 1, TaskKey: "b", ConflictPaths: []string{"shared.txt"}, StartedAt: time.Now().UTC()}},
		},
	}
	run.Gather.ActiveSession = &instance.ResolutionSessionRef{GroupRunID: run.ID, MergeIndex: 1, Attempt: 1, InstanceID: f.parentID, NodeID: "g1"}
	f.runID = run.ID
	f.groups.put(run)

	f.waitTrace = uuid.New()
	require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
		ID: f.waitTrace, InstanceID: f.parentID, NodeID: "g1", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"groupRunId": run.ID.String(), "state": "waiting"}, StartedAt: time.Now(),
	}))
	if withSession {
		f.sessionRow = uuid.New()
		require.NoError(t, f.nodeExecs.Create(context.Background(), &instance.NodeExecution{
			ID: f.sessionRow, InstanceID: f.parentID, NodeID: "g1", Attempt: 2, Status: instance.NodeStatusSucceeded,
			LLMConversation: map[string]any{
				"kind": "group_resolution", "groupRunId": run.ID.String(), "mergeIndex": float64(1), "attempt": float64(1),
				"taskKey": "b", "conflictPaths": []any{"shared.txt"}, "sessionId": "agent-sess-1", "nonce": testNonce,
				"messages": []any{
					map[string]any{"role": "user", "content": "resolve prompt"},
					map[string]any{"role": "assistant", "content": "I merged both sides."},
				},
			},
			StartedAt: time.Now(),
		}))
	}
	f.handler = engine.NewConversationHandler(f.instances, f.nodeExecs,
		&fakeStreamSelector{chatProv: f.chat, agentProv: f.agent}, groupResolverResolver(), f.bus, f.queue)
	if wire {
		f.handler.(groupResolutionSetter).SetGroupResolution(f.groups, f.completer)
	}
	return f
}

func (f *groupConvoFixture) exec(id uuid.UUID) instance.NodeExecution {
	f.t.Helper()
	f.nodeExecs.mu.Lock()
	defer f.nodeExecs.mu.Unlock()
	for _, e := range f.nodeExecs.executions {
		if e.ID == id {
			return *e
		}
	}
	f.t.Fatalf("no node execution %s", id)
	return instance.NodeExecution{}
}

func (f *groupConvoFixture) parent() *instance.Instance {
	f.t.Helper()
	p, err := f.instances.Get(context.Background(), f.parentID)
	require.NoError(f.t, err)
	return p
}

// --- Happy path -------------------------------------------------------------

func TestConversationHandler_Group_HandleMessageStreamsIntoSessionInStagingDir(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"Fixed ", "it."}, "agent-sess-2")

	require.NoError(t, f.handler.HandleMessage(convoCtx(), f.parentID, "please keep both sides"))

	reqs := f.agent.getCapturedReqs()
	require.Len(t, reqs, 1)
	assert.Equal(t, groupStagingDir, reqs[0].WorkDir, "the turn runs in the persisted STAGING dir, not {{workspace}}")
	assert.Equal(t, "agent-sess-1", reqs[0].SessionID, "the session resumes the autonomous attempt's provider session")
	assert.Equal(t, groupResolverModel, reqs[0].Model)
	assert.Equal(t, []string{"Read", "Edit"}, reqs[0].ToolScope)
	assert.Equal(t, "please keep both sides", reqs[0].Prompt)
	assert.Empty(t, f.chat.getCapturedReqs())

	row := f.exec(f.sessionRow)
	msgs := extractMessages(t, row.LLMConversation)
	require.Len(t, msgs, 4)
	assert.Equal(t, "please keep both sides", msgs[2]["content"])
	assert.Equal(t, "Fixed it.", msgs[3]["content"])
	assert.Equal(t, "agent-sess-2", row.LLMConversation["sessionId"])
	ident, ok := engine.GroupResolutionIdentityOf(row.LLMConversation)
	require.True(t, ok, "the session identity survives the turn")
	assert.Equal(t, engine.GroupResolutionIdentity{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, TaskKey: "b", Open: true}, ident)

	trace := f.exec(f.waitTrace)
	assert.Nil(t, trace.LLMConversation, "the group's wait trace is never treated as the session")
	assert.Equal(t, instance.StateWaiting, f.parent().State)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_EndSessionDispatchesWithoutQueueingOrWritingVars(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	out, err := f.handler.EndSession(convoCtx(), f.parentID)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, out)

	assert.Equal(t, []completerCall{{instanceID: f.parentID, nodeID: "g1", abandoned: false}}, f.completer.all())
	p := f.parent()
	assert.Equal(t, instance.StateWaiting, p.State, "the handler never queues a group parent itself")
	_, wrote := p.Variables["g1"]
	assert.False(t, wrote, "vars[g1] is the gather's frozen output, never the session's")
	assert.Equal(t, "kept", p.Variables["before"])
	assert.Zero(t, f.queue.notifyCount())

	row := f.exec(f.sessionRow)
	assert.False(t, engine.IsOpenGroupResolutionSession(row.LLMConversation))
	assert.Equal(t, "user_ended", row.LLMConversation["endReason"])
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	data := ended[0].Data.(map[string]any)
	assert.Equal(t, "user_ended", data["reason"])
	assert.Equal(t, "group_resolution", data["sessionKind"])
}

func TestConversationHandler_Group_SentinelCompletionDispatchesToCompleter(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	block := engine.SentinelOpenTag(testNonce) + `{"summary":"kept both","resolvedPaths":["shared.txt"]}` + engine.SentinelCloseTag(testNonce)
	f.agent.streamCallFn = cannedStreamWithSession([]string{"Done.\n", block}, "agent-sess-2")

	require.NoError(t, f.handler.HandleMessage(convoCtx(), f.parentID, "looks good, finish"))

	assert.Equal(t, []completerCall{{instanceID: f.parentID, nodeID: "g1", abandoned: false}}, f.completer.all())
	row := f.exec(f.sessionRow)
	assert.Equal(t, "kept both", row.Output["summary"])
	assert.Equal(t, "llm_completed", row.LLMConversation["endReason"])
	assert.Equal(t, "group_resolution", row.LLMConversation["kind"])
	_, wrote := f.parent().Variables["g1"]
	assert.False(t, wrote)
	assert.Equal(t, instance.StateWaiting, f.parent().State)
}

// --- Idempotence / identity -------------------------------------------------

func TestConversationHandler_Group_RepeatedEndIsRejected(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	require.NoError(t, err)

	_, err = f.handler.EndSession(convoCtx(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Len(t, f.completer.all(), 1, "a replayed end never reaches the completer twice")
}

func TestConversationHandler_Group_CompleterFailureLeavesSessionOpenForRetry(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.completer.err = assert.AnError

	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	require.Error(t, err)
	assert.True(t, engine.IsOpenGroupResolutionSession(f.exec(f.sessionRow).LLMConversation))

	f.completer.err = nil
	_, err = f.handler.EndSession(convoCtx(), f.parentID)
	require.NoError(t, err)
	assert.Len(t, f.completer.all(), 2)
}

func TestConversationHandler_Group_EarlierAttemptIsNotTheActiveSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.ActiveSession.Attempt = 2 })

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Empty(t, f.agent.getCapturedReqs())
}

func TestConversationHandler_Group_EarlierMergeIsNotTheActiveSession(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Gather.ActiveSession.MergeIndex = 2 })

	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_WaitTraceAloneIsNotASession(t *testing.T) {
	f := newGroupConvoFixture(t, true, false)

	_, err := f.handler.EndSession(convoCtx(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_NotResolvingIsNotInteractive(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.State = instance.GroupRunGathering })

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	assert.ErrorIs(t, err, instance.ErrNotInteractiveWait)
}

func TestConversationHandler_Group_ChatResolverProfileFailsClosed(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Config["resolverProfile"] = "chatty" })

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	assert.ErrorIs(t, err, engine.ErrBindingKindMismatch)
	assert.Empty(t, f.agent.getCapturedReqs())
	assert.Empty(t, f.chat.getCapturedReqs())
}

// --- Wiring / authorization -------------------------------------------------

func TestConversationHandler_Group_UnwiredHandlerKeepsGroupsNonInteractive(t *testing.T) {
	f := newGroupConvoFixture(t, false, true)

	err := f.handler.HandleMessage(convoCtx(), f.parentID, "hello")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not interactive")
	_, err = f.handler.EndSession(convoCtx(), f.parentID)
	require.Error(t, err)
	assert.Empty(t, f.completer.all())
}

func TestConversationHandler_Group_AuthorizationMatchesInteractiveNodes(t *testing.T) {
	f := newGroupConvoFixture(t, true, true)

	_, err := f.handler.EndSession(context.Background(), f.parentID)
	assert.ErrorIs(t, err, instance.ErrNotFound, "no identity: existence is not leaked")

	stranger := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "x", Role: identity.RoleUser})
	err = f.handler.HandleMessage(stranger, f.parentID, "hello")
	assert.ErrorIs(t, err, instance.ErrNotFound)
	assert.Empty(t, f.completer.all())

	admin := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin})
	_, err = f.handler.EndSession(admin, f.parentID)
	require.NoError(t, err)
	assert.Len(t, f.completer.all(), 1)
	ended := f.bus.eventsOfType(f.parentID, engine.EventTypeInteractiveEnded)
	require.Len(t, ended, 1)
	assert.NotEmpty(t, ended[0].Data.(map[string]any)["actorUserId"], "a non-owner end is attributed")
}

func TestConversationHandler_Group_OrdinaryInteractiveSessionUnchanged(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr, instanceID := conversationTestSetup(t)
	h := buildConversationHandler(instRepo, nodeExecRepo, llmProvider, eventBus, queueMgr)
	completer := &recordingCompleter{}
	h.(groupResolutionSetter).SetGroupResolution(newMemGroupRuns(), completer)

	out, err := h.EndSession(convoCtx(), instanceID)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{}, out)
	inst, err := instRepo.Get(context.Background(), instanceID)
	require.NoError(t, err)
	assert.Equal(t, instance.StateQueued, inst.State, "interactive nodes still queue on end")
	assert.Contains(t, inst.Variables, "interact")
	assert.Empty(t, completer.all())
}
```

- [x] Step 14: Write the failing InteractiveExecutor guard test. Create `domain/engine/executor_interactive_group_test.go`:

```go
package engine_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHY: InteractiveExecutor short-circuits on "a succeeded record exists for
// this node" (executor_interactive.go:72-75). Group sessions and the group's
// wait trace are also succeeded records — but on the parallel_group node,
// which the engine never dispatches to InteractiveExecutor (it is registered
// only for interactive_llm / interactive_agent, cmd/arbitron/main.go:1001-1003).
// The guard below is defense in depth: a group-resolution record must never
// be mistaken for a completed interactive session, even under a node id reused
// across snapshot versions. Inside the group, an EARLIER merge's/attempt's
// session is excluded by the identity match against GatherReport.ActiveSession
// (conversation gate) and by the gatherer verifying the CURRENT merge's latest
// attempt (P3-T6).
func TestInteractiveExecutor_GroupResolutionRecordIsNotACompletedSession(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	fileReader.files["test-wf/orchestration.md"] = []byte("Orchestration")
	fileReader.files["test-wf/template.md"] = []byte("Template")
	llmProvider.streamCallFn = cannedStream([]string{"fresh turn"}, 1, 1)
	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instanceID, NodeID: "interact", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output:          map[string]any{"stale": true},
		LLMConversation: map[string]any{"kind": "group_resolution", "groupRunId": uuid.NewString(), "mergeIndex": 0, "attempt": 1},
		StartedAt:       time.Now(),
	}))
	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	result, err := executor.Execute(engine.ContextWithInstanceID(context.Background(), instanceID), interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.True(t, result.IsWait, "a new conversation was started")
	assert.Len(t, llmProvider.getCapturedReqs(), 1)
	assert.Nil(t, result.Output)
}

func TestInteractiveExecutor_OrdinaryCompletedRecordStillShortCircuits(t *testing.T) {
	instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr, instanceID := interactiveTestSetup()
	require.NoError(t, nodeExecRepo.Create(context.Background(), &instance.NodeExecution{
		ID: uuid.New(), InstanceID: instanceID, NodeID: "interact", Attempt: 1, Status: instance.NodeStatusSucceeded,
		Output: map[string]any{"summary": "done"}, LLMConversation: map[string]any{"nonce": "n"}, StartedAt: time.Now(),
	}))
	executor := buildInteractiveExecutor(instRepo, nodeExecRepo, llmProvider, fileReader, eventBus, queueMgr)

	result, err := executor.Execute(engine.ContextWithInstanceID(context.Background(), instanceID), interactiveNode(), variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"summary": "done"}, result.Output)
	assert.Empty(t, llmProvider.getCapturedReqs())
}
```

- [x] Step 15: Run them; confirm they fail.

```bash
go test ./domain/engine/ -run 'TestConversationHandler_Group|TestInteractiveExecutor_GroupResolutionRecord|TestInteractiveExecutor_OrdinaryCompletedRecord' -count=1
```
Expected:
```
panic: interface conversion: *engine.conversationHandler is not engine_test.groupResolutionSetter: missing method SetGroupResolution
...
--- FAIL: TestInteractiveExecutor_GroupResolutionRecordIsNotACompletedSession (0.00s)
    executor_interactive_group_test.go:...: Error: Should be true ... a new conversation was started
FAIL	github.com/arbitron/arbitron/domain/engine
```

- [x] Step 16: Guard the InteractiveExecutor short-circuit. In `domain/engine/executor_interactive.go`, replace lines 68-75:

```go
	// 2. Short-circuit if the session was already completed by ConversationHandler.
	// After an interactive session ends, the queue re-starts the instance with
	// CurrentNodeID still pointing at this node. Returning the stored output here
	// lets the engine advance to the next node without starting a new conversation.
	if existing, err := e.nodeExecs.GetByInstanceAndNode(ctx, instanceID, node.ID); err == nil &&
		existing.Status == instance.NodeStatusSucceeded {
		return &NodeResult{Output: existing.Output}, nil
	}
```
with:

```go
	// 2. Short-circuit if the session was already completed by ConversationHandler.
	// After an interactive session ends, the queue re-starts the instance with
	// CurrentNodeID still pointing at this node. Returning the stored output here
	// lets the engine advance to the next node without starting a new conversation.
	//
	// A parallel_group conflict-resolution record is NEVER a completed
	// interactive session: those records live on the group node (which this
	// executor is not registered for) and carry their own identity; the guard
	// keeps one from ever being replayed as this node's output.
	if existing, err := e.nodeExecs.GetByInstanceAndNode(ctx, instanceID, node.ID); err == nil &&
		existing.Status == instance.NodeStatusSucceeded &&
		!isGroupResolutionConversation(existing.LLMConversation) {
		return &NodeResult{Output: existing.Output}, nil
	}
```

- [x] Step 17: Replace `domain/engine/conversation.go` (all 593 lines) with:

```go
package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// MaxConversationTurns is the documented upper bound for the number of
// messages (user + assistant) in a single interactive session. This is not
// enforced today — it exists as a documented constant so that future rate
// limiting or auto-completion logic has a reference value. A "turn" is one
// user message plus one assistant response (2 messages). The initial
// orchestration message also counts.
const MaxConversationTurns = 50

// ConversationHandler is a domain service that processes user messages during
// an active interactive session, manages the LLM conversation loop, detects
// session completion via tool-use, and handles user-initiated session end.
type ConversationHandler interface {
	HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error
	// EndSession ends the interactive session and returns the captured output.
	// The output is captured before any state transition to avoid a race with
	// the queue manager advancing the instance.
	EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error)
}

// conversationHandler implements ConversationHandler. It is profile-aware: a
// resumed turn resolves the waiting node's model_profile to a binding and
// selects the streaming backend via the InteractiveStreamSelector (chat profile
// -> tool-suppressed chat stream; agent profile -> tool-enabled stream).
//
// It also serves parallel_group conflict-resolution sessions (P3-T8) once
// SetGroupResolution is called: the same transports, authorization, turn
// guard, sentinel protection and history persistence, addressed to the
// group's OPEN session record, with completion dispatched to a
// GroupResolutionCompleter instead of writing node variables.
type conversationHandler struct {
	instances     instance.InstanceRepository
	nodeExecs     instance.NodeExecutionRepository
	selector      InteractiveStreamSelector
	resolver      provider.ProfileResolver
	eventBus      EventBus
	queueMgr      QueueManager
	interventions instance.InterventionRepository // optional; nil-safe attribution
	audit         AuditRecorder                   // optional; nil-safe domain audit event

	// groups and groupCompleter make a parallel_group's conflict-resolution
	// session reachable through the interactive surfaces. Both nil (the
	// default) => a parallel_group node is never interactive.
	groups         instance.GroupRunRepository
	groupCompleter GroupResolutionCompleter

	// turnInProgress enforces at most one in-flight interactive operation per
	// instance by mutual exclusion *with rejection* — overlapping calls are
	// refused, not queued/serialized. A turn drives a single stateful provider
	// session (e.g. `claude --resume`), which claude-cli cannot run concurrently:
	// a second overlapping send queues the prompt and no-ops the session,
	// permanently wedging the node. The set holds instance IDs with an operation
	// currently executing; both HandleMessage and EndSession acquire it (after
	// their auth/state gate) and a concurrent caller gets ErrInteractiveTurnInProgress
	// rather than a second concurrent resume. Does NOT cover the wider
	// EndSession/completeSession variable-rewrite race — see dbinky/arbitron#38.
	// The zero value is ready to use.
	turnInProgress sync.Map // map[uuid.UUID]struct{}
}

// groupSessionTarget is the open group conflict-resolution session a call
// addresses: the run (identity authority), the session record on the parent at
// the group node, its decoded identity, and the staging directory the agent
// works in.
type groupSessionTarget struct {
	run     *instance.GroupRun
	exec    *instance.NodeExecution
	ident   GroupResolutionIdentity
	workDir string
}

// NewConversationHandler constructs a new ConversationHandler with all required
// dependencies.
func NewConversationHandler(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	selector InteractiveStreamSelector,
	resolver provider.ProfileResolver,
	eventBus EventBus,
	queueMgr QueueManager,
) ConversationHandler {
	return &conversationHandler{
		instances: instances,
		nodeExecs: nodeExecs,
		selector:  selector,
		resolver:  resolver,
		eventBus:  eventBus,
		queueMgr:  queueMgr,
	}
}

// SetInterventions wires the intervention audit repository so admin
// participation in an interactive session is attributed. Nil-safe: when unset,
// no attribution row is written (the actor tag is still added to events).
func (h *conversationHandler) SetInterventions(repo instance.InterventionRepository) {
	h.interventions = repo
}

// SetAuditRecorder wires the Phase-2 audit service so a lead/admin steering an
// interactive session they do not own emits a domain audit event
// (instance.lead_intervention / admin.override) — the accountability trail the
// engine's mutation paths already produce. Nil-safe: when unset, no audit event
// is written. *audit.Service.Record satisfies AuditRecorder with no adapter.
func (h *conversationHandler) SetAuditRecorder(r AuditRecorder) {
	h.audit = r
}

// SetGroupResolution enables parallel_group conflict-resolution sessions:
// groups resolves the group run (identity authority, staging dir, resolver
// profile) and completer receives each session's end. Wired by the
// composition root (P3-T11) with the GroupResolutionBridge.
func (h *conversationHandler) SetGroupResolution(groups instance.GroupRunRepository, completer GroupResolutionCompleter) {
	h.groups = groups
	h.groupCompleter = completer
}

// publishInteractiveMessage publishes a single interactive.message event for a
// chat message with the given role and content. It is the one place that
// defines the wire shape of that event (type constant, the role/content keys,
// the Timestamp) so the contract lives in exactly one location rather than
// being duplicated across the executor and conversation-handler emission sites.
//
// Scope: role+content message events only. Token (interactive.token) and ended
// (interactive.ended) events have different payload shapes and are intentionally
// NOT funneled through here.
func publishInteractiveMessage(bus EventBus, instanceID uuid.UUID, role, content string) {
	bus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveMessage,
		Timestamp: time.Now(),
		Data: map[string]any{
			"role":    role,
			"content": content,
		},
	})
}

// nonOwnerActor returns the acting user's id and true when the context identity
// is present AND differs from the instance owner (i.e. an admin participating in
// another user's session). Owner self-participation and identity-less calls
// return false, so they are not attributed.
func (h *conversationHandler) nonOwnerActor(ctx context.Context, inst *instance.Instance) (uuid.UUID, bool) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.UserID == inst.OwnerID {
		return uuid.Nil, false
	}
	return id.UserID, true
}

// recordInteractiveIntervention attributes a non-owner's interactive
// participation: it writes the best-effort attribution row AND emits the domain
// audit event (instance.lead_intervention / admin.override), via the shared
// interventionRecorder the engine mutation paths also use. Routing both paths
// through one collaborator is what guarantees the interactive surface — the
// highest-trust action in the product — can never again lose its audit trail
// (PR #21). Nil-safe and non-fatal on both sinks.
func (h *conversationHandler) recordInteractiveIntervention(ctx context.Context, inst *instance.Instance, action string, detail *string) {
	rec := interventionRecorder{interventions: h.interventions, audit: h.audit}
	rec.record(ctx, inst, action, detail)
}

// HandleMessage processes a user message in an active interactive session.
// It loads the instance and node execution, resumes the stored claude session
// (sending only the new user message), streams the response, and detects
// session completion via the result sentinel block.
func (h *conversationHandler) HandleMessage(ctx context.Context, instanceID uuid.UUID, content string) error {
	// 1. Load and validate instance state. This auth/state gate runs FIRST so an
	// unauthorized caller (or a send to a non-waiting instance) gets the same
	// ErrNotFound/ErrForbidden/ErrNotInteractiveWait it always would — it must not
	// learn whether a turn is in flight (the guard below would otherwise leak that).
	inst, nodeID, nodeConfig, group, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return err
	}

	// 1a. Reject an overlapping turn for this instance. A turn resumes a single
	// stateful provider session (claude --resume); a second concurrent send would
	// drive that session concurrently, which claude-cli mishandles (it queues the
	// prompt and the session no-ops), permanently wedging the node. Acquired only
	// after the auth/state gate above and released when this turn returns, so
	// sequential turns are unaffected. NOTE: this covers HandleMessage only;
	// EndSession acquires the same guard separately.
	if _, inFlight := h.turnInProgress.LoadOrStore(instanceID, struct{}{}); inFlight {
		return fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrInteractiveTurnInProgress)
	}
	defer h.turnInProgress.Delete(instanceID)

	// 2. Load the session record. A group conflict-resolution session lives on
	// its OWN record (identified by group run, merge and attempt), not on
	// whatever record is newest for the node.
	nodeExec, err := h.sessionExecution(ctx, instanceID, nodeID, group)
	if err != nil {
		return err
	}

	// 3. Parse conversation history.
	messages := parseConversation(nodeExec.LLMConversation)

	// 4. Append user message (in memory only -- not persisted until LLM succeeds).
	messages = append(messages, map[string]any{
		"role":    "user",
		"content": content,
	})

	// 5. Publish interactive.message event for user message, tagging the actor
	// when an admin (non-owner) is participating, and record an attribution row.
	if actor, isOther := h.nonOwnerActor(ctx, inst); isOther {
		h.eventBus.Publish(instanceID, Event{
			Type:      EventTypeInteractiveMessage,
			Timestamp: time.Now(),
			Data: map[string]any{
				"role":        "user",
				"content":     content,
				"actorUserId": actor.String(),
			},
		})
		h.recordInteractiveIntervention(ctx, inst, instance.ActionInteractiveMsg, nil)
	} else {
		publishInteractiveMessage(h.eventBus, instanceID, "user", content)
	}

	// 5a. Resolve the waiting node's model_profile to a binding and select the
	// streaming backend the same way the InteractiveExecutor does on the first
	// turn: a chat binding yields the tool-suppressed chat stream, an agent
	// binding yields the tool-enabled stream. The model comes from the binding.
	binding, err := h.resolver.Resolve(ctx, nodeConfig.ModelProfile)
	if err != nil {
		return fmt.Errorf("conversation: resolve profile: %w", err)
	}
	// A conflict-resolution session edits files in the staging area: it must
	// run on an AGENT binding, exactly like an interactive_agent node.
	if group != nil {
		if err := assertBindingKind(nodeID, workflow.NodeTypeInteractiveAgent, binding.Kind); err != nil {
			return fmt.Errorf("conversation: group resolution %q: %w", nodeID, err)
		}
	}
	streamProvider, err := h.selector.StreamProviderFor(binding)
	if err != nil {
		return fmt.Errorf("conversation: select stream provider: %w", err)
	}

	// 6. Build the request: resume the stored session, send only the new
	//    user message. WorkDir comes from the instance's seeded workspace —
	//    or, for a conflict-resolution session, the gather's STAGING dir: the
	//    agent never works in the parent's live area or a task's directory.
	workDir, _ := inst.Variables[ReservedVarWorkspace].(string)
	var toolScope []string
	if group != nil {
		workDir = group.workDir
		toolScope = binding.AllowedTools
	}
	req := ChatRequest{
		SystemPrompt: nodeConfig.SystemPrompt,
		Prompt:       content,
		Model:        binding.Model,
		SessionID:    SessionID(nodeExec.LLMConversation),
		WorkDir:      workDir,
		ToolScope:    toolScope,
	}

	// 7. Call the provider with streaming.
	fullResponse, newSessionID, err := h.streamChatResponse(ctx, instanceID, streamProvider, req)
	if err != nil {
		// LLM failed -- the user message was never persisted, so there is no
		// conversation corruption to roll back.
		return err
	}

	// claude returns a session id on each turn; if the stream did not carry
	// one, keep the existing id so the next turn can still resume.
	persistSessionID := newSessionID
	if persistSessionID == "" {
		persistSessionID = req.SessionID
	}

	// 8. Detect completion via the result sentinel, scoped to the per-instance
	// nonce persisted by InteractiveExecutor. If the conversation was never
	// initialised by the executor (e.g. crash recovery where the instance was
	// set to waiting before the executor wrote its record), no nonce is
	// available and sentinel completion cannot be detected on this turn — the
	// session continues as a normal response. EndSession remains the
	// operator-side escape hatch.
	var sentinelOutput map[string]any
	var completed bool
	if nonce := conversationNonce(nodeExec.LLMConversation); nonce != "" {
		var sentErr error
		sentinelOutput, completed, sentErr = extractSentinelResult(fullResponse, nonce)
		if sentErr != nil {
			return sentErr
		}
	}

	// 9. Append the assistant message.
	messages = append(messages, map[string]any{
		"role":    "assistant",
		"content": fullResponse,
	})

	if completed {
		return h.handleSessionCompletion(ctx, instanceID, nodeID, nodeExec, messages, persistSessionID, sentinelOutput, group)
	}
	return h.handleNormalResponse(ctx, instanceID, nodeExec, messages, persistSessionID, fullResponse)
}

// EndSession handles user-initiated session termination. It captures existing
// output (or stores an empty object), updates instance variables, transitions
// the instance to queued, and publishes the interactive.ended event.
// The output is returned to the caller so it can be sent in the HTTP response
// before any state transition that might cause a race with the queue manager.
//
// For a group conflict-resolution session, ending means "finish and verify":
// the completer records the attempt's end and wakes the parent, whose gatherer
// then verifies the staged conflicts (it is NOT an abandon).
func (h *conversationHandler) EndSession(ctx context.Context, instanceID uuid.UUID) (map[string]any, error) {
	// 1. Load and validate instance state (auth/state gate first).
	inst, nodeID, _, group, err := h.loadAndValidate(ctx, instanceID)
	if err != nil {
		return nil, err
	}

	// 1a. Reject if a turn is in flight: ending mid-turn would race HandleMessage's
	// pending state transition + persist (a double-completion). Acquire the same
	// per-instance guard HandleMessage uses, released when EndSession returns.
	if _, inFlight := h.turnInProgress.LoadOrStore(instanceID, struct{}{}); inFlight {
		return nil, fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrInteractiveTurnInProgress)
	}
	defer h.turnInProgress.Delete(instanceID)

	// 2. Load the session record.
	nodeExec, err := h.sessionExecution(ctx, instanceID, nodeID, group)
	if err != nil {
		return nil, err
	}

	// 3. Determine output: use existing if present, otherwise empty object.
	output := nodeExec.Output
	if output == nil {
		output = map[string]any{}
		nodeExec.Output = output
		if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
			return nil, fmt.Errorf("conversation: persist empty output: %w", err)
		}
	}

	// 4. Complete the session (shared with handleSessionCompletion).
	if err := h.completeSession(ctx, inst, instanceID, nodeID, output, "user_ended", group, false); err != nil {
		return nil, err
	}

	return output, nil
}

// sessionExecution returns the record the call addresses: the open group
// session record, or the node's (newest) record for an interactive node.
func (h *conversationHandler) sessionExecution(ctx context.Context, instanceID uuid.UUID, nodeID string, group *groupSessionTarget) (*instance.NodeExecution, error) {
	if group != nil {
		return group.exec, nil
	}
	nodeExec, err := h.nodeExecs.GetByInstanceAndNode(ctx, instanceID, nodeID)
	if err != nil {
		return nil, fmt.Errorf("conversation: load node execution: %w", err)
	}
	return nodeExec, nil
}

// completeSession performs the shared completion steps for both LLM-initiated
// (tool call) and user-initiated session termination:
//   - Update instance variables with output namespaced under node ID
//   - Transition to queued
//   - Publish interactive.ended event
//   - Notify capacity freed
//
// A group conflict-resolution session (group != nil) is dispatched to
// completeGroupSession instead.
func (h *conversationHandler) completeSession(
	ctx context.Context,
	inst *instance.Instance,
	instanceID uuid.UUID,
	nodeID string,
	output map[string]any,
	reason string,
	group *groupSessionTarget,
	abandoned bool,
) error {
	if group != nil {
		return h.completeGroupSession(ctx, inst, instanceID, nodeID, output, reason, group, abandoned)
	}

	// 1. Update instance variables with output namespaced under node ID.
	vars := inst.Variables
	if vars == nil {
		vars = map[string]any{}
	}
	vars[nodeID] = output
	if err := h.instances.UpdateVariables(ctx, instanceID, vars); err != nil {
		return fmt.Errorf("conversation: update instance variables: %w", err)
	}

	// 2. Transition to queued.
	if err := h.instances.UpdateState(ctx, instanceID, instance.StateQueued); err != nil {
		return fmt.Errorf("conversation: transition to queued: %w", err)
	}

	// 3. Publish interactive.ended event, tagging the actor when an admin
	// (non-owner) ended the session, and record an attribution row.
	endedData := map[string]any{
		"reason": reason,
		"output": output,
	}
	if actor, isOther := h.nonOwnerActor(ctx, inst); isOther {
		endedData["actorUserId"] = actor.String()
		r := reason
		h.recordInteractiveIntervention(ctx, inst, instance.ActionInteractiveEnded, &r)
	}
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data:      endedData,
	})

	// 4. Notify capacity freed.
	h.queueMgr.NotifyCapacityFreed(ctx)

	return nil
}

// completeGroupSession ends a group conflict-resolution session. It does NOT
// write vars[nodeID] (the group's output is the gather's frozen final output)
// and does NOT transition the instance to queued: the completer records the
// attempt's outcome and wakes the parent only if it is still waiting at this
// node (never a cancelled parent). The completer runs FIRST so a failure
// leaves the session open for a retry; a repeated end then finds the attempt
// already ended (completer no-op) or the session record closed (gate rejects).
func (h *conversationHandler) completeGroupSession(
	ctx context.Context,
	inst *instance.Instance,
	instanceID uuid.UUID,
	nodeID string,
	output map[string]any,
	reason string,
	group *groupSessionTarget,
	abandoned bool,
) error {
	if err := h.groupCompleter.OnResolutionSessionEnded(ctx, instanceID, nodeID, abandoned); err != nil {
		return fmt.Errorf("conversation: record resolution session outcome: %w", err)
	}
	exec := group.exec
	exec.LLMConversation = closeGroupSessionConversation(exec.LLMConversation, reason, time.Now())
	if exec.Output == nil {
		exec.Output = output
	}
	if err := h.nodeExecs.Update(ctx, exec); err != nil {
		return fmt.Errorf("conversation: close group resolution session: %w", err)
	}
	endedData := map[string]any{
		"reason":      reason,
		"output":      output,
		"sessionKind": ConversationKindGroupResolution,
	}
	if actor, isOther := h.nonOwnerActor(ctx, inst); isOther {
		endedData["actorUserId"] = actor.String()
		r := reason
		h.recordInteractiveIntervention(ctx, inst, instance.ActionInteractiveEnded, &r)
	}
	h.eventBus.Publish(instanceID, Event{
		Type:      EventTypeInteractiveEnded,
		Timestamp: time.Now(),
		Data:      endedData,
	})
	return nil
}

// streamChatResponse calls the LLM with streaming and consumes the stream,
// publishing token events along the way. Returns the full response text and
// the provider session id (for resumption) from the terminal chunk.
func (h *conversationHandler) streamChatResponse(
	ctx context.Context,
	instanceID uuid.UUID,
	chatProvider ChatProvider,
	req ChatRequest,
) (response string, sessionID string, err error) {
	stream, err := chatProvider.StreamChat(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("conversation: stream call: %w", err)
	}

	var responseBuilder strings.Builder
	for chunk := range stream {
		if chunk.Error != nil {
			return "", "", fmt.Errorf("conversation: stream error: %w", chunk.Error)
		}
		if chunk.Token != "" {
			responseBuilder.WriteString(chunk.Token)
			h.eventBus.Publish(instanceID, Event{
				Type:      EventTypeInteractiveToken,
				Timestamp: time.Now(),
				Data:      map[string]any{"token": chunk.Token},
			})
		}
		if chunk.SessionID != "" {
			sessionID = chunk.SessionID
		}
		if chunk.Done {
			break
		}
	}
	return responseBuilder.String(), sessionID, nil
}

// loadAndValidate loads an instance and verifies it is in the waiting state
// on an interactive node — or, once SetGroupResolution is wired, on a
// parallel_group node whose OPEN conflict-resolution session is the group's
// active attempt. It returns the instance, current node ID, the node config
// (synthesized from the group's resolverProfile for a group session) and the
// group session target (nil for an interactive node).
func (h *conversationHandler) loadAndValidate(ctx context.Context, instanceID uuid.UUID) (*instance.Instance, string, workflow.NodeConfig, *groupSessionTarget, error) {
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: load instance: %w", err)
	}

	// Authorization: participating in (writing to) an interactive session is a
	// mutate action, allowed only for the instance owner, a lead of the run's
	// department, or an admin (canIntervene). A caller who cannot even view gets
	// ErrNotFound so the interactive surface does not leak existence; a viewing
	// peer who cannot intervene gets ErrForbidden. This gate runs before the
	// state check so a non-viewer never learns the session's state. Because both
	// HandleMessage and EndSession funnel through here, the WS, REST, and MCP
	// interactive surfaces all inherit it — group sessions included.
	if !canView(ctx, inst) {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrNotFound)
	}
	if !canIntervene(ctx, inst) {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s: %w", instanceID, instance.ErrForbidden)
	}

	if inst.State != instance.StateWaiting {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s (state: %s): %w", instanceID, inst.State, instance.ErrNotInteractiveWait)
	}

	if inst.CurrentNodeID == nil || *inst.CurrentNodeID == "" {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: instance %s has no current node: %w", instanceID, instance.ErrNotInteractiveWait)
	}
	nodeID := *inst.CurrentNodeID

	node, err := FindNodeByID(inst.WorkflowSnapshot, nodeID)
	if err != nil {
		return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: %w", err)
	}
	if workflow.IsInteractive(node.Type) {
		return inst, nodeID, node.Config, nil, nil
	}
	if node.Type == workflow.NodeTypeParallelGroup && h.groups != nil && h.groupCompleter != nil {
		target, cfg, err := h.openGroupSession(ctx, inst, nodeID)
		if err != nil {
			return nil, "", workflow.NodeConfig{}, nil, err
		}
		return inst, nodeID, cfg, target, nil
	}
	return nil, "", workflow.NodeConfig{}, nil, fmt.Errorf("conversation: node %s is not interactive (type: %s)", nodeID, node.Type)
}

// openGroupSession admits a parallel_group node ONLY when the group is
// resolving conflicts AND the node's newest record is an open
// group-resolution session whose identity equals the run's ActiveSession. The
// group's wait trace, an ended session, and an earlier merge's or attempt's
// session are all refused with ErrNotInteractiveWait.
func (h *conversationHandler) openGroupSession(ctx context.Context, inst *instance.Instance, nodeID string) (*groupSessionTarget, workflow.NodeConfig, error) {
	notOpen := func(why string) error {
		return fmt.Errorf("conversation: instance %s: parallel_group %q has no open conflict-resolution session (%s): %w",
			inst.ID, nodeID, why, instance.ErrNotInteractiveWait)
	}
	run, err := h.groups.GetByParentNode(ctx, inst.ID, nodeID)
	if err != nil {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: load group run: %w", err)
	}
	if run == nil {
		return nil, workflow.NodeConfig{}, notOpen("no group run")
	}
	if run.State != instance.GroupRunResolvingConflicts || run.Gather == nil || run.Gather.ActiveSession == nil {
		return nil, workflow.NodeConfig{}, notOpen("the group is not resolving conflicts")
	}
	exec, err := latestNodeExecution(ctx, h.nodeExecs, inst.ID, nodeID)
	if err != nil {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: load node execution: %w", err)
	}
	if exec == nil {
		return nil, workflow.NodeConfig{}, notOpen("no session record")
	}
	ident, ok := GroupResolutionIdentityOf(exec.LLMConversation)
	if !ok || !ident.Open {
		return nil, workflow.NodeConfig{}, notOpen("the newest record is not an open resolution session")
	}
	active := run.Gather.ActiveSession
	if ident.GroupRunID != run.ID || ident.MergeIndex != active.MergeIndex || ident.Attempt != active.Attempt {
		return nil, workflow.NodeConfig{}, notOpen(fmt.Sprintf("session for merge %d attempt %d is not the active merge %d attempt %d",
			ident.MergeIndex, ident.Attempt, active.MergeIndex, active.Attempt))
	}
	handle, ok := GatherHandleFromReport(run.Gather)
	if !ok {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: group run %s has no staged gather to resolve in", run.ID)
	}
	profile := groupConfigString(run.Config, "resolverProfile")
	if profile == "" {
		return nil, workflow.NodeConfig{}, fmt.Errorf("conversation: parallel_group %q has no resolverProfile", nodeID)
	}
	return &groupSessionTarget{run: run, exec: exec, ident: ident, workDir: handle.StagingDir},
		workflow.NodeConfig{ModelProfile: profile}, nil
}

// FindNodeByID parses a workflow snapshot and returns the node with the given ID.
// This is the shared helper that eliminates the duplicated marshal/unmarshal
// pattern across instance_handler.go, stream_handler.go, and conversation.go.
func FindNodeByID(snapshot map[string]any, nodeID string) (*workflow.Node, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("workflow snapshot is nil")
	}
	wfBytes, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal workflow snapshot: %w", err)
	}
	var wf workflow.Workflow
	if err := json.Unmarshal(wfBytes, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow snapshot: %w", err)
	}
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == nodeID {
			return &wf.Nodes[i], nil
		}
	}
	return nil, fmt.Errorf("node %s not found in workflow snapshot", nodeID)
}

// conversationNonce extracts the per-instance sentinel nonce persisted by
// InteractiveExecutor on the first turn. Empty when the conversation was
// created before the nonce was introduced — extractSentinelResult will refuse
// to parse an empty-nonce sentinel block, so the worst case is a session that
// cannot be completed via the sentinel and must be ended by the operator.
func conversationNonce(conv map[string]any) string {
	if conv == nil {
		return ""
	}
	n, _ := conv[ConversationKeyNonce].(string)
	return n
}

// parseConversation extracts the messages array from an LLM conversation map.
func parseConversation(conv map[string]any) []map[string]any {
	if conv == nil {
		return nil
	}

	rawMessages, ok := conv[ConversationKeyMessages]
	if !ok {
		return nil
	}

	switch msgs := rawMessages.(type) {
	case []map[string]any:
		// Make a copy to avoid mutating the original.
		result := make([]map[string]any, len(msgs))
		copy(result, msgs)
		return result
	case []any:
		result := make([]map[string]any, 0, len(msgs))
		for _, m := range msgs {
			if msgMap, ok := m.(map[string]any); ok {
				result = append(result, msgMap)
			}
		}
		return result
	default:
		return nil
	}
}

// nextConversation carries every key of prev forward — the nonce and, for a
// group conflict-resolution session, its identity — and replaces the session
// id and messages. For an ordinary interactive conversation (sessionId, nonce,
// messages only) the result has exactly the keys it always had.
func nextConversation(prev map[string]any, sessionID string, messages []map[string]any) map[string]any {
	out := make(map[string]any, len(prev)+3)
	for k, v := range prev {
		out[k] = v
	}
	out[ConversationKeySessionID] = sessionID
	out[ConversationKeyNonce] = conversationNonce(prev)
	out[ConversationKeyMessages] = messages
	return out
}

// handleSessionCompletion processes a session completion signalled by the
// result sentinel block in the LLM response.
func (h *conversationHandler) handleSessionCompletion(
	ctx context.Context,
	instanceID uuid.UUID,
	nodeID string,
	nodeExec *instance.NodeExecution,
	messages []map[string]any,
	sessionID string,
	output map[string]any,
	group *groupSessionTarget,
) error {
	// 1. Persist the conversation (assistant message already appended by the
	//    caller) with the refreshed session id. Carry the nonce (and any group
	//    session identity) forward.
	nodeExec.LLMConversation = nextConversation(nodeExec.LLMConversation, sessionID, messages)

	// 2. Store structured output on node execution.
	nodeExec.Output = output

	// 3. Persist node execution.
	if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
		return fmt.Errorf("conversation: persist session completion: %w", err)
	}

	// 4. Reload instance for fresh variables (avoids stale read).
	inst, err := h.instances.Get(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("conversation: reload instance for variables: %w", err)
	}

	// 5. Complete the session (shared steps).
	return h.completeSession(ctx, inst, instanceID, nodeID, output, "llm_completed", group, false)
}

// handleNormalResponse persists the assistant's text response to the
// conversation (already appended by the caller) with the refreshed session id.
func (h *conversationHandler) handleNormalResponse(
	ctx context.Context,
	instanceID uuid.UUID,
	nodeExec *instance.NodeExecution,
	messages []map[string]any,
	sessionID string,
	fullResponse string,
) error {
	// 1. Persist the conversation with the refreshed session id. Carry the
	//    nonce (and any group session identity) forward so subsequent turns
	//    continue to scope sentinel parsing and address the same session.
	nodeExec.LLMConversation = nextConversation(nodeExec.LLMConversation, sessionID, messages)

	// 2. Persist.
	if err := h.nodeExecs.Update(ctx, nodeExec); err != nil {
		return fmt.Errorf("conversation: persist assistant response: %w", err)
	}

	// 3. Publish interactive.message event for assistant.
	publishInteractiveMessage(h.eventBus, instanceID, "assistant", fullResponse)

	return nil
}
```

(The removed `findInteractiveNodeConfig` (old lines 446-458) had no callers outside `loadAndValidate`; its check now lives inline there.)

- [x] Step 18: Run the new tests, then the whole engine package (existing conversation/interactive tests guard the unchanged ordinary path).

```bash
go test ./domain/engine/ -run 'TestConversationHandler_Group|TestInteractiveExecutor_GroupResolutionRecord|TestInteractiveExecutor_OrdinaryCompletedRecord' -count=1
go test ./domain/engine/ -count=1
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
ok  	github.com/arbitron/arbitron/domain/engine	...
```

- [x] Step 19: gofmt, vet, and the adapters that consume the handler.

  All specified focused tests, whole engine package, adapter HTTP/MCP/SQLite packages, formatting and domain/adapter vet pass. The focused bridge/conversation/guard tests also pass with `-race`.

  Final self-review, in required order:
  1. Spec compliance: compared design §4 and this task's contracts against notification/bridge/conversation changes. Kept autonomous verification with no initial human notification, exact session identity, owner-scoped prompts, agent-kind enforcement, ordinary interactive behavior and group-specific completion. Abandon transports/recovery/root enablement remain in their assigned later tasks; no extra options/features added.
  2. Anti-green-theater: added `TestGroupResolutionBridge_GatherAndConversationReachFileAndPersistenceSinks`. Real Git snapshot/fork/finalize/merge producers feed the gatherer and bridge; the stream fake reads/edits/stages ONLY the supplied WorkDir. Real SQLite session/notification/parent rows prove persisted history, notification admission, node-addressed wake and no group-variable overwrite; parent file bytes prove both automatic and human repair. Temporarily emptied production StartResolution.WorkDir: both integration cases failed (automatic case asserted failure route and unchanged baseline bytes). Restored the producer; tests green. This local seam test does not replace the still-missing initDomain test.
  3. Code quality: matched neighboring executor/gatherer/repository patterns and removed unused target fields. Added red/green regressions for full active-session address matching and malformed identity numbers/UUIDs (negative/zero, non-finite, overflow). Formatting, vet, diff checks and all task tests rerun green.

```bash
gofmt -l domain/engine/conversation.go domain/engine/group_resolution.go domain/engine/executor_interactive.go domain/engine/conversation_group_test.go domain/engine/group_resolution_test.go domain/engine/executor_interactive_group_test.go domain/notification/notification.go
go vet ./domain/... ./adapter/...
go test ./adapter/http/ ./adapter/mcp/ ./adapter/sqlite/ -count=1
```
Expected: no `gofmt`/`vet` output; `ok` for each package.

- [x] Step 20: Commit.

```bash
git add domain/engine/conversation.go domain/engine/executor_interactive.go domain/engine/conversation_group_test.go domain/engine/executor_interactive_group_test.go
git commit -m "feat(engine): conversation gate and completion dispatch for group conflict-resolution sessions" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

- [x] Completion gate: Prove resolution sessions, notifications and gathered-file downstream variables through the real production composition root.

> RESOLVED: P3-T10 recovery and P3-T11 production wiring are implemented. `initDomain` constructs the bridge with real repositories, profile resolver, streaming selector, file reader, engine notifier, bus and node-addressed queue; wires the shared conversation handler, gatherer and reconciler; and enables file gathering before freezing executors. The previous blocker was missing construction and inputs at that root, not missing local bridge behavior.

Completion-pass verification (no production behavior changes):
- Focused bridge/identity/notification/conversation/interactive-guard tests pass with `-race`.
- Real-root `TestGroupFilesE2E_ConflictOpensAPersistedSessionAndWaitsUntilResolved`, `TestGroupFilesE2E_AutomaticRepairContinuesWithoutOperator` and `TestGroupFilesE2E_RestartRestoresAnInterruptedResolution` pass with `-race`. They assert persisted session and notification rows, unresolved-parent suspension, automatic repair without human notification, restart recovery, and nonempty gathered bytes read by downstream nodes into persisted parent variables.
- Whole engine package and HTTP/MCP/SQLite packages pass; specified gofmt and domain/adapter vet checks are clean.

Completion self-review, in the required order:
1. Spec compliance: compared this task's bridge/session/notification contracts with `group_resolution.go`, the conversation gate and production construction. Retained authoritative index verification, exact active-session identity, agent-profile enforcement and group-specific completion without variable overwrite. No new feature or option was needed.
2. Anti-green-theater: inspected real task-file producers, the CLI resolver's actual staging-directory edits, persisted notification/session sinks and downstream variable reads. Temporarily removed only production `SetGroupResolution` wiring (retaining a compiling unused binding): the real-root conflict test failed at `EndSession` with `node group-1 is not interactive`. Restored wiring; no mutation remains. Existing local WorkDir mutation evidence above and the automatic real-CLI repair cover nonempty file inputs.
3. Code quality: checked bridge/conversation/interactive-executor conventions and required constructor dependencies, formatting and vet. No cleanup was necessary. Reran focused race tests and all three real-root race sink tests after review, green; `git diff --check` is clean.

This pass closes only P3-T8's completion gate; other tasks' completion gates remain for their own passes.
