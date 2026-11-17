package lunaroute_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoveryErrorCodes(t *testing.T) {
	for _, op := range []provider.LunaRouteOperation{provider.LunaRouteListModels, provider.LunaRouteChat, provider.LunaRouteAgent} {
		for code, want := range map[string]error{
			"INVALID_API_KEY":                   provider.ErrLunaRouteCredentialsRejected,
			"MISSING_LUNAROUTE_HEADER":          provider.ErrLunaRouteCredentialsRejected,
			"INFERENCE_MODEL_NOT_FOUND":         provider.ErrLunaRouteModelUnavailable,
			"CONCURRENT_REQUEST_LIMIT_EXCEEDED": provider.ErrLunaRouteBusy,
			"INFERENCE_UNAVAILABLE":             provider.ErrLunaRouteServiceFailure,
			"NO_HEALTHY_PROVIDER":               provider.ErrLunaRouteServiceFailure,
			"PROVIDER_KEY_UNAVAILABLE":          provider.ErrLunaRouteServiceFailure,
			"BYOK_SPEND_GATE_UNAVAILABLE":       provider.ErrLunaRouteServiceFailure,
			"BYOK_SPEND_CAP_REACHED":            provider.ErrLunaRouteRequestRefused,
			"MODEL_UNPRICED":                    provider.ErrLunaRouteRequestRefused,
		} {
			for _, body := range []string{fmt.Sprintf(`{"code":%q,"type":"gateway_error","message":"private"}`, code), fmt.Sprintf(`{"type":"error","error":{"code":%q,"type":"gateway_error","message":"private"}}`, code)} {
				codes := errorCodes([]byte(body))
				require.Equal(t, code, codes["error.code"])
				assert.Equal(t, "gateway_error", codes["error.type"])
				assert.Len(t, codes, 2)
				decoded := provider.LunaRouteErrorCode(op, body)
				assert.Equal(t, codes["error.code"], decoded)
				assert.ErrorIs(t, provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: op, Status: 418, Code: decoded}), want)
			}
		}
		body := `{"error":{"code":"MODEL_NOT_FOUND"}}`
		assert.ErrorIs(t, provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: op, Status: 429, Code: errorCodes([]byte(body))["error.code"]}), provider.ErrLunaRouteBusy)
	}
	for _, body := range []string{"", `{`, `null`, `[]`, `{"code":null}`, `{"code":401}`, `{"code":"INVALID_API_KEY","error":null}`, `{"error":{"code":false,"type":3}}`, `{"code":"INVALID_API_KEY","error":{"code":"MODEL_UNPRICED"}}`, `{"code":"INVALID_API_KEY","code":"MODEL_UNPRICED"}`, `{"code":"INVALID_API_KEY"} {}`, `{"error":{"code":"lr_secret"}}`, strings.Repeat("x", maxGatewayResponseBytes+1)} {
		assert.Empty(t, errorCodes([]byte(body)), "malformed/ambiguous/unsafe fields cannot become facts")
	}
}

const discoveryKey = "lr_discovery-fixture-secret"

