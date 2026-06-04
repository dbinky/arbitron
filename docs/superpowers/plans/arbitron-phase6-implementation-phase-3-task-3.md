# Phase 3, Task 3: `WorkflowService` Scope-Resolution Logic (create/edit/delete/list/load)

## Goal

Introduce a thin `WorkflowService` in `domain/workflow` that owns all scope/authorization decisions off the context identity, so handlers and MCP tools stop resolving scope inline (Task 2's interim) and instead delegate. Encodes the pinned rules: create/edit/delete → `Private{caller}`; editing a **shared** workflow → domain error; `List` → caller's private **plus all shared**, each tagged with scope and (for shared) publisher; `Load`/read → private-then-shared, with admins able to load any user's private by explicit owner.

Source of truth: design §3 (Workflow Service Scope Resolution).

## Dependencies

- **Task 1** (`Scope`, scope-aware port, `Exists`).
- **Task 2** (callers route through scope; `LoadVisible` helper exists; scope-aware `FileReader`).
- **Phase 1** (`identity.FromContext`, `Identity.IsAdmin()`).
- Publisher tagging for shared list entries reads `published_workflows` — but that repo lands in **Task 4**. This task defines the `PublisherLookup` port it consumes and a nil-safe default so Task 3 can land before Task 4; Task 5 wires the real lookup. (Sequence note: if you prefer, land Task 4 before Task 3 and inject the real lookup directly. Either order compiles given the port.)

## Background (current state)

- No service layer exists for workflows. `adapter/http/workflow_handler.go` declares a local `WorkflowService` interface with only `Trigger`. Handlers call `repo` directly (Task 2 left scope resolution inline with `// TODO(task-3)` markers, notably `List` being private-only).
- `WorkflowSummary` (`domain/workflow/workflow.go:121`) is `{Name, Description, Author, NodeCount}` — no scope/publisher fields yet.
- `identity.Identity` has `UserID uuid.UUID`, `Role`, `IsAdmin() bool`.

## Files to Create/Modify

### Modify: `domain/workflow/workflow.go` — tag summaries with scope + publisher

```go
type WorkflowSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	NodeCount   int    `json:"nodeCount"`
	// Scope is "private" or "shared"; lets the dashboard/MCP distinguish.
	Scope string `json:"scope"`
	// Publisher is the publishing user's id for shared entries; empty for private.
	Publisher string `json:"publisher,omitempty"`
}
```

Add string constants `ScopeLabelPrivate = "private"`, `ScopeLabelShared = "shared"`.

### Create: `domain/workflow/service.go`

```go
package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
)

// ErrSharedReadOnly is returned when a caller attempts to mutate a workflow in
// the shared scope (publish in Task 5 is the only write path to shared).
var ErrSharedReadOnly = errors.New("shared workflows are read-only; copy to edit or re-publish")

// PublisherLookup resolves the publisher id for a shared workflow name. Backed
// by the published_workflows repo (Task 4). A nil lookup yields empty publishers.
type PublisherLookup interface {
	PublisherOf(ctx context.Context, sharedName string) (uuid.UUID, bool, error)
}

// WorkflowService resolves storage scope and authorization from the context
// identity, per the Phase 1 decision that authz lives in domain services.
type WorkflowService struct {
	repo       WorkflowRepository
	publishers PublisherLookup // may be nil
}

func NewWorkflowService(repo WorkflowRepository, publishers PublisherLookup) *WorkflowService {
	return &WorkflowService{repo: repo, publishers: publishers}
}

func caller(ctx context.Context) (identity.Identity, error) {
	id, ok := identity.FromContext(ctx)
	if !ok {
		return identity.Identity{}, fmt.Errorf("%w: no identity in context", ErrInvalidName)
	}
	return id, nil
}
```

Methods:

```go
// Create/Update/Delete always target the caller's private scope.
func (s *WorkflowService) Create(ctx context.Context, name string, wf *Workflow) error
func (s *WorkflowService) Update(ctx context.Context, name string, wf *Workflow) error
func (s *WorkflowService) Delete(ctx context.Context, name string) error
func (s *WorkflowService) SaveNodePositions(ctx context.Context, name string, p map[string]NodePosition) error
func (s *WorkflowService) WriteFile(ctx context.Context, name, rel string, content []byte) error
func (s *WorkflowService) DeleteFile(ctx context.Context, name, rel string) error
```

- Each resolves `id, err := caller(ctx)` then `s.repo.X(ctx, Private(id.UserID), …)`.
- Edit-shared rejection is structural: write methods *never* address `Shared()`, so attempting to edit a shared-only workflow simply hits "not found in private" → return `ErrSharedReadOnly` when `Exists(ctx, Shared(), name)` is true but the private one is absent (so the error is informative, not a bare 404). Implement `Update`/`SaveNodePositions`/`WriteFile`/`DeleteFile` to translate `ErrNotFound` into `ErrSharedReadOnly` iff the name exists in shared.

```go
// Load tries private(caller) then shared.
func (s *WorkflowService) Load(ctx context.Context, name string) (*Workflow, error)

// LoadAs lets an admin load a specific user's private workflow by explicit
// owner (Phase 4 admin views). Non-admins get ErrUnauthorized for owner != self.
func (s *WorkflowService) LoadAs(ctx context.Context, owner uuid.UUID, name string) (*Workflow, error)

// ListFiles tries private then shared (read path).
func (s *WorkflowService) ListFiles(ctx context.Context, name string) ([]string, error)
```

- `Load` delegates to `LoadVisible(ctx, s.repo, id.UserID, name)`.
- `LoadAs`: if `owner == id.UserID` → private; else require `id.IsAdmin()` (else `ErrUnauthorized`, a new sentinel in `errors.go`) → `repo.Load(ctx, Private(owner), name)`.

```go
// List returns the caller's private workflows plus all shared, tagged.
func (s *WorkflowService) List(ctx context.Context) ([]WorkflowSummary, error)
```

- Load `repo.List(ctx, Private(id.UserID))`, set each `Scope = ScopeLabelPrivate`.
- Load `repo.List(ctx, Shared())`, set `Scope = ScopeLabelShared` and, if `s.publishers != nil`, fill `Publisher` from `PublisherOf`.
- Concatenate (private first). De-dup is **not** required — a private and shared workflow may share a name and both appear (distinguished by `Scope`).

### Modify: `domain/workflow/errors.go`

Add `ErrUnauthorized = errors.New("not authorized")` alongside the existing sentinels.

### Modify: `adapter/http/workflow_handler.go` and `adapter/mcp/tools_workflow.go`

- Replace the Task-2 inline scope resolution with calls to `*WorkflowService`. The handler's existing local `WorkflowService` interface (which only had `Trigger`) is **renamed** to `TriggerService` to avoid a name clash; the handler gains a `wfSvc *workflow.WorkflowService` (or a consumer interface) for CRUD/list/load. Map `ErrSharedReadOnly` → 409 `CONFLICT` and `ErrUnauthorized` → 403 `FORBIDDEN` in the HTTP error switch; the MCP handlers return descriptive `NewToolResultError`.
- `List` now surfaces `scope`/`publisher` in the JSON automatically via the enriched `WorkflowSummary`.

### Modify: `cmd/arbitron/main.go`

- Construct `workflowSvc := workflow.NewWorkflowService(workflowRepo, publisherLookup)` (publisherLookup nil until Task 4/5 wires the real one) and inject into the HTTP `RouterDeps` and MCP `Deps`.

## Testing (strict TDD — five scenario categories)

New `domain/workflow/service_test.go` with a fake `WorkflowRepository` and a fake `PublisherLookup`; inject identities via `identity.WithIdentity`.

- **Happy path:** `Create`/`Update`/`Delete` target `Private{caller}`; `List` returns caller's private (scope=private) concatenated with all shared (scope=shared, publisher filled); `Load` returns the private copy when present.
- **Validation:** no-identity context → `caller` returns `ErrInvalidName`-wrapped error from every method; a private+shared name collision yields **two** list entries with distinct `Scope` labels.
- **Error/failure:** `Update` of a name that exists only in shared returns `ErrSharedReadOnly` (and *not* `ErrNotFound`); `Update` of a name that exists nowhere returns `ErrNotFound`.
- **Boundary:** `List` with an empty private scope (user has no private workflows) returns just the shared set; with `publishers == nil`, shared entries have empty `Publisher` and no error.
- **Isolation/security:** non-admin `LoadAs(otherOwner, name)` returns `ErrUnauthorized`; admin `LoadAs(otherOwner, name)` loads that owner's private workflow; `Load` for user B never returns user A's private-only workflow (only B's private + shared).
