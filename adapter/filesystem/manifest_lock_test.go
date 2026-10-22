package filesystem

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

func TestManifestLocks(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	a, b := NewWorkflowRepo(base), NewWorkflowRepo(base)
	target := workflow.MutationTarget{Scope: workflow.Shared(), Name: "demo"}
	require.NoError(t, a.WithMutations(ctx, []workflow.MutationTarget{target, target}, func(owned context.Context) error {
		require.NoError(t, a.WithMutations(owned, []workflow.MutationTarget{target}, func(context.Context) error { return nil }))
		blocked, cancel := context.WithTimeout(owned, 40*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, b.WithMutations(blocked, []workflow.MutationTarget{target}, func(context.Context) error { t.Fatal("foreign ownership accepted"); return nil }), context.DeadlineExceeded)
		require.NoError(t, b.WithMutations(ctx, []workflow.MutationTarget{{Scope: target.Scope, Name: "other"}}, func(context.Context) error { return nil }))
		return nil
	}))
	boom := errors.New("callback")
	require.ErrorIs(t, a.WithMutations(ctx, []workflow.MutationTarget{target}, func(context.Context) error { return boom }), boom)
	require.Panics(t, func() {
		_ = a.WithMutations(ctx, []workflow.MutationTarget{target}, func(context.Context) error { panic("callback") })
	})
	require.NoError(t, b.WithMutations(ctx, []workflow.MutationTarget{target}, func(context.Context) error { return nil }))
	for _, name := range []string{"", ".", "..", "../escape", "a/b", ".arbitron-locks"} {
		require.Error(t, a.WithMutations(ctx, []workflow.MutationTarget{{Scope: target.Scope, Name: name}}, func(context.Context) error { t.Fatal("invalid target"); return nil }))
	}
}

func TestManifestLocks_Process(t *testing.T) {
	if base := os.Getenv("ARBITRON_LOCK_TEST"); base != "" {
		r := NewWorkflowRepo(base)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		err := r.WithMutations(ctx, []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "demo"}}, func(context.Context) error {
			return os.WriteFile(filepath.Join(base, "acquired"), []byte("yes"), 0600)
		})
		if os.Getenv("ARBITRON_LOCK_BLOCKED") == "1" {
			require.ErrorIs(t, err, context.DeadlineExceeded)
		} else {
			require.NoError(t, err)
		}
		return
	}
	base := t.TempDir()
	r := NewWorkflowRepo(base)
	run := func(blocked string) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManifestLocks_Process$")
		cmd.Env = append(os.Environ(), "ARBITRON_LOCK_TEST="+base, "ARBITRON_LOCK_BLOCKED="+blocked)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	require.NoError(t, r.WithMutations(context.Background(), []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "demo"}}, func(context.Context) error {
		run("1")
		_, err := os.Stat(filepath.Join(base, "acquired"))
		require.True(t, os.IsNotExist(err))
		return nil
	}))
	run("0")
	data, err := os.ReadFile(filepath.Join(base, "acquired"))
	require.NoError(t, err)
	require.Equal(t, "yes", string(data))
}

func TestManifestLocks_InverseCopiesAndSourceMutation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	base := t.TempDir()
	a, b := NewWorkflowRepo(base), NewWorkflowRepo(base)
	s := workflow.Shared()
	for _, name := range []string{"a", "b"} {
		require.NoError(t, a.Create(ctx, s, name, &workflow.Workflow{Name: name}))
	}
	start := make(chan struct{})
	done := make(chan error, 2)
	go func() { <-start; done <- a.CopyTree(ctx, s, "a", s, "b", true) }()
	go func() { <-start; done <- b.CopyTree(ctx, s, "b", s, "a", true) }()
	close(start)
	for range 2 {
		require.NoError(t, <-done)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		done <- a.WithMutations(ctx, []workflow.MutationTarget{{Scope: s, Name: "a"}, {Scope: s, Name: "b"}}, func(owned context.Context) error {
			return a.CopyTreeStamped(owned, s, "a", s, "b", true, func(*workflow.Workflow) error {
				close(entered)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	blocked, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	require.ErrorIs(t, b.Update(blocked, s, "a", &workflow.Workflow{Name: "mutated"}), context.DeadlineExceeded)
	stop()
	close(release)
	require.NoError(t, <-done)
}

func TestManifestLocks_NoExpansionAndExpiredContext(t *testing.T) {
	r := NewWorkflowRepo(t.TempDir())
	target := workflow.MutationTarget{Scope: workflow.Shared(), Name: "z"}
	var old context.Context
	require.Error(t, r.WithMutations(context.Background(), []workflow.MutationTarget{target}, func(ctx context.Context) error {
		old = ctx
		return r.WithMutations(ctx, []workflow.MutationTarget{{Scope: target.Scope, Name: "a"}}, func(context.Context) error { t.Fatal("expanded owned lock set"); return nil })
	}))
	require.Error(t, r.WithMutations(old, []workflow.MutationTarget{target}, func(context.Context) error { t.Fatal("expired ownership reused"); return nil }))
}
