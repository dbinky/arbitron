package modelcatalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Failed native-only checks must retain the attempted spelling even though
// policy classification supplies no SpecificModel. Empty probe output must
// never turn into usable evidence or lose the model being diagnosed.
func TestDiscoveryNativeProbeFailureRetainsAttemptedModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		ev   UsabilityEvidence
	}{
		{name: "adapter error", err: errors.New("probe failed")},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "missing identity"},
		{name: "empty native model", ev: UsabilityEvidence{CanonicalID: "v/native-preview", PathKey: "native-path", ConfigRevision: "config", State: UsabilityUnknown}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := SelectionInput{
				Catalog: CatalogSnapshot{Slices: []CatalogSlice{{Provider: "v", Status: SliceStatusOK,
					Entries: []modelidentity.Entry{{Provider: "v", NativeID: "native-preview", CanonicalID: "v/native-preview"}},
				}}},
				Paths: PathSnapshot{Paths: []ExecutionPath{{Key: "native-path", ConfigRevision: "config", RouteProvider: "v"}}},
			}
			out := (&AdvanceSelector{Probe: nativeFailureProbe{ev: tc.ev, err: tc.err}}).Select(context.Background(), in)
			require.Len(t, out.Evidence, 1)
			ev := out.Evidence[0]
			assert.Equal(t, "native-preview", ev.NativeModel)
			assert.Equal(t, "v/native-preview", ev.CanonicalID)
			assert.Equal(t, "native-path", ev.PathKey)
			assert.Equal(t, "config", ev.ConfigRevision)
			assert.Equal(t, UsabilityUnknown, ev.State)
			assert.False(t, ev.Confirmed())
			assert.False(t, ev.CheckedAt.IsZero())
			assert.Equal(t, "v/native-preview", out.Cursor["native-path@config"])
			assert.Empty(t, out.Advances)
		})
	}
}

type nativeFailureProbe struct {
	ev  UsabilityEvidence
	err error
}

func (p nativeFailureProbe) Check(_ context.Context, _ ExecutionPath, _ modelidentity.Entry) (UsabilityEvidence, error) {
	return p.ev, p.err
}

