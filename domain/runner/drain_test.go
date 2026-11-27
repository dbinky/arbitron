// domain/runner/drain_test.go
package runner_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type drainPull struct {
	Run        uuid.UUID
	Target     runner.RunnerID
	Ref        runner.CheckpointRef
	Resource   engine.ResourceFenceRef
	HomeAtPull runner.RunnerID
}

// drainPuller stands in for the WorkspacePullCoordinator: it records what the
// controller asked for and where the home was at that moment.
type drainPuller struct {
	mu     sync.Mutex
	homes  runner.HomeStore
	calls  []drainPull
	fail   error
	tamper func(*runner.PullReceipt)
}

func (p *drainPuller) PullCommitted(ctx context.Context, run uuid.UUID, target runner.RunnerID, ref runner.CheckpointRef, res engine.ResourceFenceRef) (runner.PullReceipt, error) {
	h, err := p.homes.GetHome(ctx, run)
	if err != nil {
		return runner.PullReceipt{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, drainPull{Run: run, Target: target, Ref: ref, Resource: res, HomeAtPull: h.RunnerID})
	if p.fail != nil {
		return runner.PullReceipt{}, p.fail
	}
	rc := runner.PullReceipt{OperationID: "pull-" + run.String(), RunnerID: target, Ref: ref, Resource: res}
	if p.tamper != nil {
		p.tamper(&rc)
	}
	return rc, nil
}

func (p *drainPuller) pulls() []drainPull {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]drainPull(nil), p.calls...)
}

type drainSessions struct {
	mu  sync.Mutex
	n   map[runner.RunnerID]int
	err error
}

func (s *drainSessions) CountPinned(_ context.Context, id runner.RunnerID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n[id], s.err
}

type drainCheckpoints struct {
	mu   sync.Mutex
	refs map[uuid.UUID]runner.CheckpointRef
}

func (c *drainCheckpoints) Latest(_ context.Context, run uuid.UUID) (runner.CheckpointRef, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.refs[run]
	return r, ok, nil
}

type drainRig struct {
	pool             *hubtest.Pool
	hub              *runner.Hub
	leases           *adaptsqlite.ResourceLeaseRepo
	repo             *adaptsqlite.WorkspaceHomeRepo
	homes            runner.HomeStore
	store            *adaptsqlite.DrainItemRepo
	puller           *drainPuller
	sessions         *drainSessions
	cps              *drainCheckpoints
	ctrl             *runner.DrainController
	keys             map[runner.RunnerID]runner.RunnerKey
	conns            map[runner.RunnerID]*hubtest.FakeConn
	sessionsByRunner map[runner.RunnerID]*runner.Session
}

func newDrainRig(t *testing.T) *drainRig {
	t.Helper()
	p := hubtest.NewPool(t)
	r := &drainRig{pool: p, hub: p.NewHub(hubtest.HubConfig()), leases: adaptsqlite.NewResourceLeaseRepo(p.DB, "boot-test"),
		repo: adaptsqlite.NewWorkspaceHomeRepo(p.DB), store: adaptsqlite.NewDrainItemRepo(p.DB),
		sessions: &drainSessions{n: map[runner.RunnerID]int{}}, cps: &drainCheckpoints{refs: map[uuid.UUID]runner.CheckpointRef{}},
		keys: map[runner.RunnerID]runner.RunnerKey{}, conns: map[runner.RunnerID]*hubtest.FakeConn{}, sessionsByRunner: map[runner.RunnerID]*runner.Session{}}
	for _, id := range []runner.RunnerID{"mac-1", "mac-2"} {
		r.keys[id] = p.SeedRunner(t, id, 2)
	}
	keeper, err := runner.NewCopyKeeper(runner.CopyKeeperDeps{Inventory: adaptsqlite.NewRunnerCopyRepo(p.DB), Homes: r.repo, Deleter: r.hub, Leases: r.leases})
	require.NoError(t, err)
	r.hub.SetCopyReconciler(keeper)
	r.homes = runner.NewCopyTrackingHomes(r.repo, keeper)
	r.puller = &drainPuller{homes: r.repo}
	r.ctrl, err = runner.NewDrainController(runner.DrainDeps{Pulls: r.puller, Sessions: r.sessions, Leases: r.leases,
		Registry: p.Registry, Ledger: p.Ledger, Requirements: p.Ledger, Checkpoints: r.cps, Telemetry: r.hub,
		Homes: r.homes, Store: r.store, Events: p.Events, Notifier: p.Notifier, Clock: p.Clock.Now, Interval: time.Hour})
	require.NoError(t, err)
	return r
}

