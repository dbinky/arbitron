package anthropicapi

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

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// secretKey is the revealed credential used across tests. The security
// assertions below verify it never leaks into logs or returned errors.
const secretKey = "sk-ant-SUPERSECRET-do-not-leak-123"

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
		Provider: "anthropic-api",
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

// SECURITY (OOM DoS): a configured MaxResponseBytes is carried into the Provider
// so the response-body read is bounded.
func TestNew_CarriesMaxResponseBytes(t *testing.T) {
	const capBytes = int64(1 << 20)
	p := New(Config{MaxResponseBytes: capBytes})
	assert.Equal(t, capBytes, p.maxResponseBytes,
		"the configured MaxResponseBytes must be stored on the Provider")
}

// =====================================================================
// CATEGORY: Happy-path (non-streaming Chat)
// =====================================================================

func TestChat_HappyPath(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{
		"model": "claude-3-5-sonnet",
		"content": [{"type":"text","text":"hello world"}],
		"usage": {"input_tokens": 11, "output_tokens": 5}
	}`)}
	p := newProvider(t, ft, staticBinding("", "claude-3-5-sonnet"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{
		SystemPrompt: "be terse",
		Prompt:       "say hi",
		MaxTokens:    256,
		Temperature:  0.5,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello world", resp.Output)
	assert.Equal(t, "claude-3-5-sonnet", resp.Model)
	assert.Equal(t, 11, resp.TokensIn)
	assert.Equal(t, 5, resp.TokensOut)

	// POST to {default-base}/v1/messages.
	require.NotNil(t, ft.gotReq)
	assert.Equal(t, http.MethodPost, ft.gotReq.Method)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", ft.gotReq.URL.String())
	assert.Equal(t, "application/json", ft.gotReq.Header.Get("Content-Type"))

	// Request body carries model + single user message + system + temperature.
	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	assert.Equal(t, "claude-3-5-sonnet", body["model"])
	assert.EqualValues(t, 256, body["max_tokens"])
	assert.EqualValues(t, 0.5, body["temperature"])
	assert.Equal(t, "be terse", body["system"])
	msgs, ok := body["messages"].([]any)
	require.True(t, ok)
	require.Len(t, msgs, 1)
	usr := msgs[0].(map[string]any)
	assert.Equal(t, "user", usr["role"])
	assert.Equal(t, "say hi", usr["content"])
}

func TestChat_MultiTextBlockConcatenation(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{
		"model":"m",
		"content":[{"type":"text","text":"foo"},{"type":"text","text":"bar"}],
		"usage":{"input_tokens":1,"output_tokens":1}
	}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "foobar", resp.Output)
}

// =====================================================================
// CATEGORY: Input-validation
// =====================================================================

func TestChat_DefaultMaxTokensWhenZero(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x", MaxTokens: 0})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	v, has := body["max_tokens"]
	require.True(t, has, "max_tokens must ALWAYS be present")
	assert.EqualValues(t, defaultMaxTokens, v)
}

func TestChat_OmitsSystemWhenEmpty(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "only user"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	_, hasSystem := body["system"]
	assert.False(t, hasSystem, "system must be omitted when SystemPrompt is empty")
	_, hasTemp := body["temperature"]
	assert.False(t, hasTemp, "temperature must be omitted when zero")
}

func TestChat_StructuredOutput_EmitsForcedTool(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{
		"content":[{"type":"tool_use","name":"structured_output","input":{"answer":42}}],
		"usage":{"input_tokens":3,"output_tokens":4}
	}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"answer": map[string]any{"type": "integer"}},
	}
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "q", OutputSchema: schema})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	_, hasRF := body["response_format"]
	assert.False(t, hasRF, "Anthropic has NO response_format")

	tools, ok := body["tools"].([]any)
	require.True(t, ok, "tools must be present for structured output")
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "structured_output", tool["name"])
	assert.Equal(t, schema, tool["input_schema"])

	tc, ok := body["tool_choice"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "tool", tc["type"])
	assert.Equal(t, "structured_output", tc["name"])

	// The forced tool-use input is returned as a RAW string pre-parse.
	s, ok := resp.Output.(string)
	require.True(t, ok, "structured output must be returned as a raw string, got %T", resp.Output)
	assert.JSONEq(t, `{"answer":42}`, s)
}

func TestChat_StructuredOutput_NonForcedTextReturnedRaw(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"I cannot produce JSON"}]}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	schema := map[string]any{"type": "object"}
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "q", OutputSchema: schema})
	require.NoError(t, err, "the adapter returns raw content; parsing is the executor's job")
	require.NotNil(t, resp)
	s, ok := resp.Output.(string)
	require.True(t, ok, "Output must be the raw string, got %T", resp.Output)
	assert.Equal(t, "I cannot produce JSON", s)
}

// =====================================================================
// CATEGORY: Error-handling
// =====================================================================

func TestChat_UpstreamNon2xx(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		ft := &fakeTransport{resp: jsonResponse(status, `{"type":"error","error":{"message":"nope"}}`)}
		p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

		resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		require.Error(t, err)
		assert.Nil(t, resp)
		assert.ErrorIs(t, err, ErrUpstreamStatus)
		assert.NotContains(t, err.Error(), secretKey)
		// anthropic-version still set on the (failed) request.
		require.NotNil(t, ft.gotReq)
		assert.Equal(t, anthropicVersion, ft.gotReq.Header.Get("anthropic-version"))
	}
}

func TestChat_TransportError(t *testing.T) {
	ft := &fakeTransport{err: errors.New("dial tcp: connection refused")}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.NotContains(t, err.Error(), secretKey)
}

func TestChat_MalformedBody(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{not json`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.NotContains(t, err.Error(), secretKey)
}

