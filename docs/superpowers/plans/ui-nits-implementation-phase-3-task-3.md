# UI Nits Phase 3 — Task 3: Instance List endpoint — mark + omit/include dismissed per user

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax. STRICT TDD: failing test → run (expect FAIL) → minimal impl → run (expect PASS) → commit. **Depends on Task 1 (port) and Task 2 (handler deps + `currentUser` seam + `Dismissals` in `RouterDeps`).**

**Goal:** `GET /api/v1/instances` becomes per-user-aware: it loads the acting user's dismissed-IDs, marks each returned summary with a `dismissed` flag, **omits dismissed items by default**, and **includes them (marked) when `includeDismissed=true`** — without changing the existing sort, so the frontend can reinsert dismissed items in chronological position.

---

### Task 3: List marks/omits/includes dismissed per user + tests

**Context for the implementer (read before starting):**

- `List` is in `adapter/http/instance_handler.go` (lines ~180-221). It builds `instance.ListFilters` from query params (`state`, `workflowName`, `limit`, `offset`, `since`), calls `h.instances.List(ctx, filters)` → `[]instance.InstanceSummary`, normalizes nil→`[]`, and `WriteJSON`s the slice directly.
- The summary returned to JSON is the domain `instance.InstanceSummary` (in `domain/instance/instance.go`), which is serialized with its own json tags (`id`, `workflowName`, `state`, `priority`, `currentNodeId`, `createdAt`, `updatedAt`, `completedAt`, `queuedAt`). The frontend type `InstanceSummary` in `dashboard/src/api/types.ts` mirrors these fields.
- We must add a `Dismissed bool` to what the list returns. **Do not** mutate the domain `InstanceSummary` struct to carry per-user view state (that would leak request-scoped state into the domain model). Instead, define an HTTP-layer response DTO that embeds/copies the summary fields plus `dismissed`. Match the existing JSON shape exactly so the frontend's existing fields keep working; add `"dismissed": bool` (omit `omitempty` so the field is always present and the frontend can rely on it).
- Only terminal items can be dismissed, so Active items always serialize `dismissed: false`. The omit-by-default logic therefore only ever removes failed/completed items.
- The handler already has `h.dismissals` and `h.currentUser` from Task 2.
- Tests: extend `adapter/http/dismissal_handler_test.go` (or a sibling `instance_list_dismissal_test.go`) using `newMockInstanceRepo`, `newMockDismissalRepo`, `mockUserRepo`, and `newDismissalTestServer` from Task 2. Decode the JSON array of summaries into a local struct that includes `Dismissed bool`.

**Files:**
- `adapter/http/instance_handler.go` (modify `List`; add a `summaryResponse` DTO + converter)
- `adapter/http/instance_list_dismissal_test.go` (new — list tests)
- `dashboard/src/api/types.ts` (modify: add `dismissed?: boolean` to `InstanceSummary`) — also touched in Task 5; do the type change here so the backend contract and the TS type land together. If Task 5 runs first, skip the duplicate edit.

---

#### Step 3.1 — Failing test: default list omits the acting user's dismissed items

- [ ] Create `adapter/http/instance_list_dismissal_test.go` (package `http_test`):

```go
package http_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listItem mirrors the JSON the list endpoint returns, including the new flag.
type listItem struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Dismissed bool   `json:"dismissed"`
}

func getList(t *testing.T, url string) []listItem {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	var items []listItem
	require.NoError(t, json.Unmarshal(body, &items))
	return items
}

func TestList_OmitsDismissedByDefault(t *testing.T) {
	user := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	keep := uuid.New()
	gone := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: keep, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: gone, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, user, gone))

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	items := getList(t, srv.URL+"/api/v1/instances?state=failed")
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
		assert.False(t, it.Dismissed, "default list never returns dismissed=true items")
	}
	assert.True(t, ids[keep.String()])
	assert.False(t, ids[gone.String()], "dismissed item must be omitted by default")
}
```

- [ ] Run: `go test ./adapter/http/ -run TestList_OmitsDismissedByDefault`
- [ ] Expected: **FAIL** — dismissed item still present (and/or `dismissed` field absent).

#### Step 3.2 — Minimal impl: load dismissed-IDs, mark, omit by default

- [ ] In `instance_handler.go`, add a response DTO + converter near `instanceResponse`:

```go
// summaryResponse is the list-item JSON: the existing InstanceSummary fields
// plus a per-user "dismissed" flag. The flag is HTTP/view state and is
// intentionally not part of the domain InstanceSummary.
type summaryResponse struct {
	ID            string  `json:"id"`
	WorkflowName  string  `json:"workflowName"`
	State         string  `json:"state"`
	Priority      string  `json:"priority"`
	CurrentNodeID *string `json:"currentNodeId,omitempty"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
	CompletedAt   *string `json:"completedAt,omitempty"`
	QueuedAt      *string `json:"queuedAt,omitempty"`
	Dismissed     bool    `json:"dismissed"`
}

