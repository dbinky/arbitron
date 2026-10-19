package engine_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// ---------------------------------------------------------------------------
// memGroupRuns: an in-memory instance.GroupRunRepository that honours every
// compare-and-set the contract specifies (keyed by run id and task key), deep
// copies on every read and write, and supports per-method error injection.
// ---------------------------------------------------------------------------

type memGroupRuns struct {
	mu         sync.Mutex
	runs       map[uuid.UUID]*instance.GroupRun
	order      []uuid.UUID
	seq        int64
	failNext   map[string]error // method -> error returned ONCE, before any mutation
	failAll    map[string]error // method -> error returned on every call
	calls      []string         // "Method" or "Method:detail", in call order
	createdRaw []*instance.GroupRun
	onCall     func(method string)
}

var _ instance.GroupRunRepository = (*memGroupRuns)(nil)

func newMemGroupRuns() *memGroupRuns {
	return &memGroupRuns{
		runs:     map[uuid.UUID]*instance.GroupRun{},
		failNext: map[string]error{},
		failAll:  map[string]error{},
	}
}

func (m *memGroupRuns) failOnce(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext[method] = err
}

func (m *memGroupRuns) failAlways(method string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failAll[method] = err
}

func (m *memGroupRuns) begin(method, detail string) error {
	m.mu.Lock()
	hook := m.onCall
	m.mu.Unlock()
	if hook != nil {
		hook(method)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := method
	if detail != "" {
		entry += ":" + detail
	}
	m.calls = append(m.calls, entry)
	if err, ok := m.failNext[method]; ok {
		delete(m.failNext, method)
		return err
	}
	if err, ok := m.failAll[method]; ok {
		return err
	}
	return nil
}

func (m *memGroupRuns) callCount(entry string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.calls {
		if c == entry {
			n++
		}
	}
	return n
}

func (m *memGroupRuns) seedRun(run *instance.GroupRun) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := copyGroupRun(run)
	m.runs[cp.ID] = cp
	m.order = append(m.order, cp.ID)
	for _, t := range cp.Tasks {
		if t.TerminalSeq != nil && *t.TerminalSeq > m.seq {
			m.seq = *t.TerminalSeq
		}
	}
}

func (m *memGroupRuns) get(id uuid.UUID) *instance.GroupRun {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copyGroupRun(m.runs[id])
}

func (m *memGroupRuns) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}

func copyGroupRun(r *instance.GroupRun) *instance.GroupRun {
	if r == nil {
		return nil
	}
	cp := *r
	cp.Config = variable.DeepCopyMap(r.Config)
	cp.StartVariables = variable.DeepCopyMap(r.StartVariables)
	cp.FinalOutput = variable.DeepCopyMap(r.FinalOutput)
	if r.BodyOwnership != nil {
		cp.BodyOwnership = make(map[string][]string, len(r.BodyOwnership))
		for k, v := range r.BodyOwnership {
			cp.BodyOwnership[k] = append([]string(nil), v...)
		}
	}
	if r.Decision != nil {
		d := copyDecision(*r.Decision)
		cp.Decision = &d
	}
	cp.Tasks = make([]instance.GroupTask, len(r.Tasks))
	for i, t := range r.Tasks {
		t.Result = variable.DeepCopyMap(t.Result)
		t.Failure = variable.DeepCopyMap(t.Failure)
		if t.TerminalSeq != nil {
			s := *t.TerminalSeq
			t.TerminalSeq = &s
		}
		if t.TerminalAt != nil {
			a := *t.TerminalAt
			t.TerminalAt = &a
		}
		cp.Tasks[i] = t
	}
	if r.Gather != nil {
		g := r.Gather.Clone()
		cp.Gather = &g
	}
	sort.SliceStable(cp.Tasks, func(i, j int) bool { return cp.Tasks[i].Position < cp.Tasks[j].Position })
	return &cp
}

