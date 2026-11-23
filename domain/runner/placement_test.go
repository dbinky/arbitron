package runner_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var placeNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

func f64(v float64) *float64 { return &v }
func strp(s string) *string  { return &s }

func online(id runner.RunnerID) runner.Runner {
	return runner.Runner{ID: id, Name: string(id), BuiltIn: id == runner.BuiltInRunnerID, StepLimit: 8,
		Compat: runner.Compatibility{State: runner.CompatCompatible}, Liveness: runner.LivenessOnline}
}

func healthy() runner.SourceHealth {
	return runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}
}

func anthropicSub(r runner.RunnerID) runner.Source {
	return runner.Source{ID: runner.MakeSourceID(r, runner.CredAnthropicSubscription, ""), RunnerID: r,
		Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"},
		Backends: []string{"", "claude-code"}, Health: healthy()}
}

func snap(runners []runner.Runner, sources []runner.Source) runner.PoolSnapshot {
	return runner.PoolSnapshot{Runners: runners, Sources: sources,
		SlotsInUse: map[runner.RunnerID]int{}, SourceInFlight: map[runner.SourceID]int{},
		Telemetry: map[runner.RunnerID]runner.Telemetry{}, Homes: map[uuid.UUID]runner.Home{}, Now: placeNow}
}

func anthropicReq(aff runner.Affinity) runner.ExecRequirements {
	return runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Model: "claude-sonnet-4-5", Backend: "claude-code", Op: runner.OpAgent, Affinity: aff, RunID: uuid.New()}
}

func TestPlace_BuiltInHappyPath(t *testing.T) {
	d := runner.Place(anthropicReq(runner.AffinityBuiltIn), snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{anthropicSub(runner.BuiltInRunnerID)}), runner.PlacementPolicy{})
	require.True(t, d.Placed)
	assert.Nil(t, d.Wait)
	assert.Equal(t, runner.BuiltInRunnerID, d.Runner)
	assert.Equal(t, runner.SourceID("builtin/anthropic_subscription/-"), d.Source)
	assert.Equal(t, "rank: runner=builtin source=builtin/anthropic_subscription/- headroom=unknown slots=0/8", d.Reason)
	assert.Empty(t, d.Tier, "Phase 1 has no move tiers")
}

func TestPlace_ProcessStepNeedsSlotOnly(t *testing.T) {
	req := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess, Affinity: runner.AffinityBuiltIn, RunID: uuid.New()}
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID)}, nil)
	s.SlotsInUse[runner.BuiltInRunnerID] = 3
	d := runner.Place(req, s, runner.PlacementPolicy{})
	require.True(t, d.Placed)
	assert.Equal(t, runner.SourceID(""), d.Source)
	assert.Equal(t, "rank: runner=builtin source=- headroom=unknown slots=3/8", d.Reason)
}

func TestPlace_NinthStepWaitsForSlot(t *testing.T) {
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{anthropicSub(runner.BuiltInRunnerID)})
	s.SlotsInUse[runner.BuiltInRunnerID] = 7
	require.True(t, runner.Place(anthropicReq(runner.AffinityBuiltIn), s, runner.PlacementPolicy{}).Placed, "the eighth step still fits")

	s.SlotsInUse[runner.BuiltInRunnerID] = 8
	d := runner.Place(anthropicReq(runner.AffinityBuiltIn), s, runner.PlacementPolicy{})
	require.False(t, d.Placed)
	require.NotNil(t, d.Wait)
	assert.Equal(t, runner.WaitSlot, d.Wait.Kind)
	assert.Equal(t, "anthropic_subscription:anthropic", d.Wait.Group.String())
	assert.Equal(t, "claude-sonnet-4-5", d.Wait.Model)
	assert.Contains(t, d.Wait.Detail, "queued · slot")
	assert.Equal(t, d.Wait.Detail, d.Reason)

	process := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredNone}, Op: runner.OpProcess, Affinity: runner.AffinityBuiltIn}
	assert.Equal(t, runner.WaitSlot, runner.Place(process, s, runner.PlacementPolicy{}).Wait.Kind, "process steps share the same slots")
}

