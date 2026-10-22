package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	sgAlice = uuid.MustParse("a11ce000-0000-0000-0000-000000000001")
	sgBob   = uuid.MustParse("b0b00000-0000-0000-0000-000000000002")
)

// ---------------------------------------------------------------------------
// sgRepo: a JSON-backed, call-recording workflow.Repository. Every Load returns
// a FRESH decoded copy (like the filesystem repo), so "nothing was persisted"
// assertions cannot be fooled by a shared pointer. Writes are counted so a test
// can prove a rejected write never reached storage.
// ---------------------------------------------------------------------------

type sgRepo struct {
	mu          sync.Mutex
	store       map[string]map[string][]byte
	creates     int
	updates     int
	otherWrites int
	loads       map[string]int
	loadErr     map[string]error
	updateErr   error
}

func newSGRepo() *sgRepo {
	return &sgRepo{
		store:   map[string]map[string][]byte{},
		loads:   map[string]int{},
		loadErr: map[string]error{},
	}
}

func sgEncode(wf *workflow.Workflow) []byte {
	b, err := json.Marshal(wf)
	if err != nil {
		panic(err)
	}
	return b
}

func sgDecode(b []byte) *workflow.Workflow {
	var wf workflow.Workflow
	if err := json.Unmarshal(b, &wf); err != nil {
		panic(err)
	}
	return &wf
}

func (r *sgRepo) bucket(s workflow.Scope) map[string][]byte {
	k := scopeKey(s)
	if r.store[k] == nil {
		r.store[k] = map[string][]byte{}
	}
	return r.store[k]
}

// seed stores wf under scope WITHOUT counting a service write.
func (r *sgRepo) seed(s workflow.Scope, wf *workflow.Workflow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bucket(s)[wf.Name] = sgEncode(wf)
}

// raw returns a copy of the stored bytes (nil when absent).
func (r *sgRepo) raw(s workflow.Scope, name string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.bucket(s)[name]
	if !ok {
		return nil
	}
	return append([]byte(nil), b...)
}

// stored decodes the stored workflow (nil when absent).
func (r *sgRepo) stored(s workflow.Scope, name string) *workflow.Workflow {
	b := r.raw(s, name)
	if b == nil {
		return nil
	}
	return sgDecode(b)
}

func (r *sgRepo) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.creates + r.updates + r.otherWrites
}

func (r *sgRepo) loadCount(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loads[name]
}

func (r *sgRepo) Load(_ context.Context, s workflow.Scope, name string) (*workflow.Workflow, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loads[name]++
	if err := r.loadErr[name]; err != nil {
		return nil, err
	}
	b, ok := r.bucket(s)[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	return sgDecode(b), nil
}

func (r *sgRepo) List(_ context.Context, s workflow.Scope) ([]workflow.WorkflowSummary, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []workflow.WorkflowSummary
	for _, b := range r.bucket(s) {
		wf := sgDecode(b)
		out = append(out, workflow.WorkflowSummary{Name: wf.Name, Description: wf.Description, Author: wf.Author, NodeCount: len(wf.Nodes)})
	}
	return out, nil
}

func (r *sgRepo) SaveNodePositions(_ context.Context, s workflow.Scope, name string, _ map[string]workflow.NodePosition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bucket(s)[name]; !ok {
		return workflow.ErrNotFound
	}
	r.otherWrites++
	return nil
}

func (r *sgRepo) Create(_ context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.bucket(s)
	if _, ok := b[name]; ok {
		return workflow.ErrAlreadyExists
	}
	r.creates++
	b[name] = sgEncode(wf)
	return nil
}

func (r *sgRepo) Update(_ context.Context, s workflow.Scope, name string, wf *workflow.Workflow) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.updateErr != nil {
		return r.updateErr
	}
	b := r.bucket(s)
	if _, ok := b[name]; !ok {
		return fmt.Errorf("%w: %s", workflow.ErrNotFound, name)
	}
	r.updates++
	b[name] = sgEncode(wf)
	return nil
}

func (r *sgRepo) Delete(_ context.Context, s workflow.Scope, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.bucket(s)
	if _, ok := b[name]; !ok {
		return workflow.ErrNotFound
	}
	r.otherWrites++
	delete(b, name)
	return nil
}

func (r *sgRepo) WriteFile(_ context.Context, s workflow.Scope, name, _ string, _ []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bucket(s)[name]; !ok {
		return workflow.ErrNotFound
	}
	r.otherWrites++
	return nil
}

func (r *sgRepo) DeleteFile(_ context.Context, s workflow.Scope, name, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bucket(s)[name]; !ok {
		return workflow.ErrNotFound
	}
	r.otherWrites++
	return nil
}

func (r *sgRepo) ListFiles(_ context.Context, s workflow.Scope, name string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bucket(s)[name]; !ok {
		return nil, workflow.ErrNotFound
	}
	return []string{}, nil
}

func (r *sgRepo) Exists(_ context.Context, s workflow.Scope, name string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.bucket(s)[name]
	return ok, nil
}

