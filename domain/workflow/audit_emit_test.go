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
func (r *recordingStore) ListByTarget(_ context.Context, target string) ([]audit.Event, error) {
	out := make([]audit.Event, 0)
	for _, ev := range r.events {
		if ev.Target == target {
			out = append(out, ev)
		}
	}
	return out, nil
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

// ---------------------------------------------------------------------------
// Phase 3 Task 4: lifecycle ops emit one audit event each on success
// ---------------------------------------------------------------------------

// TestService_Rename_EmitsAuditEvent verifies an admin Rename records exactly
// one workflow.rename event carrying the before/after names.
func TestService_Rename_EmitsAuditEvent(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))

	require.NoError(t, svc.Rename(adminCtx, "alpha", "beta"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowRename, store.events[0].Action)
	assert.Equal(t, "beta", store.events[0].Target)
	assert.Contains(t, store.events[0].Detail, "alpha")
	assert.Contains(t, store.events[0].Detail, "beta")
}

// TestService_Delete_EmitsAuditEvent verifies an admin Delete records exactly
// one workflow.delete event for the removed workflow.
func TestService_Delete_EmitsAuditEvent(t *testing.T) {
	svc, repo, reg, _, _ := deleteSvc(t)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))
	owner := uuid.New()
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	require.NoError(t, svc.Delete(ctxWith(owner, true), "alpha"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowDelete, store.events[0].Action)
	assert.Equal(t, "alpha", store.events[0].Target)
}

// TestService_Unpublish_EmitsAuditEvent verifies an admin Unpublish records
// exactly one workflow.unpublish event.
func TestService_Unpublish_EmitsAuditEvent(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	store := &recordingStore{}
	svc := workflow.NewWorkflowService(repo, pubs)
	svc.SetAudit(audit.NewService(store))
	admin := uuid.New()
	publisher := uuid.New()
	repo.bucket(workflow.Shared())["alpha"] = wf("alpha")
	require.NoError(t, pubs.Upsert(context.Background(), "alpha", publisher, "src", workflow.VisibilityPublic, ""))

	require.NoError(t, svc.Unpublish(ctxWith(admin, true), "alpha"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowUnpublish, store.events[0].Action)
	assert.Equal(t, "alpha", store.events[0].Target)
}

// TestService_Duplicate_EmitsAuditEvent verifies an admin Duplicate records
// exactly one workflow.duplicate event naming the source.
func TestService_Duplicate_EmitsAuditEvent(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, _, _ := deleteSvc(t)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	require.NoError(t, svc.Duplicate(ctxWith(owner, true), "alpha", "beta"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowDuplicate, store.events[0].Action)
	assert.Equal(t, "beta", store.events[0].Target)
	assert.Contains(t, store.events[0].Detail, "alpha")
}

// TestService_Archive_EmitsAuditEvent verifies an admin Archive records exactly
// one workflow.archive event.
func TestService_Archive_EmitsAuditEvent(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, _, _ := deleteSvc(t)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	require.NoError(t, svc.Archive(ctxWith(owner, true), "alpha"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowArchive, store.events[0].Action)
	assert.Equal(t, "alpha", store.events[0].Target)
}

// TestService_Unarchive_EmitsAuditEvent verifies an admin Unarchive records
// exactly one workflow.unarchive event.
func TestService_Unarchive_EmitsAuditEvent(t *testing.T) {
	owner := uuid.New()
	svc, repo, reg, _, _ := deleteSvc(t)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	require.NoError(t, svc.Unarchive(ctxWith(owner, true), "alpha"))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowUnarchive, store.events[0].Action)
	assert.Equal(t, "alpha", store.events[0].Target)
}

// TestService_TransferOwnership_EmitsAuditEvent verifies an admin transfer of a
// private workflow records exactly one workflow.transfer_ownership event with
// the from/to owners.
func TestService_TransferOwnership_EmitsAuditEvent(t *testing.T) {
	owner := uuid.New()
	newOwner := uuid.New()
	svc, repo, reg, _, _ := deleteSvc(t)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	require.NoError(t, svc.TransferOwnership(ctxWith(owner, true), "alpha", newOwner))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowTransfer, store.events[0].Action)
	assert.Equal(t, "alpha", store.events[0].Target)
	assert.Contains(t, store.events[0].Detail, newOwner.String())
}

// TestService_AdminEditShared_EmitsEditSharedEvent verifies that when an admin
// edits a shared-only workflow in place (the adminSharedWrite escape hatch),
// exactly one workflow.edit_shared event is recorded.
func TestService_AdminEditShared_EmitsEditSharedEvent(t *testing.T) {
	repo := newFakeRepo()
	store := &recordingStore{}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetAudit(audit.NewService(store))
	admin := uuid.New()

	// A shared-only workflow (no private copy): the admin's Update lands in place.
	repo.bucket(workflow.Shared())["alpha"] = wf("alpha")

	require.NoError(t, svc.Update(ctxWith(admin, true), "alpha", wf("alpha")))

	require.Len(t, store.events, 1)
	assert.Equal(t, audit.ActionWorkflowEditShared, store.events[0].Action)
	assert.Equal(t, "alpha", store.events[0].Target)
}

// TestService_NonAdminSharedWrite_NoEditSharedEvent verifies a non-admin write
// to a shared-only workflow is rejected (ErrSharedReadOnly) and records nothing.
func TestService_NonAdminSharedWrite_NoEditSharedEvent(t *testing.T) {
	repo := newFakeRepo()
	store := &recordingStore{}
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetAudit(audit.NewService(store))
	user := uuid.New()
	repo.bucket(workflow.Shared())["alpha"] = wf("alpha")

	err := svc.Update(ctxWith(user, false), "alpha", wf("alpha"))
	require.ErrorIs(t, err, workflow.ErrSharedReadOnly)
	assert.Empty(t, store.events)
}

// failingStore returns an error on every Append, simulating an unavailable DB.
type failingStore struct{ err error }

func (f *failingStore) Append(_ context.Context, _ audit.Event) error { return f.err }
func (f *failingStore) ListByActor(context.Context, string, int) ([]audit.Event, error) {
	return nil, f.err
}
func (f *failingStore) ListByTarget(context.Context, string) ([]audit.Event, error) {
	return nil, f.err
}

// --- No event on operation failure ---

// TestService_Rename_FailedRename_NoAuditEvent verifies that a rejected Rename
// (non-admin caller → ErrForbidden) does not emit any audit event. Only
// successful mutations are audited.
func TestService_Rename_FailedRename_NoAuditEvent(t *testing.T) {
	owner := uuid.New()
	svc, _, _, _, _ := renameSvc(t, owner)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))

	err := svc.Rename(ctxWith(owner, false), "alpha", "beta")
	require.Error(t, err)
	assert.Empty(t, store.events, "failed Rename must not emit an audit event")
}

