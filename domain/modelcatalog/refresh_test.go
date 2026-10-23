package modelcatalog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/settings"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type allowAdvance struct{}

func (allowAdvance) ValidateAdvance(_ context.Context, req provider.AdvanceRequest) error {
	if req.Model == "" || req.Expected.Alias == "" || req.PathRevision == "" || req.ExecutionRevision.ID != req.PathRevision {
		return provider.ErrInvalidSelection
	}
	return nil
}

type stubProbe struct {
	mu     sync.Mutex
	calls  []string
	states map[string]string // native model -> state; absent = confirmed
	err    error
}

func (p *stubProbe) Check(_ context.Context, path ExecutionPath, e modelidentity.Entry) (UsabilityEvidence, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, path.Key+"/"+e.SpecificModel)
	if p.err != nil {
		return UsabilityEvidence{}, p.err
	}
	state := UsabilityConfirmed
	if s, ok := p.states[e.SpecificModel+"@"+path.Key]; ok {
		state = s
	}
	return UsabilityEvidence{CanonicalID: e.CanonicalID, PathKey: path.Key, ConfigRevision: path.ConfigRevision, NativeModel: e.SpecificModel, ProofRef: "proof", CheckedAt: time.Unix(1, 0), State: state}, nil
}

func entry(native, order, date string) modelidentity.Entry {
	return modelidentity.Entry{CanonicalID: "v/" + native, Provider: "v", NativeID: native, SpecificModel: native, Tier: "t", OrderKey: order, ReleaseDate: date, Immutable: true, PolicyRevision: 1}
}

func selectionFixture(current string, profile func(*provider.Profile), entries ...modelidentity.Entry) SelectionInput {
	p := provider.Profile{Alias: "w", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "v", Tier: "t"}, Model: current, Revision: 3}
	if profile != nil {
		profile(&p)
	}
	return SelectionInput{
		RefreshID: "r", Profiles: []provider.Profile{p},
		Catalog: CatalogSnapshot{Slices: []CatalogSlice{{Provider: "v", Status: SliceStatusOK, Entries: entries}}},
		Known:   entries,
		Paths: PathSnapshot{Revision: "rev", ExecutionRevision: provider.ExecutionRevision{ID: "rev", PolicyRevision: 1},
			Paths: []ExecutionPath{{Key: "p1", ConfigRevision: "c1", RouteProvider: "v", Aliases: []string{"w"}}, {Key: "p2", ConfigRevision: "c2", RouteProvider: "v", Aliases: []string{"w"}}}},
	}
}

func TestSelectorAdvancesOnlyToNewerModelConfirmedOnEveryPath(t *testing.T) {
	x, y, z := entry("x", "1", ""), entry("y", "2", ""), entry("z", "3", "")
	for _, tc := range []struct {
		name   string
		states map[string]string
		want   string
	}{
		{"newest confirmed on both paths", nil, "z"},
		{"newest unknown on one path falls back to the next newer", map[string]string{"z@p2": UsabilityUnknown}, "y"},
		{"nothing newer is usable everywhere", map[string]string{"z@p1": UsabilityUnavailable, "y@p2": UsabilityUnknown}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel := (&AdvanceSelector{Probe: &stubProbe{states: tc.states}}).Select(context.Background(), selectionFixture("x", nil, x, y, z))
			if tc.want == "" {
				assert.Empty(t, sel.Advances)
				return
			}
			require.Len(t, sel.Advances, 1)
			assert.Equal(t, tc.want, sel.Advances[0].Model)
			assert.Equal(t, "rev", sel.Advances[0].PathRevision)
			// The sink must be told which (path, revision) proofs the move rests on.
			assert.Equal(t, []provider.PathEvidenceRef{{PathKey: "p1", ConfigRevision: "c1"}, {PathKey: "p2", ConfigRevision: "c2"}}, sel.Advances[0].Evidence)
			assert.Equal(t, int64(3), sel.Advances[0].Expected.Revision)
		})
	}
}