func drainAgentCapability() runner.ExecutionCapability {
	binding := hubtest.ChatJob("claude-opus-5-5", runner.OpAgent).Binding
	binding.Backend = "claude-code"
	capability := hubtest.Capability(runner.OpAgent, binding, "anthropic")
	capability.ToolPolicy, capability.Tools = runner.ToolPolicyAllowlist, []string{"Read"}
	return capability
}

func (r *drainRig) online(t *testing.T, id runner.RunnerID) {
	t.Helper()
	conn := hubtest.NewFakeConn()
	source := hubtest.AnthropicSource()
	source.Capabilities = append(source.Capabilities, drainAgentCapability())
	session, err := r.hub.Admit(context.Background(), r.keys[id], conn, hubtest.DarwinHello(source))
	require.NoError(t, err)
	r.conns[id], r.sessionsByRunner[id] = conn, session
}

// homedRunOn gives `on` a run whose last workspace step (anthropic agent) is
// still running, with a committed server checkpoint at version 3.
func (r *drainRig) homedRunOn(t *testing.T, on runner.RunnerID, src runner.SourceID) (uuid.UUID, runner.Assignment) {
	t.Helper()
	run := r.pool.SeedRun(t)
	capability := drainAgentCapability()
	a := r.pool.ReserveJobFor(t, run, on, src, runner.ExecRequirements{
		Group:    runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Provider: capability.Provider, Backend: capability.Backend, EndpointFingerprint: capability.EndpointFingerprint,
		Model: capability.Models[0], Tools: []string{"Read"}, Op: runner.OpAgent, Affinity: runner.AffinityWorkspace, RunID: run,
	})
	_, err := r.homes.HandOff(context.Background(), run, on, "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	r.cps.mu.Lock()
	r.cps.refs[run] = runner.CheckpointRef{RunID: run, Version: 3, ManifestHash: strings.Repeat("a", 64)}
	r.cps.mu.Unlock()
	return run, a
}

func (r *drainRig) homedRun(t *testing.T) (uuid.UUID, runner.Assignment) {
	return r.homedRunOn(t, "mac-1", runner.MakeSourceID("mac-1", runner.CredAnthropicSubscription, ""))
}

func (r *drainRig) startDrain(t *testing.T, id runner.RunnerID) {
	t.Helper()
	require.NoError(t, r.pool.Registry.SetDraining(context.Background(), id, true))
	require.NoError(t, r.ctrl.Begin(context.Background(), id))
}

func (r *drainRig) deletesTo(id runner.RunnerID) int {
	n := 0
	for _, m := range r.conns[id].Sent() {
		if m.Kind == runner.MsgDeleteCopy {
			n++
		}
	}
	return n
}

func (r *drainRig) claimActive(t *testing.T, run uuid.UUID) bool {
	t.Helper()
	_, active, err := r.leases.Active(context.Background(), "run:"+run.String())
	require.NoError(t, err)
	return active
}

func TestDrainController_WaitsForActiveStepsThenMovesHomeAndCompletes(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	r.startDrain(t, "mac-1")

	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.Equal(t, 1, p.ActiveSteps, "the active step is left to finish")
	assert.Equal(t, 1, p.HomedWorkspaces)
	assert.False(t, p.Complete)
	assert.Empty(t, r.puller.pulls(), "nothing is pulled while the step runs")
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "a home with an active step does not move")

	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	p, err = r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.Equal(t, runner.DrainProgress{}.Blocked, p.Blocked)
	assert.Zero(t, p.ActiveSteps)
	assert.Zero(t, p.HomedWorkspaces, "the home was released from the drained runner")
	assert.True(t, p.Complete)

	pulls := r.puller.pulls()
	require.Len(t, pulls, 1)
	assert.Equal(t, runner.RunnerID("mac-2"), pulls[0].Target)
	assert.Equal(t, runner.RunnerID("mac-1"), pulls[0].HomeAtPull, "the target pulls BEFORE the handoff")
	assert.Equal(t, int64(3), pulls[0].Ref.Version, "the exact committed checkpoint")
	assert.Equal(t, "mac-2", pulls[0].Resource.Owner, "the pull runs under the drain's claim for the target")
	assert.False(t, r.claimActive(t, run), "the drain claim is released after the handoff")

	h, _ = r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-2"), h.RunnerID)
	hist, err := r.repo.History(ctx, run)
	require.NoError(t, err)
	assert.Contains(t, hist[len(hist)-1].Reason, "drain")
	assert.Equal(t, 1, r.deletesTo("mac-1"), "the drained copy is deleted after the handoff")

	_, err = r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	n := 0
	for _, k := range r.pool.EventKinds(t) {
		if k == runner.EventRunnerDrained {
			n++
		}
	}
	assert.Equal(t, 1, n, "runner.drained is announced once")
}