func copyDecision(d instance.GroupDecision) instance.GroupDecision {
	tasks := make([]instance.FrozenTask, len(d.Tasks))
	for i, ft := range d.Tasks {
		ft.Result = variable.DeepCopyMap(ft.Result)
		ft.Failure = variable.DeepCopyMap(ft.Failure)
		tasks[i] = ft
	}
	d.Tasks = tasks
	return d
}

func memTerminal(s instance.GroupTaskStatus) bool {
	return s == instance.GroupTaskStatusCompleted || s == instance.GroupTaskStatusFailed || s == instance.GroupTaskStatusCancelled
}

// taskLocked returns the stored run and task. Caller holds m.mu.
func (m *memGroupRuns) taskLocked(runID uuid.UUID, key string) (*instance.GroupRun, *instance.GroupTask, error) {
	run, ok := m.runs[runID]
	if !ok {
		return nil, nil, fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
	}
	for i := range run.Tasks {
		if run.Tasks[i].Key == key {
			return run, &run.Tasks[i], nil
		}
	}
	return run, nil, fmt.Errorf("group run %s has no task %q", runID, key)
}

func (m *memGroupRuns) runLocked(runID uuid.UUID) (*instance.GroupRun, error) {
	run, ok := m.runs[runID]
	if !ok {
		return nil, fmt.Errorf("group run %s: %w", runID, instance.ErrGroupRunNotFound)
	}
	return run, nil
}

func (m *memGroupRuns) CreateWithTasks(_ context.Context, run *instance.GroupRun) error {
	if err := m.begin("CreateWithTasks", ""); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.ParentInstanceID == run.ParentInstanceID && r.NodeID == run.NodeID && r.Attempt == run.Attempt {
			return fmt.Errorf("group run for %s/%s attempt %d: %w", run.ParentInstanceID, run.NodeID, run.Attempt, instance.ErrGroupRunExists)
		}
	}
	m.createdRaw = append(m.createdRaw, run)
	cp := copyGroupRun(run)
	m.runs[cp.ID] = cp
	m.order = append(m.order, cp.ID)
	return nil
}

