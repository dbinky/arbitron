package filesystem_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/adapter/filesystem"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowRepoCopyTreeStamped(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(map[bool]string{false: "fresh", true: "overwrite"}[overwrite]+map[bool]string{false: "-success", true: "-stamp-error"}[fail], func(t *testing.T) {
				base := t.TempDir()
				repo := filesystem.NewWorkflowRepo(base)
				ctx := context.Background()
				src, dst := workflow.Private(uuid.New()), workflow.Shared()
				source := &workflow.Workflow{Name: "source", ID: uuid.New(), Presentation: &workflow.Presentation{Revision: "source-token"}}
				require.NoError(t, repo.Create(ctx, src, "source", source))
				require.NoError(t, repo.WriteFile(ctx, src, "source", "extra.txt", []byte("source-extra")))
				old := &workflow.Workflow{Name: "destination", Description: "old"}
				if overwrite {
					require.NoError(t, repo.Create(ctx, dst, "destination", old))
					require.NoError(t, repo.WriteFile(ctx, dst, "destination", "extra.txt", []byte("old-extra")))
				}
				boom := errors.New("stamp rejected")
				calls := 0
				finalID := uuid.New()
				err := repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: src, Name: "source"}, {Scope: dst, Name: "destination"}}, func(owned context.Context) error {
					return repo.CopyTreeStamped(owned, src, "source", dst, "destination", overwrite, func(staged *workflow.Workflow) error {
						calls++
						assert.Equal(t, "destination", staged.Name)
						// No copied source token is visible while the callback is running.
						visible, err := repo.Load(owned, dst, "destination")
						if overwrite {
							require.NoError(t, err)
							assert.Equal(t, "old", visible.Description)
						} else {
							assert.ErrorIs(t, err, workflow.ErrNotFound)
						}
						staged.ID = finalID
						staged.Presentation = &workflow.Presentation{Revision: "final-token"}
						if fail {
							return boom
						}
						return nil
					})
				})
				require.Equal(t, 1, calls)
				if fail {
					require.ErrorIs(t, err, boom)
					saved, err := repo.Load(ctx, dst, "destination")
					if overwrite {
						require.NoError(t, err)
						assert.Equal(t, old, saved)
					} else {
						assert.ErrorIs(t, err, workflow.ErrNotFound)
					}
				} else {
					require.NoError(t, err)
					saved, err := repo.Load(ctx, dst, "destination")
					require.NoError(t, err)
					assert.Equal(t, finalID, saved.ID)
					assert.Equal(t, "destination", saved.Name)
					assert.Equal(t, "final-token", saved.SavedRevision())
				}
				unchanged, err := repo.Load(ctx, src, "source")
				require.NoError(t, err)
				assert.Equal(t, source, unchanged)
				if overwrite || !fail {
					b, err := os.ReadFile(filepath.Join(base, "shared", "destination", "extra.txt"))
					require.NoError(t, err)
					expected := "source-extra"
					if fail {
						expected = "old-extra"
					}
					assert.Equal(t, expected, string(b))
				}
				entries, err := os.ReadDir(filepath.Join(base, "shared"))
				require.NoError(t, err)
				for _, entry := range entries {
					assert.Equal(t, "destination", entry.Name(), "staging must not leak")
				}
			})
		}
	}
}

func TestWorkflowRepoCopyTreeStampedDoesNotHideOrdinaryDotNames(t *testing.T) {
	repo := filesystem.NewWorkflowRepo(t.TempDir())
	ctx := context.Background()
	src, dst := workflow.Private(uuid.New()), workflow.Shared()
	require.NoError(t, repo.Create(ctx, src, ".source", &workflow.Workflow{Name: ".source"}))
	require.NoError(t, repo.CopyTreeStamped(ctx, src, ".source", dst, ".destination", false, func(staged *workflow.Workflow) error {
		list, err := repo.List(ctx, dst)
		require.NoError(t, err)
		assert.Empty(t, list, "hidden staging must not enter the catalog")
		return nil
	}))
	list, err := repo.List(ctx, dst)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, ".destination", list[0].Name)
}