func TestSelectorNeverMovesPinnedUnknownOrUnorderableProfiles(t *testing.T) {
	probe := &stubProbe{}
	sel := &AdvanceSelector{Probe: probe}
	x, y := entry("x", "1", ""), entry("y", "2", "")
	cases := map[string]SelectionInput{
		"pinned":              selectionFixture("x", func(p *provider.Profile) { p.SelectionMode = provider.SelectionPinned; p.FollowTarget = nil }, x, y),
		"current not in feed": selectionFixture("gone", nil, x, y),
		"mutable current":     selectionFixture("x", nil, func() modelidentity.Entry { e := x; e.Immutable = false; return e }(), y),
		"ambiguous current":   selectionFixture("x", nil, func() modelidentity.Entry { e := x; e.Ambiguous = true; return e }(), y),
		"nonfixed current":    selectionFixture("x", nil, func() modelidentity.Entry { e := x; e.SpecificModel = "fixed-x"; return e }(), y),
		"downgrade":           selectionFixture("y", nil, x, y),
		"equal version no date": selectionFixture("x", nil, x, func() modelidentity.Entry {
			e := entry("x2", "1", "")
			return e
		}()),
		"stale policy revision": selectionFixture("x", nil, x, func() modelidentity.Entry { e := y; e.PolicyRevision = 2; return e }()),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, sel.Select(context.Background(), in).Advances)
		})
	}
	assert.NotEmpty(t, probe.calls, "ineligible replacements still participate in installation discovery")
}

func TestSelectorProbeFailureIsUnknownAndCancellationStops(t *testing.T) {
	x, y := entry("x", "1", ""), entry("y", "2", "")
	out := (&AdvanceSelector{Probe: &stubProbe{err: errors.New("boom")}}).Select(context.Background(), selectionFixture("x", nil, x, y))
	assert.Empty(t, out.Advances)
	assert.False(t, out.Exhausted)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out = (&AdvanceSelector{Probe: &stubProbe{}}).Select(ctx, selectionFixture("x", nil, x, y))
	assert.True(t, out.Exhausted)
	assert.Empty(t, out.Advances)
}

func TestSelectorReusesStoredConfirmationAndRecordsUnmatchedTarget(t *testing.T) {
	x, y := entry("x", "1", ""), entry("y", "2", "")
	in := selectionFixture("x", nil, x, y)
	for _, p := range in.Paths.Paths {
		in.Stored = append(in.Stored, UsabilityEvidence{CanonicalID: y.CanonicalID, PathKey: p.Key, ConfigRevision: p.ConfigRevision, NativeModel: "y", ProofRef: "p", CheckedAt: time.Unix(1, 0), State: UsabilityConfirmed})
	}
	probe := &stubProbe{}
	out := (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
	require.Len(t, out.Advances, 1)
	assert.NotContains(t, probe.calls, "p1/y", "a confirmed result is never re-probed")
	assert.NotContains(t, probe.calls, "p2/y", "a confirmed result is never re-probed")

	in.Profiles[0].FollowTarget = &provider.FollowTarget{Provider: "v", Tier: "missing"}
	out = (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
	assert.Equal(t, []provider.FollowTarget{{Provider: "v", Tier: "missing"}}, out.Unmatched)
}

// Joining an active cycle would make overlaps wait until their deadlines instead
// of reporting busy. Starting a second scan would increment runs before release.
func TestCoordinatorRejectsOverlapsAndAllowsNextCycle(t *testing.T) {
	var runs atomic.Int32
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	c := NewRefreshCoordinator(func(ctx context.Context) (RefreshReport, error) {
		runs.Add(1)
		started <- struct{}{}
		select {
		case <-release:
			return RefreshReport{ID: "r"}, nil
		case <-ctx.Done():
			return RefreshReport{}, ctx.Err()
		}
	})
	defer c.Close()
	first := make(chan error, 1)
	go func() {
		r, err := c.Refresh(context.Background())
		if err == nil && r.ID != "r" {
			err = errors.New("initiator did not receive its report")
		}
		first <- err
	}()
	<-started

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r, err := c.Refresh(ctx)
			assert.ErrorIs(t, err, ErrRefreshBusy)
			assert.Empty(t, r.ID, "busy never reports another caller's cycle as success")
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), runs.Load(), "overlaps must not start another scan")
	close(release)
	require.NoError(t, <-first)
	r, err := c.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "r", r.ID)
	assert.Equal(t, int32(2), runs.Load(), "a finished cycle releases the slot")
}

func TestCoordinatorCallerCancellationKeepsFlightUntilCloseJoins(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	allowExit, exited := make(chan struct{}), make(chan struct{})
	c := NewRefreshCoordinator(func(ctx context.Context) (RefreshReport, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-allowExit
		close(exited)
		return RefreshReport{}, ctx.Err()
	})
	// Cleanup also releases a cycle if an assertion aborts the test.
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(allowExit) }) }
	defer c.Close()
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := c.Refresh(ctx); first <- err }()
	<-started
	cancel()
	assert.ErrorIs(t, <-first, context.Canceled)
	select {
	case <-cancelled:
		t.Fatal("caller cancellation cancelled the coordinator's cycle")
	default:
	}
	overlap, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_, err := c.Refresh(overlap)
	assert.ErrorIs(t, err, ErrRefreshBusy)
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	<-cancelled
	select {
	case <-closed:
		t.Fatal("Close returned while the cycle could still access storage")
	default:
	}
	release()
	<-closed
	<-exited
	_, err = c.Refresh(context.Background())
	assert.ErrorIs(t, err, ErrRefreshClosed)
}

