package workflow_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingStore captures audit events for assertions.
type recordingStore struct{ events []audit.Event }

func (r *recordingStore) Append(_ context.Context, ev audit.Event) error {
	r.events = append(r.events, ev)
	return nil
}
func (r *recordingStore) ListByActor(context.Context, string, int) ([]audit.Event, error) {
	return r.events, nil
}

func TestService_Publish_EmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000f1")
	seedPrivate(repo, owner, "src")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	require.NoError(t, svc.Publish(
		ctxWithRoles(owner, false, []string{"engineering"}, nil),
		"src", "shared", workflow.VisibilityDepartment, "engineering"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowPublish, store.events[0].Action)
	assert.Equal(t, "shared", store.events[0].Target)
	assert.Contains(t, store.events[0].Detail, "department")
}

func TestService_Publish_FailedPublish_NoAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000f2")
	// No private source seeded → Publish fails with ErrNotFound before recording.
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil),
		"missing", "shared", workflow.VisibilityDepartment, "engineering")
	require.Error(t, err)
	assert.Empty(t, store.events) // nothing audited for a failed publish
}

func TestService_SetVisibility_EmitsVisibilityChangeWithFromTo(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000f3")
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityDepartment, "engineering")
	repo.bucket(workflow.Shared())["shared"] = wf("shared")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	require.NoError(t, svc.SetVisibility(
		ctxWithRoles(owner, false, []string{"engineering"}, []string{"engineering"}),
		"shared", workflow.VisibilityPublic, ""))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowVisibilityChange, store.events[0].Action)
	assert.Equal(t, "shared", store.events[0].Target)
	// from=department, to=public must both appear in detail.
	assert.Contains(t, store.events[0].Detail, "department")
	assert.Contains(t, store.events[0].Detail, "public")
}

func TestService_Load_AdminOverride_EmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000f4")
	adminID := uuid.MustParse("00000000-0000-0000-0000-0000000000f5")
	// A department workflow an admin (not in that dept) loads only via override.
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// Admin is in NO departments → admin status is the SOLE reason for access.
	_, err := svc.Load(ctxWithRoles(adminID, true, nil, nil), "eng-wf")
	require.NoError(t, err)

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionAdminOverride, store.events[0].Action)
	assert.Equal(t, "eng-wf", store.events[0].Target)
}

func TestService_Load_MemberInDepartment_NoOverrideEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000f6")
	member := uuid.MustParse("00000000-0000-0000-0000-0000000000f7")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// A genuine member would have seen it anyway → NOT an override.
	_, err := svc.Load(ctxWithRoles(member, true, []string{"engineering"}, nil), "eng-wf")
	require.NoError(t, err)
	assert.Empty(t, store.events)
}

func TestService_Load_PublicWorkflow_AdminNoOverrideEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000f8")
	adminID := uuid.MustParse("00000000-0000-0000-0000-0000000000f9")
	repo.bucket(workflow.Shared())["pub-wf"] = wf("pub-wf")
	_ = pubs.Upsert(context.Background(), "pub-wf", publisher, "src", workflow.VisibilityPublic, "")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// Public is visible to everyone → admin access is NOT an override.
	_, err := svc.Load(ctxWithRoles(adminID, true, nil, nil), "pub-wf")
	require.NoError(t, err)
	assert.Empty(t, store.events)
}
