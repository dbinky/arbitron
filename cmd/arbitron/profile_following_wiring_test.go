package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/provider"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFollowingProfileRootRoundTrip(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	contents := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "log") + `
profiles:
  - alias: worker
    kind: agent
    backend: claude-code
    model: Vendor/Version-X
    selection_mode: following
    follow_target:
      provider: Vendor
      tier: Premium
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	ds, err := initDomain(context.Background(), path)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			ds.Close(context.Background())
		}
	}()
	storeConfiguredFollowing(t, admin, ds, "worker")
	p, err := ds.profileSvc.Get(admin, "worker")
	require.NoError(t, err)
	assert.Equal(t, provider.SelectionFollowing, p.SelectionMode)
	require.NotNil(t, p.FollowTarget)
	assert.Equal(t, provider.FollowTarget{Provider: "vendor", Tier: "premium"}, *p.FollowTarget)
	assert.Equal(t, "Vendor/Version-X", p.Model)
	assert.Equal(t, int64(1), p.Revision)
	ds.Close(context.Background())
	closed = true
	ds, err = initDomain(context.Background(), path)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	p, err = ds.profileSvc.Get(admin, "worker")
	require.NoError(t, err)
	assert.Equal(t, provider.SelectionFollowing, p.SelectionMode)
	require.NotNil(t, p.FollowTarget)
	assert.Equal(t, "premium", p.FollowTarget.Tier)
	assert.Equal(t, int64(1), p.Revision)
}

// A malformed candidate between valid YAML entries must not abort boot and
// strand the later entry behind the nonempty-table seed gate on restart. (The
// later entry is pinned: a first boot has no catalog to prove a following start.)
func TestFollowingProfileRootSkipsInvalidSelection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		selection string
	}{
		{"unknown mode", "    selection_mode: unknown\n"},
		{"pinned target", "    selection_mode: pinned\n    follow_target:\n      provider: vendor\n      tier: premium\n"},
		{"non-slug target", "    selection_mode: following\n    follow_target:\n      provider: vendor\n      tier: bad tier\n"},
		{"missing target", "    selection_mode: following\n"},
		{"empty target", "    selection_mode: following\n    follow_target:\n      provider: ''\n      tier: ''\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.yaml")
			contents := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "log") + `
profiles:
  - alias: before
    kind: chat
    model: claude-sonnet-4-5
  - alias: invalid
    kind: chat
    model: Version-Invalid
` + tc.selection + `  - alias: after
    kind: agent
    backend: claude-code
    model: claude-opus-4-5
`
			require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
			admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
			for boot := 0; boot < 2; boot++ {
				ds, err := initDomain(context.Background(), path)
				require.NoError(t, err, "invalid selection must be skipped on boot %d", boot)
				seedBuiltStack(t, ds)
				func() {
					defer ds.Close(context.Background())
					profiles, err := ds.profileSvc.List(admin)
					require.NoError(t, err)
					require.Len(t, profiles, 2, "only valid YAML entries should persist")
					_, err = ds.profileSvc.Get(admin, "invalid")
					assert.ErrorIs(t, err, provider.ErrProfileNotFound)
					before, err := ds.profileResolver.Resolve(admin, "before")
					require.NoError(t, err)
					assert.Equal(t, "claude-sonnet-4-5", before.Model)
					after, err := ds.profileResolver.Resolve(admin, "after")
					require.NoError(t, err)
					assert.Equal(t, "claude-opus-4-5", after.Model)
					stored, err := ds.profileSvc.Get(admin, "after")
					require.NoError(t, err)
					assert.Equal(t, provider.SelectionPinned, stored.SelectionMode)
					assert.Equal(t, int64(1), stored.Revision)
				}()
			}
		})
	}
}

