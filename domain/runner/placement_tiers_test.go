package runner_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var tierRun = uuid.MustParse("6f1f3a0e-1b7e-4c55-9a9e-2f1a7c3d9b01")

func tierStrp(s string) *string { return &s }
func tierU64p(v uint64) *uint64 { return &v }

type tierPool struct{ snap runner.PoolSnapshot }

func newTierPool() *tierPool {
	return &tierPool{snap: runner.PoolSnapshot{
		SlotsInUse: map[runner.RunnerID]int{}, SourceInFlight: map[runner.SourceID]int{},
		Telemetry: map[runner.RunnerID]runner.Telemetry{}, Homes: map[uuid.UUID]runner.Home{},
		Now: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC),
	}}
}

// runner adds an online, compatible runner with an Anthropic subscription source.
func (p *tierPool) runner(id runner.RunnerID, mut ...func(*runner.Runner, *runner.Source)) *tierPool {
	r := runner.Runner{ID: id, Name: string(id), StepLimit: 8, Liveness: runner.Liveness("online"),
		Compat: runner.Compatibility{State: runner.CompatState("compatible")}}
	s := runner.Source{
		ID: runner.MakeSourceID(id, runner.CredAnthropicSubscription, ""), RunnerID: id,
		Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"},
		Backends: []string{"claude-code"},
		Health:   runner.SourceHealth{Auth: runner.AuthState("ok"), Capacity: runner.CapacityState("available")},
	}
	for _, m := range mut {
		m(&r, &s)
	}
	p.snap.Runners = append(p.snap.Runners, r)
	p.snap.Sources = append(p.snap.Sources, s)
	return p
}
func (p *tierPool) slots(id runner.RunnerID, n int) *tierPool { p.snap.SlotsInUse[id] = n; return p }
func (p *tierPool) tel(id runner.RunnerID, t runner.Telemetry) *tierPool {
	t.SampledAt = p.snap.Now
	p.snap.Telemetry[id] = t
	return p
}
func (p *tierPool) home(id runner.RunnerID) *tierPool {
	p.snap.Homes[tierRun] = runner.Home{RunID: tierRun, RunnerID: id, Generation: 3, CheckpointVersion: 2}
	return p
}

func tierAgentReq(bytes int64) runner.ExecRequirements {
	return runner.ExecRequirements{
		Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Model: "claude-sonnet-5", Backend: "claude-code", Op: runner.OpType("agent"),
		Affinity: runner.AffinityWorkspace, RunID: tierRun, WorkspaceBytes: bytes,
	}
}
func tierProcessReq() runner.ExecRequirements {
	return runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpType("process"),
		Affinity: runner.AffinityWorkspace, RunID: tierRun, WorkspaceBytes: 1 << 20}
}

var tierPolicy = runner.PlacementPolicy{TelemetryMaxAge: 15 * time.Second, MoveLoadThresholdPct: 50, MoveSizeCapBytes: 2 << 30}

func tierExhausted(_ *runner.Runner, s *runner.Source) {
	reset := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	s.Health.Capacity = runner.CapacityState("exhausted")
	s.Health.ResetAt = &reset
}

