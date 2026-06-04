# RBAC Phase 2 — Task 2: `Visibility` Type + Port Widening (`Publication`, `PublicationOf`, `Upsert`)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce the `workflow.Visibility` type and a `Publication` value, widen the `PublisherLookup`/`PublishedStore` domain ports to expose visibility+department (`PublicationOf` + a 5-arg `Upsert`), and update the SQLite adapter compile assertions and call sites — all without changing any gating behavior yet.

**Architecture:** Pure domain-port surface change. After this task the domain knows a publication's visibility and department; gating (Task 3) and role-checked publishing (Task 4) build on it. The existing `Publish`/`Copy`/`List` behavior is preserved by passing through `VisibilityPublic`/`""` where a value is newly required, so the suite stays green.

**Tech Stack:** Go, `domain/workflow`, `github.com/google/uuid`. The SQLite adapter from Task 1 already exposes `PublicationOf` and the 5-arg `Upsert`.

**Cross-phase dependency:** None for this task. (Role gating in Task 4 consumes Phase 1's `identity.Identity` helpers; this task only touches the publication ports.)

---

## Context you need

- `domain/workflow/service.go` defines `PublisherLookup` (`PublisherOf`) and `PublishedStore` (`PublisherLookup` + `Upsert(ctx, name, publisher, sourceName)` + `Delete`). `NewWorkflowService` type-asserts the lookup to `PublishedStore` to enable publishing.
- `domain/workflow/service_test.go` has `fakePublishers` implementing `PublisherOf/Upsert/Delete` — this fake MUST be updated to the new signatures or the whole `workflow_test` package fails to compile.
- `adapter/sqlite/published_workflow_repo.go` (post Task 1) has `PublicationOf(ctx, name) (*PublishedWorkflow, bool, error)` and `Upsert(ctx, name, publisher, sourceName, visibility, department)`.
- The compile assertion `var _ workflow.PublisherLookup = (*PublishedWorkflowRepo)(nil)` lives at the top of the repo file.

## Files

- Create: `domain/workflow/visibility.go` (the `Visibility` type + `Publication` struct + constants)
- Modify: `domain/workflow/service.go` (`PublisherLookup`/`PublishedStore` port definitions only — call sites move in later tasks)
- Modify: `adapter/sqlite/published_workflow_repo.go` (add `PublicationOf` to satisfy the widened port; update compile assertion)
- Test: `domain/workflow/visibility_test.go` (new)
- Test: `domain/workflow/service_test.go` (update `fakePublishers`)

---

## Steps

- [ ] **Step 1: Write the failing visibility-type test**

Create `domain/workflow/visibility_test.go`:

```go
package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
)

func TestVisibility_Constants(t *testing.T) {
	assert.Equal(t, workflow.Visibility("private"), workflow.VisibilityPrivate)
	assert.Equal(t, workflow.Visibility("department"), workflow.VisibilityDepartment)
	assert.Equal(t, workflow.Visibility("public"), workflow.VisibilityPublic)
}

func TestVisibility_Valid(t *testing.T) {
	assert.True(t, workflow.VisibilityDepartment.Valid())
	assert.True(t, workflow.VisibilityPublic.Valid())
	// private is a "no row" sentinel, not a stored publication level.
	assert.False(t, workflow.VisibilityPrivate.Valid())
	assert.False(t, workflow.Visibility("bogus").Valid())
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/workflow/ -run TestVisibility -v`
Expected: FAIL — `workflow.Visibility` undefined.

- [ ] **Step 3: Create the `Visibility` type and `Publication` struct**

Create `domain/workflow/visibility.go`:

```go
package workflow

import "github.com/google/uuid"

// Visibility is a published workflow's sharing level. Note that VisibilityPrivate
// is never persisted — a workflow with no publication record is private to its
// owner. Only VisibilityDepartment and VisibilityPublic appear as stored rows.
type Visibility string

const (
	VisibilityPrivate    Visibility = "private"
	VisibilityDepartment Visibility = "department"
	VisibilityPublic     Visibility = "public"
)

// Valid reports whether v is a storable publication level (department or
// public). private is excluded: it is the absence of a publication, not a row.
func (v Visibility) Valid() bool {
	return v == VisibilityDepartment || v == VisibilityPublic
}

// Publication is the domain view of a shared workflow's publication record: who
// published it, the private source it came from, and at what visibility level.
// Department is empty for public. SourceName is carried so SetVisibility (Task 4)
// can re-Upsert the record (source_name is NOT NULL) without re-reading it.
type Publication struct {
	Publisher  uuid.UUID
	SourceName string
	Visibility Visibility
	Department string // "" for public
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./domain/workflow/ -run TestVisibility -v`
Expected: PASS.

- [ ] **Step 5: Write the failing port-shape test**

A small test that pins the widened port shape via the fake. Append to `domain/workflow/visibility_test.go`:

```go
func TestPublishedStore_PortShape(t *testing.T) {
	// Compile-time guarantee that fakePublishers satisfies the widened port.
	var _ workflow.PublishedStore = newFakePublishers()

	p := newFakePublishers()
	require.NoError(t, p.Upsert(nil, "wf", defaultPublisher, "src", workflow.VisibilityPublic, ""))
	pub, ok, err := p.PublicationOf(nil, "wf")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, workflow.VisibilityPublic, pub.Visibility)
}
```

Add the imports and a shared `defaultPublisher` at the top of `visibility_test.go`:

```go
import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var defaultPublisher = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./domain/workflow/ -run TestPublishedStore_PortShape -v`
Expected: FAIL — `PublicationOf` not on `PublisherLookup`, `Upsert` arity mismatch, `fakePublishers` does not satisfy `PublishedStore`.

- [ ] **Step 7: Widen the ports in `service.go`**

In `domain/workflow/service.go`, replace the `PublisherLookup` and `PublishedStore` interface definitions with:

```go
// PublisherLookup resolves the publication record for a shared workflow name,
// including its visibility level. Backed by the published_workflows repo. A nil
// lookup yields no publications (local/read-only mode treats shared as public).
type PublisherLookup interface {
	// PublisherOf returns just the publisher id; retained for callers that only
	// need attribution. (false, nil) when the name is not published.
	PublisherOf(ctx context.Context, sharedName string) (uuid.UUID, bool, error)
	// PublicationOf returns the full publication (publisher + visibility +
	// department). (nil-value, false, nil) when the name is not published.
	PublicationOf(ctx context.Context, sharedName string) (Publication, bool, error)
}

// PublishedStore is the read+write authority for the shared workspace's
// publication records, used by Publish/SetVisibility. A WorkflowService
// constructed with only a PublisherLookup (no write side) supports List/Load
// but rejects Publish.
type PublishedStore interface {
	PublisherLookup
	Upsert(ctx context.Context, name string, publisher uuid.UUID, sourceName string, visibility Visibility, department string) error
	Delete(ctx context.Context, name string) error
}
```

Note: `PublicationOf` here returns a `Publication` **value** (not a pointer), distinct from the SQLite repo's `*PublishedWorkflow`. The adapter bridges the two in Step 9.

- [ ] **Step 8: Update `fakePublishers` to the widened port**

In `domain/workflow/service_test.go`, change the `fakePublishers` storage to keep full publications and implement both new methods. Replace the `pub map[string]uuid.UUID` field and the `PublisherOf`/`Upsert` methods:

```go
type fakePublishers struct {
	pub map[string]workflow.Publication
	// upsertErr, when set, makes Upsert fail so the Publish compensation paths
	// are observable.
	upsertErr error
}

func newFakePublishers() *fakePublishers {
	return &fakePublishers{pub: map[string]workflow.Publication{}}
}

func (p *fakePublishers) PublisherOf(_ context.Context, name string) (uuid.UUID, bool, error) {
	pub, ok := p.pub[name]
	return pub.Publisher, ok, nil
}

func (p *fakePublishers) PublicationOf(_ context.Context, name string) (workflow.Publication, bool, error) {
	pub, ok := p.pub[name]
	return pub, ok, nil
}

func (p *fakePublishers) Upsert(_ context.Context, name string, publisher uuid.UUID, sourceName string, visibility workflow.Visibility, department string) error {
	if p.upsertErr != nil {
		return p.upsertErr
	}
	p.pub[name] = workflow.Publication{Publisher: publisher, SourceName: sourceName, Visibility: visibility, Department: department}
	return nil
}

func (p *fakePublishers) Delete(_ context.Context, name string) error {
	delete(p.pub, name)
	return nil
}
```

Then fix existing assertions in `service_test.go` that read `pubs.PublisherOf(...)` for the recorded publisher — they keep working (still return the publisher uuid). No other change needed for the existing publish tests since they pass `VisibilityPublic` implicitly via the service (Task 4 adds the explicit-visibility tests).

- [ ] **Step 9: Bridge the SQLite adapter to the value-returning `PublicationOf`**

The repo currently returns `*PublishedWorkflow` from `PublicationOf`. The domain port wants `(workflow.Publication, bool, error)`. Add a thin port-shaped method on the repo and keep the struct-returning one renamed. In `adapter/sqlite/published_workflow_repo.go`:

- Rename the Task-1 `PublicationOf` (the `*PublishedWorkflow` one) to `GetPublication` (internal struct accessor).
- Add the port-shaped method:

```go
// PublicationOf implements workflow.PublisherLookup. It maps the stored row to
// the domain Publication value. (zero, false, nil) when the name is unpublished.
func (r *PublishedWorkflowRepo) PublicationOf(ctx context.Context, name string) (workflow.Publication, bool, error) {
	row, ok, err := r.GetPublication(ctx, name)
	if err != nil || !ok {
		return workflow.Publication{}, ok, err
	}
	return workflow.Publication{
		Publisher:  row.PublisherID,
		SourceName: row.SourceName,
		Visibility: workflow.Visibility(row.Visibility),
		Department: row.Department,
	}, true, nil
}
```

Update the compile assertion near the top of the file to assert the full store port (it is the authority):

```go
var _ workflow.PublishedStore = (*PublishedWorkflowRepo)(nil)
```

The repo's `Upsert` already has the 5-arg shape from Task 1 but takes `visibility string`; change its signature to accept `workflow.Visibility` so it satisfies the port exactly:

```go
func (r *PublishedWorkflowRepo) Upsert(ctx context.Context, name string, publisher uuid.UUID, sourceName string, visibility workflow.Visibility, department string) error {
	// ... store string(visibility) in the generated param ...
		Visibility: string(visibility),
```

Update any Task-1 test call sites in `published_workflow_repo_test.go` that passed a bare string to pass `workflow.VisibilityPublic` / `workflow.VisibilityDepartment` (import `domain/workflow`). The CHECK-constraint test (`TestPublishedWorkflowRepo_UpsertRejectsBadVisibility`) should pass `workflow.Visibility("private")`.

- [ ] **Step 10: Run the affected suites to verify they pass**

Run: `go test ./domain/workflow/... ./adapter/sqlite/... -v`
Expected: PASS. The widened ports compile, the fake satisfies `PublishedStore`, and the migration/round-trip tests still pass.

- [ ] **Step 11: Commit**

```bash
git add domain/workflow/visibility.go domain/workflow/visibility_test.go \
        domain/workflow/service.go domain/workflow/service_test.go \
        adapter/sqlite/published_workflow_repo.go \
        adapter/sqlite/published_workflow_repo_test.go
git commit -m "feat(rbac): add workflow.Visibility + widen publication ports

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Dependencies

- **Depends on:** Task 1 (the SQLite columns + `GetPublication` accessor).
- **Blocks:** Task 3 (`canSeePublication` consumes `Publication`) and Task 4 (`Publish`/`SetVisibility` consume the 5-arg `Upsert`).