func TestLoopOverlappingManualRefreshReturnsBusyAndContinues(t *testing.T) {
	var runs atomic.Int32
	manualStarted := make(chan struct{})
	release := make(chan struct{})
	c := NewRefreshCoordinator(func(ctx context.Context) (RefreshReport, error) {
		if runs.Add(1) == 2 {
			close(manualStarted)
			select {
			case <-release:
			case <-ctx.Done():
				return RefreshReport{}, ctx.Err()
			}
		}
		return RefreshReport{ID: "r"}, nil
	})
	defer c.Close()
	ticks := make(chan time.Time)
	attempts := make(chan error, 3)
	loop := &RefreshLoop{
		Refresh: func(ctx context.Context) (RefreshReport, error) {
			r, err := c.Refresh(ctx)
			attempts <- err
			return r, err
		},
		NewTicker: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); loop.refreshOnce(ctx); loop.RunScheduled(ctx) }()
	defer func() { cancel(); c.Close(); <-done }()
	require.NoError(t, <-attempts, "startup cycle")
	manual := make(chan error, 1)
	go func() { _, err := c.Refresh(context.Background()); manual <- err }()
	<-manualStarted
	ticks <- time.Now()
	select {
	case err := <-attempts:
		assert.ErrorIs(t, err, ErrRefreshBusy)
	case <-time.After(time.Second):
		t.Error("scheduled overlap joined the manual cycle instead of returning busy")
	}
	assert.Equal(t, int32(2), runs.Load())
	close(release)
	require.NoError(t, <-manual)
	ticks <- time.Now()
	require.NoError(t, <-attempts, "later tick still refreshes")
	assert.Equal(t, int32(3), runs.Load())
}

func TestCoordinatorTurnsPanicIntoErrorAndStaysUsable(t *testing.T) {
	var n atomic.Int32
	c := NewRefreshCoordinator(func(context.Context) (RefreshReport, error) {
		if n.Add(1) == 1 {
			panic("bad")
		}
		return RefreshReport{ID: "ok"}, nil
	})
	_, err := c.Refresh(context.Background())
	require.Error(t, err)
	r, err := c.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "ok", r.ID)
}

