package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// localSvc builds a Service wired with the LocalRegistry Null-Object — the
// single-path local-mode composition. publishers is nil (local mode has no
// publication store), matching the MCP/HTTP local compositions.
func localSvc(t *testing.T) (*workflow.Service, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	svc.SetRegistry(workflow.NewLocalRegistry(repo))
	return svc, repo
}

// nilSvc builds a Service with NO registry — the prior nil-branch local mode —
// used as the behavioral ORACLE the LocalRegistry path must match exactly.
func nilSvc(t *testing.T) (*workflow.Service, *fakeRepo) {
	t.Helper()
	repo := newFakeRepo()
	svc := workflow.NewWorkflowService(repo, nil)
	return svc, repo
}

// Area: LocalRegistry parity. Each of the 8 registry-touching ops must behave
// IDENTICALLY whether the registry is nil (prior local mode) or the LocalRegistry
// Null-Object (the new single-path local mode). This is the whole risk of the
// structural collapse: zero behavioral change in local mode.

// --- Create: plain private-folder create, no id allocation ---

func TestLocalParity_Create(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, true)

	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			require.NoError(t, svc.Create(ctx, "mine", wf("mine")))
			stored, ok := repo.bucket(workflow.Private(owner))["mine"]
			require.True(t, ok, "private folder created")
			assert.Equal(t, uuid.Nil, stored.ID, "local Create allocates NO id")
		})
	}
}

// --- Load: no id resolution (folder id preserved as-is) ---

func TestLocalParity_Load(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, false)

	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			legacy := wf("legacy")
			legacy.ID = uuid.Nil
			repo.bucket(workflow.Private(owner))["legacy"] = legacy

			loaded, err := svc.Load(ctx, "legacy")
			require.NoError(t, err)
			assert.Equal(t, uuid.Nil, loaded.ID, "local Load resolves/allocates NO id")
		})
	}
}

// --- Rename: not configured in local mode ---

func TestLocalParity_Rename_NotConfigured(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			repo.bucket(workflow.Private(owner))["a"] = wf("a")
			err := svc.Rename(ctx, "a", "b")
			assert.ErrorIs(t, err, workflow.ErrUnauthorized, "Rename is not configured in local mode")
		})
	}
}

// --- Archive / Unarchive: not configured in local mode ---

func TestLocalParity_Archive_NotConfigured(t *testing.T) {
	ctx := ctxWith(uuid.New(), true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := tc.svc(t)
			assert.ErrorIs(t, svc.Archive(ctx, "a"), workflow.ErrUnauthorized)
			assert.ErrorIs(t, svc.Unarchive(ctx, "a"), workflow.ErrUnauthorized)
		})
	}
}

// --- TransferOwnership: not configured in local mode ---

func TestLocalParity_Transfer_NotConfigured(t *testing.T) {
	ctx := ctxWith(uuid.New(), true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := tc.svc(t)
			err := svc.TransferOwnership(ctx, "a", uuid.New())
			assert.ErrorIs(t, err, workflow.ErrUnauthorized)
		})
	}
}

// --- Duplicate: not configured in local mode ---

func TestLocalParity_Duplicate_NotConfigured(t *testing.T) {
	ctx := ctxWith(uuid.New(), true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := tc.svc(t)
			err := svc.Duplicate(ctx, "a", "b")
			assert.ErrorIs(t, err, workflow.ErrUnauthorized)
		})
	}
}

// --- Publish: not configured in local mode (no published store) ---

func TestLocalParity_Publish_NotConfigured(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			repo.bucket(workflow.Private(owner))["mine"] = wf("mine")
			// publishers is nil in both compositions, so Publish reports the publishing
			// store is not configured (ErrUnauthorized) — identical in both modes.
			err := svc.Publish(ctx, "mine", "shared-mine", workflow.VisibilityPublic, "")
			assert.ErrorIs(t, err, workflow.ErrUnauthorized)
		})
	}
}

// --- BackfillRegistry: no-op in local mode ---

func TestLocalParity_BackfillRegistry_NoOp(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			legacy := wf("legacy")
			legacy.ID = uuid.Nil
			repo.bucket(workflow.Private(owner))["legacy"] = legacy

			require.NoError(t, svc.BackfillRegistry(ctx), "backfill is a no-op in local mode")
			// The folder is untouched: no id stamped (no durable registry to back it).
			assert.Equal(t, uuid.Nil, repo.bucket(workflow.Private(owner))["legacy"].ID,
				"BackfillRegistry must not stamp ids in local mode")
		})
	}
}

// --- Delete: private-folder delete, shared-only -> ErrSharedReadOnly, ghost -> ErrNotFound ---

func TestLocalParity_Delete_PrivateFolder(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			repo.bucket(workflow.Private(owner))["mine"] = wf("mine")

			require.NoError(t, svc.Delete(ctx, "mine"), "local Delete removes the private folder")
			_, still := repo.bucket(workflow.Private(owner))["mine"]
			assert.False(t, still, "private folder removed")
		})
	}
}

func TestLocalParity_Delete_SharedOnly_ReadOnly(t *testing.T) {
	owner := uuid.New()
	ctx := ctxWith(owner, true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			// A name that exists ONLY in the shared scope (no private copy).
			repo.bucket(workflow.Shared())["pub"] = wf("pub")

			err := svc.Delete(ctx, "pub")
			assert.ErrorIs(t, err, workflow.ErrSharedReadOnly,
				"deleting a shared-only name in local mode maps to ErrSharedReadOnly")
			_, still := repo.bucket(workflow.Shared())["pub"]
			assert.True(t, still, "shared folder is NOT removed by a local delete")
		})
	}
}

func TestLocalParity_Delete_Ghost_NotFound(t *testing.T) {
	ctx := ctxWith(uuid.New(), true)
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := tc.svc(t)
			err := svc.Delete(ctx, "ghost")
			assert.ErrorIs(t, err, workflow.ErrNotFound, "deleting an unknown name is ErrNotFound")
		})
	}
}

// --- Delete still admin-gated in local mode ---

func TestLocalParity_Delete_NonAdmin_Forbidden(t *testing.T) {
	owner := uuid.New()
	for _, tc := range []struct {
		name string
		svc  func(t *testing.T) (*workflow.Service, *fakeRepo)
	}{
		{"nil", nilSvc},
		{"local", localSvc},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := tc.svc(t)
			repo.bucket(workflow.Private(owner))["mine"] = wf("mine")
			err := svc.Delete(ctxWith(owner, false), "mine")
			assert.ErrorIs(t, err, workflow.ErrForbidden, "Delete is admin-gated in local mode")
			_, still := repo.bucket(workflow.Private(owner))["mine"]
			assert.True(t, still, "non-admin local delete removes nothing")
		})
	}
}