// This sink checks every credential-bearing request's endpoint, auth and body.
// Its response is never success if a load-bearing request input was omitted.
func discoveryFixture(t *testing.T, failure string) (http.RoundTripper, *int) {
	t.Helper()
	calls := 0
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != captureHost || len(captureAuthNames(r.Header)) != 1 || r.Header.Get("Authorization") == "" {
			return nil, errors.New("unexpected routing or auth")
		}
		for _, h := range []string{"HTTP-Referer", "X-Title", "x-api-key", "LUNAROUTE-API-KEY", "LUNAROUTE-PROJECT-ID"} {
			assert.Empty(t, r.Header.Get(h))
		}
		status, body := 200, ""
		switch {
		case r.Header.Get("Authorization") == "Bearer "+invalidGatewayKey:
			require.Equal(t, "GET", r.Method)
			require.Equal(t, "/v1/models", r.URL.Path)
			status, body = 401, `{"type":"error","error":{"code":"INVALID_API_KEY","message":"`+discoveryKey+`"}}`
			if failure == "contradicted code" {
				body = `{"code":"OTHER_AUTH_CODE"}`
			}
		case r.Header.Get("Authorization") != "Bearer "+discoveryKey:
			return nil, errors.New("wrong selected credential")
		case r.Method == "GET" && r.URL.Path == "/v1/models":
			body = `{"object":"list","data":[{"id":"glm-5.3"}]}`
			switch failure {
			case "malformed listing":
				body = `{"data":[{"id":42}]}`
			case "empty listing":
				body = `{"data":[]}`
			case "unlisted":
				body = `{"data":[{"id":"other"}]}`
			case "duplicate listing":
				body = `{"data":[],"data":[{"id":"glm-5.3"}]}`
			case "unsafe listing":
				body = `{"data":[{"id":"glm-5.3"},{"id":"lr_stolen-key"}]}`
			case "oversized":
				body = strings.Repeat("x", maxGatewayResponseBytes+1)
			case "transport":
				return nil, errors.New(discoveryKey)
			case "read":
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: brokenReadCloser{}, Request: r}, nil
			case "redirect":
				status, body = 302, `{}`
			}
		case r.Method == "POST" && r.URL.Path == "/v1/chat/completions":
			var req map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
			assert.Equal(t, []any{map[string]any{"role": "user", "content": gatewayPrompt}}, req["messages"])
			assert.NotEqual(t, true, req["stream"])
			model := req["model"]
			if model == "glm-5.3-flash" {
				assert.Equal(t, float64(discoveryNegativeTokens), req["max_tokens"])
				status, body = 404, `{"error":{"code":"INFERENCE_MODEL_NOT_FOUND","message":"`+discoveryKey+`"}}`
				if failure == "unavailable succeeds" {
					status, body = 200, `{"model":"glm-5.3-flash","choices":[{"finish_reason":"stop","message":{"content":"PONG"}}]}`
				}
			} else {
				require.Equal(t, "glm-5.3", model)
				assert.Equal(t, float64(1024), req["max_tokens"])
				body = `{"model":"glm-5.3","choices":[{"finish_reason":"stop","message":{"content":"PONG"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
				switch failure {
				case "no terminal":
					body = `{"model":"glm-5.3","choices":[{"message":{"content":"PONG"}}]}`
				case "missing model":
					body = `{"choices":[{"finish_reason":"stop","message":{"content":"PONG"}}]}`
				case "wrong model":
					body = `{"model":"gpt-6.1-sol","choices":[{"finish_reason":"stop","message":{"content":"PONG"}}]}`
				case "no pong":
					body = `{"model":"glm-5.3","choices":[{"finish_reason":"stop","message":{"content":"other text"}}]}`
				case "sse error":
					body = "event: error\ndata: {\"error\":{\"code\":\"INVALID_API_KEY\"}}\n\n"
				}
			}
		default:
			return nil, errors.New("unexpected method or path")
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}, "Location": {"https://foreign.invalid"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}), &calls
}

func TestDiscoveryChatProductionObservation(t *testing.T) {
	rt, calls := discoveryFixture(t, "")
	obs, err := collectDiscoveryChat(context.Background(), rt, discoveryKey, "glm-5.3", "glm-5.3-flash", provider.CapabilityRules{})
	require.NoError(t, err)
	assert.Equal(t, 5, *calls, "positive listing/chat, invalid key, fresh listing and absent-model inference")
	require.Len(t, obs.Inference, 1)
	assert.NoError(t, validateCapturedInference(obs.Inference, "glm-5.3", []string{"main"}))
	require.Len(t, obs.Errors, 2)
	assert.Equal(t, "credentials_rejected", obs.Errors[0].Classified)
	assert.Equal(t, "model_unavailable", obs.Errors[1].Classified, "R4's observed code must reach the evidence sink")
	root := t.TempDir()
	rel, err := saveObservation(root, discoveryKey, "discovery.json", obs)
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	for _, text := range []string{discoveryKey, invalidGatewayKey, gatewayPrompt, "PONG", "Bearer ", "lr_"} {
		assert.NotContains(t, string(raw), text)
	}
	var persisted discoveryChatObservation
	require.NoError(t, json.Unmarshal(raw, &persisted))
	assert.Equal(t, []string{"glm-5.3"}, persisted.ListingModelIDs)
	assert.Equal(t, "array", persisted.ListingShape["data"])
	assert.True(t, persisted.Chat.Completed)
	assert.Equal(t, "glm-5.3", persisted.Chat.ResponseModel)
	assert.Equal(t, 3, persisted.Chat.TokensIn)
	assert.Equal(t, "INVALID_API_KEY", persisted.Errors[0].Codes["error.code"])
	assert.Equal(t, "application/json", persisted.Errors[0].ContentType)
	assert.Equal(t, "glm-5.3-flash", persisted.Errors[1].Model)
	assert.Equal(t, "INFERENCE_MODEL_NOT_FOUND", persisted.Errors[1].Codes["error.code"])
	assert.Equal(t, 404, persisted.Errors[1].Status)
	assert.Equal(t, "model_unavailable", persisted.Errors[1].Classified)
	assert.Equal(t, []string{"Authorization"}, persisted.Errors[1].AuthHeaderNames)
	assert.True(t, persisted.Errors[1].AuthMatchesSelected)
	assert.False(t, persisted.Errors[0].AuthMatchesSelected)
	assert.Equal(t, "glm-5.3", persisted.Inference[0].RequestedModel)
	assert.Equal(t, []string{"glm-5.3"}, persisted.Inference[0].ReportedModels)
}

func TestDiscoveryChatRejectsIncompleteEvidence(t *testing.T) {
	for _, failure := range []string{"malformed listing", "empty listing", "unlisted", "duplicate listing", "unsafe listing", "oversized", "transport", "read", "redirect", "no terminal", "missing model", "wrong model", "no pong", "sse error", "contradicted code", "unavailable succeeds"} {
		t.Run(failure, func(t *testing.T) {
			rt, calls := discoveryFixture(t, failure)
			_, err := collectDiscoveryChat(context.Background(), rt, discoveryKey, "glm-5.3", "glm-5.3-flash", provider.CapabilityRules{})
			require.Error(t, err)
			assert.NotContains(t, err.Error(), discoveryKey)
			wantCalls := 1
			switch failure {
			case "no terminal", "missing model", "wrong model", "no pong", "sse error":
				wantCalls = 2
			case "contradicted code":
				wantCalls = 3
			case "unavailable succeeds":
				wantCalls = 5
			}
			assert.Equal(t, wantCalls, *calls, "must reach the intended failing seam, not fail earlier on grammar")
		})
	}
	for _, model := range []string{"", "glm-999.9", "claude-sonnet-5-lr1"} {
		rt, calls := discoveryFixture(t, "")
		_, err := collectDiscoveryChat(context.Background(), rt, discoveryKey, model, "", provider.CapabilityRules{})
		require.Error(t, err)
		assert.Zero(t, *calls)
	}
	rt, calls := discoveryFixture(t, "")
	_, err := collectDiscoveryChat(context.Background(), rt, discoveryKey, "glm-5.3-flash", "", provider.CapabilityRules{})
	require.Error(t, err, "reviewed but unlisted selection is refused after discovery")
	assert.Equal(t, 1, *calls, "only listing, no inference or substitution")
	rt, calls = discoveryFixture(t, "")
	_, err = collectDiscoveryChat(context.Background(), rt, discoveryKey, "glm-5.3", "glm-999.9", provider.CapabilityRules{})
	require.Error(t, err)
	assert.Zero(t, *calls)
}

func TestDiscoveryChatOptionalNegativeAndLoadedCapabilities(t *testing.T) {
	rt, calls := discoveryFixture(t, "")
	obs, err := collectDiscoveryChat(context.Background(), rt, discoveryKey, "glm-5.3", "", provider.CapabilityRules{})
	require.NoError(t, err)
	assert.Equal(t, 3, *calls)
	assert.Len(t, obs.Errors, 1)
	assert.Equal(t, "not supplied; deterministic root fixtures cover unavailable models", obs.UnavailableCase)
	rules := provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": {ID: "glm-5.3", OutputTokens: provider.LimitFact{Tokens: 32, Evidence: "fixture#limit"}}})
	rt, calls = discoveryFixture(t, "")
	_, err = collectDiscoveryChat(context.Background(), rt, discoveryKey, "glm-5.3", "", rules)
	require.Error(t, err, "actual production preflight must consume the supplied table")
	assert.Equal(t, 1, *calls, "known output conflict stops before credential-bearing inference")
}
