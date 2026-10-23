package mcp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/modelcatalog"
)

type fakeCatalogAdmin struct {
	listErr, refreshErr error
	refreshCalls        int
}

func (f *fakeCatalogAdmin) List(ctx context.Context) (modelcatalog.PublicCatalogView, error) {
	if _, ok := identity.FromContext(ctx); !ok {
		return modelcatalog.PublicCatalogView{}, identity.ErrUnauthenticated
	}
	return modelcatalog.PublicCatalogView{Entries: []modelcatalog.PublicCatalogEntry{{CanonicalID: "vendor/m", Usability: "confirmed"}}}, f.listErr
}

func (f *fakeCatalogAdmin) RefreshNow(ctx context.Context) (modelcatalog.PublicRefreshReport, error) {
	f.refreshCalls++
	return modelcatalog.PublicRefreshReport{ID: "r1"}, f.refreshErr
}

func TestModelCatalogToolsRenderServiceResults(t *testing.T) {
	fake := &fakeCatalogAdmin{}
	srv := adaptmcp.NewServerWithDeps("catalog", "test", &adaptmcp.Deps{ModelCatalog: fake})
	adaptmcp.RegisterModelCatalogTools(srv)
	admin := adminIdentityCtx(uuid.New())

	res := callToolCtx(t, srv, admin, "model_catalog_list", nil)
	require.False(t, res.IsError, policyToolText(t, res))
	assert.Contains(t, policyToolText(t, res), `"canonical_id": "vendor/m"`)

	res = callToolCtx(t, srv, admin, "model_catalog_refresh", nil)
	require.False(t, res.IsError, policyToolText(t, res))
	assert.Contains(t, policyToolText(t, res), `"id": "r1"`)
	assert.Equal(t, 1, fake.refreshCalls)
}

func TestModelCatalogToolsMapErrorsWithoutLeakingDetail(t *testing.T) {
	admin := adminIdentityCtx(uuid.New())
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"forbidden": {identity.ErrForbidden, "admin only"},
		"closed":    {modelcatalog.ErrRefreshClosed, "shutting down"},
		"busy":      {errors.Join(errors.New("SECRET"), modelcatalog.ErrRefreshBusy), "already in progress"},
		"cancelled": {context.Canceled, "cancelled"},
		"raw fetch": {errors.New(`Get "https://catalog.example/api.json?token=SECRET": dial tcp`), "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeCatalogAdmin{listErr: tc.err, refreshErr: tc.err}
			srv := adaptmcp.NewServerWithDeps("catalog", "test", &adaptmcp.Deps{ModelCatalog: fake})
			adaptmcp.RegisterModelCatalogTools(srv)
			for _, tool := range []string{"model_catalog_list", "model_catalog_refresh"} {
				res := callToolCtx(t, srv, admin, tool, nil)
				require.True(t, res.IsError)
				text := policyToolText(t, res)
				assert.Contains(t, text, tc.want)
				assert.NotContains(t, text, "SECRET")
			}
		})
	}
}

func TestModelCatalogToolsUnavailableUnauthenticatedAndExtraArgs(t *testing.T) {
	srv := adaptmcp.NewServerWithDeps("catalog", "test", &adaptmcp.Deps{})
	adaptmcp.RegisterModelCatalogTools(srv)
	for _, tool := range []string{"model_catalog_list", "model_catalog_refresh"} {
		assert.True(t, callToolCtx(t, srv, adminIdentityCtx(uuid.New()), tool, nil).IsError, "nil service reports unavailable")
	}
	fake := &fakeCatalogAdmin{}
	srv = adaptmcp.NewServerWithDeps("catalog", "test", &adaptmcp.Deps{ModelCatalog: fake})
	adaptmcp.RegisterModelCatalogTools(srv)
	assert.True(t, callToolCtx(t, srv, context.Background(), "model_catalog_list", nil).IsError)
	assert.True(t, callToolCtx(t, srv, adminIdentityCtx(uuid.New()), "model_catalog_refresh", map[string]any{"url": "https://evil.example"}).IsError)
	assert.Zero(t, fake.refreshCalls, "arguments (a caller-chosen URL) must be refused before any cycle runs")
}

// Exercise the real service/coordinator: an overlapping tool call must expose
// busy without waiting for or claiming the scheduled cycle's report.
func TestModelCatalogRefreshToolReportsBusyForScheduledOverlap(t *testing.T) {
	started := make(chan struct{})
	c := modelcatalog.NewRefreshCoordinator(func(ctx context.Context) (modelcatalog.RefreshReport, error) {
		close(started)
		<-ctx.Done()
		return modelcatalog.RefreshReport{}, ctx.Err()
	})
	defer c.Close()
	scheduled := make(chan error, 1)
	go func() { _, err := c.Refresh(context.Background()); scheduled <- err }()
	<-started
	srv := adaptmcp.NewServerWithDeps("catalog", "test", &adaptmcp.Deps{
		ModelCatalog: &modelcatalog.AdminService{Refresh: c.Refresh},
	})
	adaptmcp.RegisterModelCatalogTools(srv)
	ctx, cancel := context.WithTimeout(adminIdentityCtx(uuid.New()), time.Second)
	defer cancel()
	res := callToolCtx(t, srv, ctx, "model_catalog_refresh", nil)
	require.True(t, res.IsError)
	assert.Contains(t, policyToolText(t, res), "already in progress")
	select {
	case err := <-scheduled:
		t.Fatalf("overlapping tool interrupted scheduled refresh: %v", err)
	default:
	}
	c.Close()
	assert.ErrorIs(t, <-scheduled, context.Canceled)
}
