package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/adapter/openrouter"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrChatKey = "lr_unit_chat_SECRET"
const lrChatPrompt = "nonce-chat-732: ping"

type chatGW struct {
	mu       sync.Mutex
	reqs     []*http.Request
	bodies   [][]byte
	expected map[string]any
	status   int
	body     string
	header   http.Header
	err      error
	readErr  error
}

func chatBody(model, prompt string, max int, effort string, stream bool) map[string]any {
	body := map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": prompt}}}
	if max > 0 {
		body["max_tokens"] = float64(max)
	}
	if effort != "" {
		body["reasoning_effort"] = effort
	}
	if stream {
		body["stream"] = true
	}
	return body
}

// The transport rejects missing/changed load-bearing inputs, even when a reply
// has been scripted. It never returns success merely because auth was present.
func (g *chatGW) RoundTrip(r *http.Request) (*http.Response, error) {
	var b []byte
	if r.Body != nil {
		var err error
		b, err = io.ReadAll(r.Body)
		closeErr := r.Body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reqs = append(g.reqs, r)
	g.bodies = append(g.bodies, b)
	status, body := g.status, g.body
	var decoded map[string]any
	switch {
	case r.Method != http.MethodPost || r.URL.String() != "https://gw.lunaroute.com/v1/chat/completions":
		status, body = 404, "unexpected endpoint"
	case r.Header.Get("Authorization") != "Bearer "+lrChatKey || r.Header.Get("x-api-key") != "" || r.Header.Get("LUNAROUTE-API-KEY") != "":
		status, body = 401, "unexpected auth"
	case json.Unmarshal(b, &decoded) != nil || !reflect.DeepEqual(g.expected, decoded):
		status, body = 400, "unexpected chat arguments"
	default:
		if g.err != nil {
			return nil, g.err
		}
		if status == 0 {
			status = 200
			body = fmt.Sprintf(`{"model":%q,"choices":[{"message":{"role":"assistant","content":"pong"}}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`, decoded["model"])
		}
	}
	h := g.header.Clone()
	if h == nil {
		h = http.Header{"Content-Type": []string{"application/json"}}
	}
	var reader io.Reader = strings.NewReader(body)
	if g.readErr != nil {
		reader = failingChatReader{g.readErr}
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(reader), Request: r}, nil
}

type failingChatReader struct{ err error }

func (r failingChatReader) Read([]byte) (int, error) { return 0, r.err }

func newChatGW() *chatGW {
	return &chatGW{expected: chatBody("glm-5.3", lrChatPrompt, 1000, "low", false)}
}
func lrChatRequest() engine.ChatRequest {
	return engine.ChatRequest{Prompt: lrChatPrompt, MaxTokens: 1000}
}
func lrChatCaps() domprovider.CapabilityRules {
	models := map[string]domprovider.LunaRouteModel{}
	for _, id := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		models[id] = domprovider.LunaRouteModel{ID: id,
			OutputTokens: domprovider.LimitFact{Tokens: 1000, Evidence: "ev#out"},
			Efforts:      map[string]domprovider.EffortFact{"chat": {Known: true, Levels: []domprovider.Effort{domprovider.EffortLow, domprovider.EffortXHigh}, Evidence: "ev#eff"}},
		}
	}
	return domprovider.CapabilityRules{}.WithLunaRouteModels(models)
}
func lrChatBinding(base, key string, reveals *int) domprovider.ResolvedBinding {
	return domprovider.ResolvedBinding{Kind: domprovider.ProfileKindChat, Provider: "lunaroute", BaseURL: base, Model: "glm-5.3", ConnectionRef: "lr", Effort: domprovider.EffortLow,
		RevealKey: func(context.Context) (string, error) {
			if reveals != nil {
				(*reveals)++
			}
			return key, nil
		}}
}
func lrChatFactory(g *chatGW) *adapterprovider.Factory {
	return adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: &http.Client{Transport: g}, CapabilityRules: lrChatCaps()})
}
func chatProvider(t *testing.T, g *chatGW, b domprovider.ResolvedBinding) engine.ChatProvider {
	t.Helper()
	cp, err := lrChatFactory(g).ChatProviderFor(b)
	require.NoError(t, err)
	return cp
}

