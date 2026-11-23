package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestCanViewInstance(t *testing.T) {
	owner, other := uuid.New(), uuid.New()
	priv := &instance.Instance{ID: uuid.New(), OwnerID: owner, Visibility: instance.VisibilityPrivate}
	pub := &instance.Instance{ID: uuid.New(), OwnerID: owner, Visibility: instance.VisibilityPublic}
	as := func(id uuid.UUID, role identity.Role) context.Context {
		return identity.WithIdentity(context.Background(), identity.Identity{UserID: id, Role: role})
	}
	assert.True(t, engine.CanViewInstance(as(owner, identity.RoleUser), priv))
	assert.False(t, engine.CanViewInstance(as(other, identity.RoleUser), priv))
	assert.True(t, engine.CanViewInstance(as(other, identity.RoleAdmin), priv))
	assert.True(t, engine.CanViewInstance(as(other, identity.RoleUser), pub))
	assert.False(t, engine.CanViewInstance(context.Background(), pub), "no identity fails closed")
	assert.False(t, engine.CanViewInstance(as(owner, identity.RoleAdmin), nil), "nil instance fails closed")
}
