# Phase 3, Task 2: Update ALL `WorkflowRepository` Call Sites to Pass Scope from Identity

## Goal

Repair every caller broken by Task 1's port change. Each call site resolves its `workflow.Scope` from the **context identity** (`identity.FromContext`) — never from an edge-supplied parameter — per the pinned cross-cutting decision (authz/scope resolution lives off the context identity). For read paths that must fall back to shared, this task introduces the private-then-shared resolution helper that the HTTP/MCP handlers and the scope-aware `FileReader` will use. This task keeps the build/tests green; the richer service-level rules (list-merges-shared, edit-shared-rejected, admin override) land in Task 3.

Source of truth: design §3 (Workflow Service Scope Resolution), §7 (Surfaces).

## Dependencies

- **Task 1** (port + adapter now take `Scope`).
- **Phase 1** (`domain/identity`): `identity.FromContext(ctx) (Identity, bool)` / `identity.MustFromContext(ctx)` and `Identity.UserID`. The auth middleware (Phase 1) guarantees an identity is present on every request reaching these handlers.

## Background (current state — the full broken-caller list)

The Task 1 port change breaks **every** of these. They MUST all be updated:

- `adapter/http/workflow_handler.go` — `WorkflowHandler` holds `repo workflow.WorkflowRepository` and calls `repo.List/Load/Create/Update/Delete/SaveNodePositions/ListFiles/WriteFile/DeleteFile` with `r.Context()`. `ReadFile` goes through `h.fileReader.ReadFile(name, filePath)` (no scope today).
- `adapter/mcp/tools_workflow.go` — `srv.deps.Workflows.{List,Load,Create,Update,Delete,ListFiles,WriteFile,DeleteFile}(ctx, …)` plus `srv.deps.FileReader.ReadFile(name, path)`.
- `domain/engine/engine.go` — `e.workflows.Load(ctx, workflowName)` at line 108 in `Trigger`. (Trigger's full scope-resolution lands in Task 7; here just make it compile by resolving the triggering user's scope.)
- `adapter/filesystem/file_reader.go` — `engine.FileReader.ReadFile(workflowName, relativePath)` is scope-blind; the HTTP `ReadFile` and MCP `workflow_read_file` handlers depend on it.
- `cmd/arbitron/main.go` — constructs `filesystem.NewWorkflowRepo`, `filesystem.NewFileReader`, `engine.NewEngine`; wires `WorkflowHandler`/MCP `Deps`. No signature change required if the resolver is internal, but the new scope-aware FileReader constructor (below) is wired here.

There is **no `WorkflowService` yet** — `adapter/http/workflow_handler.go`'s local `WorkflowService` interface only declares `Trigger`. The handlers call the repo directly. Task 3 inserts the service; this task keeps handlers calling the repo but with a scope resolved inline so the diff in Task 3 is a clean extraction.

## Files to Create/Modify

### Create: `domain/workflow/resolve.go` — read-path scope resolution

A pure helper (no infra) used by read call sites to try private-then-shared:

```go
package workflow

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ReadScopes returns the ordered scopes a read should try for the given owner:
// the owner's private space first, then shared.
func ReadScopes(owner uuid.UUID) []Scope {
	return []Scope{Private(owner), Shared()}
}

// LoadVisible loads a workflow by name from the caller's visible scopes
// (private first, then shared), returning the first hit and the scope it
// came from. ErrNotFound if neither has it.
func LoadVisible(ctx context.Context, repo WorkflowRepository, owner uuid.UUID, name string) (*Workflow, Scope, error) {
	for _, s := range ReadScopes(owner) {
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

(Task 3 calls `LoadVisible` from the service. This task can use it directly from handlers as an interim step, then Task 3 moves the calls behind the service — keeping this commit compiling on its own.)

### Modify: `adapter/http/workflow_handler.go`

- Add a small helper to pull the caller's private scope from context:

  ```go
  func callerPrivate(ctx context.Context) workflow.Scope {
  	id := identity.MustFromContext(ctx) // middleware guarantees presence
  	return workflow.Private(id.UserID)
  }
  ```

- Read endpoints (`Get`, `Validate`, `ListFiles`, `List`): use the visible (private-then-shared) read path.
  - `List`: call `repo.List(ctx, workflow.Private(id.UserID))` here (the merged private+shared list is Task 3's job; for now keep behavior to private-only so this commit is self-consistent, with a `// TODO(task-3): merge shared` marker).
  - `Get`/`Validate`: `workflow.LoadVisible(r.Context(), h.repo, id.UserID, name)`.
  - `ListFiles`: try `Private` then `Shared` (mirror `LoadVisible` for `ListFiles`, or add a `ListFilesVisible` sibling in `resolve.go`).
