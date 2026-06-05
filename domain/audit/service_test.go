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
