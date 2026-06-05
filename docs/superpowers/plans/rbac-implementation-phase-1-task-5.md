# RBAC Phase 1 — Task 5: Identity Enrichment & Middleware Population Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `Departments` and `LedDepartments` to `identity.Identity` with helper predicates, and populate them in the auth middleware: in org mode from the stored user departments unioned with config-derived led departments; in local mode with a fixed `[defaultGroup]` admin identity. This makes a caller's departments/lead status context-resident for all later phases.

**Architecture:** Extend `identity.Identity` with two `[]string` fields and predicates `InDepartment`/`LeadsDepartment`. Give `AuthMiddleware` a `GroupConfig` dependency and the ability to read stored departments (it already holds `users instance.UserRepository`, which now has `ListDepartments` from Task 3). In each org-mode resolution branch, after loading the persisted `User`, load `ListDepartments`, compute `LedDepartments` from the user's subject+email, union them for `Departments`, and stamp the role. Local mode stamps `Departments=[defaultGroup]`, `LedDepartments=nil`, `Role=admin`.

**Tech Stack:** Go, chi middleware, `domain/identity` helpers (Task 1), `UserRepository.ListDepartments` (Task 3).

> **Subject/email for lead matching.** `LedDepartments(subject, email, cfg)` needs the user's subject and email. The middleware currently builds `identity.Identity{UserID, Username, Role}` from `*instance.User`, which carries `Subject *string` and `Email *string`. Dereference them (`""` when nil) for the match. These are used only for the in-memory match and are never placed on the `Identity` or serialized.

---

### Task 5: identity fields/predicates + middleware population

**Files:**
- Modify: `domain/identity/identity.go` (add fields + predicates)
- Modify: `adapter/http/auth_middleware.go` (GroupConfig dep; populate both branches)
- Modify: `cmd/arbitron/main.go` (pass `groupCfg` to `NewAuthMiddleware`)
- Test: `domain/identity/identity_test.go` (add predicate tests; file may already exist)
- Test: `adapter/http/auth_middleware_test.go` (add population tests; file exists)

**Dependencies:** Task 1 (`GroupConfig`, `LedDepartments`, `DefaultGroupOr`), Task 3 (`UserRepository.ListDepartments`). Best done after Task 4 so live-role + snapshot are in place, but compiles independently of Task 4.

- [ ] **Step 1: Add fields and predicates to `identity.Identity`**

In `domain/identity/identity.go`, extend the `Identity` struct:

```go
type Identity struct {
	UserID   uuid.UUID
	Username string
	Role     Role
	// Departments is the caller's effective membership: persisted (claim-derived)
	// departments unioned with config-derived led departments, all normalized.
	// In local mode this is the single default group. Later phases authorize
	// department-scoped reads/writes against this set.
	Departments []string
	// LedDepartments is the subset of departments the caller leads (config-derived,
	// not persisted). Empty in local mode. A lead may intervene on any instance in
	// these departments and publish their workflows to public.
	LedDepartments []string
}
```

- [ ] **Step 2: Write the failing predicate tests**

Append to `domain/identity/identity_test.go` (create with `package identity` if absent):

```go
func TestInDepartment(t *testing.T) {
	id := Identity{Departments: []string{"engineering", "platform"}}
	if !id.InDepartment("engineering") {
		t.Error("InDepartment(engineering) = false, want true")
	}
	if id.InDepartment("sales") {
		t.Error("InDepartment(sales) = true, want false")
	}
	// Match is on the normalized value; callers pass normalized departments.
	if id.InDepartment("Engineering") {
		t.Error("InDepartment(Engineering) matched un-normalized; want exact-normalized match only")
	}
}

func TestLeadsDepartment(t *testing.T) {
	id := Identity{LedDepartments: []string{"platform"}}
	if !id.LeadsDepartment("platform") {
		t.Error("LeadsDepartment(platform) = false, want true")
	}
	if id.LeadsDepartment("engineering") {
		t.Error("LeadsDepartment(engineering) = true, want false")
	}
}
```

