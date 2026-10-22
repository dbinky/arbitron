package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
)

func TestStdioContext_LocalModeProducesAdminIdentity(t *testing.T) {
	e := newLayoutE2EEnv(t, layoutE2EOptions{})
	ds, _ := e.boot(t)
	ctx, err := ds.stdioContext(context.Background())
	require.NoError(t, err)
	id, ok := identity.FromContext(ctx)
	require.True(t, ok)
	require.Equal(t, localDefaultUser, id.UserID)
	require.Equal(t, identity.RoleAdmin, id.Role)
}

func TestStdioContext_NonLocalModeStaysUnauthenticated(t *testing.T) {
	for _, mode := range []string{config.AuthModeOrg, config.AuthModeEdge} {
		ds := &domainStack{cfg: &config.Config{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
		ds.cfg.Auth.Mode = mode
		ctx, err := ds.stdioContext(context.Background())
		require.NoError(t, err)
		_, ok := identity.FromContext(ctx)
		require.False(t, ok, "mode %s must not invent a principal", mode)
	}
}
