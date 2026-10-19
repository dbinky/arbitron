# Parallel Workflow Gathering — Phase 3 Task 6: GroupGatherer (file gather state machine)

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Implement the resumable, rollback-safe file gatherer that takes a `results_and_files` group run from `decided` through `gathering` (and `resolving_conflicts`) to `done`, merging only eligible task refs in author order into the parent's working area.
**Architecture:** One new domain file `domain/engine/group_gatherer.go` with `*FileGatherer` (constructed by `NewGroupGatherer`, implements the Phase 2 `GroupGatherer` port). It reads the immutable Phase 2 decision, drives the `WorkingArea` port (P3-T2/T3) one merge at a time, persists a `GatherReport` (P3-T4) after every step so a crash resumes without re-applying a delta, and asks a `GroupResolutionStarter` (port declared here, implemented by P3-T8) to open a resolution session on conflict. Any merge/apply/session error or an abandoned/cancelled session rolls back the parent's area and routes `failure` without touching the decision.
**Tech Stack:** Go 1.25.5, standard library (`context`, `encoding/json`, `errors`, `sort`, `strings`, `time`), `github.com/google/uuid`, testify.
**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` ("Gathering file changes"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-3.md` §3 "Gatherer between `decided` and `done`", §1 (persist operation identity / merge index / rollback handle), Error handling ("Merge/session errors abort and restore pre-gather state; rollback failure remains visible and blocks continuation").

## Required automatic-resolution and final-route TDD cycles

Before the gatherer implementation, add tests for all five categories:

- Happy/success: `TestFileGatherer_AutomaticResolutionContinuesWithoutHumanEnd` uses
  `fakeResolutionStarter.onStart` to actually repair the keyed staging area via
  `fakeGatherArea.resolveStaged`. Assert the SAME gather call commits, applies bytes to
  `parentFiles`, returns success and closes exactly that session. Merely returning a
  synthetic `Clean` flag or completion sentence cannot satisfy this test.
- Failure: a claimed success/sentinel with unmerged paths still present returns waiting;
  no parent bytes or downstream continuation change. Keep the existing abandon/rollback tests.
- Error: conflict verification or session-close persistence failure blocks continuation
  and is recoverable. Inject errors at each new persistence boundary, including close
  before attempt-end persistence, and verify P3-T10 restores from the closed session.
- Edge: an automatic first turn that needs human help remains an open session; a later
  successful human end verifies and continues once. Replay a closed automatic session
  after restart without launching another agent or notifying a human.

`finish` must distinguish the final group outcome from the immutable task decision:
set `out["taskOutcome"] = p.run.Decision.Outcome` and `out["outcome"] = port` before
`SetFinalOutput`. Add a successful task decision followed by apply/merge failure and
assert failure port, `outcome == "failure"`, `taskOutcome == "success"`, unchanged
`run.Decision`, rollback details, and a downstream failure sink. P2-T9 emits
`taskOutcome` in results-only output too, equal to its decision outcome.


## Fake argument assertions

The automatic-resolution test asserts `fakeResolutionStarter.closed` equals the full
returned session ref and `notified` is empty. The unresolved test asserts `notified`
equals that ref and `closed` is empty; include a second run with a different ref so a
constant session identity fails. Gather-only fake snapshot/fork/finalize methods return
errors when called; launcher and finalizer tests use their own argument-checking fixtures.


**Interfaces**
- Consumes:
  - P2-T1 `domain/instance`: `GroupRun` (fields `ID, ParentInstanceID, NodeID, CompletionRule, GatherMode, Config, WorkspaceSnapshot, State, Decision, FinalOutput, ContinuationPort, Tasks`), `GroupTask` (`Position, Key, FinalizedRef`), `GroupDecision` (`Outcome, WinnerKey`), states `GroupRunDecided, GroupRunGathering, GroupRunResolvingConflicts, GroupRunDone`, `GroupRunRepository` (`Get`, `TransitionState`, `SetFinalOutput`), `ErrGroupRunNotFound`.
  - P3-T4 `domain/instance`: `GatherReport`, `GatherTaskReport`, `ResolutionAttempt`, `ResolutionSessionRef`, `GroupRun.Gather`, `GroupRunRepository.SetGatherReport(ctx, runID uuid.UUID, report GatherReport) error`.
  - P1-T1 `domain/workflow`: `GroupGatherResultsAndFiles`, `GroupRuleFirstSuccess`, `GroupPortSuccess`, `GroupPortFailure`.
  - P2-T9 `domain/engine/group_output.go`: `func BuildGroupOutput(run *instance.GroupRun) map[string]any`.
  - P2-T11 `domain/engine/executor_group.go`: `type GroupGatherer interface { Gather(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (*GatherStep, error) }`, `type GatherStep struct { Done bool; Port string; Output map[string]any }`.
  - P2-T8 `domain/engine/group_events.go`: `EventTypeGroupGathering`, `EventTypeGroupResolved`, `EventTypeGroupCompleted`.
  - P3-T2/T3 `domain/engine/working_area.go`: `WorkingArea` (`BeginGather`, `MergeTask`, `ConflictPaths`, `CommitResolution`, `Apply`, `Rollback`), `GatherHandle`, `MergeOutcome`.
  - Existing: `domain/engine/ports.go:279` `EventBus`, `ports.go:260` `Event`, `domain/engine/conventions.go:15` `ReservedVarWorkspace`.
