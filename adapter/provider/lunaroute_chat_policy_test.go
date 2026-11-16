package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Load the same policy format as production, then exercise the real factory,
// preflight and body builder. Dropping MaxTokens/settings from preflight, or
// dropping/changing their emitted fields, must fail these sink assertions.
func TestLunaRouteChat_RequestPolicyJSONAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, schemaPolicy, temperaturePolicy string
			maxTokens                             int
			schema                                bool
			temperature                           float64
			refusal                               string
		}{
			{"schema refused", "unsupported", "supported", 1000, true, 0, "output_schema"},
			{"output limit refused", "unsupported", "supported", 2000, false, 0, "max tokens"},
			{"both conflicts", "unsupported", "supported", 2000, true, 0, "max tokens"},
			{"temperature refused", "supported", "unsupported", 1000, false, 0.375, "temperature"},
			{"supported schema at limit", "supported", "supported", 1000, true, 0.375, ""},
			{"unknown schema at limit", "unknown", "unknown", 1000, true, 0.375, ""},
			{"unsupported schema omitted", "unsupported", "supported", 1000, false, 0.375, ""},
			{"optional settings omitted", "unsupported", "unsupported", 0, false, 0, ""},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "capabilities.yaml")
				require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`lunaroute_models:
  glm-5.3:
    output_tokens: {tokens: 1000, evidence: "fixture#output"}
    efforts:
      chat: {levels: [low], evidence: "fixture#effort"}
    request_settings:
      output_schema: {support: %s, evidence: "fixture#schema"}
      temperature: {support: %s, evidence: "fixture#temperature"}
`, tc.schemaPolicy, tc.temperaturePolicy)), 0o600))
				caps, err := adapterconfig.LoadCapabilityRules(path)
				require.NoError(t, err)
				req := engine.ChatRequest{Prompt: lrChatPrompt, MaxTokens: tc.maxTokens, Temperature: tc.temperature}
				if tc.schema {
					req.OutputSchema = map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []string{"answer"}}
				}
				g := newChatGW()
				g.expected = chatBody("glm-5.3", lrChatPrompt, tc.maxTokens, "low", stream)
				if tc.temperature != 0 {
					g.expected["temperature"] = 0.375
				}
				if tc.schema {
					// Independent wire expectation, including wrapper and strictness.
					g.expected["response_format"] = map[string]any{
						"type": "json_schema", "json_schema": map[string]any{
							"name": "output", "strict": true,
							"schema": map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}, "required": []any{"answer"}},
						},
					}
				}
				if stream {
					g.status, g.body = 200, "data: {\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n"
					g.header = http.Header{"Content-Type": []string{"text/event-stream"}}
				}
				reveals := 0
				b := lrChatBinding("", lrChatKey, &reveals)
				b.Pinned = true
				cp, err := adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: &http.Client{Transport: g}, CapabilityRules: caps}).ChatProviderFor(b)
				require.NoError(t, err)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if stream {
					var ch <-chan engine.ChatChunk
					ch, err = cp.StreamChat(ctx, req)
					if tc.refusal != "" {
						assert.Nil(t, ch, "refusal is synchronous, before opening a stream")
					} else {
						require.NoError(t, err)
						var content string
						var done int
						for c := range ch {
							require.NoError(t, c.Error)
							content += c.Token
							if c.Done {
								done++
								assert.Equal(t, "pong", content, "Done follows the content")
							}
						}
						assert.Equal(t, "pong", content)
						assert.Equal(t, 1, done)
					}
				} else {
					var resp *engine.ChatResponse
					resp, err = cp.Chat(ctx, req)
					if tc.refusal != "" {
						assert.Nil(t, resp)
					} else {
						require.NoError(t, err)
						assert.Equal(t, "pong", resp.Output)
					}
				}
				if tc.refusal != "" {
					require.ErrorIs(t, err, domprovider.ErrLunaRouteSettingConflict)
					assert.Contains(t, err.Error(), tc.refusal)
					assert.Empty(t, g.reqs, "policy refusals perform zero HTTP calls")
					assert.Zero(t, reveals, "preflight precedes credential reveal")
					return
				}
				require.Len(t, g.bodies, 1)
				assert.Equal(t, 1, reveals)
				var body map[string]any
				require.NoError(t, json.Unmarshal(g.bodies[0], &body))
				assert.Equal(t, g.expected, body, "emitted settings match the request checked against the loaded policy")
			})
		}
	}
}

// A missing verifier, a verifier that accepts family-only matches, or one that
// runs after content emission must fail for both response formats.
func TestLunaRouteChat_FixedIdentityJSONAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, reported := range []string{"", "glm-5.3-flash", "glm-5.3"} {
			t.Run(fmt.Sprintf("stream=%t/model=%s", stream, reported), func(t *testing.T) {
				g := newChatGW()
				g.status = 200
				modelField := ""
				if reported != "" {
					modelField = fmt.Sprintf(`"model":%q,`, reported)
				}
				g.body = fmt.Sprintf(`{%s"choices":[{"message":{"content":"pong"}}]}`, modelField)
				if stream {
					g.expected["stream"] = true
					g.header = http.Header{"Content-Type": []string{"text/event-stream"}}
					g.body = fmt.Sprintf("data: {%s\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\ndata: [DONE]\n", modelField)
				}
				b := lrChatBinding("", lrChatKey, nil)
				b.Pinned = true
				cp := chatProvider(t, g, b)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				accepted := reported == "glm-5.3"
				if stream {
					ch, err := cp.StreamChat(ctx, lrChatRequest())
					require.NoError(t, err)
					var content string
					var done, failures int
					for c := range ch {
						content += c.Token
						if c.Done {
							done++
						}
						if c.Error != nil {
							failures++
							assert.ErrorContains(t, c.Error, "execution evidence")
							assert.NotContains(t, c.Error.Error(), lrChatKey)
						}
					}
					if accepted {
						assert.Equal(t, "pong", content)
						assert.Equal(t, 1, done)
						assert.Zero(t, failures)
					} else {
						assert.Empty(t, content, "unverified content never leaves the adapter")
						assert.Zero(t, done)
						assert.Equal(t, 1, failures)
					}
				} else {
					resp, err := cp.Chat(ctx, lrChatRequest())
					if accepted {
						require.NoError(t, err)
						assert.Equal(t, "pong", resp.Output)
					} else {
						require.ErrorContains(t, err, "execution evidence")
						assert.NotContains(t, err.Error(), lrChatKey)
						assert.Nil(t, resp)
					}
				}
				assert.Len(t, g.reqs, 1, "identity refusal never retries or falls back")
			})
		}
	}
}
