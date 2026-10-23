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

func TestSelectorEvidenceUsesCheckedOffering(t *testing.T) {
	x, y := entry("x", "1", ""), entry("y", "2", "")
	for _, offering := range []string{"v", "other", ""} {
		t.Run("stored_"+offering, func(t *testing.T) {
			in := selectionFixture("x", nil, x, y)
			for _, p := range in.Paths.Paths {
				in.Stored = append(in.Stored, UsabilityEvidence{CanonicalID: y.CanonicalID, PathKey: p.Key, ConfigRevision: p.ConfigRevision, Provider: offering, NativeModel: "y", ProofRef: "stored", CheckedAt: time.Unix(1, 0), State: UsabilityConfirmed})
			}
			probe := &stubProbe{}
			out := (&AdvanceSelector{Probe: probe}).Select(context.Background(), in)
			require.Len(t, out.Advances, 1)
			assert.Equal(t, []provider.PathEvidenceRef{{PathKey: "p1", ConfigRevision: "c1"}, {PathKey: "p2", ConfigRevision: "c2"}}, out.Advances[0].Evidence)
			for _, path := range in.Paths.Paths {
				if offering == "other" {
					assert.Contains(t, probe.calls, path.Key+"/y", "another offering's proof cannot authorize this target")
				} else {
					assert.NotContains(t, probe.calls, path.Key+"/y", "unchanged or genuinely pre-provenance proof is reused")
				}
			}
			require.NotEmpty(t, out.Evidence)
			for _, ev := range out.Evidence {
				assert.Equal(t, "v", ev.Provider)
			}
		})
	}
	in := selectionFixture("x", nil, x, y)
	for _, path := range in.Paths.Paths {
		in.Stored = append(in.Stored, UsabilityEvidence{CanonicalID: y.CanonicalID, PathKey: path.Key, ConfigRevision: path.ConfigRevision, Provider: "other", NativeModel: "y", ProofRef: "other-offering", CheckedAt: time.Unix(1, 0), State: UsabilityConfirmed})
	}
	out := (&AdvanceSelector{Probe: &stubProbe{states: map[string]string{"y@p1": UsabilityUnknown, "y@p2": UsabilityUnavailable}}}).Select(context.Background(), in)
	assert.Empty(t, out.Advances, "another offering's proof cannot bypass a failed exact check")

	failed := (&AdvanceSelector{Probe: &stubProbe{err: errors.New("probe unavailable")}}).Select(context.Background(), selectionFixture("x", nil, x, y))
	require.Len(t, failed.Evidence, 4)
	assert.Empty(t, failed.Advances)
	for _, ev := range failed.Evidence {
		assert.Equal(t, "v", ev.Provider)
		assert.Equal(t, UsabilityUnknown, ev.State)
	}

	for _, state := range []string{UsabilityConfirmed, UsabilityUnknown, UsabilityUnavailable} {
		t.Run("fresh_"+state, func(t *testing.T) {
			probe := offeringProbe{&stubProbe{states: map[string]string{"y@p1": state}}}
			out := (&AdvanceSelector{Probe: probe}).Select(context.Background(), selectionFixture("x", nil, x, y))
			require.Len(t, out.Evidence, 4)
			if state == UsabilityConfirmed {
				require.Len(t, out.Advances, 1, "lane routing provenance must not override the checked catalog offering")
			} else {
				assert.Empty(t, out.Advances)
			}
			for _, ev := range out.Evidence {
				assert.Equal(t, "v", ev.Provider)
			}
		})
	}
}

// Offering provenance belongs to selection, not to a lane's routing identity.
type offeringProbe struct{ *stubProbe }

func (p offeringProbe) Check(ctx context.Context, path ExecutionPath, e modelidentity.Entry) (UsabilityEvidence, error) {
	ev, err := p.stubProbe.Check(ctx, path, e)
	ev.Provider = "lane-routing-provider"
	return ev, err
}

// Check the producer boundary, not just a permissive store's report echo.
type checkingProducerStore struct {
	CatalogStore
	input    CycleCommit
	fail     error
	advances int
}

func (s *checkingProducerStore) CommitCycle(_ context.Context, c CycleCommit) (RefreshReport, error) {
	if len(c.Evidence) != 4 || len(c.Advances) != s.advances {
		return RefreshReport{}, errors.New("missing producer observations or proposals")
	}
	if c.PolicyRevision != 1 {
		return RefreshReport{}, errors.New("missing cycle policy")
	}
	for _, ev := range c.Evidence {
		if ev.Provider != "v" {
			return RefreshReport{}, errors.New("wrong offering")
		}
	}
	for _, req := range c.Advances {
		if len(req.Evidence) != 2 || req.Evidence[0] != (provider.PathEvidenceRef{PathKey: "p1", ConfigRevision: "c1"}) || req.Evidence[1] != (provider.PathEvidenceRef{PathKey: "p2", ConfigRevision: "c2"}) {
			return RefreshReport{}, errors.New("missing consuming proof")
		}
	}
	s.input = c
	return c.Report, s.fail
}