- Relied-on `WorkingArea` semantics (P3-T3 must honour them; the crash-resume tests below pin the gatherer's side): `MergeTask` of a ref already contained in the staging history returns `Clean: true` without re-applying it; `CommitResolution` with no merge in progress returns `nil`; `Apply` makes `parentDir` *equal* the staged tree (idempotent); `Rollback` restores the exact pre-gather capture (idempotent); none of them touch `parentDir` except `Apply` and `Rollback`.
- Produces (exact):
  - `type ResolutionRequest struct { Handle GatherHandle; MergeIndex int; Attempt int; TaskKey string; TaskRef string; ConflictPaths []string }`
  - `type GroupResolutionStarter interface { StartResolution(ctx context.Context, run *instance.GroupRun, parent *instance.Instance, req ResolutionRequest) (*instance.ResolutionSessionRef, error); CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error; NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error }` (declared here, by its consumer; P3-T8's `*GroupResolutionBridge` implements it)
  - `type FileGatherer struct{ ... }`; `func NewGroupGatherer(groups instance.GroupRunRepository, area WorkingArea, bridge GroupResolutionStarter, bus EventBus) *FileGatherer`
  - `func (g *FileGatherer) Gather(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (*GatherStep, error)`
  - `func (g *FileGatherer) AbortGather(ctx context.Context, run *instance.GroupRun, outcome, reason string) (*GatherStep, error)` (P3-T10 reconciler)
  - `func GatherHandleFromReport(r *instance.GatherReport) (GatherHandle, bool)` (P3-T8 conversation gate, P3-T10 reconciler)
  - `var ErrGroupGatherRollbackFailed = errors.New("parallel_group gather could not restore the parent's working area")`
  - Constants `ResolutionOutcomeResolved`, `ResolutionOutcomeStillConflicted`, `ResolutionOutcomeAbandoned`, `ResolutionOutcomeCancelled`, `ResolutionOutcomeError` — aliases of P3-T4's `instance.ResolutionOutcome*` (values `resolved|still_conflicted|abandoned|cancelled|error`).
  - Package-private helpers reused by P3-T8/P3-T10: `copyGatherReport`, `findAttempt`, `latestAttemptIndex`.
  - Test fakes in `domain/engine/group_gather_fakes_test.go` (package `engine_test`), reused BY NAME by P3-T8, P3-T9, P3-T10: `fakeGatherArea` / `newFakeGatherArea`, `fakeResolutionStarter`, and the helper methods `put`, `snapshot`, `update`, `failCall` added to P2-T8's `memGroupRuns`. `memGroupRuns` / `newMemGroupRuns` themselves are P2-T8's shared fake (`domain/engine/group_fakes_test.go`, extended by P3-T4 with `SetGatherReport` / `SetTaskFinalizedRef`) and are NOT redeclared (a second declaration in package `engine_test` would not compile).

**Wiring & data provenance**
- Composition root: **P3-T11** (`cmd/arbitron/main.go`: `gatherer := engine.NewGroupGatherer(groupRunRepo, workingArea, groupResolution, bus)`; `groupExecutor.SetGatherer(gatherer)`). The executor-side call already exists: P2-T11 `ParallelGroupExecutor.resolve` (`domain/engine/executor_group.go`) calls `x.gatherer.Gather(ctx, run, parent)` on re-entry when the run is files-mode and `gatherableGroupState` (`decided|gathering|resolving_conflicts`), routes a `Done` step and marks the continuation delivered, and suspends otherwise. **P3-T10** adds the file-mode integration around it (repoPath resolution, startup gather reconciliation via `AbortGather`, ref pruning) without a second `Gather` call path.
- Producers of every input:
  - `run` — loaded by `ParallelGroupExecutor` (P2-T11, `domain/engine/executor_group.go`) from `adapter/sqlite/group_run_repo.go:GroupRunRepo.GetByParentNode` (P2-T3).
  - `run.Decision` — `GroupCoordinator.Evaluate` → `GroupRunRepository.Decide` (P2-T10); frozen tasks built by `DecideGroup` (P2-T9).
  - `run.Tasks[].FinalizedRef` — `GroupFileFinalizer` (P3-T5, `domain/engine/group_finalize.go`) via `GroupRunRepository.SetTaskFinalizedRef` (P3-T4) before the task's `SetCompleted`.
  - `run.WorkspaceSnapshot` — `GroupLauncher.Launch` (P2-T8) from `WorkingArea.SnapshotParent` (P3-T2 `adapter/git` `gitarea.WorkingArea`).
  - `parent.Variables["workspace"]` — `domain/engine/workspace.go:Engine.prepareWorkspace` → `WorkspaceManager.EnsureInstanceDir` (`adapter/filesystem/workspace.go`).
  - `area` — `gitarea.NewWorkingArea(cfg.Workspace.Path)` (P3-T2/T3, wired by P3-T11).
  - `bridge` — `engine.NewGroupResolutionBridge(...)` (P3-T8, wired by P3-T11).
  - `bus` — `adapter/eventbus.New()` (main.go:621).
- Provenance probes: `TestFileGatherer_Provenance_ParentDirComesFromParentWorkspaceVariable` (fails if the gatherer ignores the parent's `workspace` variable), `TestFileGatherer_MissingFinalizedRef_RoutesFailureWithReason` and `TestFileGatherer_ParentWithoutWorkspace_RoutesFailure` (fail if an empty producer value is silently merged), and the SINK is always `fakeGatherArea.parentFiles(<parent workspace>)` — the parent's working-area content.

### Task 6: GroupGatherer (FileGatherer)
**Files:**
- Create: `domain/engine/group_gatherer.go`
- Create: `domain/engine/group_gather_fakes_test.go` (package `engine_test`; shared fakes reused by P3-T8/T9/T10)
- Test:   `domain/engine/group_gatherer_test.go` (package `engine_test`)

No existing file is modified. Tests reuse the existing package fakes `mockEventBus` / `newMockEventBus` (`domain/engine/engine_test.go:665-701`, method `eventsOfType`) and P2-T8's `memGroupRuns` / `newMemGroupRuns` / `copyGroupRun` (`domain/engine/group_fakes_test.go`, with P3-T4's `SetGatherReport` / `SetTaskFinalizedRef`; its `callCount(entry)` counts exact call entries, its `get(id)` returns a deep copy, its `onCall` hook / `failNext` map drive `failCall`).

- [x] Step 1: Create the shared fakes. Create `domain/engine/group_gather_fakes_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// memGroupRuns helpers for the gather tests.
//
// memGroupRuns / newMemGroupRuns are NOT redeclared here: they are P2-T8's
// shared in-memory instance.GroupRunRepository (domain/engine/group_fakes_test.go,
// same package), which already honours every compare-and-set of the contract
// and deep-copies on every read and write, and which P3-T4 extended with
// SetGatherReport (validated, frozen once the run is done — exactly like the
// SQLite repo) and SetTaskFinalizedRef. The gather tests only add setup and
// assertion helpers on top of it.
// ---------------------------------------------------------------------------

// put stores a deep copy of run (test setup). Re-putting an existing id
// replaces it without duplicating the listing order.
func (m *memGroupRuns) put(run *instance.GroupRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := copyGroupRun(run)
	if _, exists := m.runs[cp.ID]; !exists {
		m.order = append(m.order, cp.ID)
	}
	m.runs[cp.ID] = cp
	for _, t := range cp.Tasks {
		if t.TerminalSeq != nil && *t.TerminalSeq > m.seq {
			m.seq = *t.TerminalSeq
		}
	}
}

// snapshot returns a deep copy of the stored run, or nil (test assertions).
func (m *memGroupRuns) snapshot(id uuid.UUID) *instance.GroupRun {
	return m.get(id)
}

// update mutates the stored run in place (test setup).
func (m *memGroupRuns) update(id uuid.UUID, fn func(*instance.GroupRun)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.runs[id])
}

// failCall makes the nth call (1-based, counted from now on) of method fail
// with err, before any mutation — how the crash-resume tests simulate a
// process dying between a working-area step and its persistence. It chains
// onto P2-T8's onCall hook, which begin invokes (unlocked) before its
// failNext check.
func (m *memGroupRuns) failCall(method string, n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.onCall
	seen := 0
	m.onCall = func(called string) {
		if prev != nil {
			prev(called)
		}
		if called != method {
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		seen++
		if seen == n {
			m.failNext[method] = err
		}
	}
}

// ---------------------------------------------------------------------------
// fakeGatherArea — an engine.WorkingArea keyed by ref and parent directory.
// ---------------------------------------------------------------------------

// fakeBegin records one BeginGather call.
type fakeBegin struct {
	snapshot  string
	gatherID  uuid.UUID
	parentDir string
}

// fakeGather is one staged gather: a copy of the parent's files at
// BeginGather (the rollback capture), the staged tree merges write into, the
// refs already merged, and an in-progress conflict.
type fakeGather struct {
	handle       engine.GatherHandle
	baseline     map[string]string
	staged       map[string]string
	merged       map[string]bool
	pending      string
	pendingPaths []string
}

// fakeGatherArea models the working area as maps of relative path -> content.
// parents[dir] is the SINK: the parent's working-area content the gather must
// change only through Apply (and restore through Rollback). refs[ref] is the
// set of files a finalized task ref changed; conflicts[ref] scripts which of
// those paths conflict when that ref is merged. A conflicted path is staged
// with a "<<<<<<<" marker and stays conflicted until resolveStaged rewrites it.
type fakeGatherArea struct {
	mu                sync.Mutex
	parents           map[string]map[string]string
	refs              map[string]map[string]string
	conflicts         map[string][]string
	gathers           map[uuid.UUID]*fakeGather
	merges            []string
	appliedRefs       map[string]int
	beginCalls        []fakeBegin
	applies           int
	rollbacks         int
	commits           int
	releasedRefs      []string
	releasedSnapshots []string

	failBegin           error
	failMerge           map[string]error
	failConflictPaths   error
	failCommit          error
	failApply           error
	failApplyAfterWrite error
	failRollback        error
	failReleaseRef      error
	failReleaseSnapshot error
}

var _ engine.WorkingArea = (*fakeGatherArea)(nil)

func newFakeGatherArea() *fakeGatherArea {
	return &fakeGatherArea{
		parents:     make(map[string]map[string]string),
		refs:        make(map[string]map[string]string),
		conflicts:   make(map[string][]string),
		gathers:     make(map[uuid.UUID]*fakeGather),
		failMerge:   make(map[string]error),
		appliedRefs: make(map[string]int),
	}
}

func copyFiles(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (a *fakeGatherArea) SnapshotParent(context.Context, uuid.UUID, string) (string, error) {
	return "", errors.New("gather-only fake: SnapshotParent must use the launch fixture")
}

func (a *fakeGatherArea) ForkTask(context.Context, string, string) error {
	return errors.New("gather-only fake: ForkTask must use the launch fixture")
}

func (a *fakeGatherArea) ReleaseSnapshot(_ context.Context, handle string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failReleaseSnapshot != nil {
		return a.failReleaseSnapshot
	}
	a.releasedSnapshots = append(a.releasedSnapshots, handle)
	return nil
}

func (a *fakeGatherArea) FinalizeTask(context.Context, string, uuid.UUID, string) (string, error) {
	return "", errors.New("gather-only fake: FinalizeTask must use the finalizer fixture")
}

func (a *fakeGatherArea) BeginGather(_ context.Context, snapshotHandle string, gatherID uuid.UUID, parentDir string) (engine.GatherHandle, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.beginCalls = append(a.beginCalls, fakeBegin{snapshot: snapshotHandle, gatherID: gatherID, parentDir: parentDir})
	if a.failBegin != nil {
		return engine.GatherHandle{}, a.failBegin
	}
	h := engine.GatherHandle{
		SnapshotHandle: snapshotHandle,
		GatherID:       gatherID,
		StagingDir:     "/staging/" + gatherID.String(),
		RollbackRef:    "refs/arbitron/rollback/" + gatherID.String(),
		ParentDir:      parentDir,
	}
	a.gathers[gatherID] = &fakeGather{
		handle:   h,
		baseline: copyFiles(a.parents[parentDir]),
		staged:   copyFiles(a.parents[parentDir]),
		merged:   make(map[string]bool),
	}
	return h, nil
}

func (a *fakeGatherArea) gather(h engine.GatherHandle) (*fakeGather, error) {
	g, ok := a.gathers[h.GatherID]
	if !ok {
		return nil, fmt.Errorf("fake area: unknown gather %s", h.GatherID)
	}
	return g, nil
}

func (a *fakeGatherArea) MergeTask(_ context.Context, h engine.GatherHandle, taskRef string) (engine.MergeOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.merges = append(a.merges, taskRef)
	if err := a.failMerge[taskRef]; err != nil {
		return engine.MergeOutcome{}, err
	}
	g, err := a.gather(h)
	if err != nil {
		return engine.MergeOutcome{}, err
	}
	if g.merged[taskRef] {
		return engine.MergeOutcome{Clean: true}, nil // already in the staged history: no-op
	}
	if g.pending != "" {
		return engine.MergeOutcome{}, fmt.Errorf("fake area: merge of %s still in progress", g.pending)
	}
	delta, ok := a.refs[taskRef]
	if !ok {
		return engine.MergeOutcome{}, fmt.Errorf("fake area: unknown ref %s", taskRef)
	}
	if paths := a.conflicts[taskRef]; len(paths) > 0 {
		g.pending = taskRef
		g.pendingPaths = append([]string(nil), paths...)
		for _, p := range paths {
			g.staged[p] = "<<<<<<< staged\n=======\n" + delta[p] + "\n>>>>>>> " + taskRef
		}
		return engine.MergeOutcome{Clean: false, ConflictPaths: append([]string(nil), paths...)}, nil
	}
	for p, c := range delta {
		g.staged[p] = c
	}
	g.merged[taskRef] = true
	a.appliedRefs[taskRef]++
	return engine.MergeOutcome{Clean: true}, nil
}

func (a *fakeGatherArea) ConflictPaths(_ context.Context, h engine.GatherHandle) ([]string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failConflictPaths != nil {
		return nil, a.failConflictPaths
	}
	g, err := a.gather(h)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range g.pendingPaths {
		if strings.Contains(g.staged[p], "<<<<<<<") {
			out = append(out, p)
		}
	}
	return out, nil
}

func (a *fakeGatherArea) CommitResolution(_ context.Context, h engine.GatherHandle, _ string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failCommit != nil {
		return a.failCommit
	}
	g, err := a.gather(h)
	if err != nil {
		return err
	}
	if g.pending == "" {
		return nil // nothing in progress: idempotent
	}
	for _, p := range g.pendingPaths {
		if strings.Contains(g.staged[p], "<<<<<<<") {
			return fmt.Errorf("fake area: %s is still conflicted", p)
		}
	}
	conflicted := make(map[string]bool, len(g.pendingPaths))
	for _, p := range g.pendingPaths {
		conflicted[p] = true
	}
	for p, c := range a.refs[g.pending] {
		if !conflicted[p] {
			g.staged[p] = c
		}
	}
	g.merged[g.pending] = true
	a.appliedRefs[g.pending]++
	a.commits++
	g.pending = ""
	g.pendingPaths = nil
	return nil
}

func (a *fakeGatherArea) Apply(_ context.Context, h engine.GatherHandle) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applies++
	if a.failApply != nil {
		return a.failApply
	}
	g, err := a.gather(h)
	if err != nil {
		return err
	}
	a.parents[h.ParentDir] = copyFiles(g.staged)
	if a.failApplyAfterWrite != nil {
		return a.failApplyAfterWrite
	}
	return nil
}

func (a *fakeGatherArea) Rollback(_ context.Context, h engine.GatherHandle) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rollbacks++
	if a.failRollback != nil {
		return a.failRollback
	}
	g, err := a.gather(h)
	if err != nil {
		return err
	}
	a.parents[h.ParentDir] = copyFiles(g.baseline)
	return nil
}

func (a *fakeGatherArea) ReleaseRef(_ context.Context, snapshotHandle, ref string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failReleaseRef != nil {
		return a.failReleaseRef
	}
	a.releasedRefs = append(a.releasedRefs, snapshotHandle+"|"+ref)
	return nil
}

// resolveStaged writes content to path in every gather with a pending
// conflict, the way the resolution agent edits the staging directory.
func (a *fakeGatherArea) resolveStaged(path, content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, g := range a.gathers {
		if g.pending != "" {
			g.staged[path] = content
		}
	}
}

func (a *fakeGatherArea) parentFiles(dir string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return copyFiles(a.parents[dir])
}

func (a *fakeGatherArea) mergeOrder() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.merges...)
}

func (a *fakeGatherArea) begins() []fakeBegin {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]fakeBegin(nil), a.beginCalls...)
}

func (a *fakeGatherArea) applyCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.applies
}

func (a *fakeGatherArea) rollbackCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rollbacks
}

func (a *fakeGatherArea) timesApplied(ref string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.appliedRefs[ref]
}

// ---------------------------------------------------------------------------
// fakeResolutionStarter — records StartResolution requests.
// ---------------------------------------------------------------------------

type startedResolution struct {
	runID    uuid.UUID
	parentID uuid.UUID
	nodeID   string
	req      engine.ResolutionRequest
}

// fakeResolutionStarter builds the session reference from the run, parent and
// request it is handed (never from fixed values), so a test that passes the
// wrong run or attempt sees it in the returned identity.
type fakeResolutionStarter struct {
	mu      sync.Mutex
	started []startedResolution
	err     error
	onStart func(engine.ResolutionRequest)
	closed  []instance.ResolutionSessionRef
	notified []instance.ResolutionSessionRef
	closeErr error
	notifyErr error
}

var _ engine.GroupResolutionStarter = (*fakeResolutionStarter)(nil)

func (s *fakeResolutionStarter) StartResolution(_ context.Context, run *instance.GroupRun, parent *instance.Instance, req engine.ResolutionRequest) (*instance.ResolutionSessionRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run == nil || parent == nil {
		return nil, errors.New("fake starter: run and parent are required")
	}
	s.started = append(s.started, startedResolution{runID: run.ID, parentID: parent.ID, nodeID: run.NodeID, req: req})
	if s.err != nil {
		return nil, s.err
	}
	if s.onStart != nil {
		s.onStart(req)
	}
	return &instance.ResolutionSessionRef{
		GroupRunID: run.ID,
		MergeIndex: req.MergeIndex,
		Attempt:    req.Attempt,
		InstanceID: parent.ID,
		NodeID:     run.NodeID,
	}, nil
}

func (s *fakeResolutionStarter) NotifyResolutionRequired(_ context.Context, ref instance.ResolutionSessionRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ref.GroupRunID == uuid.Nil || ref.InstanceID == uuid.Nil || ref.NodeID == "" {
		return errors.New("fake starter: invalid notification identity")
	}
	s.notified = append(s.notified, ref)
	return s.notifyErr
}

func (s *fakeResolutionStarter) CloseSession(_ context.Context, ref instance.ResolutionSessionRef, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ref.GroupRunID == uuid.Nil || ref.InstanceID == uuid.Nil || ref.NodeID == "" || reason != "completed" {
		return errors.New("fake starter: invalid session closure")
	}
	s.closed = append(s.closed, ref)
	return s.closeErr
}

func (s *fakeResolutionStarter) calls() []startedResolution {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]startedResolution(nil), s.started...)
}
```

- [x] Step 2: Write the failing gatherer tests. Create `domain/engine/group_gatherer_test.go`:

```go
package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const gatherParentDir = "/ws/parent-1"

func taskRef(key string) string { return "refs/arbitron/tasks/" + key }

type gatherFixture struct {
	t        *testing.T
	groups   *memGroupRuns
	area     *fakeGatherArea
	starter  *fakeResolutionStarter
	bus      *mockEventBus
	gatherer *engine.FileGatherer
	runID    uuid.UUID
	parent   *instance.Instance
}

// newGatherFixture persists a DECIDED results_and_files run whose tasks are
// keys in AUTHOR order. Each task's finalized ref changes "<key>.txt" to
// "from-<key>". Tasks complete in the REVERSE of author order (TerminalSeq),
// so any gatherer that merges by completion order fails the order assertions.
// The parent's working area starts with base.txt = "base".
func newGatherFixture(t *testing.T, rule, outcome, winner string, keys ...string) *gatherFixture {
	t.Helper()
	groups := newMemGroupRuns()
	area := newFakeGatherArea()
	starter := &fakeResolutionStarter{}
	bus := newMockEventBus()

	parentID := uuid.New()
	behavior := workflow.GroupUnfinishedWaitForRemaining
	if rule == workflow.GroupRuleFirstSuccess {
		behavior = workflow.GroupUnfinishedFinishIndependently
	}
	run := &instance.GroupRun{
		ID:                 uuid.New(),
		ParentInstanceID:   parentID,
		NodeID:             "g1",
		Attempt:            1,
		Form:               workflow.GroupFormBranches,
		CompletionRule:     rule,
		UnfinishedBehavior: behavior,
		GatherMode:         workflow.GroupGatherResultsAndFiles,
		Config: map[string]any{
			"form":               workflow.GroupFormBranches,
			"completionRule":     rule,
			"unfinishedBehavior": behavior,
			"gatherMode":         workflow.GroupGatherResultsAndFiles,
			"resolverProfile":    "resolver",
		},
		WorkspaceSnapshot: "snap-1",
		State:             instance.GroupRunDecided,
		ActionsApplied:    true,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	frozen := make([]instance.FrozenTask, 0, len(keys))
	for i, k := range keys {
		seq := int64(len(keys) - i)
		at := time.Now().UTC()
		child := uuid.New()
		run.Tasks = append(run.Tasks, instance.GroupTask{
			GroupRunID:      run.ID,
			Position:        i,
			Key:             k,
			Kind:            instance.GroupTaskKindBranch,
			Origin:          k + "-entry",
			ChildInstanceID: child,
			LaunchState:     instance.GroupTaskLaunched,
			LiveStatus:      instance.GroupTaskStatusCompleted,
			TerminalSeq:     &seq,
			TerminalAt:      &at,
			Result:          map[string]any{"k": k},
			FinalizedRef:    taskRef(k),
		})
		frozen = append(frozen, instance.FrozenTask{
			Key:             k,
			Kind:            instance.GroupTaskKindBranch,
			Origin:          k + "-entry",
			ChildInstanceID: child.String(),
			Status:          instance.GroupTaskStatusCompleted,
			Result:          map[string]any{"k": k},
			Winner:          k == winner,
		})
		area.refs[taskRef(k)] = map[string]string{k + ".txt": "from-" + k}
	}
	run.Decision = &instance.GroupDecision{
		Outcome:   outcome,
		Rule:      rule,
		Behavior:  behavior,
		WinnerKey: winner,
		Reason:    "decided for test",
		DecidedAt: time.Now().UTC(),
		Tasks:     frozen,
	}
	groups.put(run)
	area.parents[gatherParentDir] = map[string]string{"base.txt": "base"}

	parent := &instance.Instance{
		ID:           parentID,
		OwnerID:      uuid.New(),
		WorkflowName: "wf",
		State:        instance.StateRunning,
		Variables:    map[string]any{"workspace": gatherParentDir},
	}
	return &gatherFixture{
		t: t, groups: groups, area: area, starter: starter, bus: bus,
		gatherer: engine.NewGroupGatherer(groups, area, starter, bus),
		runID:    run.ID, parent: parent,
	}
}

func (f *gatherFixture) gather() (*engine.GatherStep, error) {
	f.t.Helper()
	return f.gatherer.Gather(context.Background(), f.groups.snapshot(f.runID), f.parent)
}

func (f *gatherFixture) report() *instance.GatherReport {
	f.t.Helper()
	run := f.groups.snapshot(f.runID)
	require.NotNil(f.t, run.Gather, "the gather report must be persisted")
	return run.Gather
}

// endOpenAttempts stands in for the completer (P3-T8): it records that the
// open resolution attempt ended, with outcome ("" = an ordinary end that the
// gatherer must verify).
func (f *gatherFixture) endOpenAttempts(outcome string) {
	f.t.Helper()
	run := f.groups.snapshot(f.runID)
	require.NotNil(f.t, run.Gather)
	rep := *run.Gather
	now := time.Now().UTC()
	for i := range rep.Attempts {
		if rep.Attempts[i].EndedAt == nil {
			rep.Attempts[i].EndedAt = &now
			rep.Attempts[i].Outcome = outcome
		}
	}
	require.NoError(f.t, f.groups.SetGatherReport(context.Background(), f.runID, rep))
}

// conflictOnB scripts tasks a and b both editing shared.txt, with b's merge
// conflicting (a is merged first, cleanly, in author order).
func (f *gatherFixture) conflictOnB() {
	f.area.parents[gatherParentDir]["shared.txt"] = "base"
	f.area.refs[taskRef("a")]["shared.txt"] = "from-a"
	f.area.refs[taskRef("b")]["shared.txt"] = "from-b"
	f.area.conflicts[taskRef("b")] = []string{"shared.txt"}
}

func gatherSection(t *testing.T, step *engine.GatherStep) map[string]any {
	t.Helper()
	require.NotNil(t, step)
	g, ok := step.Output["gather"].(map[string]any)
	require.True(t, ok, "output.gather must be the gather report map, got %T", step.Output["gather"])
	return g
}

func taskReportByKey(t *testing.T, r *instance.GatherReport, key string) instance.GatherTaskReport {
	t.Helper()
	for _, tr := range r.Tasks {
		if tr.Key == key {
			return tr
		}
	}
	t.Fatalf("no task report for %q", key)
	return instance.GatherTaskReport{}
}

// --- Happy path -------------------------------------------------------------

func TestFileGatherer_AllSucceed_MergesEveryTaskInAuthorOrderAndApplies(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)

	assert.Equal(t, []string{taskRef("a"), taskRef("b"), taskRef("c")}, f.area.mergeOrder(),
		"merges follow AUTHOR order even though tasks completed c, b, a")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b", "c.txt": "from-c"},
		f.area.parentFiles(gatherParentDir), "SINK: the parent's working area holds every task's change")
	assert.Equal(t, 1, f.area.applyCount())
	assert.Zero(t, f.area.rollbackCount())

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortSuccess, run.ContinuationPort)
	assert.True(t, run.ContinuationReady)
	rep := f.report()
	assert.Equal(t, "applied", rep.State)
	assert.Equal(t, workflow.GroupGatherResultsAndFiles, rep.Mode)
	assert.Equal(t, 3, rep.MergeIndex)
	require.Len(t, rep.Tasks, 3)
	for i, key := range []string{"a", "b", "c"} {
		assert.Equal(t, key, rep.Tasks[i].Key, "report tasks are in author order")
		assert.True(t, rep.Tasks[i].Merged)
		assert.Equal(t, "merged cleanly", rep.Tasks[i].Reason)
	}

	g := gatherSection(t, step)
	assert.Equal(t, "applied", g["state"])
	assert.Equal(t, workflow.GroupGatherResultsAndFiles, g["mode"])
	assert.Equal(t, f.runID.String(), fmt.Sprint(step.Output["groupRunId"]), "the rest of the output is BuildGroupOutput")

	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupGathering), 1)
	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupCompleted), 1)
	assert.Empty(t, f.starter.calls(), "a clean gather opens no resolution session")
}

func TestFileGatherer_FirstSuccess_MergesOnlyTheWinner(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleFirstSuccess, workflow.GroupPortSuccess, "b", "a", "b", "c")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)

	assert.Equal(t, []string{taskRef("b")}, f.area.mergeOrder(), "first_success merges the winner only")
	assert.Equal(t, map[string]string{"base.txt": "base", "b.txt": "from-b"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.True(t, taskReportByKey(t, rep, "b").Merged)
	for _, loser := range []string{"a", "c"} {
		tr := taskReportByKey(t, rep, loser)
		assert.False(t, tr.Merged)
		assert.Contains(t, tr.Reason, "not the winner")
	}
}

// --- Failure ----------------------------------------------------------------

func TestFileGatherer_FailedDecision_MergesNothingAndRoutesFailure(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortFailure, "", "a", "b")
	decisionBefore := f.groups.snapshot(f.runID).Decision

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)

	assert.Empty(t, f.area.begins(), "a failed group never stages a gather")
	assert.Empty(t, f.area.mergeOrder())
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, "not_applicable", rep.State)
	for _, tr := range rep.Tasks {
		assert.False(t, tr.Merged)
		assert.Equal(t, "group failed: no tasks merged", tr.Reason)
	}
	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunDone, run.State)
	assert.Equal(t, workflow.GroupPortFailure, run.ContinuationPort)
	assert.Equal(t, decisionBefore, run.Decision, "task-decision history is never rewritten")
	assert.Equal(t, "not_applicable", gatherSection(t, step)["state"])
}

func TestFileGatherer_MissingFinalizedRef_RoutesFailureWithReason(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.groups.update(f.runID, func(r *instance.GroupRun) { r.Tasks[1].FinalizedRef = "" })

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Empty(t, f.area.begins(), "nothing is staged when an eligible task has no finalized ref")

	rep := f.report()
	assert.Equal(t, "failed", rep.State)
	assert.Contains(t, rep.Error, "task b has no finalized working-area ref")
	assert.Contains(t, taskReportByKey(t, rep, "a").Reason, "not merged")
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))
}

func TestFileGatherer_ParentWithoutWorkspace_RoutesFailure(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.parent.Variables = map[string]any{}

	step, err := f.gather()
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Empty(t, f.area.begins())
	assert.Contains(t, f.report().Error, "no working area")
}

func TestFileGatherer_MergeError_RollsBackAndRoutesFailure(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	f.area.failMerge[taskRef("b")] = errors.New("index.lock exists")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Zero(t, f.area.applyCount())
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, "rolled_back", rep.State)
	assert.Equal(t, "rolled_back", rep.RollbackStatus)
	assert.Contains(t, rep.Error, "merge task b: index.lock exists")
	assert.Equal(t, false, taskReportByKey(t, rep, "a").Merged)
	assert.Contains(t, taskReportByKey(t, rep, "a").Reason, "rolled back:")
	assert.Contains(t, taskReportByKey(t, rep, "c").Reason, "not merged:")
}

func TestFileGatherer_ApplyError_RollsBackPartialApply(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failApplyAfterWrite = errors.New("disk full")

	step, err := f.gather()
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.applyCount())
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir),
		"SINK: a half-written apply is restored to the exact pre-gather content")
	assert.Contains(t, f.report().Error, "apply gathered changes to the parent working area: disk full")
}

// --- Conflicts / resolution sessions ---------------------------------------

func TestFileGatherer_Conflict_SuspendsInResolvingConflictsWithSession(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	f.conflictOnB()

	step, err := f.gather()
	require.NoError(t, err)
	require.False(t, step.Done, "a conflict suspends the gather")

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunResolvingConflicts, run.State)
	assert.Nil(t, run.FinalOutput)
	rep := f.report()
	assert.Equal(t, "resolving", rep.State)
	assert.Equal(t, 1, rep.MergeIndex, "a is merged; b is the conflicting merge")
	require.Len(t, rep.Attempts, 1)
	att := rep.Attempts[0]
	assert.Equal(t, 1, att.MergeIndex)
	assert.Equal(t, 1, att.Attempt)
	assert.Equal(t, "b", att.TaskKey)
	assert.Equal(t, []string{"shared.txt"}, att.ConflictPaths)
	assert.Nil(t, att.EndedAt)
	require.NotNil(t, rep.ActiveSession)
	assert.Equal(t, instance.ResolutionSessionRef{GroupRunID: f.runID, MergeIndex: 1, Attempt: 1, InstanceID: f.parent.ID, NodeID: "g1"}, *rep.ActiveSession)
	assert.Equal(t, []string{"shared.txt"}, taskReportByKey(t, rep, "b").ConflictPaths)

	calls := f.starter.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, f.runID, calls[0].runID)
	assert.Equal(t, f.parent.ID, calls[0].parentID)
	assert.Equal(t, "b", calls[0].req.TaskKey)
	assert.Equal(t, taskRef("b"), calls[0].req.TaskRef)
	assert.Equal(t, []string{"shared.txt"}, calls[0].req.ConflictPaths)
	begin := f.area.begins()[0]
	assert.Equal(t, "/staging/"+begin.gatherID.String(), calls[0].req.Handle.StagingDir, "the session works in the STAGING dir")
	assert.Equal(t, gatherParentDir, calls[0].req.Handle.ParentDir)

	assert.Zero(t, f.area.applyCount(), "nothing reaches the parent while conflicts are open")
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir))
}

func TestFileGatherer_ReEntryWhileSessionOpen_StaysWaiting(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)
	before := f.report()

	step, err := f.gather()
	require.NoError(t, err)
	assert.False(t, step.Done)
	assert.Len(t, f.starter.calls(), 1, "an open session is never reopened")
	assert.Equal(t, before, f.report())
}

func TestFileGatherer_ReEntryStillConflicted_OpensFreshAttempt(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)
	f.endOpenAttempts("") // the operator ended the session without fixing shared.txt

	step, err := f.gather()
	require.NoError(t, err)
	assert.False(t, step.Done, "still-conflicted completion keeps the parent blocked")
	assert.Equal(t, instance.GroupRunResolvingConflicts, f.groups.snapshot(f.runID).State)

	rep := f.report()
	require.Len(t, rep.Attempts, 2)
	assert.Equal(t, engine.ResolutionOutcomeStillConflicted, rep.Attempts[0].Outcome)
	assert.Equal(t, 2, rep.Attempts[1].Attempt)
	assert.Equal(t, 1, rep.Attempts[1].MergeIndex)
	assert.Nil(t, rep.Attempts[1].EndedAt)
	require.NotNil(t, rep.ActiveSession)
	assert.Equal(t, 2, rep.ActiveSession.Attempt)
	calls := f.starter.calls()
	require.Len(t, calls, 2)
	assert.Equal(t, 2, calls[1].req.Attempt)
	assert.Zero(t, f.area.applyCount())
}

func TestFileGatherer_ReEntryResolved_CommitsAndContinuesToApply(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)
	f.area.resolveStaged("shared.txt", "merged-a-and-b")
	f.endOpenAttempts("")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, []string{taskRef("a"), taskRef("b"), taskRef("c")}, f.area.mergeOrder(),
		"after the resolution the gather continues with the NEXT task")
	assert.Equal(t, map[string]string{
		"base.txt": "base", "shared.txt": "merged-a-and-b",
		"a.txt": "from-a", "b.txt": "from-b", "c.txt": "from-c",
	}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, engine.ResolutionOutcomeResolved, rep.Attempts[0].Outcome)
	assert.Equal(t, "merged after conflict resolution (attempt 1)", taskReportByKey(t, rep, "b").Reason)
	assert.Nil(t, rep.ActiveSession)
	assert.Len(t, f.bus.eventsOfType(f.parent.ID, engine.EventTypeGroupResolved), 1)
}

func TestFileGatherer_Abandoned_RollsBackAndRoutesFailureKeepingDecision(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	decisionBefore := f.groups.snapshot(f.runID).Decision
	_, err := f.gather()
	require.NoError(t, err)
	f.endOpenAttempts(engine.ResolutionOutcomeAbandoned)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	assert.Equal(t, map[string]string{"base.txt": "base", "shared.txt": "base"}, f.area.parentFiles(gatherParentDir))

	rep := f.report()
	assert.Equal(t, "rolled_back", rep.State)
	assert.Equal(t, "rolled_back", rep.RollbackStatus)
	assert.Equal(t, engine.ResolutionOutcomeAbandoned, rep.Attempts[0].Outcome)
	assert.Contains(t, rep.Error, "conflict resolution for task b was abandoned")
	a := taskReportByKey(t, rep, "a")
	assert.False(t, a.Merged)
	assert.True(t, strings.HasPrefix(a.Reason, "rolled back:"))

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, decisionBefore, run.Decision)
	assert.Equal(t, workflow.GroupPortSuccess, fmt.Sprint(step.Output["outcome"]),
		"the frozen decision still says success; only the ROUTE is failure")
}

func TestFileGatherer_SessionStartError_RollsBackAndRecordsErrorAttempt(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	f.starter.err = errors.New("resolver profile offline")

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	rep := f.report()
	require.Len(t, rep.Attempts, 1)
	assert.Equal(t, engine.ResolutionOutcomeError, rep.Attempts[0].Outcome)
	assert.NotNil(t, rep.Attempts[0].EndedAt)
	assert.Contains(t, rep.Error, "start conflict resolution for task b: resolver profile offline")
}

func TestFileGatherer_NilBridge_ConflictRollsBack(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	g := engine.NewGroupGatherer(f.groups, f.area, nil, f.bus)

	step, err := g.Gather(context.Background(), f.groups.snapshot(f.runID), f.parent)
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Contains(t, f.report().Error, "no conflict-resolution bridge is configured")
}

// --- Error: rollback failure blocks continuation ---------------------------

func TestFileGatherer_RollbackFailure_ReportsAndBlocksContinuationUntilRepaired(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.area.failMerge[taskRef("b")] = errors.New("merge exploded")
	f.area.failRollback = errors.New("permission denied")

	step, err := f.gather()
	require.Error(t, err)
	assert.Nil(t, step)
	assert.ErrorIs(t, err, engine.ErrGroupGatherRollbackFailed)

	run := f.groups.snapshot(f.runID)
	assert.Equal(t, instance.GroupRunGathering, run.State, "no continuation while the parent's area is unrestored")
	assert.Nil(t, run.FinalOutput)
	rep := f.report()
	assert.Equal(t, "rollback_failed", rep.RollbackStatus)
	assert.Equal(t, "failed", rep.State)
	assert.Contains(t, rep.Error, "rollback failed: permission denied")

	// Repaired: the next re-entry retries the rollback and only then routes failure.
	f.area.failRollback = nil
	step, err = f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 2, f.area.rollbackCount())
	rep = f.report()
	assert.Equal(t, "rolled_back", rep.RollbackStatus)
	assert.Equal(t, "merge task b: merge exploded", rep.Error, "the original abort reason survives the retry")
}

// --- Edge: crash boundaries -------------------------------------------------

func TestFileGatherer_ResumeAfterCrashMidMerge_DoesNotDoubleApply(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b", "c")
	// SetGatherReport calls: #1 merging, #2 handle, #3 after a, #4 after b.
	// Failing #4 models a crash after b entered the staged tree but before its
	// progress was persisted.
	f.groups.failCall("SetGatherReport", 4, errors.New("database is locked"))

	_, err := f.gather()
	require.Error(t, err)
	assert.Equal(t, 1, f.report().MergeIndex, "persisted progress lags the staged tree by one merge")
	assert.Equal(t, instance.GroupRunGathering, f.groups.snapshot(f.runID).State)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Len(t, f.area.begins(), 1, "resume reuses the persisted handle")
	assert.Equal(t, []string{taskRef("a"), taskRef("b"), taskRef("b"), taskRef("c")}, f.area.mergeOrder())
	assert.Equal(t, 1, f.area.timesApplied(taskRef("b")), "b's delta entered the staged tree exactly once")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b", "c.txt": "from-c"},
		f.area.parentFiles(gatherParentDir))
	assert.Equal(t, 1, f.area.applyCount())
}

func TestFileGatherer_ResumeAfterCrashBeforeHandlePersisted_StagesAfresh(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.groups.failCall("SetGatherReport", 2, errors.New("database is locked"))

	_, err := f.gather()
	require.Error(t, err)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Len(t, f.area.begins(), 2, "with no persisted handle the gather starts a fresh staging area")
	assert.Equal(t, map[string]string{"base.txt": "base", "a.txt": "from-a", "b.txt": "from-b"}, f.area.parentFiles(gatherParentDir))
}

func TestFileGatherer_ResumeAfterApplyBeforeFinalOutput_FinishesWithoutReapplying(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.groups.failCall("SetFinalOutput", 1, errors.New("database is locked"))

	_, err := f.gather()
	require.Error(t, err)
	assert.Equal(t, "applied", f.report().State)

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, 1, f.area.applyCount(), "an applied report finishes without another Apply")
}

func TestFileGatherer_DoneRunReplaysStoredOutput(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.groups.update(f.runID, func(r *instance.GroupRun) {
		r.State = instance.GroupRunDone
		r.ContinuationPort = workflow.GroupPortSuccess
		r.FinalOutput = map[string]any{"replayed": true}
	})

	step, err := f.gather()
	require.NoError(t, err)
	assert.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortSuccess, step.Port)
	assert.Equal(t, true, step.Output["replayed"])
	assert.Empty(t, f.area.begins())
}

func TestFileGatherer_RejectsResultsModeAndUndecidedRuns(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")

	results := f.groups.snapshot(f.runID)
	results.GatherMode = workflow.GroupGatherResults
	_, err := f.gatherer.Gather(context.Background(), results, f.parent)
	assert.ErrorContains(t, err, "only \"results_and_files\" gathers files")

	running := f.groups.snapshot(f.runID)
	running.State = instance.GroupRunRunning
	running.Decision = nil
	_, err = f.gatherer.Gather(context.Background(), running, f.parent)
	assert.ErrorContains(t, err, "has no decision")

	_, err = f.gatherer.Gather(context.Background(), nil, f.parent)
	assert.Error(t, err)
}

// --- AbortGather (reconciler entry point) ----------------------------------

func TestFileGatherer_AbortGather_CancelledParentMidSessionRollsBackAndSettles(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a", "b")
	f.conflictOnB()
	_, err := f.gather()
	require.NoError(t, err)

	step, err := f.gatherer.AbortGather(context.Background(), f.groups.snapshot(f.runID),
		engine.ResolutionOutcomeCancelled, "parent instance was cancelled")
	require.NoError(t, err)
	require.True(t, step.Done)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Equal(t, 1, f.area.rollbackCount())
	rep := f.report()
	assert.Equal(t, engine.ResolutionOutcomeCancelled, rep.Attempts[0].Outcome)
	assert.NotNil(t, rep.Attempts[0].EndedAt)
	assert.Nil(t, rep.ActiveSession)
	assert.Equal(t, "parent instance was cancelled", rep.Error)
	assert.Equal(t, instance.GroupRunDone, f.groups.snapshot(f.runID).State)
}

func TestFileGatherer_AbortGather_FromDecidedStagesNothing(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")

	step, err := f.gatherer.AbortGather(context.Background(), f.groups.snapshot(f.runID),
		engine.ResolutionOutcomeCancelled, "parent instance was cancelled")
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupPortFailure, step.Port)
	assert.Empty(t, f.area.begins())
	assert.Zero(t, f.area.rollbackCount())
	assert.Equal(t, "failed", f.report().State)
}

// --- Provenance -------------------------------------------------------------

func TestFileGatherer_Provenance_ParentDirComesFromParentWorkspaceVariable(t *testing.T) {
	f := newGatherFixture(t, workflow.GroupRuleAllSucceed, workflow.GroupPortSuccess, "", "a")
	f.parent.Variables["workspace"] = "/ws/other-parent"
	f.area.parents["/ws/other-parent"] = map[string]string{"x.txt": "x"}

	step, err := f.gather()
	require.NoError(t, err)
	require.True(t, step.Done)
	require.Len(t, f.area.begins(), 1)
	assert.Equal(t, "/ws/other-parent", f.area.begins()[0].parentDir)
	assert.Equal(t, "snap-1", f.area.begins()[0].snapshot, "the run's persisted snapshot handle is the baseline")
	assert.Equal(t, map[string]string{"x.txt": "x", "a.txt": "from-a"}, f.area.parentFiles("/ws/other-parent"))
	assert.Equal(t, map[string]string{"base.txt": "base"}, f.area.parentFiles(gatherParentDir), "no other directory is touched")
}

func TestGatherHandleFromReport_RoundTrip(t *testing.T) {
	id := uuid.New()
	rep := &instance.GatherReport{Handle: map[string]any{
		"snapshotHandle": "snap-1", "gatherId": id.String(), "stagingDir": "/staging/x",
		"rollbackRef": "refs/r", "parentDir": "/ws/p",
	}}
	h, ok := engine.GatherHandleFromReport(rep)
	require.True(t, ok)
	assert.Equal(t, engine.GatherHandle{SnapshotHandle: "snap-1", GatherID: id, StagingDir: "/staging/x", RollbackRef: "refs/r", ParentDir: "/ws/p"}, h)

	_, ok = engine.GatherHandleFromReport(&instance.GatherReport{})
	assert.False(t, ok)
	_, ok = engine.GatherHandleFromReport(nil)
	assert.False(t, ok)
}
```

- [x] Step 3: Run the tests and confirm they fail.

Confirmed the missing ResolutionRequest / GroupResolutionStarter / FileGatherer compile failure. Added automatic-resolution, full-ref notification/closure, restart/persistence-boundary and downstream failure-sink tests before implementation; updated the stale abandon assertion to the binding final-outcome contract.

```bash
go test ./domain/engine/ -run 'TestFileGatherer|TestGatherHandleFromReport' -count=1
```
Expected (compile failure):
```
# github.com/arbitron/arbitron/domain/engine_test [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/group_gather_fakes_test.go:...: undefined: engine.ResolutionRequest
domain/engine/group_gather_fakes_test.go:...: undefined: engine.GroupResolutionStarter
domain/engine/group_gatherer_test.go:...: undefined: engine.FileGatherer
domain/engine/group_gatherer_test.go:...: undefined: engine.NewGroupGatherer
...
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [x] Step 4: Implement. Create `domain/engine/group_gatherer.go`:

Implemented the planned state machine and binding automatic-resolution/final-route revisions. Verification and session close/notification errors block continuation and retry from the persisted full session identity; replay re-verifies staging without launching a second agent. Abort intent is persisted before rollback so a lost rollback-result write cannot turn failure into success. Resolving-state abort recovery preserves the original reason. Replay output is deep-copied and incomplete handles are rejected by the decoder.

```go
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// Gather report vocabulary. instance.GatherReport (P3-T4) owns these strings
// (and GatherReport.Validate checks them); the short local names alias them so
// the state machine and its persisted vocabulary cannot drift.
const (
	gatherStatePending       = instance.GatherStatePending
	gatherStateMerging       = instance.GatherStateMerging
	gatherStateResolving     = instance.GatherStateResolving
	gatherStateApplied       = instance.GatherStateApplied
	gatherStateFailed        = instance.GatherStateFailed
	gatherStateRolledBack    = instance.GatherStateRolledBack
	gatherStateNotApplicable = instance.GatherStateNotApplicable

	rollbackStatusRolledBack = instance.RollbackStatusRolledBack
	rollbackStatusFailed     = instance.RollbackStatusFailed

	// rollbackFailedSep joins the original abort reason and the rollback error
	// in GatherReport.Error, so a retried rollback recovers the reason.
	rollbackFailedSep = "; rollback failed: "
)

// Resolution attempt outcomes (instance.ResolutionAttempt.Outcome), re-exported
// from domain/instance (P3-T4) for engine callers (P3-T8, P3-T9, P3-T10).
const (
	ResolutionOutcomeResolved        = instance.ResolutionOutcomeResolved
	ResolutionOutcomeStillConflicted = instance.ResolutionOutcomeStillConflicted
	ResolutionOutcomeAbandoned       = instance.ResolutionOutcomeAbandoned
	ResolutionOutcomeCancelled       = instance.ResolutionOutcomeCancelled
	ResolutionOutcomeError           = instance.ResolutionOutcomeError
)

// ErrGroupGatherRollbackFailed means a gather was aborted and the parent's
// working area could NOT be restored. The run stays in `gathering` with
// GatherReport.RollbackStatus "rollback_failed": continuation is blocked, and
// the next re-entry (e.g. an operator Retry after repairing the area) retries
// the rollback before routing failure.
var ErrGroupGatherRollbackFailed = errors.New("parallel_group gather could not restore the parent's working area")

// ResolutionRequest is everything a resolution session needs about ONE
// conflicted merge: the staged gather (the agent works in Handle.StagingDir,
// never in a task's directory), the merge being resolved and its paths.
type ResolutionRequest struct {
	Handle        GatherHandle
	MergeIndex    int
	Attempt       int
	TaskKey       string
	TaskRef       string
	ConflictPaths []string
}

// GroupResolutionStarter opens a conflict-resolution session for a merge and
// returns its durable identity. Declared here by its consumer; P3-T8's
// GroupResolutionBridge implements it.
type GroupResolutionStarter interface {
	StartResolution(ctx context.Context, run *instance.GroupRun, parent *instance.Instance, req ResolutionRequest) (*instance.ResolutionSessionRef, error)
	CloseSession(ctx context.Context, ref instance.ResolutionSessionRef, reason string) error
	NotifyResolutionRequired(ctx context.Context, ref instance.ResolutionSessionRef) error
}

// FileGatherer implements GroupGatherer for gatherMode results_and_files.
//
// It is the single writer of the parent's working area for a group: it runs
// only inside the parent's executor (and, for a cancelled parent, from the
// reconciler via AbortGather), merges ONLY the eligible tasks' finalized refs
// — every task in author order for an all_succeed success, the winner alone
// for a first_success success, nothing for a failed decision — and persists a
// GatherReport after every step, so a crash at any boundary resumes from the
// persisted merge index without applying a delta twice.
type FileGatherer struct {
	groups instance.GroupRunRepository
	area   WorkingArea
	bridge GroupResolutionStarter
	bus    EventBus
	now    func() time.Time
}

var _ GroupGatherer = (*FileGatherer)(nil)

// NewGroupGatherer constructs the file gatherer. bridge may be nil only in
// configurations that never meet a conflict: a conflict with no bridge aborts
// the gather (rollback + failure route) rather than hanging the parent.
func NewGroupGatherer(groups instance.GroupRunRepository, area WorkingArea, bridge GroupResolutionStarter, bus EventBus) *FileGatherer {
	return &FileGatherer{groups: groups, area: area, bridge: bridge, bus: bus, now: time.Now}
}

// gatherPass is one invocation's working copy of the run and its report.
type gatherPass struct {
	run    instance.GroupRun
	parent *instance.Instance
	report instance.GatherReport
	handle *GatherHandle
}

func (p *gatherPass) loadReport() {
	if p.run.Gather != nil {
		p.report = copyGatherReport(*p.run.Gather)
	} else {
		p.report = newGatherReport(&p.run)
	}
	p.handle = nil
	if h, ok := GatherHandleFromReport(&p.report); ok {
		p.handle = &h
	}
}

// Gather advances the run's file gather as far as it can and reports whether
// the group is done (Port/Output set) or must wait for a resolution session.
func (g *FileGatherer) Gather(ctx context.Context, run *instance.GroupRun, parent *instance.Instance) (*GatherStep, error) {
	if run == nil || parent == nil {
		return nil, errors.New("group gather: run and parent instance are required")
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles {
		return nil, fmt.Errorf("group gather: run %s gathers %q; only %q gathers files",
			run.ID, run.GatherMode, workflow.GroupGatherResultsAndFiles)
	}
	if run.State == instance.GroupRunDone {
		return replayGatherStep(run), nil
	}
	if run.Decision == nil {
		return nil, fmt.Errorf("group gather: run %s has no decision (state %s)", run.ID, run.State)
	}
	p := &gatherPass{run: *run, parent: parent}
	switch run.State {
	case instance.GroupRunDecided:
		ok, err := g.groups.TransitionState(ctx, run.ID, instance.GroupRunDecided, instance.GroupRunGathering)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter gathering: %w", run.ID, err)
		}
		if !ok {
			return g.reload(ctx, run.ID)
		}
		p.run.State = instance.GroupRunGathering
		p.report = newGatherReport(&p.run)
		return g.start(ctx, p)
	case instance.GroupRunGathering:
		p.loadReport()
		return g.resume(ctx, p)
	case instance.GroupRunResolvingConflicts:
		if run.Gather == nil {
			return nil, fmt.Errorf("group gather: run %s is resolving conflicts without a gather report", run.ID)
		}
		p.loadReport()
		return g.reenterResolution(ctx, p)
	default:
		return nil, fmt.Errorf("group gather: run %s is %s; it has not been decided yet", run.ID, run.State)
	}
}

// AbortGather aborts a gather from OUTSIDE the parent's run — the reconciler
// for a cancelled parent, or a lost staging area. Any open resolution attempt
// is closed with outcome, everything staged is rolled back, and the run
// settles on the failure route with reason. A run that is already done
// replays its stored result; a decided run settles without staging anything.
func (g *FileGatherer) AbortGather(ctx context.Context, run *instance.GroupRun, outcome, reason string) (*GatherStep, error) {
	if run == nil {
		return nil, errors.New("group gather: run is required")
	}
	if run.State == instance.GroupRunDone {
		return replayGatherStep(run), nil
	}
	if run.GatherMode != workflow.GroupGatherResultsAndFiles {
		return nil, fmt.Errorf("group gather: run %s gathers %q; there is no file gather to abort", run.ID, run.GatherMode)
	}
	if run.Decision == nil {
		return nil, fmt.Errorf("group gather: run %s has no decision (state %s)", run.ID, run.State)
	}
	p := &gatherPass{run: *run}
	switch run.State {
	case instance.GroupRunDecided:
		ok, err := g.groups.TransitionState(ctx, run.ID, instance.GroupRunDecided, instance.GroupRunGathering)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter gathering: %w", run.ID, err)
		}
		if !ok {
			return g.reload(ctx, run.ID)
		}
		p.run.State = instance.GroupRunGathering
		p.report = newGatherReport(&p.run)
	case instance.GroupRunGathering, instance.GroupRunResolvingConflicts:
		p.loadReport()
	default:
		return nil, fmt.Errorf("group gather: run %s is %s; there is no gather to abort", run.ID, run.State)
	}
	return g.abort(ctx, p, reason, outcome)
}

// reload handles a lost state CAS. The executor is the single writer of a
// gathering run, so the only legitimate concurrent move is to done (replay).
func (g *FileGatherer) reload(ctx context.Context, id uuid.UUID) (*GatherStep, error) {
	fresh, err := g.groups.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("group gather: reload run %s: %w", id, err)
	}
	if fresh.State != instance.GroupRunDone {
		return nil, fmt.Errorf("group gather: run %s changed state concurrently (now %s)", id, fresh.State)
	}
	return replayGatherStep(fresh), nil
}