func TestLunaRouteChat_HappyPath(t *testing.T) {
	for _, base := range []string{"", "https://gw.lunaroute.com", "https://gw.lunaroute.com/v1/"} {
		t.Run(base, func(t *testing.T) {
			g := newChatGW()
			resp, err := chatProvider(t, g, lrChatBinding(base, lrChatKey, nil)).Chat(context.Background(), lrChatRequest())
			require.NoError(t, err)
			assert.Equal(t, "pong", resp.Output)
			assert.Equal(t, "glm-5.3", resp.Model)
			assert.Equal(t, 3, resp.TokensIn)
			assert.Equal(t, 1, resp.TokensOut)
			require.Len(t, g.reqs, 1)
			r := g.reqs[0]
			assert.Equal(t, "https://gw.lunaroute.com/v1/chat/completions", r.URL.String())
			assert.Equal(t, "Bearer "+lrChatKey, r.Header.Get("Authorization"))
			for _, h := range []string{"HTTP-Referer", "X-Title", "LUNAROUTE-PROJECT-ID", "x-api-key", "LUNAROUTE-API-KEY"} {
				assert.Empty(t, r.Header.Get(h), h)
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal(g.bodies[0], &body))
			assert.Equal(t, g.expected, body)
		})
	}
}

func TestLunaRouteChat_ClassifiedErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{401, `{"error":"echo ` + lrChatKey + ` and lr_other_token"}`, domprovider.ErrLunaRouteCredentialsRejected},
		{429, `{}`, domprovider.ErrLunaRouteBusy},
		{503, `{}`, domprovider.ErrLunaRouteServiceFailure},
		{400, `{}`, domprovider.ErrLunaRouteCauseUnknown},
		{400, `{"code":"MISSING_LUNAROUTE_HEADER"}`, domprovider.ErrLunaRouteCredentialsRejected},
		{404, `{"type":"error","error":{"code":"INFERENCE_MODEL_NOT_FOUND","message":"echo ` + lrChatKey + `"}}`, domprovider.ErrLunaRouteModelUnavailable},
		{404, `{"code":"MODEL_NOT_FOUND"}`, domprovider.ErrLunaRouteCauseUnknown},
		{429, `{"type":"error","error":{"code":"BYOK_SPEND_CAP_REACHED","message":"echo ` + lrChatKey + `"}}`, domprovider.ErrLunaRouteRequestRefused},
	} {
		t.Run(fmt.Sprint(tc.status, tc.want), func(t *testing.T) {
			g := newChatGW()
			g.status, g.body = tc.status, tc.body
			_, err := chatProvider(t, g, lrChatBinding("", lrChatKey, nil)).Chat(context.Background(), lrChatRequest())
			require.ErrorIs(t, err, tc.want)
			var failure *domprovider.LunaRouteFailure
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, domprovider.LunaRouteChat, failure.Operation)
			assert.NotContains(t, err.Error(), lrChatKey)
			assert.NotContains(t, err.Error(), "lr_other_token")
			assert.Len(t, g.reqs, 1, "no fallback or retry")
		})
	}
	g := newChatGW()
	g.err = errors.New("dial tcp: i/o timeout " + lrChatKey)
	_, err := chatProvider(t, g, lrChatBinding("", lrChatKey, nil)).Chat(context.Background(), lrChatRequest())
	require.ErrorIs(t, err, domprovider.ErrLunaRouteServiceFailure)
	assert.NotContains(t, err.Error(), lrChatKey)
}

