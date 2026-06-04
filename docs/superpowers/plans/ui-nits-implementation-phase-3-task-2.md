# UI Nits Phase 3 — Task 2: Dismiss / Restore / Dismiss-All HTTP handlers + acting-user seam

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. STRICT TDD: failing test → run (expect FAIL) → minimal impl → run (expect PASS) → commit. **Depends on Task 1** (the `DismissalRepository` port + adapter must exist and compile).

**Goal:** Expose per-user dismissal mutations over chi — `POST /instances/{id}/dismiss`, `POST /instances/{id}/restore`, `POST /instances/dismiss-all?state=failed|completed` — all routed through a `currentUser(r)` acting-user seam, with guards (dismiss non-terminal → 409, bad `state` → 400).

---

### Task 2: Mutation handlers + `currentUser` seam + handler tests

**Context for the implementer (read before starting):**

- Handlers live in `package http` (production) and are tested from `package http_test` (see `adapter/http/instance_handler_test.go`). `WriteJSON(w, status, v)` and `WriteError(w, status, code, message)` are the response helpers. Existing instance routes are registered in `adapter/http/server.go` inside `r.Route("/instances", ...)`.
- The acting-user seam: today every request is the seeded `local` user resolved via `instance.UserRepository.GetDefaultUser(ctx)` (returns `uuid.UUID`). The seam must depend only on the request so Phase 6 can swap its body for the authenticated user from request context. `server.go` already carries a `// TODO(phase-6)` for auth middleware.
- `RouterDeps` (in `server.go`) is the wiring struct; `NewInstanceHandler(instances, nodeExecs, snapshots, service)` is the current constructor. We will add the dismissal repo + user repo to the handler.
- The dismiss guard needs the instance's state: read it via `h.instances.Get(ctx, id)` and check `inst.State`. Terminal states are `instance.StateCompleted` and `instance.StateFailed` (see `domain/instance/instance.go` state constants). `mapEngineError`/`errors.Is(err, instance.ErrNotFound)` already exist for not-found mapping.
- Dismiss-All resolves the IDs currently visible to the user in a category, then dismisses each. Reuse `h.instances.List(ctx, instance.ListFilters{State: &state, Since: ..., Limit: ...})`. To match what `QueueView.tsx` shows for Failed/Completed (a 5-day window), accept an optional `since` query param; if absent, dismiss all listed in that state up to the default limit. Keep it simple: list with the same filters the frontend uses, dismiss each returned ID.
- Dismissal mutations emit **no** global WebSocket event (per-user view state, not instance state) — the frontend refetches its own list. Do not publish to the event bus from these handlers.
- Test patterns: `adapter/http/instance_handler_test.go` defines `mockInstanceRepo` (a `map[uuid.UUID]*instance.Instance` with `List` honoring `State`/`Since`/limit/offset) and an `errInstanceRepo`. Add a `mockDismissalRepo` and `mockUserRepo` in the test file. Use `httptest.NewServer(adapthttp.NewRouterFromDeps(...))` or call the handler method directly via a `chi` route — follow whichever the existing instance handler tests use (grep the file for `NewRouterFromDeps` vs direct handler calls and match it).

**Files:**
- `adapter/http/current_user.go` (new — the `currentUser` seam)
- `adapter/http/instance_handler.go` (modify: add deps to `InstanceHandler` + constructor; add `Dismiss`, `Restore`, `DismissAll` methods)
- `adapter/http/server.go` (modify: `RouterDeps` gets `Dismissals`; pass to `NewInstanceHandler`; register routes — lines ~197-214 inside `r.Route("/instances", ...)`)
- `adapter/http/dismissal_handler_test.go` (new — handler tests + mocks)

---

#### Step 2.1 — Failing test: `currentUser(r)` returns the default user

- [ ] Create `adapter/http/dismissal_handler_test.go` (package `http_test`) with a mock user repo and a test that the seam resolves the default user. Since `currentUser` is unexported, test it indirectly through a handler in later steps; for now write a focused test against the exported seam helper by giving it an exported test entrypoint OR test via the handler. **Decision:** make the seam a method on a small exported struct so it is testable and injectable:

Test (initially failing because the type does not exist):

```go
package http_test

import (
	"context"
	"net/http/httptest"
	"testing"

	adapthttp "github.com/arbitron/arbitron/adapter/http"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockUserRepo implements instance.UserRepository.
type mockUserRepo struct{ id uuid.UUID }

func (m *mockUserRepo) GetDefaultUser(_ context.Context) (uuid.UUID, error) { return m.id, nil }

func TestCurrentUser_ResolvesDefaultUser(t *testing.T) {
	want := uuid.New()
	resolver := adapthttp.NewCurrentUserResolver(&mockUserRepo{id: want})
	r := httptest.NewRequest("GET", "/", nil)

	got, err := resolver.CurrentUser(r)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}
```

