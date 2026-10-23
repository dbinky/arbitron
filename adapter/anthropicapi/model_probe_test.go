package anthropicapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const probeModel = "claude-sonnet-4-6"

func TestModelProbeAnthropicExactExecution(t *testing.T) {
	for _, tc := range []struct {
		name, model, role, kind string
		ok                      bool
	}{
		{"exact", probeModel, "assistant", "message", true},
		{"wrong", "fallback-secret", "assistant", "message", false},
		{"missing", "", "assistant", "message", false},
		{"prefix", "anthropic/" + probeModel, "assistant", "message", false},
		{"snapshot", probeModel + "-20260217", "assistant", "message", false},
		{"request echo", probeModel, "user", "message", false},
		{"missing role", probeModel, "", "message", false},
		{"missing type", probeModel, "assistant", "", false},
	} {
		for _, structured := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/structured=%v", tc.name, structured), func(t *testing.T) {
				b := staticBinding("https://anthropic.test", probeModel)
				b.Pinned = true
				content := `[{"type":"text","text":"verified output"}]`
				req := engine.ChatRequest{Prompt: "hello", Model: probeModel}
				if structured {
					content = `[{"type":"tool_use","name":"structured_output","input":{"answer":"verified output"}}]`
					req.OutputSchema = map[string]any{"type": "object"}
				}
				ft := &fakeTransport{resp: jsonResponse(200, fmt.Sprintf(`{"type":%q,"role":%q,"model":%q,"content":%s}`, tc.kind, tc.role, tc.model, content))}
				resp, err := newProvider(t, ft, b).Chat(context.Background(), req)
				var body map[string]any
				require.NoError(t, json.Unmarshal(ft.gotBody, &body))
				assert.Equal(t, probeModel, body["model"])
				assert.Equal(t, secretKey, ft.gotReq.Header.Get("x-api-key"))
				assert.Equal(t, "2023-06-01", ft.gotReq.Header.Get("anthropic-version"))
				if tc.ok {
					require.NoError(t, err)
					assert.Contains(t, resp.Output, "verified output")
				} else {
					require.ErrorContains(t, err, "model execution evidence")
					assert.Nil(t, resp)
					assert.NotContains(t, err.Error(), "fallback-secret")
					assert.NotContains(t, err.Error(), secretKey)
				}
			})
		}
	}
}

func TestModelProbeAnthropicStreamExactExecution(t *testing.T) {
	start := func(model, role string) string {
		return fmt.Sprintf("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"type\":\"message\",\"role\":%q,\"model\":%q,\"usage\":{\"input_tokens\":3}}}\n\n", role, model)
	}
	const token = "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"verified output\"}}\n\n"
	const usage = "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":2}}\n\n"
	const done = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	exact := start(probeModel, "assistant")
	for _, tc := range []struct {
		name, stream string
		ok, noTokens bool
	}{
		{"exact", ": comment\n\nevent: ping\ndata: {\"type\":\"ping\"}\n\n" + exact + token + usage + done, true, false},
		{"wrong", start("fallback-secret", "assistant") + token + done, false, true},
		{"missing model", start("", "assistant") + token + done, false, true},
		{"missing message", "data: {\"type\":\"message_start\"}\n\n" + token + done, false, true},
		{"wrong role", start(probeModel, "user") + token + done, false, true},
		{"request echo", "data: {\"type\":\"message_start\",\"request\":{\"model\":\"" + probeModel + "\"}}\n\n" + token + done, false, true},
		{"token before identity", token + exact + done, false, true},
		{"fallback before exact", start("fallback-secret", "assistant") + exact + token + done, false, true},
		{"fallback after exact", exact + token + start("fallback-secret", "assistant") + done, false, false},
		{"missing after exact", exact + token + start("", "assistant") + done, false, false},
		{"duplicate message", exact + token + exact + done, false, false},
		{"empty", done, false, true},
		{"truncated", exact + token, false, false},
		{"event name alone", exact + token + "event: message_stop\n\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := staticBinding("https://anthropic.test", probeModel)
			b.Pinned = true
			ft := &fakeTransport{resp: sseResponse(200, tc.stream)}
			chunks, err := newProvider(t, ft, b).StreamChat(context.Background(), engine.ChatRequest{Prompt: "hello", Model: probeModel})
			require.NoError(t, err)
			var output string
			var sawDone bool
			var streamErr error
			var tokensIn, tokensOut int
			for c := range chunks {
				output += c.Token
				sawDone = sawDone || c.Done
				tokensIn += c.TokensIn
				tokensOut += c.TokensOut
				if c.Error != nil {
					streamErr = c.Error
				}
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal(ft.gotBody, &body))
			assert.Equal(t, probeModel, body["model"])
			assert.Equal(t, true, body["stream"])
			assert.Equal(t, secretKey, ft.gotReq.Header.Get("x-api-key"))
			if tc.ok {
				require.NoError(t, streamErr)
				assert.True(t, sawDone)
				assert.Equal(t, "verified output", output)
				assert.Equal(t, 3, tokensIn)
				assert.Equal(t, 2, tokensOut)
			} else {
				require.ErrorContains(t, streamErr, "model execution evidence")
				assert.False(t, sawDone)
				assert.NotContains(t, streamErr.Error(), "fallback-secret")
				assert.NotContains(t, streamErr.Error(), secretKey)
				if tc.noTokens {
					assert.Empty(t, output, "unverified identity must not emit tokens")
				}
			}
		})
	}
}

func TestModelProbeAnthropicRequiredEvidenceAndLegacy(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, required := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/required=%v", streaming, required), func(t *testing.T) {
				b := staticBinding("https://anthropic.test", probeModel)
				ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"legacy"}]}`)}
				if streaming {
					ft.resp = sseResponse(200, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"legacy\"}}\n\n")
				}
				p := newProvider(t, ft, b)
				req := engine.ChatRequest{Prompt: "hello", RequireExecutedModel: required}
				var err error
				if streaming {
					var chunks <-chan engine.ChatChunk
					chunks, err = p.StreamChat(context.Background(), req)
					require.NoError(t, err)
					for c := range chunks {
						if c.Error != nil {
							err = c.Error
						}
					}
				} else {
					_, err = p.Chat(context.Background(), req)
				}
				if required {
					require.ErrorContains(t, err, "model execution evidence")
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestModelProbeAnthropicInvalidBindingDoesNotSend(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/empty=%v", streaming, empty), func(t *testing.T) {
				b := staticBinding("https://anthropic.test", probeModel)
				req := engine.ChatRequest{Model: "claude-sonnet-4-5", RequireExecutedModel: true}
				if empty {
					b.Model = ""
					b.Pinned = true
					req = engine.ChatRequest{}
				}
				ft := &fakeTransport{resp: jsonResponse(200, `{"content":[{"type":"text","text":"bad"}]}`)}
				p := newProvider(t, ft, b)
				var err error
				if streaming {
					var chunks <-chan engine.ChatChunk
					chunks, err = p.StreamChat(context.Background(), req)
					if err == nil {
						for range chunks {
						}
					}
				} else {
					_, err = p.Chat(context.Background(), req)
				}
				require.ErrorContains(t, err, "model execution evidence")
				assert.Nil(t, ft.gotReq)
			})
		}
	}
}