func TestPlaceWorkspaceTiers(t *testing.T) {
	cases := []struct {
		name      string
		pool      *tierPool
		req       runner.ExecRequirements
		pol       runner.PlacementPolicy
		placed    bool
		runner    runner.RunnerID
		tier      string
		reasonHas []string
		waitKind  runner.WaitKind
	}{
		{name: "healthy home with free slot stays even if another runner is idle",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 2),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-1", tier: runner.TierStay,
			reasonHas: []string{"stay:", "runner=mac-1"}},
		{name: "tier 1: exhausted source at home moves, reason records cause",
			pool: newTierPool().runner("mac-1", tierExhausted).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove,
			reasonHas: []string{"must_move:", "runner=mac-2", "home=mac-1", "cause="}},
		{name: "tier 1: logged-out source at home moves",
			pool: newTierPool().runner("mac-1", func(_ *runner.Runner, s *runner.Source) { s.Health.Auth = runner.AuthState("logged_out") }).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove},
		{name: "tier 1: draining home moves",
			pool: newTierPool().runner("mac-1", func(r *runner.Runner, _ *runner.Source) { r.Draining = true }).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove, reasonHas: []string{"draining"}},
		{name: "tier 1: lost home moves",
			pool: newTierPool().runner("mac-1", func(r *runner.Runner, _ *runner.Source) { r.Liveness = runner.Liveness("lost") }).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove, reasonHas: []string{"lost"}},
		{name: "tier 1: incompatible home moves",
			pool: newTierPool().runner("mac-1", func(r *runner.Runner, _ *runner.Source) { r.Compat.State = runner.CompatState("incompatible") }).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove, reasonHas: []string{"incompatible"}},
		{name: "tier 1: chat_only home cannot hold a workspace",
			pool: newTierPool().runner("mac-1", func(r *runner.Runner, _ *runner.Source) { r.Compat.State = runner.CompatState("chat_only") }).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove},
		{name: "tier 1: cordoned home receives no new assignment",
			pool: newTierPool().runner("mac-1", func(r *runner.Runner, _ *runner.Source) { r.Cordoned = true }).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove, reasonHas: []string{"cordoned"}},
		{name: "tier 1: confirmed insufficient disk moves",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").tel("mac-1", runner.Telemetry{DiskFreeBytes: tierU64p(100 << 20)}),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove, reasonHas: []string{"disk"}},
		{name: "tier 1: home absent from the snapshot (excluded after pull failure) moves",
			pool: func() *tierPool {
				p := newTierPool().runner("mac-2")
				p.snap.Homes[tierRun] = runner.Home{RunID: tierRun, RunnerID: "mac-1", Generation: 1}
				return p
			}(),
			req: tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove, reasonHas: []string{"unavailable"}},
		{name: "tier 1 with no alternative waits for capacity naming the group",
			pool: newTierPool().runner("mac-1", tierExhausted).home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: false, tier: runner.TierMustMove, waitKind: runner.WaitKind("capacity")},
		{name: "tier 2: full home with a free alternative moves",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 8),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierShouldMove,
			reasonHas: []string{"should_move:", "home=mac-1", "home_slots=8/8"}},
		{name: "full home and no free alternative waits for a home slot",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 8).slots("mac-2", 8),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: false, tier: runner.TierStay, waitKind: runner.WaitKind("slot")},
		{name: "tier 3: utilisation gap >= threshold moves an under-cap workspace",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 6),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMayMoveLoad,
			reasonHas: []string{"may_move_load:", "gap_pp=75", "home=mac-1"}},
		{name: "over-cap workspace stays under load imbalance",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 7),
			req:  tierAgentReq(3 << 30), pol: tierPolicy, placed: true, runner: "mac-1", tier: runner.TierStay},
		{name: "unknown workspace size never moves for load",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 7),
			req:  tierAgentReq(-1), pol: tierPolicy, placed: true, runner: "mac-1", tier: runner.TierStay},
		{name: "tier 3: critical memory pressure moves an under-cap workspace",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 1).tel("mac-1", runner.Telemetry{MemPressure: tierStrp("critical")}),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMayMoveLoad, reasonHas: []string{"memory pressure critical"}},
		{name: "tier 3: serious thermal state moves",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").tel("mac-1", runner.Telemetry{Thermal: tierStrp("serious")}),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMayMoveLoad},
		{name: "unknown telemetry never triggers a load move",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 1).tel("mac-1", runner.Telemetry{}),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-1", tier: runner.TierStay},
		{name: "fair thermal and warn pressure are not degraded",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").tel("mac-1", runner.Telemetry{Thermal: tierStrp("fair"), MemPressure: tierStrp("warn")}),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-1", tier: runner.TierStay},
		{name: "degraded home but degraded alternative stays",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").
				tel("mac-1", runner.Telemetry{MemPressure: tierStrp("critical")}).tel("mac-2", runner.Telemetry{Thermal: tierStrp("critical")}),
			req: tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-1", tier: runner.TierStay},
		{name: "zero policy disables load moves",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 7),
			req:  tierAgentReq(1 << 20), pol: runner.PlacementPolicy{}, placed: true, runner: "mac-1", tier: runner.TierStay},
		{name: "suspect home waits and never moves before loss",
			pool: newTierPool().runner("mac-1", func(r *runner.Runner, _ *runner.Source) { r.Liveness = runner.Liveness("suspect") }).runner("mac-2").home("mac-1"),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: false, tier: runner.TierStay, waitKind: runner.WaitKind("slot"), reasonHas: []string{"suspect"}},
		{name: "process step goes to an eligible home with a free slot despite load gap",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 7),
			req:  tierProcessReq(), pol: tierPolicy, placed: true, runner: "mac-1", tier: runner.TierStay},
		{name: "process step on a full home follows tier 2",
			pool: newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 8),
			req:  tierProcessReq(), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierShouldMove},
		{name: "process step on a draining home follows tier 1",
			pool: newTierPool().runner("mac-1", func(r *runner.Runner, _ *runner.Source) { r.Draining = true }).runner("mac-2").home("mac-1"),
			req:  tierProcessReq(), pol: tierPolicy, placed: true, runner: "mac-2", tier: runner.TierMustMove},
		{name: "no runner home: ordinary ranked placement without a tier",
			pool: newTierPool().runner("mac-1").runner("mac-2").slots("mac-1", 4),
			req:  tierAgentReq(1 << 20), pol: tierPolicy, placed: true, runner: "mac-2", tier: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := runner.Place(tc.req, tc.pool.snap, tc.pol)
			assert.Equal(t, tc.placed, d.Placed, "reason: %s", d.Reason)
			assert.Equal(t, tc.tier, d.Tier, "reason: %s", d.Reason)
			if tc.placed {
				assert.Equal(t, tc.runner, d.Runner, "reason: %s", d.Reason)
				assert.Nil(t, d.Wait)
			} else {
				require.NotNil(t, d.Wait)
				assert.Equal(t, tc.waitKind, d.Wait.Kind)
				assert.Equal(t, tc.req.Group, d.Wait.Group)
			}
			for _, s := range tc.reasonHas {
				assert.True(t, strings.Contains(d.Reason, s), "reason %q lacks %q", d.Reason, s)
			}
		})
	}
}