func (r *sgRepo) CopyTree(_ context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.bucket(src)[srcName]
	if !ok {
		return workflow.ErrNotFound
	}
	if _, exists := r.bucket(dst)[dstName]; exists && !overwrite {
		return workflow.ErrAlreadyExists
	}
	r.otherWrites++
	wf := sgDecode(b)
	wf.Name = dstName
	r.bucket(dst)[dstName] = sgEncode(wf)
	return nil
}

// sgRegistry counts Register calls on top of the in-memory fakeRegistry
// (service_registry_test.go), so a test can prove a rejected create never
// claimed the name.
type sgRegistry struct {
	*fakeRegistry
	mu        sync.Mutex
	registers int
}

func (r *sgRegistry) Register(ctx context.Context, e workflow.RegistryEntry) error {
	r.mu.Lock()
	r.registers++
	r.mu.Unlock()
	return r.fakeRegistry.Register(ctx, e)
}

func (r *sgRegistry) registerCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registers
}

// sgProfiles is an alias -> kind workflow.ProfileChecker.
type sgProfiles map[string]string

func (p sgProfiles) ProfileKind(alias string) (string, bool) {
	k, ok := p[alias]
	return k, ok
}

// sgFiles records and resolves files by owner, workflow and relative path.
type sgFileCall struct {
	owner    uuid.UUID
	workflow string
	path     string
}

type sgFiles struct {
	mu      sync.Mutex
	present map[sgFileCall]bool
	calls   []sgFileCall
}

func newSGFiles() *sgFiles { return &sgFiles{present: map[sgFileCall]bool{}} }

func (f *sgFiles) FileExists(owner uuid.UUID, workflowName, relativePath string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	call := sgFileCall{owner: owner, workflow: workflowName, path: relativePath}
	f.calls = append(f.calls, call)
	return f.present[call]
}

func (f *sgFiles) recorded() []sgFileCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sgFileCall(nil), f.calls...)
}

// sgAnyFile is an owner-blind workflow.FileChecker that reports every file present.
type sgAnyFile struct{}

func (sgAnyFile) FileExists(string, string) bool { return true }

type sgFixture struct {
	svc   *workflow.Service
	repo  *sgRepo
	reg   *sgRegistry
	files *sgFiles
	pubs  *fakePublishers
}

// newSGFixture builds a registry-backed (server-mode) Service. wire=true calls
// SetGroupCheckers with a chat profile "chatty" and an agent profile "agenty".
func newSGFixture(t *testing.T, wire bool) *sgFixture {
	t.Helper()
	repo := newSGRepo()
	pubs := newFakePublishers()
	svc := workflow.NewWorkflowService(repo, pubs)
	reg := &sgRegistry{fakeRegistry: newFakeRegistry()}
	svc.SetRegistry(reg)
	files := newSGFiles()
	if wire {
		svc.SetGroupCheckers(sgProfiles{"chatty": "chat", "agenty": "agent"}, files)
	}
	return &sgFixture{svc: svc, repo: repo, reg: reg, files: files, pubs: pubs}
}

// ---------------------------------------------------------------------------
// Workflow fixtures
// ---------------------------------------------------------------------------

func sgTransform(id string) workflow.Node {
	return workflow.Node{ID: id, Type: workflow.NodeTypeTransform, Config: workflow.NodeConfig{
		Operations: []workflow.Operation{{Variable: id + "_ran", Operation: "set", Value: true}},
	}}
}

// sgBranches is a valid two-task branches group on the entrypoint:
// fanout --task a--> a1, --task b--> b1, --success--> done, --failure--> failed.
func sgBranches(name string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name, Description: "branches group", Author: "t",
		Nodes: []workflow.Node{
			{ID: "fanout", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
				GatherMode:         workflow.GroupGatherResults,
			}}},
			sgTransform("a1"), sgTransform("b1"), sgTransform("done"), sgTransform("failed"),
		},
		Edges: []workflow.Edge{
			{Source: "fanout", Target: "a1", Output: workflow.GroupPortTask, Label: "a"},
			{Source: "fanout", Target: "b1", Output: workflow.GroupPortTask, Label: "b"},
			{Source: "fanout", Target: "done", Output: workflow.GroupPortSuccess},
			{Source: "fanout", Target: "failed", Output: workflow.GroupPortFailure},
		},
	}
}

// sgFilesGroup is sgBranches switched to file gathering with the given resolver.
func sgFilesGroup(name, resolverProfile, promptFile string) *workflow.Workflow {
	w := sgBranches(name)
	g := w.Nodes[0].Config.Group
	g.UnfinishedBehavior = workflow.GroupUnfinishedCancel
	g.GatherMode = workflow.GroupGatherResultsAndFiles
	g.ResolverProfile = resolverProfile
	g.ResolverPromptFile = promptFile
	return w
}

