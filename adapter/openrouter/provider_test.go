package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// secretKey is the revealed credential used across tests. The security
// assertions below verify it never leaks into logs or returned errors.
const secretKey = "sk-or-SUPERSECRET-do-not-leak-123"

// fakeTransport is the injectable http.RoundTripper seam. It records the last
// request it saw and returns a canned response (or error) — no live network.
type fakeTransport struct {
	gotReq    *http.Request
	gotBody   []byte
	resp      *http.Response
	err       error
	roundTrip func(*http.Request) (*http.Response, error)
}

func (f *fakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.gotReq = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.gotBody = b
		_ = req.Body.Close()
	}
	if f.roundTrip != nil {
		return f.roundTrip(req)
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func sseResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// staticBinding builds a ResolvedBinding whose RevealKey returns secretKey.
func staticBinding(baseURL, model string) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Kind:     provider.ProfileKindChat,
		Provider: "openrouter",
		BaseURL:  baseURL,
		Model:    model,
		RevealKey: func(_ context.Context) (string, error) {
			return secretKey, nil
		},
	}
}

// newProvider wires a Provider onto the fake transport.
func newProvider(t *testing.T, ft *fakeTransport, b provider.ResolvedBinding) *Provider {
	t.Helper()
	return New(Config{
		Binding:    b,
		HTTPClient: &http.Client{Transport: ft},
	})
}

// captureLogs redirects slog to a buffer for the duration of fn and returns
// everything logged. Used by the security assertion.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	fn()
	return buf.String()
}

// --- compile-time port assertion ---

func TestProviderImplementsPort(t *testing.T) {
	var _ engine.ChatProvider = (*Provider)(nil)
}

// --- HAPPY PATH: Chat ---

func TestChat_HappyPath(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{
		"model": "openai/gpt-4o",
		"choices": [{"index":0,"message":{"role":"assistant","content":"hello world"}}],
		"usage": {"prompt_tokens": 11, "completion_tokens": 5}
	}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "openai/gpt-4o"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{
		SystemPrompt: "be terse",
		Prompt:       "say hi",
		MaxTokens:    256,
		Temperature:  0.5,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", resp.Output)
	assert.Equal(t, "openai/gpt-4o", resp.Model)
	assert.Equal(t, 11, resp.TokensIn)
	assert.Equal(t, 5, resp.TokensOut)

	// POST to {base-url}/chat/completions.
	require.NotNil(t, ft.gotReq)
	assert.Equal(t, http.MethodPost, ft.gotReq.Method)
	assert.Equal(t, "https://router.test/v1/chat/completions", ft.gotReq.URL.String())
	assert.Equal(t, "application/json", ft.gotReq.Header.Get("Content-Type"))

	// Request body carries model + messages.
	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	assert.Equal(t, "openai/gpt-4o", body["model"])
	msgs, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, msgs, 2)
	sys := msgs[0].(map[string]any)
	assert.Equal(t, "system", sys["role"])
	assert.Equal(t, "be terse", sys["content"])
	usr := msgs[1].(map[string]any)
	assert.Equal(t, "user", usr["role"])
	assert.Equal(t, "say hi", usr["content"])
	assert.EqualValues(t, 256, body["max_tokens"])
	assert.EqualValues(t, 0.5, body["temperature"])
}

// --- SECURITY: auth header carries revealed key (verified via transport) ---

func TestChat_AuthHeaderCarriesRevealedKey(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}]}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	require.NotNil(t, ft.gotReq)
	assert.Equal(t, "Bearer "+secretKey, ft.gotReq.Header.Get("Authorization"))
}

// --- SUCCESS VARIANT: omits system message when SystemPrompt empty ---

func TestChat_OmitsSystemMessageWhenEmpty(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}]}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "only user"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	msgs := body["messages"].([]any)
	require.Len(t, msgs, 1)
	assert.Equal(t, "user", msgs[0].(map[string]any)["role"])
	// zero max_tokens / temperature are omitted.
	_, hasMax := body["max_tokens"]
	assert.False(t, hasMax)
	_, hasTemp := body["temperature"]
	assert.False(t, hasTemp)
}

// --- SUCCESS VARIANT: structured output via response_format ---

func TestChat_StructuredOutput(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{
		"choices":[{"message":{"content":"{\"answer\":42}"}}],
		"usage":{"prompt_tokens":3,"completion_tokens":4}
	}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": map[string]any{"type": "integer"}},
	}
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "q", OutputSchema: schema})
	require.NoError(t, err)

	// response_format block present and json_schema typed.
	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	rf, ok := body["response_format"].(map[string]any)
	require.True(t, ok, "response_format must be set when OutputSchema is non-nil")
	assert.Equal(t, "json_schema", rf["type"])
	js, ok := rf["json_schema"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, schema, js["schema"])

	// Structured content is parsed back into Output.
	out, ok := resp.Output.(map[string]any)
	require.True(t, ok, "structured output should be parsed JSON, got %T", resp.Output)
	assert.EqualValues(t, 42, out["answer"])
}

// --- SUCCESS VARIANT: StreamChat SSE chunking -> Done with usage ---

