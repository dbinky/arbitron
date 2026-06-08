package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// memUserRepo is an in-memory instance.UserRepository for user-tool tests.
type memUserRepo struct {
	mu    sync.Mutex
	byID  map[uuid.UUID]*instance.User
	depts map[uuid.UUID][]string
}

func newMemUserRepo() *memUserRepo {
	return &memUserRepo{byID: map[uuid.UUID]*instance.User{}, depts: map[uuid.UUID][]string{}}
}

func (r *memUserRepo) GetDefaultUser(context.Context) (uuid.UUID, error) { return uuid.Nil, nil }
func (r *memUserRepo) GetByID(_ context.Context, id uuid.UUID) (*instance.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, instance.ErrNotFound
}
func (r *memUserRepo) GetBySubject(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *memUserRepo) GetByEmail(context.Context, string) (*instance.User, error) {
	return nil, instance.ErrNotFound
}
func (r *memUserRepo) Create(_ context.Context, u *instance.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[u.ID] = u
	return nil
}
func (r *memUserRepo) UpdateUsername(context.Context, uuid.UUID, string) error { return nil }
func (r *memUserRepo) List(_ context.Context) ([]instance.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]instance.User, 0, len(r.byID))
	for _, u := range r.byID {
		out = append(out, *u)
	}
	return out, nil
}
func (r *memUserRepo) UpdateRole(_ context.Context, id uuid.UUID, role string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if u, ok := r.byID[id]; ok {
		u.Role = identity.Role(role)
	}
	return nil
}
func (r *memUserRepo) CountAdmins(_ context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, u := range r.byID {
		if u.Role == identity.RoleAdmin {
			n++
		}
	}
	return n, nil
}
func (r *memUserRepo) ListDepartments(_ context.Context, userID uuid.UUID) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.depts[userID]...), nil
}
func (r *memUserRepo) ReplaceDepartments(_ context.Context, userID uuid.UUID, departments []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.depts == nil {
		r.depts = map[uuid.UUID][]string{}
	}
	r.depts[userID] = append([]string(nil), departments...)
	return nil
}

func userToolServer(repo *memUserRepo, bootstrap ...string) *Server {
	svc := instance.NewUserService(repo, instance.NewProvisioner(repo, bootstrap, identity.GroupConfig{DefaultGroup: "default"}))
	return NewServerWithDeps("test", "0.0.0", &Deps{Users: svc})
}

func adminToolCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
}
func userToolCtx() context.Context {
	return identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleUser})
}

func sp(s string) *string { return &s }

// Happy path: admin promotes; admin demotes when another admin remains;
// user_list returns rows without IdP identifiers.
func TestUserTools_HappyPath(t *testing.T) {
	repo := newMemUserRepo()
	srv := userToolServer(repo)
	target := uuid.New()
	repo.byID[target] = &instance.User{ID: target, Subject: sp("sub-secret-mcp"), Email: sp("mcp@example.com"), Username: "u", Role: identity.RoleUser}

	// Promote user -> admin.
	res, err := userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"id": target.String(), "role": "admin"}))
	require.NoError(t, err)
	require.False(t, res.IsError, toolText(t, res))
	assert.Equal(t, identity.RoleAdmin, repo.byID[target].Role)

	// Successful demotion: seed a second admin so the last-admin floor does not fire.
	other := uuid.New()
	repo.byID[other] = &instance.User{ID: other, Subject: sp("other-sub"), Username: "other", Role: identity.RoleAdmin}
	demoteRes, err := userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"id": target.String(), "role": "user"}))
	require.NoError(t, err)
	require.False(t, demoteRes.IsError, "demotion must succeed when another admin remains: "+toolText(t, demoteRes))
	assert.Equal(t, identity.RoleUser, repo.byID[target].Role)

	listRes, err := userListHandler(srv)(adminToolCtx(), callReq(nil))
	require.NoError(t, err)
	require.False(t, listRes.IsError)
	listBody := toolText(t, listRes)
	assert.Contains(t, listBody, "\"u\"")
	assert.NotContains(t, listBody, "sub-secret-mcp", "user_list must not leak IdP subject value")
	assert.NotContains(t, listBody, "mcp@example.com", "user_list must not leak IdP email value")
	assert.NotContains(t, listBody, "subject", "user_list must not leak 'subject' field name")
	assert.NotContains(t, listBody, "email", "user_list must not leak 'email' field name")
}

