package engine_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSnapshotter records the aliases it was asked for and returns a fixed
// alias->model map, or err. Missing fixture aliases stay missing so capture
// validation cannot succeed without the requested models.
type fakeSnapshotter struct {
	mu      sync.Mutex
	calls   [][]string
	pins    map[string]string
	routes  map[string]string
	conns   map[string]string
	err     error
	ctxSeen context.Context
}

func (f *fakeSnapshotter) SnapshotModels(ctx context.Context, aliases []string, _ *workflow.Workflow) (provider.ModelPinSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxSeen = ctx
	cp := append([]string(nil), aliases...)
	f.calls = append(f.calls, cp)
	if f.err != nil {
		return provider.ModelPinSnapshot{}, f.err
	}
	out := map[string]string{}
	for _, a := range aliases {
		if m, ok := f.pins[a]; ok {
			out[a] = m
		}
	}
	return provider.ModelPinSnapshot{Models: out, OpaqueRoutes: f.routes, OpaqueConnections: f.conns}, nil
}

// echoSnapshotter pins every requested alias to a deterministic model so engine
// fixtures that are not about pinning still satisfy the trigger-time capture.
type echoSnapshotter struct{}

func (echoSnapshotter) SnapshotModels(_ context.Context, aliases []string, _ *workflow.Workflow) (provider.ModelPinSnapshot, error) {
	out := make(map[string]string, len(aliases))
	for _, a := range aliases {
		out[a] = "pinned-" + a
	}
	return provider.ModelPinSnapshot{Models: out}, nil
}

// wireModelPins gives a fixture engine the snapshotter and gate production
// always injects, so Trigger can capture pins.
func wireModelPins(eng *engine.Engine) *engine.Engine {
	eng.SetModelSnapshotter(echoSnapshotter{})
	eng.SetModelPinGate(provider.NewExecutionGate())
	return eng
}

func aliasWf(name string, aliases ...string) *workflow.Workflow {
	wf := &workflow.Workflow{Name: name}
	for i, a := range aliases {
		n := workflow.Node{ID: string(rune('A' + i)), Type: workflow.NodeTypeLLMCall, Entrypoint: i == 0}
		n.Config.ModelProfile = a
		wf.Nodes = append(wf.Nodes, n)
	}
	return wf
}

// Every non-empty alias on a model-consuming node (llm, agent, interactive,
// spawn input preparer) is captured once, de-duplicated, and persisted with version 1
// before the run is enqueued.
func TestTriggerPinsCoherentGeneration(t *testing.T) {
	h := newTriggerHarness()
	snap := &fakeSnapshotter{pins: map[string]string{"fast": "claude-haiku-4-5-20251001", "deep": "claude-opus-4-1"}}
	h.engine.SetModelSnapshotter(snap)
	h.engine.SetModelPinGate(&recordingPinGate{})
	caller := uuid.New()
	wf := aliasWf("wf", "fast", "deep", "fast", "")
	wf.Nodes[3].Type = workflow.NodeTypeScript // no alias
	h.repo.put(workflow.Private(caller), wf)

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "wf", nil, "normal", "")
	require.NoError(t, err)

	require.Len(t, snap.calls, 1, "one snapshot generation per trigger")
	got := snap.calls[0]
	sort.Strings(got)
	assert.Equal(t, []string{"deep", "fast"}, got)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion)
	assert.Equal(t, map[string]string{"fast": "claude-haiku-4-5-20251001", "deep": "claude-opus-4-1"}, inst.ModelPins)
}