// start decides what is eligible and opens the staged gather.
func (g *FileGatherer) start(ctx context.Context, p *gatherPass) (*GatherStep, error) {
	run := &p.run
	if run.Decision.Outcome != workflow.GroupPortSuccess {
		for i := range p.report.Tasks {
			p.report.Tasks[i].Merged = false
			p.report.Tasks[i].Reason = "group failed: no tasks merged"
		}
		p.report.State = gatherStateNotApplicable
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
		return g.finish(ctx, p, workflow.GroupPortFailure)
	}

	eligible, problem := eligibleGatherTasks(run)
	markIneligible(p, eligible)
	if problem != "" {
		return g.abort(ctx, p, problem, "")
	}
	parentDir := parentWorkspace(p.parent)
	if parentDir == "" {
		return g.abort(ctx, p, "parent instance has no working area to gather into", "")
	}
	if strings.TrimSpace(run.WorkspaceSnapshot) == "" {
		return g.abort(ctx, p, "group run has no working-area snapshot to compare task changes against", "")
	}

	p.report.State = gatherStateMerging
	p.report.Error = ""
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	g.publish(run, EventTypeGroupGathering, map[string]any{"state": gatherStateMerging, "eligible": len(eligible)})

	h, err := g.area.BeginGather(ctx, run.WorkspaceSnapshot, uuid.New(), parentDir)
	if err != nil {
		return g.abort(ctx, p, fmt.Sprintf("begin gather: %v", err), "")
	}
	p.handle = &h
	p.report.Handle = gatherHandleMap(h)
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	return g.mergeLoop(ctx, p, eligible)
}

