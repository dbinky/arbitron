package provider_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pinRouteSource struct {
	provider.EligibleCandidateSource
	requests []provider.VersionRequest
	expected provider.Profile
}

func (s *pinRouteSource) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if !reflect.DeepEqual(s.expected, p) {
		return nil, errors.New("prospective profile lost")
	}
	return s.requests, nil
}
func TestPinnedSelectionRequiresRouteSource(t *testing.T) {
	for _, method := range []string{"create", "patch", "replace", "seed"} {
		t.Run(method, func(t *testing.T) {
			_, repo, _, gate, cache := preparationDB(t)
			p := pinnedProfile("pin", "claude-sonnet-4-6")
			svc := provider.NewProfileService(repo, rules(), cache)
			svc.SetAdvancementDependencies(nil, nil, gate)
			if method == "patch" || method == "replace" {
				prior := p.Clone()
				prior.Model = "claude-sonnet-4-5"
				require.NoError(t, repo.Create(admins(), prior))
				require.NoError(t, cache.Reload(admins()))
			}
			var err error
			switch method {
			case "create":
				_, _, err = svc.Create(admins(), p)
			case "patch":
				_, _, err = svc.UpdatePatch(admins(), p.Alias, provider.ProfilePatch{Model: &p.Model})
			case "replace":
				_, _, err = svc.Update(admins(), p)
			case "seed":
				_, err = seedViaBatch(svc, repo, p)
			}
			require.ErrorIs(t, err, provider.ErrSelectionPortsMissing)
			stored, readErr := repo.Get(admins(), p.Alias)
			if method == "patch" || method == "replace" {
				require.NoError(t, readErr)
				assert.Equal(t, "claude-sonnet-4-5", stored.Model)
				assert.Equal(t, int64(1), stored.Revision)
			} else {
				require.ErrorIs(t, readErr, provider.ErrProfileNotFound)
			}
			cached, cacheErr := cache.SnapshotProfiles([]string{p.Alias})
			if readErr == nil {
				require.NoError(t, cacheErr)
				assert.Equal(t, stored, cached[p.Alias])
			} else {
				require.Error(t, cacheErr)
			}
		})
	}
}

type changingPinRoutes struct {
	provider.EligibleCandidateSource
	calls int
	fault error
}

func (s *changingPinRoutes) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	s.calls++
	if s.calls == 2 {
		return nil, s.fault
	}
	return []provider.VersionRequest{provider.VersionRequestFor(p, "")}, nil
}

func TestPinnedSelectionRechecksRoutesUnderMutationLease(t *testing.T) {
	for _, method := range []string{"create", "patch", "replace", "seed"} {
		t.Run(method, func(t *testing.T) {
			_, repo, _, gate, cache := preparationDB(t)
			p := pinnedProfile("pin", "claude-sonnet-4-6")
			if method == "patch" || method == "replace" {
				prior := p.Clone()
				prior.Model = "claude-sonnet-4-5"
				require.NoError(t, repo.Create(admins(), prior))
				require.NoError(t, cache.Reload(admins()))
			}
			src := &changingPinRoutes{fault: errors.New("route changed during preparation")}
			svc := provider.NewProfileService(repo, rules(), cache)
			svc.SetModelSelection(modelidentity.NewVersionResolver(), src)
			svc.SetAdvancementDependencies(nil, nil, gate)
			var err error
			switch method {
			case "create":
				_, _, err = svc.Create(admins(), p)
			case "patch":
				_, _, err = svc.UpdatePatch(admins(), p.Alias, provider.ProfilePatch{Model: &p.Model})
			case "replace":
				_, _, err = svc.Update(admins(), p)
			case "seed":
				_, err = seedViaBatch(svc, repo, p)
			}
			require.ErrorIs(t, err, src.fault)
			stored, err := repo.Get(admins(), p.Alias)
			if method == "patch" || method == "replace" {
				require.NoError(t, err)
				assert.Equal(t, "claude-sonnet-4-5", stored.Model)
				assert.Equal(t, int64(1), stored.Revision)
			} else {
				assert.ErrorIs(t, err, provider.ErrProfileNotFound)
			}
		})
	}
}

