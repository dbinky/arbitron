package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	adaptercrypto "github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ provider.SelectionMutationStore = (*ProfileRepo)(nil)

// Proof preparation is owned by P2T2/P3T6. This fixture supplies its exact
// transaction input, using revisions read from the actual persisted sources.
func selectionDatabase(t *testing.T) (*ProfileRepo, *sql.DB, *sql.DB, string, provider.PreparedSelection) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "selection.db")
	db, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, RunMigrations(db))
	other, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	crypto, err := adaptercrypto.NewAESGCM(testKey32(0x2A))
	require.NoError(t, err)
	conns := NewConnectionRepo(db, crypto)
	revisions := map[string]int64{}
	for _, name := range []string{"route", "override"} {
		require.NoError(t, conns.Create(context.Background(), sampleConn(name), "credential"))
		conn, err := conns.Get(context.Background(), name)
		require.NoError(t, err)
		revisions[name] = conn.ConfigRevision
	}
	var policy int64
	require.NoError(t, db.QueryRow(`SELECT revision FROM model_identity_policy WHERE id = 1`).Scan(&policy))
	p := followingProfile("worker")
	p.Model = "deepseek/deepseek-v4.1-flash"
	p.FollowTarget = &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}
	p.ConnectionRef = "route"
	p.Revision = 999 // The store, not the caller, owns revisions.
	prepared := provider.PreparedSelection{Profile: p, Candidate: &provider.EligibleCandidate{
		Model: p.Model, CanonicalID: "deepseek/deepseek-v4.1-flash", PathRevision: "prospective-proof",
		ExecutionRevision: provider.ExecutionRevision{ID: "prospective-proof", PolicyRevision: policy, Connections: revisions},
	}}
	return NewProfileRepo(db), db, other, path, prepared
}

func selectionWrite(t *testing.T, repo *ProfileRepo, prepared provider.PreparedSelection, update bool) (func() (provider.Profile, error), *provider.Profile) {
	t.Helper()
	ctx := context.Background()
	if !update {
		return func() (provider.Profile, error) { return repo.CreateValidated(ctx, prepared) }, nil
	}
	prior := prepared.Profile.Clone()
	prior.Model = "deepseek/deepseek-v4-flash"
	require.NoError(t, repo.Create(ctx, prior))
	prior, err := repo.Get(ctx, prior.Alias)
	require.NoError(t, err)
	return func() (provider.Profile, error) { return repo.UpdateValidated(ctx, prior, prepared) }, &prior
}

func assertSelectionUnchanged(t *testing.T, repo *ProfileRepo, prior *provider.Profile) {
	t.Helper()
	rows, err := repo.List(context.Background())
	require.NoError(t, err)
	if prior == nil {
		assert.Empty(t, rows)
	} else {
		assert.Equal(t, []provider.Profile{*prior}, rows)
	}
}

func TestValidatedSelectionPersistsExactModelAndRevision(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "update"}[update], func(t *testing.T) {
			repo, db, _, path, prepared := selectionDatabase(t)
			write, prior := selectionWrite(t, repo, prepared, update)
			got, err := write()
			require.NoError(t, err)
			want := prepared.Profile.Clone()
			want.Revision = 1
			if prior != nil {
				want.Revision = prior.Revision + 1
			}
			assert.Equal(t, want, got)
			assert.Zero(t, journalCount(t, testDB{DB: db}, want.Alias), "admin selection is not automatic advancement")
			require.NoError(t, db.Close())
			reopened, err := Open(path)
			require.NoError(t, err)
			defer reopened.Close()
			stored, err := NewProfileRepo(reopened).Get(context.Background(), want.Alias)
			require.NoError(t, err)
			assert.Equal(t, want, stored)
		})
	}
}

func TestValidatedSelectionRejectsExternalRevisionChange(t *testing.T) {
	for name, change := range map[string]func(*testing.T, *sql.DB){
		"policy": func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`UPDATE model_identity_policy SET revision = revision + 1 WHERE id = 1`)
			require.NoError(t, err)
		},
		"credential": func(t *testing.T, db *sql.DB) {
			crypto, err := adaptercrypto.NewAESGCM(testKey32(0x2A))
			require.NoError(t, err)
			conns := NewConnectionRepo(db, crypto)
			conn, err := conns.Get(context.Background(), "route")
			require.NoError(t, err)
			rotated := "rotated-credential"
			require.NoError(t, conns.Update(context.Background(), conn, &rotated))
		},
		"override connection": func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`UPDATE connections SET config_revision = config_revision + 1 WHERE name = 'override'`)
			require.NoError(t, err)
		},
		"deleted connection": func(t *testing.T, db *sql.DB) {
			_, err := db.Exec(`DELETE FROM connections WHERE name = 'route'`)
			require.NoError(t, err)
		},
	} {
		for _, update := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/create", true: "/update"}[update], func(t *testing.T) {
				repo, _, other, _, prepared := selectionDatabase(t)
				write, prior := selectionWrite(t, repo, prepared, update)
				change(t, other) // A separate process can bypass the in-process execution gate.
				_, err := write()
				assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
				assertSelectionUnchanged(t, repo, prior)
			})
		}
	}
}