// sgWorkflowsGroup runs child twice (tasks a, b) with per-task inputs.
func sgWorkflowsGroup(name, child string, inputsA, inputsB map[string]any) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name, Description: "workflows group", Author: "t",
		Nodes: []workflow.Node{
			{ID: "fanout", Type: workflow.NodeTypeParallelGroup, Entrypoint: true, Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form: workflow.GroupFormWorkflows,
				Tasks: []workflow.GroupTask{
					{Key: "a", Workflow: child, Inputs: inputsA},
					{Key: "b", Workflow: child, Inputs: inputsB},
				},
				CompletionRule:     workflow.GroupRuleFirstSuccess,
				UnfinishedBehavior: workflow.GroupUnfinishedCancel,
				GatherMode:         workflow.GroupGatherResults,
			}}},
			sgTransform("done"), sgTransform("failed"),
		},
		Edges: []workflow.Edge{
			{Source: "fanout", Target: "done", Output: workflow.GroupPortSuccess},
			{Source: "fanout", Target: "failed", Output: workflow.GroupPortFailure},
		},
	}
}

var sgTopic = workflow.InputField{Name: "topic", Type: "string", Description: "what to work on", Required: true}

func sgChild(name string, inputs ...workflow.InputField) *workflow.Workflow {
	return &workflow.Workflow{
		Name: name, Description: "child", Author: "t", Inputs: inputs,
		Nodes: []workflow.Node{func() workflow.Node { n := sgTransform("c1"); n.Entrypoint = true; return n }()},
	}
}

func sgCodes(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

func sgHard(errs []workflow.ValidationError) []workflow.ValidationError {
	var out []workflow.ValidationError
	for _, e := range errs {
		if e.Severity != "warning" {
			out = append(out, e)
		}
	}
	return out
}

func requireGroupErr(t *testing.T, err error) *workflow.GroupValidationError {
	t.Helper()
	require.Error(t, err)
	var gve *workflow.GroupValidationError
	require.True(t, errors.As(err, &gve), "want *workflow.GroupValidationError, got %T: %v", err, err)
	require.True(t, errors.Is(err, workflow.ErrGroupInvalid))
	return gve
}

// ---------------------------------------------------------------------------
// ChildContracts — the P1-B producer
// ---------------------------------------------------------------------------

func TestServiceGroup_ChildContracts_PrivateThenShared(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgChild("child", sgTopic))
	f.repo.seed(workflow.Shared(), sgChild("child", workflow.InputField{Name: "shared_only", Type: "number"}))

	got, err := f.svc.ChildContracts(ctxWith(sgAlice, false)).ChildInputs("child")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "topic", got[0].Name, "the caller's private copy wins over shared")

	got, err = f.svc.ChildContracts(ctxWith(sgBob, false)).ChildInputs("child")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "shared_only", got[0].Name, "a caller with no private copy resolves the shared one")
}

func TestServiceGroup_ChildContracts_ScopedToCallerOfTheCall(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgBob), sgChild("bobs-child", sgTopic))

	_, err := f.svc.ChildContracts(ctxWith(sgAlice, false)).ChildInputs("bobs-child")
	require.Error(t, err)
	assert.True(t, errors.Is(err, workflow.ErrNotFound), "another user's private workflow is invisible, reported as not found")

	_, err = f.svc.ChildContracts(ctxWith(sgAlice, true)).ChildInputs("bobs-child")
	assert.True(t, errors.Is(err, workflow.ErrNotFound), "admin does not see other users' PRIVATE workflows either (same as Load)")

	got, err := f.svc.ChildContracts(ctxWith(sgBob, false)).ChildInputs("bobs-child")
	require.NoError(t, err)
	assert.Equal(t, []workflow.InputField{sgTopic}, got)
}

func TestServiceGroup_ChildContracts_DepartmentGatedShared(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Shared(), sgChild("dept-child", sgTopic))
	f.pubs.pub["dept-child"] = workflow.Publication{Publisher: sgBob, Visibility: workflow.VisibilityDepartment, Department: "eng"}

	_, err := f.svc.ChildContracts(ctxWithDepts(sgAlice, false, "sales")).ChildInputs("dept-child")
	assert.True(t, errors.Is(err, workflow.ErrNotFound), "non-member must not resolve a department workflow")

	got, err := f.svc.ChildContracts(ctxWithDepts(sgAlice, false, "eng")).ChildInputs("dept-child")
	require.NoError(t, err)
	assert.Equal(t, []workflow.InputField{sgTopic}, got)
}

func TestServiceGroup_ChildContracts_LookupFailureIsNotNotFound(t *testing.T) {
	f := newSGFixture(t, true)
	boom := errors.New("disk on fire")
	f.repo.loadErr["flaky"] = boom

	_, err := f.svc.ChildContracts(ctxWith(sgAlice, false)).ChildInputs("flaky")
	require.Error(t, err)
	assert.True(t, errors.Is(err, boom))
	assert.False(t, errors.Is(err, workflow.ErrNotFound), "a storage failure must not masquerade as an unknown workflow")
}

