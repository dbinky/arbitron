# Phase 3, Task 5: `Publish` + `Copy` Operations + REST Routes + MCP Tools

## Goal

Implement the two cross-scope folder operations on `WorkflowService`: `Publish(privateName, publishedName)` (copy `users/<caller>/<privateName>` → `shared/<publishedName>` atomically, enforce publisher-only-overwrite via `published_workflows`) and `Copy(sharedName, newPrivateName)` (copy `shared/<sharedName>` → `users/<caller>/<newPrivateName>`, fully independent). Surface both via REST (`POST /api/v1/workflows/{name}/publish`, `.../copy`) and MCP (`workflow_publish`, `workflow_copy`).

Source of truth: design §5 (Publish/Copy Operations), §7 (Surfaces).

## Dependencies

- **Task 1** (`Scope`, `Exists`), **Task 3** (`WorkflowService`, `ErrSharedReadOnly`/`ErrUnauthorized`), **Task 4** (`published_workflows` repo + `PublisherLookup`).

## Background (current state)

- The filesystem adapter (`adapter/filesystem/workflow_repo.go`) stores each workflow as a **folder** (`workflow.json` + sibling files). Publish/copy duplicate the whole folder — folders are the unit of distribution (design §0, preserving the future `.arb` path). There is currently **no folder-copy primitive**; the adapter only has per-file/manifest operations.
- `WorkflowRepo.scopeRoot`/`resolveWorkflowDir` (Task 1) already resolve `users/<id>/<name>` and `shared/<name>` and create the per-user root `0o700`.
- The adapter's atomic pattern is temp-file + `os.Rename`. The folder copy must extend this to **temp-dir + rename** so a published folder never appears half-written. `os.Rename` is atomic within the same filesystem; both temp and final live under `basePath`, so this holds.
- `published_workflows` (Task 4) is the authority for unique-name + publisher-only-overwrite. The shared folder is the artifact; the row is the record.
- Re-publishing overwrites the shared folder. **Running/queued instances are UNAFFECTED** because the engine snapshots the definition at trigger time (`engine.go:146-153` marshals `wf` into `WorkflowSnapshot`). No special handling — call this out in the plan so no one adds defensive copies.

## Files to Create/Modify

### Modify: `domain/workflow/ports.go` — add a folder-copy primitive

```go
// CopyTree atomically copies an entire workflow folder from src scope/name to
// dst scope/name (temp dir + rename). It fails if dst already exists unless
// overwrite is true, in which case it replaces dst atomically.
CopyTree(ctx context.Context, src Scope, srcName string, dst Scope, dstName string, overwrite bool) error
```

### Modify: `adapter/filesystem/workflow_repo.go` — implement `CopyTree`

```go
func (r *WorkflowRepo) CopyTree(_ context.Context, src workflow.Scope, srcName string, dst workflow.Scope, dstName string, overwrite bool) error {
	srcDir, err := r.resolveWorkflowDir(src, srcName)        // scope+traversal guarded
	if err != nil {
		return err
	}
	if ok, _ := exists(filepath.Join(srcDir, "workflow.json")); !ok {
		return fmt.Errorf("%w: %s", workflow.ErrNotFound, srcName)
	}
	if _, err := r.ensureScopeRoot(dst); err != nil {        // creates users/<id> 0o700 or shared/
		return err
	}
	dstDir, err := r.resolveWorkflowDir(dst, dstName)
	if err != nil {
		return err
	}
	if ok, _ := exists(dstDir); ok && !overwrite {
		return fmt.Errorf("%w: %s", workflow.ErrAlreadyExists, dstName)
	}

	// Stage into a temp dir under the same scope root, then rename into place.
	staging := dstDir + ".tmp-" + uuid.NewString()
	if err := copyDirRecursive(srcDir, staging); err != nil { // 0o755 dirs, 0o644 files
		_ = os.RemoveAll(staging)
		return fmt.Errorf("staging copy of %q: %w", srcName, err)
	}
	if overwrite {
		// Atomic-ish replace: rename old aside, move new in, drop old.
		backup := dstDir + ".old-" + uuid.NewString()
		if ok, _ := exists(dstDir); ok {
			if err := os.Rename(dstDir, backup); err != nil {
				_ = os.RemoveAll(staging)
				return fmt.Errorf("swap aside %q: %w", dstName, err)
			}
		}
		if err := os.Rename(staging, dstDir); err != nil {
			_ = os.Rename(backup, dstDir) // best-effort restore
			_ = os.RemoveAll(staging)
			return fmt.Errorf("commit %q: %w", dstName, err)
		}
		_ = os.RemoveAll(backup)
		return nil
	}
	if err := os.Rename(staging, dstDir); err != nil {
		_ = os.RemoveAll(staging)
		return fmt.Errorf("commit %q: %w", dstName, err)
	}
	return nil
}
```