func TestStreamChat_SSEChunking(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"!"}}]}`,
		``,
		`data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":9,"completion_tokens":3}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	var tokens []string
	var done engine.ChatChunk
	var sawDone bool
	for c := range ch {
		require.NoError(t, c.Error)
		if c.Done {
			done = c
			sawDone = true
			continue
		}
		if c.Token != "" {
			tokens = append(tokens, c.Token)
		}
	}
	assert.Equal(t, []string{"Hel", "lo", "!"}, tokens)
	require.True(t, sawDone, "expected a terminal Done chunk")
	assert.Equal(t, 9, done.TokensIn)
	assert.Equal(t, 3, done.TokensOut)

	// stream:true requested.
	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	assert.Equal(t, true, body["stream"])
	// Auth header still present on the stream request.
	assert.Equal(t, "Bearer "+secretKey, ft.gotReq.Header.Get("Authorization"))
}

// --- FAILURE: upstream non-2xx for Chat ---

func TestChat_UpstreamNon2xx(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		ft := &fakeTransport{resp: jsonResponse(status, `{"error":{"message":"nope"}}`)}
		p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

		resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Nil(t, resp)
		assert.ErrorIs(t, err, ErrUpstreamStatus)
		assert.NotContains(t, err.Error(), secretKey)
	}
}

// --- FAILURE: mid-stream upstream non-2xx surfaces as Error chunk, channel closes ---

func TestStreamChat_UpstreamNon2xx(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(500, `{"error":"boom"}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err, "transport succeeds; the upstream status is delivered on the channel")

	var sawErr bool
	for c := range ch {
		if c.Error != nil {
			sawErr = true
			assert.ErrorIs(t, c.Error, ErrUpstreamStatus)
			assert.NotContains(t, c.Error.Error(), secretKey)
		}
	}
	assert.True(t, sawErr, "expected an Error chunk before the channel closed")
}

// --- ERROR: transport error propagates ---

func TestChat_TransportError(t *testing.T) {
	ft := &fakeTransport{err: errors.New("dial tcp: connection refused")}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.NotContains(t, err.Error(), secretKey)
}

// --- ERROR: RevealKey failure propagates ---

func TestChat_RevealKeyError(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}]}`)}
	b := staticBinding("https://router.test/v1", "m")
	b.RevealKey = func(_ context.Context) (string, error) {
		return "", errors.New("credential unavailable")
	}
	p := newProvider(t, ft, b)

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "credential unavailable")
	// transport must not have been called.
	assert.Nil(t, ft.gotReq)
}

// --- ERROR: RevealKey failure on StreamChat propagates ---

func TestStreamChat_RevealKeyError(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(200, "data: [DONE]\n\n")}
	b := staticBinding("https://router.test/v1", "m")
	b.RevealKey = func(_ context.Context) (string, error) {
		return "", errors.New("credential unavailable")
	}
	p := newProvider(t, ft, b)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	// Either a returned error or an error chunk is acceptable; assert one happens.
	if err != nil {
		assert.Contains(t, err.Error(), "credential unavailable")
		assert.Nil(t, ch)
		return
	}
	var sawErr bool
	for c := range ch {
		if c.Error != nil {
			sawErr = true
			assert.Contains(t, c.Error.Error(), "credential unavailable")
		}
	}
	assert.True(t, sawErr)
}

// --- EDGE: empty / [DONE]-only stream -> single terminal Done, no tokens ---

func TestStreamChat_DoneOnly(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(200, "data: [DONE]\n\n")}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)

	var chunks []engine.ChatChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	require.Len(t, chunks, 1)
	assert.True(t, chunks[0].Done)
	assert.Equal(t, "", chunks[0].Token)
	assert.Equal(t, 0, chunks[0].TokensIn)
	assert.Equal(t, 0, chunks[0].TokensOut)
}

// --- EDGE: malformed JSON body -> decode error ---

func TestChat_MalformedBody(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{not json`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.NotContains(t, err.Error(), secretKey)
}

// --- EDGE: empty body -> decode error ---

func TestChat_EmptyBody(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, ``)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// --- EDGE: missing choices array -> error ---

func TestChat_MissingChoices(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"model":"m","usage":{"prompt_tokens":1,"completion_tokens":1}}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// --- EDGE: MaxResponseBytes exceeded -> bounded read error ---

func TestChat_MaxResponseBytesExceeded(t *testing.T) {
	big := `{"choices":[{"message":{"content":"` + strings.Repeat("A", 1000) + `"}}]}`
	ft := &fakeTransport{resp: jsonResponse(200, big)}
	p := New(Config{
		Binding:          staticBinding("https://router.test/v1", "m"),
		HTTPClient:       &http.Client{Transport: ft},
		MaxResponseBytes: 32,
	})

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
}

// --- EDGE: MaxResponseBytes exceeded in stream -> error chunk, channel closes ---

func TestStreamChat_MaxResponseBytesExceeded(t *testing.T) {
	// Build a stream body far larger than the cap (each event ~47 bytes × 50).
	sse := strings.Repeat(`data: {"choices":[{"delta":{"content":"X"}}]}`+"\n\n", 50)
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := New(Config{
		Binding:          staticBinding("https://router.test/v1", "m"),
		HTTPClient:       &http.Client{Transport: ft},
		MaxResponseBytes: 32,
	})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)

	var sawErr bool
	for c := range ch {
		if c.Error != nil {
			sawErr = true
		}
	}
	assert.True(t, sawErr, "expected an error chunk when MaxResponseBytes is exceeded in a stream")
}

// --- LiteLLM: a router base-url resolves through this same adapter (no special code) ---

func TestChat_LiteLLMRouterBaseURL(t *testing.T) {
	// LiteLLM is just a connection whose base-url points at the router. No
	// adapter code is needed beyond honouring the configured base-url.
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"routed"}}],"model":"team/gpt"}`)}
	p := newProvider(t, ft, staticBinding("http://litellm.internal:4000", "team/gpt"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "routed", resp.Output)
	assert.Equal(t, "http://litellm.internal:4000/chat/completions", ft.gotReq.URL.String())
}