func TestValidatedSelectionRejectsMissingOrContradictoryProof(t *testing.T) {
	for name, change := range map[string]func(*provider.PreparedSelection){
		"candidate":                 func(s *provider.PreparedSelection) { s.Candidate = nil },
		"empty profile model":       func(s *provider.PreparedSelection) { s.Profile.Model = "" },
		"empty candidate model":     func(s *provider.PreparedSelection) { s.Candidate.Model = "" },
		"different candidate model": func(s *provider.PreparedSelection) { s.Candidate.Model = "other" },
		"canonical identity":        func(s *provider.PreparedSelection) { s.Candidate.CanonicalID = "" },
		"path revision":             func(s *provider.PreparedSelection) { s.Candidate.PathRevision = "" },
		"token":                     func(s *provider.PreparedSelection) { s.Candidate.ExecutionRevision = provider.ExecutionRevision{} },
		"token id":                  func(s *provider.PreparedSelection) { s.Candidate.ExecutionRevision.ID = "" },
		"different token":           func(s *provider.PreparedSelection) { s.Candidate.ExecutionRevision.ID = "unrelated" },
		"policy revision":           func(s *provider.PreparedSelection) { s.Candidate.ExecutionRevision.PolicyRevision = 0 },
		"route omitted":             func(s *provider.PreparedSelection) { delete(s.Candidate.ExecutionRevision.Connections, "route") },
		"prospective route omitted": func(s *provider.PreparedSelection) { s.Profile.ConnectionRef = "unproved-route" },
		"no connections":            func(s *provider.PreparedSelection) { s.Candidate.ExecutionRevision.Connections = nil },
		"target":                    func(s *provider.PreparedSelection) { s.Profile.FollowTarget = nil },
	} {
		for _, update := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/create", true: "/update"}[update], func(t *testing.T) {
				repo, _, _, _, prepared := selectionDatabase(t)
				write, prior := selectionWrite(t, repo, prepared, update)
				change(&prepared)
				// Rebuild the closure to pass the mutated proof, retaining the original expected row.
				if prior == nil {
					write = func() (provider.Profile, error) { return repo.CreateValidated(context.Background(), prepared) }
				} else {
					write = func() (provider.Profile, error) { return repo.UpdateValidated(context.Background(), *prior, prepared) }
				}
				_, err := write()
				assert.Error(t, err)
				assertSelectionUnchanged(t, repo, prior)
			})
		}
	}
}

func TestValidatedSelectionUpdateComparesEveryExpectedField(t *testing.T) {
	for name, change := range map[string]func(*provider.Profile){
		"revision":   func(p *provider.Profile) { p.Revision++ },
		"model":      func(p *provider.Profile) { p.Model = "other" },
		"kind":       func(p *provider.Profile) { p.Kind = provider.ProfileKindChat },
		"connection": func(p *provider.Profile) { p.ConnectionRef = "override" },
		"backend":    func(p *provider.Profile) { p.Backend = "opencode" },
		"tools":      func(p *provider.Profile) { p.AllowedTools = []string{"Glob"} },
		"context":    func(p *provider.Profile) { p.ContextWindow = provider.Context200K },
		"effort":     func(p *provider.Profile) { p.Effort = provider.EffortLow },
		"mode":       func(p *provider.Profile) { p.SelectionMode = provider.SelectionPinned; p.FollowTarget = nil },
		"provider":   func(p *provider.Profile) { p.FollowTarget.Provider = "other" },
		"tier":       func(p *provider.Profile) { p.FollowTarget.Tier = "other" },
		"alias":      func(p *provider.Profile) { p.Alias = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			repo, _, _, _, prepared := selectionDatabase(t)
			_, prior := selectionWrite(t, repo, prepared, true)
			expected := prior.Clone()
			change(&expected)
			_, err := repo.UpdateValidated(context.Background(), expected, prepared)
			require.ErrorIs(t, err, provider.ErrAdvanceConflict)
			assertSelectionUnchanged(t, repo, prior)
		})
	}
}

