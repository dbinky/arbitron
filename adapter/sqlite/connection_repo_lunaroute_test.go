package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/sqlite/generated"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lrConnection(base string) provider.Connection {
	return provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: base}
}

func TestConnectionRepo_LunaRouteCreateNormalizesAndSeals(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	c := lrConnection("")
	c.Provider = " LunaRoute "
	require.NoError(t, repo.Create(ctx, c, "lr_live_one"))
	got, err := repo.Get(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lunaroute", got.Provider)
	assert.Equal(t, "https://gw.lunaroute.com", got.BaseURL)
	key, err := repo.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr_live_one", key)
	var cipher, nonce []byte
	require.NoError(t, db.DB.QueryRow("SELECT api_key_cipher, api_key_nonce FROM connections WHERE name = 'lr'").Scan(&cipher, &nonce))
	assert.NotEmpty(t, cipher)
	assert.NotEmpty(t, nonce)
	assert.NotContains(t, string(cipher), key)
}

func TestConnectionRepo_LunaRouteCreateRejectsForeignKeyAndEndpoint(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	for _, k := range []string{"sk-abc", "sk-or-v1-abc", "lr_", "lr_\u2003", "lr_one\n", "lr_one\u0085"} {
		err := repo.Create(ctx, lrConnection(""), k)
		require.ErrorIs(t, err, provider.ErrLunaRouteKeyPrefix, k)
		assert.Equal(t, provider.ErrLunaRouteKeyPrefix.Error(), err.Error(), "only the static guidance, never the submitted key")
	}
	require.ErrorIs(t, repo.Create(ctx, lrConnection(""), ""), provider.ErrLunaRouteKeyRequired)
	for _, base := range []string{"https://evil.example/v1", "https://gw.lunaroute.com:"} {
		require.ErrorIs(t, repo.Create(ctx, lrConnection(base), "lr_ok"), provider.ErrLunaRouteEndpoint)
	}
	_, err := repo.Get(ctx, "lr")
	require.ErrorIs(t, err, provider.ErrConnectionNotFound, "nothing persisted on refusal")
}

func TestConnectionRepo_LunaRouteUpdateRules(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, lrConnection(""), "lr_live_one"))
	require.NoError(t, repo.Update(ctx, lrConnection("https://gw.lunaroute.com/v1/"), nil))
	got, err := repo.Get(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "https://gw.lunaroute.com", got.BaseURL)
	key, err := repo.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr_live_one", key)
	for _, bad := range []string{"sk-or-v1-x", "lr_", "lr_\u2003", "lr_one\n"} {
		require.ErrorIs(t, repo.Update(ctx, lrConnection(""), &bad), provider.ErrLunaRouteKeyPrefix)
	}
	require.ErrorIs(t, repo.Update(ctx, lrConnection("https://gw.lunaroute.com:"), nil), provider.ErrLunaRouteEndpoint)
	key, err = repo.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr_live_one", key)
	rotated := "lr_rotated"
	require.NoError(t, repo.Update(ctx, lrConnection(""), &rotated))
	key, err = repo.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, rotated, key)
	require.ErrorIs(t, repo.Update(ctx, provider.Connection{Name: "ghost", Provider: "lunaroute"}, nil), provider.ErrConnectionNotFound)
}

func TestConnectionRepo_SwitchToLunaRouteRequiresNewKey(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, provider.Connection{Name: "or", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, "sk-or-v1-foreign"))
	err := repo.Update(ctx, provider.Connection{Name: "or", Provider: "lunaroute"}, nil)
	require.ErrorIs(t, err, provider.ErrLunaRouteKeyRequired)
	got, err := repo.Get(ctx, "or")
	require.NoError(t, err)
	assert.Equal(t, "openrouter", got.Provider, "refused switch persisted nothing")
	key, err := repo.Reveal(ctx, "or")
	require.NoError(t, err)
	assert.Equal(t, "sk-or-v1-foreign", key)
	k := "lr_switch"
	require.NoError(t, repo.Update(ctx, provider.Connection{Name: "or", Provider: "lunaroute"}, &k))
	got, err = repo.Get(ctx, "or")
	require.NoError(t, err)
	assert.Equal(t, "lunaroute", got.Provider)
	assert.Equal(t, "https://gw.lunaroute.com", got.BaseURL)
}

func TestConnectionRepo_LunaRouteMetadataUpdateIsGuardedAgainstConcurrentSwitch(t *testing.T) {
	repo, db := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, lrConnection(""), "lr_live_one"))
	other := NewConnectionRepo(db.DB, repo.crypto)
	repo.afterMetaRead = func() {
		repo.afterMetaRead = nil
		foreign := "sk-or-v1-foreign"
		require.NoError(t, other.Update(ctx, provider.Connection{Name: "lr", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}, &foreign))
	}
	err := repo.Update(ctx, lrConnection(""), nil)
	require.ErrorIs(t, err, provider.ErrConnectionChangedConcurrently)
	got, err := repo.Get(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "openrouter", got.Provider)
	key, err := repo.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "sk-or-v1-foreign", key)
}

