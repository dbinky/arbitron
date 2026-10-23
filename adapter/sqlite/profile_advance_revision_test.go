package sqlite

import (
	"context"
	"testing"

	adaptercrypto "github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectedAdvance seeds a following profile routed over a stored connection
// and returns a request whose execution token matches the persisted
// revisions, as ExecutionPathInventory.Snapshot would have produced it.
func connectedAdvance(t *testing.T) (*ProfileRepo, testDB, provider.Profile, provider.AdvanceRequest) {
	t.Helper()
	ctx := context.Background()
	repo, db := newProfileRepo(t)
	crypto, err := adaptercrypto.NewAESGCM(testKey32(0x2A))
	require.NoError(t, err)
	conns := NewConnectionRepo(db.DB, crypto)
	require.NoError(t, conns.Create(ctx, sampleConn("route"), "sk-one"))
	require.NoError(t, conns.Create(ctx, sampleConn("other"), "sk-two"))
	route, err := conns.Get(ctx, "route")
	require.NoError(t, err)
	other, err := conns.Get(ctx, "other")
	require.NoError(t, err)

	p := followingProfile("worker")
	p.ConnectionRef = "route"
	require.NoError(t, repo.Create(ctx, p))
	cur, err := repo.Get(ctx, "worker")
	require.NoError(t, err)
	req := advanceReq("adv-1", cur, "vendor/version-y")
	req.ExecutionRevision.Connections = map[string]int64{"route": route.ConfigRevision, "other": other.ConfigRevision}
	return repo, db, cur, req
}

func assertNothingAdvanced(t *testing.T, repo *ProfileRepo, db testDB, cur provider.Profile) {
	t.Helper()
	got, err := repo.Get(context.Background(), cur.Alias)
	require.NoError(t, err)
	assert.Equal(t, cur, got)
	assert.Zero(t, journalCount(t, db, cur.Alias))
}

func TestCommitAdvanceAcceptsCurrentExecutionRevision(t *testing.T) {
	repo, db, cur, req := connectedAdvance(t)
	adv, err := repo.CommitAdvance(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, cur.Revision+1, adv.Revision)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
}

// A second process bumps a persisted revision after the in-process gate
// rechecked its digest: the transaction itself must catch it.
func TestCommitAdvanceRejectsRevisionBumpedOutsideTheGate(t *testing.T) {
	for name, bump := range map[string]string{
		"policy":           `UPDATE model_identity_policy SET revision = revision + 1 WHERE id = 1`,
		"route connection": `UPDATE connections SET config_revision = config_revision + 1 WHERE name = 'route'`,
		"other connection": `UPDATE connections SET config_revision = config_revision + 1 WHERE name = 'other'`,
		"deleted":          `DELETE FROM connections WHERE name = 'other'`,
	} {
		t.Run(name, func(t *testing.T) {
			repo, db, cur, req := connectedAdvance(t)
			_, err := db.DB.Exec(bump)
			require.NoError(t, err)

			_, err = repo.CommitAdvance(context.Background(), req)
			assert.ErrorIs(t, err, provider.ErrExecutionRevisionStale)
			assertNothingAdvanced(t, repo, db, cur)
		})
	}
}

func TestCommitAdvanceRejectsIncompleteExecutionRevision(t *testing.T) {
	for name, mutate := range map[string]func(*provider.ExecutionRevision){
		"empty token":              func(r *provider.ExecutionRevision) { *r = provider.ExecutionRevision{} },
		"empty id":                 func(r *provider.ExecutionRevision) { r.ID = "" },
		"missing policy revision":  func(r *provider.ExecutionRevision) { r.PolicyRevision = 0 },
		"route connection omitted": func(r *provider.ExecutionRevision) { delete(r.Connections, "route") },
		"no connections":           func(r *provider.ExecutionRevision) { r.Connections = nil },
	} {
		t.Run(name, func(t *testing.T) {
			repo, db, cur, req := connectedAdvance(t)
			mutate(&req.ExecutionRevision)

			_, err := repo.CommitAdvance(context.Background(), req)
			assert.ErrorIs(t, err, provider.ErrExecutionRevisionUnavailable)
			assertNothingAdvanced(t, repo, db, cur)
		})
	}
}

// A replay returns the committed change even after the revisions it was
// validated against have moved on: the retry is not a new advancement.
func TestCommitAdvanceReplaySurvivesLaterRevisionChange(t *testing.T) {
	repo, db, _, req := connectedAdvance(t)
	first, err := repo.CommitAdvance(context.Background(), req)
	require.NoError(t, err)
	_, err = db.DB.Exec(`UPDATE connections SET config_revision = config_revision + 1 WHERE name = 'route'`)
	require.NoError(t, err)

	again, err := repo.CommitAdvance(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, first, again)
	assert.Equal(t, 1, journalCount(t, db, "worker"))
}