func TestLunaRouteChat_PreflightRefusesBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domprovider.ResolvedBinding, *engine.ChatRequest)
		want   error
	}{
		{"output limit", func(_ *domprovider.ResolvedBinding, r *engine.ChatRequest) { r.MaxTokens = 1001 }, domprovider.ErrLunaRouteSettingConflict},
		{"lossy effort", func(b *domprovider.ResolvedBinding, _ *engine.ChatRequest) { b.Effort = domprovider.EffortXHigh }, domprovider.ErrLunaRouteAxisUnsupported},
		{"revoked alias", func(b *domprovider.ResolvedBinding, _ *engine.ChatRequest) { b.Model = "claude-sonnet-5-lr1" }, domprovider.ErrLunaRouteModelSpelling},
		{"open-weight alias", func(b *domprovider.ResolvedBinding, _ *engine.ChatRequest) { b.Model = "claude-opus-lr29" }, domprovider.ErrLunaRouteModelSpelling},
		{"weights name", func(b *domprovider.ResolvedBinding, _ *engine.ChatRequest) { b.Model = "deepseek-4.1-flash-1017" }, domprovider.ErrLunaRouteModelSpelling},
		{"foreign key", func(b *domprovider.ResolvedBinding, _ *engine.ChatRequest) {
			b.RevealKey = func(context.Context) (string, error) { return "sk-or-v1-foreign", nil }
		}, domprovider.ErrLunaRouteStoredCredential},
		{"tampered endpoint", func(b *domprovider.ResolvedBinding, _ *engine.ChatRequest) { b.BaseURL = "https://evil.example/v1" }, domprovider.ErrLunaRouteEndpoint},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newChatGW()
			reveals := 0
			b := lrChatBinding("", lrChatKey, &reveals)
			req := lrChatRequest()
			tc.change(&b, &req)
			cp := chatProvider(t, g, b)
			assert.Zero(t, reveals, "construction is lazy")
			_, err := cp.Chat(context.Background(), req)
			require.ErrorIs(t, err, tc.want)
			_, err = cp.StreamChat(context.Background(), req)
			require.ErrorIs(t, err, tc.want)
			assert.Empty(t, g.reqs)
			if tc.name != "foreign key" {
				assert.Zero(t, reveals, "preflight precedes reveal")
			}
		})
	}
}

func TestLunaRouteChat_RequestSettings(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		support       domprovider.Support
		present       bool
		refused       bool
	}{
		{"temperature conflict", "temperature", domprovider.SupportUnsupported, true, true},
		{"temperature unknown", "temperature", domprovider.SupportUnknown, true, false},
		{"temperature absent", "temperature", domprovider.SupportUnsupported, false, false},
		{"schema conflict", "output_schema", domprovider.SupportUnsupported, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newChatGW()
			req := lrChatRequest()
			if tc.present {
				if tc.setting == "temperature" {
					req.Temperature = 0.25
					g.expected["temperature"] = 0.25
				} else {
					req.OutputSchema = map[string]any{"type": "object"}
				}
			}
			caps := lrChatCaps()
			model, _ := caps.LunaRouteModel("glm-5.3")
			model.RequestSettings = map[string]domprovider.SupportFact{tc.setting: {Support: tc.support, Evidence: "ev#setting"}}
			caps = caps.WithLunaRouteModels(map[string]domprovider.LunaRouteModel{"glm-5.3": model})
			cp, err := adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: &http.Client{Transport: g}, CapabilityRules: caps}).ChatProviderFor(lrChatBinding("", lrChatKey, nil))
			require.NoError(t, err)
			resp, err := cp.Chat(context.Background(), req)
			if tc.refused {
				require.ErrorIs(t, err, domprovider.ErrLunaRouteSettingConflict)
				assert.Empty(t, g.reqs)
			} else {
				require.NoError(t, err)
				assert.Equal(t, "pong", resp.Output)
				assert.Len(t, g.reqs, 1)
			}
		})
	}
}

func TestLunaRouteChat_EmptyTableDoesNotAuthorizeEffort(t *testing.T) {
	g := newChatGW()
	cp, err := adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: &http.Client{Transport: g}}).ChatProviderFor(lrChatBinding("", lrChatKey, nil))
	require.NoError(t, err)
	_, err = cp.Chat(context.Background(), lrChatRequest())
	require.ErrorIs(t, err, domprovider.ErrLunaRouteAxisUnsupported)
	assert.Empty(t, g.reqs)
}

