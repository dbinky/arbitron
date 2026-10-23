package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelProbeHTTPExactExecution(t *testing.T) {
	for _, actual := range []string{"deepseek/deepseek-v4-flash", "deepseek-v4-flash", "other/deepseek-v4-flash", "fallback-secret", ""} {
		t.Run("actual="+actual, func(t *testing.T) {
			b := staticBinding("https://router.test/v1", "deepseek/deepseek-v4-flash")
			b.Pinned = true
			ft := &fakeTransport{resp: jsonResponse(200, fmt.Sprintf(`{"model":%q,"choices":[{"message":{"role":"assistant","content":"verified output"}}]}`, actual))}
			resp, err := newProvider(t, ft, b).Chat(context.Background(), engine.ChatRequest{Prompt: "hello", Model: b.Model})
			var body map[string]any
			require.NoError(t, json.Unmarshal(ft.gotBody, &body))
			assert.Equal(t, b.Model, body["model"])
			assert.Equal(t, "Bearer "+secretKey, ft.gotReq.Header.Get("Authorization"))
			if actual == b.Model {
				require.NoError(t, err)
				assert.Equal(t, "verified output", resp.Output)
			} else {
				require.ErrorContains(t, err, "model execution evidence")
				assert.Nil(t, resp)
				assert.NotContains(t, err.Error(), "fallback-secret")
				assert.NotContains(t, err.Error(), secretKey)
			}
		})
	}
}

func TestModelProbeHTTPStreamExactExecution(t *testing.T) {
	const model = "deepseek/deepseek-v4-flash"
	frame := func(m string) string {
		return fmt.Sprintf("data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"verified output\"}}]}\n\n", m)
	}
	const done = "data: [DONE]\n\n"
	for _, tc := range []struct {
		name, stream string
		ok           bool
	}{
		{"exact", frame(model) + done, true},
		{"wrong", frame("fallback-secret") + done, false},
		{"missing", frame("") + done, false},
		{"fallback before exact", frame("fallback-secret") + frame(model) + done, false},
		{"fallback after exact", frame(model) + frame("fallback-secret") + done, false},
		{"missing after exact", frame(model) + frame("") + done, false},
		{"request echo", "data: {\"request\":{\"model\":\"" + model + "\"},\"choices\":[{\"delta\":{\"content\":\"echo\"}}]}\n\n" + done, false},
		{"empty", done, false},
		{"truncated", frame(model), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := staticBinding("https://router.test/v1", model)
			b.Pinned = true
			ft := &fakeTransport{resp: sseResponse(200, tc.stream)}
			stream, err := newProvider(t, ft, b).StreamChat(context.Background(), engine.ChatRequest{Prompt: "hello", Model: model})
			require.NoError(t, err)
			var sawDone bool
			var output string
			var streamErr error
			for c := range stream {
				output += c.Token
				sawDone = sawDone || c.Done
				if c.Error != nil {
					streamErr = c.Error
				}
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal(ft.gotBody, &body))
			assert.Equal(t, model, body["model"])
			assert.Equal(t, true, body["stream"])
			assert.Equal(t, "Bearer "+secretKey, ft.gotReq.Header.Get("Authorization"))
			if tc.ok {
				require.NoError(t, streamErr)
				assert.True(t, sawDone)
				assert.Equal(t, "verified output", output)
			} else {
				require.ErrorContains(t, streamErr, "model execution evidence")
				assert.False(t, sawDone)
				assert.NotContains(t, streamErr.Error(), "fallback-secret")
				if tc.name == "wrong" || tc.name == "missing" || tc.name == "fallback before exact" {
					assert.Empty(t, output, "unverified first frame must not emit a token")
				}
			}
		})
	}
}

func TestModelProbeHTTPRequiredEvidenceAndLegacy(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, required := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/required=%v", streaming, required), func(t *testing.T) {
				b := staticBinding("https://router.test/v1", "gpt-6-sol")
				ft := &fakeTransport{resp: jsonResponse(200, `{"model":"fallback","choices":[{"message":{"content":"legacy"}}]}`)}
				if streaming {
					ft.resp = sseResponse(200, "data: {\"choices\":[{\"delta\":{\"content\":\"legacy\"}}]}\n\ndata: [DONE]\n\n")
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
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestModelProbeHTTPStaleBindingDoesNotSend(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			b := staticBinding("https://router.test/v1", "gpt-6-sol")
			ft := &fakeTransport{resp: jsonResponse(200, `{"model":"gpt-6-sol","choices":[{"message":{"content":"wrong run"}}]}`)}
			p := newProvider(t, ft, b)
			req := engine.ChatRequest{Model: "gpt-5-sol", RequireExecutedModel: true}
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
			assert.Nil(t, ft.gotReq, "a cached binding cannot override the pinned request")
		})
	}
}

func TestModelProbeHTTPEmptyPinDoesNotSend(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			b := staticBinding("https://router.test/v1", "")
			b.Pinned = true
			ft := &fakeTransport{resp: jsonResponse(200, `{"model":"","choices":[{"message":{"content":"bad"}}]}`)}
			p := newProvider(t, ft, b)
			var err error
			if streaming {
				var chunks <-chan engine.ChatChunk
				chunks, err = p.StreamChat(context.Background(), engine.ChatRequest{})
				if err == nil {
					for range chunks {
					}
				}
			} else {
				_, err = p.Chat(context.Background(), engine.ChatRequest{})
			}
			require.ErrorContains(t, err, "model execution evidence")
			assert.Nil(t, ft.gotReq)
		})
	}
}