// Only the five node types that resolve a model_profile at run time pin an
// alias (llm_call, agent_call, interactive_llm, interactive_agent and the
// spawn_workflow input preparer). A stale model_profile on any other node type
// is ignored by the executors and by path_inventory.addConsumers, so it must
// neither be requested from the snapshotter nor refuse the trigger.
func TestTriggerPinsOnlyModelConsumingNodeTypes(t *testing.T) {
	consumers := []workflow.NodeType{
		workflow.NodeTypeLLMCall, workflow.NodeTypeAgentCall,
		workflow.NodeTypeInteractiveLLM, workflow.NodeTypeInteractiveAgent,
		workflow.NodeTypeSpawnWorkflow,
	}
	ignored := []workflow.NodeType{
		workflow.NodeTypeScript, workflow.NodeTypeToolCall, workflow.NodeTypeConditional,
		workflow.NodeTypeTransform, workflow.NodeTypeTimerDelay, workflow.NodeTypeWebhookWait,
	}

	h := newTriggerHarness()
	pins := map[string]string{}
	wf := &workflow.Workflow{Name: "mixed"}
	var want []string
	for i, typ := range consumers {
		alias := "used-" + string(typ)
		pins[alias] = "model-" + alias
		want = append(want, alias)
		n := workflow.Node{ID: "c" + string(rune('a'+i)), Type: typ, Entrypoint: i == 0}
		n.Config.ModelProfile = alias
		wf.Nodes = append(wf.Nodes, n)
	}
	for i, typ := range ignored {
		n := workflow.Node{ID: "i" + string(rune('a'+i)), Type: typ}
		n.Config.ModelProfile = "unused-unknown-profile"
		wf.Nodes = append(wf.Nodes, n)
	}
	sort.Strings(want)

	snap := &fakeSnapshotter{pins: pins}
	h.engine.SetModelSnapshotter(snap)
	h.engine.SetModelPinGate(&recordingPinGate{})
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), wf)

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "mixed", nil, "normal", "")
	require.NoError(t, err)

	require.Len(t, snap.calls, 1)
	got := snap.calls[0]
	sort.Strings(got)
	assert.Equal(t, want, got, "aliases on non-consuming node types must not be requested")

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, pins, inst.ModelPins)
}

// Exception metadata is owned by the instance and validated before Create.
func TestTriggerSnapshotRoutesOwnedAndValidated(t *testing.T) {
	for name, tc := range map[string]struct{ routes, conns map[string]string }{
		"valid":                    {map[string]string{"fast": "azure"}, map[string]string{"fast": "conn-a"}},
		"unknown alias":            {map[string]string{"other": "azure"}, map[string]string{"other": "conn-a"}},
		"unknown family":           {map[string]string{"fast": "openai"}, map[string]string{"fast": "conn-a"}},
		"noncanonical":             {map[string]string{"fast": "azure-openai"}, map[string]string{"fast": "conn-a"}},
		"empty family":             {map[string]string{"fast": ""}, map[string]string{"fast": "conn-a"}},
		"missing connection":       {map[string]string{"fast": "azure"}, nil},
		"connection without pin":   {map[string]string{"fast": "azure"}, map[string]string{"fast": "conn-a", "slow": "conn-b"}},
		"connection without route": {nil, map[string]string{"fast": "conn-a"}},
	} {
		routes := tc.routes
		t.Run(name, func(t *testing.T) {
			h := newTriggerHarness()
			snap := &fakeSnapshotter{pins: map[string]string{"fast": "Deployment:ID"}, routes: routes, conns: tc.conns}
			h.engine.SetModelSnapshotter(snap)
			h.engine.SetModelPinGate(&recordingPinGate{})
			caller := uuid.New()
			h.repo.put(workflow.Private(caller), aliasWf("wf", "fast"))
			id, started, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "wf", nil, "normal", "")
			if name != "valid" {
				require.Error(t, err)
				assert.False(t, started)
				assert.Empty(t, h.instances.instances)
				assert.Equal(t, uuid.Nil, h.enqueuedOwner)
				return
			}
			require.NoError(t, err)
			routes["fast"] = "bedrock"
			tc.conns["fast"] = "conn-b"
			saved, err := h.instances.Get(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"fast": "Deployment:ID"}, saved.ModelPins)
			assert.Equal(t, map[string]string{"fast": "azure"}, saved.ModelPinRoutes)
			assert.Equal(t, map[string]string{"fast": "conn-a"}, saved.ModelPinRouteConnections)
		})
	}
}

