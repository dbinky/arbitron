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

var _ provider.AdvanceValidator = (*provider.ProfileService)(nil)

// Advancement validation may read routes, but must not discover or re-probe.
// Check the entire replacement configuration, not just its model/target.
type advanceRoutes struct {
	want     provider.Profile
	requests []provider.VersionRequest
	err      error
}

func (s *advanceRoutes) VersionRequests(_ context.Context, p provider.Profile) ([]provider.VersionRequest, error) {
	if !reflect.DeepEqual(p, s.want) {
		return nil, errors.New("lost prospective advancement configuration")
	}
	return s.requests, s.err
}
func (*advanceRoutes) Newest(context.Context, provider.Profile) (provider.EligibleCandidate, error) {
	return provider.EligibleCandidate{}, errors.New("unexpected discovery")
}
func (*advanceRoutes) ValidateStart(context.Context, provider.Profile) (provider.EligibleCandidate, error) {
	return provider.EligibleCandidate{}, errors.New("unexpected probe")
}
func (*advanceRoutes) RevalidateStart(context.Context, provider.Profile, provider.EligibleCandidate) error {
	return errors.New("unexpected proof revalidation")
}

type advanceVersionFault struct {
	want provider.VersionRequest
	err  error
}

func (v advanceVersionFault) SpecificVersion(_ context.Context, request provider.VersionRequest) (string, error) {
	if request != v.want {
		return "", errors.New("lost fixed-version routing input")
	}
	return "", v.err
}

