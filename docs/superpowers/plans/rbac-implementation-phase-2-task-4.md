# RBAC Phase 2 — Task 4: Role-Gated `Publish(visibility, department)` + `SetVisibility` + Handlers/MCP

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `Publish` to take a visibility + department and enforce role gating (member → own department; lead/admin → public), add a `SetVisibility` method (the cheap "column flip" the metadata model enables), and thread both through the HTTP handler and the MCP tool. The copy-then-record ordering/rollback invariant in `Publish` is PRESERVED exactly.

**Architecture:** Role decisions live in the domain service, read from the context identity (`identity.Identity`). `Publish` keeps its two-store, copy-first-record-second sequence and its fresh-vs-overwrite compensation logic untouched; the only additions are an up-front role check and passing visibility/department to `Upsert`. `SetVisibility` is a pure record update (no tree copy).

**Tech Stack:** Go, `domain/workflow`, `adapter/http`, `adapter/mcp`, `domain/identity`.

**CROSS-PHASE DEPENDENCY (REQUIRED):** consumes Phase 1's `identity.Identity.LedDepartments []string` and `InDepartment`. The public-promotion gate is `len(id.LedDepartments) > 0 || id.IsAdmin()`. Verify before starting:
`grep -n "LedDepartments" domain/identity/identity.go`

---

## Context you need

- `domain/workflow/service.go` `Publish(ctx, privateName, publishedName)` — see the ORDERING INVARIANT block; do not alter the copy-then-record / fresh-vs-overwrite rollback logic. It currently calls `s.published.PublisherOf` then `repo.CopyTree` then `s.published.Upsert(ctx, publishedName, id.UserID, privateName)`.
- After Task 2, `Upsert` takes `(ctx, name, publisher, sourceName, visibility Visibility, department string)`.
- After Task 3, `CanSeePublication` exists; `caller(ctx)` extracts identity.
- HTTP handler `adapter/http/workflow_handler.go` `Publish` reads `publishRequest{PublishedName}`; maps `ErrUnauthorized`→403, `ErrNotFound`→404, `ErrAlreadyExists`→409.
- MCP tool `adapter/mcp/tools_workflow.go` `workflowPublishHandler` reads `name` + `publishedName`; calls `workflowSvc(srv).Publish(ctx, name, publishedName)`. Tool registration at line ~132 declares the two string params.
- `domain/workflow/errors.go` has `ErrUnauthorized`. Add a new `ErrInvalidVisibility` here for the missing-department / bad-level case so adapters can map it to 400.

## Files

- Modify: `domain/workflow/errors.go` (add `ErrInvalidVisibility`)
- Modify: `domain/workflow/service.go` (`Publish` signature + gating; new `SetVisibility`)
- Modify: `adapter/http/workflow_handler.go` (`publishRequest` fields; `Publish` handler; new `SetVisibility` handler + route)
- Modify: `adapter/http/server.go` (register the `PUT /workflows/{name}/visibility` route)
- Modify: `adapter/mcp/tools_workflow.go` (publish tool gains visibility/department; new `workflow_set_visibility` tool)
- Test: `domain/workflow/service_test.go` (publish-gating + set-visibility scenarios)
- Test: `adapter/http/workflow_handler_test.go` (handler-level publish/visibility)

---

## Steps

- [ ] **Step 1: Add `ErrInvalidVisibility`**

In `domain/workflow/errors.go`, add:

```go
	// ErrInvalidVisibility indicates a publish/visibility request specified an
	// unstorable level, or omitted the required department for a department-level
	// publication.
	ErrInvalidVisibility = errors.New("invalid visibility request")
```

- [ ] **Step 2: Write failing Publish-gating tests**

In `domain/workflow/service_test.go` (uses `ctxWithDepts` from Task 3 and `idWith`-style identities; if `ctxWithDepts` lacks led-departments, add `ctxWithRoles` below):