func TestRefreshProducesPolicyAndCompletePathEvidence(t *testing.T) {
	for _, prospective := range []bool{false, true} {
		t.Run(map[bool]string{false: "advancement", true: "prospective_without_advances"}[prospective], func(t *testing.T) {
			x, y := entry("x", "1", ""), entry("y", "2", "")
			f := newCandidateFixture(t, x, y)
			d := f.svc.d.Pipeline.d
			d.Profiles = producerProfiles{selectionFixture("x", nil, x, y).Profiles}
			pipeline, err := NewDiscoveryPipeline(d)
			require.NoError(t, err)
			var p *provider.Profile
			if prospective {
				profile := followProfile("y")
				p = &profile
			}
			c, err := pipeline.Collect(context.Background(), p)
			require.NoError(t, err)
			require.EqualValues(t, 1, c.PolicyRevision)
			require.Len(t, c.Evidence, 4)
			if prospective {
				require.Empty(t, c.Advances)
			} else {
				require.Len(t, c.Advances, 1)
			}
			store := &checkingProducerStore{advances: len(c.Advances)}
			service := &RefreshService{Store: store, Gate: producerGate{}}
			_, err = service.commit(context.Background(), c)
			require.NoError(t, err)
			assert.Equal(t, c, store.input)
			// The checking sink must reject every omitted or mismatched feed.
			for _, revision := range []int64{0, 2} {
				bad := c
				bad.PolicyRevision = revision
				_, err = service.commit(context.Background(), bad)
				require.Error(t, err)
			}
			for _, offering := range []string{"", "other"} {
				bad := c
				bad.Evidence = append([]UsabilityEvidence(nil), c.Evidence...)
				bad.Evidence[0].Provider = offering
				_, err = service.commit(context.Background(), bad)
				require.Error(t, err)
			}
			if !prospective {
				for _, refs := range [][]provider.PathEvidenceRef{
					nil,
					{{PathKey: "p1", ConfigRevision: "c1"}},
					{{PathKey: "p1", ConfigRevision: "c1"}, {PathKey: "p2", ConfigRevision: "wrong"}},
				} {
					bad := c
					bad.Advances = append([]provider.AdvanceRequest(nil), c.Advances...)
					bad.Advances[0].Evidence = refs
					_, err = service.commit(context.Background(), bad)
					require.Error(t, err)
				}
			}
			store.fail = errors.New("disk full")
			_, err = service.commit(context.Background(), c)
			assert.ErrorIs(t, err, store.fail)
		})
	}
}

func TestRefreshRetainsCapturedPolicyWhenInventoryOrProfilesFail(t *testing.T) {
	for _, fault := range []string{"inventory", "profiles"} {
		t.Run(fault, func(t *testing.T) {
			f := newCandidateFixture(t, entry("x", "1", ""))
			d := f.svc.d.Pipeline.d
			if fault == "inventory" {
				f.inv.err = errors.New("inventory unavailable")
			} else {
				d.Profiles = failedProducerProfiles{}
			}
			pipeline, err := NewDiscoveryPipeline(d)
			require.NoError(t, err)
			c, err := pipeline.Collect(context.Background(), nil)
			require.NoError(t, err)
			assert.EqualValues(t, 1, c.PolicyRevision, "fresh classified entries still need transactional policy validation")
			assert.Empty(t, c.Advances)
			assert.Empty(t, c.Evidence)
			assert.Equal(t, UsabilityUnknown, c.Report.PathStatus[fault])
		})
	}
}

type failedProducerProfiles struct{}

func (failedProducerProfiles) ListInternal(context.Context) ([]provider.Profile, error) {
	return nil, errors.New("profiles unavailable")
}

type producerProfiles struct{ profiles []provider.Profile }

func (p producerProfiles) ListInternal(context.Context) ([]provider.Profile, error) {
	return p.profiles, nil
}

type producerGate struct{ readGate }

func (producerGate) WithCurrent(ctx context.Context, expected string, fn func(context.Context) error) error {
	if expected != "rev" {
		return provider.ErrExecutionRevisionStale
	}
	return fn(ctx)
}