- [ ] Run: `go test ./adapter/http/ -run TestCurrentUser_ResolvesDefaultUser`
- [ ] Expected: **FAIL (build)** — `undefined: adapthttp.NewCurrentUserResolver`.

#### Step 2.2 — Minimal impl: the acting-user seam

- [ ] Create `adapter/http/current_user.go`:

```go
package http

import (
	"net/http"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// CurrentUserResolver resolves the acting user for a request. Today it always
// returns the seeded "local" user via UserRepository.GetDefaultUser, ignoring
// the request entirely.
//
// TODO(phase-6): replace the body of CurrentUser to read the authenticated user
// from the request context (set by the auth middleware noted in server.go). All
// per-user dismissal endpoints depend only on this seam, so swapping it makes
// the per-user semantics correct for many users without touching handlers.
type CurrentUserResolver struct {
	users instance.UserRepository
}

// NewCurrentUserResolver builds a resolver over the user repository.
func NewCurrentUserResolver(users instance.UserRepository) *CurrentUserResolver {
	return &CurrentUserResolver{users: users}
}

// CurrentUser returns the acting user's ID for this request.
func (c *CurrentUserResolver) CurrentUser(r *http.Request) (uuid.UUID, error) {
	return c.users.GetDefaultUser(r.Context())
}
```

- [ ] Run: `go test ./adapter/http/ -run TestCurrentUser_ResolvesDefaultUser`
- [ ] Expected: **PASS**.
- [ ] Commit: `feat(dismissal): currentUser acting-user seam [ui-nits p3 t2]`

#### Step 2.3 — Failing test: `POST /instances/{id}/dismiss` happy path (terminal → 200, row recorded)

- [ ] Add to `dismissal_handler_test.go` a `mockDismissalRepo` and a router-construction helper, then the happy-path test. The dismissal repo mock:

```go
import (
	"github.com/arbitron/arbitron/domain/instance"
)

type mockDismissalRepo struct {
	// dismissed[userID][instanceID] = true
	dismissed map[uuid.UUID]map[uuid.UUID]bool
}

func newMockDismissalRepo() *mockDismissalRepo {
	return &mockDismissalRepo{dismissed: map[uuid.UUID]map[uuid.UUID]bool{}}
}
func (m *mockDismissalRepo) Dismiss(_ context.Context, u, i uuid.UUID) error {
	if m.dismissed[u] == nil {
		m.dismissed[u] = map[uuid.UUID]bool{}
	}
	m.dismissed[u][i] = true
	return nil
}
func (m *mockDismissalRepo) Restore(_ context.Context, u, i uuid.UUID) error {
	if m.dismissed[u] != nil {
		delete(m.dismissed[u], i)
	}
	return nil
}
func (m *mockDismissalRepo) DismissedIDs(_ context.Context, u uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for i := range m.dismissed[u] {
		out[i] = true
	}
	return out, nil
}
func (m *mockDismissalRepo) ClearForInstance(_ context.Context, i uuid.UUID) error {
	for u := range m.dismissed {
		delete(m.dismissed[u], i)
	}
	return nil
}

var _ instance.DismissalRepository = (*mockDismissalRepo)(nil)
```

- [ ] Build a test server. Reuse `newMockInstanceRepo()` from `instance_handler_test.go` (same `http_test` package, so it is accessible). Construct the router via `adapthttp.NewRouterFromDeps` with the minimum deps the instance routes need (Service can be a stub satisfying `EngineService`; see how `instance_handler_test.go`/`server_test.go` build a router and copy that). Add the new `Dismissals` dep and a `mockUserRepo`. Test:

```go
func TestDismiss_TerminalInstance_OK(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed,
	}))
	dis := newMockDismissalRepo()

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[user][id], "dismiss must record the per-user row")
}
```

Define helpers `newDismissalTestServer(...)` (wraps `NewRouterFromDeps` + `httptest.NewServer`) and `doPOST` near the top of the file. Model `newDismissalTestServer` on whatever router-construction the existing instance handler tests already use.

- [ ] Run: `go test ./adapter/http/ -run TestDismiss_TerminalInstance_OK`
- [ ] Expected: **FAIL** — route not registered / handler/constructor field missing (build or 404).

#### Step 2.4 — Minimal impl: handler deps + `Dismiss` method + route

- [ ] In `instance_handler.go`, extend `InstanceHandler` and its constructor:

