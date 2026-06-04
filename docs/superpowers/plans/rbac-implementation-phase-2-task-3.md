# RBAC Phase 2 — Task 3: `canSeePublication` Predicate + Read-Path Gating (List / Load / LoadVisible / Copy / Trigger)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce the single `canSeePublication` visibility predicate and apply it to every read path that can surface a shared workflow — `List`, `Load`/`LoadVisible`, `Copy`, and the engine `Trigger` path — so a caller can only see (and trigger and copy) shared workflows their identity is allowed to see. Unseeable department workflows return `ErrNotFound` (no existence leak).

**Architecture:** Gating lives in `domain/workflow` (authz-in-domain-services, Phase 1 decision). `LoadVisible` gains an optional publication-aware filter so the engine's trigger path is gated without the engine importing publication internals. A service constructed with **no** `PublishedStore` treats all shared entries as public, preserving local/read-only-mode openness. The copy-then-record publish ordering/rollback invariant in `service.go` is untouched.

**Tech Stack:** Go, `domain/workflow`, `domain/engine`, `domain/identity`.

**CROSS-PHASE DEPENDENCY (REQUIRED):** This task consumes Phase 1's `identity.Identity` helpers `InDepartment(dept string) bool` and the `Departments`/`LedDepartments` fields. If Phase 1 has not landed in this branch, `domain/identity/identity.go` will lack them — verify before starting:
`grep -n "func (i Identity) InDepartment" domain/identity/identity.go`
If absent, STOP and land Phase 1 first; do not stub these helpers here.

---

## Context you need

- `domain/workflow/service.go`: `List` iterates shared entries and tags `Publisher` from `s.publishers.PublisherOf`. `Load` calls `LoadVisible(ctx, s.repo, id.UserID, name)`. `Copy` calls `repo.CopyTree(Shared(), ...)`. The service holds `publishers PublisherLookup` (may be nil) and `published PublishedStore` (nil when read-only).
- `domain/workflow/resolve.go`: `LoadVisible(ctx, repo, owner, name) (*Workflow, Scope, error)` tries `Private(owner)` then `Shared()`. `ReadScopes` returns the ordered scopes.
- `domain/engine/engine.go:182`: `wf, _, err := workflow.LoadVisible(ctx, e.workflows, owner, workflowName)` is the **only** trigger read path. The engine holds `e.workflows workflow.WorkflowRepository` but NOT a publisher lookup today.
- `domain/workflow/visibility.go` (Task 2): `Publication{Publisher, Visibility, Department}`, `Visibility` constants.
- Identity from context via `identity.FromContext`. `caller(ctx)` helper already extracts it in the service.
- Existing test fakes: `fakeRepo`, `fakePublishers` (now publication-aware after Task 2), `ctxWith(id, admin)` in `service_test.go`. NOTE Task 2 left `ctxWith` building an identity with only `UserID/Username/Role`; this task needs identities carrying departments, so add a richer helper (Step 1).

## Files

- Create: `domain/workflow/gating.go` (`canSeePublication` + the publication-aware visible-filter used by Load/Trigger)
- Modify: `domain/workflow/resolve.go` (add `LoadVisibleFiltered` that consults a publication predicate for the shared scope)
- Modify: `domain/workflow/service.go` (`List`, `Load`, `Copy` gating)
- Modify: `domain/engine/engine.go` (Trigger uses the gated load; engine gains an optional publication lookup)
- Test: `domain/workflow/gating_test.go` (new)
- Test: `domain/workflow/service_test.go` (gating scenarios + richer identity helper)
- Test: `domain/engine/engine_trigger_visibility_test.go` (new)

---

## Steps

- [ ] **Step 1: Write the failing `canSeePublication` unit test**

Create `domain/workflow/gating_test.go`:

```go
package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// idWith builds an identity carrying departments and (optionally) admin role.
func idWith(user uuid.UUID, admin bool, depts ...string) identity.Identity {
	role := identity.RoleUser
	if admin {
		role = identity.RoleAdmin
	}
	return identity.Identity{UserID: user, Username: "u", Role: role, Departments: depts}
}

func TestCanSeePublication(t *testing.T) {
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	viewer := uuid.MustParse("00000000-0000-0000-0000-0000000000a2")

	pubPublic := workflow.Publication{Publisher: publisher, Visibility: workflow.VisibilityPublic}
	pubDeptEng := workflow.Publication{Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"}

	t.Run("admin sees everything", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(viewer, true), pubDeptEng))
	})
	t.Run("publisher sees own department workflow", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(publisher, false), pubDeptEng))
	})
	t.Run("public visible to any member", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(viewer, false), pubPublic))
	})
	t.Run("department visible to a member of that department", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(viewer, false, "engineering"), pubDeptEng))
	})
	t.Run("department hidden from a non-member outsider", func(t *testing.T) {
		assert.False(t, workflow.CanSeePublication(idWith(viewer, false, "sales"), pubDeptEng))
	})
	t.Run("department hidden from a deptless outsider", func(t *testing.T) {
		assert.False(t, workflow.CanSeePublication(idWith(viewer, false), pubDeptEng))
	})
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./domain/workflow/ -run TestCanSeePublication -v`
Expected: FAIL — `workflow.CanSeePublication` undefined (and `identity.Identity.Departments` must exist — see cross-phase note).

- [ ] **Step 3: Implement `canSeePublication`**

Create `domain/workflow/gating.go`:

```go
package workflow

import (
	"context"

	"github.com/arbitron/arbitron/domain/identity"
)

// CanSeePublication is the single visibility predicate consulted by every read
// path that can surface a shared (published) workflow. Order matters only for
// clarity, not correctness:
//   - admins see everything (this is also the "admin override" audit trigger,
//     recorded by the caller in Task 6 when admin is the SOLE reason);
//   - the publisher always sees their own publication;
//   - public is visible to everyone;
//   - department is visible only to members of that department.
// private workflows are never publications, so they never reach this predicate.
func CanSeePublication(id identity.Identity, p Publication) bool {
	return id.IsAdmin() ||
		p.Publisher == id.UserID ||
		p.Visibility == VisibilityPublic ||
		(p.Visibility == VisibilityDepartment && id.InDepartment(p.Department))
}

// canSeeShared resolves the publication for a shared name and applies
// CanSeePublication. A nil lookup, or a name with no publication record, is
// treated as visible-public (local/read-only mode, and pre-publication-model
// folders, stay open).
func canSeeShared(ctx context.Context, lookup PublisherLookup, id identity.Identity, sharedName string) (bool, error) {
	if lookup == nil {
		return true, nil
	}
	pub, ok, err := lookup.PublicationOf(ctx, sharedName)
	if err != nil {
		return false, err
	}
	if !ok {
		return true, nil
	}
	return CanSeePublication(id, pub), nil
}
```

- [ ] **Step 4: Run to verify the predicate test passes**

Run: `go test ./domain/workflow/ -run TestCanSeePublication -v`
Expected: PASS.

- [ ] **Step 5: Write the failing `List` gating test**

In `domain/workflow/service_test.go`, add a richer context helper and a list-gating test. Add near `ctxWith`:

```go
// ctxWithDepts builds a context identity carrying departments.
func ctxWithDepts(user uuid.UUID, admin bool, depts ...string) context.Context {
	role := identity.RoleUser
	if admin {
		role = identity.RoleAdmin
	}
	return identity.WithIdentity(context.Background(),
		identity.Identity{UserID: user, Username: "u", Role: role, Departments: depts})
}
```

Then:

```go
func TestService_List_FiltersByVisibility(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
	viewer := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")

	// Three shared folders exist in the repo.
	repo.bucket(workflow.Shared())["pub-wf"] = wf("pub-wf")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	repo.bucket(workflow.Shared())["sales-wf"] = wf("sales-wf")
	// With matching publication records.
	_ = pubs.Upsert(context.Background(), "pub-wf", publisher, "src", workflow.VisibilityPublic, "")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	_ = pubs.Upsert(context.Background(), "sales-wf", publisher, "src", workflow.VisibilityDepartment, "sales")

	svc := workflow.NewWorkflowService(repo, pubs)

	// An engineering member sees public + eng, NOT sales.
	got, err := svc.List(ctxWithDepts(viewer, false, "engineering"))
	require.NoError(t, err)
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	assert.True(t, names["pub-wf"])
	assert.True(t, names["eng-wf"])
	assert.False(t, names["sales-wf"])
}

func TestService_List_CarriesVisibilityAndDepartment(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000b3")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")

	svc := workflow.NewWorkflowService(repo, pubs)
	got, err := svc.List(ctxWithDepts(publisher, false, "engineering"))
	require.NoError(t, err)

	var eng *workflow.WorkflowSummary
	for i := range got {
		if got[i].Name == "eng-wf" {
			eng = &got[i]
		}
	}
	require.NotNil(t, eng)
	assert.Equal(t, string(workflow.VisibilityDepartment), eng.Visibility)
	assert.Equal(t, "engineering", eng.Department)
}
```

