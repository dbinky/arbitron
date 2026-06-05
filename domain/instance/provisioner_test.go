package instance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvUserRepo is a fake UserRepository capturing provisioning calls.
type fakeProvUserRepo struct {
	bySubject      map[string]*User
	getErr         error
	createErr      error
	created        []*User
	updatedNames   map[uuid.UUID]string
	updateNameErr  error
	createCalls    int
	depts          map[uuid.UUID][]string
	replaceDeptErr error
	updateRoleErr  error
	updateRoleCalls int
}

func newFakeProvUserRepo() *fakeProvUserRepo {
	return &fakeProvUserRepo{bySubject: map[string]*User{}, updatedNames: map[uuid.UUID]string{}, depts: map[uuid.UUID][]string{}}
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
func (r *fakeProvUserRepo) List(context.Context) ([]User, error)     { return nil, nil }
func (r *fakeProvUserRepo) CountAdmins(context.Context) (int, error) { return 0, nil }

// UpdateRole mutates the stored user (by ID, across both bySubject and created)
// so the live role re-evaluation tests can observe the change.
func (r *fakeProvUserRepo) UpdateRole(_ context.Context, id uuid.UUID, role string) error {
	r.updateRoleCalls++
	if r.updateRoleErr != nil {
		return r.updateRoleErr
	}
	for _, u := range r.bySubject {
		if u.ID == id {
			u.Role = identity.Role(role)
		}
	}
	for _, u := range r.created {
		if u.ID == id {
			u.Role = identity.Role(role)
		}
	}
	return nil
}

func (r *fakeProvUserRepo) ListDepartments(_ context.Context, id uuid.UUID) ([]string, error) {
	return append([]string(nil), r.depts[id]...), nil
}

func (r *fakeProvUserRepo) ReplaceDepartments(_ context.Context, id uuid.UUID, departments []string) error {
	if r.replaceDeptErr != nil {
		return r.replaceDeptErr
	}
	r.depts[id] = append([]string(nil), departments...)
	return nil
}

// Happy path: an unknown, non-bootstrap subject creates a plain user.
func TestProvisioner_Resolve_NewPlainUser(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})

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
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})

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
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})
	_, err := p.Resolve(context.Background(), Claims{Subject: "s"})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 0, repo.createCalls)

	repo2 := newFakeProvUserRepo()
	repo2.createErr = boom
	p2 := NewProvisioner(repo2, nil, identity.GroupConfig{DefaultGroup: "default"})
	_, err = p2.Resolve(context.Background(), Claims{Subject: "s2", Username: "u"})
	assert.ErrorIs(t, err, boom)
}

// Boundary: a known subject is returned without Create; username refresh fires
// only on change. Per RBAC Phase 1, role is now LIVE re-evaluated from the
// bootstrap config at every login, so a known user on the list is promoted.
func TestProvisioner_Resolve_KnownSubject(t *testing.T) {
	repo := newFakeProvUserRepo()
	id := uuid.New()
	sub := "sub-known"
	repo.bySubject[sub] = &User{ID: id, Subject: &sub, Username: "old", Role: identity.RoleUser}
	// subject is ALSO on the bootstrap list; config is authoritative at login.
	p := NewProvisioner(repo, []string{"sub-known"}, identity.GroupConfig{DefaultGroup: "default"})

	u, err := p.Resolve(context.Background(), Claims{Subject: sub, Username: "new"})
	require.NoError(t, err)
	assert.Equal(t, 0, repo.createCalls, "known subject must not be re-created")
	assert.Equal(t, identity.RoleAdmin, u.Role, "known bootstrap subject promoted at login (live re-eval)")
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
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})

	u, err := p.Resolve(context.Background(), Claims{Subject: sub, Username: "taken"})
	require.NoError(t, err, "UpdateUsername failure must not abort login")
	assert.Equal(t, "taken", u.Username, "in-memory username reflects the claim despite DB error")
	assert.Equal(t, 0, repo.createCalls, "no new user must be created")
}

