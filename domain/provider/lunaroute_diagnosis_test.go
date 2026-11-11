package provider_test

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
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
