package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRelocationIntents_FilenameKeyedScoping(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	j := NewRevisionJournal(base)
	owner := workflow.Private(uuid.New())
	healthy, err := j.BeginRelocation(ctx, owner, "a", workflow.Shared(), "b", uuid.New())
	require.NoError(t, err)
	damaged, err := j.BeginRelocation(ctx, owner, "c", owner, "d", uuid.New())
	require.NoError(t, err)
	dir := filepath.Join(base, ".arbitron-layout-journal", "relocations")
	require.FileExists(t, filepath.Join(dir, relocationFilename(healthy)))
	require.Len(t, relocationKey(owner, "a"), 32)
	require.Equal(t, relocationKey(workflow.Shared(), "b"), relocationKey(workflow.Scope{Kind: workflow.ScopeShared, OwnerID: uuid.New()}, "b"))
	require.NotEqual(t, relocationKey(owner, "a"), relocationKey(workflow.Private(uuid.New()), "a"))
	damagedPath := filepath.Join(dir, relocationFilename(damaged))
	require.NoError(t, os.WriteFile(damagedPath, []byte(`{"ID":`), 0600))
	// Durable-write temporaries and operator directories are not intents.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".write-123.tmp"), []byte("partial"), 0600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "quarantine"), 0700))

	for _, endpoint := range []struct {
		s workflow.Scope
		n string
	}{{owner, "a"}, {workflow.Shared(), "b"}} {
		got, err := j.RelocationIntentsAt(ctx, endpoint.s, endpoint.n)
		require.NoError(t, err, "an unrelated damaged body is never read")
		require.Equal(t, []workflow.RelocationIntent{healthy}, got)
	}
	got, err := j.RelocationIntentsAt(ctx, owner, "unrelated")
	require.NoError(t, err)
	require.Empty(t, got)
	for _, name := range []string{"c", "d"} {
		_, err := j.RelocationIntentsAt(ctx, owner, name)
		require.ErrorIs(t, err, workflow.ErrMutationPending, "damaged body still blocks endpoint %s", name)
		var local *workflow.DestinationError
		require.ErrorAs(t, err, &local)
		require.Equal(t, damaged.ID, local.Record)
		require.Equal(t, []workflow.MutationTarget{{Scope: owner, Name: name}}, local.Targets)
	}
	all, err := j.RelocationIntents(ctx)
	require.Equal(t, []workflow.RelocationIntent{healthy}, all)
	var local *workflow.DestinationError
	require.ErrorAs(t, err, &local)
	require.Equal(t, damaged.ID, local.Record)
	require.FileExists(t, damagedPath, "unreadable intents are left in place")

	// A body whose endpoints disagree with its filename keys is damage too.
	swapped, err := os.ReadFile(filepath.Join(dir, relocationFilename(healthy)))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(damagedPath, swapped, 0600))
	_, err = j.RelocationIntentsAt(ctx, owner, "c")
	require.ErrorIs(t, err, workflow.ErrMutationPending)

	require.NoError(t, j.EndRelocation(ctx, healthy.ID))
	require.NoFileExists(t, filepath.Join(dir, relocationFilename(healthy)))
}

func TestRelocationIntents_UnscopedFilenameFailsClosedGlobally(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{uuid.NewString() + ".json", "garbage.json", "notes.txt", uuid.NewString() + ".zz.zz.json"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			j := NewRevisionJournal(base)
			owner := workflow.Private(uuid.New())
			intent, err := j.BeginRelocation(ctx, owner, "a", owner, "b", uuid.New())
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(base, ".arbitron-layout-journal", "relocations", name), []byte("{}"), 0600))
			_, err = j.RelocationIntentsAt(ctx, owner, "unrelated")
			require.Error(t, err)
			var local *workflow.DestinationError
			require.False(t, errors.As(err, &local), "an unscoped record is systemic")
			all, err := j.RelocationIntents(ctx)
			require.Error(t, err)
			require.False(t, errors.As(err, &local))
			require.Equal(t, []workflow.RelocationIntent{intent}, all)
			// Ending a known intent still works beside the unscoped record.
			require.NoError(t, j.EndRelocation(ctx, intent.ID))
		})
	}
}