func TestD8ProfileSelection(t *testing.T) {
	for _, route := range []string{"azure", "azure-openai", "azure-foundry", "bedrock", "amazon-bedrock"} {
		for _, following := range []bool{false, true} {
			t.Run(route+map[bool]string{false: "/pinned", true: "/following"}[following], func(t *testing.T) {
				_, repo, _, gate, cache := preparationDB(t)
				p := provider.Profile{Alias: "native", Kind: provider.ProfileKindChat, ConnectionRef: "route", Model: "Configured/Deployment:ID"}
				if following {
					p.SelectionMode = provider.SelectionFollowing
					p.FollowTarget = &provider.FollowTarget{Provider: "openai", Tier: "sol"}
				}
				normalized, err := provider.NormalizeSelection(p)
				require.NoError(t, err)
				src := &pinRouteSource{expected: normalized, requests: []provider.VersionRequest{{Model: p.Model, RouteProvider: route, ConnectionRef: p.ConnectionRef}}}
				svc := provider.NewProfileService(repo, rules(), cache)
				svc.SetModelSelection(modelidentity.NewVersionResolver(), src)
				svc.SetAdvancementDependencies(nil, nil, gate)
				got, _, err := svc.Create(admins(), p)
				if following {
					require.Error(t, err)
					_, err = repo.Get(admins(), p.Alias)
					require.ErrorIs(t, err, provider.ErrProfileNotFound)
					_, err = cache.SnapshotProfiles([]string{p.Alias})
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				stored, err := repo.Get(admins(), p.Alias)
				require.NoError(t, err)
				assert.Equal(t, got, stored)
				assert.Equal(t, p.Model, stored.Model)
				assert.Equal(t, int64(1), stored.Revision)
				cached, err := cache.SnapshotProfiles([]string{p.Alias})
				require.NoError(t, err)
				assert.Equal(t, stored, cached[p.Alias])
			})
		}
	}
}

func TestValidatePinRoutesCodexUsesSubscription(t *testing.T) {
	for _, mode := range []provider.SelectionMode{provider.SelectionPinned, provider.SelectionFollowing} {
		t.Run(string(mode), func(t *testing.T) {
			p := provider.Profile{Kind: provider.ProfileKindAgent, Backend: "codex", ConnectionRef: "azure", Model: "gpt-6.1-sol", SelectionMode: mode}
			if mode == provider.SelectionFollowing {
				p.FollowTarget = &provider.FollowTarget{Provider: "openai", Tier: "sol"}
			}
			req := provider.VersionRequest{Model: p.Model, Backend: "codex", RouteProvider: "azure-openai", ConnectionRef: p.ConnectionRef}
			family, err := provider.ValidatePinRoutes(context.Background(), p, []provider.VersionRequest{req}, modelidentity.NewVersionResolver(), nil)
			require.NoError(t, err)
			assert.Empty(t, family, "Codex must never inherit a connection's opaque exception")
			versions := &fakeVersions{}
			_, err = provider.ValidatePinRoutes(context.Background(), p, []provider.VersionRequest{req}, versions, nil)
			require.NoError(t, err)
			assert.Equal(t, []provider.VersionRequest{{Model: p.Model, Backend: "codex"}}, versions.seen, "only the actual subscription route may authorize the fixed version")
			p.Model = "deployment"
			req.Model = p.Model
			_, err = provider.ValidatePinRoutes(context.Background(), p, []provider.VersionRequest{req}, modelidentity.NewVersionResolver(), nil)
			require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
		})
	}
}

type emptyPinVersion struct{ expected provider.VersionRequest }

func (v emptyPinVersion) SpecificVersion(_ context.Context, request provider.VersionRequest) (string, error) {
	if request != v.expected {
		return "", errors.New("version request lost routing fields")
	}
	return "", nil
}

func TestValidatePinRoutesRejectsEmptyVersionEvidence(t *testing.T) {
	p := provider.Profile{Model: "gpt-6.1-sol"}
	request := provider.VersionRequest{Model: p.Model, RouteProvider: "openai", ConnectionRef: "route"}
	_, err := provider.ValidatePinRoutes(context.Background(), p, []provider.VersionRequest{request}, emptyPinVersion{expected: request}, nil)
	require.ErrorIs(t, err, provider.ErrSpecificVersionUnavailable)
	fault := errors.New("version source fault")
	versions := &fakeVersions{err: fault}
	_, err = provider.ValidatePinRoutes(context.Background(), p, []provider.VersionRequest{request}, versions, nil)
	require.ErrorIs(t, err, fault)
	assert.Equal(t, []provider.VersionRequest{request}, versions.seen)
}

func TestValidatePinRoutes(t *testing.T) {
	p := provider.Profile{Alias: "pin", Kind: provider.ProfileKindChat, Model: "deployment"}
	azure := provider.VersionRequest{Model: p.Model, RouteProvider: "azure-openai", ConnectionRef: "route"}
	bedrock := azure
	bedrock.RouteProvider = "amazon-bedrock"
	strict := provider.VersionRequest{Model: "gpt-6.1-sol", RouteProvider: "openai", ConnectionRef: "route"}
	for name, tc := range map[string]struct {
		p        provider.Profile
		requests []provider.VersionRequest
		versions provider.VersionResolver
		family   string
		ok       bool
	}{
		"native aliases":                  {p, []provider.VersionRequest{azure, {Model: p.Model, RouteProvider: "azure-foundry", ConnectionRef: "other"}}, modelidentity.NewVersionResolver(), provider.CanonicalAzure, true},
		"bedrock":                         {p, []provider.VersionRequest{bedrock}, modelidentity.NewVersionResolver(), provider.CanonicalBedrock, true},
		"different families":              {p, []provider.VersionRequest{azure, bedrock}, modelidentity.NewVersionResolver(), "", false},
		"missing routes":                  {p, nil, modelidentity.NewVersionResolver(), "", false},
		"missing versions":                {p, []provider.VersionRequest{azure}, nil, "", false},
		"wrong model":                     {p, []provider.VersionRequest{strict}, modelidentity.NewVersionResolver(), "", false},
		"empty model":                     {provider.Profile{}, []provider.VersionRequest{{RouteProvider: "azure"}}, modelidentity.NewVersionResolver(), "", false},
		"agent cannot inherit exception":  {p, []provider.VersionRequest{{Model: p.Model, RouteProvider: "azure", Backend: "claude-code"}}, modelidentity.NewVersionResolver(), "", false},
		"strict":                          {provider.Profile{Model: strict.Model}, []provider.VersionRequest{strict}, modelidentity.NewVersionResolver(), "", true},
		"mixed strict and native":         {provider.Profile{Model: strict.Model}, []provider.VersionRequest{strict, {Model: strict.Model, RouteProvider: "bedrock"}}, modelidentity.NewVersionResolver(), "", false},
		"codex subscription is strict":    {provider.Profile{Model: strict.Model}, []provider.VersionRequest{{Model: strict.Model, Backend: "codex"}}, modelidentity.NewVersionResolver(), "", true},
		"fixed looking bedrock following": {provider.Profile{Model: "anthropic.claude-sonnet-4-5-20250929-v1:0", SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "anthropic", Tier: "sonnet"}}, []provider.VersionRequest{{Model: "anthropic.claude-sonnet-4-5-20250929-v1:0", RouteProvider: "bedrock"}}, modelidentity.NewVersionResolver(), "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := provider.ValidatePinRoutes(context.Background(), tc.p, tc.requests, tc.versions, nil)
			if tc.ok {
				require.NoError(t, err)
				assert.Equal(t, tc.family, got)
			} else {
				require.Error(t, err)
				assert.Empty(t, got)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.ValidatePinRoutes(ctx, p, []provider.VersionRequest{azure}, modelidentity.NewVersionResolver(), nil)
	require.ErrorIs(t, err, context.Canceled)
}
