# Phase 3, Task 6: Startup Flat-Store Migration into `users/<default-user>/`

## Goal

Add a one-time, marker-guarded startup migration in `cmd/arbitron/main.go` that moves any pre-existing **top-level** workflow folders under `basePath/` into `basePath/users/<default-user>/`, so the local default user's existing flat store becomes their private workspace with zero data loss and no user action. Idempotent: a marker file prevents re-running; an empty/already-migrated store is a no-op.

Source of truth: design §1 (On-Disk Layout — startup migration).

## Dependencies

- **Task 1** (the `users/<id>/<name>` layout the workflows move into; the `0o700` per-user dir convention).
- **Phase 1** seeds/owns the default user. The default user id is the existing `00000000-0000-0000-0000-000000000001` (`adapter/sqlite/migrations/001_initial_schema.up.sql`), resolvable at runtime via `userRepo.GetDefaultUser(ctx)` (`adapter/sqlite/user_repo.go`).

## Background (current state)

- Today every workflow lives flat: `basePath/<workflowName>/workflow.json` (the pre-Task-1 `WorkflowRepo` resolved `filepath.Join(basePath, name)`). After Task 1, the adapter only looks under `users/<id>/` and `shared/` — so a fresh upgrade of an existing install would "lose" its workflows until they're relocated. This migration performs that relocation once.
- `cmd/arbitron/main.go` (~line 188-205): after `RunMigrations(db)` and after constructing `userRepo := adaptsqlite.NewUserRepo(db)` and `workflowRepo := filesystem.NewWorkflowRepo(cfg.Workspace.Path)`. The migration must run **after** the default user exists (DB migrations applied) and **before** the server starts serving. `cfg.Workspace.Path` is the `basePath`.
- The workspace root also contains non-workflow subdirs that must be **left alone**: `instances/` (per-instance workspaces, `adapter/filesystem/workspace.go`), and the new `users/` and `shared/` roots. The migration must skip these reserved names and only move directories that look like a flat workflow (contain a `workflow.json`).
- The `0o700`/chmod precedent for the per-user dir is `adapter/filesystem/workspace.go` and Task 1's `ensureScopeRoot`.

## Files to Create/Modify

### Create: `cmd/arbitron/flatstore_migrate.go` (same package `main`)

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// flatStoreMarker is the sentinel file recording that the one-time flat→user
// migration already ran for this workspace root.
const flatStoreMarker = ".flatstore_migrated"

// reservedRoots are top-level workspace dirs that are NOT flat workflows.
var reservedRoots = map[string]bool{
	"users":     true,
	"shared":    true,
	"instances": true,
}

// migrateFlatStore relocates pre-existing top-level workflow folders under
// basePath into basePath/users/<defaultUser>/. It is idempotent: the marker
// file short-circuits subsequent runs, and an empty/already-structured store
// is a no-op. A folder counts as a workflow iff it contains workflow.json.
func migrateFlatStore(basePath string, defaultUser uuid.UUID) error {
	markerPath := filepath.Join(basePath, flatStoreMarker)
	if _, err := os.Stat(markerPath); err == nil {
		return nil // already migrated
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat flatstore marker: %w", err)
	}

	entries, err := os.ReadDir(basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate; workspace not yet created
		}
		return fmt.Errorf("read workspace root: %w", err)
	}

	userRoot := filepath.Join(basePath, "users", defaultUser.String())
	createdUserRoot := false

	for _, e := range entries {
		if !e.IsDir() || reservedRoots[e.Name()] {
			continue
		}
		src := filepath.Join(basePath, e.Name())
		if _, err := os.Stat(filepath.Join(src, "workflow.json")); err != nil {
			continue // not a flat workflow folder; leave untouched
		}
		if !createdUserRoot {
			if err := os.MkdirAll(userRoot, 0o700); err != nil {
				return fmt.Errorf("create default user workspace: %w", err)
			}
			// Defeat umask on both users/ and users/<id> (workspace.go precedent).
			_ = os.Chmod(filepath.Dir(userRoot), 0o700)
			_ = os.Chmod(userRoot, 0o700)
			createdUserRoot = true
		}
		dst := filepath.Join(userRoot, e.Name())
		if _, err := os.Stat(dst); err == nil {
			// Collision (partial prior run): skip to avoid clobbering. Surface
			// so an operator can resolve, rather than silently dropping data.
			return fmt.Errorf("flatstore migration conflict: %s already exists under users/", e.Name())
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %q into default user workspace: %w", e.Name(), err)
		}
	}

	// Write the marker even when nothing moved, so an empty store is a
	// permanent no-op and we never re-scan on every boot.
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		return fmt.Errorf("ensure workspace root: %w", err)
	}
	if err := os.WriteFile(markerPath, []byte("1\n"), 0o600); err != nil {
		return fmt.Errorf("write flatstore marker: %w", err)
	}
	return nil
}
```

### Modify: `cmd/arbitron/main.go`

After `RunMigrations` succeeds and `userRepo`/`workflowRepo` are constructed (and before the HTTP/MCP servers start), resolve the default user and run the migration:

```go
defaultUser, err := userRepo.GetDefaultUser(context.Background())
if err != nil {
	db.Close(); logCloser.Close()
	return nil, fmt.Errorf("resolving default user for flat-store migration: %w", err)
}
if err := migrateFlatStore(cfg.Workspace.Path, defaultUser); err != nil {
	db.Close(); logCloser.Close()
	return nil, fmt.Errorf("flat-store migration: %w", err)
}
logger.Info("flat-store migration complete", "defaultUser", defaultUser)
```

(Use whichever `context` is already in scope; `GetDefaultUser` takes a `context.Context`.)

## Testing (strict TDD — five scenario categories)

Test `migrateFlatStore` directly with a `t.TempDir()` workspace (package `main` test, or extract to a tiny internal package if `main` is awkward to test — prefer testing the function in `cmd/arbitron/flatstore_migrate_test.go`).

- **Happy path:** two flat workflow folders (each with `workflow.json` + a sibling file) plus the marker absent → after migration both live under `users/<default>/<name>/` with all files intact, originals gone, marker written.
- **Validation:** a top-level directory **without** `workflow.json` (e.g. a stray `notes/`) is left in place, not moved; reserved roots `instances/`, `users/`, `shared/` are never touched.
- **Error/failure:** a name collision (a workflow folder already exists under `users/<default>/`) returns an error and does **not** clobber the existing destination (assert dst contents unchanged).
- **Boundary:** an empty workspace (or one with only reserved dirs) is a no-op that still writes the marker; running `migrateFlatStore` a **second** time short-circuits on the marker and moves nothing (idempotent).
- **Isolation/security:** the created `users/<default>/` directory and its `users/` parent are mode `0o700` (stat the modes); the marker file is `0o600`; a workflow folder is moved as a unit (no partial copy) so a crash between renames leaves whole folders, never split files.