func TestServiceGroup_ChildContracts_NoIdentityIsUnauthenticated(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Shared(), sgChild("child", sgTopic))

	_, err := f.svc.ChildContracts(context.Background()).ChildInputs("child")
	require.Error(t, err)
	assert.True(t, errors.Is(err, identity.ErrUnauthenticated))
	assert.False(t, errors.Is(err, workflow.ErrNotFound))
	assert.Equal(t, 0, f.repo.loadCount("child"), "no identity => no storage read")
}

func TestServiceGroup_ChildContracts_InvalidNameIsNotFound(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.loadErr["../escape"] = fmt.Errorf("%w: ../escape", workflow.ErrInvalidName)

	_, err := f.svc.ChildContracts(ctxWith(sgAlice, false)).ChildInputs("../escape")
	assert.True(t, errors.Is(err, workflow.ErrNotFound), "an unaddressable name is reported as an unknown workflow")
}

func TestServiceGroup_ChildContracts_CachesPerReader(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgChild("child", sgTopic))
	ctx := ctxWith(sgAlice, false)

	r := f.svc.ChildContracts(ctx)
	_, err := r.ChildInputs("child")
	require.NoError(t, err)
	_, err = r.ChildInputs("child")
	require.NoError(t, err)
	assert.Equal(t, 1, f.repo.loadCount("child"), "a reader resolves each name once")

	_, err = f.svc.ChildContracts(ctx).ChildInputs("child")
	require.NoError(t, err)
	assert.Equal(t, 2, f.repo.loadCount("child"), "the cache is per call, never a stale process-wide cache")
}

func TestServiceGroup_ChildContracts_CachesFailures(t *testing.T) {
	f := newSGFixture(t, true)
	r := f.svc.ChildContracts(ctxWith(sgAlice, false))
	_, err1 := r.ChildInputs("ghost")
	_, err2 := r.ChildInputs("ghost")
	assert.True(t, errors.Is(err1, workflow.ErrNotFound))
	assert.True(t, errors.Is(err2, workflow.ErrNotFound))
	assert.Equal(t, 2, f.repo.loadCount("ghost"), "one private probe + one shared probe, then cached")
}

func TestServiceGroup_ChildContracts_ReturnsIndependentCopy(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgChild("child", sgTopic))
	r := f.svc.ChildContracts(ctxWith(sgAlice, false))

	first, err := r.ChildInputs("child")
	require.NoError(t, err)
	first[0].Name = "mutated"

	second, err := r.ChildInputs("child")
	require.NoError(t, err)
	assert.Equal(t, "topic", second[0].Name, "a caller mutating the returned slice must not poison the cache")
}

// ---------------------------------------------------------------------------
// GroupValidateOptions — what the validate endpoints append (P1-T7)
// ---------------------------------------------------------------------------

func TestServiceGroup_GroupValidateOptions_BindChildLookupToCtx(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgBob), sgChild("bobs-child", sgTopic))
	w := sgWorkflowsGroup("fan", "bobs-child", map[string]any{"topic": "x"}, map[string]any{"topic": "y"})
	profiles := sgProfiles{"agenty": "agent"}

	asAlice := workflow.Validate(w, sgAnyFile{}, profiles, nil, f.svc.GroupValidateOptions(ctxWith(sgAlice, false))...)
	assert.Contains(t, sgCodes(asAlice), "GROUP_UNKNOWN_CHILD_WORKFLOW")

	asBob := workflow.Validate(w, sgAnyFile{}, profiles, nil, f.svc.GroupValidateOptions(ctxWith(sgBob, false))...)
	assert.NotContains(t, sgCodes(asBob), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	assert.Empty(t, sgHard(asBob))
}

func TestServiceGroup_GroupValidateOptions_IncludesSpawnNameChecker(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgChild("child"))
	spawning := &workflow.Workflow{
		Name: "spawner",
		Nodes: []workflow.Node{
			{ID: "s", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{ChildWorkflow: "ghost", SpawnMode: "fire_and_forget"}},
			func() workflow.Node { n := sgTransform("start"); n.Entrypoint = true; return n }(),
		},
		Edges: []workflow.Edge{{Source: "start", Target: "s"}},
	}
	got := workflow.Validate(spawning, sgAnyFile{}, nil, nil, f.svc.GroupValidateOptions(ctxWith(sgAlice, false))...)
	assert.Contains(t, sgCodes(got), "UNKNOWN_CHILD_WORKFLOW", "the existing spawn reference check rides the same option set")

	spawning.Nodes[0].Config.ChildWorkflow = "child"
	got = workflow.Validate(spawning, sgAnyFile{}, nil, nil, f.svc.GroupValidateOptions(ctxWith(sgAlice, false))...)
	assert.NotContains(t, sgCodes(got), "UNKNOWN_CHILD_WORKFLOW")
}

func TestServiceGroup_GroupValidateOptions_RequireCheckers(t *testing.T) {
	f := newSGFixture(t, true)
	got := workflow.Validate(sgFilesGroup("files", "agenty", ""), nil, nil, nil, f.svc.GroupValidateOptions(ctxWith(sgAlice, false))...)
	assert.Contains(t, sgCodes(got), "GROUP_CHECKER_UNAVAILABLE",
		"a validate endpoint with no profile/file checker must report it, not silently skip")
}

