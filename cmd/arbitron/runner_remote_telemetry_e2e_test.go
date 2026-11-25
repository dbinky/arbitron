package main

import (
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
)

// The built-in telemetry loop (P2 T7) feeds the hub window from the real
// sampler measuring the server execution root, and the operator surface
// (ServiceDeps.Telemetry = hub) shows it. Not starting the loop, or leaving
// ServiceDeps.Telemetry on the Phase 1 empty source, fails this test.
func TestRemoteRunnersCompositionRootBuiltInTelemetryReachesHub(t *testing.T) {
	st := bootRemoteRunnerStack(t, nil, false)
	builtin := runner.BuiltInRunnerID

	require.Eventually(t, func() bool {
		tl, ok := st.ds.remote.Hub.Latest()[builtin]
		return ok && !tl.SampledAt.IsZero()
	}, 20*time.Second, 50*time.Millisecond, "the built-in runner has a telemetry sample")
	require.Eventually(t, func() bool { return len(st.ds.remote.Hub.Window(builtin)) >= 2 },
		20*time.Second, 50*time.Millisecond, "the loop keeps sampling every heartbeat")
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		tl := st.ds.remote.Hub.Latest()[builtin]
		require.NotNil(t, tl.DiskFreeBytes, "the real sampler measured the workspace volume")
		assert.Positive(t, *tl.DiskFreeBytes)
		require.NotNil(t, tl.Cores)
		assert.Positive(t, *tl.Cores)
	}
	require.Eventually(t, func() bool {
		r, ok := rrRunner(st.srv, string(builtin))
		return ok && r["telemetry"] != nil
	}, 20*time.Second, 50*time.Millisecond, "GET /api/v1/runners/builtin shows the hub telemetry")
}

// Placement reads the hub window (DispatcherDeps.Telemetry = hub). The
// built-in is cordoned and the two remote runners are identical — same
// advertised anthropic source, unknown headroom, step limit 1, and runs are
// sequential so both sit at 0/1 — except that mac-hot reports critical memory
// pressure, critical thermal state and 99% CPU. Every P1 T4 rank key ahead of
// the telemetry penalty is therefore equal, and without telemetry the per-run
// fair tie-break alone decides. For each run the test asks the pure
// runner.Place which runner the telemetry-blind ranking picks for that run ID
// ("adversarial" when it picks mac-hot) and keeps triggering until three
// adversarial runs have been placed. An adversarial run can reach mac-cool only
// through the telemetry penalty, so with the Phase 1 empty telemetry source the
// first adversarial run lands on mac-hot and the test fails with certainty; if
// no adversarial run occurs within maxRuns the test fails too (fail closed).
func TestRemoteRunnersCompositionRootPlacementUsesRunnerTelemetry(t *testing.T) {
	st := bootRemoteRunnerStack(t, map[string]string{"rr-single": rrSingleManifest}, false)
	ctx := adminCtxForLocalUser(t, st.ds)
	code, body := rrDo(t, http.MethodPost, st.srv.URL+"/api/v1/runners/builtin/cordon", nil)
	require.Less(t, code, 300, string(body))

	critical, cpu := "critical", 99.0
	hot := runner.Telemetry{MemPressure: &critical, Thermal: &critical, CPUPercent: &cpu}
	plainHot, idHot, _ := rrCreateKey(t, st, "mac-hot")
	plainCool, idCool, _ := rrCreateKey(t, st, "mac-cool")
	execHot := newRRExecutor(t, "hot-answer", false)
	execCool := newRRExecutor(t, "cool-answer", false)
	startRemoteAgent(t, st, plainHot, st.workspaceDir, execHot, hot)
	startRemoteAgent(t, st, plainCool, st.workspaceDir, execCool, runner.Telemetry{})
	rrAwaitLiveness(t, st, idHot, "online")
	rrAwaitLiveness(t, st, idCool, "online")
	hotID, coolID := runner.RunnerID(idHot), runner.RunnerID(idCool)
	require.Eventually(t, func() bool {
		tl, ok := st.ds.remote.Hub.Latest()[hotID]
		return ok && tl.MemPressure != nil && *tl.MemPressure == "critical"
	}, 20*time.Second, 50*time.Millisecond, "mac-hot's heartbeat telemetry reached the hub")

	srcCool := runner.MakeSourceID(coolID, runner.CredAnthropicSubscription, "")
	withTelemetry := map[runner.RunnerID]runner.Telemetry{hotID: hot, coolID: {}}
	noTelemetry := map[runner.RunnerID]runner.Telemetry{}

	const wantAdversarial, maxRuns = 3, 40
	adversarial, runs := 0, 0
	for adversarial < wantAdversarial && runs < maxRuns {
		runs++
		id, _, err := st.ds.eng.Trigger(ctx, "rr-single", map[string]any{}, "normal")
		require.NoError(t, err)
		rrRequireCompleted(t, st.ds, id)

		var owner, source, model string
		require.NoError(t, st.ds.db.QueryRow(
			`SELECT COALESCE(runner_id, ''), COALESCE(source_id, ''), model FROM runner_jobs WHERE run_id = ? AND state = 'completed'`,
			id.String()).Scan(&owner, &source, &model))
		require.Equal(t, string(srcCool), source, "the oracle below models the source the runner really advertised")
		require.Equal(t, coolID, rrPlacementOracle(t, id, model, withTelemetry, hotID, coolID),
			"with telemetry the ranking always prefers the healthy machine")
		blind := rrPlacementOracle(t, id, model, noTelemetry, hotID, coolID)
		if blind == hotID {
			adversarial++
		}
		require.Equal(t, idCool, owner,
			"run %s: the telemetry-blind tie-break picks %s; only the hub telemetry penalty places it on mac-cool", id, blind)
	}
	require.GreaterOrEqual(t, adversarial, wantAdversarial,
		"%d runs never produced a run whose fair tie-break favours mac-hot", runs)
	assert.Zero(t, execHot.count(), "no step is placed on the pressured machine")
	assert.Equal(t, runs, execCool.count())
}