func TestDrainController_DryPoolStaysVisiblyBlocked(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1") // mac-2 never connects: no other runner serves anthropic
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	r.startDrain(t, "mac-1")

	for i := 0; i < 3; i++ {
		p, err := r.ctrl.Step(ctx, "mac-1")
		require.NoError(t, err)
		assert.False(t, p.Complete, "a dry-pool drain is never complete")
		assert.Equal(t, 1, p.HomedWorkspaces)
		require.Len(t, p.Blocked, 1)
		assert.Equal(t, runner.DrainKindWorkspace, p.Blocked[0].Kind)
		require.NotNil(t, p.Blocked[0].RunID)
		assert.Equal(t, run, *p.Blocked[0].RunID)
		assert.Equal(t, "anthropic_subscription:anthropic", p.Blocked[0].Group, "names the missing capacity")
		assert.Contains(t, p.Blocked[0].Reason, "no eligible runner")
	}
	assert.Empty(t, r.puller.pulls())
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "not released to server storage")
	items, err := r.store.List(ctx, "mac-1")
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, runner.DrainItemBlocked, items[0].State, "persisted")
	assert.NotContains(t, r.pool.EventKinds(t), runner.EventRunnerDrained)

	r.online(t, "mac-2") // capacity arrives
	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.True(t, p.Complete)
	assert.Empty(t, p.Blocked)
}

func TestDrainController_RunWithoutWorkspaceJobIsReleased(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	run := r.pool.SeedRun(t)
	_, err := r.homes.HandOff(ctx, run, "mac-1", "placement", 0, 0, runner.HomeMoveCause{})
	require.NoError(t, err)
	r.startDrain(t, "mac-1")
	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.True(t, p.Complete)
	assert.Empty(t, r.puller.pulls(), "no step ever ran there: nothing to pull")
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.ServerHome, h.RunnerID)
	assert.False(t, r.claimActive(t, run))
}

func TestDrainController_TickDrivesDrainingRunnersOnly(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))

	require.NoError(t, r.ctrl.Tick(ctx))
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "not draining: nothing moves")

	r.startDrain(t, "mac-1")
	require.NoError(t, r.ctrl.Tick(ctx))
	h, _ = r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-2"), h.RunnerID)

	_, err := r.ctrl.Step(ctx, "mac-2")
	require.ErrorIs(t, err, runner.ErrNotDraining)
}