func TestPlace_NoSourceWaitsForCapacityNamingGroupAndEarliestReset(t *testing.T) {
	early := placeNow.Add(2 * time.Hour)
	late := placeNow.Add(5 * time.Hour)
	a := anthropicSub(runner.BuiltInRunnerID)
	a.Health = runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ResetAt: &late}
	b := anthropicSub("mac-mini")
	b.Health = runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ResetAt: &early}
	c := anthropicSub("mac-studio")
	c.Health = runner.SourceHealth{Auth: runner.AuthLoggedOut, Capacity: runner.CapacityAvailable}
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID), online("mac-mini"), online("mac-studio")}, []runner.Source{a, b, c})

	d := runner.Place(anthropicReq(runner.AffinityNone), s, runner.PlacementPolicy{})
	require.False(t, d.Placed)
	require.NotNil(t, d.Wait)
	assert.Equal(t, runner.WaitCapacity, d.Wait.Kind)
	assert.Equal(t, "anthropic_subscription:anthropic", d.Wait.Group.String())
	require.NotNil(t, d.Wait.NextReset)
	assert.Equal(t, early, *d.Wait.NextReset, "the earliest known reset among matching exhausted sources")
	assert.Contains(t, d.Wait.Detail, "waiting · capacity")
	assert.Contains(t, d.Wait.Detail, "anthropic_subscription:anthropic")
	assert.Contains(t, d.Wait.Detail, "exhausted=2")
	assert.Contains(t, d.Wait.Detail, "logged_out=1")

	empty := runner.Place(anthropicReq(runner.AffinityBuiltIn), snap([]runner.Runner{online(runner.BuiltInRunnerID)}, nil), runner.PlacementPolicy{})
	require.NotNil(t, empty.Wait)
	assert.Equal(t, runner.WaitCapacity, empty.Wait.Kind)
	assert.Nil(t, empty.Wait.NextReset, "no exhausted source => no known reset")
	assert.Contains(t, empty.Wait.Detail, "no source serves anthropic_subscription:anthropic")
}

func TestPlace_PastResetIsHandledByHealthNotPlacement(t *testing.T) {
	past := placeNow.Add(-10 * time.Minute)
	src := anthropicSub(runner.BuiltInRunnerID)
	src.Health = runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ResetAt: &past}
	d := runner.Place(anthropicReq(runner.AffinityBuiltIn), snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{src}), runner.PlacementPolicy{})
	require.False(t, d.Placed, "placement never reinterprets a reset; HealthMonitor.Tick moves it to probation (P1 T13)")
	assert.Equal(t, runner.WaitCapacity, d.Wait.Kind)
	assert.Equal(t, past, *d.Wait.NextReset)

	src.Health = runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityProbation}
	assert.True(t, runner.Place(anthropicReq(runner.AffinityBuiltIn), snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{src}), runner.PlacementPolicy{}).Placed,
		"once health has moved it to probation it takes a step")
}

func TestPlace_ProbationTakesOneStepAtATime(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	src.Health = runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityProbation}
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{src})
	require.True(t, runner.Place(anthropicReq(runner.AffinityBuiltIn), s, runner.PlacementPolicy{}).Placed)
	s.SourceInFlight[src.ID] = 1
	d := runner.Place(anthropicReq(runner.AffinityBuiltIn), s, runner.PlacementPolicy{})
	require.False(t, d.Placed)
	assert.Equal(t, runner.WaitCapacity, d.Wait.Kind)
	assert.Contains(t, d.Wait.Detail, "probation_busy=1")
}

