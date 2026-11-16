package provider_test

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteDiagnosis(t *testing.T) {
	original := errors.New("opencode: exit status 1 (token lr_live_SECRET)")
	cases := []struct {
		name            string
		observation     error
		checked, listed bool
		want            error
		text            string
	}{
		{"unchecked", nil, false, false, provider.ErrLunaRouteCauseUnknown, "could not be checked"},
		{"rejected now", provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteListModels, Status: 401}), true, false, provider.ErrLunaRouteCredentialsRejected, "LunaRoute rejected the credential"},
		{"missing now", provider.ErrLunaRouteModelUnavailable, true, false, provider.ErrLunaRouteModelUnavailable, "not proof of the original cause"},
		{"listed now", nil, true, true, provider.ErrLunaRouteCauseUnknown, "model is listed now"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &provider.LunaRouteDiagnosis{Original: original, Observation: tc.observation, Checked: tc.checked, ModelListed: tc.listed}
			assert.ErrorIs(t, d, original)
			assert.ErrorIs(t, d, tc.want)
			assert.Contains(t, d.Error(), tc.text)
			assert.NotContains(t, d.Error(), "lr_live_SECRET")
			if tc.checked {
				assert.Contains(t, d.Error(), "not proof of the original cause")
			}
			var got *provider.LunaRouteDiagnosis
			assert.ErrorAs(t, d, &got)
		})
	}
}

func TestLunaRouteDiagnosis_NilOriginalAndObservationPrecedence(t *testing.T) {
	observation := errors.New("gateway observation lr_observation_SECRET")
	for _, tc := range []struct {
		name string
		d    provider.LunaRouteDiagnosis
		text string
		want error
	}{
		{"nil original unchecked", provider.LunaRouteDiagnosis{}, "could not be checked afterwards, so the cause is unknown", provider.ErrLunaRouteCauseUnknown},
		{"nil original listed", provider.LunaRouteDiagnosis{Checked: true, ModelListed: true}, "the credential was accepted and the model is listed now, so the cause is unknown", provider.ErrLunaRouteCauseUnknown},
		{"nil original unknown", provider.LunaRouteDiagnosis{Checked: true}, "not proof of the original cause): the cause is unknown", provider.ErrLunaRouteCauseUnknown},
		{"observation before listed", provider.LunaRouteDiagnosis{Checked: true, ModelListed: true, Observation: observation}, "gateway observation lr_[REDACTED]", observation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var text string
			require.NotPanics(t, func() { text = tc.d.Error() })
			assert.Contains(t, text, tc.text)
			assert.NotContains(t, text, "lr_observation_SECRET")
			assert.NotContains(t, text, "<nil>")
			assert.ErrorIs(t, &tc.d, tc.want)
			if tc.d.Observation != nil {
				assert.NotContains(t, text, "credential was accepted")
			}
		})
	}
}