func TestLunaRouteChat_RedirectNotFollowed(t *testing.T) {
	g := newChatGW()
	g.status = 307
	g.header = http.Header{"Location": []string{"https://evil.example/v1/chat/completions"}}
	client := &http.Client{Transport: g}
	cp, err := adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: client, CapabilityRules: lrChatCaps()}).ChatProviderFor(lrChatBinding("", lrChatKey, nil))
	require.NoError(t, err)
	_, err = cp.Chat(context.Background(), lrChatRequest())
	require.ErrorIs(t, err, domprovider.ErrLunaRouteCauseUnknown)
	assert.Len(t, g.reqs, 1)
	assert.Nil(t, client.CheckRedirect, "shared client was not mutated")
}
func TestLunaRouteChat_ConstructionIsLazy(t *testing.T) {
	reveals := 0
	b := lrChatBinding("", lrChatKey, &reveals)
	b.Model = ""
	g := newChatGW()
	_ = chatProvider(t, g, b)
	assert.Zero(t, reveals)
	assert.Empty(t, g.reqs)
}

func TestLunaRouteChat_ExecutionEvidence(t *testing.T) {
	for _, tc := range []struct {
		selected, reported string
		accepted           bool
	}{
		{"glm-5.3", "glm-5.3", true}, {"glm-5.3", "glm-5.4", false},
		{"glm-5.3-flash", "glm-5.3-flash", true}, {"glm-5.3-flash", "glm-5.3", false}, {"glm-5.3-flash", "", false},
		{"deepseek-4.1-flash", "deepseek-4.1-flash", true}, {"deepseek-4.1-flash", "deepseek-4.1-flash-1017", false}, {"deepseek-4.1-flash", "claude-sonnet-5-5", false},
	} {
		t.Run(tc.selected+"/"+tc.reported, func(t *testing.T) {
			g := newChatGW()
			g.expected["model"] = tc.selected
			g.status = 200
			g.body = fmt.Sprintf(`{"model":%q,"choices":[{"message":{"content":"pong"}}]}`, tc.reported)
			b := lrChatBinding("", lrChatKey, nil)
			b.Model = tc.selected
			b.Pinned = true
			resp, err := chatProvider(t, g, b).Chat(context.Background(), lrChatRequest())
			if tc.accepted {
				require.NoError(t, err)
				assert.Equal(t, "pong", resp.Output)
				assert.Equal(t, tc.reported, resp.Model)
			} else {
				require.ErrorContains(t, err, "execution evidence")
			}
			assert.Len(t, g.reqs, 1)
		})
	}
}

func TestLunaRouteChat_ProtocolFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		readErr    error
	}{
		{"decode", `not json`, nil}, {"empty", ``, nil}, {"read", ``, errors.New("read failed " + lrChatKey)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newChatGW()
			g.status, g.body, g.readErr = 200, tc.body, tc.readErr
			_, err := chatProvider(t, g, lrChatBinding("", lrChatKey, nil)).Chat(context.Background(), lrChatRequest())
			require.ErrorIs(t, err, domprovider.ErrLunaRouteServiceFailure)
			assert.NotContains(t, err.Error(), lrChatKey)
			if tc.readErr != nil {
				assert.ErrorIs(t, err, tc.readErr)
			}
		})
	}
}