Add unexported `copyDirRecursive(src, dst string) error` (walk, `MkdirAll` dirs `0o755`, copy file bytes `0o644`, skip `*.tmp` artifacts) and an `exists(path) (bool, error)` helper. `CopyTree` must NOT cross into another user's partition — both endpoints go through `resolveWorkflowDir`, which already pins each side to its scope root.

### Modify: `domain/workflow/service.go` — `Publish` and `Copy`

```go
// Publish copies the caller's private workflow into the shared scope under
// publishedName and records the caller as publisher. Only the original
// publisher may overwrite an existing published name.
func (s *WorkflowService) Publish(ctx context.Context, privateName, publishedName string) error {
	id, err := caller(ctx)
	if err != nil { return err }

	// Source must exist privately.
	if ok, err := s.repo.Exists(ctx, Private(id.UserID), privateName); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, privateName)
	}

	// Publisher-only-overwrite check.
	pubID, published, err := s.publishers.PublisherOf(ctx, publishedName)
	if err != nil { return err }
	overwrite := false
	if published {
		if pubID != id.UserID {
			return fmt.Errorf("%w: published name %q owned by another user", ErrUnauthorized, publishedName)
		}
		overwrite = true // re-publish by the same publisher
	}

	if err := s.repo.CopyTree(ctx, Private(id.UserID), privateName, Shared(), publishedName, overwrite); err != nil {
		return err
	}
	// Record the publication AFTER the folder is in place. If this fails, the
	// folder is the artifact and the record is the authority — surface the error
	// (a follow-up re-publish reconciles); do not silently leave them divergent.
	return s.publisher.Upsert(ctx, publishedName, id.UserID, privateName)
}

// Copy duplicates a shared workflow into the caller's private scope. The copy
// is fully independent — no back-link to the shared original.
func (s *WorkflowService) Copy(ctx context.Context, sharedName, newPrivateName string) error {
	id, err := caller(ctx)
	if err != nil { return err }
	if ok, err := s.repo.Exists(ctx, Private(id.UserID), newPrivateName); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, newPrivateName)
	}
	// CopyTree(overwrite=false) also fails if dst exists, but the explicit check
	// yields the cleaner ErrAlreadyExists for the duplicate-private-name rule.
	return s.repo.CopyTree(ctx, Shared(), sharedName, Private(id.UserID), newPrivateName, false)
}
```

`WorkflowService` now also holds the `*PublishedWorkflowRepo` (for `Upsert`) — widen the `publishers` dependency to a `PublishedStore` interface combining `PublisherLookup` + `Upsert`/`Delete`, or add a second field. Keep `NewWorkflowService(repo, publishedStore)` where `publishedStore` satisfies both; nil-guard remains for the read-only path (`List`) but `Publish` requires it non-nil (return an error if `s.publishers == nil`).

### Modify: `adapter/http/workflow_handler.go` — `Publish` / `Copy` endpoints

```go
type publishRequest struct{ PublishedName string `json:"publishedName"` }
type copyRequest struct{ NewName string `json:"newName"` }
```