```go
// ctxWithRoles builds a context identity with departments AND led departments.
func ctxWithRoles(user uuid.UUID, admin bool, depts, led []string) context.Context {
	role := identity.RoleUser
	if admin {
		role = identity.RoleAdmin
	}
	return identity.WithIdentity(context.Background(), identity.Identity{
		UserID: user, Username: "u", Role: role,
		Departments: depts, LedDepartments: led,
	})
}

func seedPrivate(repo *fakeRepo, owner uuid.UUID, name string) {
	repo.bucket(workflow.Private(owner))[name] = wf(name)
}

func TestService_Publish_MemberToOwnDepartment_OK(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d1")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityDepartment, "engineering")
	require.NoError(t, err)

	pub, ok, _ := pubs.PublicationOf(context.Background(), "shared")
	require.True(t, ok)
	assert.Equal(t, workflow.VisibilityDepartment, pub.Visibility)
	assert.Equal(t, "engineering", pub.Department)
}

func TestService_Publish_MemberToOtherDepartment_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d2")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityDepartment, "sales")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}

func TestService_Publish_MemberToPublic_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d3")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityPublic, "")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}

func TestService_Publish_LeadToPublic_OK(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d4")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, []string{"engineering"}), "src", "shared", workflow.VisibilityPublic, "")
	require.NoError(t, err)
	pub, ok, _ := pubs.PublicationOf(context.Background(), "shared")
	require.True(t, ok)
	assert.Equal(t, workflow.VisibilityPublic, pub.Visibility)
}

func TestService_Publish_AdminToPublic_OK(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d5")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Publish(ctxWithRoles(owner, true, nil, nil), "src", "shared", workflow.VisibilityPublic, "")
	require.NoError(t, err)
}

func TestService_Publish_DepartmentLevelMissingDepartment_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d6")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityDepartment, "")
	assert.ErrorIs(t, err, workflow.ErrInvalidVisibility)
}

func TestService_Publish_PrivateVisibility_Rejected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000d7")
	seedPrivate(repo, owner, "src")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	// "private" is not a storable publication level.
	err := svc.Publish(ctxWithRoles(owner, false, []string{"engineering"}, nil), "src", "shared", workflow.VisibilityPrivate, "")
	assert.ErrorIs(t, err, workflow.ErrInvalidVisibility)
}
```

The EXISTING publish tests (`TestService_Publish_HappyPath`, `_Overwrites`, `_SharedContentUpdated`, `_SourceMissing_NotFound`, `_DifferentPublisher_Unauthorized`, `_NoStore_Errors`, and the compensation tests) call `svc.Publish(ctx, "src", "pub")` with two name args. Update each to the new 4-arg form using `workflow.VisibilityPublic, ""` and an admin context (`ctxWith(owner, true)`), so the public-promotion gate passes and prior behavior is preserved. The compensation/overwrite tests must keep using the same publisher identity across calls.

- [ ] **Step 3: Run to verify they fail**

Run: `go test ./domain/workflow/ -run TestService_Publish -v`
Expected: FAIL — `Publish` arity mismatch / no gating.

- [ ] **Step 4: Implement gating + the widened `Publish`**

Add a private helper above `Publish` in `service.go`:

```go
// authorizePublish validates the requested visibility/department against the
// caller's role per spec §Workflow Visibility:
//   - department: caller must be a MEMBER of department; department required.
//   - public: caller must be a LEAD (of any department) or an ADMIN.
//   - private: not a storable publication level → ErrInvalidVisibility.
func authorizePublish(id identity.Identity, visibility Visibility, department string) error {
	switch visibility {
	case VisibilityDepartment:
		if department == "" {
			return fmt.Errorf("%w: department required for department visibility", ErrInvalidVisibility)
		}
		if !id.IsAdmin() && !id.InDepartment(department) {
			return fmt.Errorf("%w: not a member of department %q", ErrUnauthorized, department)
		}
		return nil
	case VisibilityPublic:
		if id.IsAdmin() || len(id.LedDepartments) > 0 {
			return nil
		}
		return fmt.Errorf("%w: publishing to public requires a lead or admin", ErrUnauthorized)
	default:
		return fmt.Errorf("%w: %q is not a publishable visibility", ErrInvalidVisibility, visibility)
	}
}
```

Change the `Publish` signature and add the gate up-front; keep the ordering invariant block verbatim, only changing the final `Upsert` call to pass visibility+department:

```go
func (s *WorkflowService) Publish(ctx context.Context, privateName, publishedName string, visibility Visibility, department string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if s.published == nil {
		return fmt.Errorf("%w: publishing is not configured", ErrUnauthorized)
	}
	if err := authorizePublish(id, visibility, department); err != nil {
		return err
	}

	// ... UNCHANGED: source-exists check, publisher-only-overwrite check,
	//     CopyTree(overwrite), and the fresh-vs-overwrite compensation logic ...
	// ONLY the recording call changes:
	if err := s.published.Upsert(ctx, publishedName, id.UserID, privateName, visibility, department); err != nil {
		// ... UNCHANGED compensation block (fresh path deletes, overwrite path surfaces) ...
	}
	return nil
}
```

