package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// svInstances overrides Get only; any other call panics on the nil embedded interface.
type svInstances struct {
	instance.InstanceRepository
	byID map[uuid.UUID]*instance.Instance
	err  error
}

func (s svInstances) Get(_ context.Context, id uuid.UUID) (*instance.Instance, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byID[id], nil
}

func TestInstanceVisibility(t *testing.T) {
	owner, other := uuid.New(), uuid.New()
	run := uuid.New()
	v := NewInstanceVisibility(svInstances{byID: map[uuid.UUID]*instance.Instance{run: {ID: run, OwnerID: owner, Visibility: instance.VisibilityPrivate}}})
	user := func(id uuid.UUID, r identity.Role) context.Context {
		return identity.WithIdentity(context.Background(), identity.Identity{UserID: id, Role: r})
	}
	assert.True(t, v.CanView(user(owner, identity.RoleUser), run))
	assert.False(t, v.CanView(user(other, identity.RoleUser), run))
	assert.True(t, v.CanView(user(other, identity.RoleAdmin), run))
	assert.False(t, v.CanView(user(owner, identity.RoleAdmin), uuid.New()), "unknown run (e.g. synthetic probe run) is hidden")
	assert.False(t, NewInstanceVisibility(svInstances{err: errors.New("db")}).CanView(user(owner, identity.RoleAdmin), run), "errors fail closed")
	assert.Panics(t, func() { NewInstanceVisibility(nil) })
}

func TestRunnerAuditActions(t *testing.T) {
	assert.Equal(t, "runner.update", audit.ActionRunnerUpdate)
	assert.Equal(t, "runner.cordon", audit.ActionRunnerCordon)
	assert.Equal(t, "runner.uncordon", audit.ActionRunnerUncordon)
	assert.Equal(t, "runner.source_probe", audit.ActionRunnerSourceProbe)
	assert.Equal(t, "runner_key.create", audit.ActionRunnerKeyCreate)
	assert.Equal(t, "runner_key.revoke", audit.ActionRunnerKeyRevoke)
	assert.Equal(t, "runner.drain", audit.ActionRunnerDrain)
}
