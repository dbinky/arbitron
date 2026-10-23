package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// D8 permits configured-ID Bedrock execution without claiming exact identity.
// Genuine legacy NULL/0 rows keep live resolution. Only the network leaf is
// redirected; services, credentials and pins are real.
func TestUsabilityBedrockPinnedAndLegacyExecutionRoot(t *testing.T) {
	const model = "anthropic.claude-sonnet-4-6-v1:0"
	for _, route := range []string{"bedrock", "amazon-bedrock"} {
		for _, interactive := range []bool{false, true} {
			t.Run(fmt.Sprintf("provider=%s/interactive=%v", route, interactive), func(t *testing.T) {
				var attempts, accepted atomic.Int32
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts.Add(1)
					var body struct {
						Messages []struct {
							Role    string                  `json:"role"`
							Content []struct{ Text string } `json:"content"`
						} `json:"messages"`
					}
					path := "/model/" + model + "/converse"
					if interactive {
						path += "-stream"
					}
					raw, readErr := io.ReadAll(r.Body)
					valid := readErr == nil && json.Unmarshal(raw, &body) == nil && len(body.Messages) == 1 &&
						body.Messages[0].Role == "user" && len(body.Messages[0].Content) == 1 && body.Messages[0].Content[0].Text != ""
					// Re-sign with the expected secret so a missing/wrong credential
					// cannot make the legacy control succeed merely by naming an account.
					signedAt, timeErr := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
					expected := r.Clone(r.Context())
					signErr := awssig.Sign(expected, raw, "bedrock-root-access", awssig.Credential{
						SecretAccessKey: "bedrock-root-secret", SessionToken: "bedrock-root-session",
					}, "us-east-1", "bedrock", signedAt)
					if !valid || timeErr != nil || signErr != nil || r.Method != http.MethodPost ||
						r.Host != "bedrock-runtime.us-east-1.amazonaws.com" || r.URL.Path != path ||
						r.Header.Get("Authorization") != expected.Header.Get("Authorization") ||
						r.Header.Get("X-Amz-Security-Token") != "bedrock-root-session" {
						http.Error(w, "invalid execution inputs", http.StatusBadRequest)
						return
					}
					accepted.Add(1)
					if interactive {
						fmt.Fprintln(w, `{"contentBlockDelta":{"delta":{"text":"unverified Bedrock output"}}}`)
						fmt.Fprintln(w, `{"messageStop":{"stopReason":"end_turn"}}`)
					} else {
						fmt.Fprint(w, `{"output":{"message":{"role":"assistant","content":[{"text":"unverified Bedrock output"}]}},"usage":{}}`)
					}
				}))
				t.Cleanup(upstream.Close)

				// initDomain clones this transport. Substitute only TLS socket I/O
				// for the exact derived AWS host; no production routing is replaced.
				original := http.DefaultTransport
				transport := original.(*http.Transport).Clone()
				transport.Proxy = nil
				transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					if addr != "bedrock-runtime.us-east-1.amazonaws.com:443" {
						return nil, fmt.Errorf("unexpected Bedrock destination")
					}
					dialer := tls.Dialer{Config: upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
					return dialer.DialContext(ctx, network, upstream.Listener.Addr().String())
				}
				ds := func() *domainStack {
					http.DefaultTransport = transport
					defer func() { http.DefaultTransport = original }()
					kind := "llm_call"
					if interactive {
						kind = "interactive_llm"
					}
					return bootSinkRoot(t, "", func(workspace string) {
						writeWorkflow(t, workspace, "usability-bedrock", fmt.Sprintf(`{"name":"usability-bedrock","nodes":[{"id":"work","type":%q,"entrypoint":true,"config":{"model_profile":"chat","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, kind))
						writePinTemplate(t, workspace, "usability-bedrock")
						require.NoError(t, os.WriteFile(filepath.Join(workspace, "users", localDefaultUser.String(), "usability-bedrock", "templates", "o.md"), []byte("orchestrate"), 0o600))
					})
				}()
				ctx := adminCtxForLocalUser(t, ds)
				require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{
					Name: "bedrock-api", Provider: route, BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com",
					Extras: map[string]string{"region": "us-east-1", "access-key-id": "bedrock-root-access"},
				}, `{"secret":"bedrock-root-secret","session_token":"bedrock-root-session"}`))
				_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "chat", Kind: provider.ProfileKindChat, ConnectionRef: "bedrock-api", Model: model})
				require.NoError(t, err)
				id, _, err := ds.eng.Trigger(ctx, "usability-bedrock", nil, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					saved, e := ds.instanceRepo.Get(ctx, id)
					return e == nil && (saved.State.IsTerminal() || saved.State == instance.StateWaiting)
				}, 20*time.Second, 50*time.Millisecond)
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, instance.ModelPinsVersionSnapshot, saved.ModelPinsVersion)
				require.Equal(t, map[string]string{"chat": model}, saved.ModelPins)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				want := instance.StateCompleted
				if interactive {
					want = instance.StateWaiting
				}
				require.Equal(t, want, saved.State, "trace: %+v", trace)
				assert.Equal(t, map[string]string{"chat": provider.CanonicalBedrock}, saved.ModelPinRoutes)
				assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Output, trace[0].LLMConversation), "unverified Bedrock output")
				assert.EqualValues(t, 1, attempts.Load(), "native pinned execution reaches the signed HTTP sink")

				// Emulate only a genuinely pre-migration instance; ordinary trigger
				// above must persist version-1 pins. Use the same root/resolver/adapter.
				legacyID := uuid.New()
				_, err = ds.db.Exec(`INSERT INTO instances (
  id, owner_id, workflow_name, workflow_snapshot, state, priority,
  variables, original_variables, workflow_id, visibility, model_pins, model_pins_version
) SELECT ?, owner_id, workflow_name, workflow_snapshot, 'queued', priority,
  variables, original_variables, workflow_id, visibility, NULL, 0
  FROM instances WHERE id = ?`, legacyID.String(), id.String())
				require.NoError(t, err)
				_, err = ds.queueMgr.Enqueue(ctx, legacyID, localDefaultUser)
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					legacy, e := ds.instanceRepo.Get(ctx, legacyID)
					return e == nil && (legacy.State.IsTerminal() || legacy.State == instance.StateWaiting)
				}, 20*time.Second, 50*time.Millisecond)
				legacy, err := ds.instanceRepo.Get(ctx, legacyID)
				require.NoError(t, err)
				require.Equal(t, want, legacy.State)
				assert.Equal(t, instance.ModelPinsVersionLegacy, legacy.ModelPinsVersion)
				assert.Nil(t, legacy.ModelPins)
				legacyTrace, err := ds.nodeExecRepo.ListByInstance(ctx, legacyID)
				require.NoError(t, err)
				require.Len(t, legacyTrace, 1)
				assert.Equal(t, instance.NodeStatusSucceeded, legacyTrace[0].Status)
				assert.Contains(t, fmt.Sprint(legacyTrace[0].Output, legacyTrace[0].LLMConversation), "unverified Bedrock output")
				wantCalls := int32(2)
				if interactive {
					require.NoError(t, ds.conversationHandler.HandleMessage(ctx, legacyID, "legacy follow-up"))
					wantCalls++
					continued, err := ds.nodeExecRepo.ListByInstance(ctx, legacyID)
					require.NoError(t, err)
					assert.Contains(t, fmt.Sprint(continued[0].LLMConversation), "legacy follow-up")
				}
				assert.Equal(t, wantCalls, attempts.Load())
				assert.Equal(t, wantCalls, accepted.Load(), "all native pinned and legacy calls must carry the real model/account/prompt")
			})
		}
	}
}
