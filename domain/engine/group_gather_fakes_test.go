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
	if previous := a.gathers[gatherID]; previous != nil {
		if previous.handle != h {
			return engine.GatherHandle{}, errors.New("fake area: gather identity rebound")
		}
		return h, nil
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
	if !ok || g.handle != h {
		return nil, fmt.Errorf("fake area: unknown or mismatched gather %s", h.GatherID)
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
		if g.pending == taskRef {
			return engine.MergeOutcome{ConflictPaths: append([]string(nil), g.pendingPaths...)}, nil
		}
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

// resolveStagedIn repairs only the staging area identified by the request.
func (a *fakeGatherArea) resolveStagedIn(h engine.GatherHandle, path, content string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g, err := a.gather(h)
	if err != nil {
		panic(err)
	}
	g.staged[path] = content
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
	mu        sync.Mutex
	started   []startedResolution
	err       error
	onStart   func(engine.ResolutionRequest)
	closed    []instance.ResolutionSessionRef
	notified  []instance.ResolutionSessionRef
	closeErr  error
	notifyErr error
}

var _ engine.GroupResolutionStarter = (*fakeResolutionStarter)(nil)

func (s *fakeResolutionStarter) StartResolution(_ context.Context, run *instance.GroupRun, parent *instance.Instance, req engine.ResolutionRequest) (*instance.ResolutionSessionRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run == nil || parent == nil {
		return nil, errors.New("fake starter: run and parent are required")
	}
	ref := &instance.ResolutionSessionRef{
		GroupRunID: run.ID, MergeIndex: req.MergeIndex, Attempt: req.Attempt,
		InstanceID: parent.ID, NodeID: run.NodeID,
	}
	for _, previous := range s.started {
		if previous.runID == run.ID && previous.parentID == parent.ID && previous.nodeID == run.NodeID &&
			previous.req.MergeIndex == req.MergeIndex && previous.req.Attempt == req.Attempt {
			if previous.req.Handle != req.Handle || previous.req.TaskKey != req.TaskKey || previous.req.TaskRef != req.TaskRef {
				return nil, errors.New("fake starter: replay rebound the session inputs")
			}
			return ref, nil
		}
	}
	if s.err != nil {
		return nil, s.err
	}
	s.started = append(s.started, startedResolution{runID: run.ID, parentID: parent.ID, nodeID: run.NodeID, req: req})
	if s.onStart != nil {
		s.onStart(req)
	}
	return ref, nil
}

func (s *fakeResolutionStarter) NotifyResolutionRequired(_ context.Context, ref instance.ResolutionSessionRef) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ref.GroupRunID == uuid.Nil || ref.InstanceID == uuid.Nil || ref.NodeID == "" {
		return errors.New("fake starter: invalid notification identity")
	}
	if s.notifyErr != nil {
		return s.notifyErr
	}
	for _, notified := range s.notified {
		if notified == ref {
			return nil
		}
	}
	s.notified = append(s.notified, ref)
	return nil
}

func (s *fakeResolutionStarter) CloseSession(_ context.Context, ref instance.ResolutionSessionRef, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ref.GroupRunID == uuid.Nil || ref.InstanceID == uuid.Nil || ref.NodeID == "" {
		return errors.New("fake starter: invalid session closure")
	}
	switch reason {
	case "completed", "cancelled", "abandoned", "error":
	default:
		return errors.New("fake starter: invalid session closure reason")
	}
	if s.closeErr != nil {
		return s.closeErr
	}
	for _, closed := range s.closed {
		if closed == ref {
			return nil
		}
	}
	s.closed = append(s.closed, ref)
	return nil
}

func (s *fakeResolutionStarter) calls() []startedResolution {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]startedResolution(nil), s.started...)
}
