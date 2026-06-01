package azurefoundry_test

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

	"github.com/arbitron/arbitron/adapter/azurefoundry"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time assertion: *azurefoundry.Provider must satisfy engine.ChatProvider.
var _ engine.ChatProvider = (*azurefoundry.Provider)(nil)

const revealedKey = "super-secret-azure-key-DO-NOT-LOG"

// roundTripFunc is a fake http.RoundTripper seam: tests inject responses and
// capture the outgoing request, so no live network is used.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// captured records what the fake transport saw for assertions.
type captured struct {
	req  *http.Request
	body []byte
}

// newFakeClient returns an *http.Client whose transport runs fn and records the
// request (with its body read out) into cap.
func newFakeClient(cap *captured, fn func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cap.req = r
		if r.Body != nil {
			b, _ := io.ReadAll(r.Body)
			cap.body = b
			r.Body = io.NopCloser(bytes.NewReader(b))
		}
		return fn(r)
	})}
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func sseResp(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func resourceBinding() provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "azure",
		BaseURL:  "https://my-resource.openai.azure.com",
		Model:    "gpt-4o-deploy",
		Extras:   map[string]string{"api-version": "2024-08-01-preview"},
		RevealKey: func(context.Context) (string, error) {
			return revealedKey, nil
		},
	}
}

func serverlessBinding() provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "azure",
		BaseURL:  "https://llama-serverless.eastus.models.ai.azure.com",
		Model:    "llama-3.1-70b",
		RevealKey: func(context.Context) (string, error) {
			return revealedKey, nil
		},
	}
}

const okChatBody = `{
  "model": "gpt-4o-deploy",
  "choices": [{"message": {"role": "assistant", "content": "hello there"}}],
  "usage": {"prompt_tokens": 11, "completion_tokens": 7}
}`

// --- Happy path: resource topology -----------------------------------------

func TestChat_ResourceTopology_HappyPath(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, okChatBody), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi", MaxTokens: 256})
	require.NoError(t, err)

	// URL is the resource deployment path with api-version from Extras.
	assert.Equal(t,
		"https://my-resource.openai.azure.com/openai/deployments/gpt-4o-deploy/chat/completions?api-version=2024-08-01-preview",
		cap.req.URL.String())
	// api-key header carries the revealed key (NOT Authorization: Bearer).
	assert.Equal(t, revealedKey, cap.req.Header.Get("api-key"))
	assert.Empty(t, cap.req.Header.Get("Authorization"))
	assert.Equal(t, "application/json", cap.req.Header.Get("Content-Type"))
	// Body model field is the deployment name.
	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.body, &body))
	assert.Equal(t, "gpt-4o-deploy", body["model"])

	assert.Equal(t, "hello there", resp.Output)
	assert.Equal(t, 11, resp.TokensIn)
	assert.Equal(t, 7, resp.TokensOut)
}

// --- Success variants: serverless topology ---------------------------------

func TestChat_ServerlessTopology_HappyPath(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, okChatBody), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: serverlessBinding(), HTTPClient: client})

	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	// Serverless: the connection's own endpoint + /chat/completions, no api-version.
	assert.Equal(t,
		"https://llama-serverless.eastus.models.ai.azure.com/chat/completions",
		cap.req.URL.String())
	assert.Equal(t, revealedKey, cap.req.Header.Get("api-key"))
	assert.Equal(t, "hello there", resp.Output)
}

func TestChat_StructuredOutput_SetsResponseFormat(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, okChatBody), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})

	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi", OutputSchema: schema})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.body, &body))
	rf, ok := body["response_format"].(map[string]any)
	require.True(t, ok, "response_format must be set when OutputSchema present, body=%s", cap.body)
	assert.Equal(t, "json_schema", rf["type"])
}

func TestChat_NoSchema_OmitsResponseFormat(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, okChatBody), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.body, &body))
	_, has := body["response_format"]
	assert.False(t, has, "response_format must be omitted without OutputSchema")
}