```go
type InstanceHandler struct {
	instances   instance.InstanceRepository
	nodeExecs   instance.NodeExecutionRepository
	snapshots   instance.VariableSnapshotRepository
	service     InstanceService
	dismissals  instance.DismissalRepository
	currentUser *CurrentUserResolver
}

func NewInstanceHandler(
	instances instance.InstanceRepository,
	nodeExecs instance.NodeExecutionRepository,
	snapshots instance.VariableSnapshotRepository,
	service InstanceService,
	dismissals instance.DismissalRepository,
	currentUser *CurrentUserResolver,
) *InstanceHandler {
	return &InstanceHandler{
		instances:   instances,
		nodeExecs:   nodeExecs,
		snapshots:   snapshots,
		service:     service,
		dismissals:  dismissals,
		currentUser: currentUser,
	}
}
```

- [ ] Add the `Dismiss` handler in `instance_handler.go`. Terminal-only guard returns 409:

```go
// isTerminal reports whether a state is dismissible (completed or failed).
func isTerminalState(s instance.State) bool {
	return s == instance.StateCompleted || s == instance.StateFailed
}

// Dismiss handles POST /instances/{id}/dismiss. Per-user; terminal-only.
func (h *InstanceHandler) Dismiss(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}
	inst, err := h.instances.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, instance.ErrNotFound) {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "instance not found: "+id.String())
			return
		}
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to get instance")
		return
	}
	if !isTerminalState(inst.State) {
		WriteError(w, http.StatusConflict, "CONFLICT", "only completed or failed instances can be dismissed")
		return
	}
	user, err := h.currentUser.CurrentUser(r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	if err := h.dismissals.Dismiss(r.Context(), user, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to dismiss instance")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [ ] In `server.go`, add `Dismissals instance.DismissalRepository` to `RouterDeps`, build a resolver `currentUser := NewCurrentUserResolver(d.Users)` (see note below on `Users`), pass both to `NewInstanceHandler`, and register the route inside `r.Route("/instances", ...)`:

```go
r.Post("/{id}/dismiss", instHandler.Dismiss)
```

- [ ] `RouterDeps` has no user repo today. Add `Users instance.UserRepository` to `RouterDeps`. The deprecated `NewRouter(...)` shim does not need it (leave it constructing with `Users: nil` only if nothing calls dismissal through that path; safer: in `NewRouterFromDeps`, if `d.Users == nil`, the dismissal routes can still be registered but `currentUser` will nil-panic — so guard route registration: only register dismiss/restore/dismiss-all when `d.Dismissals != nil && d.Users != nil`, mirroring the existing `if d.NotifHandler != nil` guard). Document this guard with a comment.

- [ ] Run: `go test ./adapter/http/ -run TestDismiss_TerminalInstance_OK`
- [ ] Expected: **PASS**.
- [ ] Run `go build ./...` to surface any caller of `NewInstanceHandler` that now needs the two new args (only `server.go` and tests should call it; update them). Update `server_test.go`/`instance_handler_test.go` router builders to pass a dismissal repo + resolver (a `newMockDismissalRepo()` and `NewCurrentUserResolver(&mockUserRepo{...})`).
- [ ] Run: `go test ./adapter/http/...`
- [ ] Expected: **PASS** (all existing handler tests still green).
- [ ] Commit: `feat(dismissal): POST /instances/{id}/dismiss handler+route [ui-nits p3 t2]`

#### Step 2.5 — Failing test: dismiss guard (non-terminal → 409) and not-found (→ 404)

- [ ] Add tests:

```go
func TestDismiss_NonTerminal_409(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateRunning,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 409, resp.StatusCode)
}

func TestDismiss_NotFound_404(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+uuid.New().String()+"/dismiss")
	defer resp.Body.Close()
	assert.Equal(t, 404, resp.StatusCode)
}
```

- [ ] Run: `go test ./adapter/http/ -run TestDismiss_`
- [ ] Expected: **PASS** (the Step 2.4 impl already enforces both). If the running-state mock instance is omitted from `mockInstanceRepo.Get`, ensure `Create` stores `State`. Commit.
- [ ] Commit: `test(dismissal): dismiss guards (409 non-terminal, 404 missing) [ui-nits p3 t2]`

#### Step 2.6 — Restore endpoint (happy + no-op idempotent)

- [ ] Failing tests:

```go
func TestRestore_RemovesDismissal_OK(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted,
	}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(context.Background(), user, id))
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/restore")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.False(t, dis.dismissed[user][id])
}