func TestPlace_FamilyNeverCrossed(t *testing.T) {
	openai := runner.Source{ID: runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredOpenAISubscription, ""), RunnerID: runner.BuiltInRunnerID,
		Kind: runner.CredOpenAISubscription, Families: []modelidentity.Family{"openai"}, Backends: []string{"codex"}, Health: healthy()}
	// An anthropic-kind source that only proves openai (misadvertised) must not serve anthropic.
	wrongFamily := anthropicSub("mac-mini")
	wrongFamily.Families = []modelidentity.Family{"openai"}
	unproven := anthropicSub("mac-studio")
	unproven.Families = nil
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID), online("mac-mini"), online("mac-studio")}, []runner.Source{openai, wrongFamily, unproven})
	d := runner.Place(anthropicReq(runner.AffinityNone), s, runner.PlacementPolicy{})
	require.False(t, d.Placed)
	assert.Equal(t, runner.WaitCapacity, d.Wait.Kind)

	lr := runner.Source{ID: runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredLunaRoute, "lr"), RunnerID: runner.BuiltInRunnerID, Kind: runner.CredLunaRoute,
		CredentialRef: "lr", Families: []modelidentity.Family{"openai", "zhipuai"}, Backends: []string{"codex", "pi"}, Health: healthy()}
	unresolved := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredLunaRoute, ConnectionRef: "lr", Family: modelidentity.FamilyUnresolved}, Model: "x", Backend: "codex", Op: runner.OpAgent, Affinity: runner.AffinityBuiltIn}
	assert.False(t, runner.Place(unresolved, snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{lr}), runner.PlacementPolicy{}).Placed,
		"an unresolved family is never a wildcard on a family-gated source")
}

func TestPlace_ExactRequestFilters(t *testing.T) {
	src := anthropicSub(runner.BuiltInRunnerID)
	src.Models = []string{"claude-opus-4-1"}
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{src})
	assert.False(t, runner.Place(anthropicReq(runner.AffinityBuiltIn), s, runner.PlacementPolicy{}).Placed, "model allowlist is exact")
	req := anthropicReq(runner.AffinityBuiltIn)
	req.Model = "claude-opus-4-1"
	assert.True(t, runner.Place(req, s, runner.PlacementPolicy{}).Placed)
	req.Backend = "codex"
	assert.False(t, runner.Place(req, s, runner.PlacementPolicy{}).Placed, "backend must be advertised")
}

func TestPlace_ServerConnectionNeverOffBuiltIn(t *testing.T) {
	group := runner.ModelGroup{Kind: runner.CredServerConnection, ConnectionRef: "azure-east", Family: "openai"}
	remote := runner.Source{ID: runner.MakeSourceID("mac-mini", runner.CredServerConnection, "azure-east"), RunnerID: "mac-mini", Kind: runner.CredServerConnection,
		CredentialRef: "azure-east", Health: healthy()}
	req := runner.ExecRequirements{Group: group, Model: "gpt-4o", Op: runner.OpChat, Affinity: runner.AffinityNone}
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID), online("mac-mini")}, []runner.Source{remote})
	d := runner.Place(req, s, runner.PlacementPolicy{})
	require.False(t, d.Placed, "a server-connection source advertised by a remote runner is ignored")
	assert.Equal(t, runner.WaitCapacity, d.Wait.Kind)

	builtin := remote
	builtin.ID, builtin.RunnerID = runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "azure-east"), runner.BuiltInRunnerID
	other := builtin
	other.ID, other.CredentialRef = runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredServerConnection, "azure-west"), "azure-west"
	d = runner.Place(req, snap([]runner.Runner{online(runner.BuiltInRunnerID), online("mac-mini")}, []runner.Source{other, builtin, remote}), runner.PlacementPolicy{})
	require.True(t, d.Placed)
	assert.Equal(t, runner.BuiltInRunnerID, d.Runner)
	assert.Equal(t, builtin.ID, d.Source, "only the binding's own connection source; another connection never substitutes")

	opaque := req
	opaque.Group.Family = modelidentity.FamilyUnresolved
	opaque.Model = "my-deployment-7"
	assert.True(t, runner.Place(opaque, snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{builtin}), runner.PlacementPolicy{}).Placed,
		"an opaque deployment still runs on its own built-in connection (existing behaviour)")
}

func TestPlace_RunnerGates(t *testing.T) {
	type mut func(*runner.Runner)
	cases := map[string]mut{
		"cordoned": func(r *runner.Runner) { r.Cordoned = true },
		"draining": func(r *runner.Runner) { r.Draining = true },
		"suspect":  func(r *runner.Runner) { r.Liveness = runner.LivenessSuspect },
		"lost":     func(r *runner.Runner) { r.Liveness = runner.LivenessLost },
		"offline":  func(r *runner.Runner) { r.Liveness = runner.LivenessOffline },
		"incompatible": func(r *runner.Runner) {
			r.Compat = runner.Compatibility{State: runner.CompatIncompatible, Reason: "claude 2.0 < 2.1"}
		},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			r := online("mac-mini")
			m(&r)
			d := runner.Place(anthropicReq(runner.AffinityNone), snap([]runner.Runner{r}, []runner.Source{anthropicSub("mac-mini")}), runner.PlacementPolicy{})
			require.False(t, d.Placed)
			assert.Equal(t, runner.WaitCapacity, d.Wait.Kind)
		})
	}
}

