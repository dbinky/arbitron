package instance

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeUserSvcRepo is a fake UserRepository for UserService tests.
type fakeUserSvcRepo struct {
	byID          map[uuid.UUID]*User
	listResult    []User
	adminCount    int
	getErr        error
	updateRoleErr error
	countErr      error
	listErr       error

	updatedRoles map[uuid.UUID]string
	updateCalls  int
	listCalls    int
}

func newFakeUserSvcRepo() *fakeUserSvcRepo {
	return &fakeUserSvcRepo{byID: map[uuid.UUID]*User{}, updatedRoles: map[uuid.UUID]string{}}
}

func (r *fakeUserSvcRepo) GetDefaultUser(context.Context) (uuid.UUID, error) { return uuid.Nil, nil }
func (r *fakeUserSvcRepo) GetByID(_ context.Context, id uuid.UUID) (*User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, ErrNotFound
}
func (r *fakeUserSvcRepo) UsersByID(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]User, error) {
	out := make(map[uuid.UUID]User, len(ids))
	for _, id := range ids {
		if u, ok := r.byID[id]; ok {
			out[id] = *u
		}
	}
	return out, nil
}
func (r *fakeUserSvcRepo) GetBySubject(context.Context, string) (*User, error) {
	return nil, ErrNotFound
}
func (r *fakeUserSvcRepo) GetByEmail(context.Context, string) (*User, error) {
	return nil, ErrNotFound
}
func (r *fakeUserSvcRepo) Create(context.Context, *User) error                     { return nil }
func (r *fakeUserSvcRepo) UpdateUsername(context.Context, uuid.UUID, string) error { return nil }
func (r *fakeUserSvcRepo) List(context.Context) ([]User, error) {
	r.listCalls++
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.listResult, nil
}
func (r *fakeUserSvcRepo) UpdateRole(_ context.Context, id uuid.UUID, role string) error {
	r.updateCalls++
	if r.updateRoleErr != nil {
		return r.updateRoleErr
	}
	r.updatedRoles[id] = role
	if u, ok := r.byID[id]; ok {
		u.Role = identity.Role(role)
	}
	return nil
}
func (r *fakeUserSvcRepo) CountAdmins(context.Context) (int, error) {
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.adminCount, nil
}
func (r *fakeUserSvcRepo) ListDepartments(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (r *fakeUserSvcRepo) ReplaceDepartments(context.Context, uuid.UUID, []string) error {
	return nil
}

// stubBootstrap records the args it was called with.
type stubBootstrap struct {
	admins    map[string]bool // normalized subject/email -> true
	calledSub string
	calledEml string
}

func (s *stubBootstrap) IsBootstrapAdmin(subject, email string) bool {
	s.calledSub, s.calledEml = subject, email
	return s.admins[subject] || s.admins[email]
}

func adminCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
}
func userCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleUser})
}

func strptr(s string) *string { return &s }

// Happy path: admin promotes; admin demotes a non-bootstrap admin when another
// remains; List returns all users.
func TestUserService_HappyPath(t *testing.T) {
	repo := newFakeUserSvcRepo()
	svc := NewUserService(repo, &stubBootstrap{admins: map[string]bool{}})

	// Promote.
	target := uuid.New()
	repo.byID[target] = &User{ID: target, Username: "u", Role: identity.RoleUser}
	require.NoError(t, svc.SetRole(adminCtx(), target, "admin"))
	assert.Equal(t, "admin", repo.updatedRoles[target])

	// Demote a non-bootstrap admin with another admin remaining.
	admin2 := uuid.New()
	repo.byID[admin2] = &User{ID: admin2, Subject: strptr("a2"), Username: "a2", Role: identity.RoleAdmin}
	repo.adminCount = 2
	require.NoError(t, svc.SetRole(adminCtx(), admin2, "user"))
	assert.Equal(t, "user", repo.updatedRoles[admin2])

	// List.
	repo.listResult = []User{{ID: target, Username: "u"}}
	users, err := svc.List(adminCtx())
	require.NoError(t, err)
	assert.Len(t, users, 1)
}

// Input validation: bad role; no-op when target already has the role.
func TestUserService_Validation(t *testing.T) {
	repo := newFakeUserSvcRepo()
	svc := NewUserService(repo, &stubBootstrap{admins: map[string]bool{}})
	target := uuid.New()
	repo.byID[target] = &User{ID: target, Role: identity.RoleUser}

	err := svc.SetRole(adminCtx(), target, "superuser")
	assert.ErrorIs(t, err, ErrInvalidRole)
	assert.Equal(t, 0, repo.updateCalls)

	// No-op: already a user.
	require.NoError(t, svc.SetRole(adminCtx(), target, "user"))
	assert.Equal(t, 0, repo.updateCalls, "idempotent no-op writes nothing")
}

