# RBAC Phase 1 — Task 6: `GET /api/v1/me` Whoami Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `GET /api/v1/me` endpoint that returns the authenticated caller's identity projection — `{ userId, username, role, departments, ledDepartments, isAdmin }` — read straight from `identity.FromContext`. It serves both org and local mode, behind the existing auth middleware, and never leaks subject/email.

**Architecture:** A new tiny handler `adapter/http/me_handler.go` that reads `identity.FromContext`, maps it to a JSON DTO, and writes it. Wired into the authenticated `/api/v1` group in `server.go`, gated like other auth-aware routes. No new service, no DB read — the middleware already resolved and enriched the identity (Task 5).

**Tech Stack:** Go, chi routing, `identity.FromContext`, the existing `WriteJSON` helper.

> The `/me` route lives INSIDE the `/api/v1` group, which is wrapped by `d.Auth` (the auth middleware) when auth is enabled. In local mode the middleware injects the default-group admin identity, so `/me` returns that. With auth disabled (nil middleware, legacy/tests), there is no identity in context — the handler must then fail gracefully (401), never panic. Use `identity.FromContext` (the `(Identity, bool)` form), NOT `MustFromContext`, so a missing identity is a clean 401 rather than a panic.

---

### Task 6: me handler + route

**Files:**
- Create: `adapter/http/me_handler.go`
- Modify: `adapter/http/server.go` (mount `GET /me` inside `/api/v1`)
- Test: `adapter/http/me_handler_test.go`

**Dependencies:** Task 5 (`identity.Identity.Departments`, `LedDepartments`, `IsAdmin`). Do after Task 5. No dependency on Tasks 2/3/4 at compile time, but the endpoint is only meaningful once the middleware populates the enriched identity (Task 5).

- [ ] **Step 1: Write the failing handler test**

Create `adapter/http/me_handler_test.go`:

```go
package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

func TestMeHandler_ReturnsProjection(t *testing.T) {
	uid := uuid.New()
	id := identity.Identity{
		UserID:         uid,
		Username:       "alice",
		Role:           identity.RoleUser,
		Departments:    []string{"engineering", "platform"},
		LedDepartments: []string{"platform"},
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))

	NewMeHandler().Me(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		UserID         string   `json:"userId"`
		Username       string   `json:"username"`
		Role           string   `json:"role"`
		Departments    []string `json:"departments"`
		LedDepartments []string `json:"ledDepartments"`
		IsAdmin        bool     `json:"isAdmin"`
		Subject        string   `json:"subject"` // must remain empty (never serialized)
		Email          string   `json:"email"`   // must remain empty (never serialized)
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID != uid.String() {
		t.Errorf("userId = %q, want %q", got.UserID, uid.String())
	}
	if got.Username != "alice" {
		t.Errorf("username = %q, want alice", got.Username)
	}
	if got.Role != "user" {
		t.Errorf("role = %q, want user", got.Role)
	}
	if !reflect.DeepEqual(got.Departments, []string{"engineering", "platform"}) {
		t.Errorf("departments = %v, want [engineering platform]", got.Departments)
	}
	if !reflect.DeepEqual(got.LedDepartments, []string{"platform"}) {
		t.Errorf("ledDepartments = %v, want [platform]", got.LedDepartments)
	}
	if got.IsAdmin {
		t.Error("isAdmin = true, want false for a user role")
	}
	if got.Subject != "" || got.Email != "" {
		t.Errorf("leaked subject/email: subject=%q email=%q", got.Subject, got.Email)
	}
}

func TestMeHandler_AdminFlag(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin, Departments: []string{"default"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))

	NewMeHandler().Me(rec, req)

	var got struct {
		IsAdmin bool `json:"isAdmin"`
		Role    string `json:"role"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&got)
	if !got.IsAdmin || got.Role != "admin" {
		t.Errorf("isAdmin=%v role=%q, want true/admin", got.IsAdmin, got.Role)
	}
}

func TestMeHandler_EmptyDepartmentsSerializeAsEmptyArray(t *testing.T) {
	// departments/ledDepartments must serialize as [] (not null) so clients can
	// rely on an array shape.
	id := identity.Identity{UserID: uuid.New(), Username: "x", Role: identity.RoleUser}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req = req.WithContext(identity.WithIdentity(req.Context(), id))

	NewMeHandler().Me(rec, req)

	body := rec.Body.String()
	if !contains(body, `"departments":[]`) || !contains(body, `"ledDepartments":[]`) {
		t.Errorf("empty arrays not serialized as []: %s", body)
	}
}

func TestMeHandler_NoIdentityReturns401(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil) // no identity in context
	NewMeHandler().Me(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 when no identity", rec.Code)
	}
}

// contains is a tiny local helper (avoid importing strings just for this).
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

> If a `contains` helper already exists in the `http` test package, drop the local one to avoid a redeclaration. Check: `grep -rn "func contains(" adapter/http/*_test.go`.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./adapter/http/ -run TestMeHandler -v`
Expected: FAIL — `undefined: NewMeHandler`.

- [ ] **Step 3: Implement the handler**

Create `adapter/http/me_handler.go`:

```go
package http