// Input validation: missing id/role, bad role, invalid UUID.
func TestUserTools_Validation(t *testing.T) {
	repo := newMemUserRepo()
	srv := userToolServer(repo)
	target := uuid.New()
	repo.byID[target] = &instance.User{ID: target, Role: identity.RoleUser}

	res, _ := userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"role": "admin"}))
	assert.True(t, res.IsError)

	res, _ = userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"id": target.String()}))
	assert.True(t, res.IsError)

	res, _ = userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"id": target.String(), "role": "superuser"}))
	require.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "role must be")

	// Invalid UUID string — uuid.Parse fails before any repo access.
	res, _ = userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"id": "not-a-uuid", "role": "admin"}))
	require.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "invalid id")
}

// Error/failure: unknown id -> "user not found".
func TestUserTools_NotFound(t *testing.T) {
	srv := userToolServer(newMemUserRepo())
	res, _ := userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"id": uuid.New().String(), "role": "admin"}))
	require.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "user not found")
}

// Boundary: last-admin and bootstrap-admin demotion -> respective tool errors.
func TestUserTools_DemotionFloors(t *testing.T) {
	// Last admin.
	repo := newMemUserRepo()
	srv := userToolServer(repo)
	admin := uuid.New()
	repo.byID[admin] = &instance.User{ID: admin, Subject: sp("a"), Role: identity.RoleAdmin}
	res, _ := userSetRoleHandler(srv)(adminToolCtx(), callReq(map[string]any{"id": admin.String(), "role": "user"}))
	require.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "last admin")

	// Bootstrap admin (another admin present).
	repo2 := newMemUserRepo()
	srv2 := userToolServer(repo2, "boss-sub")
	boss := uuid.New()
	other := uuid.New()
	repo2.byID[boss] = &instance.User{ID: boss, Subject: sp("boss-sub"), Role: identity.RoleAdmin}
	repo2.byID[other] = &instance.User{ID: other, Subject: sp("other"), Role: identity.RoleAdmin}
	res2, _ := userSetRoleHandler(srv2)(adminToolCtx(), callReq(map[string]any{"id": boss.String(), "role": "user"}))
	require.True(t, res2.IsError)
	assert.Contains(t, toolText(t, res2), "bootstrap admin")
}

// Isolation/security: non-admin -> forbidden; unauthenticated -> authentication
// required; nil dep -> unavailable.
func TestUserTools_Security(t *testing.T) {
	repo := newMemUserRepo()
	srv := userToolServer(repo)
	target := uuid.New()
	repo.byID[target] = &instance.User{ID: target, Role: identity.RoleUser}

	res, _ := userSetRoleHandler(srv)(userToolCtx(), callReq(map[string]any{"id": target.String(), "role": "admin"}))
	require.True(t, res.IsError)
	assert.Contains(t, toolText(t, res), "forbidden: admin only")

	listRes, _ := userListHandler(srv)(userToolCtx(), callReq(nil))
	require.True(t, listRes.IsError)
	assert.Contains(t, toolText(t, listRes), "forbidden: admin only")

	// Unauthenticated (no identity in context) — admin gate must reject before
	// any repo access, returning "authentication required" on both tools.
	noIDCtx := context.Background()
	unauthedSet, _ := userSetRoleHandler(srv)(noIDCtx, callReq(map[string]any{"id": target.String(), "role": "admin"}))
	require.True(t, unauthedSet.IsError)
	assert.Contains(t, toolText(t, unauthedSet), "authentication required", "user_set_role must reject unauthenticated callers")

	unauthedList, _ := userListHandler(srv)(noIDCtx, callReq(nil))
	require.True(t, unauthedList.IsError)
	assert.Contains(t, toolText(t, unauthedList), "authentication required", "user_list must reject unauthenticated callers")

	// nil dep.
	nilSrv := NewServerWithDeps("test", "0.0.0", &Deps{})
	nilRes, _ := userListHandler(nilSrv)(context.Background(), callReq(nil))
	require.True(t, nilRes.IsError)
	assert.Contains(t, toolText(t, nilRes), "user service not available")
}

var _ = strings.TrimSpace
