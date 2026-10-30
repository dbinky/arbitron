package main

import (
	"os"
	"path/filepath"
	"testing"

	"context"
	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLibraryWiring_ServiceReachesFilesystemLib drives the real composition
// helper: a service wired through wireLibrary writes to <workspace>/shared/_lib.
func TestLibraryWiring_ServiceReachesFilesystemLib(t *testing.T) {
	ws := t.TempDir()
	svc := workflow.NewWorkflowService(filesystem.NewWorkflowRepo(ws), nil)
	lib := wireLibrary(svc, ws)
	require.NotNil(t, lib)

	admin := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Username: "a", Role: identity.RoleAdmin,
	})
	require.NoError(t, svc.LibraryWrite(admin, workflow.Shared(), "x", []byte("body")))

	got, err := os.ReadFile(filepath.Join(ws, "shared", "_lib", "x"))
	require.NoError(t, err)
	assert.Equal(t, "body", string(got))

	user := identity.WithIdentity(context.Background(), identity.Identity{
		UserID: uuid.New(), Username: "u", Role: identity.RoleUser,
	})
	b, err := svc.LibraryRead(user, nil, "x")
	require.NoError(t, err)
	assert.Equal(t, "body", string(b))
	err = svc.LibraryWrite(user, workflow.Shared(), "y", []byte("n"))
	require.ErrorIs(t, err, workflow.ErrUnauthorized)
}
