package provider_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Check the whole prospective profile, not only the model: these fields decide
// whether previously confirmed execution evidence can still be used.
type configurationSource struct {
	fakeSource
	t        *testing.T
	expected provider.Profile
}

func (s *configurationSource) ValidateStart(ctx context.Context, p provider.Profile) (provider.EligibleCandidate, error) {
	s.t.Helper()
	require.Equal(s.t, s.expected, p)
	return s.fakeSource.ValidateStart(ctx, p)
}

func TestFollowingConfigurationEditsRequireValidation(t *testing.T) {
	for _, field := range []string{"tools", "clear tools", "context", "clear context", "effort", "clear effort"} {
		for _, method := range []string{"patch", "replace"} {
			for _, availability := range []string{"missing", "down", "confirmed"} {
				t.Run(field+"/"+method+"/"+availability, func(t *testing.T) {
					prior := followingProfile("worker", "claude-sonnet-4-5")
					prior.AllowedTools = []string{"Read"}
					prior.ContextWindow = provider.Context200K
					prior.Effort = provider.EffortLow
					next := prior.Clone()
					var patch provider.ProfilePatch
					switch field {
					case "tools":
						next.AllowedTools = []string{"Glob"}
						patch.AllowedTools = &next.AllowedTools
					case "clear tools":
						next.AllowedTools = []string{}
						patch.AllowedTools = &next.AllowedTools
					case "context", "clear context":
						value := string(provider.Context1M)
						if field == "clear context" {
							value = string(provider.ContextDefault)
						}
						next.ContextWindow = provider.ContextWindow(value)
						patch.ContextWindow = &value
					case "effort", "clear effort":
						value := string(provider.EffortHigh)
						if field == "clear effort" {
							value = string(provider.EffortDefault)
						}
						next.Effort = provider.Effort(value)
						patch.Effort = &value
					}
					store := newMemProfileStore(prior)
					down := errors.New("configuration has no confirmed execution evidence")
					src := &configurationSource{t: t, expected: next}
					var source provider.EligibleCandidateSource = src
					switch availability {
					case "missing":
						source = nil
					case "down":
						src.validateErr = down
					}
					reload := &fakeReloader{}
					svc := provider.NewProfileService(store, rules(), reload)
					svc.SetModelSelection(&fakeVersions{}, source)
					var err error
					if method == "replace" {
						_, _, err = svc.Update(admins(), next)
					} else {
						_, _, err = svc.UpdatePatch(admins(), prior.Alias, patch)
					}
					persisted, getErr := store.Get(context.Background(), prior.Alias)
					require.NoError(t, getErr)
					if availability == "confirmed" {
						require.NoError(t, err)
						assert.Equal(t, next, persisted)
						assert.Equal(t, 1, store.writeCount())
						assert.Equal(t, 1, reload.calls)
					} else {
						if availability == "missing" {
							require.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
						} else {
							require.ErrorIs(t, err, down)
						}
						assert.Equal(t, prior, persisted)
						assert.Zero(t, store.writeCount())
						assert.Zero(t, reload.calls)
					}
					if source != nil {
						assert.Len(t, src.startSeen, 1, "every configuration change requires prospective evidence")
					}
				})
			}
		}
	}
}

func TestFollowingUnchangedConfigurationNeedsNoDiscovery(t *testing.T) {
	prior := followingProfile("worker", "claude-sonnet-4-5")
	prior.AllowedTools = []string{"Read"}
	prior.ContextWindow = provider.Context200K
	prior.Effort = provider.EffortLow
	for _, method := range []string{"patch", "replace"} {
		t.Run(method, func(t *testing.T) {
			store := newMemProfileStore(prior)
			svc := provider.NewProfileService(store, rules(), &fakeReloader{})
			var err error
			if method == "replace" {
				_, _, err = svc.Update(admins(), prior.Clone())
			} else {
				tools := []string{"Read"}
				contextWindow, effort := string(prior.ContextWindow), string(prior.Effort)
				_, _, err = svc.UpdatePatch(admins(), prior.Alias, provider.ProfilePatch{AllowedTools: &tools, ContextWindow: &contextWindow, Effort: &effort})
			}
			require.NoError(t, err)
			persisted, err := store.Get(context.Background(), prior.Alias)
			require.NoError(t, err)
			assert.Equal(t, prior, persisted)
		})
	}
}
