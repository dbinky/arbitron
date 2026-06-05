package engine

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func member(u uuid.UUID, depts ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Role: identity.RoleUser, Departments: depts,
	})
}

func lead(u uuid.UUID, led ...string) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: u, Role: identity.RoleUser, Departments: led, LedDepartments: led,
	})
}

func admin(u uuid.UUID) context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: u, Role: identity.RoleAdmin})
}

func inst(owner uuid.UUID, vis instance.Visibility, dept string) *instance.Instance {
	return &instance.Instance{ID: uuid.New(), OwnerID: owner, Visibility: vis, Department: dept}
}

// --- canView ---

func TestCanView_Owner(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(member(o), inst(o, instance.VisibilityPrivate, "")))
}
func TestCanView_Admin(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(admin(uuid.New()), inst(o, instance.VisibilityPrivate, "")))
}
func TestCanView_Public(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(member(uuid.New(), "finance"), inst(o, instance.VisibilityPublic, "engineering")))
}
func TestCanView_DepartmentMember(t *testing.T) {
	o := uuid.New()
	assert.True(t, canView(member(uuid.New(), "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanView_DepartmentNonMember_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canView(member(uuid.New(), "finance"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanView_OthersPrivate_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canView(member(uuid.New(), "engineering"), inst(o, instance.VisibilityPrivate, "engineering")))
}
func TestCanView_NoIdentity_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canView(context.Background(), inst(o, instance.VisibilityPublic, "")))
}

// --- canIntervene ---

func TestCanIntervene_Owner(t *testing.T) {
	o := uuid.New()
	assert.True(t, canIntervene(member(o, "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_Admin(t *testing.T) {
	o := uuid.New()
	assert.True(t, canIntervene(admin(uuid.New()), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_Lead(t *testing.T) {
	o := uuid.New()
	assert.True(t, canIntervene(lead(uuid.New(), "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_Peer_Denied(t *testing.T) {
	o := uuid.New()
	// A department MEMBER who is not owner and not lead: can view, cannot intervene.
	assert.False(t, canIntervene(member(uuid.New(), "engineering"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_LeadOfOtherDept_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canIntervene(lead(uuid.New(), "finance"), inst(o, instance.VisibilityDepartment, "engineering")))
}
func TestCanIntervene_NoIdentity_Denied(t *testing.T) {
	o := uuid.New()
	assert.False(t, canIntervene(context.Background(), inst(o, instance.VisibilityDepartment, "engineering")))
}