- [ ] **Step 3: Run to verify it fails**

Run: `go test ./domain/identity/ -run "TestInDepartment|TestLeadsDepartment" -v`
Expected: FAIL — `id.InDepartment undefined`.

- [ ] **Step 4: Add the predicates**

In `domain/identity/identity.go`, after `IsAdmin`:

```go
// InDepartment reports whether the caller is a member of dept. dept must be a
// normalized (trimmed, lower-cased) department name; the membership set is
// already normalized.
func (i Identity) InDepartment(dept string) bool {
	for _, d := range i.Departments {
		if d == dept {
			return true
		}
	}
	return false
}

// LeadsDepartment reports whether the caller is a configured lead of dept. dept
// must be a normalized department name.
func (i Identity) LeadsDepartment(dept string) bool {
	for _, d := range i.LedDepartments {
		if d == dept {
			return true
		}
	}
	return false
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./domain/identity/ -run "TestInDepartment|TestLeadsDepartment" -v`
Expected: PASS.

- [ ] **Step 6: Give `AuthMiddleware` a `GroupConfig` and an enrichment helper**

In `adapter/http/auth_middleware.go`:

(a) Add a field to the `AuthMiddleware` struct (after `users`):

```go
	groups   identity.GroupConfig // RBAC: default group + leads mapping
```

(b) Change `NewAuthMiddleware` to accept and store it. New signature:

```go
func NewAuthMiddleware(mode identity.Mode, cookieName string, sessions identity.SessionRepository, tokens identity.TokenRepository, users instance.UserRepository, groups identity.GroupConfig) *AuthMiddleware {
	m := &AuthMiddleware{mode: mode, cookie: cookieName, sessions: sessions, tokens: tokens, users: users, groups: groups}
	if mode == identity.ModeOrg && tokens != nil {
		m.toucher = newTouchFlusher(tokens, time.NewTicker(touchFlushInterval).C)
	}
	return m
}
```

(c) Add an enrichment helper that builds the org-mode identity from a persisted user. Add near the bottom of the file:

```go
// enrich builds the request identity for an org-mode user: it loads the user's
// persisted departments, unions them with the config-derived led departments,
// and stamps the (already live-evaluated) role. Subject/email drive lead
// matching only — they are never placed on the Identity. A ListDepartments
// failure is non-fatal: the caller still authenticates with their led
// departments (plus, defensively, nothing else), so a transient read error does
// not lock a user out — but it is logged.
func (m *AuthMiddleware) enrich(ctx context.Context, u *instance.User) identity.Identity {
	subject := ""
	if u.Subject != nil {
		subject = *u.Subject
	}
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	stored, err := m.users.ListDepartments(ctx, u.ID)
	if err != nil {
		slog.Error("auth: could not load user departments", "user", u.ID, "error", err)
		stored = nil
	}
	led := identity.LedDepartments(subject, email, m.groups)
	departments := unionDepts(stored, led)
	if len(departments) == 0 {
		// Defensive: a user with no stored departments and no led departments
		// still belongs to the default group (matches the provisioner snapshot).
		departments = []string{m.groups.DefaultGroupOr()}
	}
	return identity.Identity{
		UserID:         u.ID,
		Username:       u.Username,
		Role:           u.Role,
		Departments:    departments,
		LedDepartments: led,
	}
}

// unionDepts returns stored ∪ led, preserving stored order then appending any
// led departments not already present.
func unionDepts(stored, led []string) []string {
	seen := make(map[string]struct{}, len(stored)+len(led))
	out := make([]string, 0, len(stored)+len(led))
	for _, d := range stored {
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	for _, d := range led {
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
```

Add `"context"` to the import block of `adapter/http/auth_middleware.go` (it is not currently imported; `slog` already is).

- [ ] **Step 7: Use `enrich` in both org-mode resolution branches**

In `adapter/http/auth_middleware.go`, in `resolve`:

(a) Session-cookie branch — replace:

