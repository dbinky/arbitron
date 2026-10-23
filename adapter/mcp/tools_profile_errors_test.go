package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// failingNewest is a candidate source whose starting-model lookup fails with a
// configured error, as the real CandidateService does under a busy refresh or a
// raced execution revision.
type failingNewest struct {
	emptySource
	err error
}

func (f failingNewest) Newest(_ context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	if p.Alias != "follow" || p.Kind != provider.ProfileKindChat || p.ConnectionRef != "route" ||
		p.Model != "" || p.SelectionMode != provider.SelectionFollowing || p.FollowTarget == nil ||
		*p.FollowTarget != (provider.FollowTarget{Provider: "openrouter", Tier: "flash"}) {
		return provider.EligibleCandidate{}, errors.New("incomplete prospective profile")
	}
	return provider.EligibleCandidate{}, f.err
}

// A following create whose start lookup fails with a retryable condition gives
// the caller an explicit, secret-free message and persists nothing, instead of
// "profile operation failed" plus the raw error chain.
func TestProfileCreate_RetryableSelectionErrorsAreMapped(t *testing.T) {
	const secret = "https://catalog.example/api.json?token=s3cret"
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"refresh busy", fmt.Errorf("start lookup: %w", modelcatalog.ErrRefreshBusy), "retry later"},
		{"revision stale", fmt.Errorf("%w: policy revision 4, token 3 %s", provider.ErrExecutionRevisionStale, secret), "changed while"},
		{"revision unavailable", fmt.Errorf("%w: reading connection %q revision: %s", provider.ErrExecutionRevisionUnavailable, "leaky-ref", secret), "could not be verified"},
		{"inventory incomplete", fmt.Errorf("%w: connection %q: %s", modelcatalog.ErrInventoryIncomplete, "leaky-ref", secret), "inventory is incomplete"},
	}
	args := map[string]any{
		"alias": "follow", "kind": "chat", "connection_ref": "route",
		"selection_mode": "following",
		"follow_target":  map[string]any{"provider": "openrouter", "tier": "flash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemProfileStore()
			rules := profileRules()
			svc := provider.NewProfileService(store, rules, &noopReloader{})
			svc.SetModelSelection(nil, failingNewest{err: tc.err})
			srv := adaptmcp.NewServerWithDeps("t", "0", &adaptmcp.Deps{ProfileAdmin: svc, Capabilities: rules})
			adaptmcp.RegisterProfileTools(srv)

			res := callToolAdmin(t, srv, "profile_create", args)
			require.True(t, res.IsError)
			text := extractText(t, res)
			assert.Contains(t, text, tc.want)
			assert.NotContains(t, text, "profile operation failed")
			assert.NotContains(t, text, "s3cret")
			assert.NotContains(t, text, "leaky-ref")
			_, err := store.Get(context.Background(), "follow")
			assert.ErrorIs(t, err, provider.ErrProfileNotFound)
		})
	}
}

// Returning any wrapped diagnostic from profileErrorResult discloses the URL
// token below. Exercise both public writes and verify refusal at the store.
func TestProfileWrites_SelectionErrorsAreSecretFree(t *testing.T) {
	const diagnostic = "reading leaky-ref: https://catalog.example/api.json?token=s3cret"
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"internal", errors.New(diagnostic), "retry"},
		{"ports missing", fmt.Errorf("%w: %s", provider.ErrSelectionPortsMissing, diagnostic), "not configured"},
		{"no candidate", fmt.Errorf("%w: %s", provider.ErrNoEligibleCandidate, diagnostic), "no eligible model candidate"},
		{"unknown backend", fmt.Errorf("%w: %s", provider.ErrUnknownBackend, diagnostic), "backend"},
		{"invalid selection", fmt.Errorf("%w: %s", provider.ErrInvalidSelection, diagnostic), "invalid profile selection"},
		{"invalid effort", fmt.Errorf("%w: %s", provider.ErrInvalidEffort, diagnostic), "effort"},
		{"invalid context", fmt.Errorf("%w: %s", provider.ErrInvalidContextWindow, diagnostic), "context window"},
		{"axis not applicable", fmt.Errorf("%w: %s", provider.ErrAxisNotApplicable, diagnostic), "not applicable"},
		{"fixed version unavailable", fmt.Errorf("%w: %s", provider.ErrSpecificVersionUnavailable, diagnostic), "fixed model version"},
		{"cancelled", fmt.Errorf("%w: %s", context.Canceled, diagnostic), "retry"},
		{"deadline", fmt.Errorf("%w: %s", context.DeadlineExceeded, diagnostic), "retry"},
	}
	for _, operation := range []string{"profile_create", "profile_update"} {
		for _, tc := range cases {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				store := newMemProfileStore()
				if operation == "profile_update" {
					require.NoError(t, store.Create(context.Background(), provider.Profile{
						Alias: "follow", Kind: provider.ProfileKindChat, ConnectionRef: "route",
						Model: "deepseek/deepseek-v4-flash", SelectionMode: provider.SelectionPinned,
						AllowedTools: []string{"Read"},
					}))
				}
				before, err := store.List(context.Background())
				require.NoError(t, err)
				reload := &noopReloader{}
				svc := provider.NewProfileService(store, profileRules(), reload)
				svc.SetModelSelection(nil, failingNewest{err: tc.err})
				srv := adaptmcp.NewServerWithDeps("t", "0", &adaptmcp.Deps{ProfileAdmin: svc})
				adaptmcp.RegisterProfileTools(srv)
				res := callToolAdmin(t, srv, operation, map[string]any{
					"alias": "follow", "kind": "chat", "connection_ref": "route", "model": "",
					"selection_mode": "following", "revision": 1,
					"follow_target": map[string]any{"provider": "openrouter", "tier": "flash"},
				})
				require.True(t, res.IsError)
				message := extractText(t, res)
				assert.Contains(t, message, tc.want)
				assert.NotContains(t, message, "s3cret")
				assert.NotContains(t, message, "leaky-ref")
				assert.NotContains(t, message, "catalog.example")
				after, err := store.List(context.Background())
				require.NoError(t, err)
				assert.Equal(t, before, after, "refusal must preserve all fields and the revision")
				assert.Zero(t, reload.calls, "refused writes cannot publish")
			})
		}
	}
}