// --- SECURITY: a non-http(s) base-url scheme is rejected at request build ---

func TestChat_RejectsNonHTTPScheme(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"x"}}]}`)}
	p := newProvider(t, ft, staticBinding("file:///etc/passwd", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.ErrorIs(t, err, ErrUnsupportedScheme)
	assert.Nil(t, resp)
	assert.Nil(t, ft.gotReq, "request must never reach the transport on a bad scheme")
}

// --- base-url trailing slash is trimmed ---

func TestChat_BaseURLTrailingSlash(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}]}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1/", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "https://router.test/v1/chat/completions", ft.gotReq.URL.String())
}

// --- SECURITY: revealed key never appears in any log output, across branches ---

func TestSecurity_KeyNeverLogged(t *testing.T) {
	branches := map[string]func(p *Provider){
		"chat_success": func(p *Provider) {
			_, _ = p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		},
		"chat_non2xx": func(p *Provider) {
			_, _ = p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		},
		"chat_malformed": func(p *Provider) {
			_, _ = p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		},
		"stream": func(p *Provider) {
			ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
			if err == nil {
				for range ch { //nolint:revive // drain
				}
			}
		},
	}

	responders := map[string]func() *fakeTransport{
		"chat_success":   func() *fakeTransport { return &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}]}`)} },
		"chat_non2xx":    func() *fakeTransport { return &fakeTransport{resp: jsonResponse(401, `{"error":"`+secretKey+`-ish"}`)} },
		"chat_malformed": func() *fakeTransport { return &fakeTransport{resp: jsonResponse(200, `{bad`)} },
		"stream": func() *fakeTransport {
			return &fakeTransport{resp: sseResponse(200, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")}
		},
	}

	for name, run := range branches {
		t.Run(name, func(t *testing.T) {
			ft := responders[name]()
			p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))
			logs := captureLogs(t, func() { run(p) })
			assert.NotContains(t, logs, secretKey, "revealed key must never appear in logs")
		})
	}
}

// --- EDGE: nil RevealKey (public/no-key endpoint) -> no Authorization header ---

func TestChat_NilRevealKey_NoAuthHeader(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"ok"}}]}`)}
	b := provider.ResolvedBinding{
		Kind:      provider.ProfileKindChat,
		Provider:  "openrouter",
		BaseURL:   "https://router.test/v1",
		Model:     "m",
		RevealKey: nil, // explicitly no key
	}
	p := newProvider(t, ft, b)

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	require.NotNil(t, ft.gotReq)
	assert.Empty(t, ft.gotReq.Header.Get("Authorization"),
		"no Authorization header when RevealKey is nil")
}

// --- EDGE: structured output requested but model returns invalid JSON content ---

func TestChat_StructuredOutput_ContentNotJSON(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"choices":[{"message":{"content":"I cannot produce JSON"}}]}`)}
	p := newProvider(t, ft, staticBinding("https://router.test/v1", "m"))

	schema := map[string]any{"type": "object"}
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "q", OutputSchema: schema})
	require.Error(t, err, "non-JSON content with OutputSchema must be an error")
	assert.Nil(t, resp)
	assert.NotContains(t, err.Error(), secretKey)
}

// --- parseSSELine unit table (stream parsing is table-testable) ---

func TestParseSSELine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantDelta string
		wantDone  bool
		wantUsage bool
		wantErr   bool
	}{
		{name: "token delta", line: `data: {"choices":[{"delta":{"content":"hi"}}]}`, wantDelta: "hi"},
		{name: "done sentinel", line: `data: [DONE]`, wantDone: true},
		{name: "usage frame", line: `data: {"choices":[{"delta":{}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`, wantUsage: true},
		{name: "blank line", line: ``},
		{name: "comment line", line: `: keep-alive`},
		{name: "non-data line", line: `event: message`},
		{name: "malformed json", line: `data: {nope`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			delta, usage, done, err := parseSSELine([]byte(tc.line))
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantDelta, delta)
			assert.Equal(t, tc.wantDone, done)
			if tc.wantUsage {
				require.NotNil(t, usage)
			} else {
				assert.Nil(t, usage)
			}
		})
	}
}