func TestDiscoveryAllSourceCandidatesOnEveryPath(t *testing.T) {
	in := discoveryFixture()
	unknown := modelidentity.Entry{CanonicalID: "v/native-snapshot", Provider: "v", NativeID: "native-snapshot", PolicyRevision: 1}
	in.Catalog.Slices[0].Entries = append(in.Catalog.Slices[0].Entries, unknown)
	in.Paths.Paths = append(in.Paths.Paths, ExecutionPath{Key: "subscription", ConfigRevision: "account", RouteProvider: "v", Backend: "claude-code"})
	probe := &nativeCheckingProbe{}
	out := (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
	for _, path := range []string{"p1", "solo", "subscription"} {
		for _, model := range []string{"x", "y", "o1", "o2", "native-snapshot"} {
			assert.Contains(t, probe.calls, path+"/"+model)
		}
	}
	require.Len(t, out.Evidence, 15)
	require.Len(t, out.Advances, 1)
	assert.Equal(t, "y", out.Advances[0].Model)
	for _, ev := range out.Evidence {
		assert.True(t, ev.Confirmed(), "%+v", ev)
	}
}

type nativeCheckingProbe struct {
	mu    sync.Mutex
	calls []string
}

func (p *nativeCheckingProbe) Check(_ context.Context, path ExecutionPath, e modelidentity.Entry) (UsabilityEvidence, error) {
	model := e.SpecificModel
	if model == "" {
		model = e.NativeID
	}
	p.mu.Lock()
	p.calls = append(p.calls, path.Key+"/"+model)
	p.mu.Unlock()
	return UsabilityEvidence{CanonicalID: e.CanonicalID, PathKey: path.Key, ConfigRevision: path.ConfigRevision, NativeModel: model, State: UsabilityConfirmed, ProofRef: "exact", CheckedAt: time.Unix(1, 0)}, nil
}

func TestDiscoveryRejectsWrongExecutedModelAndD8Confirmation(t *testing.T) {
	for _, route := range []string{"v", "azure", "bedrock"} {
		in := selectionFixture("x", nil, entry("x", "1", ""), entry("y", "2", ""))
		in.Paths.Paths = in.Paths.Paths[:1]
		in.Paths.Paths[0].RouteProvider = route
		out := (&AdvanceSelector{Probe: wrongModelProbe{}}).Select(context.Background(), in)
		assert.Empty(t, out.Advances)
		for _, ev := range out.Evidence {
			assert.False(t, ev.Confirmed())
		}
	}
}

type wrongModelProbe struct{}

func (wrongModelProbe) Check(ctx context.Context, p ExecutionPath, e modelidentity.Entry) (UsabilityEvidence, error) {
	ev, _ := (&nativeCheckingProbe{}).Check(ctx, p, e)
	ev.NativeModel = "fallback"
	return ev, nil
}

func TestDiscoveryDoesNotReuseMismatchedOrOpaqueStoredProof(t *testing.T) {
	for _, route := range []string{"v", "azure", "bedrock"} {
		in := selectionFixture("x", nil, entry("x", "1", ""), entry("y", "2", ""))
		in.Paths.Paths = in.Paths.Paths[:1]
		in.Paths.Paths[0].RouteProvider = route
		ev, _ := (&nativeCheckingProbe{}).Check(context.Background(), in.Paths.Paths[0], entry("y", "2", ""))
		if route == "v" {
			ev.NativeModel = "fallback"
		}
		in.Stored = []UsabilityEvidence{ev}
		probe := &stubProbe{err: context.DeadlineExceeded}
		out := (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
		assert.Empty(t, out.Advances, route)
		assert.Contains(t, probe.calls, "p1/y", "a mismatched stored proof cannot skip verification")
	}
}

func TestCandidateSelectionClassifiesUnresolvedCurrentAndRoutes(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		edit         func(*SelectionInput)
	}{
		{"unknown current", "unknown_current", func(in *SelectionInput) { in.Profiles[0].Model = "gone" }},
		{"ambiguous current", "ambiguous_identity", func(in *SelectionInput) { in.Known[0].Ambiguous = true }},
		{"no routes", "unavailable_paths", func(in *SelectionInput) { in.Paths.Paths = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := selectionFixture("x", nil, entry("x", "1", ""), entry("y", "2", ""))
			tc.edit(&in)
			out := (&AdvanceSelector{Probe: &stubProbe{}}).Select(context.Background(), in)
			assert.Empty(t, out.Advances)
			assert.Equal(t, tc.reason, out.PathStatus["profile:w"])
			public := ProjectReport(RefreshReport{PathStatus: out.PathStatus})
			assert.Equal(t, tc.reason, public.PathStatus["profile:w"])
		})
	}
}

func TestDiscoveryPathStatusRetainsEarlierUnknownCheck(t *testing.T) {
	in := discoveryFixture()
	out := (&AdvanceSelector{Probe: &stubProbe{states: map[string]string{"y@p1": UsabilityUnknown}}}).Select(context.Background(), in)
	assert.Equal(t, UsabilityUnknown, out.PathStatus["path:p1"])
	assert.Empty(t, out.Advances)
}

func TestCandidateSelectionDoesNotHideFourthEligibleRelease(t *testing.T) {
	in := selectionFixture("x", nil, entry("x", "1", ""), entry("a", "2", ""), entry("b", "3", ""), entry("c", "4", ""), entry("d", "5", ""))
	probe := &stubProbe{states: map[string]string{"d@p1": UsabilityUnavailable, "c@p1": UsabilityUnavailable, "b@p1": UsabilityUnavailable}}
	out := (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
	require.Len(t, out.Advances, 1)
	assert.Equal(t, "a", out.Advances[0].Model)
}