// TestFollowingProfileUpdateThroughMCPPreservesSelection drives the real
// profile_update MCP tool against a configured following row and asserts the
// sinks: the persisted selection and revision (across a reopen) and the live
// resolver cache the executors read.
func TestFollowingProfileUpdateThroughMCPPreservesSelection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	contents := "auth:\n  mode: local\ndatabase:\n  path: " + filepath.Join(root, "db") + "\nworkspace:\n  path: " + filepath.Join(root, "workspace") + "\nsamples:\n  seed: false\nlogging:\n  file: " + filepath.Join(root, "log") + `
profiles:
  - alias: worker
    kind: agent
    backend: claude-code
    model: Vendor/Version-X
    selection_mode: following
    follow_target:
      provider: Vendor
      tier: Premium
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	admin := identity.WithIdentity(context.Background(), identity.Identity{Role: identity.RoleAdmin})
	ds, err := initDomain(context.Background(), path)
	require.NoError(t, err)
	closed := false
	defer func() {
		if !closed {
			ds.Close(context.Background())
		}
	}()
	storeConfiguredFollowing(t, admin, ds, "worker")
	before, err := ds.profileResolver.Resolve(admin, "worker")
	require.NoError(t, err)
	require.Equal(t, "Vendor/Version-X", before.Model)
	before.RevealKey = nil // Compare configuration, not the per-resolution closure.

	srv := adaptmcp.NewServerWithDeps("root", "test", ds.mcpDeps())
	adaptmcp.RegisterAllTools(srv)
	call := func(name string, args map[string]any) gomcp.CallToolResult {
		t.Helper()
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		require.NoError(t, err)
		data, err := json.Marshal(srv.MCPServer().HandleMessage(admin, request))
		require.NoError(t, err)
		var response struct {
			Result gomcp.CallToolResult `json:"result"`
			Error  any                  `json:"error"`
		}
		require.NoError(t, json.Unmarshal(data, &response))
		require.Nil(t, response.Error, string(data))
		return response.Result
	}
	// The root injects the real candidate source and version resolver. This
	// fixture's fake vendor model has no fixed-version spelling on the claude-cli
	// route, so the edit cannot be validated and must fail closed, leaving the
	// row and the live cache untouched.
	// MCP exposes only the static, actionable message, never resolver diagnostics.
	wantVersionError := gomcp.NewToolResultError("no specific model version available; set the profile to a fixed model version supported on its routes")
	res := call("profile_update", map[string]any{"alias": "worker", "model": "Vendor/Version-Y"})
	require.True(t, res.IsError, "unvalidated following model edit must be refused")
	assert.Equal(t, wantVersionError.Content, res.Content)

	assertStored := func(ds *domainStack, revision int64) {
		t.Helper()
		p, err := ds.profileSvc.Get(admin, "worker")
		require.NoError(t, err)
		assert.Equal(t, provider.SelectionFollowing, p.SelectionMode)
		require.NotNil(t, p.FollowTarget)
		assert.Equal(t, provider.FollowTarget{Provider: "vendor", Tier: "premium"}, *p.FollowTarget)
		assert.Equal(t, "Vendor/Version-X", p.Model)
		assert.Equal(t, revision, p.Revision)
	}
	assertStored(ds, 1)
	after, err := ds.profileResolver.Resolve(admin, "worker")
	require.NoError(t, err)
	assert.Equal(t, "Vendor/Version-X", after.Model)

	// Tools and axes change execution eligibility even when model/target/route
	// stay fixed. Selection that cannot be proved must reject them before SQL or
	// cache publication, just as it rejects an unvalidated model edit.
	storedBefore, err := ds.profileSvc.Get(admin, "worker")
	require.NoError(t, err)
	for _, edit := range []struct {
		field string
		value any
	}{
		{"allowed_tools", []any{"Read"}},
		{"context_window", "200k"},
		{"effort", "high"},
	} {
		t.Run(edit.field, func(t *testing.T) {
			res := call("profile_update", map[string]any{"alias": "worker", edit.field: edit.value})
			assert.True(t, res.IsError, "unvalidated configuration edit must be refused")
			assert.Equal(t, wantVersionError.Content, res.Content)
			stored, err := ds.profileSvc.Get(admin, "worker")
			require.NoError(t, err)
			assert.Equal(t, storedBefore, stored, "no field or revision may change")
			binding, err := ds.profileResolver.Resolve(admin, "worker")
			require.NoError(t, err)
			binding.RevealKey = nil
			assert.Equal(t, before, binding, "no unvalidated configuration may reach executors")
		})
	}

	// An explicit unchanged tool list is a no-op selection edit and still works
	// without discovery; the reopened boot reads the persisted row.
	require.False(t, call("profile_update", map[string]any{"alias": "worker", "allowed_tools": []any{}}).IsError)
	assertStored(ds, 2)
	ds.Close(context.Background())
	closed = true
	ds, err = initDomain(context.Background(), path)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	assertStored(ds, 2)
	storedBefore.Revision++ // Only the successful no-op write increments revision.
	reopened, err := ds.profileSvc.Get(admin, "worker")
	require.NoError(t, err)
	assert.Equal(t, storedBefore, reopened)
	reloaded, err := ds.profileResolver.Resolve(admin, "worker")
	require.NoError(t, err)
	assert.Equal(t, "Vendor/Version-X", reloaded.Model)
	assert.Empty(t, reloaded.AllowedTools)
	assert.Equal(t, before.ContextWindow, reloaded.ContextWindow)
	assert.Equal(t, before.Effort, reloaded.Effort)
}
