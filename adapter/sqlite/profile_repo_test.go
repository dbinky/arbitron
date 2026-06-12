package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newProfileRepo(t *testing.T) (*ProfileRepo, testDB) {
	t.Helper()
	db := setupTestDB(t)
	return NewProfileRepo(db.DB), db
}

func sampleProfile(alias string) provider.Profile {
	return provider.Profile{
		Alias:         alias,
		Kind:          provider.ProfileKindAgent,
		Backend:       "claude-code",
		Model:         "opus-4.8",
		AllowedTools:  []string{"Read", "Bash"},
		ContextWindow: provider.Context1M,
		Effort:        provider.EffortHigh,
	}
}

// ---------------------------------------------------------------------------
// Migration: the profiles table exists after RunMigrations.
// ---------------------------------------------------------------------------

func TestProfilesMigration_TableExists(t *testing.T) {
	db := setupTestDB(t)
	var count int
	require.NoError(t, db.DB.QueryRow("SELECT COUNT(*) FROM profiles").Scan(&count))
	assert.Equal(t, 0, count)
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

// Happy: Create then Get round-trips every field, including the new axes and the
// JSON-encoded AllowedTools.
func TestProfileRepo_CreateGet(t *testing.T) {
	repo, _ := newProfileRepo(t)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, sampleProfile("coder")))

	got, err := repo.Get(ctx, "coder")
	require.NoError(t, err)
	assert.Equal(t, "coder", got.Alias)
	assert.Equal(t, provider.ProfileKindAgent, got.Kind)
	assert.Equal(t, "claude-code", got.Backend)
	assert.Equal(t, "opus-4.8", got.Model)
	assert.Equal(t, []string{"Read", "Bash"}, got.AllowedTools)
	assert.Equal(t, provider.Context1M, got.ContextWindow)
	assert.Equal(t, provider.EffortHigh, got.Effort)
}

// ---------------------------------------------------------------------------
// Success
// ---------------------------------------------------------------------------

// Success: Update overwrites the mutable fields by alias.
func TestProfileRepo_Update(t *testing.T) {
	repo, _ := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("p1")))

	upd := sampleProfile("p1")
	upd.Model = "sonnet-4.7"
	upd.ContextWindow = provider.Context200K
	upd.Effort = provider.EffortLow
	upd.AllowedTools = []string{"Read"}
	require.NoError(t, repo.Update(ctx, upd))

	got, err := repo.Get(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, "sonnet-4.7", got.Model)
	assert.Equal(t, provider.Context200K, got.ContextWindow)
	assert.Equal(t, provider.EffortLow, got.Effort)
	assert.Equal(t, []string{"Read"}, got.AllowedTools)
}

// Success: List returns all created profiles ordered by alias.
func TestProfileRepo_List(t *testing.T) {
	repo, _ := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("bravo")))
	require.NoError(t, repo.Create(ctx, sampleProfile("alpha")))
	require.NoError(t, repo.Create(ctx, sampleProfile("charlie")))

	list, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, "alpha", list[0].Alias)
	assert.Equal(t, "bravo", list[1].Alias)
	assert.Equal(t, "charlie", list[2].Alias)
}

// Success: Delete removes the row so a subsequent Get errors.
func TestProfileRepo_Delete(t *testing.T) {
	repo, _ := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("temp")))

	require.NoError(t, repo.Delete(ctx, "temp"))

	_, err := repo.Get(ctx, "temp")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
}

// ---------------------------------------------------------------------------
// Failure
// ---------------------------------------------------------------------------

// Failure: Create with a duplicate alias returns ErrProfileExists.
func TestProfileRepo_DuplicateAlias(t *testing.T) {
	repo, _ := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("dup")))

	err := repo.Create(ctx, sampleProfile("dup"))
	assert.ErrorIs(t, err, provider.ErrProfileExists)
}

// Failure: Get on a missing alias returns ErrProfileNotFound.
func TestProfileRepo_MissingProfile(t *testing.T) {
	repo, _ := newProfileRepo(t)
	_, err := repo.Get(context.Background(), "ghost")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
}

// ---------------------------------------------------------------------------
// Error
// ---------------------------------------------------------------------------

// Error: Update on a missing alias returns ErrProfileNotFound.
func TestProfileRepo_UpdateMissing(t *testing.T) {
	repo, _ := newProfileRepo(t)
	err := repo.Update(context.Background(), sampleProfile("ghost"))
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
}

// Error: Delete on a missing alias returns ErrProfileNotFound.
func TestProfileRepo_DeleteMissing(t *testing.T) {
	repo, _ := newProfileRepo(t)
	err := repo.Delete(context.Background(), "ghost")
	assert.ErrorIs(t, err, provider.ErrProfileNotFound)
}

