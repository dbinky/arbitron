package provider_test

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLunaRouteProfileReasonsWrapInvalidSelection(t *testing.T) {
	for _, reason := range []error{
		provider.ErrLunaRouteFamilyUnresolved, provider.ErrLunaRouteIncompatible,
		provider.ErrLunaRouteAxisUnsupported, provider.ErrLunaRouteLimitsUnknown,
		provider.ErrLunaRouteBackendUnsupported, provider.ErrLunaRouteBackendRequired,
		provider.ErrLunaRoutePinnedOnly, provider.ErrLunaRouteSettingConflict,
	} {
		err := error(&provider.LunaRouteRefusal{Reason: reason, Model: "glm-5.3", Backend: "opencode", Detail: "do X"})
		assert.ErrorIs(t, err, reason)
		assert.ErrorIs(t, err, provider.ErrInvalidSelection)
		var refusal *provider.LunaRouteRefusal
		require.True(t, errors.As(err, &refusal))
		assert.Contains(t, err.Error(), `model "glm-5.3"`)
		assert.Contains(t, err.Error(), `backend "opencode"`)
		assert.Contains(t, err.Error(), "do X")
	}
}

func TestLunaRouteRefusal_OmitsEmptyBackend(t *testing.T) {
	err := &provider.LunaRouteRefusal{Reason: provider.ErrLunaRouteIncompatible, Model: "glm-5.3"}
	assert.NotContains(t, err.Error(), "backend")
}

func TestLunaRouteSentinelsAreDistinct(t *testing.T) {
	all := append([]error{
		provider.ErrLunaRouteKeyPrefix, provider.ErrLunaRouteKeyRequired, provider.ErrLunaRouteStoredCredential,
		provider.ErrLunaRouteFamilyUnresolved, provider.ErrLunaRouteIncompatible, provider.ErrLunaRouteAxisUnsupported,
		provider.ErrLunaRouteLimitsUnknown, provider.ErrLunaRouteBackendUnsupported, provider.ErrLunaRouteBackendRequired,
		provider.ErrLunaRoutePinnedOnly, provider.ErrLunaRouteSettingConflict, provider.ErrLunaRouteEndpoint,
		provider.ErrLunaRouteLocalFailure,
	}, provider.LunaRouteGatewaySentinels()...)
	for i, a := range all {
		for j, b := range all {
			if i != j {
				assert.NotErrorIs(t, a, b, "%v vs %v", a, b)
			}
		}
	}
	outcomes := provider.LunaRouteGatewaySentinels()
	assert.Len(t, outcomes, 6)
	assert.Same(t, provider.ErrLunaRouteRequestRefused, outcomes[5])
	outcomes[0] = nil
	assert.Same(t, provider.ErrLunaRouteCredentialsRejected, provider.LunaRouteGatewaySentinels()[0])
}

func TestLunaRouteWriteSentinelsAreActionable(t *testing.T) {
	assert.Contains(t, provider.ErrLunaRouteKeyPrefix.Error(), "lr_")
	assert.Contains(t, provider.ErrLunaRouteKeyRequired.Error(), "api_key")
	assert.Contains(t, provider.ErrLunaRouteStoredCredential.Error(), "lr_")
	assert.Contains(t, provider.ErrLunaRouteRequestRefused.Error(), "will not succeed on retry")
}

func TestSanitizeLunaRouteError_IdentityAndSafeText(t *testing.T) {
	original := &lunaRouteSecretError{text: "reveal partial-value: " + secretKey + " Bearer sk-foreign-token"}
	safe := provider.SanitizeLunaRouteError(original, secretKey)
	require.Error(t, safe)
	assert.ErrorIs(t, safe, original)
	var typed *lunaRouteSecretError
	require.ErrorAs(t, safe, &typed)
	assert.Same(t, original, typed)
	assert.Contains(t, safe.Error(), "reveal partial-value:")
	assert.Contains(t, safe.Error(), "[REDACTED]")
	assert.NotContains(t, safe.Error(), secretKey)
	assert.NotContains(t, safe.Error(), "sk-foreign-token")
	assert.Nil(t, provider.SanitizeLunaRouteError(nil, secretKey))
}

type lunaRouteSecretError struct{ text string }

func (e *lunaRouteSecretError) Error() string { return e.text }

func TestLocalLunaRouteError_MarksLocalAndSanitizesStage(t *testing.T) {
	original := &lunaRouteSecretError{text: "write: " + secretKey + " x-api-key: sk-foreign-token"}
	err := provider.LocalLunaRouteError("config "+secretKey, original, secretKey)
	require.Error(t, err)
	assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
	assert.ErrorIs(t, err, original)
	var typed *lunaRouteSecretError
	require.ErrorAs(t, err, &typed)
	assert.Same(t, original, typed)
	assert.Contains(t, err.Error(), "config")
	assert.Contains(t, err.Error(), "write:")
	assert.NotContains(t, err.Error(), secretKey)
	assert.NotContains(t, err.Error(), "sk-foreign-token")
	for _, gateway := range provider.LunaRouteGatewaySentinels() {
		assert.NotErrorIs(t, err, gateway)
	}
	assert.Nil(t, provider.LocalLunaRouteError("reveal", nil))
}
