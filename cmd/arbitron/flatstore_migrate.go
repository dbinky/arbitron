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

// reservedRoots are top-level workspace dirs that are NOT flat workflows and
// must never be moved.
var reservedRoots = map[string]bool{
	"users":     true,
	"shared":    true,
	"instances": true,
}

// migrateFlatStore relocates pre-existing top-level workflow folders under
// basePath into basePath/users/<defaultUser>/, so an upgraded install's flat
// store becomes the default user's private workspace with zero data loss. It is
// idempotent: a marker file short-circuits subsequent runs, and an empty or
// already-structured store is a no-op (the marker is still written so we never
// re-scan on every boot). A folder counts as a workflow iff it contains a
// workflow.json. Each workflow folder is moved as a unit via os.Rename so a
// crash between renames leaves whole folders, never split files.
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
			// Collision (partial prior run): skip to avoid clobbering. Surface so
			// an operator can resolve, rather than silently dropping data.
			return fmt.Errorf("flatstore migration conflict: %s already exists under users/", e.Name())
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("move %q into default user workspace: %w", e.Name(), err)
		}
	}

	// Write the marker even when nothing moved, so an empty store is a permanent
	// no-op and we never re-scan on every boot.
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		return fmt.Errorf("ensure workspace root: %w", err)
	}
	if err := os.WriteFile(markerPath, []byte("1\n"), 0o600); err != nil {
		return fmt.Errorf("write flatstore marker: %w", err)
	}
	return nil
}