// Error: a malformed stored context_window is a corruption error on Get — not a
// missing-profile error and not a silently garbled struct.
func TestProfileRepo_CorruptedAxis_GetErrors(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("corrupt")))

	_, err := db.DB.Exec("UPDATE profiles SET context_window = ? WHERE alias = ?", "not-a-tier", "corrupt")
	require.NoError(t, err)

	got, err := repo.Get(ctx, "corrupt")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, provider.ErrProfileNotFound, "corruption is not a missing-profile error")
	assert.Equal(t, provider.Profile{}, got)
}

// Error: a malformed stored effort is a corruption error on Get (mirrors the
// context_window test — both axes go through rowToProfile's ParseEffort path).
func TestProfileRepo_CorruptedEffort_GetErrors(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("corrupteff")))

	_, err := db.DB.Exec("UPDATE profiles SET effort = ? WHERE alias = ?", "not-an-effort", "corrupteff")
	require.NoError(t, err)

	got, err := repo.Get(ctx, "corrupteff")
	assert.Error(t, err)
	assert.NotErrorIs(t, err, provider.ErrProfileNotFound, "effort corruption is not a missing-profile error")
	assert.Equal(t, provider.Profile{}, got)
}

// Error: a malformed stored allowed_tools JSON is a corruption error on Get.
func TestProfileRepo_CorruptedTools_GetErrors(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("badtools")))

	_, err := db.DB.Exec("UPDATE profiles SET allowed_tools = ? WHERE alias = ?", "not-json{{{", "badtools")
	require.NoError(t, err)

	_, err = repo.Get(ctx, "badtools")
	assert.Error(t, err)
}

// Error: a corrupted row encountered by List propagates the error (not swallowed,
// not silently dropped). The List loop calls rowToProfile for every row.
func TestProfileRepo_CorruptedAxis_ListErrors(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleProfile("good")))
	require.NoError(t, repo.Create(ctx, sampleProfile("corrupt")))

	_, err := db.DB.Exec("UPDATE profiles SET context_window = ? WHERE alias = ?", "not-a-tier", "corrupt")
	require.NoError(t, err)

	_, err = repo.List(ctx)
	assert.Error(t, err, "corruption in any row must cause List to return an error")
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

// Edge: a chat profile with empty axes and no connection round-trips as the
// default sentinels (empty == default), the claude-cli no-key shape.
func TestProfileRepo_DefaultAxesRoundTrip(t *testing.T) {
	repo, _ := newProfileRepo(t)
	ctx := context.Background()
	p := provider.Profile{Alias: "chat", Kind: provider.ProfileKindChat}
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.Get(ctx, "chat")
	require.NoError(t, err)
	assert.Equal(t, provider.ProfileKindChat, got.Kind)
	assert.Equal(t, "", got.ConnectionRef)
	// Empty/unset axes are stored as the 'default' literal and parse back to the
	// default enum value.
	assert.Equal(t, provider.ContextDefault, got.ContextWindow)
	assert.Equal(t, provider.EffortDefault, got.Effort)
}

// Edge: List on an empty store returns a non-nil empty slice, not nil, so JSON
// callers see [] rather than null.
func TestProfileRepo_ListEmpty(t *testing.T) {
	repo, _ := newProfileRepo(t)
	list, err := repo.List(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, list, "List must return a non-nil slice on an empty store")
	assert.Empty(t, list)
}

// Edge: empty AllowedTools round-trips as a non-nil empty slice (stored '[]').
func TestProfileRepo_EmptyToolsRoundTrip(t *testing.T) {
	repo, db := newProfileRepo(t)
	ctx := context.Background()
	p := provider.Profile{Alias: "notools", Kind: provider.ProfileKindAgent}
	require.NoError(t, repo.Create(ctx, p))

	got, err := repo.Get(ctx, "notools")
	require.NoError(t, err)
	assert.Equal(t, []string{}, got.AllowedTools)

	var stored string
	require.NoError(t, db.DB.QueryRow("SELECT allowed_tools FROM profiles WHERE alias = ?", "notools").Scan(&stored))
	assert.Equal(t, "[]", stored)
}

// Edge: Count reflects the number of rows (0 fresh, N after N creates). This
// feeds the one-time seed gate.
func TestProfileRepo_Count(t *testing.T) {
	repo, _ := newProfileRepo(t)
	ctx := context.Background()

	n, err := repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	require.NoError(t, repo.Create(ctx, sampleProfile("a")))
	require.NoError(t, repo.Create(ctx, sampleProfile("b")))
	n, err = repo.Count(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}
