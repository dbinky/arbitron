package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var sessNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func sessHeadroom(v float64) *float64 { return &v }

func sessSource(r RunnerID, headroom float64) Source {
	return Source{
		ID: MakeSourceID(r, CredAnthropicSubscription, ""), RunnerID: r, Kind: CredAnthropicSubscription,
		Families: []modelidentity.Family{modelidentity.Family("anthropic")}, Backends: []string{"claude-code"},
		Capabilities: []ExecutionCapability{{Op: OpStream, Backend: "claude-code", Families: []modelidentity.Family{"anthropic"}, Models: []string{"claude-sonnet-4-5"}, ExecutedModel: true}},
		Health:       SourceHealth{Auth: AuthState("ok"), Capacity: CapacityState("available"), Headroom: sessHeadroom(headroom)},
	}
}

func sessRunner(id RunnerID) Runner {
	return Runner{ID: id, Name: string(id), StepLimit: 2, Liveness: Liveness("online"), Compat: Compatibility{State: CompatState("compatible")}}
}

// sessPool: mac-a holds the session; mac-b is idle with MORE headroom, so any
// load-based choice would pick mac-b.
func sessPool() PoolSnapshot {
	return PoolSnapshot{
		Runners:        []Runner{sessRunner("mac-a"), sessRunner("mac-b")},
		Sources:        []Source{sessSource("mac-a", 0.30), sessSource("mac-b", 0.95)},
		SlotsInUse:     map[RunnerID]int{"mac-a": 1},
		SourceInFlight: map[SourceID]int{},
		Now:            sessNow,
	}
}

func sessReq(pin RunnerID) ExecRequirements {
	return ExecRequirements{
		Group: ModelGroup{Kind: CredAnthropicSubscription, Family: modelidentity.Family("anthropic")},
		Model: "claude-sonnet-4-5", Backend: "claude-code", Op: OpType("stream"),
		Affinity: AffinitySession, RunID: uuid.New(), PinnedRunner: pin,
	}
}

func sessRunnerAt(snap *PoolSnapshot, id RunnerID) *Runner {
	for i := range snap.Runners {
		if snap.Runners[i].ID == id {
			return &snap.Runners[i]
		}
	}
	return nil
}

func sessSourceOf(snap *PoolSnapshot, id RunnerID) *Source {
	for i := range snap.Sources {
		if snap.Sources[i].RunnerID == id {
			return &snap.Sources[i]
		}
	}
	return nil
}

func TestPlaceSession_StaysOnEligiblePin(t *testing.T) {
	d := Place(sessReq("mac-a"), sessPool(), PlacementPolicy{})
	require.True(t, d.Placed)
	assert.Equal(t, RunnerID("mac-a"), d.Runner, "never the roomier mac-b")
	assert.Equal(t, MakeSourceID("mac-a", CredAnthropicSubscription, ""), d.Source)
	assert.Equal(t, "stay", d.Tier)
	assert.False(t, d.MoveRequired)
	assert.Equal(t, "session_pin: runner=mac-a source=mac-a/anthropic_subscription/- headroom=0.30 slots=1/2", d.Reason)
}

