package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openPolicyDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, RunMigrations(db))
	return db
}

func TestPolicySeedMatchesInitialRules(t *testing.T) {
	db := openPolicyDB(t, ":memory:")
	got, err := NewModelIdentityPolicyRepo(db).Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.Revision)
	assert.Equal(t, modelidentity.InitialRules(), got.Rules, "migration 029 must seed exactly the D2 table")
}

// The old shipped document is an immutable fixture, independent of InitialRules.
// Only an untouched default may be repaired automatically; an admin's saved
// policy, even if it is identical to the old seed, retains its revision/rules.
func TestIdentityPolicyMigrationExcludesDatedSnapshotsAndPreservesEdits(t *testing.T) {
	legacy, err := os.ReadFile("testdata/identity_policy_legacy.json")
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		revision int64
		rules    string
		repair   bool
	}{
		{"untouched shipped policy", 1, strings.TrimSpace(string(legacy)), true},
		{"admin saved original rules", 2, strings.TrimSpace(string(legacy)), false},
		{"custom policy", 1, `[{"providers":["anthropic"],"tier":"custom","pattern":"claude-opus-(\\d+)","version_groups":[1]}]`, false},
		{"corrupt policy", 1, `not json`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "policy.db")
			db := openPolicyDB(t, path)
			_, err := db.Exec(`UPDATE model_identity_policy SET revision=?,rules=? WHERE id=1`, tc.revision, tc.rules)
			require.NoError(t, err)
			_, err = db.Exec(`DELETE FROM schema_migrations WHERE version=32`)
			require.NoError(t, err)
			require.NoError(t, RunMigrations(db))
			var revision int64
			var rules string
			require.NoError(t, db.QueryRow(`SELECT revision,rules FROM model_identity_policy WHERE id=1`).Scan(&revision, &rules))
			if tc.repair {
				assert.Equal(t, int64(2), revision, "repair invalidates all evidence under the old policy")
				mapper, err := modelidentity.NewIdentityMapper(NewModelIdentityPolicyRepo(db))
				require.NoError(t, err)
				for _, id := range []string{"claude-opus-4-20250514", "claude-sonnet-4-0731"} {
					entry, err := mapper.Map(context.Background(), modelidentity.CatalogModel{CatalogProvider: "anthropic", NativeID: id})
					require.NoError(t, err)
					assert.Empty(t, entry.Tier)
					assert.Empty(t, entry.OrderKey)
					assert.True(t, entry.Immutable, "explicit snapshots remain pinnable")
				}
				valid, err := mapper.Map(context.Background(), modelidentity.CatalogModel{CatalogProvider: "anthropic", NativeID: "claude-opus-4-6"})
				require.NoError(t, err)
				assert.Equal(t, "opus", valid.Tier)
				assert.Equal(t, "4.6", valid.OrderKey)
				_, err = NewModelIdentityPolicyRepo(db).Replace(context.Background(), 1, modelidentity.InitialRules())
				assert.ErrorIs(t, err, modelidentity.ErrConflict, "a stale edit cannot overwrite the repaired policy")
			} else {
				assert.Equal(t, tc.revision, revision)
				assert.Equal(t, tc.rules, rules, "admin/corrupt evidence is never silently replaced")
			}
			require.NoError(t, db.Close())
			reopened := openPolicyDB(t, path)
			var afterRevision int64
			var afterRules string
			require.NoError(t, reopened.QueryRow(`SELECT revision,rules FROM model_identity_policy WHERE id=1`).Scan(&afterRevision, &afterRules))
			assert.Equal(t, revision, afterRevision)
			assert.Equal(t, rules, afterRules, "restart must not rewrite the policy a second time")
		})
	}
}

func TestIdentityPolicyReplaceRoundTripAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.db")
	db := openPolicyDB(t, path)
	repo := NewModelIdentityPolicyRepo(db)
	rules := []modelidentity.PatternRule{{Providers: []string{"anthropic"}, Tier: "opus", Pattern: `claude-opus-(\d+)`, VersionGroups: []int{1}}}

	got, err := repo.Replace(context.Background(), 1, rules)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.Revision)
	assert.Equal(t, rules, got.Rules)

	// Reopen: the edit survives and migrations do not reseed over it.
	require.NoError(t, db.Close())
	db2 := openPolicyDB(t, path)
	after, err := NewModelIdentityPolicyRepo(db2).Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), after.Revision)
	assert.Equal(t, rules, after.Rules)
}

func TestIdentityPolicyReplaceStaleRevisionConflicts(t *testing.T) {
	db := openPolicyDB(t, ":memory:")
	repo := NewModelIdentityPolicyRepo(db)
	rules := []modelidentity.PatternRule{{Providers: []string{"p"}, Tier: "t", Pattern: `m-(\d+)`, VersionGroups: []int{1}}}
	_, err := repo.Replace(context.Background(), 7, rules)
	assert.ErrorIs(t, err, modelidentity.ErrConflict)
	got, err := repo.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.Revision, "a conflicting write changes nothing")
	assert.Equal(t, modelidentity.InitialRules(), got.Rules)
}

func TestIdentityPolicyConcurrentReplacementsOneWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.db")
	db := openPolicyDB(t, path)
	repoA := NewModelIdentityPolicyRepo(db)
	db2 := openPolicyDB(t, path)
	repoB := NewModelIdentityPolicyRepo(db2)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, repo := range []*ModelIdentityPolicyRepo{repoA, repoB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = repo.Replace(context.Background(), 1, []modelidentity.PatternRule{
				{Providers: []string{"p"}, Tier: "t", Pattern: `m-(\d+)-` + string(rune('a'+i)), VersionGroups: []int{1}},
			})
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		} else {
			assert.ErrorIs(t, err, modelidentity.ErrConflict)
		}
	}
	assert.Equal(t, 1, wins, "exactly one concurrent replacement against revision 1 may succeed: %v", errs)
	got, err := repoA.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.Revision)
}

func TestIdentityPolicyFaultsPropagateAndKeepPolicy(t *testing.T) {
	db := openPolicyDB(t, ":memory:")
	repo := NewModelIdentityPolicyRepo(db)
	rules := []modelidentity.PatternRule{{Providers: []string{"p"}, Tier: "t", Pattern: `m-(\d+)`, VersionGroups: []int{1}}}

	_, err := db.Exec(`CREATE TRIGGER fail_policy BEFORE UPDATE ON model_identity_policy BEGIN SELECT RAISE(ABORT, 'boom'); END;`)
	require.NoError(t, err)
	_, err = repo.Replace(context.Background(), 1, rules)
	require.Error(t, err)
	assert.NotErrorIs(t, err, modelidentity.ErrConflict)
	_, err = db.Exec(`DROP TRIGGER fail_policy`)
	require.NoError(t, err)
	got, err := repo.Snapshot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.Revision)

	_, err = db.Exec(`UPDATE model_identity_policy SET rules = 'not json'`)
	require.NoError(t, err)
	_, err = repo.Snapshot(context.Background())
	assert.Error(t, err, "corrupt stored rules are an error, not an empty policy")

	_, err = db.Exec(`DELETE FROM model_identity_policy`)
	require.NoError(t, err)
	_, err = repo.Snapshot(context.Background())
	assert.Error(t, err, "a missing policy row is an error, not an empty policy")
	_, err = repo.Replace(context.Background(), 1, rules)
	assert.Error(t, err)
}

func TestIdentityPolicyMigrationDown(t *testing.T) {
	db := openPolicyDB(t, ":memory:")
	down, err := migrationsFS.ReadFile("migrations/029_model_identity_policy.down.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(down))
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'model_identity_policy'`).Scan(&n))
	assert.Zero(t, n)
}