import (
	"net/http"

	"github.com/arbitron/arbitron/domain/identity"
)

// MeHandler serves GET /api/v1/me, the whoami endpoint. It reads the caller's
// already-resolved identity from context (the auth middleware populated it) and
// returns the projection that drives client gating and the later dashboard
// department selector. It performs no DB read.
type MeHandler struct{}

// NewMeHandler builds the stateless me handler.
func NewMeHandler() *MeHandler { return &MeHandler{} }

// meView is the read DTO. SECURITY: it exposes ONLY non-secret projection fields
// — never the IdP subject or email (those live on the persisted User and never
// reach the Identity, but this DTO is the serialization boundary, so it is
// documented here as the place that must stay free of identifiers).
type meView struct {
	UserID         string   `json:"userId"`
	Username       string   `json:"username"`
	Role           string   `json:"role"`
	Departments    []string `json:"departments"`
	LedDepartments []string `json:"ledDepartments"`
	IsAdmin        bool     `json:"isAdmin"`
}

// Me handles GET /me. A missing identity (auth disabled, or a route reached
// outside the middleware) is a clean 401, never a panic — so this uses the
// (Identity, bool) FromContext form rather than MustFromContext.
func (h *MeHandler) Me(w http.ResponseWriter, r *http.Request) {
	id, ok := identity.FromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	// Normalize nil slices to empty so the JSON shape is always an array.
	depts := id.Departments
	if depts == nil {
		depts = []string{}
	}
	led := id.LedDepartments
	if led == nil {
		led = []string{}
	}
	WriteJSON(w, http.StatusOK, meView{
		UserID:         id.UserID.String(),
		Username:       id.Username,
		Role:           string(id.Role),
		Departments:    depts,
		LedDepartments: led,
		IsAdmin:        id.IsAdmin(),
	})
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./adapter/http/ -run TestMeHandler -v`
Expected: PASS (all four cases).

- [ ] **Step 5: Mount the route in `server.go`**

In `adapter/http/server.go`, inside `NewRouterFromDeps`, within the `r.Route("/api/v1", func(r chi.Router) { ... })` block, add the `/me` route alongside the other top-level authenticated routes (e.g. just after the existing `r.Get("/health", ...)` registration so it sits at the group root):

```go
		// Whoami: returns the caller's identity projection (RBAC Phase 1). Behind
		// the auth middleware (when enabled); in local mode it returns the
		// default-group admin. No secrets (subject/email) are serialized.
		meHandler := NewMeHandler()
		r.Get("/me", meHandler.Me)
```

> The handler is stateless and needs no `RouterDeps` field, so it can be constructed inline. There is nothing to add to `RouterDeps`.

- [ ] **Step 6: Write a routing-level test (optional but recommended) confirming the route is mounted and authed**

Append to `adapter/http/me_handler_test.go`:

```go
func TestMeRoute_MountedUnderAPIV1(t *testing.T) {
	// Build a minimal router with auth enabled (local mode) and hit /api/v1/me.
	// Reuse the server test helper that constructs RouterDeps. If your suite has
	// a helper like newTestRouter(t, deps), use it; otherwise this asserts the
	// route exists by checking it is not a 404.
	deps := minimalRouterDeps(t) // existing/server-test helper; see note
	r := NewRouterFromDeps(deps)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.ServeHTTP(rec, req)

	// In local mode with auth wired, /me returns 200 with the default admin.
	// Without auth wired (deps.Auth == nil) the handler still resolves via
	// FromContext and returns 401 — either way it is NOT a 404 (route exists).
	if rec.Code == http.StatusNotFound {
		t.Fatalf("/api/v1/me returned 404 — route not mounted")
	}
}
```

> If the server-test suite has no `minimalRouterDeps`/`newTestRouter` helper, SKIP this step (the handler unit tests in Steps 1–4 already prove behavior). Check: `grep -rn "NewRouterFromDeps(" adapter/http/server_test.go`. Reuse whatever minimal-deps pattern `server_test.go` already establishes; do not invent new infra.

- [ ] **Step 7: Run the full http package test + vet**

Run: `go test ./adapter/http/ -v && go vet ./adapter/http/`
Expected: PASS, clean.

- [ ] **Step 8: Build everything**

Run: `go build ./...`
Expected: success.

- [ ] **Step 9: Commit**

```bash
git add adapter/http/me_handler.go adapter/http/me_handler_test.go adapter/http/server.go
git commit -m "feat(rbac): GET /api/v1/me whoami endpoint

Returns { userId, username, role, departments, ledDepartments, isAdmin } from the
enriched context identity. Empty department sets serialize as []; subject/email
are never serialized; missing identity is a clean 401.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Test Scenario Coverage (this task)

| Category | Scenario | Step |
|----------|----------|------|
| Happy | Returns full projection for an org user | 1 |
| Happy | Admin flag true for admin role | 1 |
| Edge | Empty departments serialize as `[]` not `null` | 1 |
| Edge | Route mounted under `/api/v1` (not 404) | 6 |
| Error | No identity in context → 401 (no panic) | 1 |
| Authz | subject/email never serialized (leak guard) | 1 |
| Mode | Local mode returns default-group admin via middleware | 6 (route test), Task 5 |