When editing, do not change the structure of the rollback `if err := s.published.Upsert(...)` block other than the call arguments.

- [ ] **Step 5: Run to verify Publish tests pass**

Run: `go test ./domain/workflow/ -run TestService_Publish -v`
Expected: PASS (both new gating tests and the migrated existing tests).

- [ ] **Step 6: Write failing `SetVisibility` tests**

```go
func TestService_SetVisibility_LeadPromotesDeptToPublic(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e1")
	// Existing department publication owned by `owner`.
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityDepartment, "engineering")
	repo.bucket(workflow.Shared())["shared"] = wf("shared")
	svc := workflow.NewWorkflowService(repo, pubs)

	// A lead of engineering promotes to public.
	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, []string{"engineering"}), "shared", workflow.VisibilityPublic, "")
	require.NoError(t, err)
	pub, _, _ := pubs.PublicationOf(context.Background(), "shared")
	assert.Equal(t, workflow.VisibilityPublic, pub.Visibility)
}

func TestService_SetVisibility_MemberCannotPromoteToPublic(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e2")
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, nil), "shared", workflow.VisibilityPublic, "")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}

func TestService_SetVisibility_DemotePublicToDepartment(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e3")
	_ = pubs.Upsert(context.Background(), "shared", owner, "src", workflow.VisibilityPublic, "")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, nil), "shared", workflow.VisibilityDepartment, "engineering")
	require.NoError(t, err)
	pub, _, _ := pubs.PublicationOf(context.Background(), "shared")
	assert.Equal(t, workflow.VisibilityDepartment, pub.Visibility)
	assert.Equal(t, "engineering", pub.Department)
}

func TestService_SetVisibility_UnknownPublication_NotFound(t *testing.T) {
	svc := workflow.NewWorkflowService(newFakeRepo(), newFakePublishers())
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000e4")
	err := svc.SetVisibility(ctxWithRoles(owner, false, []string{"engineering"}, nil), "missing", workflow.VisibilityDepartment, "engineering")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_SetVisibility_DifferentPublisher_Unauthorized(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000e5")
	other := uuid.MustParse("00000000-0000-0000-0000-0000000000e6")
	_ = pubs.Upsert(context.Background(), "shared", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	// A non-owner, non-admin, non-lead cannot change someone else's publication.
	err := svc.SetVisibility(ctxWithRoles(other, false, []string{"engineering"}, nil), "shared", workflow.VisibilityDepartment, "engineering")
	assert.ErrorIs(t, err, workflow.ErrUnauthorized)
}
```

- [ ] **Step 7: Run to verify SetVisibility tests fail**

Run: `go test ./domain/workflow/ -run TestService_SetVisibility -v`
Expected: FAIL — `SetVisibility` undefined.

- [ ] **Step 8: Implement `SetVisibility`**

Add to `service.go`:

```go
// SetVisibility changes an existing publication's level without re-copying the
// shared tree (the metadata model makes this a cheap "column flip"). The caller
// must be the original publisher, a lead of the relevant department, or an
// admin; the target level is gated by the same rules as Publish (member→own
// department, lead/admin→public). An unknown publication is ErrNotFound.
func (s *WorkflowService) SetVisibility(ctx context.Context, publishedName string, visibility Visibility, department string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if s.published == nil {
		return fmt.Errorf("%w: publishing is not configured", ErrUnauthorized)
	}

	cur, ok, err := s.published.PublicationOf(ctx, publishedName)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, publishedName)
	}

	// Only the publisher, a lead, or an admin may mutate a publication.
	if cur.Publisher != id.UserID && !id.IsAdmin() && len(id.LedDepartments) == 0 {
		return fmt.Errorf("%w: cannot change visibility of %q", ErrUnauthorized, publishedName)
	}
	// Target level role gate (same as Publish).
	if err := authorizePublish(id, visibility, department); err != nil {
		return err
	}

	// Preserve the original publisher and source name (Task 2's Publication
	// carries SourceName so the NOT NULL source_name column survives the flip);
	// only the visibility level changes.
	if err := s.published.Upsert(ctx, publishedName, cur.Publisher, cur.SourceName, visibility, department); err != nil {
		return fmt.Errorf("updating visibility for %q: %w", publishedName, err)
	}
	return nil
}
```