- `Publish(w, r)`: `name := chi.URLParam(r, "name")` (the private source); decode body for `publishedName`; `safeWorkflowName` both; `h.wfSvc.Publish(r.Context(), name, req.PublishedName)`. Map `ErrUnauthorized` → 403, `ErrNotFound` → 404, `ErrAlreadyExists` → 409, else 500. On success 200 `{"status":"published","name":publishedName}`.
- `Copy(w, r)`: `name` is the shared source; body `newName`; `h.wfSvc.Copy(...)`. `ErrAlreadyExists` → 409, `ErrNotFound` → 404.

### Modify: `adapter/http/server.go` — routes

Inside the `/workflows` route block (after `trigger`):

```go
r.Post("/{name}/publish", wfHandler.Publish)
r.Post("/{name}/copy", wfHandler.Copy)
```

### Modify: `adapter/mcp/tools_workflow.go` — `workflow_publish`, `workflow_copy`

Register two tools in `RegisterWorkflowTools`:

```go
gomcp.NewTool("workflow_publish",
	gomcp.WithDescription("Publish a private workflow to the shared workspace"),
	gomcp.WithString("name", gomcp.Required(), gomcp.Description("Private workflow name to publish")),
	gomcp.WithString("publishedName", gomcp.Required(), gomcp.Description("Name to publish under in the shared workspace")),
)
gomcp.NewTool("workflow_copy",
	gomcp.WithDescription("Copy a shared workflow into your private workspace"),
	gomcp.WithString("name", gomcp.Required(), gomcp.Description("Shared workflow name to copy")),
	gomcp.WithString("newName", gomcp.Required(), gomcp.Description("New private workflow name")),
)
```

Handlers mirror the create/delete handlers: validate names with `safeWorkflowName`, read `identity` from ctx (the service does the scope work), call `srv.deps.WorkflowSvc.Publish/Copy(ctx, …)`, map `ErrUnauthorized`/`ErrAlreadyExists`/`ErrNotFound` to descriptive `NewToolResultError`, success → `MarshalResult`. Add `WorkflowSvc *workflow.WorkflowService` (or a consumer interface) to `adapter/mcp/deps.go` `Deps`.

### Modify: `cmd/arbitron/main.go`

- Wire `publishedRepo` (Task 4) as the `publishedStore` into `workflow.NewWorkflowService`, and inject the service into `RouterDeps` and MCP `Deps` (already partly done in Task 3; this completes the publish path).

## Testing (strict TDD — five scenario categories)

Service tests with a real filesystem `WorkflowRepo` over `t.TempDir()` and a real (temp SQLite) `PublishedWorkflowRepo`; inject identities via context. Adapter-level `CopyTree` tests directly.

- **Happy path:** `Publish` copies the *entire folder* (manifest + sibling files) into `shared/<publishedName>` and writes a `published_workflows` row with the caller as publisher; `Copy` produces an independent `users/<caller>/<newName>` folder with all files.
- **Validation:** invalid/`..` names rejected before any filesystem mutation; `Publish` with no identity errors; `Copy` of a shared name that doesn't exist → `ErrNotFound`.
- **Error/failure:** second `Publish` of the same `publishedName` by a **different** user → `ErrUnauthorized` and the shared folder/record are unchanged; `Copy` into an existing private name → `ErrAlreadyExists`.
- **Boundary:** re-`Publish` by the **same** publisher overwrites `shared/<name>` (new file contents visible) and a pre-existing running/queued instance keeps its original `WorkflowSnapshot` (construct an instance, re-publish, assert the snapshot bytes are unchanged) — proves no special handling needed.
- **Isolation/security:** `Copy` produces a folder with NO back-link — editing the copy (`WriteFile` to the private copy) leaves `shared/<original>` byte-identical; `CopyTree` cannot be coerced (via `../` in either name) to read/write another user's `users/<other>` partition; the staging temp dir is cleaned up on a mid-copy failure (inject a copy error, assert no `*.tmp-*` residue and dst absent).