func TestChat_EmptyBody(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, ``)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestChat_MissingContent(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"model":"m","usage":{"input_tokens":1,"output_tokens":1}}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "no content")
}

func TestChat_RevealKeyError(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	b := staticBinding("https://api.anthropic.com", "m")
	b.RevealKey = func(_ context.Context) (string, error) {
		return "", errors.New("credential unavailable")
	}
	p := newProvider(t, ft, b)

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Contains(t, err.Error(), "credential unavailable")
	assert.Nil(t, ft.gotReq, "transport must not be called when reveal fails")
}

// =====================================================================
// CATEGORY: Edge-cases
// =====================================================================

func TestChat_BaseURLDefaultWhenEmpty(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	p := newProvider(t, ft, staticBinding("", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", ft.gotReq.URL.String())
}

func TestChat_BaseURLTrailingSlashTrimmed(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com/", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "https://api.anthropic.com/v1/messages", ft.gotReq.URL.String())
}

func TestChat_AnthropicVersionHeaderAlwaysPresent(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	require.NotNil(t, ft.gotReq)
	assert.Equal(t, "2023-06-01", ft.gotReq.Header.Get("anthropic-version"))
	assert.Equal(t, anthropicVersion, ft.gotReq.Header.Get("anthropic-version"))
}

func TestChat_ContentTypeJSON(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	assert.Equal(t, "application/json", ft.gotReq.Header.Get("Content-Type"))
}

// =====================================================================
// CATEGORY: Security
// =====================================================================

func TestChat_APIKeySetsXApiKeyOnly(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	require.NotNil(t, ft.gotReq)
	assert.Equal(t, secretKey, ft.gotReq.Header.Get("x-api-key"))
	assert.Equal(t, "", ft.gotReq.Header.Get("Authorization"),
		"Anthropic uses x-api-key, never Bearer")
}

func TestChat_MissingKey_FailsClosedBeforeCall(t *testing.T) {
	cases := map[string]func(ctx context.Context) (string, error){
		"nil RevealKey":        nil,
		"empty result, no err": func(_ context.Context) (string, error) { return "", nil },
		"whitespace result":    func(_ context.Context) (string, error) { return "   ", nil },
	}
	for name, rk := range cases {
		t.Run(name, func(t *testing.T) {
			ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)}
			b := staticBinding("https://api.anthropic.com", "m")
			b.RevealKey = rk
			p := newProvider(t, ft, b)

			resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrMissingAPIKey)
			assert.Nil(t, resp)
			assert.Nil(t, ft.gotReq, "request must never reach the transport without a key")
		})
	}
}