NOTE: `cur.SourceName` is populated by `PublicationOf` (Task 2 added `SourceName` to `Publication` and the adapter/fake populate it). No additional struct change is needed here. Add an assertion to `TestService_SetVisibility_DemotePublicToDepartment` that `pub` read back after the flip retains its publisher (the fake preserves `SourceName` through `Upsert`).

- [ ] **Step 9: Run to verify SetVisibility tests pass**

Run: `go test ./domain/workflow/... -v`
Expected: PASS.

- [ ] **Step 10: Update the HTTP Publish handler + add SetVisibility handler**

In `adapter/http/workflow_handler.go`, extend `publishRequest`:

```go
type publishRequest struct {
	PublishedName string `json:"publishedName"`
	Visibility    string `json:"visibility"` // "department" | "public"
	Department    string `json:"department,omitempty"`
}
```

In `Publish`, after validating `PublishedName`, default + validate visibility and pass it through. Default to `public` only when empty AND the caller is allowed (let the service decide); simplest: require the client to send it, but default empty→`public` to preserve the old single-arg contract for existing clients:

```go
	vis := req.Visibility
	if vis == "" {
		vis = "public"
	}
	if err := h.wfSvc.Publish(r.Context(), name, req.PublishedName, workflow.Visibility(vis), req.Department); err != nil {
		switch {
		case errors.Is(err, workflow.ErrInvalidVisibility):
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		case errors.Is(err, workflow.ErrUnauthorized):
			WriteError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		case errors.Is(err, workflow.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		case errors.Is(err, workflow.ErrAlreadyExists):
			WriteError(w, http.StatusConflict, "CONFLICT", err.Error())
		default:
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to publish workflow")
		}
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "published", "name": req.PublishedName})
```

Add a `SetVisibility` handler:

```go
type visibilityRequest struct {
	Visibility string `json:"visibility"`
	Department string `json:"department,omitempty"`
}

// SetVisibility handles PUT /workflows/{name}/visibility and changes an existing
// shared workflow's sharing level. {name} is the published name.
func (h *WorkflowHandler) SetVisibility(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !safeWorkflowName(name) {
		WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTriggerBodyBytes)
	var req visibilityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid request body: "+err.Error())
		return
	}
	if err := h.wfSvc.SetVisibility(r.Context(), name, workflow.Visibility(req.Visibility), req.Department); err != nil {
		switch {
		case errors.Is(err, workflow.ErrInvalidVisibility):
			WriteError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		case errors.Is(err, workflow.ErrUnauthorized):
			WriteError(w, http.StatusForbidden, "FORBIDDEN", err.Error())
		case errors.Is(err, workflow.ErrNotFound):
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "workflow not found: "+name)
		default:
			WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to set visibility")
		}
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{"status": "visibility_updated", "name": name})
}
```

- [ ] **Step 11: Register the visibility route**

In `adapter/http/server.go`, find where the workflow sub-routes are registered (near `wfHandler.SetWorkflowService` / the `/workflows/{name}/...` route group) and add:

```go
		r.Put("/{name}/visibility", wfHandler.SetVisibility)
```

next to the existing `Publish`/`Copy` routes (match the surrounding chi router style — they may use `r.Post("/{name}/publish", wfHandler.Publish)`).

- [ ] **Step 12: Write a handler-level test**

In `adapter/http/workflow_handler_test.go` (follow the existing handler test harness — it constructs a `WorkflowHandler`, injects a `WorkflowService` via `SetWorkflowService`, and sets an identity on the request context). Add a member-forbidden-to-public test:

```go
func TestWorkflowHandler_Publish_MemberToPublic_Forbidden(t *testing.T) {
	// build handler with a real WorkflowService over a fake repo+publishers,
	// request body {"publishedName":"s","visibility":"public"}, identity = member.
	// assert HTTP 403.
}
```

(If the existing handler tests do not already construct a service with publishers, mirror the domain `fakeRepo`/`fakePublishers` setup, or use an in-memory SQLite repo via `adapter/sqlite` helpers. Keep it to one happy + one forbidden case at this layer; exhaustive role logic is covered by the domain tests.)

- [ ] **Step 13: Update the MCP publish tool + add set_visibility tool**

In `adapter/mcp/tools_workflow.go`:

- In the `workflow_publish` registration, add two optional string params:

```go
		gomcp.WithString("visibility", gomcp.Description("Sharing level: department or public (default public)")),
		gomcp.WithString("department", gomcp.Description("Required when visibility=department; one of your departments")),
```