- Write endpoints (`Create`, `Update`, `Delete`, `SaveLayout`, `WriteFile`, `DeleteFile`): always `callerPrivate(ctx)`.
- `ReadFile`: now needs a scope-aware reader (see FileReader change below) — pass the caller's `UserID` so the reader tries private-then-shared.
- Add `"github.com/arbitron/arbitron/domain/identity"` import.

### Modify: `domain/engine/ports.go` + `adapter/filesystem/file_reader.go` — scope-aware `FileReader`

`engine.FileReader.ReadFile(workflowName, relativePath)` must learn the owner so it can search private-then-shared. Minimal-ripple change:

```go
// FileReader provides access to workflow-associated files on disk.
type FileReader interface {
	ReadFile(owner uuid.UUID, workflowName, relativePath string) ([]byte, error)
}
```

- `adapter/filesystem/file_reader.go`: `NewFileReader(basePath)` keeps the same constructor; `ReadFile(owner, workflowName, relativePath)` builds candidate roots `basePath/users/<owner>/<wf>` then `basePath/shared/<wf>`, runs the existing two-level `safeJoin` containment per candidate, and returns the first existing file (`ErrFileNotFound` / `ErrNotFound` if neither). Preserve both containment checks (workspace-root escape + workflow-dir escape) per candidate root.
- Update all `FileReader.ReadFile` callers to pass the owner: HTTP `ReadFile`, MCP `workflow_read_file`, and any engine executor that reads files. The script/LLM/agent executors in `domain/engine` read files during a *run*; they should use the **instance's `OwnerID`** (already on `instance.Instance`) — thread it from the run context. Grep `\.ReadFile(` across `domain/engine` and `adapter/process` and fix each.

### Modify: `adapter/mcp/tools_workflow.go`

- Same pattern: add an MCP-side `callerPrivate(ctx)` / `callerOwner(ctx)` helper reading `identity.MustFromContext`. (The Phase 1 middleware also wraps `/mcp`, so identity is present.)
- Reads (`workflow_list`, `workflow_get`, `workflow_validate`, `workflow_list_files`, `workflow_read_file`): visible/private-then-shared. Writes (`workflow_create/update/delete/write_file/delete_file`): `Private(owner)`.

### Modify: `domain/engine/engine.go` (`Trigger`, line 108)

For now, resolve the triggering user from context and load private-then-shared so the snapshot still works. Full run-shared semantics (owner = triggering user already holds) are finalized in Task 7. Interim:

```go
id, ok := identity.FromContext(ctx)
owner := id.UserID
if !ok { // background/system trigger fallback
	owner, _ = e.users.GetDefaultUser(ctx)
}
wf, scope, err := workflow.LoadVisible(ctx, e.workflows, owner, workflowName)
```

Keep `ownerID` assignment from the **triggering** user (replace the unconditional `GetDefaultUser` for the OwnerID with `owner` when an identity is present; retain `GetDefaultUser` fallback). `scope` is carried into Task 7.

### Modify: `cmd/arbitron/main.go`

- No constructor signature changes if resolution stays internal, but update the `FileReader` wiring if its interface/owner arg changed (it did). Confirm `Deps.FileReader` / `RouterDeps.FileReader` still satisfy the new `engine.FileReader`.

## Testing (strict TDD — five scenario categories)

Update the existing handler/MCP/engine/file-reader tests **first** to the new signatures, then make them pass. Tests inject an identity into the request context (`identity.WithIdentity(ctx, identity.Identity{UserID: u})`).

- **Happy path:** `GET /workflows/{name}` returns a workflow the caller owns privately; a shared-only workflow is also returned (private-then-shared fallback); `POST /workflows` writes under the caller's private scope.
- **Validation:** a request with no identity in context panics via `MustFromContext` in handler tests is *not* exercised (middleware guarantees it) — instead assert the engine `Trigger` background path uses the default-user fallback when `FromContext` returns `ok=false`.
- **Error/failure:** `Get` of a name that exists in neither private nor shared returns 404 (`ErrNotFound` from `LoadVisible`); a non-`ErrNotFound` adapter error from the private probe is surfaced (not masked by the shared fallback).
- **Boundary:** `FileReader.ReadFile` resolves a file present only in `shared/<wf>` when the private copy is absent, and prefers the private file when both exist.
- **Isolation/security:** user B's `GET`/`PUT` against user A's private workflow name resolves to B's *own* private scope (and shared) — never A's private partition; write endpoints can never target another user's `users/<A>` dir.