This requires `WorkflowSummary` to carry `Visibility` and `Department`. Add those JSON fields in `domain/workflow/workflow.go`:

```go
// Visibility is "department" or "public" for shared entries; empty for private.
Visibility string `json:"visibility,omitempty"`
// Department is the owning department for department-visibility shared entries.
Department string `json:"department,omitempty"`
```

- [ ] **Step 6: Run to verify the List test fails**

Run: `go test ./domain/workflow/ -run TestService_List_FiltersByVisibility -v`
Expected: FAIL — sales-wf still listed; Visibility/Department empty.

- [ ] **Step 7: Gate `List` in `service.go`**

Replace the shared-loop body in `List` so each shared entry is filtered through the publication and tagged with its visibility:

```go
	id2 := id // already have caller identity from caller(ctx) above
	shared, err := s.repo.List(ctx, Shared())
	if err != nil {
		return nil, err
	}
	for _, sum := range shared {
		sum.Scope = ScopeLabelShared
		// Resolve the publication once; use it for both gating and tagging.
		var pub Publication
		published := false
		if s.publishers != nil {
			if p, ok, perr := s.publishers.PublicationOf(ctx, sum.Name); perr == nil && ok {
				pub, published = p, true
			}
		}
		if published {
			if !CanSeePublication(id2, pub) {
				continue // hide department workflows from non-members
			}
			sum.Publisher = pub.Publisher.String()
			sum.Visibility = string(pub.Visibility)
			sum.Department = pub.Department
		} else {
			// Unpublished shared folder (pre-model) or local mode: treat as public.
			sum.Visibility = string(VisibilityPublic)
		}
		out = append(out, sum)
	}
	return out, nil
```

(Remove the old `PublisherOf` call in the loop; `PublicationOf` supplies the publisher.)

- [ ] **Step 8: Run to verify List tests pass**

Run: `go test ./domain/workflow/ -run TestService_List -v`
Expected: PASS (both the new gating tests and the existing `TestService_List_*` tests — those use a nil/empty publisher or default-public path).

- [ ] **Step 9: Write the failing `Load`/`Copy` gating tests**

```go
func TestService_Load_DeptWorkflow_HiddenFromOutsider_NotFound(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000c1")
	outsider := uuid.MustParse("00000000-0000-0000-0000-0000000000c2")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	_, err := svc.Load(ctxWithDepts(outsider, false, "sales"), "eng-wf")
	// MUST be ErrNotFound, not Unauthorized — do not leak existence.
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_Load_DeptWorkflow_VisibleToMember(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
	member := uuid.MustParse("00000000-0000-0000-0000-0000000000c4")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	got, err := svc.Load(ctxWithDepts(member, false, "engineering"), "eng-wf")
	require.NoError(t, err)
	assert.Equal(t, "eng-wf", got.Name)
}

func TestService_Copy_UnseeableDept_NotFound(t *testing.T) {
	repo := newFakeRepo()
	pubs := newFakePublishers()
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000c5")
	outsider := uuid.MustParse("00000000-0000-0000-0000-0000000000c6")
	repo.bucket(workflow.Shared())["eng-wf"] = wf("eng-wf")
	_ = pubs.Upsert(context.Background(), "eng-wf", publisher, "src", workflow.VisibilityDepartment, "engineering")
	svc := workflow.NewWorkflowService(repo, pubs)

	err := svc.Copy(ctxWithDepts(outsider, false, "sales"), "eng-wf", "my-copy")
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestService_Load_PrivateUnaffected(t *testing.T) {
	repo := newFakeRepo()
	owner := uuid.MustParse("00000000-0000-0000-0000-0000000000c7")
	repo.bucket(workflow.Private(owner))["mine"] = wf("mine")
	svc := workflow.NewWorkflowService(repo, newFakePublishers())

	got, err := svc.Load(ctxWithDepts(owner, false), "mine")
	require.NoError(t, err)
	assert.Equal(t, "mine", got.Name)
}
```