func TestValidatedSelectionFaultsRollback(t *testing.T) {
	for name, fault := range map[string]string{
		"write":         `CREATE TRIGGER reject_selection BEFORE INSERT ON profiles BEGIN SELECT RAISE(ABORT, 'selection fault'); END`,
		"readback":      `CREATE TRIGGER corrupt_selection AFTER INSERT ON profiles BEGIN UPDATE profiles SET effort = 'corrupt' WHERE alias = NEW.alias; END`,
		"revision read": `DROP TABLE model_identity_policy`,
	} {
		for _, update := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/create", true: "/update"}[update], func(t *testing.T) {
				repo, db, _, _, prepared := selectionDatabase(t)
				write, prior := selectionWrite(t, repo, prepared, update)
				statement := fault
				if update && name != "revision read" {
					if name == "write" {
						statement = `CREATE TRIGGER reject_selection BEFORE UPDATE ON profiles BEGIN SELECT RAISE(ABORT, 'selection fault'); END`
					} else {
						statement = `CREATE TRIGGER corrupt_selection AFTER UPDATE ON profiles BEGIN UPDATE profiles SET effort = 'corrupt' WHERE alias = NEW.alias; END`
					}
				}
				_, err := db.Exec(statement)
				require.NoError(t, err)
				_, err = write()
				require.Error(t, err)
				assertSelectionUnchanged(t, repo, prior)
			})
		}
	}
}

func TestValidatedSelectionPreservesAliasAndExpectedRevision(t *testing.T) {
	ctx := context.Background()
	for _, action := range []string{"duplicate", "deleted", "ABA", "advanced"} {
		t.Run(action, func(t *testing.T) {
			repo, db, _, _, prepared := selectionDatabase(t)
			_, prior := selectionWrite(t, repo, prepared, true)
			switch action {
			case "duplicate":
				_, err := repo.CreateValidated(ctx, prepared)
				assert.ErrorIs(t, err, provider.ErrProfileExists)
				assertSelectionUnchanged(t, repo, prior)
			case "deleted":
				require.NoError(t, repo.Delete(ctx, prior.Alias))
				_, err := repo.UpdateValidated(ctx, *prior, prepared)
				assert.ErrorIs(t, err, provider.ErrAdvanceConflict)
				assertSelectionUnchanged(t, repo, nil)
				return
			case "ABA":
				edit := prior.Clone()
				edit.Model = "deepseek/deepseek-v5-flash"
				require.NoError(t, repo.Update(ctx, edit))
				require.NoError(t, repo.Update(ctx, *prior))
			case "advanced":
				req := advanceReq("concurrent-advance", *prior, "deepseek/deepseek-v5-flash")
				req.ExecutionRevision = prepared.Candidate.ExecutionRevision.Clone()
				_, err := repo.CommitAdvance(ctx, req)
				require.NoError(t, err)
			}
			current, err := repo.Get(ctx, prior.Alias)
			require.NoError(t, err)
			if action != "duplicate" {
				_, err = repo.UpdateValidated(ctx, *prior, prepared)
				assert.ErrorIs(t, err, provider.ErrAdvanceConflict)
			}
			assertSelectionUnchanged(t, repo, &current)
			wantJournal := 0
			if action == "advanced" {
				wantJournal = 1
			}
			assert.Equal(t, wantJournal, journalCount(t, testDB{DB: db}, prior.Alias))
		})
	}
}

func TestValidatedSelectionPinnedNeedsNoFollowPolicy(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "update"}[update], func(t *testing.T) {
			repo, db, _, _, prepared := selectionDatabase(t)
			prepared.Profile.SelectionMode = provider.SelectionPinned
			prepared.Profile.FollowTarget = nil
			prepared.Candidate = nil // Fixed-version validation above this port needs no tier policy.
			write, prior := selectionWrite(t, repo, prepared, update)
			_, err := db.Exec(`DROP TABLE model_identity_policy`)
			require.NoError(t, err)
			got, err := write()
			require.NoError(t, err)
			want := prepared.Profile.Clone()
			want.Revision = 1
			if prior != nil {
				want.Revision = prior.Revision + 1
			}
			assert.Equal(t, want, got)
			persisted, err := repo.Get(context.Background(), got.Alias)
			require.NoError(t, err)
			assert.Equal(t, want, persisted)
		})
	}
}