func TestSecurity_KeyNeverLogged(t *testing.T) {
	branches := map[string]func() *fakeTransport{
		"chat_success":   func() *fakeTransport { return &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"ok"}]}`)} },
		"chat_non2xx":    func() *fakeTransport { return &fakeTransport{resp: jsonResponse(401, `{"error":{"message":"`+secretKey+`-ish"}}`)} },
		"chat_malformed": func() *fakeTransport { return &fakeTransport{resp: jsonResponse(200, `{bad`)} },
	}
	for name, mk := range branches {
		t.Run(name, func(t *testing.T) {
			ft := mk()
			p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))
			logs := captureLogs(t, func() {
				_, _ = p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
			})
			assert.NotContains(t, logs, secretKey, "revealed key must never appear in logs")
		})
	}
}

func TestStatusError_ScrubsControlCharacters(t *testing.T) {
	dirty := "bad\nrequest\r\twith\x1b[31mescape\x00null"
	ft := &fakeTransport{resp: jsonResponse(500, dirty)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.ErrorIs(t, err, ErrUpstreamStatus)
	msg := err.Error()
	assert.NotContains(t, msg, "\n")
	assert.NotContains(t, msg, "\r")
	assert.NotContains(t, msg, "\x1b")
	assert.NotContains(t, msg, "\x00")
	assert.Contains(t, msg, "request")
	assert.Contains(t, msg, "escape")
}

// SECURITY (direct unit): scrubControl is the chokepoint that prevents hostile
// upstream bodies from injecting control characters into structured log output.
// This table pins every character category so a regression in the function
// itself is caught independently of the integration paths above.
func TestScrubControl(t *testing.T) {
	cases := []struct {
		input string
		want  string
		desc  string
	}{
		{"", "", "empty string unchanged"},
		{"hello world", "hello world", "printable ASCII preserved"},
		{"a\x00b", "a b", "NUL (0x00) → space"},
		{"a\tb", "a b", "TAB (0x09) → space"},
		{"a\nb", "a b", "LF (0x0A) → space"},
		{"a\rb", "a b", "CR (0x0D) → space"},
		{"a\x1bb", "a b", "ESC (0x1B) → space"},
		{"a\x7fb", "a b", "DEL (0x7F) → space"},
		{"\x00\t\n\r\x1b\x7f", "      ", "sequence of control chars all → space"},
		{"café", "café", "valid UTF-8 multibyte preserved"},
		{"你好世界", "你好世界", "CJK runes preserved"},
		// Verify the printable boundary: 0x1F is control, 0x20 (space) is printable.
		{"a\x1fb", "a b", "0x1F (unit sep) → space"},
		{"a b", "a b", "0x20 (space) preserved as-is"},
	}
	for _, c := range cases {
		got := procenv.ScrubControl(c.input)
		assert.Equal(t, c.want, got, c.desc)
	}
}

func TestChat_MaxResponseBytesExceeded(t *testing.T) {
	big := `{"content":[{"type":"text","text":"` + strings.Repeat("A", 1000) + `"}]}`
	ft := &fakeTransport{resp: jsonResponse(200, big)}
	p := New(Config{
		Binding:          staticBinding("https://api.anthropic.com", "m"),
		HTTPClient:       &http.Client{Transport: ft},
		MaxResponseBytes: 32,
	})

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.Nil(t, resp)
}

func TestChat_RejectsNonHTTPScheme(t *testing.T) {
	ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"x"}]}`)}
	p := newProvider(t, ft, staticBinding("file:///etc/passwd", "m"))

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.ErrorIs(t, err, ErrUnsupportedScheme)
	assert.Nil(t, resp)
	assert.Nil(t, ft.gotReq, "request must never reach the transport on a bad scheme")
}

