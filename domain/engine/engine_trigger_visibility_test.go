package engine_test

import (
	"context"
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
}

func (f *fakePubLookup) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	p, ok := f.pubs[name]
	return p.Publisher, ok, nil
}

func (f *fakePubLookup) PublicationOf(_ context.Context, name string) (workflow.Publication, bool, error) {
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
