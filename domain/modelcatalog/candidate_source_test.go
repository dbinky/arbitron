package modelcatalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCandidateInventory struct {
	snap    PathSnapshot
	reqs    []modelidentity.VersionRequest
	err     error
	profile provider.Profile
}

func (f *fakeCandidateInventory) PathsForProfile(_ context.Context, p provider.Profile) (PathSnapshot, error) {
	f.profile = p
	return f.snap, f.err
}

func (f *fakeCandidateInventory) Snapshot(context.Context) (PathSnapshot, error) {
	return f.snap, f.err
}

func (f *fakeCandidateInventory) VersionRequests(context.Context, provider.Profile) ([]modelidentity.VersionRequest, error) {
	return f.reqs, f.err
}

type fakeCatalogView struct {
	CatalogStore
	view CatalogView
	err  error
}

func (f *fakeCatalogView) Snapshot(context.Context) (CatalogView, error) { return f.view, f.err }
func (f *fakeCatalogView) CommitCycle(_ context.Context, c CycleCommit) (RefreshReport, error) {
	f.view.Evidence = append(f.view.Evidence, c.Evidence...)
	return c.Report, f.err
}
func (f *fakeCatalogView) MarkPublished(context.Context, string) error { return f.err }

type candidateNormalizer struct{ store *fakeCatalogView }

func (n candidateNormalizer) Normalize(context.Context, string, []byte) (CatalogSnapshot, error) {
	return n.store.view.Catalog, nil
}

type candidateFetcher struct{}

func (candidateFetcher) Fetch(context.Context, string) ([]byte, error) { return []byte(`{}`), nil }

type candidateProfiles struct{}

func (candidateProfiles) ListInternal(context.Context) ([]provider.Profile, error) { return nil, nil }

type candidateFixture struct {
	svc   *CandidateService
	inv   *fakeCandidateInventory
	store *fakeCatalogView
	probe *stubProbe
}

func newCandidateFixture(t *testing.T, entries ...modelidentity.Entry) candidateFixture {
	t.Helper()
	inv := &fakeCandidateInventory{
		snap: PathSnapshot{Revision: "rev", ExecutionRevision: provider.ExecutionRevision{ID: "rev", PolicyRevision: 1},
			Paths: []ExecutionPath{{Key: "p1", ConfigRevision: "c1", RouteProvider: "v", Aliases: []string{"w"}}, {Key: "p2", ConfigRevision: "c2", RouteProvider: "v", Aliases: []string{"w"}}}},
		reqs: []modelidentity.VersionRequest{{Model: "z", RouteProvider: "v"}},
	}
	store := &fakeCatalogView{view: CatalogView{Catalog: CatalogSnapshot{Slices: []CatalogSlice{{Provider: "v", Status: SliceStatusOK, Entries: entries}}}}}
	probe := &stubProbe{}
	pipeline, err := NewDiscoveryPipeline(DiscoveryDeps{Settings: faultSettings{}, Fetcher: candidateFetcher{}, Normalizer: candidateNormalizer{store}, Mapper: struct{ EntryMapper }{}, Inventory: inv, Profiles: candidateProfiles{}, Store: store, Probe: probe, AdvanceValidator: allowAdvance{}})
	require.NoError(t, err)
	coordinator := NewRefreshCoordinator(func(context.Context) (RefreshReport, error) { return RefreshReport{}, nil })
	t.Cleanup(coordinator.Close)
	svc, err := NewCandidateService(CandidateDeps{Store: store, Inventory: inv, Pipeline: pipeline, Coordinator: coordinator, Gate: readGate{}, Mapper: struct{ EntryMapper }{}, Clock: func() time.Time { return time.Unix(5, 0) }})
	require.NoError(t, err)
	return candidateFixture{svc: svc, inv: inv, store: store, probe: probe}
}

func followProfile(model string) provider.Profile {
	return provider.Profile{Alias: "w", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "v", Tier: "t"}, Model: model}
}

var _ provider.EligibleCandidateSource = (*CandidateService)(nil)

func TestCandidateServiceRequiresEveryCollaborator(t *testing.T) {
	f := newCandidateFixture(t)
	for name, omit := range map[string]func(*CandidateDeps){
		"store":       func(d *CandidateDeps) { d.Store = nil },
		"inventory":   func(d *CandidateDeps) { d.Inventory = nil },
		"pipeline":    func(d *CandidateDeps) { d.Pipeline = nil },
		"coordinator": func(d *CandidateDeps) { d.Coordinator = nil },
		"gate":        func(d *CandidateDeps) { d.Gate = nil },
		"mapper":      func(d *CandidateDeps) { d.Mapper = nil },
	} {
		d := f.svc.d
		omit(&d)
		_, err := NewCandidateService(d)
		assert.ErrorIs(t, err, errNilCollaborator, name)
	}
}

