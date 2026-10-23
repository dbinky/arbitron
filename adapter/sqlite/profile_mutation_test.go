package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileMutationExpectedFields(t *testing.T) {
	for name, change := range map[string]func(*provider.Profile){
		"revision":   func(p *provider.Profile) { p.Revision++ },
		"model":      func(p *provider.Profile) { p.Model = "other" },
		"kind":       func(p *provider.Profile) { p.Kind = provider.ProfileKindChat },
		"connection": func(p *provider.Profile) { p.ConnectionRef = "other" },
		"backend":    func(p *provider.Profile) { p.Backend = "opencode" },
		"tools":      func(p *provider.Profile) { p.AllowedTools = []string{"Glob"} },
		"context":    func(p *provider.Profile) { p.ContextWindow = provider.Context200K },
		"effort":     func(p *provider.Profile) { p.Effort = provider.EffortLow },
		"mode":       func(p *provider.Profile) { p.SelectionMode = provider.SelectionPinned; p.FollowTarget = nil },
		"provider":   func(p *provider.Profile) { p.FollowTarget.Provider = "other" },
		"tier":       func(p *provider.Profile) { p.FollowTarget.Tier = "other" },
	} {
		for _, deleting := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/update", true: "/delete"}[deleting], func(t *testing.T) {
				repo, db := newProfileRepo(t)
				ctx := context.Background()
				require.NoError(t, repo.Create(ctx, followingProfile("worker")))
				original, err := repo.Get(ctx, "worker")
				require.NoError(t, err)
				expected := original.Clone()
				change(&expected)
				if deleting {
					err = repo.DeleteExpected(ctx, expected)
				} else {
					replacement := original.Clone()
					replacement.AllowedTools = []string{"Write"}
					_, err = repo.UpdateExpected(ctx, expected, replacement)
				}
				require.ErrorIs(t, err, provider.ErrAdvanceConflict)
				got, err := repo.Get(ctx, "worker")
				require.NoError(t, err)
				assert.Equal(t, original, got)
				assert.Zero(t, journalCount(t, db, "worker"))
			})
		}
	}
}

func TestProfileMutationAfterAdvancement(t *testing.T) {
	ctx := context.Background()
	for _, deleting := range []bool{false, true} {
		t.Run(map[bool]string{false: "update", true: "delete"}[deleting], func(t *testing.T) {
			repo, db := newProfileRepo(t)
			require.NoError(t, repo.Create(ctx, followingProfile("worker")))
			old, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			_, err = repo.CommitAdvance(ctx, advanceReq("advance", old, "vendor/version-y"))
			require.NoError(t, err)
			current, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			replacement := old.Clone()
			replacement.AllowedTools = []string{"Glob"}
			if deleting {
				err = repo.DeleteExpected(ctx, old)
			} else {
				_, err = repo.UpdateExpected(ctx, old, replacement)
			}
			require.ErrorIs(t, err, provider.ErrAdvanceConflict)
			got, err := repo.Get(ctx, "worker")
			require.NoError(t, err)
			assert.Equal(t, current, got)
			assert.Equal(t, 1, journalCount(t, db, "worker"))
			if deleting {
				require.NoError(t, repo.DeleteExpected(ctx, current))
				require.ErrorIs(t, repo.DeleteExpected(ctx, current), provider.ErrAdvanceConflict)
			} else {
				replacement = current.Clone()
				replacement.AllowedTools = []string{"Glob"}
				replacement.Revision = 999
				stored, err := repo.UpdateExpected(ctx, current, replacement)
				require.NoError(t, err)
				assert.Equal(t, current.Revision+1, stored.Revision)
				assert.Equal(t, current.Model, stored.Model)
				assert.Equal(t, []string{"Glob"}, stored.AllowedTools)
				got, err := repo.Get(ctx, "worker")
				require.NoError(t, err)
				assert.Equal(t, stored, got)
			}
		})
	}
}

func TestProfileMutationABAAndFaults(t *testing.T) {
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	require.NoError(t, repo.Create(ctx, followingProfile("worker")))
	old, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	edit := old.Clone()
	edit.Model = "away"
	require.NoError(t, repo.Update(ctx, edit))
	require.NoError(t, repo.Update(ctx, old))
	_, err = repo.UpdateExpected(ctx, old, old)
	require.ErrorIs(t, err, provider.ErrAdvanceConflict)
	require.ErrorIs(t, repo.DeleteExpected(ctx, old), provider.ErrAdvanceConflict)
	current, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	renamed := current.Clone()
	renamed.Alias = "renamed"
	_, err = repo.UpdateExpected(ctx, current, renamed)
	require.Error(t, err)
	_, err = db.DB.Exec(`CREATE TRIGGER reject_profile_update BEFORE UPDATE ON profiles BEGIN SELECT RAISE(ABORT, 'write fault'); END`)
	require.NoError(t, err)
	_, err = repo.UpdateExpected(ctx, current, current)
	require.ErrorContains(t, err, "write fault")
	got, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, current, got)
	require.NoError(t, db.DB.Close())
	_, err = repo.UpdateExpected(ctx, current, current)
	require.Error(t, err)
	require.Error(t, repo.DeleteExpected(ctx, current))
}
