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

// fakeProvUserRepo is a fake UserRepository capturing provisioning calls.
type fakeProvUserRepo struct {
	bySubject     map[string]*User
	getErr        error
	createErr     error
	created       []*User
	updatedNames  map[uuid.UUID]string
	updateNameErr error
	createCalls   int
}

func newFakeProvUserRepo() *fakeProvUserRepo {
	return &fakeProvUserRepo{bySubject: map[string]*User{}, updatedNames: map[uuid.UUID]string{}}
}

func (r *fakeProvUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) {
	return uuid.Nil, nil
}
func (r *fakeProvUserRepo) GetByID(context.Context, uuid.UUID) (*User, error) {
	return nil, ErrNotFound
}
func (r *fakeProvUserRepo) GetBySubject(_ context.Context, subject string) (*User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if u, ok := r.bySubject[subject]; ok {
		return u, nil
	}
	return nil, ErrNotFound
}
func (r *fakeProvUserRepo) Create(_ context.Context, u *User) error {
	r.createCalls++
	if r.createErr != nil {
		return r.createErr
	}
	r.created = append(r.created, u)
	if u.Subject != nil {
		r.bySubject[*u.Subject] = u
	}
	return nil
}
func (r *fakeProvUserRepo) UpdateUsername(_ context.Context, id uuid.UUID, username string) error {
	if r.updateNameErr != nil {
		return r.updateNameErr
	}
	r.updatedNames[id] = username
	return nil
}
func (r *fakeProvUserRepo) List(context.Context) ([]User, error)                { return nil, nil }
func (r *fakeProvUserRepo) UpdateRole(context.Context, uuid.UUID, string) error { return nil }
func (r *fakeProvUserRepo) CountAdmins(context.Context) (int, error)            { return 0, nil }

// Happy path: an unknown, non-bootstrap subject creates a plain user.
func TestProvisioner_Resolve_NewPlainUser(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil)

	u, err := p.Resolve(context.Background(), Claims{Subject: "sub-1", Username: "alice", Email: "alice@x.com"})
	require.NoError(t, err)
	require.Equal(t, 1, repo.createCalls)
	assert.Equal(t, identity.RoleUser, u.Role)
	require.NotNil(t, u.Subject)
	assert.Equal(t, "sub-1", *u.Subject)
	assert.Equal(t, "alice", u.Username)
	require.NotNil(t, u.Email)
	assert.Equal(t, "alice@x.com", *u.Email)
}

// Input validation: empty subject -> ErrInvalidClaims, no write. Empty username
// falls back to email.
func TestProvisioner_Resolve_Validation(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil)

	_, err := p.Resolve(context.Background(), Claims{Subject: "", Username: "x"})
	assert.ErrorIs(t, err, ErrInvalidClaims)
	assert.Equal(t, 0, repo.createCalls)

	u, err := p.Resolve(context.Background(), Claims{Subject: "sub-2", Username: "", Email: "bob@x.com"})
	require.NoError(t, err)
	assert.Equal(t, "bob@x.com", u.Username, "username falls back to email")
	assert.NotEmpty(t, u.Username)
}

// Error/failure: a non-ErrNotFound GetBySubject error propagates without a write;
// a Create error propagates.
func TestProvisioner_Resolve_Errors(t *testing.T) {
	boom := errors.New("db down")
	repo := newFakeProvUserRepo()
	repo.getErr = boom
	p := NewProvisioner(repo, nil)
	_, err := p.Resolve(context.Background(), Claims{Subject: "s"})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 0, repo.createCalls)

	repo2 := newFakeProvUserRepo()
	repo2.createErr = boom
	p2 := NewProvisioner(repo2, nil)
	_, err = p2.Resolve(context.Background(), Claims{Subject: "s2", Username: "u"})
	assert.ErrorIs(t, err, boom)
}