func TestNewestChoosesNewestModelUsableOnEveryProspectiveRoute(t *testing.T) {
	x, y, z := entry("x", "1", ""), entry("y", "2", ""), entry("z", "3", "")
	f := newCandidateFixture(t, x, y, z)

	c, err := f.svc.Newest(context.Background(), followProfile(""))
	require.NoError(t, err)
	assert.Equal(t, "z", c.Model)
	assert.Equal(t, "v/z", c.CanonicalID)
	assert.Equal(t, "rev", c.PathRevision)
	assert.Equal(t, "rev", c.ExecutionRevision.ID)
	assert.Equal(t, int64(1), c.ExecutionRevision.PolicyRevision)
	assert.Equal(t, "w", f.inv.profile.Alias, "the prospective profile, not a stored one, is proved")

	f = newCandidateFixture(t, x, y, z)
	f.probe.states = map[string]string{"z@p2": UsabilityUnknown}
	c, err = f.svc.Newest(context.Background(), followProfile(""))
	require.NoError(t, err)
	assert.Equal(t, "y", c.Model, "a model unusable on one route falls back to the next newest")
}

func TestCandidateNewestDoesNotHideFourthUsableRelease(t *testing.T) {
	f := newCandidateFixture(t, entry("a", "1", ""), entry("b", "2", ""), entry("c", "3", ""), entry("d", "4", ""))
	f.probe.states = map[string]string{"d@p1": UsabilityUnavailable, "c@p1": UsabilityUnavailable, "b@p1": UsabilityUnknown}
	cand, err := f.svc.Newest(context.Background(), followProfile(""))
	require.NoError(t, err)
	assert.Equal(t, "a", cand.Model)
	require.NoError(t, f.svc.RevalidateStart(context.Background(), followProfile("a"), cand))
}

func TestNewestFailsClosedWhenNothingIsUsable(t *testing.T) {
	x := entry("x", "1", "")
	other := entry("o", "9", "")
	other.Tier = "other"
	stale := entry("s", "8", "")
	stale.PolicyRevision = 2
	mutable := entry("m", "7", "")
	mutable.Immutable, mutable.SpecificModel = false, ""
	for name, tc := range map[string]struct {
		entries  []modelidentity.Entry
		states   map[string]string
		probeErr error
	}{
		"empty catalog":                  {},
		"unusable on a route":            {entries: []modelidentity.Entry{x}, states: map[string]string{"x@p1": UsabilityUnavailable}},
		"probe fails":                    {entries: []modelidentity.Entry{x}, probeErr: errors.New("boom")},
		"other tier or stale or mutable": {entries: []modelidentity.Entry{other, stale, mutable}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCandidateFixture(t, tc.entries...)
			f.probe.states, f.probe.err = tc.states, tc.probeErr
			_, err := f.svc.Newest(context.Background(), followProfile(""))
			if name == "other tier or stale or mutable" {
				assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
			} else {
				assert.ErrorIs(t, err, provider.ErrNoEligibleCandidate)
			}
		})
	}
}

func TestNewestNeverTreatsAMissingInventoryAsVacuouslyUsable(t *testing.T) {
	f := newCandidateFixture(t, entry("x", "1", ""))
	f.inv.snap.Paths = nil
	_, err := f.svc.Newest(context.Background(), followProfile(""))
	assert.ErrorIs(t, err, provider.ErrExecutionRevisionUnavailable)
	assert.Empty(t, f.probe.calls)

	f = newCandidateFixture(t, entry("x", "1", ""))
	f.inv.err = ErrInventoryIncomplete
	_, err = f.svc.Newest(context.Background(), followProfile(""))
	assert.ErrorIs(t, err, ErrInventoryIncomplete)
	assert.NotErrorIs(t, err, provider.ErrNoEligibleCandidate)

	f = newCandidateFixture(t, entry("x", "1", ""))
	f.store.err = errors.New("db down")
	_, err = f.svc.Newest(context.Background(), followProfile(""))
	assert.Error(t, err)
	assert.NotErrorIs(t, err, provider.ErrNoEligibleCandidate)
}