// ---------------------------------------------------------------------------
// ValidateGroups
// ---------------------------------------------------------------------------

func TestServiceGroup_ValidateGroups_NonGroupWorkflowIsNil(t *testing.T) {
	f := newSGFixture(t, false)
	assert.Nil(t, f.svc.ValidateGroups(ctxWith(sgAlice, false), wf("plain")))
	assert.Nil(t, f.svc.ValidateGroups(ctxWith(sgAlice, false), nil))
	assert.Empty(t, f.files.recorded(), "a non-group workflow never consults the group checkers")
}

func TestServiceGroup_ValidateGroups_ReturnsOnlyGroupCodes(t *testing.T) {
	f := newSGFixture(t, true)
	w := sgBranches("mixed")
	w.Nodes[0].Config.Group.CompletionRule = "most_succeed"
	// A non-group defect (llm_call without templateFile/model_profile) that the
	// legacy write path has never gated on.
	w.Nodes = append(w.Nodes, workflow.Node{ID: "orphan-llm", Type: workflow.NodeTypeLLMCall})

	got := f.svc.ValidateGroups(ctxWith(sgAlice, false), w)
	require.NotEmpty(t, got)
	for _, e := range got {
		assert.True(t, workflow.IsGroupValidationCode(e.Code), "unexpected non-group code %s", e.Code)
	}
	assert.Contains(t, sgCodes(got), "GROUP_INVALID_RULE")
}

func TestServiceGroup_ValidateGroups_ValidGroupIsClean(t *testing.T) {
	f := newSGFixture(t, true)
	assert.Empty(t, sgHard(f.svc.ValidateGroups(ctxWith(sgAlice, false), sgBranches("ok"))))
}

// ---------------------------------------------------------------------------
// Create / Update gate
// ---------------------------------------------------------------------------

func TestServiceGroup_Create_InvalidGroupRejectedBeforeRegistryAndFolder(t *testing.T) {
	f := newSGFixture(t, true)
	w := sgBranches("bad-gather")
	w.Nodes[0].Config.Group.GatherMode = "everything"

	err := f.svc.Create(ctxWith(sgAlice, false), "bad-gather", w)
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_INVALID_GATHER")
	assert.Contains(t, err.Error(), "GROUP_INVALID_GATHER")

	assert.Equal(t, 0, f.reg.registerCount(), "registry must not be claimed")
	assert.Equal(t, 0, f.repo.writeCount(), "folder must not be written")
	assert.Nil(t, f.repo.raw(workflow.Private(sgAlice), "bad-gather"))
	_, rerr := f.reg.Resolve(context.Background(), "bad-gather")
	assert.True(t, errors.Is(rerr, workflow.ErrNotFound))
	assert.Equal(t, uuid.Nil, w.ID, "no identity is allocated for a rejected create")
}

func TestServiceGroup_Create_LocalModeRejectsBeforeFolder(t *testing.T) {
	repo := newSGRepo()
	svc := workflow.NewWorkflowService(repo, nil) // local mode: LocalRegistry Null-Object
	svc.SetGroupCheckers(sgProfiles{"agenty": "agent"}, newSGFiles())
	w := sgBranches("bad-rule")
	w.Nodes[0].Config.Group.CompletionRule = "quorum"

	err := svc.Create(ctxWith(sgAlice, false), "bad-rule", w)
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_INVALID_RULE")
	assert.Equal(t, 0, repo.writeCount())
}

func TestServiceGroup_Create_ValidGroupRegistersAndPersists(t *testing.T) {
	f := newSGFixture(t, true)
	w := sgBranches("good")

	require.NoError(t, f.svc.Create(ctxWith(sgAlice, false), "good", w))
	assert.Equal(t, 1, f.reg.registerCount())
	assert.NotEqual(t, uuid.Nil, w.ID)

	stored := f.repo.stored(workflow.Private(sgAlice), "good")
	require.NotNil(t, stored)
	require.NotNil(t, stored.Nodes[0].Config.Group)
	assert.Equal(t, *sgBranches("good").Nodes[0].Config.Group, *stored.Nodes[0].Config.Group)
	assert.Equal(t, sgBranches("good").Edges, stored.Edges)
}

func TestServiceGroup_Create_MissingCheckerWiringIsReportedNotSkipped(t *testing.T) {
	f := newSGFixture(t, false) // SetGroupCheckers never called
	f.files.present[sgFileCall{owner: sgAlice, workflow: "files", path: "prompts/resolve.md"}] = true

	err := f.svc.Create(ctxWith(sgAlice, false), "files", sgFilesGroup("files", "agenty", "prompts/resolve.md"))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_CHECKER_UNAVAILABLE")
	assert.Equal(t, 0, f.reg.registerCount())
	assert.Equal(t, 0, f.repo.writeCount())

	// Wiring the checkers is exactly what turns the same write green.
	f.svc.SetGroupCheckers(sgProfiles{"agenty": "agent"}, f.files)
	require.NoError(t, f.svc.Create(ctxWith(sgAlice, false), "files", sgFilesGroup("files", "agenty", "prompts/resolve.md")))
	assert.NotNil(t, f.repo.raw(workflow.Private(sgAlice), "files"))
}