func (m *memGroupRuns) Get(_ context.Context, id uuid.UUID) (*instance.GroupRun, error) {
	if err := m.begin("Get", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(id)
	if err != nil {
		return nil, err
	}
	return copyGroupRun(run), nil
}

func (m *memGroupRuns) GetByParentNode(_ context.Context, parentID uuid.UUID, nodeID string) (*instance.GroupRun, error) {
	if err := m.begin("GetByParentNode", nodeID); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *instance.GroupRun
	for _, id := range m.order {
		r := m.runs[id]
		if r.ParentInstanceID == parentID && r.NodeID == nodeID && (latest == nil || r.Attempt > latest.Attempt) {
			latest = r
		}
	}
	return copyGroupRun(latest), nil
}

func (m *memGroupRuns) GetByChild(_ context.Context, childID uuid.UUID) (*instance.GroupRun, error) {
	if err := m.begin("GetByChild", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		for _, t := range m.runs[id].Tasks {
			if t.ChildInstanceID == childID {
				return copyGroupRun(m.runs[id]), nil
			}
		}
	}
	return nil, nil
}

func (m *memGroupRuns) ListByParent(_ context.Context, parentID uuid.UUID) ([]instance.GroupRun, error) {
	if err := m.begin("ListByParent", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []instance.GroupRun
	for _, id := range m.order {
		if m.runs[id].ParentInstanceID == parentID {
			out = append(out, *copyGroupRun(m.runs[id]))
		}
	}
	return out, nil
}

func (m *memGroupRuns) ListUnsettled(_ context.Context) ([]uuid.UUID, error) {
	if err := m.begin("ListUnsettled", ""); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []uuid.UUID{}
	for _, id := range m.order {
		r := m.runs[id]
		if r.State != instance.GroupRunDone || !r.ContinuationDelivered || !r.ActionsApplied {
			out = append(out, id)
		}
	}
	return out, nil
}

func (m *memGroupRuns) MarkTaskLaunched(_ context.Context, runID uuid.UUID, key string) error {
	if err := m.begin("MarkTaskLaunched", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.LaunchState = instance.GroupTaskLaunched
	if t.LiveStatus == instance.GroupTaskStatusNotStarted {
		t.LiveStatus = instance.GroupTaskStatusQueued
	}
	run.UpdatedAt = time.Now()
	return nil
}

func (m *memGroupRuns) MarkTaskLaunchFailed(_ context.Context, runID uuid.UUID, key, reason string) error {
	if err := m.begin("MarkTaskLaunchFailed", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.LaunchState = instance.GroupTaskLaunchFailed
	t.LaunchError = reason
	run.UpdatedAt = time.Now()
	return nil
}

func (m *memGroupRuns) TransitionState(_ context.Context, runID uuid.UUID, from, to instance.GroupRunState) (bool, error) {
	if err := m.begin("TransitionState", string(from)+"->"+string(to)); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return false, err
	}
	if run.State != from {
		return false, nil
	}
	run.State = to
	run.UpdatedAt = time.Now()
	return true, nil
}

func (m *memGroupRuns) RecordTaskTerminal(_ context.Context, runID uuid.UUID, key string, status instance.GroupTaskStatus, result, failure map[string]any, at time.Time) (int64, bool, error) {
	if err := m.begin("RecordTaskTerminal", key); err != nil {
		return 0, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, t, err := m.taskLocked(runID, key)
	if err != nil {
		return 0, false, err
	}
	if t.TerminalSeq != nil {
		return *t.TerminalSeq, false, nil
	}
	m.seq++
	s := m.seq
	when := at
	t.TerminalSeq = &s
	t.TerminalAt = &when
	t.LiveStatus = status
	t.Result = variable.DeepCopyMap(result)
	t.Failure = variable.DeepCopyMap(failure)
	run.UpdatedAt = time.Now()
	return s, true, nil
}

func (m *memGroupRuns) UpdateTaskLiveStatus(_ context.Context, runID uuid.UUID, key string, status instance.GroupTaskStatus) error {
	if err := m.begin("UpdateTaskLiveStatus", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	if memTerminal(t.LiveStatus) && !memTerminal(status) {
		return nil
	}
	t.LiveStatus = status
	return nil
}

func (m *memGroupRuns) Decide(_ context.Context, runID uuid.UUID, d instance.GroupDecision) (bool, error) {
	if err := m.begin("Decide", ""); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return false, err
	}
	if run.Decision != nil || (run.State != instance.GroupRunLaunching && run.State != instance.GroupRunRunning) {
		return false, nil
	}
	cp := copyDecision(d)
	run.Decision = &cp
	run.State = instance.GroupRunDecided
	for i := range run.Tasks {
		run.Tasks[i].Blocking = false
	}
	run.UpdatedAt = time.Now()
	return true, nil
}

func (m *memGroupRuns) MarkTaskCancelRequested(_ context.Context, runID uuid.UUID, key string) error {
	if err := m.begin("MarkTaskCancelRequested", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.CancelRequested = true
	return nil
}

func (m *memGroupRuns) MarkTaskContinuingIndependently(_ context.Context, runID uuid.UUID, key string) error {
	if err := m.begin("MarkTaskContinuingIndependently", key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, t, err := m.taskLocked(runID, key)
	if err != nil {
		return err
	}
	t.ContinuingIndependently = true
	return nil
}

func (m *memGroupRuns) MarkActionsApplied(_ context.Context, runID uuid.UUID) error {
	if err := m.begin("MarkActionsApplied", ""); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return err
	}
	run.ActionsApplied = true
	return nil
}

func (m *memGroupRuns) SetFinalOutput(_ context.Context, runID uuid.UUID, port string, output map[string]any, from instance.GroupRunState) (bool, error) {
	if err := m.begin("SetFinalOutput", port); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return false, err
	}
	if run.State != from || run.FinalOutput != nil {
		return false, nil
	}
	run.FinalOutput = variable.DeepCopyMap(output)
	run.ContinuationPort = port
	run.ContinuationReady = true
	run.State = instance.GroupRunDone
	run.UpdatedAt = time.Now()
	return true, nil
}

func (m *memGroupRuns) MarkContinuationDelivered(_ context.Context, runID uuid.UUID) error {
	if err := m.begin("MarkContinuationDelivered", ""); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return err
	}
	run.ContinuationDelivered = true
	return nil
}

// ---------------------------------------------------------------------------
// Gather report / finalized ref writes mirror the SQLite repository contract.
func (m *memGroupRuns) SetGatherReport(_ context.Context, runID uuid.UUID, report instance.GatherReport) error {
	if err := m.begin("SetGatherReport", ""); err != nil {
		return err
	}
	if err := report.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return err
	}
	if run.State == instance.GroupRunDone {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportFrozen)
	}
	rep := report.Normalized()
	run.Gather = &rep
	run.UpdatedAt = time.Now()
	return nil
}

func (m *memGroupRuns) ReplaceGatherReport(_ context.Context, runID uuid.UUID, expected *instance.GatherReport, report instance.GatherReport) error {
	if err := m.begin("ReplaceGatherReport", ""); err != nil {
		return err
	}
	if err := report.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, err := m.runLocked(runID)
	if err != nil {
		return err
	}
	if run.State == instance.GroupRunDone {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportFrozen)
	}
	same, err := instance.SameGatherReport(run.Gather, expected)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("group run %s: %w", runID, instance.ErrGatherReportStale)
	}
	rep := report.Normalized()
	run.Gather = &rep
	run.UpdatedAt = time.Now()
	return nil
}

