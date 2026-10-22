package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

type readSettlerFunc func(context.Context, workflow.Scope, string) error

func (f readSettlerFunc) Settle(ctx context.Context, s workflow.Scope, n string) error {
	return f(ctx, s, n)
}

func TestWorkflowRepo_OperationBoundaries(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	r := NewWorkflowRepo(base)
	peer := NewWorkflowRepo(base)
	s := workflow.Shared()
	require.NoError(t, r.Create(ctx, s, "src", &workflow.Workflow{Name: "src"}))
	require.NoError(t, r.Create(ctx, s, "dst", &workflow.Workflow{Name: "dst"}))
	loaded, err := peer.Load(ctx, s, "dst")
	require.NoError(t, err)
	loaded.Description = "blocked update"
	ops := map[string]func(context.Context) error{
		"create":           func(c context.Context) error { return peer.Create(c, s, "dst", &workflow.Workflow{Name: "bad"}) },
		"files":            func(c context.Context) error { _, err := peer.ListFiles(c, s, "dst"); return err },
		"exists":           func(c context.Context) error { _, err := peer.Exists(c, s, "dst"); return err },
		"load":             func(c context.Context) error { _, err := peer.Load(c, s, "dst"); return err },
		"update":           func(c context.Context) error { return peer.Update(c, s, "dst", loaded) },
		"delete":           func(c context.Context) error { return peer.Delete(c, s, "dst") },
		"write":            func(c context.Context) error { return peer.WriteFile(c, s, "dst", "x", []byte("bad")) },
		"delete file":      func(c context.Context) error { return peer.DeleteFile(c, s, "dst", "x") },
		"copy destination": func(c context.Context) error { return peer.CopyTree(c, s, "src", s, "dst", true) },
		"copy source":      func(c context.Context) error { return peer.CopyTree(c, s, "dst", s, "other", false) },
	}
	require.NoError(t, r.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: "dst"}}, func(owned context.Context) error {
		for name, op := range ops {
			t.Run(name, func(t *testing.T) {
				c, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
				defer cancel()
				require.ErrorIs(t, op(c), context.DeadlineExceeded)
			})
		}
		require.NoError(t, r.Update(owned, s, "dst", &workflow.Workflow{Name: "final"}))
		got, err := r.Load(owned, s, "dst")
		require.NoError(t, err)
		require.Equal(t, "final", got.Name)
		return nil
	}))
	got, err := peer.Load(ctx, s, "dst")
	require.NoError(t, err)
	require.Equal(t, "final", got.Name)
}

func TestWorkflowRepo_ReadSettlement(t *testing.T) {
	ctx := context.Background()
	r := NewWorkflowRepo(t.TempDir())
	s := workflow.Shared()
	require.NoError(t, r.Create(ctx, s, "demo", &workflow.Workflow{Name: "uncommitted"}))
	calls := 0
	r.SetReadSettler(readSettlerFunc(func(owned context.Context, scope workflow.Scope, name string) error {
		calls++
		require.Equal(t, s, scope)
		require.Equal(t, "demo", name)
		_, err := r.Load(owned, scope, name)
		require.NoError(t, err)
		return workflow.ErrMutationPending
	}))
	_, err := r.Load(ctx, s, "demo")
	require.ErrorIs(t, err, workflow.ErrMutationPending)
	require.Equal(t, 1, calls)
	_, err = r.List(ctx, s)
	require.ErrorIs(t, err, workflow.ErrMutationPending)
	boom := errors.New("proof unavailable")
	r.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error { return boom }))
	_, err = r.List(ctx, s)
	require.ErrorIs(t, err, boom)
}

func TestWorkflowRepo_ReservedArtifacts(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	r := NewWorkflowRepo(base)
	s := workflow.Shared()
	require.NoError(t, r.Create(ctx, s, "demo", &workflow.Workflow{Name: "demo"}))
	for _, name := range []string{"layout.json", "./layout.json", "x/../layout.json", "./workflow.json", "layout.json/child", ".arbitron-tree-commits/child"} {
		require.ErrorIs(t, r.WriteFile(ctx, s, "demo", name, []byte("bad")), workflow.ErrInvalidFilePath)
		require.ErrorIs(t, r.DeleteFile(ctx, s, "demo", name), workflow.ErrInvalidFilePath)
	}
	require.NoError(t, os.WriteFile(filepath.Join(base, "shared", "demo", "layout.json"), []byte("cache"), 0600))
	require.NoError(t, r.WriteFile(ctx, s, "demo", "scripts/run.sh", []byte("ok")))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "shared", "demo", ".arbitron-tree-commits"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(base, "shared", "demo", ".arbitron-tree-commits", "backup"), []byte("private"), 0600))
	files, err := r.ListFiles(ctx, s, "demo")
	require.NoError(t, err)
	require.Equal(t, []string{"scripts/run.sh"}, files)
	for _, name := range []string{".arbitron-locks", ".arbitron-layout-journal", ".arbitron-tree-commits"} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, "shared", name), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(base, "shared", name, "workflow.json"), []byte(`{"name":"leak"}`), 0600))
	}
	summaries, err := r.List(ctx, s)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
}