func TestValidateAdvance(t *testing.T) {
	inventoryFault := errors.New("inventory unavailable")
	versionFault := errors.New("version unavailable")
	for _, tc := range []struct {
		name                          string
		edit                          func(*provider.AdvanceRequest)
		routes                        func(provider.Profile) []provider.VersionRequest
		sourceErr                     error
		versionErr                    error
		emptyVersion                  bool
		missingSource, missingVersion bool
		want                          error
	}{
		{name: "compatible newer fixed version"},
		{name: "requested effort floors without rewriting", edit: func(r *provider.AdvanceRequest) { r.Expected.Effort = provider.EffortMax }},
		{name: "model-specific context", edit: func(r *provider.AdvanceRequest) {
			r.Model = "claude-sonnet-4-7"
			r.Expected.ContextWindow = provider.Context1M
		}, want: provider.ErrAxisNotApplicable},
		{name: "model-specific effort", edit: func(r *provider.AdvanceRequest) {
			r.Model = "claude-sonnet-4-7"
			r.Expected.Effort = provider.EffortHigh
		}, want: provider.ErrAxisNotApplicable},
		{name: "pinned", edit: func(r *provider.AdvanceRequest) {
			r.Expected.SelectionMode = provider.SelectionPinned
			r.Expected.FollowTarget = nil
		}, want: provider.ErrAdvanceConflict},
		{name: "missing target", edit: func(r *provider.AdvanceRequest) { r.Expected.FollowTarget = nil }, want: provider.ErrInvalidSelection},
		{name: "invalid target", edit: func(r *provider.AdvanceRequest) { r.Expected.FollowTarget.Provider = "bad/target" }, want: provider.ErrInvalidSelection},
		{name: "invalid mode", edit: func(r *provider.AdvanceRequest) { r.Expected.SelectionMode = "invalid" }, want: provider.ErrInvalidSelection},
		{name: "missing source", missingSource: true, want: provider.ErrSelectionPortsMissing},
		{name: "missing version resolver", missingVersion: true, want: provider.ErrSelectionPortsMissing},
		{name: "inventory fault", sourceErr: inventoryFault, want: inventoryFault},
		{name: "version fault", versionErr: versionFault, want: versionFault},
		{name: "empty version evidence", emptyVersion: true, want: provider.ErrSpecificVersionUnavailable},
		{name: "no routes", routes: func(provider.Profile) []provider.VersionRequest { return nil }, want: provider.ErrExecutionRevisionUnavailable},
		{name: "missing replacement", edit: func(r *provider.AdvanceRequest) { r.Model = "" }, want: provider.ErrSpecificVersionUnavailable},
		{name: "mutable replacement", edit: func(r *provider.AdvanceRequest) { r.Model = "sonnet" }, want: provider.ErrSpecificVersionUnavailable},
		{name: "unsupported actual route", routes: func(p provider.Profile) []provider.VersionRequest {
			return []provider.VersionRequest{provider.VersionRequestFor(p, "unknown")}
		}, want: provider.ErrSpecificVersionUnavailable},
		{name: "every route checked", routes: func(p provider.Profile) []provider.VersionRequest {
			return []provider.VersionRequest{provider.VersionRequestFor(p, "anthropic"), provider.VersionRequestFor(p, "azure-openai")}
		}, want: provider.ErrInvalidSelection},
		{name: "native bedrock cannot follow", routes: func(p provider.Profile) []provider.VersionRequest {
			return []provider.VersionRequest{{Model: p.Model, RouteProvider: "amazon-bedrock", ConnectionRef: "native"}}
		}, want: provider.ErrInvalidSelection},
		{name: "native azure cannot follow", routes: func(p provider.Profile) []provider.VersionRequest {
			return []provider.VersionRequest{{Model: p.Model, RouteProvider: "azure-foundry", ConnectionRef: "native"}}
		}, want: provider.ErrInvalidSelection},
		{name: "contradictory route model", routes: func(p provider.Profile) []provider.VersionRequest {
			r := provider.VersionRequestFor(p, "anthropic")
			r.Model = "claude-sonnet-4-5"
			return []provider.VersionRequest{r}
		}, want: provider.ErrInvalidSelection},
		{name: "unknown backend", edit: func(r *provider.AdvanceRequest) { r.Expected.Backend = "unknown" }, want: provider.ErrUnknownBackend},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store, pub, req := advanceFixture(t, "rev-A")
			req.Expected = req.Expected.Clone()
			req.Expected.ContextWindow, req.Expected.Effort = provider.ContextDefault, provider.EffortDefault
			req.Expected.ConnectionRef = "route-connection"
			req.Expected.AllowedTools = []string{"Read", "Grep"}
			if tc.edit != nil {
				tc.edit(&req)
			}
			before := req
			before.Expected = req.Expected.Clone()
			next := req.Expected.Clone()
			next.Model = req.Model
			requests := []provider.VersionRequest{provider.VersionRequestFor(next, "anthropic")}
			if tc.routes != nil {
				requests = tc.routes(next)
			}
			var source provider.EligibleCandidateSource = &advanceRoutes{want: next, requests: requests, err: tc.sourceErr}
			if tc.missingSource {
				source = nil
			}
			var versions provider.VersionResolver = modelidentity.NewVersionResolver()
			if tc.versionErr != nil || tc.emptyVersion {
				versions = advanceVersionFault{want: requests[0], err: tc.versionErr}
			}
			if tc.missingVersion {
				versions = nil
			}
			capabilities := provider.NewCapabilityRules(map[string]provider.BackendCapability{
				"claude-code": {ContextTiers: []provider.ContextWindow{provider.ContextDefault, provider.Context1M}, EffortLevels: []provider.Effort{provider.EffortHigh}, ModelOverrides: map[string]provider.ModelCapability{
					"claude-sonnet-4-7": {ContextTiers: []provider.ContextWindow{provider.ContextDefault}, EffortLevels: []provider.Effort{}},
				}},
			})
			svc := provider.NewProfileService(nil, capabilities, nil)
			svc.SetModelSelection(versions, source)
			gate := provider.NewExecutionGate()
			gate.SetCurrentRevision(func(context.Context) (string, error) {
				t.Error("validator acquired execution gate")
				return "rev-A", nil
			})
			svc.SetAdvancementDependencies(store, pub, gate)
			svc.SetPublicationJournal(&checkingPublicationJournal{store: store, publisher: pub})
			err := svc.ValidateAdvance(context.Background(), req)
			if tc.want != nil {
				require.ErrorIs(t, err, tc.want)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, before, req, "validation must preserve target, route, axes, tools and proof")
			assert.Empty(t, store.calls)
			assert.Empty(t, pub.seen)
		})
	}
}

func TestValidateAdvanceNeedsNoMutationPorts(t *testing.T) {
	_, _, _, req := advanceFixture(t, "rev-A")
	next := req.Expected.Clone()
	next.Model = req.Model
	svc := provider.NewProfileService(nil, rules(), nil)
	svc.SetModelSelection(modelidentity.NewVersionResolver(), &advanceRoutes{want: next, requests: []provider.VersionRequest{provider.VersionRequestFor(next, "")}})
	require.NoError(t, svc.ValidateAdvance(context.Background(), req))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, svc.ValidateAdvance(ctx, req), context.Canceled)
}
