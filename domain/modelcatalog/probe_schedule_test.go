package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Ignoring the persisted path/config cursor retries o2 at every restart when
// observations have the same timestamp. A changed configuration must start fresh.
func TestSelectorConsumesCursorWithinPathAndResetsForNewConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, config, want string }{
		{"same configuration resumes after o2", "cs", "solo/x"},
		{"new configuration ignores old cursor", "new", "solo/o1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := discoveryFixture()
			in.Profiles = nil
			in.Paths.Paths = []ExecutionPath{{Key: "solo", ConfigRevision: tc.config, RouteProvider: "v"}}
			in.ProbeCursor = map[string]string{"solo@cs": "v/o2"}
			// No usable evidence; the cursor is scheduling position, never proof.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			probe := &cancelAfterProbe{stubProbe: &stubProbe{states: map[string]string{"o2@solo": UsabilityUnknown, "y@solo": UsabilityUnknown}}, after: tc.want, cancel: cancel}
			out := (&AdvanceSelector{Probe: probe}).Select(ctx, in)
			require.NotEmpty(t, probe.calls)
			queue := probeQueue(in, map[evidenceKey]UsabilityEvidence{}, time.Now())
			assert.Equal(t, tc.want, queue[0].path.Key+"/"+queue[0].entry.SpecificModel, "the persisted cursor determines fair dispatch order")
			assert.Empty(t, out.Advances)
			assert.True(t, out.DiscoveryExhausted)
		})
	}
}

type deadlineProbe struct{ calls atomic.Int32 }

func (p *deadlineProbe) Check(ctx context.Context, path ExecutionPath, cand modelidentity.Entry) (UsabilityEvidence, error) {
	p.calls.Add(1)
	<-ctx.Done()
	return UsabilityEvidence{}, ctx.Err()
}

// Discarding a timed-out attempt's position/unknown evidence allows that same
// hung path to consume every later budget before any other path is considered.
func TestSelectorRetainsUnknownAttemptOnBudgetDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	in := discoveryFixture()
	probe := &deadlineProbe{}
	out := (&AdvanceSelector{Probe: probe}).Select(ctx, in)
	require.GreaterOrEqual(t, probe.calls.Load(), int32(1))
	require.LessOrEqual(t, probe.calls.Load(), int32(probeWorkers))
	require.NotEmpty(t, out.Evidence)
	assert.Equal(t, UsabilityUnknown, out.Evidence[0].State)
	assert.False(t, out.Evidence[0].Confirmed())
	assert.NotEmpty(t, out.Cursor)
	assert.True(t, out.Exhausted)
	assert.Empty(t, out.Advances)
}

// Failed checks must release their scheduling turn even when the error is not
// a timeout, and nil/empty state must remain safe on an already cancelled scan.
func TestSelectorProbeErrorRecordsUnknownAndCancelledScanDoesNotAttempt(t *testing.T) {
	in := selectionFixture("x", nil, entry("x", "1", ""), entry("y", "2", ""))
	out := (&AdvanceSelector{Probe: &stubProbe{err: errors.New("failure")}}).Select(context.Background(), in)
	require.Len(t, out.Evidence, 4)
	for _, ev := range out.Evidence {
		assert.Equal(t, UsabilityUnknown, ev.State)
	}
	assert.Empty(t, out.Advances)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := &stubProbe{}
	out = (&AdvanceSelector{Probe: probe}).Select(ctx, in)
	assert.Empty(t, probe.calls)
	assert.Empty(t, out.Evidence)
	assert.Empty(t, out.Cursor)
	assert.Empty(t, out.Advances)
	assert.Empty(t, (&AdvanceSelector{}).Select(ctx, SelectionInput{}).Advances)
}

// A pi route with no connection runs only OAuth providers named by the model's
// own prefix (adapter/pi/env.go:resolveDescriptor); the anthropic catalog's
// spellings carry none, so every such check is refused before launch and only
// re-records unknown each cycle. The Claude subscription routes that share the
// claude-cli route provider keep their anthropic source candidates.
func TestProbeQueueSkipsAnthropicCatalogOnPiSubscriptionPath(t *testing.T) {
	in := SelectionInput{Catalog: CatalogSnapshot{Slices: []CatalogSlice{{Provider: "anthropic", Status: SliceStatusOK, Entries: []modelidentity.Entry{
		{CanonicalID: "anthropic/claude-sonnet-4-6", Provider: "anthropic", NativeID: "claude-sonnet-4-6", SpecificModel: "claude-sonnet-4-6"},
	}}}}}
	for _, tc := range []struct {
		backend string
		want    bool
	}{
		{backend: "", want: true},
		{backend: "claude-code", want: true},
		{backend: "pi", want: false},
	} {
		t.Run("backend="+tc.backend, func(t *testing.T) {
			in.Paths = PathSnapshot{Paths: []ExecutionPath{{Key: "sub-" + tc.backend, ConfigRevision: "r", Backend: tc.backend, RouteProvider: "claude-cli"}}}
			queue := probeQueue(in, map[evidenceKey]UsabilityEvidence{}, time.Now())
			assert.Equal(t, tc.want, len(queue) == 1)
		})
	}
	// A connection-backed pi route still enumerates its connection's catalog.
	in.Paths = PathSnapshot{Paths: []ExecutionPath{{Key: "pi-conn", ConfigRevision: "r", Backend: "pi", ConnectionRef: "c", RouteProvider: "anthropic"}}}
	assert.Len(t, probeQueue(in, map[evidenceKey]UsabilityEvidence{}, time.Now()), 1)
}