func TestStreamChat_SSE_OrderedTokensThenDoneWithUsage(t *testing.T) {
	var cap captured
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		`data: {"choices":[{"delta":{"content":"!"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`data: [DONE]`,
		``,
	}, "\n\n")
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return sseResp(stream), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: serverlessBinding(), HTTPClient: client})

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
	require.True(t, sawDone, "expected a terminal Done chunk")
	assert.Equal(t, []string{"Hel", "lo", "!"}, tokens)
	assert.Equal(t, 3, done.TokensIn)
	assert.Equal(t, 2, done.TokensOut)

	// stream body must request stream:true
	var body map[string]any
	require.NoError(t, json.Unmarshal(cap.body, &body))
	assert.Equal(t, true, body["stream"])
}

func TestStreamChat_DoneOnly_SingleTerminalChunk(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return sseResp("data: [DONE]\n\n"), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: serverlessBinding(), HTTPClient: client})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	var chunks []engine.ChatChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	require.Len(t, chunks, 1)
	assert.True(t, chunks[0].Done)
	assert.NoError(t, chunks[0].Error)
}

// --- Failure: upstream non-2xx ---------------------------------------------

func TestChat_Upstream401_ReturnsTypedError(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})

	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	var apiErr *azurefoundry.APIError
	require.True(t, errors.As(err, &apiErr), "expected *APIError, got %T: %v", err, err)
	assert.Equal(t, http.StatusUnauthorized, apiErr.StatusCode)
	assert.NotContains(t, err.Error(), revealedKey)
}

func TestChat_Upstream404Deployment_ReturnsTypedError(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusNotFound, `{"error":{"message":"deployment not found"}}`), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	var apiErr *azurefoundry.APIError
	require.True(t, errors.As(err, &apiErr))
	assert.Equal(t, http.StatusNotFound, apiErr.StatusCode)
}

func TestStreamChat_MidStreamUpstreamError_ChunkThenClose(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusTooManyRequests, `{"error":{"message":"rate limited"}}`), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: serverlessBinding(), HTTPClient: client})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	var chunks []engine.ChatChunk
	for c := range ch {
		chunks = append(chunks, c)
	}
	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1]
	require.Error(t, last.Error)
	assert.NotContains(t, last.Error.Error(), revealedKey)
}

// --- Error: transport / RevealKey / request build --------------------------

func TestChat_TransportError_Propagates(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: connection refused")
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), revealedKey)
}

func TestChat_RevealKeyError_Propagates(t *testing.T) {
	b := resourceBinding()
	b.RevealKey = func(context.Context) (string, error) {
		return "", errors.New("decrypt failed")
	}
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, okChatBody), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: b, HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt failed")
	assert.Nil(t, cap.req, "no request should be sent when key reveal fails")
}

func TestStreamChat_RevealKeyError_Propagates(t *testing.T) {
	b := serverlessBinding()
	b.RevealKey = func(context.Context) (string, error) {
		return "", errors.New("decrypt failed")
	}
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return sseResp("data: [DONE]\n\n"), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: b, HTTPClient: client})
	// Either an immediate error or an error chunk is acceptable; the key must
	// never leak and no request goes out.
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	if err != nil {
		assert.Contains(t, err.Error(), "decrypt failed")
		assert.Nil(t, cap.req)
		return
	}
	var last engine.ChatChunk
	for c := range ch {
		last = c
	}
	require.Error(t, last.Error)
	assert.Contains(t, last.Error.Error(), "decrypt failed")
}

// --- Edge cases -------------------------------------------------------------

func TestChat_MissingDeployment_ResourceTopology_TypedError(t *testing.T) {
	b := resourceBinding()
	b.Model = ""
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, okChatBody), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: b, HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, azurefoundry.ErrMissingDeployment))
	assert.Nil(t, cap.req)
}

func TestChat_ServerlessAlreadySuffixed_NoDoubleSuffix(t *testing.T) {
	b := serverlessBinding()
	b.BaseURL = "https://llama-serverless.eastus.models.ai.azure.com/chat/completions"
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, okChatBody), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: b, HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)
	assert.Equal(t,
		"https://llama-serverless.eastus.models.ai.azure.com/chat/completions",
		cap.req.URL.String())
}

func TestChat_MalformedJSONBody_DecodeError(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, `{not json`), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), revealedKey)
}

func TestChat_EmptyBody_DecodeError(t *testing.T) {
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, ``), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: resourceBinding(), HTTPClient: client})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
}

func TestChat_MaxResponseBytesExceeded_BoundedReadError(t *testing.T) {
	big := `{"choices":[{"message":{"content":"` + strings.Repeat("x", 5000) + `"}}]}`
	var cap captured
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusOK, big), nil
	})
	p := azurefoundry.New(azurefoundry.Config{
		Binding:          resourceBinding(),
		HTTPClient:       client,
		MaxResponseBytes: 64,
	})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), revealedKey)
}

// Edge: a malformed JSON frame in an otherwise-200 stream delivers an Error
// chunk before the channel closes — it does not silently discard the frame.
func TestStreamChat_MalformedSSEFrame_ErrorChunk(t *testing.T) {
	var cap captured
	// A valid first token, then a malformed frame, then more tokens that are
	// never reached (channel closes on first decode error).
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n" +
		"data: {not-valid-json}\n" +
		"data: [DONE]\n\n"
	client := newFakeClient(&cap, func(*http.Request) (*http.Response, error) {
		return sseResp(stream), nil
	})
	p := azurefoundry.New(azurefoundry.Config{Binding: serverlessBinding(), HTTPClient: client})

	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.NoError(t, err)

	var sawErr bool
	for c := range ch {
		if c.Error != nil {
			sawErr = true
		}
	}
	assert.True(t, sawErr, "a malformed SSE data frame must produce an Error chunk")
}

// --- Security: revealed key never appears in logs or errors -----------------

func TestSecurity_RevealedKeyNeverLogged_AcrossBranches(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	type branch struct {
		name string
		run  func(p *azurefoundry.Provider)
	}

	mkProvider := func(b provider.ResolvedBinding, fn func(*http.Request) (*http.Response, error)) (*azurefoundry.Provider, *captured) {
		var cap captured
		return azurefoundry.New(azurefoundry.Config{Binding: b, HTTPClient: newFakeClient(&cap, fn)}), &cap
	}

	branches := []branch{
		{"chat-ok-resource", func(p *azurefoundry.Provider) {
			_, _ = p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
		}},
		{"chat-ok-serverless", func(p *azurefoundry.Provider) {
			_, _ = p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
		}},
		{"stream-ok", func(p *azurefoundry.Provider) {
			ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
			if err == nil {
				for range ch {
				}
			}
		}},
	}

	// Run an OK resource chat, an OK serverless chat, and a stream — assert each
	// captured request carried the api-key header but the buffer never did.
	for _, br := range branches {
		var p *azurefoundry.Provider
		var cap *captured
		switch br.name {
		case "stream-ok":
			p, cap = mkProvider(serverlessBinding(), func(*http.Request) (*http.Response, error) {
				return sseResp("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"), nil
			})
		case "chat-ok-serverless":
			p, cap = mkProvider(serverlessBinding(), func(*http.Request) (*http.Response, error) {
				return jsonResp(http.StatusOK, okChatBody), nil
			})
		default:
			p, cap = mkProvider(resourceBinding(), func(*http.Request) (*http.Response, error) {
				return jsonResp(http.StatusOK, okChatBody), nil
			})
		}
		br.run(p)
		// The api-key header IS set (verified via the fake transport).
		require.NotNil(t, cap.req, "branch %s sent no request", br.name)
		assert.Equal(t, revealedKey, cap.req.Header.Get("api-key"), "branch %s", br.name)
	}

	// Also run an error branch to capture any error-path logging.
	pErr, _ := mkProvider(resourceBinding(), func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusUnauthorized, `{"error":{"message":"nope"}}`), nil
	})
	_, _ = pErr.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})

	assert.NotContains(t, logBuf.String(), revealedKey, "revealed key must never appear in logs")
}