// rrPlacementOracle replays the pure P1 T4 ranking for one run over the two
// otherwise-identical remote runners of the telemetry test (online,
// compatible, step limit 1, 0 slots in use, one healthy anthropic source with
// unknown headroom each). tel is the telemetry the dispatcher would see; an
// empty map is what it sees when DispatcherDeps.Telemetry is not wired.
func rrPlacementOracle(t *testing.T, run uuid.UUID, model string, tel map[runner.RunnerID]runner.Telemetry, ids ...runner.RunnerID) runner.RunnerID {
	t.Helper()
	snap := runner.PoolSnapshot{
		SlotsInUse: map[runner.RunnerID]int{}, SourceInFlight: map[runner.SourceID]int{},
		Telemetry: tel, Homes: map[uuid.UUID]runner.Home{}, Now: time.Now().UTC(),
	}
	for _, id := range ids {
		snap.Runners = append(snap.Runners, runner.Runner{ID: id, Name: string(id), StepLimit: 1,
			Compat: runner.Compatibility{State: runner.CompatCompatible}, Liveness: runner.LivenessOnline})
		snap.Sources = append(snap.Sources, runner.Source{ID: runner.MakeSourceID(id, runner.CredAnthropicSubscription, ""),
			RunnerID: id, Kind: runner.CredAnthropicSubscription, Families: []modelidentity.Family{"anthropic"},
			Capabilities: rrChatCapabilities(rrFP, model),
			Health:       runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable}})
	}
	d := runner.Place(runner.ExecRequirements{
		Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Model: model, Op: runner.OpChat, Affinity: runner.AffinityNone, RunID: run,
		Provider: "claude-cli", EndpointFingerprint: rrFP, RequireExecutedModel: true,
	}, snap, runner.PlacementPolicy{})
	require.True(t, d.Placed, d.Reason)
	return d.Runner
}