```go
				if user, uerr := m.users.GetByID(r.Context(), sess.UserID); uerr == nil {
					return identity.Identity{UserID: user.ID, Username: user.Username, Role: user.Role}, nil
				}
```

with:

```go
				if user, uerr := m.users.GetByID(r.Context(), sess.UserID); uerr == nil {
					return m.enrich(r.Context(), user), nil
				}
```

(b) Bearer-token branch — replace:

```go
					return identity.Identity{UserID: user.ID, Username: user.Username, Role: user.Role}, nil
```

with:

```go
					return m.enrich(r.Context(), user), nil
```

- [ ] **Step 8: Populate the local-mode identity with the default group**

In `adapter/http/auth_middleware.go`, in the local-mode branch of `resolve`, replace:

```go
			m.localID = identity.Identity{UserID: uid, Username: "local", Role: identity.RoleAdmin}
```

with:

```go
			// Local mode: the single user is admin in the default group, so all
			// RBAC predicates pass trivially and today's behavior is preserved.
			m.localID = identity.Identity{
				UserID:      uid,
				Username:    "local",
				Role:        identity.RoleAdmin,
				Departments: []string{m.groups.DefaultGroupOr()},
			}
```

- [ ] **Step 9a: Extend the existing `fakeUserRepo` with the Task-3 port methods**

The existing `fakeUserRepo` in `adapter/http/auth_middleware_test.go` (fields: `defaultID uuid.UUID`, `byID map[uuid.UUID]*instance.User`) does NOT yet implement `ListDepartments`/`ReplaceDepartments`, so after Task 3 the test package will not compile. Add a `depts` map and the two methods. In `adapter/http/auth_middleware_test.go`, change the struct/constructor and add methods:

```go
// add field to the fakeUserRepo struct definition:
//   depts map[uuid.UUID][]string

// in newFakeUserRepo(), initialize it:
//   return &fakeUserRepo{defaultID: uuid.New(), byID: map[uuid.UUID]*instance.User{}, depts: map[uuid.UUID][]string{}}

func (r *fakeUserRepo) ListDepartments(_ context.Context, userID uuid.UUID) ([]string, error) {
	return append([]string(nil), r.depts[userID]...), nil
}

func (r *fakeUserRepo) ReplaceDepartments(_ context.Context, userID uuid.UUID, departments []string) error {
	r.depts[userID] = append([]string(nil), departments...)
	return nil
}
```

- [ ] **Step 9b: Write failing middleware population tests**

Append to `adapter/http/auth_middleware_test.go`. The session fake is seeded via its `byToken` map (there is no `put` helper); the user fake's default-user field is `defaultID`. These tests assert the identity placed in context.

```go
func TestAuthMiddleware_OrgMode_PopulatesDepartmentsAndLed(t *testing.T) {
	// A session resolves to a user with stored departments [engineering]; config
	// makes this user a lead of [platform]. Effective Departments = union; Led = [platform].
	uid := uuid.New()
	subj := "alice-subject"
	email := "alice@corp.example"
	users := newFakeUserRepo()
	users.byID[uid] = &instance.User{ID: uid, Subject: &subj, Email: &email, Username: "alice", Role: identity.RoleUser}
	users.depts[uid] = []string{"engineering"}

	sessions := newFakeSessionRepo()
	sessions.byToken["cookie-token"] = &identity.Session{ID: "cookie-token", UserID: uid, ExpiresAt: time.Now().Add(time.Hour)}

	groups := identity.GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"alice@corp.example"}},
	}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", sessions, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "arbitron_session", Value: "cookie-token"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !reflect.DeepEqual(got.Departments, []string{"engineering", "platform"}) {
		t.Errorf("Departments = %v, want [engineering platform]", got.Departments)
	}
	if !reflect.DeepEqual(got.LedDepartments, []string{"platform"}) {
		t.Errorf("LedDepartments = %v, want [platform]", got.LedDepartments)
	}
}

func TestAuthMiddleware_LocalMode_DefaultGroupAdmin(t *testing.T) {
	users := newFakeUserRepo() // GetDefaultUser returns users.defaultID

	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewAuthMiddleware(identity.ModeLocal, "arbitron_session", nil, nil, users, groups)

	var got identity.Identity
	h := mw.Handler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = identity.FromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if got.Role != identity.RoleAdmin {
		t.Errorf("Role = %q, want admin", got.Role)
	}
	if !reflect.DeepEqual(got.Departments, []string{"default"}) {
		t.Errorf("Departments = %v, want [default]", got.Departments)
	}
	if len(got.LedDepartments) != 0 {
		t.Errorf("LedDepartments = %v, want empty in local mode", got.LedDepartments)
	}
}

func TestAuthMiddleware_OrgMode_NoCredentialStill401(t *testing.T) {
	users := newFakeUserRepo()
	groups := identity.GroupConfig{DefaultGroup: "default"}
	mw := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, users, groups)

	h := mw.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler should not run without a credential")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}
```

