package audit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	events  []audit.Event
	failErr error
}

func (f *fakeStore) Append(_ context.Context, ev audit.Event) error {
	if f.failErr != nil {
		return f.failErr
	}
	f.events = append(f.events, ev)
	return nil
}
func (f *fakeStore) ListByActor(_ context.Context, _ string, _ int) ([]audit.Event, error) {
	return f.events, nil
}
func (f *fakeStore) ListByTarget(_ context.Context, target string) ([]audit.Event, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	out := make([]audit.Event, 0)
	for _, ev := range f.events {
		if ev.Target == target {
			out = append(out, ev)
		}
	}
	return out, nil
}

func TestService_Record_StampsActorFromContext(t *testing.T) {
	store := &fakeStore{}
	svc := audit.NewService(store)
	user := uuid.New()
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: user, Role: identity.RoleUser})

	svc.Record(ctx, audit.ActionWorkflowPublish, "triage", `{"to":"public"}`)

	require.Len(t, store.events, 1)
	assert.Equal(t, user.String(), store.events[0].ActorID)
	assert.Equal(t, audit.ActionWorkflowPublish, store.events[0].Action)
	assert.Equal(t, "triage", store.events[0].Target)
	assert.False(t, store.events[0].OccurredAt.IsZero())
	assert.NotEqual(t, uuid.Nil, store.events[0].ID)
}

func TestService_Record_NoIdentity_UsesSystemActor(t *testing.T) {
	store := &fakeStore{}
	svc := audit.NewService(store)
	svc.Record(context.Background(), audit.ActionAdminOverride, "x", "")
	require.Len(t, store.events, 1)
	assert.Equal(t, "system", store.events[0].ActorID)
}

func TestService_Record_StoreFailure_DoesNotPanicOrReturn(t *testing.T) {
	store := &fakeStore{failErr: errors.New("db down")}
	svc := audit.NewService(store)
	// Best-effort: Record must NOT propagate the failure (no return value).
	svc.Record(context.Background(), audit.ActionWorkflowPublish, "x", "")
	assert.Empty(t, store.events)
}

func TestService_Record_NilStore_IsNoop(t *testing.T) {
	var svc *audit.Service // nil-safe
	assert.NotPanics(t, func() {
		svc.Record(context.Background(), audit.ActionWorkflowPublish, "x", "")
	})
}

// Edge: NewService(nil) returns a non-nil Service with a nil store. Record must
// be a no-op without panicking — the s.store == nil guard must fire before any
// store interaction.
func TestService_Record_NewServiceWithNilStore_IsNoop(t *testing.T) {
	svc := audit.NewService(nil)
	assert.NotPanics(t, func() {
		svc.Record(context.Background(), audit.ActionWorkflowPublish, "x", "")
	})
}

// TestService_SetLogger_NilArgPreservesLogger verifies that SetLogger(nil) is
// a safe no-op: the existing logger is not replaced, and the service does not
// panic on subsequent Record calls (the store failure path still logs).
func TestService_SetLogger_NilArgPreservesLogger(t *testing.T) {
	store := &fakeStore{failErr: errors.New("db down")}
	svc := audit.NewService(store)
	svc.SetLogger(nil) // must not replace the logger with nil
	assert.NotPanics(t, func() {
		// Store fails → code tries to log; must not dereference a nil logger.
		svc.Record(context.Background(), audit.ActionWorkflowPublish, "x", "")
	})
}

// adminCtx returns a context carrying an admin identity (the authorized caller
// for the forensic ListByTarget surface).
func adminCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
}

// TestService_ListByTarget_DelegatesToStore verifies an admin caller's
// target-scoped query is forwarded to the store and its events returned.
func TestService_ListByTarget_DelegatesToStore(t *testing.T) {
	store := &fakeStore{events: []audit.Event{
		{ID: uuid.New(), Action: audit.ActionWorkflowRename, Target: "alpha"},
		{ID: uuid.New(), Action: audit.ActionWorkflowDelete, Target: "beta"},
	}}
	svc := audit.NewService(store)
	got, err := svc.ListByTarget(adminCtx(), "alpha")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "alpha", got[0].Target)
}

// TestService_ListByTarget_NonAdmin_Forbidden verifies the domain-side authz
// gate (Consider #1, option a): a non-admin caller is refused with
// identity.ErrForbidden BEFORE the store is queried, so a future transport that
// forgets to gate cannot leak the forensic audit trail.
func TestService_ListByTarget_NonAdmin_Forbidden(t *testing.T) {
	store := &fakeStore{events: []audit.Event{
		{ID: uuid.New(), Action: audit.ActionWorkflowRename, Target: "alpha"},
	}}
	svc := audit.NewService(store)
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleUser})

	_, err := svc.ListByTarget(ctx, "alpha")
	assert.ErrorIs(t, err, identity.ErrForbidden, "non-admin must be refused by the domain service")
}

// TestService_ListByTarget_NoIdentity_Forbidden verifies a context with no
// identity is also refused (the gate fails closed on an unauthenticated caller).
func TestService_ListByTarget_NoIdentity_Forbidden(t *testing.T) {
	svc := audit.NewService(&fakeStore{})
	_, err := svc.ListByTarget(context.Background(), "alpha")
	assert.ErrorIs(t, err, identity.ErrForbidden, "missing identity must be refused")
}

// TestService_ListByTarget_NilStore_EmptyNoError verifies a nil/unconfigured
// service returns an empty slice (not an error) for an authorized admin caller,
// so the query surface is harmless in local mode.
func TestService_ListByTarget_NilStore_EmptyNoError(t *testing.T) {
	var svc *audit.Service
	got, err := svc.ListByTarget(adminCtx(), "alpha")
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = audit.NewService(nil).ListByTarget(adminCtx(), "alpha")
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestService_Record_TwoCallsProduceDifferentIDs verifies that each Record
// invocation stamps a fresh UUID. Reusing the same ID would collapse two
// separate audit events into one in the store.
func TestService_Record_TwoCallsProduceDifferentIDs(t *testing.T) {
	store := &fakeStore{}
	svc := audit.NewService(store)
	svc.Record(context.Background(), audit.ActionWorkflowPublish, "wf-1", "")
	svc.Record(context.Background(), audit.ActionWorkflowVisibilityChange, "wf-1", "")
	require.Len(t, store.events, 2)
	assert.NotEqual(t, store.events[0].ID, store.events[1].ID,
		"each Record call must assign a distinct UUID")
}