func (m *memGroupRuns) SetTaskFinalizedRef(_ context.Context, runID uuid.UUID, key, ref string) error {
	if err := m.begin("SetTaskFinalizedRef", key); err != nil {
		return err
	}
	if key == "" || ref == "" {
		return fmt.Errorf("set task finalized ref: task key and ref are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.runLocked(runID); err != nil {
		return err
	}
	_, task, err := m.taskLocked(runID, key)
	if err != nil {
		return fmt.Errorf("group run %s task %q: %w", runID, key, instance.ErrGroupRunNotFound)
	}
	task.FinalizedRef = ref
	return nil
}

// ---------------------------------------------------------------------------
// fakeGroupTaskStarter: records every StartGroupTask call with the identity on
// ctx, fails per TASK KEY, and (when instances is set) creates one child row
// per reserved id — idempotently, exactly like Engine.StartGroupTask.
// ---------------------------------------------------------------------------

type fakeGroupTaskStarter struct {
	mu         sync.Mutex
	reqs       []engine.GroupTaskStartRequest
	idents     []identity.Identity
	errByKey   map[string]error
	onStart    func(req engine.GroupTaskStartRequest)
	instances  *mockInstanceRepo
	childState instance.State
}

var _ engine.GroupTaskStarter = (*fakeGroupTaskStarter)(nil)

func (s *fakeGroupTaskStarter) StartGroupTask(ctx context.Context, req engine.GroupTaskStartRequest) error {
	who, _ := identity.FromContext(ctx)
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.idents = append(s.idents, who)
	hook := s.onStart
	err := s.errByKey[req.TaskKey]
	repo := s.instances
	state := s.childState
	s.mu.Unlock()
	if hook != nil {
		hook(req)
	}
	if err != nil {
		return err
	}
	if repo == nil {
		return nil
	}
	if state == "" {
		state = instance.StateQueued
	}
	repo.mu.Lock()
	_, exists := repo.instances[req.TaskInstanceID]
	repo.mu.Unlock()
	if exists {
		return nil
	}
	parentID, parentNode, runID, key := req.ParentInstanceID, req.ParentNodeID, req.GroupRunID, req.TaskKey
	now := time.Now()
	return repo.Create(ctx, &instance.Instance{
		ID: req.TaskInstanceID, OwnerID: who.UserID, WorkflowName: req.ChildWorkflow,
		ParentInstanceID: &parentID, ParentNodeID: &parentNode, SpawnMode: instance.SpawnModeGroupTask,
		GroupRunID: &runID, GroupTaskKey: &key, GroupTaskKind: req.Kind,
		State: state, Priority: instance.Priority(req.Priority),
		Variables: variable.DeepCopyMap(req.Variables), CreatedAt: now, UpdatedAt: now,
	})
}

func (s *fakeGroupTaskStarter) requests() []engine.GroupTaskStartRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]engine.GroupTaskStartRequest(nil), s.reqs...)
}