// resume continues a run left in `gathering` by a crash or an earlier step.
func (g *FileGatherer) resume(ctx context.Context, p *gatherPass) (*GatherStep, error) {
	switch {
	case p.report.RollbackStatus == rollbackStatusFailed:
		reason, _, _ := strings.Cut(p.report.Error, rollbackFailedSep)
		return g.abort(ctx, p, reason, "")
	case p.report.State == gatherStateApplied:
		return g.finish(ctx, p, workflow.GroupPortSuccess)
	case p.report.State == gatherStateNotApplicable,
		p.report.State == gatherStateFailed,
		p.report.State == gatherStateRolledBack:
		return g.finish(ctx, p, workflow.GroupPortFailure)
	case p.report.State == gatherStateResolving:
		// Crash between recording a conflict and entering resolving_conflicts.
		ok, err := g.groups.TransitionState(ctx, p.run.ID, instance.GroupRunGathering, instance.GroupRunResolvingConflicts)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter resolving_conflicts: %w", p.run.ID, err)
		}
		if !ok {
			return g.reload(ctx, p.run.ID)
		}
		p.run.State = instance.GroupRunResolvingConflicts
		return g.reenterResolution(ctx, p)
	case p.handle == nil:
		// Nothing was staged durably yet: stage afresh. The parent's area is
		// untouched before Apply, so a fresh BeginGather is always safe.
		return g.start(ctx, p)
	default:
		eligible, problem := eligibleGatherTasks(&p.run)
		if problem != "" {
			return g.abort(ctx, p, problem, "")
		}
		return g.mergeLoop(ctx, p, eligible)
	}
}