// A workflow with no alias persists an empty non-nil map at version 1, not
// the legacy NULL/0 record.
func TestTriggerNoAliasPersistsEmptySnapshot(t *testing.T) {
	h := newTriggerHarness()
	h.engine.SetModelSnapshotter(&fakeSnapshotter{})
	h.engine.SetModelPinGate(&recordingPinGate{})
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), sharedWf("plain"))

	id, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "plain", nil, "normal", "")
	require.NoError(t, err)

	inst, err := h.instances.Get(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, instance.ModelPinsVersionSnapshot, inst.ModelPinsVersion)
	require.NotNil(t, inst.ModelPins)
	assert.Empty(t, inst.ModelPins)
}

// A snapshotter failure (unknown alias, mutable alias, repository fault) fails
// the trigger before Create: nothing is persisted and nothing is enqueued.
func TestTriggerSnapshotFailureCreatesNothing(t *testing.T) {
	h := newTriggerHarness()
	boom := errors.New("alias \"sonnet\" names no fixed version; set the profile to a fixed model version")
	h.engine.SetModelSnapshotter(&fakeSnapshotter{err: boom})
	h.engine.SetModelPinGate(&recordingPinGate{})
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), aliasWf("wf", "sonnet"))

	_, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "wf", nil, "normal", "")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, uuid.Nil, h.enqueuedOwner, "no enqueue after a failed snapshot")
	assert.Zero(t, len(h.instances.instances), "no instance row after a failed snapshot")
}

type createFaultInstanceRepo struct {
	*mockInstanceRepo
	err      error
	attempts []*instance.Instance
}

func (r *createFaultInstanceRepo) Create(ctx context.Context, inst *instance.Instance) error {
	r.attempts = append(r.attempts, inst)
	if r.err != nil {
		return r.err
	}
	return r.mockInstanceRepo.Create(ctx, inst)
}

func TestTriggerCreateFailureCreatesNothing(t *testing.T) {
	for _, fault := range []struct {
		name string
		err  error
	}{
		{"storage", errors.New("instance insert failed")},
		{"cancelled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
	} {
		t.Run(fault.name, func(t *testing.T) {
			repo := newScopeAwareRepo()
			instances := &createFaultInstanceRepo{mockInstanceRepo: newMockInstanceRepo(), err: fault.err}
			bus := newMockEventBus()
			qm := newMockQueueManager()
			snap := &fakeSnapshotter{pins: map[string]string{"chat-default": "claude-opus-4-1"}}
			fr := newMockFileReader()
			fr.files["wf/prompt.txt"] = []byte("hello")
			providerCalls := 0
			chat := &mockChatProvider{callFn: func(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
				providerCalls++
				return &engine.ChatResponse{Output: "ok", Model: req.Model}, nil
			}}
			eng := engine.NewEngine(repo, instances, newMockNodeExecutionRepo(), newMockSnapshotRepo(), newMockUserRepo(),
				map[workflow.NodeType]engine.NodeExecutor{
					workflow.NodeTypeLLMCall: engine.NewLLMCallExecutor(fr, chatSelector(chat), chatResolver("claude-opus-4-1")),
				}, bus, qm, &mockQueueRepo{instRepo: instances.mockInstanceRepo}, "")
			t.Cleanup(eng.Shutdown)
			eng.SetModelSnapshotter(snap)
			eng.SetModelPinGate(provider.NewExecutionGate())
			enqueues := 0
			qm.enqueueFn = func(ctx context.Context, id, _ uuid.UUID) (bool, error) {
				enqueues++
				err := eng.StartInstance(ctx, id)
				return err == nil, err
			}
			caller := uuid.New()
			ctx := ctxDepts(caller)
			wf := aliasWf("wf", "chat-default")
			wf.Nodes[0].Config.TemplateFile = "prompt.txt"
			repo.put(workflow.Private(caller), wf)

			id, started, err := eng.TriggerWithDepartment(ctx, "wf", nil, "normal", "")
			eng.Shutdown()
			assert.ErrorIs(t, err, fault.err)
			assert.Equal(t, uuid.Nil, id)
			assert.False(t, started)
			require.Len(t, instances.attempts, 1, "the fault must reach Create after a successful pin capture")
			assert.Equal(t, instance.ModelPinsVersionSnapshot, instances.attempts[0].ModelPinsVersion)
			assert.Equal(t, snap.pins, instances.attempts[0].ModelPins)
			assert.Empty(t, instances.instances)
			assert.Zero(t, enqueues, "Create must commit before enqueue")
			assert.Empty(t, bus.events, "a refused Create must not announce an instance")
			assert.Zero(t, providerCalls)

			// Clearing a transient persistence fault allows a fresh trigger through
			// the same enqueue path to execute, so its provider assertion is fed.
			instances.err = nil
			id, started, err = eng.TriggerWithDepartment(ctx, "wf", nil, "normal", "")
			require.NoError(t, err)
			eng.Shutdown()
			assert.True(t, started)
			assert.NotEqual(t, instances.attempts[0].ID, id)
			assert.Len(t, snap.calls, 2)
			assert.Len(t, instances.instances, 1)
			assert.Equal(t, 1, enqueues)
			assert.Equal(t, 1, providerCalls)
			assert.Len(t, bus.eventsOfType(id, "instance.created"), 1)
			stored, err := instances.Get(ctx, id)
			require.NoError(t, err)
			assert.Equal(t, instance.StateCompleted, stored.State)
		})
	}
}

// A snapshotter that omits a requested alias is a fault, not a legacy run:
// the engine must not persist a partial map.
func TestTriggerIncompleteSnapshotFails(t *testing.T) {
	h := newTriggerHarness()
	h.engine.SetModelSnapshotter(&fakeSnapshotter{pins: map[string]string{"fast": "m-1"}})
	h.engine.SetModelPinGate(&recordingPinGate{})
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), aliasWf("wf", "fast", "deep"))

	_, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "wf", nil, "normal", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deep")
	assert.Zero(t, len(h.instances.instances))
}

