package runner

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthMonitor_CombinedLogoutAndExhaustionBothPersist(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	allowed := false
	reset := f.clock.Now().Add(time.Hour)
	h, err := f.mon.ObserveProbe(context.Background(), hmAnth, ProbeResult{Auth: AuthLoggedOut, Capacity: &CapacityObservation{OrdinaryUsageAllowed: &allowed, ResetAt: &reset}})
	require.NoError(t, err)
	assert.Equal(t, AuthLoggedOut, h.Auth)
	assert.Equal(t, CapacityExhausted, h.Capacity)
	assert.Equal(t, h, f.reg.health(hmAnth))
}

func TestHealthMonitor_GlobalAllowedDoesNotClearModelWindow(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	ctx := context.Background()
	require.NoError(t, f.mon.ApplyOutcome(ctx, hmAnth, SourceOutcome{Class: OutcomeExhausted, Model: "claude-sonnet-4-5"}))
	before := f.reg.health(hmAnth)
	allowed := true
	h, err := f.mon.ObserveProbe(ctx, hmAnth, ProbeResult{HealthRevision: before.Revision, Capacity: &CapacityObservation{OrdinaryUsageAllowed: &allowed}})
	require.NoError(t, err)
	assert.Equal(t, CapacityExhausted, h.Capacity)
	require.NoError(t, f.mon.ApplyOutcome(ctx, hmAnth, SourceOutcome{Class: OutcomeNone, Model: "other-model"}))
	assert.Equal(t, CapacityExhausted, f.reg.health(hmAnth).Capacity)
}

func TestHealthMonitor_AuthOnlyHeartbeatNeverRecoversQuota(t *testing.T) {
	f := newHMFixture(t, hmSource(hmAnth, CredAnthropicSubscription))
	ctx := context.Background()
	reset := f.clock.Now().Add(time.Hour)
	require.NoError(t, f.mon.ApplyOutcome(ctx, hmAnth, SourceOutcome{Class: OutcomeExhausted, ResetAt: &reset}))
	before := f.reg.health(hmAnth)
	h, err := f.mon.ObserveProbe(ctx, hmAnth, ProbeResult{Auth: AuthOK, HealthRevision: before.Revision})
	require.NoError(t, err)
	assert.Equal(t, AuthOK, h.Auth)
	assert.Equal(t, before.Capacity, h.Capacity)
	assert.Equal(t, before.ResetAt, h.ResetAt)
}

func TestHealthMonitor_ProbeStartedBeforeExhaustionCannotRecoverIt(t *testing.T) {
	f := newHMFixture(t, hmSource(hmOpenAI, CredOpenAISubscription))
	ctx := context.Background()
	revision := f.reg.health(hmOpenAI).Revision
	require.NoError(t, f.mon.ApplyOutcome(ctx, hmOpenAI, SourceOutcome{Class: OutcomeExhausted}))
	allowed := true
	h, err := f.mon.ObserveProbe(ctx, hmOpenAI, ProbeResult{Auth: AuthOK, HealthRevision: revision, Capacity: &CapacityObservation{OrdinaryUsageAllowed: &allowed}})
	require.NoError(t, err)
	assert.Greater(t, h.Revision, revision)
	assert.Equal(t, CapacityExhausted, f.reg.health(hmOpenAI).Capacity)
}
