package main

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ONE concrete resolver must satisfy BOTH domain ports, so the composition
// root can pass the same value to the schedule evaluator and the spawn
// executor and they can never resolve a user differently.
var _ engine.IdentityResolver = userIdentityResolver{}

// stubUserRepo is the minimal instance.UserRepository the resolver needs.
type stubUserRepo struct {
	instance.UserRepository
	users   map[uuid.UUID]*instance.User
	depts   map[uuid.UUID][]string
	deptErr error
}

func (r stubUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	u, ok := r.users[id]
	if !ok {
		return nil, instance.ErrNotFound
	}
	return u, nil
}

func (r stubUserRepo) ListDepartments(_ context.Context, id uuid.UUID) ([]string, error) {
	if r.deptErr != nil {
		return nil, r.deptErr
	}
	return r.depts[id], nil
}

// The local-mode default user (seeded by migration 001 as the admin "local")
// resolves cleanly through the SAME builder the request path uses. This is the
// precondition the spawn wiring depends on: without it, every spawn in a
// local-mode deployment would fail at identity reconstruction.
func TestUserIdentityResolver_LocalDefaultUser(t *testing.T) {
	localID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	repo := stubUserRepo{
		users: map[uuid.UUID]*instance.User{
			localID: {ID: localID, Username: "local", Role: identity.RoleAdmin},
		},
		depts: map[uuid.UUID][]string{},
	}
	r := userIdentityResolver{users: repo, groups: identity.GroupConfig{DefaultGroup: "default"}}

	got, err := r.IdentityForUser(context.Background(), localID)
	require.NoError(t, err, "the local-mode default user must resolve; every spawn depends on it")
	assert.Equal(t, localID, got.UserID)
	assert.Equal(t, identity.RoleAdmin, got.Role)
	assert.Equal(t, []string{"default"}, got.Departments,
		"a user with no stored departments still belongs to the default group")
}

func TestUserIdentityResolver_CarriesStoredDepartments(t *testing.T) {
	uid := uuid.New()
	repo := stubUserRepo{
		users: map[uuid.UUID]*instance.User{uid: {ID: uid, Username: "dev", Role: identity.RoleUser}},
		depts: map[uuid.UUID][]string{uid: {"engineering", "platform"}},
	}
	r := userIdentityResolver{users: repo, groups: identity.GroupConfig{DefaultGroup: "default"}}

	got, err := r.IdentityForUser(context.Background(), uid)
	require.NoError(t, err)
	assert.Equal(t, []string{"engineering", "platform"}, got.Departments,
		"the reconstructed identity must carry the owner's departments so a "+
			"department-scoped child workflow resolves")
}

func TestUserIdentityResolver_UnknownUserErrors(t *testing.T) {
	r := userIdentityResolver{
		users:  stubUserRepo{users: map[uuid.UUID]*instance.User{}, depts: map[uuid.UUID][]string{}},
		groups: identity.GroupConfig{DefaultGroup: "default"},
	}
	_, err := r.IdentityForUser(context.Background(), uuid.New())
	require.Error(t, err, "identity resolution must fail rather than degrade to a system identity")
}

// userIdentityResolver.IdentityForUser duplicates AuthMiddleware.enrich's
// Subject/Email extraction (main.go:1384-1391 vs auth_middleware.go:315-322)
// rather than sharing it, because the two live on opposite sides of the
// domain/adapter boundary. auth_middleware_test.go thoroughly pins that the
// request path's copy threads Subject/Email into LedDepartments (e.g.
// TestAuthMiddleware_Enrich_SubjectLead_Match), but nothing pinned the
// resolver's own copy — every existing resolver test leaves Subject/Email nil,
// so a regression that dropped or swapped the two pointer derefs in main.go
// (breaking the "identical identity across request/schedule/spawn paths"
// guarantee the type's docstring promises) would go undetected.
func TestUserIdentityResolver_SubjectAndEmailDriveLedDepartments(t *testing.T) {
	uid := uuid.New()
	subject := "svc-subject-id"
	email := "lead@corp.example"
	repo := stubUserRepo{
		users: map[uuid.UUID]*instance.User{
			uid: {ID: uid, Username: "lead", Role: identity.RoleUser, Subject: &subject, Email: &email},
		},
		depts: map[uuid.UUID][]string{},
	}
	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"svc-subject-id", "lead@corp.example"}},
	}
	r := userIdentityResolver{users: repo, groups: groups}

	got, err := r.IdentityForUser(context.Background(), uid)
	require.NoError(t, err)
	assert.Equal(t, []string{"platform"}, got.LedDepartments,
		"Subject/Email on the User row must reach LedDepartments through the "+
			"resolver's own copy of the enrich logic, matching the request path")
}

// A ListDepartments failure (e.g. a transient DB error) must propagate rather
// than silently building a departmentless identity: a spawn or schedule fire
// that resolves the wrong (under-scoped) identity for its owner is a silent
// authz regression, not a recoverable degradation.
func TestUserIdentityResolver_ListDepartmentsErrorPropagates(t *testing.T) {
	uid := uuid.New()
	repo := stubUserRepo{
		users:   map[uuid.UUID]*instance.User{uid: {ID: uid, Username: "dev", Role: identity.RoleUser}},
		deptErr: errors.New("departments unavailable"),
	}
	r := userIdentityResolver{users: repo, groups: identity.GroupConfig{DefaultGroup: "default"}}

	_, err := r.IdentityForUser(context.Background(), uid)
	require.Error(t, err, "a ListDepartments failure must propagate, not degrade to a departmentless identity")
}