// Boundary: a known subject is returned without Create and without role change,
// even if also on the bootstrap list; username refresh fires only on change.
func TestProvisioner_Resolve_KnownSubject(t *testing.T) {
	repo := newFakeProvUserRepo()
	id := uuid.New()
	sub := "sub-known"
	repo.bySubject[sub] = &User{ID: id, Subject: &sub, Username: "old", Role: identity.RoleUser}
	// subject is ALSO on the bootstrap list; must not be promoted on login.
	p := NewProvisioner(repo, []string{"sub-known"})

	u, err := p.Resolve(context.Background(), Claims{Subject: sub, Username: "new"})
	require.NoError(t, err)
	assert.Equal(t, 0, repo.createCalls, "known subject must not be re-created")
	assert.Equal(t, identity.RoleUser, u.Role, "role unchanged on login (promotion is Task 6)")
	assert.Equal(t, "new", u.Username)
	assert.Equal(t, "new", repo.updatedNames[id], "username refreshed on change")

	// Unchanged username -> no UpdateUsername.
	repo.updatedNames = map[uuid.UUID]string{}
	_, err = p.Resolve(context.Background(), Claims{Subject: sub, Username: "new"})
	require.NoError(t, err)
	_, refreshed := repo.updatedNames[id]
	assert.False(t, refreshed, "no refresh when username unchanged")
}

// Error/edge: UpdateUsername collision must not abort login (best-effort refresh).
// The code deliberately ignores the error from UpdateUsername so a colliding
// username from another user's row never breaks an active login round-trip.
func TestProvisioner_Resolve_UpdateUsernameCollisionTolerated(t *testing.T) {
	repo := newFakeProvUserRepo()
	id := uuid.New()
	sub := "sub-collision"
	repo.bySubject[sub] = &User{ID: id, Subject: &sub, Username: "old", Role: identity.RoleUser}
	repo.updateNameErr = errors.New("username already taken by another user")
	p := NewProvisioner(repo, nil)

	u, err := p.Resolve(context.Background(), Claims{Subject: sub, Username: "taken"})
	require.NoError(t, err, "UpdateUsername failure must not abort login")
	assert.Equal(t, "taken", u.Username, "in-memory username reflects the claim despite DB error")
	assert.Equal(t, 0, repo.createCalls, "no new user must be created")
}

// Edge: username="" and email="" -> username falls back to subject (third tier).
func TestProvisioner_Resolve_UsernameSubjectFallback(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil)

	u, err := p.Resolve(context.Background(), Claims{Subject: "sub-only"})
	require.NoError(t, err)
	assert.Equal(t, "sub-only", u.Username, "username must fall back to subject when both username and email are empty")
}

// Isolation/security: bootstrap by subject and by email both create admins;
// matching is case-insensitive and exact (not substring); IsBootstrapAdmin is
// consistent with the provisioning verdict.
func TestProvisioner_Bootstrap(t *testing.T) {
	t.Run("bootstrap by subject", func(t *testing.T) {
		repo := newFakeProvUserRepo()
		p := NewProvisioner(repo, []string{"admin-sub"})
		u, err := p.Resolve(context.Background(), Claims{Subject: "admin-sub", Username: "a"})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleAdmin, u.Role)
	})

	t.Run("bootstrap by email case-insensitive", func(t *testing.T) {
		repo := newFakeProvUserRepo()
		p := NewProvisioner(repo, []string{"foo@bar.com"})
		u, err := p.Resolve(context.Background(), Claims{Subject: "other-sub", Username: "a", Email: "Foo@Bar.com"})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleAdmin, u.Role)
	})

	t.Run("substring of a listed email is not a match", func(t *testing.T) {
		repo := newFakeProvUserRepo()
		p := NewProvisioner(repo, []string{"foo@bar.com"})
		u, err := p.Resolve(context.Background(), Claims{Subject: "s", Username: "a", Email: "xfoo@bar.com"})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleUser, u.Role, "exact normalized match only")
	})

	t.Run("IsBootstrapAdmin consistent with provisioning", func(t *testing.T) {
		p := NewProvisioner(newFakeProvUserRepo(), []string{"foo@bar.com", "admin-sub"})
		assert.True(t, p.IsBootstrapAdmin("admin-sub", ""))
		assert.True(t, p.IsBootstrapAdmin("any", "FOO@BAR.COM"))
		assert.False(t, p.IsBootstrapAdmin("nope", "nobody@x.com"))
	})
}