// SECURITY — the remote catalog decides how many ids a provider slice carries,
// and every check is a paid turn. One path runs at most maxProbesPerPathPerCycle
// per cycle, follower checks first; the cursor rotates the rest.
func TestProbeQueueCapsChecksPerPathPerCycle(t *testing.T) {
	var many []modelidentity.Entry
	for i := range 3 * maxProbesPerPathPerCycle {
		e := entry(fmt.Sprintf("m%02d", i), fmt.Sprint(i+1), "")
		e.Tier = "other"
		many = append(many, e)
	}
	follow := entry("zz-new", "999", "")
	in := selectionFixture("cur", nil, append(many, entry("cur", "1", ""), follow)...)
	in.Paths.Paths = []ExecutionPath{{Key: "p1", ConfigRevision: "c1", RouteProvider: "v", Aliases: []string{"w"}}}
	queue := probeQueue(in, map[evidenceKey]UsabilityEvidence{}, time.Now())
	require.Len(t, queue, maxProbesPerPathPerCycle)
	var followers int
	for _, w := range queue {
		if w.follower {
			followers++
		}
	}
	assert.Equal(t, 1, followers, "the follower candidate always takes a slot")
	assert.True(t, slices.ContainsFunc(queue, func(w probeWork) bool { return w.entry.SpecificModel == "zz-new" }))

	probe := &stubProbe{states: map[string]string{}}
	(&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
	assert.LessOrEqual(t, len(probe.calls), maxProbesPerPathPerCycle)
}

// SECURITY — failed/unknown evidence is retried with exponential backoff and
// retired after maxProbeFailures, so a nonexistent id is not probed forever.
func TestProbeQueueBacksOffAndRetiresFailingChecks(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	o := entry("o1", "1", "")
	o.Tier = "other"
	in := SelectionInput{Catalog: CatalogSnapshot{Slices: []CatalogSlice{{Provider: "v", Status: SliceStatusOK, Entries: []modelidentity.Entry{o}}}},
		Paths: PathSnapshot{Paths: []ExecutionPath{{Key: "solo", ConfigRevision: "cs", RouteProvider: "v"}}}}
	stored := func(failures int, age time.Duration) []UsabilityEvidence {
		return []UsabilityEvidence{{CanonicalID: o.CanonicalID, PathKey: "solo", ConfigRevision: "cs", NativeModel: "o1",
			State: UsabilityUnknown, CheckedAt: now.Add(-age), Failures: failures}}
	}
	for _, tc := range []struct {
		name     string
		failures int
		age      time.Duration
		due      bool
	}{
		{"legacy evidence without a count", 0, time.Minute, true},
		{"first failure inside the base delay", 1, probeRetryBase - time.Minute, false},
		{"first failure after the base delay", 1, probeRetryBase, true},
		{"third failure waits four base delays", 3, 3 * probeRetryBase, false},
		{"third failure after four base delays", 3, 4 * probeRetryBase, true},
		{"retired", maxProbeFailures, 365 * 24 * time.Hour, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in.Stored = stored(tc.failures, tc.age)
			assert.Equal(t, tc.due, len(probeQueue(in, map[evidenceKey]UsabilityEvidence{}, now)) == 1)
		})
	}
	// A new configuration revision starts over.
	in.Stored = stored(maxProbeFailures, time.Minute)
	in.Paths.Paths[0].ConfigRevision = "new"
	assert.Len(t, probeQueue(in, map[evidenceKey]UsabilityEvidence{}, now), 1)

	// Each non-confirming check increments the count; a confirmation clears it.
	in.Paths.Paths[0].ConfigRevision = "cs"
	in.Stored = stored(2, 10*probeRetryBase)
	out := (&AdvanceSelector{Probe: &stubProbe{states: map[string]string{"o1@solo": UsabilityUnknown}}, Now: func() time.Time { return now }}).Select(context.Background(), in)
	require.Len(t, out.Evidence, 1)
	assert.Equal(t, 3, out.Evidence[0].Failures)
	out = (&AdvanceSelector{Probe: &stubProbe{}, Now: func() time.Time { return now }}).Select(context.Background(), in)
	require.Len(t, out.Evidence, 1)
	assert.Equal(t, 0, out.Evidence[0].Failures)
}
