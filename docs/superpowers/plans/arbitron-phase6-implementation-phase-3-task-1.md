# Phase 3, Task 1: `Scope` Type + Scope-Aware `WorkflowRepository` Port + Adapter Path Resolution

## Goal

Introduce a storage `Scope` value in `domain/workflow` and thread it through **every** `WorkflowRepository` port method, then make the filesystem adapter resolve directories per-scope (`users/<ownerID>/<name>` for Private, `shared/<name>` for Shared) while retaining the path-traversal guard and adding a cross-scope-escape assertion. The per-user directory is created `0o700`.

**This is a breaking port change.** This task changes the port and the adapter only (plus the adapter's own tests); it deliberately leaves every *caller* broken. Task 2 fixes all call sites. Do them back-to-back.

Source of truth: design §1 (On-Disk Layout) and §2 (Scope-Aware `WorkflowRepository` Port).

## Dependencies

- Phase 1 (Identity core): the `Scope.OwnerID` is a `uuid.UUID` that callers will derive from `identity.FromContext` in Task 2. This task only needs `github.com/google/uuid`, already vendored.
- None within Phase 3 — this is the first task.

## Background (current state)

- `domain/workflow/ports.go` defines `WorkflowRepository` with **no owner dimension**: `Load(ctx, name)`, `List(ctx)`, `SaveNodePositions(ctx, name, positions)`, `Create/Update/Delete(ctx, name, …)`, `WriteFile/DeleteFile/ListFiles(ctx, name, …)`.
- `adapter/filesystem/workflow_repo.go`:
  - `WorkflowRepo{ basePath string }`, constructed by `NewWorkflowRepo(basePath)`.
  - `resolveWorkflowDir(name)` does `filepath.Join(basePath, name)` + a clean/prefix traversal guard against `basePath`, rejecting `.`/`..`/empty with `workflow.ErrInvalidName`.
  - Every method calls `resolveWorkflowDir` then reads/writes `wfDir/workflow.json`. `Create` uses `os.MkdirAll(wfDir, 0o755)`. Atomic temp-file+rename throughout.
  - `var _ workflow.WorkflowRepository = (*WorkflowRepo)(nil)` compile assertion at the top.
- `adapter/filesystem/workspace.go` is the security precedent for per-user dirs: `instanceDirMode = 0o700`, `MkdirAll` then explicit `os.Chmod` on dir **and** parent (because `MkdirAll` honours umask). Mirror this for `users/<ownerID>/`.
- Sentinels in `domain/workflow/errors.go`: `ErrNotFound`, `ErrAlreadyExists`, `ErrInvalidName`, `ErrInvalidFilePath`, `ErrFileNotFound`.

## Files to Create/Modify

### Create: `domain/workflow/scope.go`

```go
package workflow

import "github.com/google/uuid"

// ScopeKind distinguishes a caller's private workspace from the shared
// (published) space.
type ScopeKind int

const (
	// ScopePrivate addresses basePath/users/<OwnerID>/<name>.
	ScopePrivate ScopeKind = iota
	// ScopeShared addresses basePath/shared/<name>; OwnerID is ignored.
	ScopeShared
)

// Scope selects the storage root a WorkflowRepository operation acts on.
// For ScopeShared the OwnerID field is ignored.
type Scope struct {
	Kind    ScopeKind
	OwnerID uuid.UUID
}

// Private builds a Scope addressing the given owner's private workspace.
func Private(ownerID uuid.UUID) Scope { return Scope{Kind: ScopePrivate, OwnerID: ownerID} }

// Shared is the shared/published scope.
func Shared() Scope { return Scope{Kind: ScopeShared} }
```

### Modify: `domain/workflow/ports.go`

Add `scope Scope` as the first post-`ctx` argument to every method:

```go
type WorkflowRepository interface {
	Load(ctx context.Context, scope Scope, name string) (*Workflow, error)
	List(ctx context.Context, scope Scope) ([]WorkflowSummary, error)
	SaveNodePositions(ctx context.Context, scope Scope, name string, positions map[string]NodePosition) error

	Create(ctx context.Context, scope Scope, name string, wf *Workflow) error
	Update(ctx context.Context, scope Scope, name string, wf *Workflow) error
	Delete(ctx context.Context, scope Scope, name string) error

	WriteFile(ctx context.Context, scope Scope, name string, relativePath string, content []byte) error
	DeleteFile(ctx context.Context, scope Scope, name string, relativePath string) error
	ListFiles(ctx context.Context, scope Scope, name string) ([]string, error)
}
```

Also add `Exists(ctx context.Context, scope Scope, name string) (bool, error)` — the publish/copy operations (Task 5) need an existence probe, and List should not be abused for it.

### Modify: `adapter/filesystem/workflow_repo.go`

- Add a `scopeRoot` helper that maps a `Scope` to its absolute root under `basePath`, resolving the per-user `userID` directory and creating it `0o700` on write paths:

  ```go
  // scopeRoot returns the directory that contains workflow folders for the
  // given scope: basePath/users/<ownerID> for Private, basePath/shared for
  // Shared. It does not create the directory.
  func (r *WorkflowRepo) scopeRoot(scope workflow.Scope) (string, error) {
  	switch scope.Kind {
  	case workflow.ScopePrivate:
  		if scope.OwnerID == uuid.Nil {
  			return "", fmt.Errorf("%w: private scope requires owner", workflow.ErrInvalidName)
  		}
  		return filepath.Join(r.basePath, "users", scope.OwnerID.String()), nil
  	case workflow.ScopeShared:
  		return filepath.Join(r.basePath, "shared"), nil
  	default:
  		return "", fmt.Errorf("%w: unknown scope kind", workflow.ErrInvalidName)
  	}
  }
  ```

- Make `resolveWorkflowDir` scope-aware. The traversal guard now anchors on the **scope root**, not `basePath`, so a cross-scope escape (`name="../../shared/x"` from a private scope) is also rejected:

  ```go
  func (r *WorkflowRepo) resolveWorkflowDir(scope workflow.Scope, name string) (string, error) {
  	scopeRoot, err := r.scopeRoot(scope)
  	if err != nil {
  		return "", err
  	}
  	root := filepath.Clean(scopeRoot) + string(os.PathSeparator)
  	wfDir := filepath.Join(scopeRoot, name)
  	if wfDir == filepath.Clean(scopeRoot) ||
  		!strings.HasPrefix(wfDir+string(os.PathSeparator), root) {
  		return "", fmt.Errorf("%w: %s", workflow.ErrInvalidName, name)
  	}
  	return wfDir, nil
  }
  ```

- Add a per-user-dir creator mirroring `workspace.go` (umask-safe `Chmod`), used by `Create` and the file-write paths:

  ```go
  const userScopeDirMode os.FileMode = 0o700

  // ensureScopeRoot creates the scope root dir (and, for private scopes, the
  // parent users/ dir) with 0o700 perms, chmodding to defeat umask relaxation,
  // matching adapter/filesystem/workspace.go's security precedent.
  func (r *WorkflowRepo) ensureScopeRoot(scope workflow.Scope) (string, error) {
  	scopeRoot, err := r.scopeRoot(scope)
  	if err != nil {
  		return "", err
  	}
  	if err := os.MkdirAll(scopeRoot, userScopeDirMode); err != nil {
  		return "", fmt.Errorf("creating scope root: %w", err)
  	}
  	if scope.Kind == workflow.ScopePrivate {
  		// Tighten the per-user dir and its users/ parent (MkdirAll honours umask).
  		if err := os.Chmod(filepath.Dir(scopeRoot), userScopeDirMode); err != nil {
  			return "", fmt.Errorf("tighten users dir: %w", err)
  		}
  		if err := os.Chmod(scopeRoot, userScopeDirMode); err != nil {
  			return "", fmt.Errorf("tighten user dir: %w", err)
  		}
  	}
  	return scopeRoot, nil
  }
  ```

- Thread `scope` through **every** method signature and `resolveWorkflowDir` call. `Create` calls `ensureScopeRoot(scope)` before `os.MkdirAll(wfDir, …)` (keep the workflow dir itself `0o755` — only the user partition root is `0o700`, matching the workspace precedent where the per-instance dir is the protected boundary). `List` reads `scopeRoot` via `os.ReadDir`; a non-existent scope root (user never created a private workflow) must return an **empty slice, not an error** — guard `os.IsNotExist`. Add `uuid` to imports.

- Implement `Exists`:

  ```go
  func (r *WorkflowRepo) Exists(_ context.Context, scope workflow.Scope, name string) (bool, error) {
  	wfDir, err := r.resolveWorkflowDir(scope, name)
  	if err != nil {
  		return false, err
  	}
  	_, statErr := os.Stat(filepath.Join(wfDir, "workflow.json"))
  	if statErr == nil {
  		return true, nil
  	}
  	if os.IsNotExist(statErr) {
  		return false, nil
  	}
  	return false, fmt.Errorf("checking workflow %q: %w", name, statErr)
  }
  ```

- Keep the per-file relative-path traversal guard in `WriteFile`/`DeleteFile` exactly as-is (it anchors on `wfDir`, which is now scope-correct).

### Note: `FileReader` (`adapter/filesystem/file_reader.go`) is handled in Task 2

`engine.FileReader.ReadFile(workflowName, relativePath)` has no scope. Task 2 introduces the scope-aware read path (private-then-shared) it needs; leave `file_reader.go` untouched here.

## Testing (strict TDD — five scenario categories)

Write/adjust `adapter/filesystem/workflow_repo_test.go` and a new `domain/workflow/scope_test.go` **first**. Adapter tests use a `t.TempDir()` basePath. Drive each method through both a `Private(uuid)` and `Shared()` scope.

- **Happy path:** `Create` then `Load` round-trips a workflow under `users/<id>/<name>/workflow.json` for Private and `shared/<name>/workflow.json` for Shared; `List(Private(a))` returns only `a`'s folders; `Exists` is true after create, false before.
- **Validation:** Private scope with `OwnerID == uuid.Nil` is rejected (`ErrInvalidName`); unknown `ScopeKind` rejected; empty/`.`/`..` names rejected in both scopes.
- **Error/failure:** `Load`/`Update`/`Delete` of a missing name returns `ErrNotFound`; `Create` over an existing name returns `ErrAlreadyExists`; `List` against a never-created private scope root returns `([]WorkflowSummary{}, nil)` (no error) — assert via `os.IsNotExist` guard.
- **Boundary:** the per-user directory `users/<id>` is mode `0o700` and its `users/` parent is `0o700` after `Create` (stat the modes, mirroring `workspace_test.go`); the workflow dir itself is `0o755`.
- **Isolation/security:** `name="../<otherID>/secret"` under `Private(a)` is rejected (cannot reach another user's partition); `name="../shared/x"` under a Private scope is rejected; `name="../users/<a>/x"` under `Shared()` is rejected — assert the resolved path never escapes the scope root.
