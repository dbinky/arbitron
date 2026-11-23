package runner_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
)

func freshnessPool() *tierPool {
	return newTierPool().runner("mac-1").runner("mac-2").home("mac-1")
}

func TestPlaceWorkspace_StaleDiskCannotForceMove(t *testing.T) {
	p := freshnessPool()
	p.snap.Telemetry["mac-1"] = runner.Telemetry{SampledAt: p.snap.Now.Add(-16 * time.Second), DiskFreeBytes: tierU64p(1)}
	d := runner.Place(tierAgentReq(1), p.snap, tierPolicy)
	assert.Equal(t, runner.RunnerID("mac-1"), d.Runner)
	assert.Equal(t, runner.TierStay, d.Tier)
}

func TestPlaceWorkspace_ZeroAndFuturePressureAreUnknown(t *testing.T) {
	for _, offset := range []time.Duration{0, time.Second} {
		p := freshnessPool()
		stamp := time.Time{}
		if offset != 0 {
			stamp = p.snap.Now.Add(offset)
		}
		p.snap.Telemetry["mac-1"] = runner.Telemetry{SampledAt: stamp, MemPressure: tierStrp("critical"), Thermal: tierStrp("serious")}
		d := runner.Place(tierAgentReq(1), p.snap, tierPolicy)
		assert.Equal(t, runner.RunnerID("mac-1"), d.Runner)
		assert.Equal(t, runner.TierStay, d.Tier)
	}
}

func TestPlaceWorkspace_FreshDiskAndPressureApplyTiers(t *testing.T) {
	for _, tc := range []struct {
		tel  runner.Telemetry
		tier string
	}{
		{runner.Telemetry{DiskFreeBytes: tierU64p(1)}, runner.TierMustMove},
		{runner.Telemetry{MemPressure: tierStrp("critical")}, runner.TierMayMoveLoad},
	} {
		p := freshnessPool().tel("mac-1", tc.tel)
		d := runner.Place(tierAgentReq(1), p.snap, tierPolicy)
		assert.Equal(t, runner.RunnerID("mac-2"), d.Runner)
		assert.Equal(t, tc.tier, d.Tier)
	}
}

func TestPlaceWorkspace_TelemetryExpiresAtConfiguredBoundary(t *testing.T) {
	for _, age := range []time.Duration{0, 7 * time.Second, 7*time.Second + time.Nanosecond} {
		p := freshnessPool()
		p.snap.Telemetry["mac-1"] = runner.Telemetry{SampledAt: p.snap.Now.Add(-age), DiskFreeBytes: tierU64p(1)}
		pol := tierPolicy
		pol.TelemetryMaxAge = 7 * time.Second
		d := runner.Place(tierAgentReq(1), p.snap, pol)
		want := runner.TierMustMove
		if age > pol.TelemetryMaxAge {
			want = runner.TierStay
		}
		assert.Equal(t, want, d.Tier)
		assert.NotNil(t, p.snap.Telemetry["mac-1"].DiskFreeBytes, "Place must not mutate its input")
	}
}

func TestPlaceWorkspace_NonPortableHomeIsBuiltInOnly(t *testing.T) {
	p := newTierPool().runner(runner.BuiltInRunnerID).runner("remote").home("remote").slots(runner.BuiltInRunnerID, 7).
		tel(runner.BuiltInRunnerID, runner.Telemetry{MemPressure: tierStrp("critical")})
	h := p.snap.Homes[tierRun]
	h.NonPortableReason = "missing promisor objects"
	p.snap.Homes[tierRun] = h
	d := runner.Place(tierAgentReq(1), p.snap, tierPolicy)
	assert.True(t, d.Placed)
	assert.Equal(t, runner.BuiltInRunnerID, d.Runner)
	assert.Equal(t, runner.TierStay, d.Tier)
	assert.Contains(t, d.Reason, "non_portable: missing promisor objects;")
}

func TestPlaceWorkspace_NonPortableHomeNeverLoadMoves(t *testing.T) {
	for _, slots := range []int{7, 8} {
		p := newTierPool().runner(runner.BuiltInRunnerID).runner("remote").home(runner.BuiltInRunnerID).slots(runner.BuiltInRunnerID, slots)
		h := p.snap.Homes[tierRun]
		h.NonPortableReason = "missing objects"
		p.snap.Homes[tierRun] = h
		d := runner.Place(tierAgentReq(1), p.snap, tierPolicy)
		assert.Equal(t, runner.TierStay, d.Tier)
		if slots == 8 {
			assert.False(t, d.Placed)
			if assert.NotNil(t, d.Wait) {
				assert.Equal(t, runner.WaitSlot, d.Wait.Kind)
			}
		} else {
			assert.Equal(t, runner.BuiltInRunnerID, d.Runner)
		}
	}
}

func TestPlaceWorkspace_ClearedMarkerRestoresTiers(t *testing.T) {
	p := newTierPool().runner(runner.BuiltInRunnerID).runner("remote").home(runner.BuiltInRunnerID).slots(runner.BuiltInRunnerID, 7)
	h := p.snap.Homes[tierRun]
	h.NonPortableReason = "missing objects"
	p.snap.Homes[tierRun] = h
	assert.Equal(t, runner.BuiltInRunnerID, runner.Place(tierAgentReq(1), p.snap, tierPolicy).Runner)
	h.NonPortableReason = ""
	p.snap.Homes[tierRun] = h
	d := runner.Place(tierAgentReq(1), p.snap, tierPolicy)
	assert.Equal(t, runner.RunnerID("remote"), d.Runner)
	assert.Equal(t, runner.TierMayMoveLoad, d.Tier)
}