// mergeLoop merges eligible tasks from the persisted merge index onward.
func (g *FileGatherer) mergeLoop(ctx context.Context, p *gatherPass, eligible []instance.GroupTask) (*GatherStep, error) {
	for p.report.MergeIndex < len(eligible) {
		task := eligible[p.report.MergeIndex]
		outcome, err := g.area.MergeTask(ctx, *p.handle, task.FinalizedRef)
		if err != nil {
			return g.abort(ctx, p, fmt.Sprintf("merge task %s: %v", task.Key, err), "")
		}
		if !outcome.Clean {
			paths := outcome.ConflictPaths
			if len(paths) == 0 {
				if paths, err = g.area.ConflictPaths(ctx, *p.handle); err != nil {
					return g.abort(ctx, p, fmt.Sprintf("list conflicts of task %s: %v", task.Key, err), "")
				}
			}
			if len(paths) == 0 {
				return g.abort(ctx, p, fmt.Sprintf("merge of task %s reported a conflict but no conflicted paths", task.Key), "")
			}
			return g.openResolution(ctx, p, task, paths)
		}
		if ti := taskReportIndex(&p.report, task.Key); ti >= 0 {
			p.report.Tasks[ti].Merged = true
			p.report.Tasks[ti].Reason = "merged cleanly"
			p.report.Tasks[ti].ConflictPaths = nil
		}
		p.report.MergeIndex++
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
	}
	if err := g.area.Apply(ctx, *p.handle); err != nil {
		return g.abort(ctx, p, fmt.Sprintf("apply gathered changes to the parent working area: %v", err), "")
	}
	p.report.State = gatherStateApplied
	p.report.Error = ""
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	return g.finish(ctx, p, workflow.GroupPortSuccess)
}

