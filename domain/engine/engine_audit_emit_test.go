package engine_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedAudit struct {
	action, target, detail string
}

type fakeAuditRecorder struct {
	mu      sync.Mutex
	events  []recordedAudit
	failErr error
}

func (a *fakeAuditRecorder) Record(_ context.Context, action, target, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failErr != nil {
		// Simulate the audit service swallowing a store failure internally: it
		// records nothing and returns nothing. The intervention must still stand.
		return
	}
	a.events = append(a.events, recordedAudit{action, target, detail})
}

func (a *fakeAuditRecorder) all() []recordedAudit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]recordedAudit(nil), a.events...)
}

// A lead pausing a non-owned department run emits instance.lead_intervention.
func TestAudit_LeadIntervention_Emitted(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(leadCtx(uuid.New(), "engineering"), run))

	events := audit.all()
	require.Len(t, events, 1)
	assert.Equal(t, engine.AuditActionLeadIntervention, events[0].action)
	assert.Equal(t, run.String(), events[0].target, "target is the instance id")
}

// An admin canceling a non-owned run emits admin.override.
func TestAudit_AdminOverride_Emitted(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateQueued)
	require.NoError(t, f.engine.Cancel(adminCtx(uuid.New()), run))

	events := audit.all()
	require.Len(t, events, 1)
	assert.Equal(t, engine.AuditActionAdminOverride, events[0].action)
}

// The owner intervening on their own run emits NO audit event.
func TestAudit_OwnerSelf_NoEvent(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(memberCtx(owner, "engineering"), run))
	assert.Empty(t, audit.all(), "owner self-action is not audited")
}

// An audit write failure does NOT fail the intervention (best-effort).
func TestAudit_RecordFailure_DoesNotFailIntervention(t *testing.T) {
	f := newTestFixtures()
	audit := &fakeAuditRecorder{failErr: errors.New("audit db down")}
	f.engine.SetAuditRecorder(audit)

	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	err := f.engine.Pause(leadCtx(uuid.New(), "engineering"), run)
	require.NoError(t, err, "a failed audit write must not fail the pause that already succeeded")
	assert.Equal(t, instance.StatePaused, currentState(t, f, run))
}

// No recorder wired => no panic (nil-safe).
func TestAudit_NoRecorder_NilSafe(t *testing.T) {
	f := newTestFixtures()
	owner := uuid.New()
	run := seedDeptInstance(f, owner, "engineering", instance.StateRunning)
	require.NoError(t, f.engine.Pause(leadCtx(uuid.New(), "engineering"), run))
}