// Edge: username="" and email="" -> username falls back to subject (third tier).
func TestProvisioner_Resolve_UsernameSubjectFallback(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})

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
		p := NewProvisioner(repo, []string{"admin-sub"}, identity.GroupConfig{DefaultGroup: "default"})
		u, err := p.Resolve(context.Background(), Claims{Subject: "admin-sub", Username: "a"})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleAdmin, u.Role)
	})

	t.Run("bootstrap by VERIFIED email case-insensitive", func(t *testing.T) {
		repo := newFakeProvUserRepo()
		p := NewProvisioner(repo, []string{"foo@bar.com"}, identity.GroupConfig{DefaultGroup: "default"})
		u, err := p.Resolve(context.Background(), Claims{Subject: "other-sub", Username: "a", Email: "Foo@Bar.com", EmailVerified: true})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleAdmin, u.Role)
	})

	t.Run("bootstrap by UNVERIFIED email is NOT an admin (privilege-escalation guard)", func(t *testing.T) {
		repo := newFakeProvUserRepo()
		p := NewProvisioner(repo, []string{"foo@bar.com"}, identity.GroupConfig{DefaultGroup: "default"})
		// Attacker self-asserts a bootstrap email at the IdP without verifying it.
		// The email must NOT drive bootstrap-admin matching; provision as RoleUser.
		u, err := p.Resolve(context.Background(), Claims{Subject: "attacker-sub", Username: "a", Email: "foo@bar.com", EmailVerified: false})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleUser, u.Role, "unverified bootstrap email must provision as RoleUser")
	})

	t.Run("bootstrap by subject is unaffected by email verification", func(t *testing.T) {
		repo := newFakeProvUserRepo()
		p := NewProvisioner(repo, []string{"admin-sub"}, identity.GroupConfig{DefaultGroup: "default"})
		// Subject is the IdP-stable, verifier-checked identifier; an unverified
		// email must not suppress a subject-list match.
		u, err := p.Resolve(context.Background(), Claims{Subject: "admin-sub", Username: "a", Email: "spoof@bar.com", EmailVerified: false})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleAdmin, u.Role, "subject-based bootstrap matching is unaffected by email_verified")
	})

	t.Run("substring of a listed email is not a match", func(t *testing.T) {
		repo := newFakeProvUserRepo()
		p := NewProvisioner(repo, []string{"foo@bar.com"}, identity.GroupConfig{DefaultGroup: "default"})
		u, err := p.Resolve(context.Background(), Claims{Subject: "s", Username: "a", Email: "xfoo@bar.com"})
		require.NoError(t, err)
		assert.Equal(t, identity.RoleUser, u.Role, "exact normalized match only")
	})

	t.Run("IsBootstrapAdmin consistent with provisioning", func(t *testing.T) {
		p := NewProvisioner(newFakeProvUserRepo(), []string{"foo@bar.com", "admin-sub"}, identity.GroupConfig{DefaultGroup: "default"})
		assert.True(t, p.IsBootstrapAdmin("admin-sub", ""))
		assert.True(t, p.IsBootstrapAdmin("any", "FOO@BAR.COM"))
		assert.False(t, p.IsBootstrapAdmin("nope", "nobody@x.com"))
	})
}