func TestPlaceWorkspace_MustMoveCapacityWaitCarriesReset(t *testing.T) {
	p := newTierPool().runner("mac-1", tierExhausted).home("mac-1")
	d := runner.Place(tierAgentReq(1<<20), p.snap, tierPolicy)
	require.NotNil(t, d.Wait)
	require.NotNil(t, d.Wait.NextReset)
	assert.Equal(t, time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC), *d.Wait.NextReset)
}

func TestPlaceWorkspace_NeverCrossesFamilyToMove(t *testing.T) {
	p := newTierPool().runner("mac-1", tierExhausted).runner("mac-2", func(_ *runner.Runner, s *runner.Source) {
		s.Kind = runner.CredOpenAISubscription
		s.Families = []modelidentity.Family{"openai"}
		s.Backends = []string{"codex"}
	}).home("mac-1")
	d := runner.Place(tierAgentReq(1<<20), p.snap, tierPolicy)
	assert.False(t, d.Placed, fmt.Sprintf("moved across families: %s", d.Reason))
	assert.Equal(t, runner.TierMustMove, d.Tier)
}

func TestPlace_NonWorkspaceAffinityUnchanged(t *testing.T) {
	p := newTierPool().runner("mac-1").runner("mac-2").home("mac-1").slots("mac-1", 6)
	req := tierAgentReq(1 << 20)
	req.Affinity = runner.AffinityNone
	req.Op = runner.OpType("chat")
	req.Backend = ""
	d := runner.Place(req, p.snap, tierPolicy)
	assert.Equal(t, "", d.Tier, "homes only matter for workspace affinity")
}