// openResolution records a new attempt for the current merge, moves the run
// to resolving_conflicts and starts the session.
func (g *FileGatherer) openResolution(ctx context.Context, p *gatherPass, task instance.GroupTask, paths []string) (*GatherStep, error) {
	attemptNo := nextAttemptNumber(&p.report, p.report.MergeIndex)
	if ti := taskReportIndex(&p.report, task.Key); ti >= 0 {
		p.report.Tasks[ti].ConflictPaths = append([]string(nil), paths...)
	}
	p.report.State = gatherStateResolving
	p.report.ActiveSession = nil
	p.report.Attempts = append(p.report.Attempts, instance.ResolutionAttempt{
		MergeIndex:    p.report.MergeIndex,
		Attempt:       attemptNo,
		TaskKey:       task.Key,
		ConflictPaths: append([]string(nil), paths...),
		StartedAt:     g.now().UTC(),
	})
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	if p.run.State != instance.GroupRunResolvingConflicts {
		ok, err := g.groups.TransitionState(ctx, p.run.ID, instance.GroupRunGathering, instance.GroupRunResolvingConflicts)
		if err != nil {
			return nil, fmt.Errorf("group gather: run %s: enter resolving_conflicts: %w", p.run.ID, err)
		}
		if !ok {
			return nil, fmt.Errorf("group gather: run %s left gathering concurrently", p.run.ID)
		}
		p.run.State = instance.GroupRunResolvingConflicts
	}
	return g.startSession(ctx, p, task, len(p.report.Attempts)-1)
}

// startSession asks the bridge to open the session for attempt idx and
// persists its identity. A failure to open aborts the gather.
func (g *FileGatherer) startSession(ctx context.Context, p *gatherPass, task instance.GroupTask, idx int) (*GatherStep, error) {
	att := p.report.Attempts[idx]
	if g.bridge == nil {
		g.endAttempt(p, idx, ResolutionOutcomeError)
		return g.abort(ctx, p, fmt.Sprintf("task %s has conflicts but no conflict-resolution bridge is configured", task.Key), "")
	}
	ref, err := g.bridge.StartResolution(ctx, &p.run, p.parent, ResolutionRequest{
		Handle:        *p.handle,
		MergeIndex:    att.MergeIndex,
		Attempt:       att.Attempt,
		TaskKey:       task.Key,
		TaskRef:       task.FinalizedRef,
		ConflictPaths: append([]string(nil), att.ConflictPaths...),
	})
	if err != nil {
		g.endAttempt(p, idx, ResolutionOutcomeError)
		return g.abort(ctx, p, fmt.Sprintf("start conflict resolution for task %s: %v", task.Key, err), "")
	}
	if ref == nil {
		g.endAttempt(p, idx, ResolutionOutcomeError)
		return g.abort(ctx, p, fmt.Sprintf("start conflict resolution for task %s: no session was opened", task.Key), "")
	}
	active := *ref
	p.report.ActiveSession = &active
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	// The first agent turn may have fully resolved the merge. The actual
	// index/content is authoritative; a model claim or sentinel is not.
	paths, err := g.area.ConflictPaths(ctx, *p.handle)
	if err != nil {
		return g.abort(ctx, p, fmt.Sprintf("verify automatic resolution for task %s: %v", task.Key, err), ResolutionOutcomeError)
	}
	if len(paths) == 0 {
		if err := g.bridge.CloseSession(ctx, active, "completed"); err != nil {
			return nil, fmt.Errorf("close automatically resolved session: %w", err)
		}
		g.endAttempt(p, idx, ResolutionOutcomeResolved)
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
		return g.reenterResolution(ctx, p)
	}
	if err := g.bridge.NotifyResolutionRequired(ctx, active); err != nil {
		return nil, fmt.Errorf("notify unresolved gather: %w", err)
	}
	return &GatherStep{Done: false}, nil
}