> Ensure the test file imports include `time` (for the session expiry), `httptest`, `net/http`, `reflect`, `uuid`, `identity`, `instance` — most are already present in this file.

- [ ] **Step 10: Run middleware tests to verify they fail (signature) then pass**

Run: `go test ./adapter/http/ -run TestAuthMiddleware -v`
Expected: first FAIL on `NewAuthMiddleware` arity (5 args vs 6) across the existing tests; after updating ALL existing `NewAuthMiddleware(...)` call sites in the test file to pass a `groups` arg (use `identity.GroupConfig{DefaultGroup: "default"}` for cases that don't care), the new tests PASS.

- [ ] **Step 11: Update the middleware wiring in `main.go`**

In `cmd/arbitron/main.go`, update the `NewAuthMiddleware` call (~line 303) to pass `groupCfg` (built in Task 4 Step 13a; if Task 4 is not yet merged, build it here with `adaptconfig.ToGroupConfig(cfg.Auth.RBAC)`):

```go
	authMW := adapthttp.NewAuthMiddleware(authMode, cfg.Auth.Session.CookieName, sessionRepo, tokenRepo, userRepo, groupCfg)
```

- [ ] **Step 12: Build; fix every remaining `NewAuthMiddleware` call site**

Run: `go build ./... 2>&1 | grep -i "NewAuthMiddleware\|not enough\|too many" || echo "build clean"`
Expected: `build clean`. Update any other test or wiring call site to the 6-arg signature, passing `identity.GroupConfig{DefaultGroup: "default"}` where the value is irrelevant.

- [ ] **Step 13: Full test + vet**

Run: `go test ./domain/identity/ ./adapter/http/ ./cmd/... && go vet ./domain/identity/ ./adapter/http/`
Expected: PASS, clean.

- [ ] **Step 14: Commit**

```bash
git add domain/identity/identity.go domain/identity/identity_test.go adapter/http/auth_middleware.go adapter/http/auth_middleware_test.go cmd/arbitron/main.go
git commit -m "feat(rbac): enrich identity with departments + led, populate in middleware

Identity gains Departments/LedDepartments + InDepartment/LeadsDepartment
predicates. Org-mode middleware unions stored departments with config-derived
led departments; local mode injects a default-group admin so RBAC is inert.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Test Scenario Coverage (this task)

| Category | Scenario | Step |
|----------|----------|------|
| Happy | Org session populates Departments (stored ∪ led) + LedDepartments | 9 |
| Happy | Predicates InDepartment/LeadsDepartment | 2 |
| Edge | Lead-of-D added to membership even if not stored | 9 |
| Edge | User with no stored depts + no led → default group | 6 (code path), covered defensively |
| Error | ListDepartments failure is non-fatal (logged, led still applied) | 6 (code path) |
| Authz | LedDepartments derived from subject/email, never serialized | 6, 9 |
| Mode | Local mode → default-group admin, no led, role admin | 9 |
| Mode | Org mode missing credential → 401 unchanged | 9 |
