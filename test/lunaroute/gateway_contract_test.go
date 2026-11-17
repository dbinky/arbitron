package lunaroute_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGatewayContractHTTP(t *testing.T) {
	key := "lr_fixture-secret"
	for _, failure := range []string{"", "listing envelope", "listing id", "empty catalog", "chat terminal", "messages terminal", "responses terminal", "wrong model", "invalid credentials", "malformed", "too large", "redirect"} {
		t.Run(failure, func(t *testing.T) {
			paths := []string{}
			targetCalls := 0
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
			defer target.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				assert.Empty(t, r.Header.Get("x-api-key"))
				assert.Empty(t, r.Header.Get("LUNAROUTE-API-KEY"))
				assert.Empty(t, r.Header.Get("X-LunaRoute-Client"))
				if r.Header.Get("Authorization") == "Bearer "+invalidGatewayKey {
					assert.Equal(t, http.MethodGet, r.Method)
					assert.Equal(t, "/v1/models", r.URL.Path)
					if failure == "invalid credentials" {
						w.WriteHeader(403)
					} else {
						w.WriteHeader(401)
					}
					_, _ = io.WriteString(w, `{"type":"error","error":{"code":"INVALID_API_KEY","message":"`+key+`"}}`)
					return
				}
				assert.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
				if r.URL.Path == "/v1/models" {
					assert.Equal(t, http.MethodGet, r.Method)
					assert.Empty(t, r.Header.Get("anthropic-version"))
					switch failure {
					case "listing envelope":
						_, _ = io.WriteString(w, `[{"id":"glm-5.3"}]`)
					case "listing id":
						_, _ = io.WriteString(w, `{"data":[{"id":42}]}`)
					case "empty catalog":
						_, _ = io.WriteString(w, `{"data":[]}`)
					case "malformed":
						_, _ = io.WriteString(w, `{`)
					case "too large":
						_, _ = io.WriteString(w, strings.Repeat("x", maxGatewayResponseBytes+1))
					case "redirect":
						http.Redirect(w, r, target.URL, http.StatusFound)
					default:
						_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"glm-5.3","object":"model"}]}`)
					}
					return
				}
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
				var request map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				assert.Equal(t, "glm-5.3", request["model"])
				assert.Equal(t, false, request["stream"])
				model := "glm-5.3"
				if failure == "wrong model" {
					model = "gpt-6.1-sol"
				}
				switch r.URL.Path {
				case "/v1/chat/completions":
					assert.Equal(t, float64(gatewayOutputTokens), request["max_tokens"])
					assert.Equal(t, []any{map[string]any{"role": "user", "content": gatewayPrompt}}, request["messages"])
					finish := "stop"
					if failure == "chat terminal" {
						finish = "length"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "choices": []any{map[string]any{"finish_reason": finish, "message": map[string]any{"content": "PONG"}}}})
				case "/v1/messages":
					assert.Equal(t, "2023-06-01", r.Header.Get("anthropic-version"))
					assert.Equal(t, float64(gatewayOutputTokens), request["max_tokens"])
					assert.Equal(t, []any{map[string]any{"role": "user", "content": gatewayPrompt}}, request["messages"])
					stop := "end_turn"
					if failure == "messages terminal" {
						stop = "max_tokens"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "type": "message", "stop_reason": stop, "content": []any{map[string]any{"type": "text", "text": "PONG"}}})
				case "/v1/responses":
					assert.Equal(t, gatewayPrompt, request["input"])
					assert.Equal(t, false, request["store"])
					assert.Equal(t, float64(gatewayOutputTokens), request["max_output_tokens"])
					status := "completed"
					if failure == "responses terminal" {
						status = "incomplete"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"model": model, "status": status, "output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "PONG"}}}}})
				default:
					t.Error("unexpected gateway path")
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			observations, err := runGatewayContract(ctx, liveClient(), srv.URL, key, gatewayModels{Chat: "glm-5.3", Messages: "glm-5.3", Responses: "glm-5.3"})
			if failure != "" {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), key)
			} else {
				require.NoError(t, err)
				require.Len(t, observations, 5)
				assert.Equal(t, []string{"/v1/models", "/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/models"}, paths)
				for _, o := range observations {
					assert.Equal(t, "Authorization", o.AuthHeader)
					assert.NotEmpty(t, o.Schema)
				}
				for _, o := range observations[1:4] {
					assert.True(t, o.Terminal)
					assert.True(t, o.HasContent)
					assert.Equal(t, "glm-5.3", o.ReportedModel)
				}
				assert.Equal(t, 1, observations[0].ModelCount)
				assert.Equal(t, "INVALID_API_KEY", observations[4].ErrorCode)
			}
			assert.Zero(t, targetCalls)
			// Assert the final persisted sink, not just an intermediate safe struct.
			root := t.TempDir()
			rel, werr := saveObservation(root, key, "gateway.json", observations)
			require.NoError(t, werr)
			raw, rerr := os.ReadFile(filepath.Join(root, rel))
			require.NoError(t, rerr)
			assert.NotContains(t, string(raw), key)
			assert.NotContains(t, string(raw), "PONG")
			assert.NotContains(t, string(raw), invalidGatewayKey)
			if failure == "empty catalog" {
				assert.Contains(t, string(raw), `"model_count": 0`, "empty availability must be explicitly recorded, not omitted")
			}
		})
	}
}

func TestGatewayTransportAndDecodeErrors(t *testing.T) {
	client := liveClient()
	assert.Greater(t, client.Timeout, 60*time.Second)
	assert.LessOrEqual(t, client.Timeout, 3*time.Minute)
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport echoed lr_transport-secret")
	})
	_, err := runGatewayContract(context.Background(), client, hostedRoot, "lr_transport-secret", gatewayModels{Chat: "glm-5.3", Messages: "glm-5.3", Responses: "glm-5.3"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "lr_transport-secret")
	client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: brokenReadCloser{}}, nil
	})
	_, err = runGatewayContract(context.Background(), client, hostedRoot, "lr_transport-secret", gatewayModels{Chat: "glm-5.3", Messages: "glm-5.3", Responses: "glm-5.3"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "lr_transport-secret")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenReadCloser struct{}

func (brokenReadCloser) Read([]byte) (int, error) { return 0, errors.New("read lr_transport-secret") }
func (brokenReadCloser) Close() error             { return nil }

func TestGatewayInputsAndEscalation(t *testing.T) {
	assert.Equal(t, provider.LunaRouteHostedRoot, hostedRoot)
	for _, models := range []gatewayModels{{}, {Chat: "glm-999.9", Messages: "glm-5.3", Responses: "glm-5.3"}} {
		called := false
		client := liveClient()
		client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("unexpected") })
		_, err := runGatewayContract(context.Background(), client, hostedRoot, "lr_fixture-secret", models)
		require.Error(t, err)
		assert.False(t, called, "invalid explicit models fail before auth")
	}
	for _, key := range []string{"", "lr_", "sk-foreign-secret", "lr_with\nnewline"} {
		called := false
		client := liveClient()
		client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, errors.New("unexpected") })
		_, err := runGatewayContract(context.Background(), client, hostedRoot, key, gatewayModels{Chat: "glm-5.3", Messages: "glm-5.3", Responses: "glm-5.3"})
		require.Error(t, err)
		assert.False(t, called, "foreign or missing keys must never reach the gateway")
	}
	root, r := fixtureRegister(t)
	raw, err := marshalRegister(r)
	require.NoError(t, err)
	p := filepath.Join(root, evidencePath, "register.yaml")
	require.NoError(t, os.WriteFile(p, raw, 0600))
	require.NoError(t, escalateGateway(root))
	raw, err = os.ReadFile(p)
	require.NoError(t, err)
	updated, err := parseRegister(raw)
	require.NoError(t, err)
	assert.Equal(t, "escalated", updated.Items["LR-01"].Status)
	assert.Contains(t, updated.Items["LR-01"].Escalation, "Bearer")
	assert.Empty(t, validateRegisterAt(root, updated))
	assert.Equal(t, r.Items["LR-02"], updated.Items["LR-02"])
}

func TestGatewayShape(t *testing.T) {
	assert.Equal(t, map[string]string{"data": "array", "error": "object", "model": "string", "count": "number", "ok": "boolean", "absent": "null"}, shape([]byte(`{"data":[{"id":"lr_hidden-secret"}],"error":{"message":"do not retain"},"model":"glm-5.3","count":1,"ok":true,"absent":null}`)))
	assert.Nil(t, shape([]byte(`{`)))
	assert.Nil(t, shape([]byte(`[]`)))
}