- In `workflowPublishHandler`, read them and pass through:

```go
		visibility, _ := args["visibility"].(string)
		if visibility == "" {
			visibility = "public"
		}
		department, _ := args["department"].(string)
		if err := workflowSvc(srv).Publish(ctx, name, publishedName, workflow.Visibility(visibility), department); err != nil {
			switch {
			case errors.Is(err, workflow.ErrInvalidVisibility):
				return gomcp.NewToolResultError(fmt.Sprintf("invalid visibility for %q: %s", publishedName, err)), nil
			case errors.Is(err, workflow.ErrUnauthorized):
				return gomcp.NewToolResultError(fmt.Sprintf("not authorized to publish %q: %s", publishedName, err)), nil
			case errors.Is(err, workflow.ErrAlreadyExists):
				return gomcp.NewToolResultError(fmt.Sprintf("published name already exists: %s", publishedName)), nil
			default:
				return gomcp.NewToolResultError(fmt.Sprintf("failed to publish workflow: %s", err)), nil
			}
		}
```

(Confirm `errors` and `workflow` are imported in this file; add if missing.)

- Register a new tool near the publish tool:

```go
		gomcp.NewTool("workflow_set_visibility",
			gomcp.WithDescription("Change an existing shared workflow's sharing level"),
			gomcp.WithString("name", gomcp.Required(), gomcp.Description("Published workflow name")),
			gomcp.WithString("visibility", gomcp.Required(), gomcp.Description("department or public")),
			gomcp.WithString("department", gomcp.Description("Required when visibility=department")),
		),
		workflowSetVisibilityHandler(srv),
```

- Add the handler:

```go
func workflowSetVisibilityHandler(srv *Server) server.ToolHandlerFunc {
	return func(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
		args := req.GetArguments()
		name, _ := args["name"].(string)
		visibility, _ := args["visibility"].(string)
		department, _ := args["department"].(string)
		if name == "" {
			return gomcp.NewToolResultError("name is required"), nil
		}
		if !safeWorkflowName(name) {
			return gomcp.NewToolResultError(fmt.Sprintf("invalid workflow name: %s", name)), nil
		}
		if err := workflowSvc(srv).SetVisibility(ctx, name, workflow.Visibility(visibility), department); err != nil {
			switch {
			case errors.Is(err, workflow.ErrInvalidVisibility):
				return gomcp.NewToolResultError(fmt.Sprintf("invalid visibility: %s", err)), nil
			case errors.Is(err, workflow.ErrUnauthorized):
				return gomcp.NewToolResultError(fmt.Sprintf("not authorized: %s", err)), nil
			case errors.Is(err, workflow.ErrNotFound):
				return gomcp.NewToolResultError(fmt.Sprintf("workflow not found: %s", name)), nil
			default:
				return gomcp.NewToolResultError(fmt.Sprintf("failed to set visibility: %s", err)), nil
			}
		}
		return MarshalResult(map[string]string{"status": "visibility_updated", "name": name})
	}
}
```

NOTE: `workflowSvc(srv)` currently builds the service with `nil` publishers (`tools_workflow.go:29`). For publish/set-visibility to function via MCP it must use the real `PublishedStore`. Update `workflowSvc` to construct the service over `srv.deps.Workflows` and the published repo if `srv.deps` exposes it; if not, add a `PublishedStore` to `srv.deps` and wire it in `cmd/arbitron/main.go` where the MCP server is constructed. Verify the deps struct: `grep -n "deps\b" adapter/mcp/server.go`.

- [ ] **Step 14: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 15: Commit**

```bash
git add domain/workflow/errors.go domain/workflow/service.go domain/workflow/service_test.go \
        domain/workflow/visibility.go \
        adapter/http/workflow_handler.go adapter/http/workflow_handler_test.go adapter/http/server.go \
        adapter/mcp/tools_workflow.go adapter/mcp/server.go cmd/arbitron/main.go \
        adapter/sqlite/published_workflow_repo.go
git commit -m "feat(rbac): role-gated Publish(visibility) + SetVisibility + handlers/MCP

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Dependencies

- **Depends on:** Task 2 (`Upsert` arity, `Publication`), Task 3 (`CanSeePublication`), and **Phase 1** identity (`LedDepartments`, `InDepartment`).
- **Blocks:** Task 6 (audit emission wraps `Publish`/`SetVisibility`). Task 5 (audit infra) is independent and may proceed in parallel.