// Error/failure: GetByID/CountAdmins/UpdateRole errors propagate for SetRole;
// List repo error propagates when caller is an authenticated admin.
func TestUserService_Errors(t *testing.T) {
	boom := errors.New("db down")

	repo := newFakeUserSvcRepo()
	repo.getErr = ErrNotFound
	svc := NewUserService(repo, &stubBootstrap{})
	err := svc.SetRole(adminCtx(), uuid.New(), "admin")
	assert.ErrorIs(t, err, ErrNotFound)

	repo2 := newFakeUserSvcRepo()
	target := uuid.New()
	repo2.byID[target] = &User{ID: target, Subject: strptr("a"), Role: identity.RoleAdmin}
	repo2.countErr = boom
	svc2 := NewUserService(repo2, &stubBootstrap{admins: map[string]bool{}})
	err = svc2.SetRole(adminCtx(), target, "user")
	assert.ErrorIs(t, err, boom)

	repo3 := newFakeUserSvcRepo()
	repo3.byID[target] = &User{ID: target, Role: identity.RoleUser}
	repo3.updateRoleErr = boom
	svc3 := NewUserService(repo3, &stubBootstrap{admins: map[string]bool{}})
	err = svc3.SetRole(adminCtx(), target, "admin")
	assert.ErrorIs(t, err, boom)

	// List repo error propagates when the caller is an authenticated admin.
	repo4 := newFakeUserSvcRepo()
	repo4.listErr = boom
	svc4 := NewUserService(repo4, &stubBootstrap{})
	_, err = svc4.List(adminCtx())
	assert.ErrorIs(t, err, boom, "List must propagate repo errors to the caller")
	assert.Equal(t, 1, repo4.listCalls, "List called once before error")
}

// Boundary: last admin cannot be demoted; self-demote allowed when another
// remains; promotion never blocked by floors.
func TestUserService_Boundary(t *testing.T) {
	// Last admin.
	repo := newFakeUserSvcRepo()
	target := uuid.New()
	repo.byID[target] = &User{ID: target, Subject: strptr("a"), Role: identity.RoleAdmin}
	repo.adminCount = 1
	svc := NewUserService(repo, &stubBootstrap{admins: map[string]bool{}})
	err := svc.SetRole(adminCtx(), target, "user")
	assert.ErrorIs(t, err, ErrLastAdmin)
	assert.Equal(t, 0, repo.updateCalls)

	// Self-demotion with another admin remaining.
	repo2 := newFakeUserSvcRepo()
	self := uuid.New()
	repo2.byID[self] = &User{ID: self, Subject: strptr("self"), Role: identity.RoleAdmin}
	repo2.adminCount = 2
	svc2 := NewUserService(repo2, &stubBootstrap{admins: map[string]bool{}})
	selfCtx := identity.WithIdentity(context.Background(), identity.Identity{UserID: self, Role: identity.RoleAdmin})
	require.NoError(t, svc2.SetRole(selfCtx, self, "user"))
	assert.Equal(t, "user", repo2.updatedRoles[self])

	// Promotion of a user is never blocked even with adminCount==1.
	repo3 := newFakeUserSvcRepo()
	u := uuid.New()
	repo3.byID[u] = &User{ID: u, Role: identity.RoleUser}
	repo3.adminCount = 1
	svc3 := NewUserService(repo3, &stubBootstrap{admins: map[string]bool{}})
	require.NoError(t, svc3.SetRole(adminCtx(), u, "admin"))
}

// Isolation/security: non-admin forbidden (no writes); no identity ->
// unauthenticated; bootstrap admin (by subject and by email) protected.
func TestUserService_Security(t *testing.T) {
	t.Run("non-admin forbidden", func(t *testing.T) {
		repo := newFakeUserSvcRepo()
		svc := NewUserService(repo, &stubBootstrap{})
		err := svc.SetRole(userCtx(), uuid.New(), "admin")
		assert.ErrorIs(t, err, ErrForbidden)
		_, err = svc.List(userCtx())
		assert.ErrorIs(t, err, ErrForbidden)
		assert.Equal(t, 0, repo.updateCalls)
		assert.Equal(t, 0, repo.listCalls)
	})

	t.Run("no identity unauthenticated", func(t *testing.T) {
		svc := NewUserService(newFakeUserSvcRepo(), &stubBootstrap{})
		assert.ErrorIs(t, svc.SetRole(context.Background(), uuid.New(), "admin"), ErrUnauthenticated)
		_, err := svc.List(context.Background())
		assert.ErrorIs(t, err, ErrUnauthenticated)
	})

	t.Run("bootstrap admin protected by subject", func(t *testing.T) {
		repo := newFakeUserSvcRepo()
		target := uuid.New()
		repo.byID[target] = &User{ID: target, Subject: strptr("boss-sub"), Email: strptr("boss@x.com"), Role: identity.RoleAdmin}
		repo.adminCount = 5 // plenty of admins; still protected
		boot := &stubBootstrap{admins: map[string]bool{"boss-sub": true}}
		svc := NewUserService(repo, boot)
		err := svc.SetRole(adminCtx(), target, "user")
		assert.ErrorIs(t, err, ErrBootstrapAdminProtected)
		assert.Equal(t, 0, repo.updateCalls)
		// Floor consulted with the persisted subject AND email.
		assert.Equal(t, "boss-sub", boot.calledSub)
		assert.Equal(t, "boss@x.com", boot.calledEml)
	})

	t.Run("bootstrap admin protected by email only", func(t *testing.T) {
		repo := newFakeUserSvcRepo()
		target := uuid.New()
		repo.byID[target] = &User{ID: target, Subject: strptr("plain-sub"), Email: strptr("boss@x.com"), Role: identity.RoleAdmin}
		repo.adminCount = 5
		boot := &stubBootstrap{admins: map[string]bool{"boss@x.com": true}}
		svc := NewUserService(repo, boot)
		err := svc.SetRole(adminCtx(), target, "user")
		assert.ErrorIs(t, err, ErrBootstrapAdminProtected, "email-listed admin protected with no live claims")
	})
}
