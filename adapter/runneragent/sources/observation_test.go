package sources

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type obsProber struct {
	res   runner.ProbeResult
	err   error
	calls int
	got   runner.Source
}

func (p *obsProber) Probe(_ context.Context, src runner.Source) (runner.ProbeResult, error) {
	p.got = src
	p.calls++
	return p.res, p.err
}

func TestInventory_ObservationsCarrySequenceAndMonotonicAge(t *testing.T) {
	now := time.Now()
	allowed := false
	claude := &obsProber{res: runner.ProbeResult{Auth: runner.AuthOK, Capacity: &runner.CapacityObservation{State: runner.CapacityExhausted, ModelScope: "claude-opus-5-5", OrdinaryUsageAllowed: &allowed}}}
	luna := &obsProber{res: runner.ProbeResult{Auth: runner.AuthOK}}
	inv := New([]Spec{{Source: runneragent.Source{Kind: runner.CredAnthropicSubscription}, Prober: claude}, {Source: runneragent.Source{Kind: runner.CredLunaRoute, CredentialRef: "lr-main"}, Prober: luna}}, nil, func() time.Time { return now })
	ctx := context.Background()
	for _, s := range inv.Sources(ctx) {
		assert.Nil(t, s.Observation)
	}
	inv.Probe(ctx)
	now = now.Add(1500 * time.Millisecond)
	got := inv.Sources(ctx)
	require.NotNil(t, got[0].Observation)
	assert.Equal(t, uint64(1), got[0].ObservationSeq)
	assert.Equal(t, uint64(2), got[1].ObservationSeq)
	assert.Equal(t, int64(1500), got[0].SampleAgeMs)
	assert.Equal(t, &allowed, got[0].Observation.Capacity.OrdinaryUsageAllowed)
	assert.Equal(t, "claude-opus-5-5", got[0].Observation.Capacity.ModelScope)
	now = now.Add(time.Second)
	again := inv.Sources(ctx)
	assert.Equal(t, got[0].ObservationSeq, again[0].ObservationSeq)
	assert.Equal(t, int64(2500), again[0].SampleAgeMs)
	claude.err = errors.New("garbled")
	inv.Probe(ctx)
	assert.Equal(t, uint64(1), inv.Sources(ctx)[0].ObservationSeq)
}

func TestInventory_ProbeSourceIsFreshAndExact(t *testing.T) {
	claude := &obsProber{res: runner.ProbeResult{Auth: runner.AuthLoggedOut}}
	luna := &obsProber{res: runner.ProbeResult{Auth: runner.AuthOK}}
	inv := New([]Spec{{Source: runneragent.Source{Kind: runner.CredAnthropicSubscription}, Prober: claude}, {Source: runneragent.Source{Kind: runner.CredLunaRoute, CredentialRef: "lr-main"}, Prober: luna}}, nil, time.Now)
	var _ runneragent.FreshSourceProber = inv
	ctx := context.Background()
	src, err := inv.ProbeSource(ctx, runner.CredAnthropicSubscription, "")
	require.NoError(t, err)
	require.NotNil(t, src.Observation)
	assert.Equal(t, runner.AuthLoggedOut, src.Observation.Auth)
	assert.Equal(t, int64(0), src.SampleAgeMs)
	assert.Equal(t, 1, claude.calls)
	assert.Zero(t, luna.calls)
	assert.Equal(t, runner.CredAnthropicSubscription, claude.got.Kind)
	assert.Empty(t, claude.got.CredentialRef)
	_, err = inv.ProbeSource(ctx, runner.CredLunaRoute, "lr-main")
	require.NoError(t, err)
	assert.Equal(t, runner.CredLunaRoute, luna.got.Kind)
	assert.Equal(t, "lr-main", luna.got.CredentialRef)
	_, err = inv.ProbeSource(ctx, runner.CredLunaRoute, "other-ref")
	assert.ErrorIs(t, err, runneragent.ErrUnknownSource)
	claude.err = errors.New("timeout")
	src, err = inv.ProbeSource(ctx, runner.CredAnthropicSubscription, "")
	assert.Error(t, err)
	assert.Nil(t, src.Observation)
}
