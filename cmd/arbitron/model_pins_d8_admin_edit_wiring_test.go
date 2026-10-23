package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Paired row 4: an MCP profile_update that moves a D8 opaque pin's profile to
// another connection must refuse the old run's batch retry and interactive
// turn before any native request, including after reopen, without rewriting
// the persisted pin or route exception. The same-family case moves to a
// distinct Azure host or Bedrock region; the cross-family case is the control.
// Moving connection_ref back restores configured-X on the original endpoint.
// The guard is domain/provider/pinning.go:PinningResolver.Resolve, which
// compares the trigger-time OpaqueConnections ref with the live binding.
// Editing the connection itself (connSvc.Update) remains G3.
func TestD8PinnedAdminConnectionEditThroughMCP(t *testing.T) {
	for _, route := range []string{"azure", "bedrock"} {
		for _, stream := range []bool{false, true} {
			for _, sameFamily := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/stream=%v/sameFamily=%v", route, stream, sameFamily), func(t *testing.T) {
					sink := newD8ExecutionSink(t, route, stream)
					sink.fail = !stream
					ds, closeStack, _, _ := bootD8Root(t, sink, route)
					ctx := adminCtxForLocalUser(t, ds)
					id, _, err := ds.eng.Trigger(ctx, "pin-d8", nil, "normal")
					require.NoError(t, err)
					if stream {
						waitD8Output(t, ds, id, true)
					} else {
						require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
					}
					before, attempts := sink.seen()
					require.Equal(t, []string{"configured-X"}, before)

					other := provider.Connection{Name: "other", Provider: provider.CanonicalBedrock, BaseURL: "https://d8-azure.example.com", Extras: map[string]string{"region": "us-east-1", "access-key-id": "d8-access", "api-version": "2024-10-21"}}
					if route == provider.CanonicalBedrock {
						other.Provider = provider.CanonicalAzure
					}
					if sameFamily {
						other = provider.Connection{Name: "other", Provider: route, BaseURL: "https://d8-azure-b.example.com", Extras: map[string]string{"api-version": "2024-10-21"}}
						if route == provider.CanonicalBedrock {
							other.BaseURL = "https://bedrock-runtime.us-west-2.amazonaws.com"
							other.Extras = map[string]string{"region": "us-west-2", "access-key-id": "d8-access"}
						}
					}
					secret := "original-secret"
					if other.Provider == provider.CanonicalBedrock {
						secret = `{"secret":"original-secret","session_token":"d8-session"}`
					}
					require.NoError(t, ds.connSvc.Create(ctx, other, secret))
					call := mcpToolCaller(t, ctx, ds)
					res, text := call("profile_update", map[string]any{"alias": "pin-alias", "connection_ref": "other"})
					require.False(t, res.IsError, text)
					sink.mu.Lock()
					sink.fail = false
					sink.mu.Unlock()

					refused := func(t *testing.T, ds *domainStack) {
						t.Helper()
						prior, err := ds.nodeExecRepo.ListByInstance(ctx, id)
						require.NoError(t, err)
						if stream {
							err := ds.conversationHandler.HandleMessage(ctx, id, "after the connection edit")
							require.Error(t, err)
							assert.Contains(t, err.Error(), "incompatible with live route")
							assert.Contains(t, err.Error(), "connection changed since trigger")
						} else {
							require.NoError(t, ds.eng.Retry(ctx, id, nil))
							require.Eventually(t, func() bool {
								now, err := ds.nodeExecRepo.ListByInstance(ctx, id)
								saved, getErr := ds.instanceRepo.Get(ctx, id)
								return err == nil && getErr == nil && len(now) > len(prior) && saved.State.IsTerminal()
							}, 20*time.Second, 20*time.Millisecond)
							assert.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
						}
						models, got := sink.seen()
						assert.Equal(t, before, models, "no native request after a connection edit")
						assert.Equal(t, attempts, got)
						saved, err := ds.instanceRepo.Get(ctx, id)
						require.NoError(t, err)
						assert.Equal(t, map[string]string{"pin-alias": "configured-X"}, saved.ModelPins)
						assert.Equal(t, map[string]string{"pin-alias": route}, saved.ModelPinRoutes)
						assert.Equal(t, map[string]string{"pin-alias": "d8"}, saved.ModelPinRouteConnections, "trigger-time connection persisted by the root snapshotter")
					}
					refused(t, ds)

					configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
					closeStack()
					reopened, err := sink.construct(func() (*domainStack, error) { return initDomain(context.Background(), configPath) })
					require.NoError(t, err)
					t.Cleanup(func() { reopened.Close(context.Background()) })
					require.False(t, reopened.pinLookup.Cached(id))
					refused(t, reopened)

					call = mcpToolCaller(t, ctx, reopened)
					res, text = call("profile_update", map[string]any{"alias": "pin-alias", "connection_ref": "d8"})
					require.False(t, res.IsError, text)
					if stream {
						require.NoError(t, reopened.conversationHandler.HandleMessage(ctx, id, "back on the pinned family"))
					} else {
						require.NoError(t, reopened.eng.Retry(ctx, id, nil))
					}
					waitD8Output(t, reopened, id, stream)
					models, got := sink.seen()
					assert.Equal(t, append(before, "configured-X"), models)
					assert.Equal(t, attempts+1, got)
				})
			}
		}
	}
}