func TestRestore_NonDismissed_OK_NoOp(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(context.Background(), &instance.Instance{
		ID: id, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted,
	}))
	srv := newDismissalTestServer(t, instRepo, newMockDismissalRepo(), &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/"+id.String()+"/restore")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode) // no-op success
}
```

- [ ] Run: `go test ./adapter/http/ -run TestRestore_` → **FAIL** (route/handler missing).
- [ ] Impl: add `Restore` to `instance_handler.go` (no terminal guard — restoring is always safe; still parse ID and resolve user) and register `r.Post("/{id}/restore", instHandler.Restore)`:

```go
// Restore handles POST /instances/{id}/restore. Per-user; idempotent no-op if
// not currently dismissed.
func (h *InstanceHandler) Restore(w http.ResponseWriter, r *http.Request) {
	id, err := parseInstanceID(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid instance ID")
		return
	}
	user, err := h.currentUser.CurrentUser(r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	if err := h.dismissals.Restore(r.Context(), user, id); err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to restore instance")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

- [ ] Run: `go test ./adapter/http/ -run TestRestore_` → **PASS**.
- [ ] Commit: `feat(dismissal): POST /instances/{id}/restore handler+route [ui-nits p3 t2]`

#### Step 2.7 — Dismiss-All endpoint (happy, bad state → 400, empty category no-op)

- [ ] Failing tests:

```go
func TestDismissAll_DismissesEveryListedInCategory(t *testing.T) {
	user := uuid.New()
	instRepo := newMockInstanceRepo()
	ctx := context.Background()
	a := uuid.New(); b := uuid.New(); other := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: a, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: b, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: other, OwnerID: user, WorkflowName: "wf", State: instance.StateCompleted}))
	dis := newMockDismissalRepo()
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=failed")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
	assert.True(t, dis.dismissed[user][a])
	assert.True(t, dis.dismissed[user][b])
	assert.False(t, dis.dismissed[user][other], "completed item must not be dismissed by state=failed")
}

func TestDismissAll_BadState_400(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()
	for _, s := range []string{"", "running", "queued", "bogus"} {
		resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state="+s)
		assert.Equal(t, 400, resp.StatusCode, "state=%q must be rejected", s)
		resp.Body.Close()
	}
}

func TestDismissAll_EmptyCategory_OK(t *testing.T) {
	srv := newDismissalTestServer(t, newMockInstanceRepo(), newMockDismissalRepo(), &mockUserRepo{id: uuid.New()})
	defer srv.Close()
	resp := doPOST(t, srv.URL+"/api/v1/instances/dismiss-all?state=completed")
	defer resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode) // no-op success on empty category
}
```

- [ ] Run: `go test ./adapter/http/ -run TestDismissAll_` → **FAIL**.
- [ ] Impl: add `DismissAll` and register the route **before** the `{id}` routes so `dismiss-all` is not captured as an `{id}` (chi matches static segments before wildcards, but register it explicitly and order it first to be safe):

```go
// DismissAll handles POST /instances/dismiss-all?state=failed|completed. It
// dismisses every instance currently listed in that category for the acting
// user (the same window the dashboard shows). Empty category is a no-op.
func (h *InstanceHandler) DismissAll(w http.ResponseWriter, r *http.Request) {
	stateParam := r.URL.Query().Get("state")
	if stateParam != string(instance.StateFailed) && stateParam != string(instance.StateCompleted) {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "state must be 'failed' or 'completed'")
		return
	}
	user, err := h.currentUser.CurrentUser(r)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
		return
	}
	state := instance.State(stateParam)
	filters := instance.ListFilters{State: &state, Limit: maxListLimit}
	if s := r.URL.Query().Get("since"); s != "" {
		if t, perr := time.Parse(time.RFC3339, s); perr == nil {
			filters.Since = &t
		}
	}
	summaries, err := h.instances.List(r.Context(), filters)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list instances")
		return
	}
	for _, s := range summaries {
		if err := h.dismissals.Dismiss(r.Context(), user, s.ID); err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to dismiss instances")
			return
		}
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
```

In `server.go`, register inside the instances route group, **first** in the group:

```go
r.Post("/dismiss-all", instHandler.DismissAll)
```

- [ ] Run: `go test ./adapter/http/ -run TestDismissAll_` → **PASS**.
- [ ] Run full gate: `make test` → **PASS**.
- [ ] Commit: `feat(dismissal): POST /instances/dismiss-all handler+route [ui-nits p3 t2]`

**Definition of done for Task 2:** `currentUser` seam in place (returns default user today, Phase-6 comment); dismiss (terminal-only, 409 otherwise, 404 missing), restore (idempotent no-op), and dismiss-all (state ∈ {failed,completed} or 400, empty no-op) handlers registered and tested; all dismissal endpoints resolve the acting user via the seam; no global WS events emitted; `make test` green.