func TestDrain_TargetPullFailureCannotReportCompleteOrDeleteSource(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	r.puller.fail = errors.New("manifest hash mismatch")
	r.startDrain(t, "mac-1")

	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.False(t, p.Complete)
	require.Len(t, p.Blocked, 1)
	assert.Contains(t, p.Blocked[0].Reason, "target pull failed")
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID, "the source keeps the home")
	assert.Zero(t, r.deletesTo("mac-1"), "the source copy is never deleted without a verified target")
	assert.False(t, r.claimActive(t, run), "a failed pull gives the claim back")
	assert.NotContains(t, r.pool.EventKinds(t), runner.EventRunnerDrained)

	r.puller.fail = nil
	p, err = r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.True(t, p.Complete, "the next pass retries the pull")
}

func TestDrain_TransferReadyCompletesAtExactGeneration(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	before, err := r.repo.GetHome(ctx, run)
	require.NoError(t, err)
	r.puller.tamper = func(rc *runner.PullReceipt) { rc.Resource.Generation++ } // a receipt for another claim
	r.startDrain(t, "mac-1")

	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.False(t, p.Complete, "a receipt that does not match the drain transfer is refused")
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, before.Generation, h.Generation)
	assert.Zero(t, r.deletesTo("mac-1"))

	r.puller.tamper = nil
	p, err = r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	require.True(t, p.Complete)
	h, _ = r.repo.GetHome(ctx, run)
	assert.Equal(t, before.Generation+1, h.Generation, "exactly one generation step")
	hist, err := r.repo.History(ctx, run)
	require.NoError(t, err)
	last := hist[len(hist)-1]
	assert.Equal(t, "pull-"+run.String(), last.OperationID, "the move carries the verified receipt's operation")
	assert.Nil(t, last.TargetJobID, "a drain move has no executing target job")
	assert.Equal(t, before.Generation, last.FromGeneration)
	assert.Equal(t, before.Generation+1, last.ToGeneration)
}

func TestDrain_HeldResourceWaitsWithoutMoving(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	held, err := r.leases.Claim(ctx, "run:"+run.String(), "group-mutation:test", runner.BuiltInRunnerID)
	require.NoError(t, err)
	r.startDrain(t, "mac-1")

	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.False(t, p.Complete)
	assert.Empty(t, r.puller.pulls(), "no pull while another writer holds the resource")
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID)

	require.NoError(t, r.leases.Release(ctx, held))
	p, err = r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.True(t, p.Complete)
}

func TestDrain_NonPortableHomeIsAVisibleBlocker(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	require.NoError(t, r.repo.SetNonPortable(ctx, run, runner.NonPortablePromisorReason))
	r.startDrain(t, "mac-1")

	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.False(t, p.Complete)
	require.Len(t, p.Blocked, 1)
	assert.Contains(t, p.Blocked[0].Reason, runner.NonPortablePromisorReason)
	assert.Empty(t, r.puller.pulls(), "a non-portable home never moves")
}

