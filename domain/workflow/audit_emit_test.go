package workflow_test

import (
	"context"
	"errors"
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

// ---------------------------------------------------------------------------
// FA #13 Pass 2: robustness — List and Copy admin override emission sites
// ---------------------------------------------------------------------------

// Success: admin List includes a department-only workflow they are NOT a member
// of → exactly one ActionAdminOverride event is emitted for that entry in the
// list (same adminOverrode gate as Load, but via the List code path).
func TestService_List_AdminOverride_EmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	adminID := uuid.MustParse("00000000-0000-0000-0000-0000000000a2")

	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// Admin has NO engineering membership → override.
	_, err := svc.List(ctxWithRoles(adminID, true, nil, nil))
	require.NoError(t, err)

	require.Len(t, store.events, 1, "List must emit AdminOverride for each dept-only entry visible only via admin")
	assert.Equal(t, audit.ActionAdminOverride, store.events[0].Action)
	assert.Equal(t, "eng-wf", store.events[0].Target)
}

// Success: admin Copy of a department-visibility workflow they are NOT a member
// of → one ActionAdminOverride event is emitted. The ordering differs from
// Publish: audit fires before CopyTree (the intent is already consequential).
func TestService_Copy_AdminOverride_EmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	adminID := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")

	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))

	// Admin has NO engineering membership → override.
	err := svc.Copy(ctxWithRoles(adminID, true, nil, nil), "eng-wf", "my-copy")
	require.NoError(t, err)

	require.Len(t, store.events, 1, "Copy must emit AdminOverride when admin is sole reason for access")
	assert.Equal(t, audit.ActionAdminOverride, store.events[0].Action)
	assert.Equal(t, "eng-wf", store.events[0].Target)
}

// ---------------------------------------------------------------------------
// FA #31: best-effort audit — SetVisibility still succeeds on store failure
// ---------------------------------------------------------------------------

// failingStore returns an error on every Append, simulating an unavailable DB.
type failingStore struct{ err error }

func (f *failingStore) Append(_ context.Context, _ audit.Event) error    { return f.err }
func (f *failingStore) ListByActor(context.Context, string, int) ([]audit.Event, error) {
	return nil, f.err
}

// TestService_SetVisibility_AuditStoreFailure_OperationSucceeds verifies that
// a store failure during Record() inside SetVisibility does NOT propagate to
// the caller. Audit is best-effort and void — the mutation must still succeed.
// If this contract is broken, a transient audit-DB outage would make all
// visibility changes fail, which is strictly worse than silent audit loss.
func TestService_SetVisibility_AuditStoreFailure_OperationSucceeds(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-000000000c01")

	seedPrivate(repo, owner, "wf")
	// First publish so SetVisibility has an existing record to update.
	svcSetup := workflow.NewWorkflowService(repo, pubs)
	require.NoError(t, svcSetup.Publish(
		ctxWithRoles(owner, false, []string{"engineering"}, nil),
		"wf", "shared", workflow.VisibilityDepartment, "engineering"))

	// Wire a failing audit store — every Append returns an error.
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(&failingStore{err: errors.New("db unavailable")}))

	// SetVisibility must succeed even though the audit store is broken.
	// Use admin=true so the role gate (lead/admin required for public) is satisfied;
	// the test focuses on audit failure, not authorization.
	err := svc.SetVisibility(
		ctxWithRoles(owner, true, []string{"engineering"}, []string{"engineering"}),
		"shared", workflow.VisibilityPublic, "")
	assert.NoError(t, err,
		"SetVisibility must succeed when the audit store fails — audit is best-effort")
}
