package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePubLookup is a minimal workflow.PublisherLookup for trigger gating tests.
type fakePubLookup struct {
	pubs map[string]workflow.Publication
	errs map[string]error
}

func (f *fakePubLookup) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	p, ok := f.pubs[name]
	return p.Publisher, ok, nil
}

func (f *fakePubLookup) PublicationOf(_ context.Context, name string) (workflow.Publication, bool, error) {
	if err, ok := f.errs[name]; ok {
		return workflow.Publication{}, false, err
	}
	p, ok := f.pubs[name]
	return p, ok, nil
}

func ctxWithDept(u uuid.UUID, dept string) context.Context {
	return identity.WithIdentity(context.Background(),
		identity.Identity{UserID: u, Username: "u", Role: identity.RoleUser, Departments: []string{dept}})
}

func TestEngine_Trigger_DepartmentWorkflow_HiddenFromOutsider(t *testing.T) {
	h := newTriggerHarness()
	publisher := uuid.New()
	outsider := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"eng-wf": {Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}})

	_, _, err := h.engine.Trigger(ctxWithDept(outsider, "sales"), "eng-wf", nil, "normal")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestEngine_Trigger_DepartmentWorkflow_AllowedForMember(t *testing.T) {
	h := newTriggerHarness()
	publisher := uuid.New()
	member := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"eng-wf": {Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}})

	_, _, err := h.engine.Trigger(ctxWithDept(member, "engineering"), "eng-wf", nil, "normal")
	require.NoError(t, err)
}

// Success: admin bypasses the department-membership gate for Trigger. The
// visibility filter calls CanSeePublication which returns true for any admin,
// so an admin without engineering membership can still trigger an
// engineering-department workflow.
func TestEngine_Trigger_DepartmentWorkflow_AdminBypassesMembershipGate(t *testing.T) {
	h := newTriggerHarness()
	publisher := uuid.New()
	admin := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"eng-wf": {Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}})

	// Admin has NO engineering membership.
	adminCtx := identity.WithIdentity(context.Background(),
		identity.Identity{UserID: admin, Username: "u", Role: identity.RoleAdmin})
	_, _, err := h.engine.Trigger(adminCtx, "eng-wf", nil, "normal")
	require.NoError(t, err, "admin must be able to trigger a department-visibility workflow without membership")
}

// Success: a system trigger (no context identity — scheduler/webhook) skips
// the visibility gate entirely even when a publisher lookup is configured.
// The engine's filter is only built when identity.FromContext succeeds; a bare
// context.Background() carries no identity, so the filter is nil and gating is
// bypassed. This is intentional: system triggers passed auth at registration.
func TestEngine_Trigger_SystemTrigger_SkipsVisibilityGate(t *testing.T) {
	h := newTriggerHarness()
	publisher := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("eng-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{
		"eng-wf": {Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"},
	}})

	// No identity in context → system trigger path → gate skipped.
	_, _, err := h.engine.Trigger(context.Background(), "eng-wf", nil, "normal")
	require.NoError(t, err, "system trigger must bypass visibility gating regardless of publisher config")
}

// Success: a shared workflow with NO publication record at all (PublicationOf
// found=false) is treated as open, not hidden — "unpublished shared folder ⇒
// open" per the closure's own comment. This is distinct from the department
// cases above, which all register a Publication for the name; this exercises
// the not-found branch of sharedVisibilityFilter's own PublicationOf call,
// which no existing test reached (a caller with no publisher lookup configured
// takes a different code path — filter is nil and gating is skipped entirely
// — so this requires a configured lookup that simply has no row for the name).
func TestEngine_Trigger_UnpublishedSharedWorkflow_VisibleWithIdentity(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	h.repo.put(workflow.Shared(), sharedWf("open-wf"))
	// Publisher lookup configured, but "open-wf" has no entry: PublicationOf
	// returns found=false.
	h.engine.SetPublisherLookup(&fakePubLookup{pubs: map[string]workflow.Publication{}})

	_, _, err := h.engine.Trigger(ctxAs(caller), "open-wf", nil, "normal")
	require.NoError(t, err, "a shared workflow with no publication record must remain visible")
}

// Failure: a PublicationOf lookup error must fail the trigger closed, not
// silently treat the workflow as invisible or as open. No existing test drove
// a lookup failure through the real closure returned by sharedVisibilityFilter
// (as opposed to workflow.LoadVisibleFiltered's own generic filter-error
// test, which uses a fake filter function rather than the engine's actual
// PublicationOf-backed closure).
func TestEngine_Trigger_PublicationLookupError_FailsClosed(t *testing.T) {
	h := newTriggerHarness()
	caller := uuid.New()
	lookupErr := errors.New("publication lookup unavailable")
	h.repo.put(workflow.Shared(), sharedWf("flaky-wf"))
	h.engine.SetPublisherLookup(&fakePubLookup{errs: map[string]error{"flaky-wf": lookupErr}})

	_, _, err := h.engine.Trigger(ctxAs(caller), "flaky-wf", nil, "normal")
	require.Error(t, err)
	assert.ErrorIs(t, err, lookupErr)
}