func TestLoopRunsStartupPassThenEveryTickAndSurvivesFailure(t *testing.T) {
	ticks := make(chan time.Time)
	var runs atomic.Int32
	var gotInterval atomic.Int64
	loop := &RefreshLoop{
		Refresh: func(context.Context) (RefreshReport, error) {
			if runs.Add(1) == 2 {
				return RefreshReport{}, errors.New("fetch exploded")
			}
			return RefreshReport{}, nil
		},
		NewTicker: func(d time.Duration) (<-chan time.Time, func()) { gotInterval.Store(int64(d)); return ticks, func() {} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { loop.refreshOnce(ctx); loop.RunScheduled(ctx); close(done) }()
	require.Eventually(t, func() bool { return runs.Load() == 1 }, time.Second, time.Millisecond, "startup pass")
	ticks <- time.Now()
	ticks <- time.Now()
	require.Eventually(t, func() bool { return runs.Load() == 3 }, time.Second, time.Millisecond, "a failed cycle does not stop the loop")
	assert.Equal(t, int64(4*time.Hour), gotInterval.Load())
	cancel()
	<-done
}

// discoveryFixture has one follower path (p1) and standalone paths with no
// following profile; the vendor "v" catalog has two tiers.
func discoveryFixture() SelectionInput {
	x, y := entry("x", "1", ""), entry("y", "2", "")
	o1, o2 := entry("o1", "1", ""), entry("o2", "2", "")
	o1.Tier, o2.Tier = "other", "other"
	in := selectionFixture("x", nil, x, y, o1, o2)
	in.Paths.Paths = []ExecutionPath{
		{Key: "p1", ConfigRevision: "c1", RouteProvider: "v", Aliases: []string{"w"}},
		{Key: "solo", ConfigRevision: "cs", RouteProvider: "v", Aliases: []string{"plain"}},
		{Key: "other-vendor", ConfigRevision: "co", RouteProvider: "nobody", Aliases: []string{"x"}},
	}
	return in
}

func TestSelectorDiscoversAllSourceEntriesOnPathsWithoutFollowers(t *testing.T) {
	probe := &stubProbe{}
	sel := (&AdvanceSelector{Probe: probe}).Select(context.Background(), discoveryFixture())

	var solo []string
	for _, ev := range sel.Evidence {
		if ev.PathKey == "solo" {
			solo = append(solo, ev.NativeModel)
			assert.True(t, ev.Confirmed())
		}
	}
	assert.ElementsMatch(t, []string{"x", "y", "o1", "o2"}, solo, "discovery is independent of replacement eligibility")
	assert.NotEmpty(t, sel.Cursor["solo@cs"], "the sweep advances the path cursor")
	assert.NotContains(t, probe.calls, "other-vendor/y", "a path whose route provider has no catalog slice probes nothing")
	require.Len(t, sel.Advances, 1, "the follower still advances")
	assert.Equal(t, "y", sel.Advances[0].Model)
	assert.False(t, sel.Exhausted)
}

func TestSelectorDiscoveryReusesConfirmationRetriesUnknownAndNeverAdvances(t *testing.T) {
	in := discoveryFixture()
	for _, id := range []string{"v/y", "v/o2"} {
		in.Stored = append(in.Stored, UsabilityEvidence{CanonicalID: id, PathKey: "solo", ConfigRevision: "cs", NativeModel: id[2:], ProofRef: "p", CheckedAt: time.Unix(1, 0), State: UsabilityConfirmed})
	}
	probe := &stubProbe{}
	sel := (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
	assert.NotContains(t, probe.calls, "solo/y")
	assert.NotContains(t, probe.calls, "solo/o2")

	// An unknown result is re-probed next cycle, a changed config revision is new proof.
	in.Stored = nil
	probe = &stubProbe{states: map[string]string{"y@solo": UsabilityUnknown}}
	sel = (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
	for _, ev := range sel.Evidence {
		if ev.PathKey == "solo" && ev.NativeModel == "y" {
			assert.Equal(t, UsabilityUnknown, ev.State)
		}
	}
	assert.Equal(t, UsabilityUnknown, sel.PathStatus["path:solo"], "one unknown check keeps the path unknown")
	for _, adv := range sel.Advances {
		assert.Equal(t, "w", adv.Expected.Alias, "discovery proposes no profile change")
	}
}

func TestSelectorDiscoveryBudgetEndKeepsFollowerAdvances(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	probe := &cancelAfterProbe{stubProbe: &stubProbe{}, after: "p1/y", cancel: cancel}
	sel := (&AdvanceSelector{Probe: probe}).selectWithin(context.Background(), ctx, discoveryFixture())
	require.Len(t, sel.Advances, 1, "follower proof was complete before discovery ran out of budget")
	assert.True(t, sel.DiscoveryExhausted)
	assert.False(t, sel.Exhausted)
}

type cancelAfterProbe struct {
	*stubProbe
	after  string
	cancel context.CancelFunc
}

func (p *cancelAfterProbe) Check(ctx context.Context, path ExecutionPath, e modelidentity.Entry) (UsabilityEvidence, error) {
	ev, err := p.stubProbe.Check(ctx, path, e)
	if path.Key+"/"+e.SpecificModel == p.after {
		p.cancel()
	}
	return ev, err
}

type announceSpy struct{ calls atomic.Int32 }

func (a *announceSpy) Announce(context.Context, RefreshReport) error { a.calls.Add(1); return nil }

type faultSettings struct{ err error }

func (f faultSettings) ReadInternal(context.Context) (settings.Values, error) {
	return settings.Values{}, f.err
}

type faultStore struct {
	CatalogStore
	commitErr error
}

func (s faultStore) Snapshot(context.Context) (CatalogView, error)    { return CatalogView{}, nil }
func (s faultStore) Recover(context.Context) ([]RefreshReport, error) { return nil, nil }

type emptyPublicationJournal struct{}

func (emptyPublicationJournal) Unpublished(context.Context) ([]provider.Advancement, error) {
	return nil, nil
}
func (emptyPublicationJournal) Acknowledge(context.Context, []string) error { return nil }
func (s faultStore) CommitCycle(context.Context, CycleCommit) (RefreshReport, error) {
	return RefreshReport{}, s.commitErr
}

type failFetcher struct{ CatalogFetcher }

func (failFetcher) Fetch(context.Context, string) ([]byte, error) { return nil, errors.New("down") }

type readGate struct{ Gate }

func (readGate) WithRead(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

// Pending announcements must drain after every refresh result, including a
// cycle that failed before or while committing, so a configured webhook that
// was unreachable earlier is retried even while discovery itself is failing.
func TestRefreshDrainsAnnouncementsOnCollectAndCommitFaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings faultSettings
		commit   error
	}{
		{name: "settings fault before collection", settings: faultSettings{err: errors.New("settings down")}},
		{name: "commit fault", commit: errors.New("disk full")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := faultStore{commitErr: tc.commit}
			var settingsReader SettingsReader = faultSettings{}
			if tc.settings.err != nil {
				settingsReader = tc.settings
			}
			pipeline, err := NewDiscoveryPipeline(DiscoveryDeps{
				Settings: settingsReader, Fetcher: failFetcher{}, Normalizer: struct{ CatalogNormalizer }{}, Mapper: struct{ EntryMapper }{},
				Inventory: struct{ PathInventory }{}, Profiles: struct{ ProfileLister }{}, Store: store, Probe: &stubProbe{}, AdvanceValidator: allowAdvance{},
			})
			require.NoError(t, err)
			spy := &announceSpy{}
			svc := &RefreshService{Pipeline: pipeline, Store: store, Gate: readGate{}, Publisher: struct{ Publisher }{}, Journal: emptyPublicationJournal{}, Announcer: spy}
			_, err = svc.Refresh(context.Background())
			require.Error(t, err)
			assert.EqualValues(t, 1, spy.calls.Load(), "the drain runs although the cycle failed")
		})
	}
}

// scriptedSettings answers each ReadInternal from a script, so a test can make
// the fresh post-collection eligibility read differ from the initial one.
type scriptedSettings struct {
	reads []settings.Values
	errs  []error
	n     int
}

func (s *scriptedSettings) ReadInternal(context.Context) (settings.Values, error) {
	i := s.n
	s.n++
	if i < len(s.errs) && s.errs[i] != nil {
		return settings.Values{}, s.errs[i]
	}
	return s.reads[i], nil
}

// The webhook classification a discovery event is frozen with comes from a
// fresh read after collection, separate from the initial catalog-URL read: a
// fault on it yields nil (awaiting_setting) without discarding the cycle, and a
// webhook cleared or set while the fetch ran is seen as it is at commit time.
func TestCollectClassifiesWebhookFromFreshPostCollectionRead(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    *scriptedSettings
		want *bool
	}{
		{name: "fresh read fault is unknown", s: &scriptedSettings{reads: []settings.Values{{TeamsWebhookURL: "https://hook"}, {}}, errs: []error{nil, errors.New("settings down")}}},
		{name: "cleared during the fetch is empty", s: &scriptedSettings{reads: []settings.Values{{TeamsWebhookURL: "https://hook"}, {}}}, want: ptrBool(false)},
		{name: "set during the fetch is configured", s: &scriptedSettings{reads: []settings.Values{{}, {TeamsWebhookURL: "https://hook"}}}, want: ptrBool(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pipeline, err := NewDiscoveryPipeline(DiscoveryDeps{
				Settings: tc.s, Fetcher: failFetcher{}, Normalizer: struct{ CatalogNormalizer }{}, Mapper: struct{ EntryMapper }{},
				Inventory: struct{ PathInventory }{}, Profiles: struct{ ProfileLister }{}, Store: faultStore{}, Probe: &stubProbe{}, AdvanceValidator: allowAdvance{},
			})
			require.NoError(t, err)
			commit, err := pipeline.Collect(context.Background(), nil)
			require.NoError(t, err, "a fault on the eligibility read must not discard the cycle")
			assert.Equal(t, tc.want, commit.WebhookConfigured)
			assert.Equal(t, "fetch_failed", commit.Report.PathStatus["catalog"], "the collected cycle is intact")
		})
	}
}

func ptrBool(b bool) *bool { return &b }

// Immediate runs one cycle as soon as the loop starts, before any tick, so a
// serving entrypoint begins discovery without startup waiting on it.
func TestRefreshLoopImmediateFirstPass(t *testing.T) {
	ticks := make(chan time.Time)
	cycles := make(chan struct{}, 2)
	loop := &RefreshLoop{
		Refresh:   func(context.Context) (RefreshReport, error) { cycles <- struct{}{}; return RefreshReport{}, nil },
		NewTicker: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
		Immediate: true,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); loop.RunScheduled(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-cycles:
	case <-time.After(time.Second):
		t.Fatal("no immediate first pass")
	}
	ticks <- time.Now()
	select {
	case <-cycles:
	case <-time.After(time.Second):
		t.Fatal("ticks still drive later cycles")
	}
}