func TestResolve_PromotesExistingUserToAdminWhenInBootstrapList(t *testing.T) {
	repo := newFakeProvUserRepo()
	// Seed an existing non-admin user whose subject is now in the bootstrap list.
	subj := "alice-subject"
	repo.bySubject[subj] = &User{ID: uuid.New(), Subject: &subj, Username: "alice", Role: identity.RoleUser}

	p := NewProvisioner(repo, []string{"alice-subject"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{Subject: subj, Username: "alice"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if u.Role != identity.RoleAdmin {
		t.Errorf("role = %q, want admin (live promotion)", u.Role)
	}
}

func TestResolve_DemotesExistingAdminRemovedFromBootstrapList(t *testing.T) {
	repo := newFakeProvUserRepo()
	subj := "bob-subject"
	repo.bySubject[subj] = &User{ID: uuid.New(), Subject: &subj, Username: "bob", Role: identity.RoleAdmin}

	// Bootstrap list NO LONGER contains bob -> demote at login.
	p := NewProvisioner(repo, []string{"someone-else"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{Subject: subj, Username: "bob"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if u.Role != identity.RoleUser {
		t.Errorf("role = %q, want user (live demotion)", u.Role)
	}
}

func TestResolve_UnverifiedEmailDoesNotGrantAdmin(t *testing.T) {
	repo := newFakeProvUserRepo()
	// New user; email is in bootstrap list but NOT verified -> must not be admin.
	p := NewProvisioner(repo, []string{"eve@corp.example"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{
		Subject: "eve-subject", Username: "eve", Email: "eve@corp.example", EmailVerified: false,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if u.Role != identity.RoleUser {
		t.Errorf("role = %q, want user (unverified email must not bootstrap admin)", u.Role)
	}
}

func TestResolve_SnapshotsDepartmentsFromGroups(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{
		Subject: "carol-subject", Username: "carol", Groups: []string{"Engineering", " engineering ", "Platform"},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got := repo.depts[u.ID]
	want := []string{"engineering", "platform"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("snapshot = %v, want %v", got, want)
	}
}

func TestResolve_NoGroupsSnapshotsDefaultGroup(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{Subject: "dan-subject", Username: "dan"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !reflect.DeepEqual(repo.depts[u.ID], []string{"default"}) {
		t.Errorf("snapshot = %v, want [default]", repo.depts[u.ID])
	}
}

func TestResolve_ReplacesDepartmentsOnEachLogin(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})
	ctx := context.Background()
	u1, _ := p.Resolve(ctx, Claims{Subject: "ed-subject", Username: "ed", Groups: []string{"sales"}})
	// Second login with a different group set fully replaces the first snapshot.
	_, _ = p.Resolve(ctx, Claims{Subject: "ed-subject", Username: "ed", Groups: []string{"eng"}})
	if !reflect.DeepEqual(repo.depts[u1.ID], []string{"eng"}) {
		t.Errorf("snapshot = %v, want [eng] after re-login", repo.depts[u1.ID])
	}
}

// Failure: if ReplaceDepartments fails, Resolve must return an error and not
// return a user whose department state is inconsistent with their claims.
func TestResolve_SnapshotFailurePropagates(t *testing.T) {
	boom := errors.New("dept store down")
	repo := newFakeProvUserRepo()
	repo.replaceDeptErr = boom
	p := NewProvisioner(repo, nil, identity.GroupConfig{DefaultGroup: "default"})

	_, err := p.Resolve(context.Background(), Claims{Subject: "fiona-subject", Username: "fiona"})
	if !errors.Is(err, boom) {
		t.Errorf("expected wrapped dept-store error, got %v", err)
	}
}

// Edge: bootstrap admin also gets departments snapshotted — the snapshot is
// unconditional (runs after the role path, for both new and existing users).
func TestResolve_AdminAlsoGetsDepartmentsSnapshotted(t *testing.T) {
	repo := newFakeProvUserRepo()
	p := NewProvisioner(repo, []string{"admin-subject"}, identity.GroupConfig{DefaultGroup: "default"})

	u, err := p.Resolve(context.Background(), Claims{
		Subject: "admin-subject", Username: "ga", Groups: []string{"engineering"},
	})
	require.NoError(t, err)
	require.Equal(t, identity.RoleAdmin, u.Role)
	if !reflect.DeepEqual(repo.depts[u.ID], []string{"engineering"}) {
		t.Errorf("admin snapshot = %v, want [engineering] (admins are not exempt from dept snapshot)", repo.depts[u.ID])
	}
}

// Failure: UpdateRole failure during live demotion must abort the login.
// If a removed-from-bootstrap admin's demotion fails silently, they retain
// privilege beyond the config's intent. The login must fail so the inconsistency
// is surfaced immediately.
func TestResolve_DemotionFailurePropagates(t *testing.T) {
	dbErr := errors.New("role store down")
	repo := newFakeProvUserRepo()
	subj := "formerly-admin"
	repo.bySubject[subj] = &User{ID: uuid.New(), Subject: &subj, Username: "fa", Role: identity.RoleAdmin}
	repo.updateRoleErr = dbErr

	// Bootstrap list no longer contains this subject → demotion required, but DB fails.
	p := NewProvisioner(repo, []string{"someone-else"}, identity.GroupConfig{DefaultGroup: "default"})
	_, err := p.Resolve(context.Background(), Claims{Subject: subj, Username: "fa"})
	if !errors.Is(err, dbErr) {
		t.Errorf("expected wrapped role-store error on failed demotion, got %v", err)
	}
}

// Edge: an existing admin still on the bootstrap list must NOT call UpdateRole
// (the role is unchanged — avoid a spurious write on every login).
func TestResolve_ExistingAdminStillOnList_NoRoleWrite(t *testing.T) {
	repo := newFakeProvUserRepo()
	subj := "still-admin"
	repo.bySubject[subj] = &User{ID: uuid.New(), Subject: &subj, Username: "sa", Role: identity.RoleAdmin}

	p := NewProvisioner(repo, []string{"still-admin"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.Resolve(context.Background(), Claims{Subject: subj, Username: "sa"})
	require.NoError(t, err)
	assert.Equal(t, identity.RoleAdmin, u.Role)
	assert.Equal(t, 0, repo.updateRoleCalls, "UpdateRole must not be called when the role is unchanged")
}
