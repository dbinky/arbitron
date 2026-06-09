package schedule_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// denyCanTriggerRN denies the can-trigger gate: it models a caller who has LOST
// trigger authorization for the schedule's workflow since the schedule was
// created.
type denyCanTriggerRN struct{ calls int }

func (d *denyCanTriggerRN) CanTrigger(context.Context, identity.Identity, string) (bool, error) {
	d.calls++
	return false, nil
}

// recordingCanTriggerRN allows but records the call so we can assert the recheck
// happened on the privileged path.
type recordingCanTriggerRN struct{ calls int }

func (r *recordingCanTriggerRN) CanTrigger(context.Context, identity.Identity, string) (bool, error) {
	r.calls++
	return true, nil
}

// SECURITY (Must Fix #2): run-now must re-run the can-trigger gate that
// Create/Update enforce. A caller who could trigger the workflow when the
// schedule was created but has since lost that authorization must NOT be able to
// fire it on demand — the firer must never be reached.
func TestService_RunNow_RechecksCanTrigger_DeniedDoesNotFire(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	gate := &denyCanTriggerRN{}
	svc := schedule.NewService(newRunNowRepo(s), gate, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})

	require.ErrorIs(t, err, schedule.ErrNotAuthorizedWorkflow,
		"a caller who lost trigger authorization must be rejected")
	assert.Equal(t, 1, gate.calls, "the can-trigger gate must be consulted on run-now")
	assert.Equal(t, 0, firer.calls, "the firer must not be reached when can-trigger is denied")
}

// The recheck runs for an authorized caller too (it is a gate, not a denier):
// when can-trigger passes, the fire proceeds.
func TestService_RunNow_RechecksCanTrigger_AllowedFires(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	gate := &recordingCanTriggerRN{}
	svc := schedule.NewService(newRunNowRepo(s), gate, schedule.WithFirer(firer))

	res, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})

	require.NoError(t, err)
	assert.True(t, res.Fired)
	assert.GreaterOrEqual(t, gate.calls, 1, "the can-trigger gate must be consulted")
	assert.Equal(t, 1, firer.calls)
}

// Throttle (Must Fix #3): run-now coalesces against an active run BY DEFAULT.
// When the caller does not explicitly request fire-anyway, the service must pass
// skipWhenBusy=true to the firer so a tight loop of fires for a busy
// workflow/creator does not spawn unbounded concurrent runs.
func TestService_RunNow_CoalescesByDefault(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	// No SkipWhenBusy preference set => coalesce default.
	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})

	require.NoError(t, err)
	assert.True(t, firer.gotSkip, "run-now must coalesce (skipWhenBusy=true) by default")
}

// An explicit fire-anyway override is honored: a caller may set Force to bypass
// the coalesce default (e.g. a deliberate concurrent run).
func TestService_RunNow_ForceOverridesCoalesce(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.New(), retFired: true}
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{}, schedule.WithFirer(firer))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{Force: true})

	require.NoError(t, err)
	assert.False(t, firer.gotSkip, "Force must override the coalesce default (skipWhenBusy=false)")
}

// Audit log (Should Fix #11): a structured audit line is emitted at the service
// seam on a fired run-now, carrying caller UserID, schedule id, workflow, and
// instance id — and NOT the raw Variables (PII).
func TestService_RunNow_EmitsAuditLog(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	wantInst := uuid.New()
	firer := &stubFirer{retInstance: wantInst, retFired: true}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{},
		schedule.WithFirer(firer), schedule.WithServiceLogger(logger))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})
	require.NoError(t, err)

	out := buf.String()
	require.NotEmpty(t, out, "run-now must emit an audit log line")
	// Parse the (last) log line as JSON and assert the structured fields.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &rec))
	assert.Equal(t, owner.String(), rec["callerUserID"], "audit must record the caller UserID")
	assert.Equal(t, s.ID.String(), rec["scheduleID"])
	assert.Equal(t, "wf", rec["workflow"])
	assert.Equal(t, wantInst.String(), rec["instanceID"])
	assert.Equal(t, true, rec["fired"])
	// PII guard: the raw variables value "v" must never appear in the log line.
	assert.NotContains(t, out, "\"v\"", "raw schedule Variables must not be logged")
}

// A skipped (coalesced) run-now is still audited, with fired=false and no
// instance id.
func TestService_RunNow_AuditsSkipped(t *testing.T) {
	owner := uuid.New()
	s := enabledSchedule(owner)
	firer := &stubFirer{retInstance: uuid.Nil, retFired: false}

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	svc := schedule.NewService(newRunNowRepo(s), allowCanTriggerRN{},
		schedule.WithFirer(firer), schedule.WithServiceLogger(logger))

	_, err := svc.RunNow(ctxAs(ownerIdent(owner)), s.ID, schedule.RunNowInput{})
	require.NoError(t, err)

	out := strings.TrimSpace(buf.String())
	require.NotEmpty(t, out)
	lines := strings.Split(out, "\n")
	var rec map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &rec))
	assert.Equal(t, false, rec["fired"])
}