// =====================================================================
// Unit tables (build / decode helpers)
// =====================================================================

func TestBuildBody_StructuredOutputShape(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}
	raw, err := buildBody(engine.ChatRequest{Prompt: "q", OutputSchema: schema}, "m", false)
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	_, hasRF := body["response_format"]
	assert.False(t, hasRF)
	tools := body["tools"].([]any)
	require.Len(t, tools, 1)
	tool := tools[0].(map[string]any)
	assert.Equal(t, "structured_output", tool["name"])
	assert.Equal(t, schema, tool["input_schema"])
	tc := body["tool_choice"].(map[string]any)
	assert.Equal(t, "tool", tc["type"])
	assert.Equal(t, "structured_output", tc["name"])
	// max_tokens always present.
	assert.EqualValues(t, defaultMaxTokens, body["max_tokens"])
}

func TestMessagesOutput_PrefersToolUseOverText(t *testing.T) {
	m := &messagesResponse{
		Content: []contentBlock{
			{Type: "text", Text: "ignored"},
			{Type: "tool_use", Name: "structured_output", Input: json.RawMessage(`{"answer":42}`)},
		},
	}
	out, err := m.output()
	require.NoError(t, err)
	assert.JSONEq(t, `{"answer":42}`, out)
}

// edge: when the forced-tool block carries a nil, empty, or whitespace-only
// Input it is skipped (bytes.TrimSpace check). If there are no text blocks
// either, output() returns an error — not a silent empty string.
func TestMessagesOutput_ToolUseEmptyInput_NoTextFallback_Errors(t *testing.T) {
	cases := []json.RawMessage{
		nil,
		json.RawMessage(""),
		json.RawMessage("   "),
	}
	for _, input := range cases {
		m := &messagesResponse{
			Content: []contentBlock{
				{Type: "tool_use", Name: "structured_output", Input: input},
			},
		}
		_, err := m.output()
		require.Error(t, err, "expected error for empty/whitespace Input = %q", input)
		assert.Contains(t, err.Error(), "no text or tool_use content")
	}
}

// edge: JSON literal `null` (RawMessage("null")) is non-empty after
// bytes.TrimSpace, so output() treats it as a valid (if unusual) tool-use
// input and returns the raw string "null" for the executor to parse.
func TestMessagesOutput_ToolUseNullInput_ReturnedVerbatim(t *testing.T) {
	m := &messagesResponse{
		Content: []contentBlock{
			{Type: "tool_use", Name: "structured_output", Input: json.RawMessage("null")},
		},
	}
	out, err := m.output()
	require.NoError(t, err)
	assert.Equal(t, "null", out)
}

// =====================================================================
// CATEGORY: Happy-path (streaming)
// =====================================================================

func TestStreamChat_SSEChunking(t *testing.T) {
	sse := strings.Join([]string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"usage":{"input_tokens":9,"output_tokens":0}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"!"}}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	var tokens []string
	var done engine.ChatChunk
	var doneCount int
	for c := range ch {
		require.NoError(t, c.Error)
		if c.Done {
			done = c
			doneCount++
			continue
		}
		if c.Token != "" {
			tokens = append(tokens, c.Token)
		}
	}
	assert.Equal(t, []string{"Hel", "lo", "!"}, tokens)
	assert.Equal(t, 1, doneCount, "exactly one terminal Done chunk")
	assert.Equal(t, 9, done.TokensIn)
	assert.Equal(t, 3, done.TokensOut)

	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	assert.Equal(t, true, body["stream"])
	assert.Equal(t, secretKey, ft.gotReq.Header.Get("x-api-key"))
	assert.Equal(t, "2023-06-01", ft.gotReq.Header.Get("anthropic-version"))
}

// =====================================================================
// CATEGORY: Input-validation (streaming)
// =====================================================================