func TestLunaRouteChat_Stream(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"success", 200, "data: {\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"content\":\"pong\"}}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n", nil},
		{"status", 401, `nope`, domprovider.ErrLunaRouteCredentialsRejected},
		{"missing model frame", 200, "data: {\"error\":{\"code\":\"INFERENCE_MODEL_NOT_FOUND\",\"message\":\"" + lrChatKey + "\"}}\n", domprovider.ErrLunaRouteModelUnavailable},
		{"missing auth frame", 200, "data: {\"error\":{\"code\":\"MISSING_LUNAROUTE_HEADER\"}}\n", domprovider.ErrLunaRouteCredentialsRejected},
		{"frame", 200, "data: {\"error\":{\"code\":\"BYOK_SPEND_CAP_REACHED\",\"message\":\"" + lrChatKey + "\"}}\n", domprovider.ErrLunaRouteRequestRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newChatGW()
			g.expected["stream"] = true
			g.status, g.body = tc.status, tc.body
			g.header = http.Header{"Content-Type": []string{"text/event-stream"}}
			b := lrChatBinding("", lrChatKey, nil)
			b.Pinned = true
			ch, err := chatProvider(t, g, b).StreamChat(context.Background(), lrChatRequest())
			require.NoError(t, err)
			var content string
			var chunkErr error
			var done engine.ChatChunk
			for c := range ch {
				content += c.Token
				if c.Error != nil {
					chunkErr = c.Error
				}
				if c.Done {
					done = c
				}
			}
			if tc.want != nil {
				require.ErrorIs(t, chunkErr, tc.want)
				assert.NotContains(t, chunkErr.Error(), lrChatKey)
				assert.False(t, done.Done)
			} else {
				require.NoError(t, chunkErr)
				assert.Equal(t, "pong", content)
				assert.True(t, done.Done)
				assert.Equal(t, 3, done.TokensIn)
				assert.Equal(t, 1, done.TokensOut)
			}
			assert.Len(t, g.reqs, 1)
		})
	}
}

func TestLunaRouteChat_FakeRejectsMutatedInputs(t *testing.T) {
	for _, field := range []string{"model", "auth", "messages", "max_tokens", "reasoning_effort"} {
		for _, omit := range []bool{true, false} {
			t.Run(fmt.Sprint(field, omit), func(t *testing.T) {
				g := newChatGW()
				body := chatBody("glm-5.3", lrChatPrompt, 1000, "low", false)
				if field != "auth" {
					if omit {
						delete(body, field)
					} else {
						body[field] = "mutated"
					}
				}
				raw, err := json.Marshal(body)
				require.NoError(t, err)
				r, err := http.NewRequest(http.MethodPost, "https://gw.lunaroute.com/v1/chat/completions", bytes.NewReader(raw))
				require.NoError(t, err)
				auth := "Bearer " + lrChatKey
				if field == "auth" {
					if omit {
						auth = ""
					} else {
						auth = "Bearer lr_wrong"
					}
				}
				r.Header.Set("Authorization", auth)
				resp, err := g.RoundTrip(r)
				require.NoError(t, err)
				defer resp.Body.Close()
				assert.GreaterOrEqual(t, resp.StatusCode, 400)
			})
		}
	}
}

type nonLunaRouteChatTransport struct{}

func (nonLunaRouteChatTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodPost || r.URL.String() != "https://openrouter.ai/api/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-or-unit" {
		return nil, errors.New("unexpected OpenRouter request")
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if err := r.Body.Close(); err != nil {
		return nil, err
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || !reflect.DeepEqual(body, chatBody("x", "ping", 0, "", false)) {
		return nil, errors.New("unexpected OpenRouter body")
	}
	return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("boom")), Request: r}, nil
}
func TestOpenRouterBinding_HooksNotApplied(t *testing.T) {
	b := domprovider.ResolvedBinding{Kind: domprovider.ProfileKindChat, Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "x",
		RevealKey: func(context.Context) (string, error) { return "sk-or-unit", nil }}
	cp, err := adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: &http.Client{Transport: nonLunaRouteChatTransport{}}}).ChatProviderFor(b)
	require.NoError(t, err)
	_, err = cp.Chat(context.Background(), engine.ChatRequest{Prompt: "ping"})
	require.ErrorIs(t, err, openrouter.ErrUpstreamStatus)
	assert.NotErrorIs(t, err, domprovider.ErrLunaRouteServiceFailure)
}