func (g *FileGatherer) endAttempt(p *gatherPass, idx int, outcome string) {
	now := g.now().UTC()
	p.report.Attempts[idx].EndedAt = &now
	p.report.Attempts[idx].Outcome = outcome
}

// reenterResolution runs when the parent re-enters the group node while the
// run is resolving_conflicts: it verifies the latest attempt of the current
// merge and either keeps waiting, reopens a fresh attempt, aborts, or commits
// the resolution and continues merging.
func (g *FileGatherer) reenterResolution(ctx context.Context, p *gatherPass) (*GatherStep, error) {
	if p.handle == nil {
		return g.abort(ctx, p, "gather handle is missing while resolving conflicts; the staged merge cannot be verified", "")
	}
	eligible, problem := eligibleGatherTasks(&p.run)
	if problem != "" {
		return g.abort(ctx, p, problem, "")
	}
	if p.report.State == gatherStateMerging {
		// Crash after a resolved merge was recorded, before leaving resolving_conflicts.
		if err := g.enterGathering(ctx, p); err != nil {
			return nil, err
		}
		return g.mergeLoop(ctx, p, eligible)
	}
	if p.report.MergeIndex >= len(eligible) {
		return g.abort(ctx, p, fmt.Sprintf("merge index %d is past the %d eligible tasks", p.report.MergeIndex, len(eligible)), "")
	}
	task := eligible[p.report.MergeIndex]
	ti := taskReportIndex(&p.report, task.Key)
	idx := latestAttemptIndex(&p.report, p.report.MergeIndex)
	if idx < 0 {
		var paths []string
		if ti >= 0 {
			paths = p.report.Tasks[ti].ConflictPaths
		}
		if len(paths) == 0 {
			return g.abort(ctx, p, fmt.Sprintf("task %s is resolving conflicts but no conflicted paths were recorded", task.Key), "")
		}
		return g.openResolution(ctx, p, task, paths)
	}
	att := p.report.Attempts[idx]
	if att.EndedAt == nil {
		if p.report.ActiveSession == nil {
			// Crash between recording the attempt and opening its session.
			return g.startSession(ctx, p, task, idx)
		}
		return &GatherStep{Done: false}, nil
	}
	switch att.Outcome {
	case ResolutionOutcomeAbandoned:
		return g.abort(ctx, p, fmt.Sprintf("conflict resolution for task %s was abandoned (attempt %d)", task.Key, att.Attempt), "")
	case ResolutionOutcomeCancelled:
		return g.abort(ctx, p, fmt.Sprintf("conflict resolution for task %s was cancelled (attempt %d)", task.Key, att.Attempt), "")
	case ResolutionOutcomeError:
		return g.abort(ctx, p, fmt.Sprintf("conflict resolution for task %s failed (attempt %d)", task.Key, att.Attempt), "")
	case ResolutionOutcomeStillConflicted:
		// Crash after recording still_conflicted, before the fresh attempt.
		paths := att.ConflictPaths
		if ti >= 0 && len(p.report.Tasks[ti].ConflictPaths) > 0 {
			paths = p.report.Tasks[ti].ConflictPaths
		}
		return g.openResolution(ctx, p, task, paths)
	}

	// The session ended normally ("" or a crash after "resolved"): verify.
	paths, err := g.area.ConflictPaths(ctx, *p.handle)
	if err != nil {
		p.report.Attempts[idx].Outcome = ResolutionOutcomeError
		return g.abort(ctx, p, fmt.Sprintf("verify conflict resolution for task %s: %v", task.Key, err), "")
	}
	if len(paths) > 0 {
		p.report.Attempts[idx].Outcome = ResolutionOutcomeStillConflicted
		p.report.ActiveSession = nil
		if ti >= 0 {
			p.report.Tasks[ti].ConflictPaths = append([]string(nil), paths...)
		}
		if err := g.persist(ctx, p); err != nil {
			return nil, err
		}
		return g.openResolution(ctx, p, task, paths)
	}
	p.report.Attempts[idx].Outcome = ResolutionOutcomeResolved
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("arbitron: resolve conflicts of task %s (merge %d, attempt %d)", task.Key, att.MergeIndex, att.Attempt)
	if err := g.area.CommitResolution(ctx, *p.handle, msg); err != nil {
		return g.abort(ctx, p, fmt.Sprintf("record conflict resolution for task %s: %v", task.Key, err), "")
	}
	if ti >= 0 {
		p.report.Tasks[ti].Merged = true
		p.report.Tasks[ti].Reason = fmt.Sprintf("merged after conflict resolution (attempt %d)", att.Attempt)
	}
	p.report.MergeIndex++
	p.report.ActiveSession = nil
	p.report.State = gatherStateMerging
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	g.publish(&p.run, EventTypeGroupResolved, map[string]any{"taskKey": task.Key, "mergeIndex": att.MergeIndex, "attempt": att.Attempt})
	if err := g.enterGathering(ctx, p); err != nil {
		return nil, err
	}
	return g.mergeLoop(ctx, p, eligible)
}

// abort closes every open attempt, rolls the parent's area back to its exact
// pre-gather content, and settles the failure route. The decision is never
// touched. On rollback failure the run stays in gathering (continuation
// blocked) and ErrGroupGatherRollbackFailed is returned.
func (g *FileGatherer) abort(ctx context.Context, p *gatherPass, reason, openOutcome string) (*GatherStep, error) {
	// An abort must complete even while the run context is being torn down
	// (parent cancelled mid-merge): a rollback on a cancelled context would
	// fail every working-area call and strand the parent's area half-written.
	ctx = context.WithoutCancel(ctx)
	if openOutcome == "" {
		openOutcome = ResolutionOutcomeError
	}
	now := g.now().UTC()
	for i := range p.report.Attempts {
		a := &p.report.Attempts[i]
		if a.EndedAt == nil {
			ended := now
			a.EndedAt = &ended
			a.Outcome = openOutcome
		} else if a.Outcome == "" {
			a.Outcome = openOutcome
		}
	}
	p.report.ActiveSession = nil
	p.report.Error = reason
	for i := range p.report.Tasks {
		t := &p.report.Tasks[i]
		switch {
		case t.Merged:
			t.Merged = false
			t.Reason = "rolled back: " + reason
		case t.Reason == "":
			t.Reason = "not merged: " + reason
		}
	}
	if p.handle != nil {
		if err := g.area.Rollback(ctx, *p.handle); err != nil {
			p.report.RollbackStatus = rollbackStatusFailed
			p.report.State = gatherStateFailed
			p.report.Error = reason + rollbackFailedSep + err.Error()
			failure := fmt.Errorf("%w: run %s: %v (gather was aborted because: %s)", ErrGroupGatherRollbackFailed, p.run.ID, err, reason)
			if perr := g.persist(ctx, p); perr != nil {
				return nil, errors.Join(failure, perr)
			}
			g.publish(&p.run, EventTypeGroupGathering, map[string]any{
				"state": gatherStateFailed, "rollbackStatus": rollbackStatusFailed, "error": p.report.Error,
			})
			return nil, failure
		}
		p.report.RollbackStatus = rollbackStatusRolledBack
		p.report.State = gatherStateRolledBack
	} else {
		p.report.State = gatherStateFailed
	}
	if err := g.persist(ctx, p); err != nil {
		return nil, err
	}
	if err := g.enterGathering(ctx, p); err != nil {
		return nil, err
	}
	return g.finish(ctx, p, workflow.GroupPortFailure)
}

// enterGathering moves a resolving_conflicts run back to gathering.
func (g *FileGatherer) enterGathering(ctx context.Context, p *gatherPass) error {
	if p.run.State != instance.GroupRunResolvingConflicts {
		return nil
	}
	ok, err := g.groups.TransitionState(ctx, p.run.ID, instance.GroupRunResolvingConflicts, instance.GroupRunGathering)
	if err != nil {
		return fmt.Errorf("group gather: run %s: leave resolving_conflicts: %w", p.run.ID, err)
	}
	if !ok {
		return fmt.Errorf("group gather: run %s left resolving_conflicts concurrently", p.run.ID)
	}
	p.run.State = instance.GroupRunGathering
	return nil
}

// finish freezes the final output — BuildGroupOutput with "gather" replaced
// by the report — and moves the run gathering -> done (continuation ready).
func (g *FileGatherer) finish(ctx context.Context, p *gatherPass, port string) (*GatherStep, error) {
	report := copyGatherReport(p.report)
	p.run.Gather = &report
	out := BuildGroupOutput(&p.run)
	out["taskOutcome"] = p.run.Decision.Outcome
	out["outcome"] = port
	out["gather"] = gatherReportMap(p.report)
	ok, err := g.groups.SetFinalOutput(ctx, p.run.ID, port, out, instance.GroupRunGathering)
	if err != nil {
		return nil, fmt.Errorf("group gather: run %s: record final output: %w", p.run.ID, err)
	}
	if !ok {
		return g.reload(ctx, p.run.ID)
	}
	g.publish(&p.run, EventTypeGroupCompleted, map[string]any{"port": port, "gatherState": p.report.State})
	return &GatherStep{Done: true, Port: port, Output: out}, nil
}

func (g *FileGatherer) persist(ctx context.Context, p *gatherPass) error {
	report := copyGatherReport(p.report)
	if err := g.groups.SetGatherReport(ctx, p.run.ID, report); err != nil {
		return fmt.Errorf("group gather: run %s: persist gather report: %w", p.run.ID, err)
	}
	stored := copyGatherReport(p.report)
	p.run.Gather = &stored
	return nil
}

func (g *FileGatherer) publish(run *instance.GroupRun, eventType string, extra map[string]any) {
	if g.bus == nil {
		return
	}
	data := map[string]any{"groupRunId": run.ID.String(), "nodeId": run.NodeID}
	for k, v := range extra {
		data[k] = v
	}
	g.bus.Publish(run.ParentInstanceID, Event{Type: eventType, Timestamp: g.now(), Data: data})
}

// --- pure helpers -----------------------------------------------------------

func replayGatherStep(run *instance.GroupRun) *GatherStep {
	return &GatherStep{Done: true, Port: run.ContinuationPort, Output: run.FinalOutput}
}