func TestConnectionRepo_NonLunaRouteUpdateUnchanged(t *testing.T) {
	repo, _ := newConnRepo(t)
	ctx := context.Background()
	require.NoError(t, repo.Create(ctx, sampleConn("az"), "key"))
	c := sampleConn("az")
	c.BaseURL = "https://other.openai.azure.com"
	require.NoError(t, repo.Update(ctx, c, nil))
	got, err := repo.Get(ctx, "az")
	require.NoError(t, err)
	assert.Equal(t, c.BaseURL, got.BaseURL)
}

// Only writes are faulted; reads still use the real database so atomicity can
// be checked against the actual persisted provider/key pair.
type lrWriteFailure struct {
	generated.DBTX
	t   *testing.T
	err error
}

func (f lrWriteFailure) ExecContext(_ context.Context, query string, args ...interface{}) (sql.Result, error) {
	require.True(f.t, strings.Contains(query, "WHERE name = ? AND provider = ?"), "must use the conditional SQL update")
	require.Equal(f.t, []interface{}{"lunaroute", "https://gw.lunaroute.com", "{}", "lr", "lunaroute"}, args)
	return nil, f.err
}

type lrSealFailure struct {
	provider.Crypto
	t   *testing.T
	key string
	err error
}

func (f lrSealFailure) Seal(pt []byte) ([]byte, []byte, error) {
	require.Equal(f.t, f.key, string(pt))
	return nil, nil, f.err
}

func TestConnectionRepo_LunaRoutePersistenceErrors(t *testing.T) {
	ctx := context.Background()
	for _, stage := range []string{"meta read", "seal create", "seal update", "conditional SQL", "deleted"} {
		t.Run(stage, func(t *testing.T) {
			repo, db := newConnRepo(t)
			// Seed using a spelling already accepted before LunaRoute enforcement.
			require.NoError(t, repo.Create(ctx, lrConnection("https://gw.lunaroute.com"), "lr_original"))
			before, err := repo.Get(ctx, "lr")
			require.NoError(t, err)
			originalCrypto := repo.crypto
			want := errors.New("safe storage failure")
			secret := "lr_SUBMITTED_SECRET"
			c := lrConnection("https://gw.lunaroute.com/v1")
			var key *string
			switch stage {
			case "meta read":
				conn, err := db.DB.Conn(ctx)
				require.NoError(t, err)
				require.NoError(t, conn.Close())
				repo.queries = generated.New(conn)
				want = sql.ErrConnDone
				key = &secret
			case "seal create", "seal update":
				repo.crypto = lrSealFailure{Crypto: repo.crypto, t: t, key: secret, err: want}
				key = &secret
			case "conditional SQL":
				repo.queries = generated.New(lrWriteFailure{DBTX: db.DB, t: t, err: want})
			case "deleted":
				repo.afterMetaRead = func() {
					_, err := db.DB.Exec("DELETE FROM connections WHERE name = 'lr'")
					require.NoError(t, err)
				}
				want = provider.ErrConnectionNotFound
			}
			if stage == "seal create" {
				c.Name = "new"
				err = repo.Create(ctx, c, secret)
			} else {
				err = repo.Update(ctx, c, key)
			}
			require.ErrorIs(t, err, want)
			assert.NotContains(t, err.Error(), secret)
			reader := NewConnectionRepo(db.DB, originalCrypto)
			if stage == "deleted" {
				_, err := reader.Get(ctx, "lr")
				require.ErrorIs(t, err, provider.ErrConnectionNotFound)
				return
			}
			got, err := reader.Get(ctx, "lr")
			require.NoError(t, err)
			assert.Equal(t, before, got, "no partial metadata or revision write")
			revealed, err := reader.Reveal(ctx, "lr")
			require.NoError(t, err)
			assert.Equal(t, "lr_original", revealed)
			_, err = reader.Get(ctx, "new")
			require.ErrorIs(t, err, provider.ErrConnectionNotFound)
		})
	}
}

// Every accepted base-URL spelling must persist as the hosted root, observed
// through a fresh Get (the stored row), for both Create and Update.
func TestConnectionRepo_LunaRouteAcceptedSpellingsPersistHostedRoot(t *testing.T) {
	spellings := []string{
		"", "  ", "https://gw.lunaroute.com", "https://gw.lunaroute.com/", "https://gw.lunaroute.com/v1",
		"https://gw.lunaroute.com/v1/", "https://GW.LunaRoute.com/v1/", "HTTPS://gw.lunaroute.com", " https://gw.lunaroute.com ",
	}
	for _, base := range spellings {
		t.Run("create/"+base, func(t *testing.T) {
			repo, _ := newConnRepo(t)
			ctx := context.Background()
			c := lrConnection(base)
			c.Provider = "  LunaRoute "
			require.NoError(t, repo.Create(ctx, c, "lr_live_one"))
			got, err := repo.Get(ctx, "lr")
			require.NoError(t, err)
			assert.Equal(t, "lunaroute", got.Provider)
			assert.Equal(t, provider.LunaRouteHostedRoot, got.BaseURL)
		})
		t.Run("update/"+base, func(t *testing.T) {
			repo, _ := newConnRepo(t)
			ctx := context.Background()
			require.NoError(t, repo.Create(ctx, lrConnection(""), "lr_live_one"))
			require.NoError(t, repo.Update(ctx, lrConnection(base), nil))
			got, err := repo.Get(ctx, "lr")
			require.NoError(t, err)
			assert.Equal(t, "lunaroute", got.Provider)
			assert.Equal(t, provider.LunaRouteHostedRoot, got.BaseURL)
		})
	}
}
