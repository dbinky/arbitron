package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/provider"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectionExecutionRevision(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	conn := sampleConn("revision")
	conn.ConfigRevision = 99 // callers cannot choose the stored revision
	require.NoError(t, repo.Create(ctx, conn, "initial"))
	got, err := repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	require.Equal(t, int64(1), got.ConfigRevision)
	conn.BaseURL = "https://changed.example.com"
	require.NoError(t, repo.Update(ctx, conn, nil))
	got, err = repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, int64(2), got.ConfigRevision)
	key := "rotated"
	require.NoError(t, repo.Update(ctx, conn, &key))
	got, err = repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.ConfigRevision)
	revealed, err := repo.Reveal(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, key, revealed)
	list, err := repo.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, got.ConfigRevision, list[0].ConfigRevision)
	conn.BaseURL = "http://localhost"
	require.Error(t, repo.Update(ctx, conn, &key))
	got, err = repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.ConfigRevision, "rejected edits must not increment")
}

func TestConnectionExecutionRevisionMigrationRoundTrip(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	conn := sampleConn("legacy-revision")
	require.NoError(t, repo.Create(ctx, conn, "legacy-key"))
	// Roll back newest-first: 024 depends on the column 023 adds.
	down024, err := migrationsFS.ReadFile("migrations/028_connection_revision_floor.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down024))
	require.NoError(t, err)
	down, err := migrationsFS.ReadFile("migrations/027_execution_revisions.down.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(down))
	require.NoError(t, err)
	// Existing credential data must survive rollback and reapplication.
	key, err := repo.Reveal(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, "legacy-key", key)
	_, err = db.DB.Exec("SELECT config_revision FROM connections")
	require.Error(t, err)
	up, err := migrationsFS.ReadFile("migrations/027_execution_revisions.up.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(up))
	require.NoError(t, err)
	up024, err := migrationsFS.ReadFile("migrations/028_connection_revision_floor.up.sql")
	require.NoError(t, err)
	_, err = db.DB.Exec(string(up024))
	require.NoError(t, err)
	got, err := repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.ConfigRevision, "pre-revision connections start at one")
	_, err = db.DB.Exec("UPDATE connections SET config_revision = 0")
	require.Error(t, err)
	var initialized int
	require.NoError(t, db.DB.QueryRow("SELECT secret_initialized FROM model_state_meta WHERE id = 1").Scan(&initialized))
	assert.Zero(t, initialized)
	_, err = db.DB.Exec("UPDATE model_state_meta SET secret_initialized = 2")
	require.Error(t, err)
}

func TestConnectionExecutionRevisionConcurrentUpdates(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	conn := sampleConn("concurrent")
	require.NoError(t, repo.Create(ctx, conn, "key"))
	const updates = 12
	var wg sync.WaitGroup
	errs := make(chan error, updates)
	for i := 0; i < updates; i++ {
		wg.Go(func() { errs <- repo.Update(ctx, conn, nil) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	got, err := repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, int64(1+updates), got.ConfigRevision, "SQL increment must be atomic, not a read/modify/write")
}

// A connection deleted and recreated under the same name (typically with a
// different key) must never reuse an earlier revision: the revision is the only
// credential-change signal, so a repeat would let stale evidence pass.
func TestConnectionExecutionRevisionRecreateNeverReuses(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	conn := sampleConn("recreated")
	require.NoError(t, repo.Create(ctx, conn, "first-key"))
	key := "rotated"
	require.NoError(t, repo.Update(ctx, conn, &key))
	before, err := repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	require.Equal(t, int64(2), before.ConfigRevision)

	require.NoError(t, repo.Delete(ctx, conn.Name))
	require.NoError(t, repo.Create(ctx, conn, "second-key"))
	after, err := repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Greater(t, after.ConfigRevision, before.ConfigRevision, "a recreated connection must not reuse a prior revision")

	require.NoError(t, repo.Update(ctx, conn, nil))
	require.NoError(t, repo.Delete(ctx, conn.Name))
	require.NoError(t, repo.Create(ctx, conn, "third-key"))
	third, err := repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Greater(t, third.ConfigRevision, after.ConfigRevision+1, "repeated recreation stays monotonic")

	// A never-used name still starts at 1, and other names are unaffected.
	fresh := sampleConn("fresh")
	require.NoError(t, repo.Create(ctx, fresh, "k"))
	got, err := repo.Get(ctx, fresh.Name)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.ConfigRevision)
}

// The policy pauses after the existence read so a real delete can win before
// either UPDATE. Ignoring the affected-row count makes both cases return nil.
type revisionURLPolicyFunc func(string) error

func (f revisionURLPolicyFunc) ValidateURL(raw string) error { return f(raw) }

func TestConnectionExecutionRevisionUpdateDeletedDuringValidation(t *testing.T) {
	for _, rotate := range []bool{false, true} {
		t.Run(fmt.Sprintf("rotate=%v", rotate), func(t *testing.T) {
			repo, db := newConnRepo(t)
			ctx := context.Background()
			conn := sampleConn("deleted-during-update")
			require.NoError(t, repo.Create(ctx, conn, "original"))
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			updating := NewConnectionRepoWithPolicy(db.DB, repo.crypto, revisionURLPolicyFunc(func(raw string) error {
				close(entered)
				<-release
				return provider.StrictURLPolicy.ValidateURL(raw)
			}))
			var key *string
			if rotate {
				replacement := "replacement"
				key = &replacement
			}
			result := make(chan error, 1)
			go func() { result <- updating.Update(ctx, conn, key) }()
			<-entered
			require.NoError(t, repo.Delete(ctx, conn.Name))
			release <- struct{}{}
			require.ErrorIs(t, <-result, provider.ErrConnectionNotFound)
			require.NoError(t, repo.Create(ctx, conn, "recreated"))
			got, err := repo.Get(ctx, conn.Name)
			require.NoError(t, err)
			assert.Equal(t, int64(2), got.ConfigRevision, "unsuccessful update must not advance the deleted revision floor")
			revealed, err := repo.Reveal(ctx, conn.Name)
			require.NoError(t, err)
			assert.Equal(t, "recreated", revealed)
		})
	}
}

// Fault injection delegates real encryption/decryption unless Seal fails.
type revisionSealFault struct {
	provider.Crypto
	err error
}

func (c revisionSealFault) Seal(pt []byte) ([]byte, []byte, error) {
	if c.err != nil {
		return nil, nil, c.err
	}
	return c.Crypto.Seal(pt)
}

func TestConnectionExecutionRevisionRejectedUpdatesLeaveRowUnchanged(t *testing.T) {
	for _, fault := range []string{"provider", "url", "seal", "sql", "cancelled"} {
		t.Run(fault, func(t *testing.T) {
			repo, db := newConnRepo(t)
			ctx := context.Background()
			conn := sampleConn("rejected")
			require.NoError(t, repo.Create(ctx, conn, "original"))
			before, err := repo.queries.GetConnectionMeta(ctx, conn.Name)
			require.NoError(t, err)
			cipher, err := repo.queries.GetConnectionCipher(ctx, conn.Name)
			require.NoError(t, err)
			changed := sampleConn(conn.Name)
			changed.BaseURL = "https://changed.example.com"
			key := "replacement"
			var want error
			switch fault {
			case "provider":
				changed.Extras = nil
				want = provider.ErrAzureAPIVersionRequired
			case "url":
				changed.BaseURL = ""
				want = provider.ErrInvalidBaseURL
			case "seal":
				want = errors.New("injected seal failure")
				repo = NewConnectionRepo(db.DB, revisionSealFault{Crypto: repo.crypto, err: want})
			case "sql":
				_, err := db.DB.Exec(`CREATE TRIGGER reject_connection_update BEFORE UPDATE ON connections
     BEGIN SELECT RAISE(ABORT, 'injected update failure'); END`)
				require.NoError(t, err)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
				want = context.Canceled
			}
			err = repo.Update(ctx, changed, &key)
			require.Error(t, err)
			if want != nil {
				require.ErrorIs(t, err, want)
			}
			after, err := repo.queries.GetConnectionMeta(context.Background(), conn.Name)
			require.NoError(t, err)
			assert.Equal(t, before, after, "rejected update must preserve metadata and revision")
			afterCipher, err := repo.queries.GetConnectionCipher(context.Background(), conn.Name)
			require.NoError(t, err)
			assert.Equal(t, cipher, afterCipher, "rejected update must preserve exact ciphertext and nonce")
		})
	}
}

func TestConnectionExecutionRevisionConcurrentMixedUpdates(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	conn := sampleConn("mixed")
	require.NoError(t, repo.Create(ctx, conn, "original"))
	const updates = 12
	start := make(chan struct{})
	errs := make(chan error, updates)
	var wg sync.WaitGroup
	for i := 0; i < updates; i++ {
		wg.Go(func() {
			<-start
			if i%2 == 0 {
				errs <- repo.Update(ctx, conn, nil)
				return
			}
			// All credential writes use the same target, so every legal ordering has
			// one final key and metadata-only writes must never replace it.
			key := "rotated"
			errs <- repo.Update(ctx, conn, &key)
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	got, err := repo.Get(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, int64(13), got.ConfigRevision)
	key, err := repo.Reveal(ctx, conn.Name)
	require.NoError(t, err)
	assert.Equal(t, "rotated", key)
}