func TestPlace_ChatOnlyRunnerAcceptsOnlyHermeticWork(t *testing.T) {
	r := online("mac-mini")
	r.Compat = runner.Compatibility{State: runner.CompatChatOnly, Reason: "harness too old for agents"}
	s := snap([]runner.Runner{r}, []runner.Source{anthropicSub("mac-mini")})
	assert.True(t, runner.Place(anthropicReq(runner.AffinityNone), s, runner.PlacementPolicy{}).Placed)
	for _, aff := range []runner.Affinity{runner.AffinityBuiltIn, runner.AffinityWorkspace, runner.AffinitySession} {
		assert.False(t, runner.Place(anthropicReq(aff), s, runner.PlacementPolicy{}).Placed, "affinity %s", aff)
	}
}

func TestPlace_AffinityRestrictsRunner(t *testing.T) {
	runners := []runner.Runner{online(runner.BuiltInRunnerID), online("mac-mini")}
	sources := []runner.Source{anthropicSub(runner.BuiltInRunnerID), anthropicSub("mac-mini")}

	assert.Equal(t, runner.BuiltInRunnerID, runner.Place(anthropicReq(runner.AffinityBuiltIn), snap(runners, sources), runner.PlacementPolicy{}).Runner)

	ws := anthropicReq(runner.AffinityWorkspace)
	s := snap(runners, sources)
	assert.Equal(t, runner.BuiltInRunnerID, runner.Place(ws, s, runner.PlacementPolicy{}).Runner, "no home => built-in")
	s.Homes[ws.RunID] = runner.Home{RunID: ws.RunID, RunnerID: "mac-mini", Generation: 1}
	assert.Equal(t, runner.RunnerID("mac-mini"), runner.Place(ws, s, runner.PlacementPolicy{}).Runner, "home runner")

	sess := anthropicReq(runner.AffinitySession)
	sess.PinnedRunner = "mac-mini"
	assert.Equal(t, runner.RunnerID("mac-mini"), runner.Place(sess, snap(runners, sources), runner.PlacementPolicy{}).Runner)
	sess.PinnedRunner = ""
	assert.Equal(t, runner.BuiltInRunnerID, runner.Place(sess, snap(runners, sources), runner.PlacementPolicy{}).Runner)
}

func TestPlace_LocalHarnessBuiltInOnly(t *testing.T) {
	req := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredLocalHarness, ConnectionRef: "pi", Family: "zhipuai"}, Model: "glm-5.3", Backend: "pi", Op: runner.OpAgent, Affinity: runner.AffinityNone}
	remote := runner.Source{ID: runner.MakeSourceID("mac-mini", runner.CredLocalHarness, "pi"), RunnerID: "mac-mini", Kind: runner.CredLocalHarness, CredentialRef: "pi", Health: healthy()}
	builtin := remote
	builtin.ID, builtin.RunnerID = runner.MakeSourceID(runner.BuiltInRunnerID, runner.CredLocalHarness, "pi"), runner.BuiltInRunnerID
	d := runner.Place(req, snap([]runner.Runner{online("mac-mini"), online(runner.BuiltInRunnerID)}, []runner.Source{remote, builtin}), runner.PlacementPolicy{})
	require.True(t, d.Placed)
	assert.Equal(t, runner.BuiltInRunnerID, d.Runner)
}

func TestPlace_IsPure(t *testing.T) {
	s := snap([]runner.Runner{online(runner.BuiltInRunnerID)}, []runner.Source{anthropicSub(runner.BuiltInRunnerID)})
	req := anthropicReq(runner.AffinityBuiltIn)
	assert.Equal(t, runner.Place(req, s, runner.PlacementPolicy{}), runner.Place(req, s, runner.PlacementPolicy{}))
	assert.Empty(t, s.SlotsInUse, "the snapshot is not mutated")
}