// Missing trigger dependencies must not manufacture a new legacy run, even
// when the workflow has no model aliases.
func TestTriggerCannotCreateNewLegacyRun(t *testing.T) {
	for _, aliases := range [][]string{nil, {"fast"}} {
		for _, missing := range []string{"snapshotter", "gate", "both"} {
			t.Run(missing+"/"+strings.Join(aliases, ","), func(t *testing.T) {
				h := newBareTriggerHarness()
				snap := &fakeSnapshotter{pins: map[string]string{"fast": "claude-opus-4-1"}}
				if missing == "gate" {
					h.engine.SetModelSnapshotter(snap)
				}
				if missing == "snapshotter" {
					h.engine.SetModelPinGate(&recordingPinGate{})
				}
				caller := uuid.New()
				wf := sharedWf("wf")
				if len(aliases) > 0 {
					wf = aliasWf("wf", aliases...)
				}
				h.repo.put(workflow.Private(caller), wf)

				id, started, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "wf", nil, "normal", "")
				require.Error(t, err)
				if missing != "both" {
					assert.Contains(t, err.Error(), missing)
				}
				assert.Equal(t, uuid.Nil, id)
				assert.False(t, started)
				assert.Empty(t, h.instances.instances, "no row without a complete pin capture")
				assert.Equal(t, uuid.Nil, h.enqueuedOwner, "no enqueue without a complete pin capture")
				assert.Empty(t, snap.calls, "no snapshot outside the shared lease")
			})
		}
	}
}

// A terminal transition (and the queue's child-terminal hook, which is the same
// function) releases the run's cached pins; a root run is released too.
func TestOnChildTerminalReleasesPinCache(t *testing.T) {
	h := newTriggerHarness()
	var released []uuid.UUID
	h.engine.SetPinCacheRelease(func(id uuid.UUID) { released = append(released, id) })
	id := uuid.New()
	h.engine.OnChildTerminal(context.Background(), id)
	assert.Equal(t, []uuid.UUID{id}, released)
}