func TestServiceGroup_SetGroupCheckers_NilUnwires(t *testing.T) {
	f := newSGFixture(t, true)
	f.svc.SetGroupCheckers(nil, nil)
	err := f.svc.Create(ctxWith(sgAlice, false), "files", sgFilesGroup("files", "agenty", ""))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_CHECKER_UNAVAILABLE")
}

func TestServiceGroup_Create_ResolverProfileKindEnforced(t *testing.T) {
	f := newSGFixture(t, true)
	ctx := ctxWith(sgAlice, false)

	err := f.svc.Create(ctx, "chat-resolver", sgFilesGroup("chat-resolver", "chatty", ""))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_RESOLVER_PROFILE_KIND")
	assert.Nil(t, f.repo.raw(workflow.Private(sgAlice), "chat-resolver"))

	err = f.svc.Create(ctx, "ghost-resolver", sgFilesGroup("ghost-resolver", "nobody", ""))
	gve = requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_UNKNOWN_RESOLVER_PROFILE")

	require.NoError(t, f.svc.Create(ctx, "agent-resolver", sgFilesGroup("agent-resolver", "agenty", "")))
	assert.NotNil(t, f.repo.raw(workflow.Private(sgAlice), "agent-resolver"))
}

func TestServiceGroup_Create_FileCheckerReceivesCallerOwner(t *testing.T) {
	f := newSGFixture(t, true)
	ctx := ctxWith(sgAlice, false)

	err := f.svc.Create(ctx, "prompted", sgFilesGroup("prompted", "agenty", "prompts/resolve.md"))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_MISSING_PROMPT_FILE")

	f.files.present[sgFileCall{owner: sgAlice, workflow: "prompted", path: "prompts/resolve.md"}] = true
	// Alice's private prompt must not satisfy Bob's write of the same manifest.
	err = f.svc.Create(ctxWith(sgBob, false), "prompted", sgFilesGroup("prompted", "agenty", "prompts/resolve.md"))
	gve = requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_MISSING_PROMPT_FILE")
	assert.Equal(t, 0, f.reg.registerCount())
	assert.Equal(t, 0, f.repo.writeCount())
	assert.Nil(t, f.repo.raw(workflow.Private(sgBob), "prompted"))

	require.NoError(t, f.svc.Create(ctx, "prompted", sgFilesGroup("prompted", "agenty", "prompts/resolve.md")))
	stored := f.repo.stored(workflow.Private(sgAlice), "prompted")
	require.NotNil(t, stored)
	assert.Equal(t, "prompts/resolve.md", stored.Nodes[0].Config.Group.ResolverPromptFile)

	assert.Equal(t, []sgFileCall{
		{owner: sgAlice, workflow: "prompted", path: "prompts/resolve.md"},
		{owner: sgBob, workflow: "prompted", path: "prompts/resolve.md"},
		{owner: sgAlice, workflow: "prompted", path: "prompts/resolve.md"},
	}, f.files.recorded(), "every prompt check must be bound to that call's owner")
}

func TestServiceGroup_Create_InvisibleChildRejected(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgBob), sgChild("secret-child", sgTopic))
	inA, inB := map[string]any{"topic": "x"}, map[string]any{"topic": "y"}

	err := f.svc.Create(ctxWith(sgAlice, false), "fan", sgWorkflowsGroup("fan", "secret-child", inA, inB))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")
	assert.Equal(t, 0, f.reg.registerCount())
	assert.Equal(t, 0, f.repo.writeCount())

	require.NoError(t, f.svc.Create(ctxWith(sgBob, false), "fan", sgWorkflowsGroup("fan", "secret-child", inA, inB)),
		"the owner of the child sees it, so the same definition is valid for them")
}

func TestServiceGroup_Create_DepartmentChildVisibilityFollowsCaller(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Shared(), sgChild("dept-child", sgTopic))
	f.pubs.pub["dept-child"] = workflow.Publication{Publisher: sgBob, Visibility: workflow.VisibilityDepartment, Department: "eng"}
	inA, inB := map[string]any{"topic": "x"}, map[string]any{"topic": "y"}

	err := f.svc.Create(ctxWithDepts(sgAlice, false, "sales"), "fan", sgWorkflowsGroup("fan", "dept-child", inA, inB))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_UNKNOWN_CHILD_WORKFLOW")

	require.NoError(t, f.svc.Create(ctxWithDepts(sgAlice, false, "eng"), "fan", sgWorkflowsGroup("fan", "dept-child", inA, inB)))
}