func (s *fakeGroupTaskStarter) identities() []identity.Identity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]identity.Identity(nil), s.idents...)
}

// ---------------------------------------------------------------------------
// recordingCanceller: an engine.RunCanceller that records (id, identity) and,
// when instances is set, behaves like Engine.Cancel's state outcome: an absent
// row is ErrNotFound, a terminal row ErrCannotCancel, otherwise cancelled.
// ---------------------------------------------------------------------------

type cancelCall struct {
	ID       uuid.UUID
	Identity identity.Identity
}

type recordingCanceller struct {
	mu         sync.Mutex
	calls      []cancelCall
	errByID    map[uuid.UUID]error
	failNext   error
	instances  *mockInstanceRepo
	onTerminal func(id uuid.UUID) // simulates the atomic persistence boundary
	onCancel   func(id uuid.UUID)
}

var (
	_ engine.RunCanceller       = (*recordingCanceller)(nil)
	_ engine.GroupTaskCanceller = (*recordingCanceller)(nil)
)

// CancelGroupTask records like Cancel; the fake has no subtree to stamp, so
// the two differ only on the real engine (group_task_cancel_test.go).
func (c *recordingCanceller) CancelGroupTask(ctx context.Context, id uuid.UUID) error {
	return c.Cancel(ctx, id)
}

func (c *recordingCanceller) Cancel(ctx context.Context, id uuid.UUID) error {
	who, _ := identity.FromContext(ctx)
	c.mu.Lock()
	c.calls = append(c.calls, cancelCall{ID: id, Identity: who})
	err := c.errByID[id]
	if c.failNext != nil {
		err = c.failNext
		c.failNext = nil
	}
	repo := c.instances
	terminalHook := c.onTerminal
	hook := c.onCancel
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if repo == nil {
		return nil
	}
	repo.mu.Lock()
	inst, ok := repo.instances[id]
	if !ok {
		repo.mu.Unlock()
		return fmt.Errorf("instance %s: %w", id, instance.ErrNotFound)
	}
	if inst.State.IsTerminal() {
		repo.mu.Unlock()
		return fmt.Errorf("instance %s cannot be cancelled (state: %s): %w", id, inst.State, instance.ErrCannotCancel)
	}
	now := time.Now()
	inst.State = instance.StateCancelled
	inst.CompletedAt = &now
	inst.UpdatedAt = now
	repo.mu.Unlock()
	if terminalHook != nil {
		terminalHook(id)
	}
	if hook != nil {
		hook(id)
	}
	return nil
}

func (c *recordingCanceller) callsFor(id uuid.UUID) []cancelCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []cancelCall
	for _, call := range c.calls {
		if call.ID == id {
			out = append(out, call)
		}
	}
	return out
}

func (c *recordingCanceller) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.calls)
}

// ---------------------------------------------------------------------------
// fakeChildContracts: an engine.ChildWorkflowReader keyed by workflow NAME that
// records the identity each lookup was made under.
// ---------------------------------------------------------------------------

type fakeChildContracts struct {
	mu     sync.Mutex
	byName map[string][]workflow.InputField
	err    error
	asked  []identity.Identity
	names  []string
}