func toSummaryResponse(s instance.InstanceSummary, dismissed bool) summaryResponse {
	resp := summaryResponse{
		ID:            s.ID.String(),
		WorkflowName:  s.WorkflowName,
		State:         string(s.State),
		Priority:      string(s.Priority),
		CurrentNodeID: s.CurrentNodeID,
		CreatedAt:     s.CreatedAt.Format(APITimeFormat),
		UpdatedAt:     s.UpdatedAt.Format(APITimeFormat),
		Dismissed:     dismissed,
	}
	if s.CompletedAt != nil {
		v := s.CompletedAt.Format(APITimeFormat)
		resp.CompletedAt = &v
	}
	if s.QueuedAt != nil {
		v := s.QueuedAt.Format(APITimeFormat)
		resp.QueuedAt = &v
	}
	return resp
}
```

- [ ] Rewrite the tail of `List` (after `summaries` is fetched) to resolve the user, load dismissed-IDs, and filter/mark:

```go
	summaries, err := h.instances.List(r.Context(), filters)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list instances")
		return
	}

	includeDismissed := false
	if v := r.URL.Query().Get("includeDismissed"); v != "" {
		if parsed, perr := strconv.ParseBool(v); perr == nil {
			includeDismissed = parsed
		}
	}

	var dismissedIDs map[uuid.UUID]bool
	if h.dismissals != nil && h.currentUser != nil {
		user, uerr := h.currentUser.CurrentUser(r)
		if uerr != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve user")
			return
		}
		dismissedIDs, err = h.dismissals.DismissedIDs(r.Context(), user)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load dismissals")
			return
		}
	}

	out := make([]summaryResponse, 0, len(summaries))
	for _, s := range summaries {
		isDismissed := dismissedIDs[s.ID]
		if isDismissed && !includeDismissed {
			continue // omit dismissed items by default
		}
		out = append(out, toSummaryResponse(s, isDismissed))
	}
	WriteJSON(w, http.StatusOK, out)
```

- [ ] Ensure `uuid` and `strconv` are imported (already imported in this file).
- [ ] Run: `go test ./adapter/http/ -run TestList_OmitsDismissedByDefault` → **PASS**.
- [ ] Commit: `feat(dismissal): list omits+marks dismissed per user [ui-nits p3 t3]`

#### Step 3.3 — Failing test: `includeDismissed=true` returns dismissed items marked, order unchanged

- [ ] Add:

```go
func TestList_IncludeDismissed_MarksAndKeepsOrder(t *testing.T) {
	user := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	a := uuid.New(); b := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: a, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: b, OwnerID: user, WorkflowName: "wf", State: instance.StateFailed}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, user, a))

	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: user})
	defer srv.Close()

	items := getList(t, srv.URL+"/api/v1/instances?state=failed&includeDismissed=true")
	require.Len(t, items, 2, "includeDismissed=true returns all items in the window")
	flags := map[string]bool{}
	for _, it := range items {
		flags[it.ID] = it.Dismissed
	}
	assert.True(t, flags[a.String()], "dismissed item must be marked dismissed=true")
	assert.False(t, flags[b.String()], "non-dismissed item marked dismissed=false")
}
```

- [ ] Run: `go test ./adapter/http/ -run TestList_IncludeDismissed` → **PASS** (Step 3.2 already implements include).

> Order note: the mock repo's `List` iterates a map (unordered). The real `instance_repo.go` `List` preserves `ORDER BY created_at DESC` and Task 3 does not re-sort — the filter loop preserves input order. The "order unchanged" guarantee is asserted at the repo level (Task 1 / existing `instances.sql` `ORDER BY created_at DESC`) and relied on here. If a deterministic-order assertion is desired, add it against the sqlite repo path rather than the map-based mock.

- [ ] Commit: `test(dismissal): list includeDismissed marks + preserves order [ui-nits p3 t3]`

#### Step 3.4 — Failing test: scoping — another user's list is unaffected

- [ ] Add:

```go
func TestList_DismissalScopedToActingUser(t *testing.T) {
	userA := uuid.New()
	userB := uuid.New()
	ctx := context.Background()
	instRepo := newMockInstanceRepo()
	id := uuid.New()
	require.NoError(t, instRepo.Create(ctx, &instance.Instance{ID: id, OwnerID: userA, WorkflowName: "wf", State: instance.StateCompleted}))
	dis := newMockDismissalRepo()
	require.NoError(t, dis.Dismiss(ctx, userA, id)) // only A dismisses

	// Acting as B: the item is still visible (B never dismissed it).
	srv := newDismissalTestServer(t, instRepo, dis, &mockUserRepo{id: userB})
	defer srv.Close()

	items := getList(t, srv.URL+"/api/v1/instances?state=completed")
	require.Len(t, items, 1)
	assert.False(t, items[0].Dismissed, "B sees the item as not dismissed")
}
```

- [ ] Run: `go test ./adapter/http/ -run TestList_DismissalScopedToActingUser` → **PASS**.
- [ ] Run full handler suite: `go test ./adapter/http/...` → **PASS** (the list JSON now includes `dismissed`; confirm no existing list test asserts an exact JSON without the new field — if one does, update it to tolerate the added field).
- [ ] Commit: `test(dismissal): list dismissal scoped per acting user [ui-nits p3 t3]`

#### Step 3.5 — Frontend type contract (if not already done in Task 5)

- [ ] In `dashboard/src/api/types.ts`, add the flag to `InstanceSummary`:

```ts
export interface InstanceSummary {
  id: string
  workflowName: string
  state: InstanceState
  priority: Priority
  currentNodeId?: string
  createdAt: string
  updatedAt: string
  completedAt?: string
  queuedAt?: string
  /** Whether the acting user has dismissed this instance from their view. */
  dismissed?: boolean
}
```

- [ ] Run: `cd dashboard && npx tsc --noEmit` → **PASS** (additive optional field; nothing breaks).
- [ ] Run full gate: `make test` → **PASS**.
- [ ] Commit: `feat(dismissal): InstanceSummary.dismissed type [ui-nits p3 t3]`

**Definition of done for Task 3:** `GET /instances` loads the acting user's dismissed-IDs, marks each summary `dismissed`, omits dismissed by default, includes+marks them with `includeDismissed=true`, preserves existing order, and is scoped to the acting user; the TS `InstanceSummary` carries `dismissed?`; `make test` green.
