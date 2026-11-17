package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"

	adaptercrypto "github.com/arbitron/arbitron/adapter/crypto"
	"github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/google/uuid"
	gomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnectionCreate_LunaRouteStoredEcho(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, sqlite.RunMigrations(db))
	crypto, err := adaptercrypto.NewAESGCM(bytes.Repeat([]byte{0x31}, 32))
	require.NoError(t, err)
	repo := sqlite.NewConnectionRepo(db, crypto)
	var logs bytes.Buffer
	srv := NewServerWithDeps("test", "test", &Deps{
		ConnectionAdmin: provider.NewConnectionService(repo, nil),
		Logger:          slog.New(slog.NewTextHandler(&logs, nil)),
	})
	ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
	req := gomcp.CallToolRequest{}
	req.Params.Arguments = map[string]any{"name": "lr", "provider": " LunaRoute ", "base_url": "", "api_key": "lr_ECHO_SECRET"}
	res, err := connectionCreateHandler(srv)(ctx, req)
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	var response struct {
		Connection connectionMeta `json:"connection"`
	}
	require.NoError(t, json.Unmarshal([]byte(resultText(t, res)), &response))
	assert.Equal(t, "https://gw.lunaroute.com", response.Connection.BaseURL)
	assert.Equal(t, "lunaroute", response.Connection.Provider)
	assert.NotContains(t, resultText(t, res), "lr_ECHO_SECRET")
	assert.NotContains(t, logs.String(), "lr_ECHO_SECRET")
	key, err := repo.Reveal(ctx, "lr")
	require.NoError(t, err)
	assert.Equal(t, "lr_ECHO_SECRET", key)
}

// Inject only a safe Seal failure; all metadata reads/writes use real SQLite.
type lunaRouteSealError struct {
	provider.Crypto
	t   *testing.T
	key string
}

func (c lunaRouteSealError) Seal(pt []byte) ([]byte, []byte, error) {
	require.Equal(c.t, c.key, string(pt))
	return nil, nil, errors.New("safe seal failure")
}

func TestConnectionLunaRoutePersistenceErrorsDoNotDiscloseKeys(t *testing.T) {
	for _, stage := range []string{"meta read", "seal create", "seal update", "SQL update", "deleted"} {
		t.Run(stage, func(t *testing.T) {
			db, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"))
			require.NoError(t, err)
			t.Cleanup(func() { db.Close() })
			require.NoError(t, sqlite.RunMigrations(db))
			crypto, err := adaptercrypto.NewAESGCM(bytes.Repeat([]byte{0x32}, 32))
			require.NoError(t, err)
			reader := sqlite.NewConnectionRepo(db, crypto)
			c := provider.Connection{Name: "lr", Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com"}
			require.NoError(t, reader.Create(context.Background(), c, "lr_original"))
			before, err := reader.Get(context.Background(), "lr")
			require.NoError(t, err)
			const secret = "lr_ERROR_SECRET"
			var sealing provider.Crypto = crypto
			if stage == "seal create" || stage == "seal update" {
				sealing = lunaRouteSealError{Crypto: crypto, t: t, key: secret}
			}
			repo := sqlite.NewConnectionRepo(db, sealing)
			var logs bytes.Buffer
			srv := NewServerWithDeps("test", "test", &Deps{
				ConnectionAdmin: provider.NewConnectionService(repo, nil),
				Logger:          slog.New(slog.NewTextHandler(&logs, nil)),
			})
			ctx := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin})
			req := gomcp.CallToolRequest{}
			args := map[string]any{"name": "lr", "provider": "lunaroute", "base_url": "https://gw.lunaroute.com/v1", "api_key": secret}
			if stage == "SQL update" {
				delete(args, "api_key")
				_, err := db.Exec(`CREATE TRIGGER fail_lr_update BEFORE UPDATE ON connections BEGIN SELECT RAISE(ABORT, 'safe SQL failure'); END`)
				require.NoError(t, err)
			}
			if stage == "deleted" {
				delete(args, "api_key")
				_, err := db.Exec(`CREATE TRIGGER delete_lr_update BEFORE UPDATE ON connections BEGIN DELETE FROM connections WHERE name = OLD.name; SELECT RAISE(IGNORE); END`)
				require.NoError(t, err)
			}
			if stage == "meta read" {
				require.NoError(t, db.Close())
			}
			req.Params.Arguments = args
			var res *gomcp.CallToolResult
			if stage == "seal create" {
				args["name"] = "new"
				res, err = connectionCreateHandler(srv)(ctx, req)
			} else {
				res, err = connectionUpdateHandler(srv)(ctx, req)
			}
			require.NoError(t, err)
			require.True(t, res.IsError)
			assert.NotContains(t, resultText(t, res), secret)
			assert.NotContains(t, logs.String(), secret)
			if stage == "meta read" {
				return
			}
			got, err := reader.Get(ctx, "lr")
			if stage == "deleted" {
				require.ErrorIs(t, err, provider.ErrConnectionNotFound)
				assert.Contains(t, resultText(t, res), `connection "lr" not found`)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, before, got)
			key, err := reader.Reveal(ctx, "lr")
			require.NoError(t, err)
			assert.Equal(t, "lr_original", key)
			_, err = reader.Get(ctx, "new")
			require.ErrorIs(t, err, provider.ErrConnectionNotFound)
		})
	}
}

func TestConnErrorResult_LunaRouteSentinels(t *testing.T) {
	cases := map[error]string{
		provider.ErrLunaRouteKeyPrefix:              `start with "lr_"`,
		provider.ErrLunaRouteKeyRequired:            "lr_ api_key",
		provider.ErrLunaRouteDestinationKeyRequired: "destination provider",
		provider.ErrLunaRouteEndpoint:               "https://gw.lunaroute.com",
		provider.ErrConnectionChangedConcurrently:   "retry",
	}
	for sentinel, want := range cases {
		t.Run(sentinel.Error(), func(t *testing.T) {
			// Even a wrapped error containing a secret must map to static guidance.
			r := connErrorResult("create", "lr", fmt.Errorf("lr_SUBMITTED_SECRET: %w", sentinel))
			require.True(t, r.IsError)
			text := resultText(t, r)
			assert.Contains(t, text, want)
			assert.Contains(t, text, `"lr"`)
			assert.NotContains(t, text, "failed to create", "mapped, not the generic fallback")
			assert.NotContains(t, text, "lr_SUBMITTED_SECRET")
		})
	}
}