// TestService_Delete_GuardRefusal_NoAuditEvent verifies that a Delete rejected
// by the running-instances guard does not emit an audit event. The guard
// refuses before any mutation takes place.
func TestService_Delete_GuardRefusal_NoAuditEvent(t *testing.T) {
	svc, repo, reg, _, counter := deleteSvc(t)
	store := &recordingStore{}
	svc.SetAudit(audit.NewService(store))
	admin := uuid.New()
	idA := seedPrivateRegistered(t, repo, reg, admin, "alpha")
	counter.seed(idA, "running")

	err := svc.Delete(ctxWith(admin, true), "alpha")
	require.ErrorIs(t, err, workflow.ErrHasRunningInstances)
	assert.Empty(t, store.events, "guard-refused Delete must not emit an audit event")
}

// --- Audit best-effort for lifecycle ops ---

// TestService_Archive_AuditStoreFailure_OperationSucceeds verifies that a
// failure in the audit store does not propagate from Archive. Audit is
// best-effort: a transient DB outage must not block admin ops.
func TestService_Archive_AuditStoreFailure_OperationSucceeds(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)
	svc.SetAudit(audit.NewService(&failingStore{err: errors.New("db unavailable")}))

	require.NoError(t, svc.Archive(adminCtx, "alpha"),
		"Archive must succeed when the audit store fails — audit is best-effort")
}

// TestService_Rename_AuditStoreFailure_OperationSucceeds verifies that an audit
// store failure does not propagate from Rename. Audit is best-effort: the folder
// move and registry update must complete regardless of audit-DB health.
func TestService_Rename_AuditStoreFailure_OperationSucceeds(t *testing.T) {
	owner := uuid.New()
	svc, _, _, adminCtx, _ := renameSvc(t, owner)
	svc.SetAudit(audit.NewService(&failingStore{err: errors.New("db unavailable")}))

	require.NoError(t, svc.Rename(adminCtx, "alpha", "beta"),
		"Rename must succeed when the audit store fails — audit is best-effort")
}

// TestService_Delete_AuditStoreFailure_OperationSucceeds verifies that an audit
// store failure does not propagate from Delete. The folder and registry must be
// removed regardless of audit-DB health.
func TestService_Delete_AuditStoreFailure_OperationSucceeds(t *testing.T) {
	svc, repo, reg, _, _ := deleteSvc(t)
	svc.SetAudit(audit.NewService(&failingStore{err: errors.New("db unavailable")}))
	owner := uuid.New()
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	require.NoError(t, svc.Delete(ctxWith(owner, true), "alpha"),
		"Delete must succeed when the audit store fails — audit is best-effort")
}

// TestService_Transfer_AuditStoreFailure_OperationSucceeds verifies that an
// audit store failure does not propagate from TransferOwnership.
func TestService_Transfer_AuditStoreFailure_OperationSucceeds(t *testing.T) {
	owner := uuid.New()
	newOwner := uuid.New()
	svc, repo, reg, _, _ := deleteSvc(t)
	svc.SetAudit(audit.NewService(&failingStore{err: errors.New("db unavailable")}))
	seedPrivateRegistered(t, repo, reg, owner, "alpha")

	require.NoError(t, svc.TransferOwnership(ctxWith(owner, true), "alpha", newOwner),
		"TransferOwnership must succeed when the audit store fails — audit is best-effort")
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