func TestServiceGroup_Create_ChildLookupFailureRejected(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.loadErr["flaky-child"] = errors.New("disk on fire")

	err := f.svc.Create(ctxWith(sgAlice, false), "fan",
		sgWorkflowsGroup("fan", "flaky-child", map[string]any{"topic": "x"}, map[string]any{"topic": "y"}))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_CHECKER_UNAVAILABLE",
		"a failed contract lookup is reported, never treated as 'no inputs to check'")
	assert.Equal(t, 0, f.repo.writeCount())
}

func TestServiceGroup_Create_UnknownExplicitChildInputRejected(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgChild("child", sgTopic))

	err := f.svc.Create(ctxWith(sgAlice, false), "fan",
		sgWorkflowsGroup("fan", "child", map[string]any{"topic": "x", "tpoic": "typo"}, map[string]any{"topic": "y"}))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_UNKNOWN_CHILD_INPUT")
}

func TestServiceGroup_Create_RequiredInputAdvisoryDoesNotGate(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgChild("child", sgTopic))
	ctx := ctxWith(sgAlice, false)
	w := sgWorkflowsGroup("fan", "child", nil, nil) // "topic" left to the group-start variables

	require.NoError(t, f.svc.Create(ctx, "fan", w), "an advisory must never block a write")
	found := f.svc.ValidateGroups(ctx, f.repo.stored(workflow.Private(sgAlice), "fan"))
	require.NotEmpty(t, found)
	for _, e := range found {
		if e.Code == "GROUP_REQUIRED_INPUT_RUNTIME" {
			assert.Equal(t, "warning", e.Severity)
			return
		}
	}
	t.Fatalf("expected a GROUP_REQUIRED_INPUT_RUNTIME advisory, got %v", sgCodes(found))
}

func TestServiceGroup_Create_NoIdentityStillUnauthenticated(t *testing.T) {
	f := newSGFixture(t, true)
	err := f.svc.Create(context.Background(), "good", sgBranches("good"))
	assert.True(t, errors.Is(err, identity.ErrUnauthenticated))
	assert.Equal(t, 0, f.repo.writeCount())
}

func TestServiceGroup_Update_InvalidGroupRejectedBeforeWrite(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgBranches("wf"))
	before := f.repo.raw(workflow.Private(sgAlice), "wf")

	bad := sgBranches("wf")
	bad.Nodes[0].Config.Group.UnfinishedBehavior = workflow.GroupUnfinishedFinishIndependently // illegal with all_succeed
	err := f.svc.Update(ctxWith(sgAlice, false), "wf", bad)
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_INVALID_UNFINISHED")
	assert.Equal(t, 0, f.repo.writeCount())
	assert.Equal(t, before, f.repo.raw(workflow.Private(sgAlice), "wf"), "stored definition is untouched")
}

func TestServiceGroup_Update_ValidGroupPersists(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgBranches("wf"))
	next := sgBranches("wf")
	next.Nodes[0].Config.Group.CompletionRule = workflow.GroupRuleFirstSuccess
	next.Nodes[0].Config.Group.UnfinishedBehavior = workflow.GroupUnfinishedFinishIndependently

	require.NoError(t, f.svc.Update(ctxWith(sgAlice, false), "wf", next))
	got := f.repo.stored(workflow.Private(sgAlice), "wf")
	assert.Equal(t, workflow.GroupRuleFirstSuccess, got.Nodes[0].Config.Group.CompletionRule)
	assert.Equal(t, 1, f.repo.updates)
}

func TestServiceGroup_Update_AdminSharedEditIsGatedToo(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Shared(), sgBranches("shared-wf"))
	before := f.repo.raw(workflow.Shared(), "shared-wf")

	bad := sgBranches("shared-wf")
	bad.Nodes[0].Config.Group.Tasks = nil
	err := f.svc.Update(ctxWith(sgAlice, true), "shared-wf", bad)
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_NO_TASKS")
	assert.Equal(t, before, f.repo.raw(workflow.Shared(), "shared-wf"))

	good := sgBranches("shared-wf")
	good.Description = "admin edited"
	require.NoError(t, f.svc.Update(ctxWith(sgAlice, true), "shared-wf", good))
	assert.Equal(t, "admin edited", f.repo.stored(workflow.Shared(), "shared-wf").Description)
}