func newGatherReport(run *instance.GroupRun) instance.GatherReport {
	tasks := orderedGroupTasks(run)
	r := instance.GatherReport{
		Mode:     workflow.GroupGatherResultsAndFiles,
		State:    gatherStatePending,
		Tasks:    make([]instance.GatherTaskReport, 0, len(tasks)),
		Attempts: []instance.ResolutionAttempt{},
	}
	for _, t := range tasks {
		r.Tasks = append(r.Tasks, instance.GatherTaskReport{Key: t.Key})
	}
	return r
}

func orderedGroupTasks(run *instance.GroupRun) []instance.GroupTask {
	tasks := append([]instance.GroupTask(nil), run.Tasks...)
	sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].Position < tasks[j].Position })
	return tasks
}

// eligibleGatherTasks returns the tasks whose changes a SUCCESSFUL decision
// gathers (author order) and a problem that makes the gather impossible.
func eligibleGatherTasks(run *instance.GroupRun) ([]instance.GroupTask, string) {
	tasks := orderedGroupTasks(run)
	var eligible []instance.GroupTask
	if run.CompletionRule == workflow.GroupRuleFirstSuccess {
		winner := run.Decision.WinnerKey
		if winner == "" {
			return nil, "first_success decision names no winner; nothing can be gathered"
		}
		for _, t := range tasks {
			if t.Key == winner {
				eligible = append(eligible, t)
			}
		}
		if len(eligible) == 0 {
			return nil, fmt.Sprintf("winning task %q is not on the group roster", winner)
		}
	} else {
		eligible = tasks
	}
	for _, t := range eligible {
		if strings.TrimSpace(t.FinalizedRef) == "" {
			return eligible, fmt.Sprintf("task %s has no finalized working-area ref; its file changes cannot be gathered", t.Key)
		}
	}
	return eligible, ""
}

func markIneligible(p *gatherPass, eligible []instance.GroupTask) {
	in := make(map[string]bool, len(eligible))
	for _, t := range eligible {
		in[t.Key] = true
	}
	for i := range p.report.Tasks {
		t := &p.report.Tasks[i]
		if in[t.Key] || t.Reason != "" {
			continue
		}
		if p.run.CompletionRule == workflow.GroupRuleFirstSuccess {
			t.Reason = "not the winner: first_success gathers only the winning task's changes"
		} else {
			t.Reason = "not eligible for gathering"
		}
	}
}

func parentWorkspace(parent *instance.Instance) string {
	if parent == nil {
		return ""
	}
	s, _ := parent.Variables[ReservedVarWorkspace].(string)
	return strings.TrimSpace(s)
}

func taskReportIndex(r *instance.GatherReport, key string) int {
	for i := range r.Tasks {
		if r.Tasks[i].Key == key {
			return i
		}
	}
	return -1
}

// latestAttemptIndex returns the index of the newest attempt for mergeIndex, or -1.
func latestAttemptIndex(r *instance.GatherReport, mergeIndex int) int {
	idx, best := -1, 0
	for i, a := range r.Attempts {
		if a.MergeIndex == mergeIndex && a.Attempt >= best {
			idx, best = i, a.Attempt
		}
	}
	return idx
}

func nextAttemptNumber(r *instance.GatherReport, mergeIndex int) int {
	n := 0
	for _, a := range r.Attempts {
		if a.MergeIndex == mergeIndex && a.Attempt > n {
			n = a.Attempt
		}
	}
	return n + 1
}

// findAttempt returns a pointer into r.Attempts for (mergeIndex, attempt), or nil.
func findAttempt(r *instance.GatherReport, mergeIndex, attempt int) *instance.ResolutionAttempt {
	if r == nil {
		return nil
	}
	for i := range r.Attempts {
		if r.Attempts[i].MergeIndex == mergeIndex && r.Attempts[i].Attempt == attempt {
			return &r.Attempts[i]
		}
	}
	return nil
}

// copyGatherReport deep-copies a report (JSON round trip: the report is
// exactly the persisted JSON document, so this is also its storage shape).
func copyGatherReport(r instance.GatherReport) instance.GatherReport {
	b, err := json.Marshal(r)
	if err != nil {
		return r
	}
	var out instance.GatherReport
	if err := json.Unmarshal(b, &out); err != nil {
		return r
	}
	return out
}

func gatherReportMap(r instance.GatherReport) map[string]any {
	b, err := json.Marshal(r)
	if err != nil {
		return map[string]any{"mode": r.Mode, "state": r.State, "error": r.Error}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"mode": r.Mode, "state": r.State, "error": r.Error}
	}
	return m
}

func gatherHandleMap(h GatherHandle) map[string]any {
	return map[string]any{
		"snapshotHandle": h.SnapshotHandle,
		"gatherId":       h.GatherID.String(),
		"stagingDir":     h.StagingDir,
		"rollbackRef":    h.RollbackRef,
		"parentDir":      h.ParentDir,
	}
}

// GatherHandleFromReport decodes the persisted gather handle. ok is false when
// no gather has been staged yet (or the handle is incomplete).
func GatherHandleFromReport(r *instance.GatherReport) (GatherHandle, bool) {
	if r == nil || len(r.Handle) == 0 {
		return GatherHandle{}, false
	}
	b, err := json.Marshal(r.Handle)
	if err != nil {
		return GatherHandle{}, false
	}
	var h GatherHandle
	if err := json.Unmarshal(b, &h); err != nil {
		return GatherHandle{}, false
	}
	if h.GatherID == uuid.Nil || h.StagingDir == "" || h.ParentDir == "" {
		return GatherHandle{}, false
	}
	return h, true
}
```

- [x] Step 5: Run the tests and confirm they pass.

Focused gatherer/handle tests pass, including automatic same-call commit/apply, full-ref close/notification checks across distinct runs, unresolved human follow-up, error injection at active-identity/verification/close/notification/attempt-end/verified/merge-progress/applied boundaries, closed-session replay without another agent, rollback-result persistence recovery, and a real Engine → ParallelGroupExecutor → FileGatherer downstream failure-variable sink.

```bash
go test ./domain/engine/ -run 'TestFileGatherer|TestGatherHandleFromReport' -count=1 -v 2>&1 | tail -40
```
Expected (abridged):
```
--- PASS: TestFileGatherer_AllSucceed_MergesEveryTaskInAuthorOrderAndApplies (0.00s)
--- PASS: TestFileGatherer_FirstSuccess_MergesOnlyTheWinner (0.00s)
--- PASS: TestFileGatherer_FailedDecision_MergesNothingAndRoutesFailure (0.00s)
...
--- PASS: TestFileGatherer_Provenance_ParentDirComesFromParentWorkspaceVariable (0.00s)
--- PASS: TestGatherHandleFromReport_RoundTrip (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.0XXs
```

- [x] Step 6: Run the whole package, gofmt and vet.

Completion pass after P3-T11 (`62faa02`): the production root now constructs the real Git working area, task finalizer, resolution bridge, gatherer and startup/periodic gather reconciler before executor freeze. `groupExecutor.SetGatherer(gatherer)` feeds persisted decisions, snapshots, finalized refs and the immediate parent's workspace into the gatherer. The former wiring/recovery blocker is resolved; no new implementation was needed in this pass.

Verification (all green, repeated after self-review):
- `go test ./domain/engine/ -count=1`
- `go test ./domain/engine/ -run 'TestFileGatherer|TestGatherHandleFromReport' -race -count=10`
- `go test ./cmd/arbitron/ -run '^TestGroupFilesE2E_' -race -count=1`
- The specified `gofmt -l`, `go vet ./domain/engine/`, and `git diff --check` produce no findings.

Completion self-review, in the required order:
1. Spec compliance: reviewed the gatherer against this task's state/eligibility/author-order/rollback/replay contracts and automatic-resolution/final-route refinements. Existing implementation covers the required slice; no extra features, flags or production changes were added.
2. Anti-green-theater: the real-root tests assert downstream persisted variables read from actual parent files (clean gathering, winner-only gathering, failed decisions, automatic and human resolution, abandon, failed apply, nesting and restart recovery). Temporarily removed only `groupExecutor.SetGatherer(gatherer)` from `initDomain`; the all-succeed sink test failed because the parent failed instead of completing. Restored wiring and reran the entire file-root race test set, green. No mutation remains.
3. Code quality: checked neighboring finalizer/executor conventions and gatherer copy/persistence/error handling. No cleanup was necessary. Reran the focused repeated race tests, whole engine package, production-root file tests, formatting, vet and diff checks after review, all green.

Historical blocker and initial verification evidence (resolved by P3-T8/P3-T10/P3-T11):

> RESOLVED: The earlier blocker was: The local implementation/verifications are green, but the mission's wired-and-fed completion gate requires P3-T8's real GroupResolutionBridge, P3-T10's startup closed-session recovery, and P3-T11's initDomain construction/data/sink tests. None exists yet: cmd/arbitron/main.go:990-1002 still constructs filesystem.NewGroupWorkspace and results-only support, and has no NewGroupGatherer/SetGatherer/SetGroupTaskFinalizer calls. The existing executor consumes this gatherer through SetGatherer, and P3-T5 now produces finalized refs, but neither is enabled at the production root. Do not enable partial file support or substitute domain fakes for those planned runtime probes. Leave this completion gate unchecked until those dependencies land; skip re-failing it on intervening passes and proceed to independent P3-T7 steps.

Verification: the full engine package passes; focused gatherer/handle tests pass under `-race -count=10`; module build, engine vet, gofmt and diff checks pass. Step 6 remains unchecked only for the runtime gate above.

Self-review (in required order):
1. Spec compliance: kept this slice to the gatherer/port/shared fakes/tests; no early file-mode enablement, flags, root wiring, bridge or reconciler implementation. Followed the binding outcome revision (`outcome` matches route, `taskOutcome`/decision remain immutable). Red/green review regressions cover abort intent before rollback, failure-state recovery without losing its reason, rollback failure returning to gathering, immutable replay copies and incomplete handle decoding.
2. Anti-green-theater: keyed staging repairs drive real fake-area byte changes, full refs are asserted at both closure and notification sinks across distinct runs, and the real Engine/executor seam persists downstream failure variables. Temporarily made production Apply a no-op: clean/automatic byte-sink tests and the downstream failure-route test all failed. Restored Apply; no mutation remains. These domain probes do not claim the still-missing production root or P3-T10 closed-session reconciliation is proven.
3. Code quality: reviewed neighboring output/repository/finalizer conventions; reused GatherReport.Clone and variable.DeepCopyMap rather than JSON-copy fallback, retained existing constants/ports, and made test replay effects idempotent and argument-checked. All focused/race/package/build/format/vet checks rerun after fixes pass.

```bash
go test ./domain/engine/ -count=1
gofmt -l domain/engine/group_gatherer.go domain/engine/group_gatherer_test.go domain/engine/group_gather_fakes_test.go
go vet ./domain/engine/
```
Expected: `ok  	github.com/arbitron/arbitron/domain/engine	...`; `gofmt -l` and `go vet` print nothing.

- [x] Step 7: Commit.

Committed the unblocked gatherer, shared fakes, sink/recovery regression tests and verification evidence initially. This completion pass closes Step 6 after its assigned bridge/recovery/composition dependencies landed, recording verification and self-review without changing production behavior.

```bash
git add domain/engine/group_gatherer.go domain/engine/group_gatherer_test.go domain/engine/group_gather_fakes_test.go
git commit -m "feat(engine): resumable file gatherer for results_and_files parallel groups" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