func TestSetPinCacheReleaseRejectsNil(t *testing.T) {
	h := newTriggerHarness()
	assert.Panics(t, func() { h.engine.SetPinCacheRelease(nil) })
}

func TestSetModelSnapshotterRejectsNil(t *testing.T) {
	for name, snapshotter := range map[string]provider.ModelSnapshotter{
		"nil":       nil,
		"typed nil": (*fakeSnapshotter)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			h := newTriggerHarness()
			require.PanicsWithValue(t, "engine: SetModelSnapshotter requires a non-nil snapshotter", func() {
				h.engine.SetModelSnapshotter(snapshotter)
			})
			// A refused replacement must preserve the working capture dependency.
			caller := uuid.New()
			h.repo.put(workflow.Private(caller), aliasWf("wf", "fast"))
			id, _, err := h.engine.Trigger(ctxDepts(caller), "wf", nil, "normal")
			require.NoError(t, err)
			stored, err := h.instances.Get(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"fast": "pinned-fast"}, stored.ModelPins)
		})
	}
}

// recordingPinGate counts leases and reports whether the snapshotter ran while
// the lease was held, so the test fails if the capture happens outside it.
type recordingPinGate struct {
	held   bool
	leases int
	err    error
}

func (g *recordingPinGate) WithRead(ctx context.Context, fn func(context.Context) error) error {
	if g.err != nil {
		return g.err
	}
	g.leases++
	g.held = true
	defer func() { g.held = false }()
	return fn(ctx)
}

type leaseCheckingSnapshotter struct {
	gate     *recordingPinGate
	heldSeen bool
	pins     map[string]string
}

func (s *leaseCheckingSnapshotter) SnapshotModels(_ context.Context, aliases []string, _ *workflow.Workflow) (provider.ModelPinSnapshot, error) {
	s.heldSeen = s.gate.held
	return provider.ModelPinSnapshot{Models: s.pins}, nil
}

// The trigger-time snapshot is captured under the shared execution-gate read
// lease, so a concurrent advancement/mutation cannot interleave with it.
func TestTriggerCapturesPinsUnderGateLease(t *testing.T) {
	h := newTriggerHarness()
	gate := &recordingPinGate{}
	snap := &leaseCheckingSnapshotter{gate: gate, pins: map[string]string{"fast": "m-1"}}
	h.engine.SetModelSnapshotter(snap)
	h.engine.SetModelPinGate(gate)
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), aliasWf("wf", "fast"))

	_, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "wf", nil, "normal", "")
	require.NoError(t, err)
	assert.Equal(t, 1, gate.leases)
	assert.True(t, snap.heldSeen, "capture must run inside the lease")
}

// A gate that cannot grant the lease fails the trigger: no snapshot call, no
// row, no enqueue.
func TestTriggerGateFailureCreatesNothing(t *testing.T) {
	h := newTriggerHarness()
	boom := errors.New("gate unavailable")
	snap := &fakeSnapshotter{pins: map[string]string{"fast": "m-1"}}
	h.engine.SetModelSnapshotter(snap)
	h.engine.SetModelPinGate(&recordingPinGate{err: boom})
	caller := uuid.New()
	h.repo.put(workflow.Private(caller), aliasWf("wf", "fast"))

	_, _, err := h.engine.TriggerWithDepartment(ctxDepts(caller), "wf", nil, "normal", "")
	require.ErrorIs(t, err, boom)
	assert.Empty(t, snap.calls, "snapshotter must not run without the lease")
	assert.Equal(t, uuid.Nil, h.enqueuedOwner)
	assert.Zero(t, len(h.instances.instances))
}

func TestSetModelPinGateRejectsNil(t *testing.T) {
	for name, gate := range map[string]engine.ModelPinGate{
		"nil":       nil,
		"typed nil": (*recordingPinGate)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			h := newTriggerHarness()
			assert.PanicsWithValue(t, "engine: SetModelPinGate requires a non-nil gate", func() {
				h.engine.SetModelPinGate(gate)
			})
		})
	}
}