- [ ] **Step 10: Run to verify they fail**

Run: `go test ./domain/workflow/ -run 'TestService_(Load_Dept|Copy_Unseeable)' -v`
Expected: FAIL — `Load`/`Copy` still return the shared workflow to outsiders.

- [ ] **Step 11: Add `LoadVisibleFiltered` to `resolve.go`**

```go
// SharedVisibilityFilter reports whether the caller may see the given shared
// workflow name. A nil filter disables shared-scope gating (every shared name
// is visible) for callers that have no publication lookup.
type SharedVisibilityFilter func(ctx context.Context, name string) (bool, error)

// LoadVisibleFiltered behaves like LoadVisible but, when the resolution falls
// through to the shared scope, requires the filter to approve the name. A
// disapproved shared workflow returns ErrNotFound — never revealing that a
// department workflow exists to an outsider.
func LoadVisibleFiltered(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string, filter SharedVisibilityFilter) (*Workflow, Scope, error) {
	for _, s := range ReadScopes(owner) {
		if s.Kind == ScopeShared && filter != nil {
			ok, err := filter(ctx, name)
			if err != nil {
				return nil, Scope{}, err
			}
			if !ok {
				continue // treat as absent: do not leak existence
			}
		}
		wf, err := repo.Load(ctx, s, name)
		if err == nil {
			return wf, s, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, Scope{}, err
		}
	}
	return nil, Scope{}, ErrNotFound
}
```

Keep the existing `LoadVisible` as a thin wrapper so other callers are unaffected:

```go
func LoadVisible(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string) (*Workflow, Scope, error) {
	return LoadVisibleFiltered(ctx, repo, owner, name, nil)
}
```

- [ ] **Step 12: Gate `Load` and `Copy` in `service.go`**

Add a private helper on the service that builds the caller-bound filter:

```go
// sharedFilter returns a SharedVisibilityFilter bound to id, or nil when no
// publisher lookup is configured (local/read-only ⇒ all shared visible).
func (s *WorkflowService) sharedFilter(id identity.Identity) SharedVisibilityFilter {
	if s.publishers == nil {
		return nil
	}
	return func(ctx context.Context, name string) (bool, error) {
		return canSeeShared(ctx, s.publishers, id, name)
	}
}
```

Change `Load`:

```go
func (s *WorkflowService) Load(ctx context.Context, name string) (*Workflow, error) {
	id, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	wf, _, err := LoadVisibleFiltered(ctx, s.repo, id.UserID, name, s.sharedFilter(id))
	return wf, err
}
```

Change `Copy` to check visibility before copying:

```go
func (s *WorkflowService) Copy(ctx context.Context, sharedName, newPrivateName string) error {
	id, err := caller(ctx)
	if err != nil {
		return err
	}
	if ok, err := s.repo.Exists(ctx, Private(id.UserID), newPrivateName); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, newPrivateName)
	}
	// A user may only copy a shared workflow they can see.
	if s.publishers != nil {
		canSee, err := canSeeShared(ctx, s.publishers, id, sharedName)
		if err != nil {
			return err
		}
		if !canSee {
			return fmt.Errorf("%w: %s", ErrNotFound, sharedName)
		}
	}
	return s.repo.CopyTree(ctx, Shared(), sharedName, Private(id.UserID), newPrivateName, false)
}
```

Also gate `ListFiles` (it falls through to shared via `ListFilesVisible`). For minimal surface, change `ListFiles` to first resolve visibility for shared the same way — but since `ListFilesVisible` mirrors `LoadVisible`, add a `ListFilesVisibleFiltered` analogous to Step 11 and call it from `ListFiles` with `s.sharedFilter(id)`. (Pattern identical to `LoadVisibleFiltered`; the file probe replaces `repo.Load`.)

- [ ] **Step 13: Run to verify Load/Copy gating passes**

Run: `go test ./domain/workflow/... -v`
Expected: PASS — all gating tests green, existing tests unaffected (they use nil publishers or public visibility).

- [ ] **Step 14: Write the failing engine-trigger gating test**

Create `domain/engine/engine_trigger_visibility_test.go`. Model the engine construction on existing engine tests (find one with `go test ./domain/engine -run Trigger -list .*` and copy its harness). The new assertion:

```go
// An outsider triggering a department-only shared workflow must be rejected
// with a not-found-class error (no existence leak), even though the shared
// folder physically exists.
func TestEngine_Trigger_DepartmentWorkflow_HiddenFromOutsider(t *testing.T) {
	// ... build engine with workflows repo holding shared/"eng-wf" ...
	// ... wire a publication lookup reporting eng-wf as department "engineering" ...
	// ... ctx identity = outsider in department "sales" ...
	_, _, err := eng.Trigger(ctxOutsider, "eng-wf", nil, "normal")
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrNotFound)
}

func TestEngine_Trigger_DepartmentWorkflow_AllowedForMember(t *testing.T) {
	// ... same setup, ctx identity = member in department "engineering" ...
	_, _, err := eng.Trigger(ctxMember, "eng-wf", nil, "normal")
	require.NoError(t, err)
}
```

- [ ] **Step 15: Run to verify it fails**

Run: `go test ./domain/engine/ -run TestEngine_Trigger_DepartmentWorkflow -v`
Expected: FAIL — the engine has no publication lookup and `Trigger` uses the ungated `LoadVisible`.

- [ ] **Step 16: Wire a publication lookup into the engine and gate Trigger**

In `domain/engine/engine.go`:

- Add a field `publishers workflow.PublisherLookup` to `Engine` (nil-safe; default nil keeps today's open behavior for local mode).
- Add a setter:

```go
// SetPublisherLookup wires the publication lookup used to gate the trigger read
// path by workflow visibility. When unset (nil), all shared workflows are
// triggerable (local/read-only mode), preserving today's behavior.
func (e *Engine) SetPublisherLookup(p workflow.PublisherLookup) {
	e.publishers = p
}
```

- In `Trigger`, replace the load call. The owner is the triggering user; build the filter from the context identity (not the resolved `owner`, which may be the default system user). When there is no context identity (system trigger), skip gating — a scheduler/webhook trigger already passed authorization at registration time:

```go
	var filter workflow.SharedVisibilityFilter
	if id, ok := identity.FromContext(ctx); ok && e.publishers != nil {
		pubs := e.publishers
		filter = func(ctx context.Context, name string) (bool, error) {
			pub, found, err := pubs.PublicationOf(ctx, name)
			if err != nil {
				return false, err
			}
			if !found {
				return true, nil // unpublished shared folder ⇒ open
			}
			return workflow.CanSeePublication(id, pub), nil
		}
	}
	wf, _, err := workflow.LoadVisibleFiltered(ctx, e.workflows, owner, workflowName, filter)
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("failed to load workflow: %w", err)
	}
```

(Keep the surrounding code identical: the `%w`-wrapped error still `errors.Is`-matches `workflow.ErrNotFound`, which the test asserts.)

- [ ] **Step 17: Run the engine trigger tests**

Run: `go test ./domain/engine/ -run TestEngine_Trigger -v`
Expected: PASS.

- [ ] **Step 18: Wire `SetPublisherLookup` in `cmd/arbitron/main.go`**

Find where the engine is constructed (`NewEngine(...)`) and where `publishedRepo := adaptsqlite.NewPublishedWorkflowRepo(db)` already exists (around `main.go:327`). After the engine is built, add:

```go
	eng.SetPublisherLookup(publishedRepo)
```

(Guard with the same auth-mode condition used for other RBAC wiring if local mode must stay fully open; otherwise rely on the per-trigger "no context identity ⇒ skip" rule, which already preserves local-mode openness since local mode's fixed identity is admin and `CanSeePublication` returns true for admins.)

- [ ] **Step 19: Run the full suite**

Run: `make test`
Expected: PASS.

- [ ] **Step 20: Commit**

```bash
git add domain/workflow/gating.go domain/workflow/gating_test.go \
        domain/workflow/resolve.go domain/workflow/service.go \
        domain/workflow/service_test.go domain/workflow/workflow.go \
        domain/engine/engine.go domain/engine/engine_trigger_visibility_test.go \
        cmd/arbitron/main.go
git commit -m "feat(rbac): gate workflow read paths by visibility (canSeePublication)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Dependencies

- **Depends on:** Task 2 (`Publication`, `PublicationOf`), and **Phase 1** identity helpers (`InDepartment`, `Departments`).
- **Blocks:** Task 4 (publish/visibility gating reuses `CanSeePublication` and the publication-aware service), Task 6 (admin-override audit fires at this gate).
