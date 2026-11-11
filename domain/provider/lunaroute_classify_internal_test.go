package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// LR-04: the documented codes, identical for every operation. Each recognised
// code wins over status, and every mapping is exercised at the error sink.
func TestLunaRouteCodesAreOperationScoped(t *testing.T) {
	original := lunaRouteVerifiedCodes[LunaRouteChat]
	t.Cleanup(func() { lunaRouteVerifiedCodes[LunaRouteChat] = original })
	lunaRouteVerifiedCodes[LunaRouteChat] = map[string]error{"CHAT_FIXTURE": ErrLunaRouteRequestRefused}
	chat := ClassifyLunaRoute(LunaRouteOutcome{Operation: LunaRouteChat, Status: 400, Code: "CHAT_FIXTURE"})
	assert.ErrorIs(t, chat, ErrLunaRouteRequestRefused)
	listing := ClassifyLunaRoute(LunaRouteOutcome{Operation: LunaRouteListModels, Status: 400, Code: "CHAT_FIXTURE"})
	assert.ErrorIs(t, listing, ErrLunaRouteCauseUnknown)
	listing = ClassifyLunaRoute(LunaRouteOutcome{Operation: LunaRouteListModels, Status: 400, Code: "INVALID_API_KEY"})
	assert.ErrorIs(t, listing, ErrLunaRouteCredentialsRejected)
}

func TestLunaRouteDocumentedCodeTable(t *testing.T) {
	want := map[string]error{
		"INVALID_API_KEY":                   ErrLunaRouteCredentialsRejected,
		"MISSING_LUNAROUTE_HEADER":          ErrLunaRouteCredentialsRejected,
		"INFERENCE_MODEL_NOT_FOUND":        ErrLunaRouteModelUnavailable,
		"CONCURRENT_REQUEST_LIMIT_EXCEEDED": ErrLunaRouteBusy,
		"INFERENCE_UNAVAILABLE":             ErrLunaRouteServiceFailure,
		"NO_HEALTHY_PROVIDER":               ErrLunaRouteServiceFailure,
		"PROVIDER_KEY_UNAVAILABLE":          ErrLunaRouteServiceFailure,
		"BYOK_SPEND_GATE_UNAVAILABLE":       ErrLunaRouteServiceFailure,
		"BYOK_SPEND_CAP_REACHED":            ErrLunaRouteRequestRefused,
		"MODEL_UNPRICED":                    ErrLunaRouteRequestRefused,
	}
	for _, op := range []LunaRouteOperation{LunaRouteListModels, LunaRouteChat, LunaRouteAgent} {
		assert.Len(t, lunaRouteVerifiedCodes[op], len(want), "op %s carries exactly the documented and R4-observed codes", op)
		for code, sentinel := range want {
			assert.Same(t, sentinel, lunaRouteVerifiedCodes[op][code], "op %s code %s", op, code)
			for _, status := range []int{400, 401, 429, 503} {
				body := `{"error":{"code":"` + code + `","message":"do not surface"}}`
				decoded := LunaRouteErrorCode(op, body)
				f := ClassifyLunaRoute(LunaRouteOutcome{Operation: op, Status: status, Code: decoded, Body: body})
				assert.ErrorIs(t, f, sentinel)
				assert.Contains(t, f.Error(), "code="+code)
				assert.NotContains(t, f.Error(), "do not surface")
				for _, other := range LunaRouteGatewaySentinels() {
					if other != sentinel {
						assert.NotErrorIs(t, f, other)
					}
				}
			}
		}
		assert.NotContains(t, lunaRouteVerifiedCodes[op], "MODEL_NOT_FOUND")
	}
	f := ClassifyLunaRoute(LunaRouteOutcome{Operation: LunaRouteChat, Status: 429, Code: "BYOK_SPEND_CAP_REACHED"})
	assert.ErrorIs(t, f, ErrLunaRouteRequestRefused)
	assert.NotErrorIs(t, f, ErrLunaRouteBusy)
	f = ClassifyLunaRoute(LunaRouteOutcome{Operation: LunaRouteChat, Status: 400, Code: "invalid_api_key"})
	assert.ErrorIs(t, f, ErrLunaRouteCauseUnknown)
	f = ClassifyLunaRoute(LunaRouteOutcome{Operation: "unsupported", Status: 400, Code: "INVALID_API_KEY"})
	assert.ErrorIs(t, f, ErrLunaRouteCauseUnknown)
}