func TestStreamChat_StructuredOutput_InputJSONDelta(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"answer\":"}}`,
		``,
		`data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"42}"}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	schema := map[string]any{"type": "object"}
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "q", OutputSchema: schema})
	require.NoError(t, err)

	var sb strings.Builder
	for c := range ch {
		require.NoError(t, c.Error)
		sb.WriteString(c.Token)
	}
	assert.JSONEq(t, `{"answer":42}`, sb.String())

	var body map[string]any
	require.NoError(t, json.Unmarshal(ft.gotBody, &body))
	tools := body["tools"].([]any)
	require.Len(t, tools, 1)
	assert.Equal(t, "structured_output", tools[0].(map[string]any)["name"])
}

// =====================================================================
// CATEGORY: Error-handling (streaming)
// =====================================================================

func TestStreamChat_UpstreamNon2xx(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(500, `{"type":"error","error":{"message":"boom"}}`)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err, "transport succeeds; upstream status delivered on the channel")

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

func TestStreamChat_MidStreamErrorEvent(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hi"}}`,
		``,
		`event: error`,
		`data: {"type":"error","error":{"message":"overloaded"}}`,
		``,
	}, "\n")
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)

	var sawErr bool
	for c := range ch {
		if c.Error != nil {
			sawErr = true
			assert.Contains(t, c.Error.Error(), "overloaded")
			assert.NotContains(t, c.Error.Error(), secretKey)
		}
	}
	assert.True(t, sawErr, "expected an Error chunk for the mid-stream error event")
}

func TestStreamChat_MalformedDataFrame(t *testing.T) {
	sse := "data: {nope\n\n"
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)

	var sawErr bool
	for c := range ch {
		if c.Error != nil {
			sawErr = true
		}
	}
	assert.True(t, sawErr, "expected an Error chunk for a malformed data frame")
}