var _ engine.ChildWorkflowReader = (*fakeChildContracts)(nil)

func (f *fakeChildContracts) ChildInputs(ctx context.Context, name string) ([]workflow.InputField, error) {
	who, _ := identity.FromContext(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, who)
	f.names = append(f.names, name)
	if f.err != nil {
		return nil, f.err
	}
	fields, ok := f.byName[name]
	if !ok {
		return nil, fmt.Errorf("child inputs %q: %w", name, workflow.ErrNotFound)
	}
	return fields, nil
}

// ---------------------------------------------------------------------------
// dirWorkspaces: a real-directory engine.WorkspaceManager under a temp root,
// laid out like adapter/filesystem (<root>/instances/<id>).
// ---------------------------------------------------------------------------

type dirWorkspaces struct {
	root      string
	mu        sync.Mutex
	ensured   []uuid.UUID
	removed   []uuid.UUID
	ensureErr error
}

var _ engine.WorkspaceManager = (*dirWorkspaces)(nil)

func newDirWorkspaces(t *testing.T) *dirWorkspaces {
	t.Helper()
	return &dirWorkspaces{root: t.TempDir()}
}

func (w *dirWorkspaces) path(id uuid.UUID) string {
	return filepath.Join(w.root, "instances", id.String())
}

func (w *dirWorkspaces) EnsureInstanceDir(id uuid.UUID) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ensureErr != nil {
		return "", w.ensureErr
	}
	p := w.path(id)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return "", err
	}
	w.ensured = append(w.ensured, id)
	return p, nil
}

func (w *dirWorkspaces) RemoveInstanceDir(id uuid.UUID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removed = append(w.removed, id)
	return os.RemoveAll(w.path(id))
}

func (w *dirWorkspaces) InstanceDirStats(id uuid.UUID) (int64, int, error) {
	var size int64
	files := 0
	err := filepath.WalkDir(w.path(id), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			size += info.Size()
			files++
		}
		return nil
	})
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	return size, files, err
}

func (w *dirWorkspaces) ensuredIDs() []uuid.UUID {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]uuid.UUID(nil), w.ensured...)
}

// ---------------------------------------------------------------------------
// copyGroupWorkspace: an engine.GroupWorkspace that really copies directory
// trees, so tests assert file content at the sink (the task directory).
// ---------------------------------------------------------------------------

type copyGroupWorkspace struct {
	root     string
	mu       sync.Mutex
	snapErr  error
	forkErr  error
	snaps    []string
	forks    []string
	released []string
}

var _ engine.GroupWorkspace = (*copyGroupWorkspace)(nil)

func newCopyGroupWorkspace(t *testing.T) *copyGroupWorkspace {
	t.Helper()
	return &copyGroupWorkspace{root: t.TempDir()}
}

func (g *copyGroupWorkspace) SnapshotParent(_ context.Context, groupRunID uuid.UUID, parentDir string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.snapErr != nil {
		return "", g.snapErr
	}
	dst := filepath.Join(g.root, ".group-snapshots", groupRunID.String())
	if err := copyDirTree(parentDir, dst); err != nil {
		return "", err
	}
	g.snaps = append(g.snaps, dst)
	return dst, nil
}

func (g *copyGroupWorkspace) ForkTask(_ context.Context, handle string, taskDir string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.forkErr != nil {
		return g.forkErr
	}
	if err := copyDirTree(handle, taskDir); err != nil {
		return err
	}
	g.forks = append(g.forks, taskDir)
	return nil
}

func (g *copyGroupWorkspace) ReleaseSnapshot(_ context.Context, handle string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.released = append(g.released, handle)
	return os.RemoveAll(handle)
}

func (g *copyGroupWorkspace) snapshotHandles() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.snaps...)
}

func (g *copyGroupWorkspace) releasedHandles() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.released...)
}

func copyDirTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm())
	})
}
