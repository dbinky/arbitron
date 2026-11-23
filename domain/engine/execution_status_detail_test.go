package engine

import (
	"context"
	"errors"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

type statusDetailRepo struct {
	instance.InstanceRepository
	row *instance.Instance
}

func (r statusDetailRepo) Get(context.Context, uuid.UUID) (*instance.Instance, error) {
	return r.row, nil
}

type detailProvider struct {
	calls int
	err   error
}

func (p *detailProvider) ExecutionStatuses(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]instance.ExecutionStatus, error) {
	p.calls++
	return map[uuid.UUID][]instance.ExecutionStatus{ids[0]: {{RunID: ids[0], State: instance.ExecQueuedSlot}}}, p.err
}
func TestGetExecutionStatus_AuthorizesBeforeProjection(t *testing.T) {
	id, owner := uuid.New(), uuid.New()
	p := &detailProvider{}
	e := &Engine{instances: statusDetailRepo{row: &instance.Instance{ID: id, OwnerID: owner}}, enricher: &InstanceEnricher{execStatus: p}}
	_, err := e.GetExecutionStatus(context.Background(), id)
	require.ErrorIs(t, err, instance.ErrNotFound)
	require.Zero(t, p.calls)
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: owner, Role: identity.RoleUser})
	got, err := e.GetExecutionStatus(ctx, id)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, id, got[0].RunID)
	p.err = errors.New("ledger unavailable")
	_, err = e.GetExecutionStatus(ctx, id)
	require.ErrorIs(t, err, p.err)
}