func TestStreamChat_RevealKeyError(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(200, "data: {\"type\":\"message_stop\"}\n\n")}
	b := staticBinding("https://api.anthropic.com", "m")
	b.RevealKey = func(_ context.Context) (string, error) {
		return "", errors.New("credential unavailable")
	}
	p := newProvider(t, ft, b)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	if err != nil {
		assert.Contains(t, err.Error(), "credential unavailable")
		assert.Nil(t, ch)
		assert.Nil(t, ft.gotReq)
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

// =====================================================================
// CATEGORY: Edge-cases (streaming)
// =====================================================================

func TestStreamChat_StopOnly(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(200, "data: {\"type\":\"message_stop\"}\n\n")}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

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

func TestStreamChat_IgnoresPingAndComments(t *testing.T) {
	sse := strings.Join([]string{
		`: keep-alive`,
		`event: ping`,
		`data: {"type":"ping"}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"A"}}`,
		``,
		`: another comment`,
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"B"}}`,
		``,
		`data: {"type":"message_stop"}`,
		``,
	}, "\n")
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)

	var tokens []string
	for c := range ch {
		require.NoError(t, c.Error)
		if c.Token != "" {
			tokens = append(tokens, c.Token)
		}
	}
	assert.Equal(t, []string{"A", "B"}, tokens)
}

func TestStreamChat_AnthropicVersionHeaderPresent(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(200, "data: {\"type\":\"message_stop\"}\n\n")}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	for range ch { //nolint:revive // drain
	}
	require.NotNil(t, ft.gotReq)
	assert.Equal(t, "2023-06-01", ft.gotReq.Header.Get("anthropic-version"))
}

// =====================================================================
// CATEGORY: Security (streaming)
// =====================================================================

func TestStreamChat_XApiKeyOnly_NoAuthHeader(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(200, "data: {\"type\":\"message_stop\"}\n\n")}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)
	for range ch { //nolint:revive // drain
	}
	require.NotNil(t, ft.gotReq)
	assert.Equal(t, secretKey, ft.gotReq.Header.Get("x-api-key"))
	assert.Equal(t, "", ft.gotReq.Header.Get("Authorization"))
}

func TestStreamChat_MissingKey_FailsClosedBeforeCall(t *testing.T) {
	ft := &fakeTransport{resp: sseResponse(200, "data: {\"type\":\"message_stop\"}\n\n")}
	b := staticBinding("https://api.anthropic.com", "m")
	b.RevealKey = nil
	p := newProvider(t, ft, b)

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingAPIKey)
	assert.Nil(t, ch)
	assert.Nil(t, ft.gotReq, "request must never reach the transport without a key")
}

func TestStreamChat_MaxResponseBytesExceeded(t *testing.T) {
	sse := strings.Repeat(`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"X"}}`+"\n\n", 50)
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := New(Config{
		Binding:          staticBinding("https://api.anthropic.com", "m"),
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
	assert.True(t, sawErr, "expected an error chunk when MaxResponseBytes is exceeded")
}

func TestStreamChat_KeyNeverLogged(t *testing.T) {
	branches := map[string]func() *fakeTransport{
		"stream_success": func() *fakeTransport {
			return &fakeTransport{resp: sseResponse(200, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")}
		},
		"stream_error_event": func() *fakeTransport {
			return &fakeTransport{resp: sseResponse(200, `data: {"type":"error","error":{"message":"`+secretKey+`-ish"}}`+"\n\n")}
		},
	}
	for name, mk := range branches {
		t.Run(name, func(t *testing.T) {
			ft := mk()
			p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))
			logs := captureLogs(t, func() {
				ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
				if err == nil {
					for range ch { //nolint:revive // drain
					}
				}
			})
			assert.NotContains(t, logs, secretKey)
		})
	}
}

func TestStreamChat_ScrubsControlCharsInErrorEvent(t *testing.T) {
	sse := "data: {\"type\":\"error\",\"error\":{\"message\":\"bad\\nreq\\r\\tesc\\u001b[31m\\u0000 end\"}}\n\n"
	ft := &fakeTransport{resp: sseResponse(200, sse)}
	p := newProvider(t, ft, staticBinding("https://api.anthropic.com", "m"))

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	require.NoError(t, err)

	var sawErr bool
	for c := range ch {
		if c.Error != nil {
			sawErr = true
			msg := c.Error.Error()
			assert.NotContains(t, msg, "\n")
			assert.NotContains(t, msg, "\r")
			assert.NotContains(t, msg, "\x1b")
			assert.NotContains(t, msg, "\x00")
			assert.Contains(t, msg, "bad")
			assert.Contains(t, msg, "end")
		}
	}
	assert.True(t, sawErr)
}

// =====================================================================
// Unit table: parseSSEEvent
// =====================================================================

func TestParseSSEEvent(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantTok   string
		wantUsage bool
		wantDone  bool
		wantErr   bool
	}{
		{name: "text_delta", line: `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`, wantTok: "hi"},
		{name: "input_json_delta", line: `data: {"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`, wantTok: `{"a":1}`},
		{name: "message_delta usage", line: `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`, wantUsage: true},
		{name: "message_start usage", line: `data: {"type":"message_start","message":{"usage":{"input_tokens":9}}}`, wantUsage: true},
		{name: "message_stop done", line: `data: {"type":"message_stop"}`, wantDone: true},
		{name: "ping ignored", line: `data: {"type":"ping"}`},
		{name: "event line updates curEvent", line: `event: content_block_delta`},
		{name: "blank line", line: ``},
		{name: "comment line", line: `: keep-alive`},
		{name: "error event", line: `data: {"type":"error","error":{"message":"overloaded"}}`, wantErr: true},
		{name: "malformed json", line: `data: {nope`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cur string
			tok, usage, done, err := parseSSEEvent([]byte(tc.line), &cur)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTok, tok)
			assert.Equal(t, tc.wantDone, done)
			if tc.wantUsage {
				require.NotNil(t, usage)
			} else {
				assert.Nil(t, usage)
			}
		})
	}
}

func TestParseSSEEvent_EventLineSetsCurEvent(t *testing.T) {
	cur := ""
	_, _, _, err := parseSSEEvent([]byte("event: message_stop"), &cur)
	require.NoError(t, err)
	assert.Equal(t, "message_stop", cur)
}