func TestNewestRejectsPinnedOrTargetlessProfileAndCancelledContext(t *testing.T) {
	f := newCandidateFixture(t, entry("x", "1", ""))
	p := followProfile("")
	p.FollowTarget = nil
	_, err := f.svc.Newest(context.Background(), p)
	assert.ErrorIs(t, err, provider.ErrInvalidSelection)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = f.svc.Newest(ctx, followProfile(""))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestValidateStartProvesTheSuppliedModelWithoutSubstitutingANewerOne(t *testing.T) {
	x, y := entry("x", "1", ""), entry("y", "2", "")
	f := newCandidateFixture(t, x, y)

	c, err := f.svc.ValidateStart(context.Background(), followProfile("x"))
	require.NoError(t, err)
	assert.Equal(t, "x", c.Model, "newer y is confirmed too, yet x is what was asked for")
	assert.Equal(t, "v/x", c.CanonicalID)
	assert.Equal(t, "rev", c.PathRevision)

	_, err = f.svc.ValidateStart(context.Background(), followProfile("unlisted"))
	assert.ErrorIs(t, err, provider.ErrNoEligibleCandidate)

	f.probe.states = map[string]string{"x@p2": UsabilityUnknown}
	f2 := newCandidateFixture(t, x, y)
	f2.probe.states = map[string]string{"x@p2": UsabilityUnknown}
	_, err = f2.svc.ValidateStart(context.Background(), followProfile("x"))
	assert.ErrorIs(t, err, provider.ErrNoEligibleCandidate)
}

func TestValidateStartRefusesWrongTierAndNonFixedSpelling(t *testing.T) {
	other := entry("o", "1", "")
	other.Tier = "other"
	alias := entry("latest", "1", "")
	alias.NativeID, alias.SpecificModel, alias.Immutable = "latest", "latest-2026", true
	f := newCandidateFixture(t, other, alias)
	_, err := f.svc.ValidateStart(context.Background(), followProfile("o"))
	assert.ErrorIs(t, err, provider.ErrNoEligibleCandidate, "a model outside the follow tier is not a start")
	f = newCandidateFixture(t, alias)
	_, err = f.svc.ValidateStart(context.Background(), followProfile("latest"))
	assert.ErrorIs(t, err, provider.ErrInvalidSelection, "only the fixed spelling may start a following profile")
	assert.NotContains(t, f.probe.calls, "p1/latest-2026", "invalid explicit spelling cannot probe a substitute")
}

func TestValidateStartCanProveAHistoricalModelAbsentFromTheLatestSlice(t *testing.T) {
	f := newCandidateFixture(t, entry("y", "2", ""))
	f.store.view.HistoricalEntries = []modelidentity.Entry{entry("x", "1", "")}
	c, err := f.svc.ValidateStart(context.Background(), followProfile("x"))
	require.NoError(t, err)
	assert.Equal(t, "x", c.Model)
}

func TestValidateStartReusesStoredConfirmation(t *testing.T) {
	x := entry("x", "1", "")
	f := newCandidateFixture(t, x)
	for _, p := range f.inv.snap.Paths {
		f.store.view.Evidence = append(f.store.view.Evidence, UsabilityEvidence{CanonicalID: x.CanonicalID, PathKey: p.Key, ConfigRevision: p.ConfigRevision, NativeModel: "x", ProofRef: "p", CheckedAt: time.Unix(1, 0), State: UsabilityConfirmed})
	}
	_, err := f.svc.ValidateStart(context.Background(), followProfile("x"))
	require.NoError(t, err)
	assert.Empty(t, f.probe.calls, "a stored confirmation for the exact path configuration is never re-probed")
}

func TestVersionRequestsComeFromTheInventoryAndFailWhenEmpty(t *testing.T) {
	f := newCandidateFixture(t)
	reqs, err := f.svc.VersionRequests(context.Background(), followProfile("z"))
	require.NoError(t, err)
	assert.Equal(t, f.inv.reqs, reqs)

	f.inv.reqs = nil
	_, err = f.svc.VersionRequests(context.Background(), followProfile("z"))
	assert.ErrorIs(t, err, provider.ErrExecutionRevisionUnavailable)

	f.inv.err = ErrInventoryIncomplete
	_, err = f.svc.VersionRequests(context.Background(), followProfile("z"))
	assert.ErrorIs(t, err, ErrInventoryIncomplete)
}

func TestRevalidateStartIsReadOnlyAndRejectsAChangedWorld(t *testing.T) {
	x := entry("x", "1", "")
	f := newCandidateFixture(t, x)
	c, err := f.svc.ValidateStart(context.Background(), followProfile("x"))
	require.NoError(t, err)
	calls := len(f.probe.calls)

	require.NoError(t, f.svc.RevalidateStart(context.Background(), followProfile("x"), c))
	assert.Len(t, f.probe.calls, calls, "revalidation does no probing")

	f.inv.snap.Revision, f.inv.snap.ExecutionRevision.ID = "rev2", "rev2"
	assert.ErrorIs(t, f.svc.RevalidateStart(context.Background(), followProfile("x"), c), provider.ErrExecutionRevisionStale)
	f.inv.snap.Revision, f.inv.snap.ExecutionRevision.ID = "rev", "rev"

	assert.Error(t, f.svc.RevalidateStart(context.Background(), followProfile("y"), c), "proof is for model x only")

	f.inv.snap.Paths[1].ConfigRevision = "rotated"
	f.inv.snap.Revision, f.inv.snap.ExecutionRevision.ID = "rev", "rev"
	assert.ErrorIs(t, f.svc.RevalidateStart(context.Background(), followProfile("x"), c), provider.ErrExecutionRevisionUnavailable, "a rotated route needs fresh proof")
}

func TestRevalidateStartWithoutAnyProofFails(t *testing.T) {
	f := newCandidateFixture(t, entry("x", "1", ""))
	cand := provider.EligibleCandidate{Model: "x", CanonicalID: "v/x", PathRevision: "rev", ExecutionRevision: provider.ExecutionRevision{ID: "rev", PolicyRevision: 1}}
	assert.ErrorIs(t, f.svc.RevalidateStart(context.Background(), followProfile("x"), cand), provider.ErrExecutionRevisionUnavailable)
}