// An admin's in-place shared edit is run by OTHER users, whose resolver reads
// the prompt from their own scope then shared (engine.LoadGroupResolverPrompt
// with the run owner). A file only the admin's private partition holds must not
// satisfy the gate; the check is bound to the shared scope (uuid.Nil) only.
func TestServiceGroup_Update_AdminSharedEditChecksPromptInSharedScope(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Shared(), sgBranches("shared-wf"))
	before := f.repo.raw(workflow.Shared(), "shared-wf")
	admin := ctxWith(sgAlice, true)
	f.files.present[sgFileCall{owner: sgAlice, workflow: "shared-wf", path: "prompts/resolve.md"}] = true

	err := f.svc.Update(admin, "shared-wf", sgFilesGroup("shared-wf", "agenty", "prompts/resolve.md"))
	gve := requireGroupErr(t, err)
	assert.Contains(t, sgCodes(gve.Errors), "GROUP_MISSING_PROMPT_FILE")
	assert.Equal(t, before, f.repo.raw(workflow.Shared(), "shared-wf"))
	assert.Equal(t, []sgFileCall{{owner: uuid.Nil, workflow: "shared-wf", path: "prompts/resolve.md"}}, f.files.recorded())

	f.files.present[sgFileCall{owner: uuid.Nil, workflow: "shared-wf", path: "prompts/resolve.md"}] = true
	require.NoError(t, f.svc.Update(admin, "shared-wf", sgFilesGroup("shared-wf", "agenty", "prompts/resolve.md")))
	assert.Equal(t, "prompts/resolve.md", f.repo.stored(workflow.Shared(), "shared-wf").Nodes[0].Config.Group.ResolverPromptFile)

	// A private copy is what the admin's own write (and run) targets: caller scope.
	f.repo.seed(workflow.Private(sgAlice), sgBranches("shared-wf"))
	require.NoError(t, f.svc.Update(admin, "shared-wf", sgFilesGroup("shared-wf", "agenty", "prompts/resolve.md")))
	assert.Equal(t, sgFileCall{owner: sgAlice, workflow: "shared-wf", path: "prompts/resolve.md"}, f.files.recorded()[2])
}

func TestServiceGroup_Update_RepositoryFailureSurfaces(t *testing.T) {
	f := newSGFixture(t, true)
	f.repo.seed(workflow.Private(sgAlice), sgBranches("wf"))
	f.repo.updateErr = errors.New("disk full")

	err := f.svc.Update(ctxWith(sgAlice, false), "wf", sgBranches("wf"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disk full")
	assert.False(t, errors.Is(err, workflow.ErrGroupInvalid))
}

// ---------------------------------------------------------------------------
// Legacy compatibility: non-group writes are unchanged
// ---------------------------------------------------------------------------

func TestServiceGroup_NonGroupWritesUnchanged(t *testing.T) {
	f := newSGFixture(t, false) // no group checkers at all
	ctx := ctxWith(sgAlice, false)

	// Create has never validated non-group definitions: even a structurally
	// invalid one (no entrypoint, llm_call without templateFile) is written.
	legacy := &workflow.Workflow{Name: "legacy", Nodes: []workflow.Node{{ID: "x", Type: workflow.NodeTypeLLMCall}}}
	require.NoError(t, f.svc.Create(ctx, "legacy", legacy))
	assert.Equal(t, 1, f.reg.registerCount())
	assert.Equal(t, 1, f.repo.creates)
	assert.Equal(t, 0, f.repo.loadCount("legacy"), "the non-group create path performs no extra reads")
	assert.Empty(t, f.files.recorded())

	legacy.Description = "edited"
	require.NoError(t, f.svc.Update(ctx, "legacy", legacy))
	assert.Equal(t, 1, f.repo.updates)
	assert.Equal(t, "edited", f.repo.stored(workflow.Private(sgAlice), "legacy").Description)
}

// ---------------------------------------------------------------------------
// GroupValidationError
// ---------------------------------------------------------------------------

func TestServiceGroup_GroupValidationError_Shape(t *testing.T) {
	e := &workflow.GroupValidationError{Errors: []workflow.ValidationError{
		{Code: "GROUP_INVALID_RULE", NodeID: "g1"},
		{Code: "GROUP_DUPLICATE_TASK_KEY", NodeID: "g1", TaskKey: "a"},
		{Code: "GROUP_REQUIRED_INPUT_RUNTIME", NodeID: "g2", TaskKey: "b", Severity: "warning"},
	}}
	msg := e.Error()
	assert.Contains(t, msg, "parallel group definition is invalid")
	assert.Contains(t, msg, "GROUP_INVALID_RULE (node g1)")
	assert.Contains(t, msg, "GROUP_DUPLICATE_TASK_KEY (node g1, task a)")
	assert.Contains(t, msg, "GROUP_REQUIRED_INPUT_RUNTIME (node g2, task b, warning)")
	assert.True(t, errors.Is(e, workflow.ErrGroupInvalid))
	assert.False(t, errors.Is(e, workflow.ErrNotFound))

	wrapped := fmt.Errorf("upsert: %w", e)
	var back *workflow.GroupValidationError
	require.True(t, errors.As(wrapped, &back))
	assert.Len(t, back.Errors, 3)
	assert.Equal(t, "parallel group definition is invalid", (&workflow.GroupValidationError{}).Error())
}

// CopyTreeStamped satisfies the stamped-copy port: copy, then stamp the staged
// manifest and persist it. Group tests run without a revision stamper, so this
// only keeps sgRepo a complete workflow.Repository.
func (r *sgRepo) CopyTreeStamped(ctx context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool, stamp func(*workflow.Workflow) error) error {
	if err := r.CopyTree(ctx, src, srcName, dst, dstName, overwrite); err != nil {
		return err
	}
	wf, err := r.Load(ctx, dst, dstName)
	if err != nil {
		return err
	}
	if err := stamp(wf); err != nil {
		return err
	}
	return r.Update(ctx, dst, dstName, wf)
}