// Design §8 Edge: cordon, suspect liveness and slot pressure never move a pinned session.
func TestPlaceSession_CordonSuspectSlotAndCongestionWaitOnPin(t *testing.T) {
	cases := map[string]struct {
		mut    func(*PoolSnapshot)
		kind   WaitKind
		detail string
	}{
		"cordoned": {func(s *PoolSnapshot) { sessRunnerAt(s, "mac-a").Cordoned = true }, WaitKind("slot"), "cordoned"},
		"suspect":  {func(s *PoolSnapshot) { sessRunnerAt(s, "mac-a").Liveness = Liveness("suspect") }, WaitKind("slot"), "suspect"},
		"offline":  {func(s *PoolSnapshot) { sessRunnerAt(s, "mac-a").Liveness = Liveness("offline") }, WaitKind("slot"), "offline"},
		"incompatible": {func(s *PoolSnapshot) {
			sessRunnerAt(s, "mac-a").Compat = Compatibility{State: CompatState("incompatible"), Reason: "harness too old"}
		}, WaitKind("slot"), "incompatible"},
		"slot full": {func(s *PoolSnapshot) { s.SlotsInUse["mac-a"] = 2 }, WaitKind("slot"), "slots 2/2"},
		"congested": {func(s *PoolSnapshot) {
			until := sessNow.Add(time.Minute)
			src := sessSourceOf(s, "mac-a")
			src.Health.Congested, src.Health.CongestedUntil = true, &until
		}, WaitKind("capacity"), "congested"},
		"probation busy": {func(s *PoolSnapshot) {
			src := sessSourceOf(s, "mac-a")
			src.Health.Capacity = CapacityState("probation")
			s.SourceInFlight[src.ID] = 1
		}, WaitKind("capacity"), "probation"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap := sessPool()
			tc.mut(&snap)
			d := Place(sessReq("mac-a"), snap, PlacementPolicy{MoveLoadThresholdPct: 10})
			assert.False(t, d.Placed, "waits")
			assert.Empty(t, d.Runner, "never placed on another runner")
			assert.False(t, d.MoveRequired, "load and operator state never move a pin")
			assert.Equal(t, "stay", d.Tier)
			require.NotNil(t, d.Wait)
			assert.Equal(t, tc.kind, d.Wait.Kind)
			assert.Contains(t, d.Wait.Detail, "session pinned to mac-a")
			assert.Contains(t, d.Wait.Detail, tc.detail)
			assert.Equal(t, "claude-sonnet-4-5", d.Wait.Model)
		})
	}
}

func TestPlaceSession_MoveTriggersAreMustMove(t *testing.T) {
	cases := map[string]struct {
		mut    func(*PoolSnapshot)
		reason string
	}{
		"exhausted": {func(s *PoolSnapshot) {
			reset := sessNow.Add(2 * time.Hour)
			src := sessSourceOf(s, "mac-a")
			src.Health.Capacity, src.Health.ResetAt = CapacityState("exhausted"), &reset
		}, "exhausted"},
		"logged out": {func(s *PoolSnapshot) { sessSourceOf(s, "mac-a").Health.Auth = AuthState("logged_out") }, "logged_out"},
		"draining":   {func(s *PoolSnapshot) { sessRunnerAt(s, "mac-a").Draining = true }, "drain"},
		"lost":       {func(s *PoolSnapshot) { sessRunnerAt(s, "mac-a").Liveness = Liveness("lost") }, "runner_lost"},
		"removed":    {func(s *PoolSnapshot) { s.Runners = s.Runners[1:] }, "runner_lost"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap := sessPool()
			tc.mut(&snap)
			d := Place(sessReq("mac-a"), snap, PlacementPolicy{})
			assert.False(t, d.Placed, "the placer never moves the session itself")
			assert.Empty(t, d.Runner)
			assert.Nil(t, d.Wait)
			assert.True(t, d.MoveRequired)
			assert.Equal(t, tc.reason, d.MoveReason)
			assert.Equal(t, "must_move", d.Tier)
			assert.Equal(t, "must_move: pinned runner=mac-a reason="+tc.reason, d.Reason)
		})
	}
}

func TestPlaceSession_DrainBeatsWaitAndOtherFamiliesNeverMatch(t *testing.T) {
	snap := sessPool()
	sessRunnerAt(&snap, "mac-a").Draining = true
	sessRunnerAt(&snap, "mac-a").Cordoned = true
	assert.Equal(t, "drain", Place(sessReq("mac-a"), snap, PlacementPolicy{}).MoveReason, "drain is a move trigger even when also cordoned")

	snap = sessPool()
	src := sessSourceOf(&snap, "mac-a")
	src.Families = []modelidentity.Family{modelidentity.Family("openai")}
	d := Place(sessReq("mac-a"), snap, PlacementPolicy{})
	assert.False(t, d.MoveRequired, "missing matching inventory is not proof of logout")
	require.NotNil(t, d.Wait)
}

func TestPlaceSession_FirstTurnPlacesLikeAWorkspaceStep(t *testing.T) {
	req := sessReq("")
	snap := sessPool()
	first := Place(req, snap, PlacementPolicy{})
	ws := req
	ws.Affinity = AffinityWorkspace
	assert.Equal(t, Place(ws, snap, PlacementPolicy{}), first, "no pin yet: identical to a workspace step")
	assert.False(t, first.MoveRequired)
	assert.False(t, strings.HasPrefix(first.Reason, "session_pin"))
}
