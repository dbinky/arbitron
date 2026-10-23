package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Converse currently has no evidenced actual-model identity/mapping. Neither a
// fixed-looking URL nor a successful response can satisfy required execution.
func TestModelProbeBedrockRequiredEvidenceDoesNotSend(t *testing.T) {
	const model = "anthropic.claude-sonnet-4-6-v1:0"
	for _, stream := range []bool{false, true} {
		for _, proof := range []struct {
			name             string
			pinned, required bool
		}{
			{"pinned", true, false},
			{"explicit", false, true},
			{"both", true, true},
		} {
			for _, models := range []struct{ name, binding, request string }{
				{"fixed", model, model},
				{"empty binding", "", model},
				{"empty request", model, ""},
				{"stale binding", "anthropic.claude-sonnet-4-5-v1:0", model},
			} {
				t.Run(fmt.Sprintf("stream=%v/%s/%s", stream, proof.name, models.name), func(t *testing.T) {
					var sent, revealed atomic.Int32
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						sent.Add(1)
						if stream {
							fmt.Fprintln(w, `{"contentBlockDelta":{"delta":{"text":"unverified output"}}}`)
							fmt.Fprintln(w, `{"messageStop":{"stopReason":"end_turn"}}`)
						} else {
							fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"unverified output"}]}},"usage":{}}`)
						}
					}))
					t.Cleanup(srv.Close)
					binding := bindingTo(srv.URL, "us-east-1", models.binding, false)
					binding.Pinned = proof.pinned
					binding.RevealKey = func(context.Context) (string, error) {
						revealed.Add(1)
						return testSecret, nil
					}
					p := newProvider(t, srv, binding, 1024)
					req := engine.ChatRequest{Model: models.request, Prompt: "check exact execution", RequireExecutedModel: proof.required}
					if stream {
						chunks, err := p.StreamChat(context.Background(), req)
						assert.ErrorContains(t, err, "model execution evidence unavailable")
						if chunks != nil {
							for chunk := range chunks {
								assert.Empty(t, chunk.Token, "unverified tokens must not escape")
								assert.False(t, chunk.Done, "unverified completion must not escape")
							}
							t.Error("required execution must fail before returning a stream")
						}
					} else {
						response, err := p.Chat(context.Background(), req)
						assert.ErrorContains(t, err, "model execution evidence unavailable")
						assert.Nil(t, response, "a copied request model is not execution evidence")
					}
					assert.Zero(t, revealed.Load(), "unsupported proof must fail before revealing credentials")
					assert.Zero(t, sent.Load(), "unsupported proof must fail before transport")
				})
			}
		}
	}
}

func TestModelProbeBedrockLegacyExecution(t *testing.T) {
	const model = "anthropic.claude-sonnet-4-6-v1:0"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			var sent atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Messages []struct {
						Role    string                  `json:"role"`
						Content []struct{ Text string } `json:"content"`
					} `json:"messages"`
				}
				path := "/model/" + model + "/converse"
				if stream {
					path += "-stream"
				}
				raw, readErr := io.ReadAll(r.Body)
				valid := readErr == nil && json.Unmarshal(raw, &body) == nil && len(body.Messages) == 1 &&
					body.Messages[0].Role == "user" && len(body.Messages[0].Content) == 1 && body.Messages[0].Content[0].Text == "legacy prompt"
				expected := r.Clone(r.Context())
				// rewriteTo changes the host and loses RawPath. Restore the exact
				// original signing target, including the escaped model revision.
				target, targetErr := endpointURL(bindingTo("", "us-east-1", model, false), stream)
				var parseErr error
				expected.URL, parseErr = url.Parse(target)
				if targetErr != nil || parseErr != nil {
					http.Error(w, "invalid signing target", http.StatusBadRequest)
					return
				}
				expected.Host = expected.URL.Host
				signErr := awssig.Sign(expected, raw, "AKID", awssig.Credential{SecretAccessKey: testSecret}, "us-east-1", sigService, fixedNow())
				if !valid || signErr != nil || r.Method != http.MethodPost || r.URL.Path != path ||
					r.Header.Get("Authorization") != expected.Header.Get("Authorization") || r.Header.Get("X-Amz-Date") != expected.Header.Get("X-Amz-Date") {
					http.Error(w, "invalid execution inputs", http.StatusBadRequest)
					return
				}
				sent.Add(1)
				if stream {
					fmt.Fprintln(w, `{"contentBlockDelta":{"delta":{"text":"legacy output"}}}`)
					fmt.Fprintln(w, `{"messageStop":{"stopReason":"end_turn"}}`)
				} else {
					fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"legacy output"}]}},"usage":{}}`)
				}
			}))
			t.Cleanup(srv.Close)
			p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", model, false), 1024)
			req := engine.ChatRequest{Model: model, Prompt: "legacy prompt"}
			if stream {
				chunks, err := p.StreamChat(context.Background(), req)
				require.NoError(t, err)
				var text string
				var done bool
				for chunk := range chunks {
					require.NoError(t, chunk.Error)
					text += chunk.Token
					done = done || chunk.Done
				}
				assert.Equal(t, "legacy output", text)
				assert.True(t, done)
			} else {
				response, err := p.Chat(context.Background(), req)
				require.NoError(t, err)
				assert.Equal(t, "legacy output", response.Output)
			}
			assert.EqualValues(t, 1, sent.Load())
		})
	}
}