func TestDrain_BuiltInInteractiveSessionBlocks(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.pool.SeedBuiltIn(t, 2, hubtest.BuiltInAnthropic())
	r.sessions.n[runner.BuiltInRunnerID] = 1
	r.startDrain(t, runner.BuiltInRunnerID)

	p, err := r.ctrl.Step(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Sessions)
	assert.False(t, p.Complete, "a pinned built-in interactive session keeps the drain open")
	require.Len(t, p.Blocked, 1)
	assert.Equal(t, runner.DrainKindSession, p.Blocked[0].Kind)
	assert.Contains(t, p.Blocked[0].Reason, "interactive session")

	r.sessions.n[runner.BuiltInRunnerID] = 0
	p, err = r.ctrl.Step(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.True(t, p.Complete)
}

func TestDrain_SessionInventoryErrorIsNeverAZeroCount(t *testing.T) {
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.sessions.err = errors.New("instance store down")
	r.startDrain(t, "mac-1")
	_, err := r.ctrl.Step(context.Background(), "mac-1")
	require.Error(t, err)
	assert.NotContains(t, r.pool.EventKinds(t), runner.EventRunnerDrained)
}

func TestDrain_BuiltInTransfersHomeAtCommittedBoundary(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.pool.SeedBuiltIn(t, 2, hubtest.BuiltInAnthropic())
	r.online(t, "mac-2")
	run, a := r.homedRunOn(t, runner.BuiltInRunnerID, hubtest.BuiltInAnthropic().ID)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	r.startDrain(t, runner.BuiltInRunnerID)

	p, err := r.ctrl.Step(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.True(t, p.Complete)
	pulls := r.puller.pulls()
	require.Len(t, pulls, 1)
	assert.Equal(t, runner.RunnerID("mac-2"), pulls[0].Target)
	assert.Equal(t, runner.BuiltInRunnerID, pulls[0].HomeAtPull)
	h, _ := r.repo.GetHome(ctx, run)
	assert.Equal(t, runner.RunnerID("mac-2"), h.RunnerID)
}

func TestDrain_LocalOnlyPoolReportsCapacityBlocker(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.pool.SeedBuiltIn(t, 2, hubtest.BuiltInAnthropic())
	run, a := r.homedRunOn(t, runner.BuiltInRunnerID, hubtest.BuiltInAnthropic().ID)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	r.startDrain(t, runner.BuiltInRunnerID)

	p, err := r.ctrl.Step(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err, "draining the built-in runner is supported, never rejected")
	assert.False(t, p.Complete)
	require.Len(t, p.Blocked, 1)
	assert.Equal(t, run, *p.Blocked[0].RunID)
	assert.Equal(t, "anthropic_subscription:anthropic", p.Blocked[0].Group)
	assert.Contains(t, p.Blocked[0].Reason, "no eligible runner")
}

func TestDrain_InsufficientTargetDiskKeepsSourceHomeAndCopy(t *testing.T) {
	ctx := context.Background()
	r := newDrainRig(t)
	r.online(t, "mac-1")
	r.online(t, "mac-2")
	run, a := r.homedRun(t)
	require.NoError(t, r.pool.Ledger.Complete(ctx, a.Fence, "result"))
	free := uint64(0)
	require.NoError(t, r.hub.Handle(ctx, r.sessionsByRunner["mac-2"], runner.Inbound{Kind: runner.MsgHeartbeat,
		Heartbeat: &runner.HeartbeatInfo{Telemetry: runner.Telemetry{SampledAt: r.pool.Clock.Now(), DiskFreeBytes: &free}}}))
	r.startDrain(t, "mac-1")
	p, err := r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.False(t, p.Complete, "verified capacity is required before transferring bytes")
	require.Len(t, p.Blocked, 1)
	assert.Contains(t, p.Blocked[0].Reason, "no eligible runner")
	assert.Empty(t, r.puller.pulls())
	assert.Zero(t, r.deletesTo("mac-1"))
	h, err := r.repo.GetHome(ctx, run)
	require.NoError(t, err)
	assert.Equal(t, runner.RunnerID("mac-1"), h.RunnerID)
	free = 2 * runner.MinHomeFreeDiskBytes
	require.NoError(t, r.hub.Handle(ctx, r.sessionsByRunner["mac-2"], runner.Inbound{Kind: runner.MsgHeartbeat,
		Heartbeat: &runner.HeartbeatInfo{Telemetry: runner.Telemetry{SampledAt: r.pool.Clock.Now(), DiskFreeBytes: &free}}}))
	p, err = r.ctrl.Step(ctx, "mac-1")
	require.NoError(t, err)
	assert.True(t, p.Complete)
	assert.Len(t, r.puller.pulls(), 1)
	assert.Equal(t, 1, r.deletesTo("mac-1"))
}

func TestNewDrainController_RequiresEveryCollaborator(t *testing.T) {
	_, err := runner.NewDrainController(runner.DrainDeps{})
	require.Error(t, err)
	for _, name := range []string{"Pulls", "Sessions", "Leases", "Requirements", "Checkpoints", "Telemetry"} {
		assert.Contains(t, err.Error(), name)
	}
}
