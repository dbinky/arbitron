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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Only TLS socket I/O is substituted. initDomain still constructs the native
// adapters, derives the AWS endpoint, resolves credentials and reads real pins.
type d8ExecutionSink struct {
	mu        sync.Mutex
	models    []string
	attempts  int
	fail      bool
	content   string
	key       string
	family    string
	stream    bool
	transport *http.Transport
}

func newD8ExecutionSink(t *testing.T, family string, stream bool) *d8ExecutionSink {
	t.Helper()
	s := &d8ExecutionSink{family: family, stream: stream, key: "original-secret", content: "D8 output"}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.attempts++
		raw, err := io.ReadAll(r.Body)
		valid := err == nil && r.Method == http.MethodPost
		var model string
		if family == provider.CanonicalAzure {
			var body struct {
				Model    string
				Stream   bool
				Messages []struct{ Content string }
			}
			valid = valid && json.Unmarshal(raw, &body) == nil && body.Model != "" && body.Stream == stream && len(body.Messages) > 0 && body.Messages[len(body.Messages)-1].Content != ""
			model = body.Model
			valid = valid && r.Host == "d8-azure.example.com" && r.URL.Path == "/openai/deployments/"+model+"/chat/completions" && r.URL.Query().Get("api-version") == "2024-10-21" && r.Header.Get("api-key") == s.key
		} else {
			var body struct {
				Messages []struct {
					Role    string
					Content []struct{ Text string }
				}
			}
			valid = valid && json.Unmarshal(raw, &body) == nil && len(body.Messages) > 0
			if len(body.Messages) > 0 {
				last := body.Messages[len(body.Messages)-1]
				valid = valid && last.Role == "user" && len(last.Content) > 0 && last.Content[0].Text != ""
			}
			action := "converse"
			if stream {
				action += "-stream"
			}
			model = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/model/"), "/"+action)
			signedAt, timeErr := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
			expected := r.Clone(r.Context())
			signErr := awssig.Sign(expected, raw, "d8-access", awssig.Credential{SecretAccessKey: s.key, SessionToken: "d8-session"}, "us-east-1", "bedrock", signedAt)
			valid = valid && r.URL.Path == "/model/"+model+"/"+action && model != "" && timeErr == nil && signErr == nil && r.Host == "bedrock-runtime.us-east-1.amazonaws.com" && r.Header.Get("Authorization") == expected.Header.Get("Authorization") && r.Header.Get("X-Amz-Security-Token") == "d8-session"
		}
		if !valid {
			http.Error(w, "invalid model/auth/route/prompt", http.StatusBadRequest)
			return
		}
		s.models = append(s.models, model)
		if s.fail {
			http.Error(w, "temporary upstream fault", http.StatusServiceUnavailable)
			return
		}
		// Deliberately no actual-model identity: D8 accepts configured-ID execution,
		// not a request echo masquerading as exact execution evidence.
		reply, _ := json.Marshal(s.content)
		if family == provider.CanonicalAzure {
			if stream {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\ndata: [DONE]\n\n", reply)
			} else {
				fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s}}]}`, reply)
			}
		} else if stream {
			fmt.Fprintf(w, `{"contentBlockDelta":{"delta":{"text":%s}}}`+"\n", reply)
			fmt.Fprintln(w, `{"messageStop":{"stopReason":"end_turn"}}`)
		} else {
			fmt.Fprintf(w, `{"output":{"message":{"role":"assistant","content":[{"text":%s}]}},"usage":{}}`, reply)
		}
	}))
	t.Cleanup(srv.Close)
	s.transport = http.DefaultTransport.(*http.Transport).Clone()
	s.transport.Proxy = nil
	s.transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		// The "-b"/us-west-2 hosts are same-family move targets (paired row 4):
		// they reach the sink, which counts the attempt but rejects the route.
		switch addr {
		case "d8-azure.example.com:443", "bedrock-runtime.us-east-1.amazonaws.com:443",
			"d8-azure-b.example.com:443", "bedrock-runtime.us-west-2.amazonaws.com:443":
		default:
			return nil, fmt.Errorf("unexpected destination %s", addr)
		}
		dialer := tls.Dialer{Config: srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
		return dialer.DialContext(ctx, network, srv.Listener.Addr().String())
	}
	t.Cleanup(s.transport.CloseIdleConnections)
	return s
}

func (s *d8ExecutionSink) construct(build func() (*domainStack, error)) (*domainStack, error) {
	original := http.DefaultTransport
	http.DefaultTransport = s.transport
	defer func() { http.DefaultTransport = original }()
	return build()
}
func (s *d8ExecutionSink) seen() ([]string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.models...), s.attempts
}

func bootD8Root(t *testing.T, s *d8ExecutionSink, route string) (*domainStack, func(), provider.Connection, provider.Profile) {
	t.Helper()
	return bootD8RootWith(t, s, route, nil)
}

// bootD8RootWith lets a test add workflows next to the default pin-d8 one.
func bootD8RootWith(t *testing.T, s *d8ExecutionSink, route string, extra func(dir string)) (*domainStack, func(), provider.Connection, provider.Profile) {
	t.Helper()
	var closeStack func()
	ds, err := s.construct(func() (*domainStack, error) {
		stack, close := bootSinkRootWithClose(t, "", func(dir string) {
			if extra != nil {
				extra(dir)
			}
			kind := "llm_call"
			if s.stream {
				kind = "interactive_llm"
			}
			writeWorkflow(t, dir, "pin-d8", fmt.Sprintf(`{"name":"pin-d8","nodes":[{"id":"ask","type":%q,"entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md","orchestrationFile":"templates/o.md"}}],"edges":[]}`, kind))
			writePinTemplate(t, dir, "pin-d8")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "users", localDefaultUser.String(), "pin-d8", "templates", "o.md"), []byte("orchestrate"), 0o600))
		})
		closeStack = close
		return stack, nil
	})
	require.NoError(t, err)
	conn := provider.Connection{Name: "d8", Provider: route, BaseURL: "https://d8-azure.example.com", Extras: map[string]string{"api-version": "2024-10-21"}}
	secret := "original-secret"
	if s.family == provider.CanonicalBedrock {
		conn.BaseURL = "https://bedrock-runtime.us-east-1.amazonaws.com"
		conn.Extras = map[string]string{"region": "us-east-1", "access-key-id": "d8-access"}
		secret = `{"secret":"original-secret","session_token":"d8-session"}`
	}
	ctx := adminCtxForLocalUser(t, ds)
	require.NoError(t, ds.connSvc.Create(ctx, conn, secret))
	p := provider.Profile{Alias: "pin-alias", Kind: provider.ProfileKindChat, ConnectionRef: "d8", Model: "configured-X"}
	_, _, err = ds.profileSvc.Create(ctx, p)
	require.NoError(t, err)
	return ds, closeStack, conn, p
}

func waitD8Output(t *testing.T, ds *domainStack, id uuid.UUID, stream bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		saved, err := ds.instanceRepo.Get(context.Background(), id)
		return err == nil && (saved.State.IsTerminal() || saved.State == instance.StateWaiting)
	}, 20*time.Second, 20*time.Millisecond)
	saved, err := ds.instanceRepo.Get(context.Background(), id)
	require.NoError(t, err)
	trace, err := ds.nodeExecRepo.ListByInstance(context.Background(), id)
	require.NoError(t, err)
	require.NotEmpty(t, trace)
	want := instance.StateCompleted
	if stream {
		want = instance.StateWaiting
	}
	require.Equal(t, want, saved.State, "trace: %+v", trace)
	succeeded := 0
	for _, node := range trace {
		if node.Status == instance.NodeStatusSucceeded {
			succeeded++
			require.Contains(t, fmt.Sprint(node.Output, node.LLMConversation), "D8 output")
		} else {
			require.Equal(t, instance.NodeStatusFailed, node.Status)
			require.NotContains(t, fmt.Sprint(node.Output, node.LLMConversation), "D8 output")
		}
	}
	require.Equal(t, 1, succeeded)
}

func TestD8PinnedExecutionRoot(t *testing.T) {
	// Mixed-case spellings must reach the same native family at capture,
	// execution and refresh (paired:10; TestUsabilityD8AndUnknownMappingRoot).
	for _, route := range []string{"azure-openai", "azure-foundry", "azure", "bedrock", "amazon-bedrock", "Azure-OpenAI", " AMAZON-BEDROCK "} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", route, stream), func(t *testing.T) {
				family := provider.Canonicalize(route)
				sink := newD8ExecutionSink(t, family, stream)
				sink.fail = !stream
				ds, closeStack, conn, p := bootD8Root(t, sink, route)
				ctx := adminCtxForLocalUser(t, ds)
				id, _, err := ds.eng.Trigger(ctx, "pin-d8", nil, "normal")
				require.NoError(t, err)
				if stream {
					waitD8Output(t, ds, id, true)
				} else {
					require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
				}
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, map[string]string{"pin-alias": "configured-X"}, saved.ModelPins)
				require.Equal(t, map[string]string{"pin-alias": family}, saved.ModelPinRoutes)
				models, attempts := sink.seen()
				require.Equal(t, []string{"configured-X"}, models)
				require.Equal(t, 1, attempts)
				// Returned maps are caller-owned. In the streaming case the next
				// real turn hits this cache and must still reach the original sink.
				pins, err := ds.pinLookup.Lookup(ctx, id)
				require.NoError(t, err)
				require.Equal(t, family, pins.OpaqueRoutes["pin-alias"])
				pins.Models["pin-alias"], pins.OpaqueRoutes["pin-alias"] = "mutated", "wrong-family"
				p.Model = "configured-Y"
				_, _, err = ds.profileSvc.Update(ctx, p)
				require.NoError(t, err)
				secret := "rotated-secret"
				if family == provider.CanonicalBedrock {
					secret = `{"secret":"rotated-secret","session_token":"d8-session"}`
				}
				require.NoError(t, ds.connSvc.Update(ctx, conn, &secret))
				sink.mu.Lock()
				sink.key = "rotated-secret"
				sink.fail = false
				sink.mu.Unlock()
				if stream {
					require.NoError(t, ds.conversationHandler.HandleMessage(ctx, id, "old run live credential"))
				}
				configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
				closeStack()
				reopened, err := sink.construct(func() (*domainStack, error) { return initDomain(context.Background(), configPath) })
				require.NoError(t, err)
				t.Cleanup(func() { reopened.Close(context.Background()) })
				require.False(t, reopened.pinLookup.Cached(id))
				if stream {
					require.NoError(t, reopened.conversationHandler.HandleMessage(ctx, id, "continue after reopen"))
				} else {
					require.NoError(t, reopened.eng.Retry(ctx, id, nil))
				}
				waitD8Output(t, reopened, id, stream)
				saved, err = reopened.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"pin-alias": "configured-X"}, saved.ModelPins)
				assert.Equal(t, map[string]string{"pin-alias": family}, saved.ModelPinRoutes)
				fresh, _, err := reopened.eng.Trigger(ctx, "pin-d8", nil, "normal")
				require.NoError(t, err)
				waitD8Output(t, reopened, fresh, stream)
				freshSaved, err := reopened.instanceRepo.Get(ctx, fresh)
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"pin-alias": "configured-Y"}, freshSaved.ModelPins)
				expected := []string{"configured-X", "configured-X", "configured-Y"}
				if stream {
					expected = []string{"configured-X", "configured-X", "configured-X", "configured-Y"}
				}
				models, attempts = sink.seen()
				assert.Equal(t, expected, models)
				assert.Equal(t, len(expected), attempts)

				// Only a genuinely historical NULL/0 row follows live model edits.
				legacyID := uuid.New()
				_, err = reopened.db.Exec(`INSERT INTO instances (
 id, owner_id, workflow_name, workflow_snapshot, state, priority,
 variables, original_variables, workflow_id, visibility, model_pins, model_pins_version
) SELECT ?, owner_id, workflow_name, workflow_snapshot, 'queued', priority,
 variables, original_variables, workflow_id, visibility, NULL, 0
 FROM instances WHERE id = ?`, legacyID.String(), fresh.String())
				require.NoError(t, err)
				p.Model = "legacy-live-Z"
				_, _, err = reopened.profileSvc.Update(ctx, p)
				require.NoError(t, err)
				_, err = reopened.queueMgr.Enqueue(ctx, legacyID, localDefaultUser)
				require.NoError(t, err)
				waitD8Output(t, reopened, legacyID, stream)
				legacy, err := reopened.instanceRepo.Get(ctx, legacyID)
				require.NoError(t, err)
				assert.Equal(t, instance.ModelPinsVersionLegacy, legacy.ModelPinsVersion)
				assert.Nil(t, legacy.ModelPins)
				assert.Nil(t, legacy.ModelPinRoutes)
				expected = append(expected, "legacy-live-Z")
				if stream {
					p.Model = "legacy-live-next"
					_, _, err = reopened.profileSvc.Update(ctx, p)
					require.NoError(t, err)
					require.NoError(t, reopened.conversationHandler.HandleMessage(ctx, legacyID, "legacy follows live"))
					expected = append(expected, "legacy-live-next")
				}
				models, attempts = sink.seen()
				assert.Equal(t, expected, models)
				assert.Equal(t, len(expected), attempts)
			})
		}
	}
}

func TestD8PinnedExecutionRootRejectsInvalidBinding(t *testing.T) {
	for _, route := range []string{"azure", "bedrock"} {
		t.Run(route, func(t *testing.T) {
			sink := newD8ExecutionSink(t, route, true)
			ds, _, _, p := bootD8Root(t, sink, route)
			ctx := adminCtxForLocalUser(t, ds)
			id, _, err := ds.eng.Trigger(ctx, "pin-d8", nil, "normal")
			require.NoError(t, err)
			waitD8Output(t, ds, id, true)
			before, attempts := sink.seen()
			trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
			require.NoError(t, err)
			transcript := fmt.Sprint(trace[0].LLMConversation)
			// A different live native family cannot inherit this run's exemption.
			other := "bedrock"
			if route == "bedrock" {
				other = "azure"
			}
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "other", Provider: other, BaseURL: "https://d8-azure.example.com", Extras: map[string]string{"region": "us-east-1", "access-key-id": "d8-access", "api-version": "2024-10-21"}}, "irrelevant"))
			p.ConnectionRef = "other"
			storeProfileUnchecked(t, ctx, ds, p)
			require.Error(t, ds.conversationHandler.HandleMessage(ctx, id, "wrong live family"))
			p.ConnectionRef = "d8"
			storeProfileUnchecked(t, ctx, ds, p)
			for _, bad := range []struct{ name, column, value string }{
				{"missing exception", "model_pin_routes", `{}`},
				{"wrong exception", "model_pin_routes", fmt.Sprintf(`{"pin-alias":%q}`, other)},
				{"corrupt exception", "model_pin_routes", `{"pin-alias":"openai"}`},
				{"malformed exception", "model_pin_routes", `not-json`},
				{"missing pin", "model_pins", `{}`},
			} {
				t.Run(bad.name, func(t *testing.T) {
					ds.pinLookup.Release(id)
					_, err := ds.db.Exec("UPDATE instances SET "+bad.column+" = ? WHERE id = ?", bad.value, id.String())
					require.NoError(t, err)
					require.Error(t, ds.conversationHandler.HandleMessage(ctx, id, "must not be accepted"))
					models, got := sink.seen()
					assert.Equal(t, before, models)
					assert.Equal(t, attempts, got)
					trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
					require.NoError(t, err)
					assert.Equal(t, transcript, fmt.Sprint(trace[0].LLMConversation))
					_, err = ds.db.Exec(`UPDATE instances SET model_pins = '{"pin-alias":"configured-X"}', model_pin_routes = ? WHERE id = ?`, fmt.Sprintf(`{"pin-alias":%q}`, route), id.String())
					require.NoError(t, err)
				})
			}
			models, got := sink.seen()
			assert.Equal(t, before, models)
			assert.Equal(t, attempts, got)
		})
	}
}

// TestD8PinnedBatchRootRejectsInvalidBinding is the llm_call counterpart of the
// interactive rejection test: a retry of a failed batch run must refuse a
// cross-family live edit and every corrupt 031 record before the native sink
// is reached, instead of falling back to the live profile.
func TestD8PinnedBatchRootRejectsInvalidBinding(t *testing.T) {
	for _, route := range []string{"azure", "bedrock"} {
		t.Run(route, func(t *testing.T) {
			sink := newD8ExecutionSink(t, route, false)
			sink.fail = true
			ds, _, _, p := bootD8Root(t, sink, route)
			ctx := adminCtxForLocalUser(t, ds)
			id, _, err := ds.eng.Trigger(ctx, "pin-d8", nil, "normal")
			require.NoError(t, err)
			require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
			before, attempts := sink.seen()
			require.Equal(t, []string{"configured-X"}, before)

			retryRefused := func(t *testing.T) {
				t.Helper()
				prior, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				if err := ds.eng.Retry(ctx, id, nil); err != nil {
					// A corrupt 031 record is refused when the row is loaded.
					models, got := sink.seen()
					assert.Equal(t, before, models)
					assert.Equal(t, attempts, got)
					return
				}
				require.Eventually(t, func() bool {
					now, err := ds.nodeExecRepo.ListByInstance(ctx, id)
					saved, getErr := ds.instanceRepo.Get(ctx, id)
					return err == nil && getErr == nil && len(now) > len(prior) && saved.State.IsTerminal()
				}, 20*time.Second, 20*time.Millisecond)
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, instance.StateFailed, saved.State)
				models, got := sink.seen()
				assert.Equal(t, before, models)
				assert.Equal(t, attempts, got)
			}

			other := "bedrock"
			if route == "bedrock" {
				other = "azure"
			}
			require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: "other", Provider: other, BaseURL: "https://d8-azure.example.com", Extras: map[string]string{"region": "us-east-1", "access-key-id": "d8-access", "api-version": "2024-10-21"}}, "irrelevant"))
			p.ConnectionRef = "other"
			storeProfileUnchecked(t, ctx, ds, p)
			t.Run("wrong live family", retryRefused)
			p.ConnectionRef = "d8"
			storeProfileUnchecked(t, ctx, ds, p)
			for _, bad := range []struct{ name, column, value string }{
				{"missing exception", "model_pin_routes", `{}`},
				{"wrong exception", "model_pin_routes", fmt.Sprintf(`{"pin-alias":%q}`, other)},
				{"corrupt exception", "model_pin_routes", `{"pin-alias":"openai"}`},
				{"malformed exception", "model_pin_routes", `not-json`},
				{"missing pin", "model_pins", `{}`},
			} {
				t.Run(bad.name, func(t *testing.T) {
					ds.pinLookup.Release(id)
					_, err := ds.db.Exec("UPDATE instances SET "+bad.column+" = ? WHERE id = ?", bad.value, id.String())
					require.NoError(t, err)
					retryRefused(t)
					_, err = ds.db.Exec(`UPDATE instances SET model_pins = '{"pin-alias":"configured-X"}', model_pin_routes = ? WHERE id = ?`, fmt.Sprintf(`{"pin-alias":%q}`, route), id.String())
					require.NoError(t, err)
				})
			}
		})
	}
}

// Migration 031 leaves historical version-1 routes NULL. Native live routing
// must not grant those strict runs a new opaque exception, even after an edit.
func TestD8Pre031RouteRemainsStrictAtRoot(t *testing.T) {
	for _, tc := range []struct{ route, model, refusal string }{
		{"azure", "configured-X", "specific model version"},
		{"bedrock", "configured-X", "specific model version"},
		// Matching the fixed-version grammar must not bypass D8 capture or
		// let a historical strict run execute without native model evidence.
		{"bedrock", "anthropic.claude-sonnet-4-5-20250929-v1:0", "model execution evidence unavailable"},
	} {
		route := tc.route
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%s/stream=%v", route, tc.model, stream), func(t *testing.T) {
				sink := newD8ExecutionSink(t, route, stream)
				sink.fail = !stream
				ds, _, _, p := bootD8Root(t, sink, route)
				ctx := adminCtxForLocalUser(t, ds)
				p.Model = tc.model
				_, _, err := ds.profileSvc.Update(ctx, p)
				require.NoError(t, err)
				id, _, err := ds.eng.Trigger(ctx, "pin-d8", nil, "normal")
				require.NoError(t, err)
				if stream {
					waitD8Output(t, ds, id, true)
				} else {
					require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
				}
				ds.eng.Shutdown()
				captured, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, map[string]string{"pin-alias": route}, captured.ModelPinRoutes)
				prior, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, prior, 1)
				p.Model = "configured-Y"
				_, _, err = ds.profileSvc.Update(ctx, p)
				require.NoError(t, err)
				_, err = ds.db.Exec(`UPDATE instances SET model_pin_routes = NULL WHERE id = ?`, id.String())
				require.NoError(t, err)
				ds.pinLookup.Release(id)
				if stream {
					require.ErrorContains(t, ds.conversationHandler.HandleMessage(ctx, id, "strict historical pin"), tc.refusal)
					now, err := ds.nodeExecRepo.ListByInstance(ctx, id)
					require.NoError(t, err)
					require.Equal(t, prior, now, "refused turn must not overwrite the conversation")
				} else {
					require.NoError(t, ds.eng.Retry(ctx, id, nil))
					require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
					ds.eng.Shutdown()
					now, err := ds.nodeExecRepo.ListByInstance(ctx, id)
					require.NoError(t, err)
					require.Len(t, now, 2)
					require.Equal(t, instance.NodeStatusFailed, now[1].Status)
					require.Contains(t, fmt.Sprint(now[1].Error), tc.refusal)
				}
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, 1, saved.ModelPinsVersion)
				assert.Equal(t, map[string]string{"pin-alias": tc.model}, saved.ModelPins)
				assert.Nil(t, saved.ModelPinRoutes)
				models, attempts := sink.seen()
				assert.Equal(t, []string{tc.model}, models)
				assert.Equal(t, 1, attempts, "strict historical run must stop before native HTTP")
			})
		}
	}
}

// TestD8RerunRepinsConfiguredIDAndRoute: Rerun re-enters Trigger, so the new run
// re-captures the CURRENT configured ID and its route exception, while the
// finished source run keeps its own pin and exception.
func TestD8RerunRepinsConfiguredIDAndRoute(t *testing.T) {
	for _, route := range []string{"azure", "bedrock"} {
		t.Run(route, func(t *testing.T) {
			family := provider.Canonicalize(route)
			sink := newD8ExecutionSink(t, family, false)
			ds, _, _, p := bootD8Root(t, sink, route)
			ctx := adminCtxForLocalUser(t, ds)
			id, _, err := ds.eng.Trigger(ctx, "pin-d8", nil, "normal")
			require.NoError(t, err)
			waitD8Output(t, ds, id, false)
			p.Model = "configured-Y"
			_, _, err = ds.profileSvc.Update(ctx, p)
			require.NoError(t, err)
			ds.eng.Shutdown()
			fresh, err := ds.eng.Rerun(ctx, id)
			require.NoError(t, err)
			require.NotEqual(t, id, fresh)
			waitD8Output(t, ds, fresh, false)
			for want, got := range map[uuid.UUID]string{id: "configured-X", fresh: "configured-Y"} {
				saved, err := ds.instanceRepo.Get(ctx, want)
				require.NoError(t, err)
				assert.Equal(t, map[string]string{"pin-alias": got}, saved.ModelPins)
				assert.Equal(t, map[string]string{"pin-alias": family}, saved.ModelPinRoutes)
			}
			models, attempts := sink.seen()
			assert.Equal(t, []string{"configured-X", "configured-Y"}, models)
			assert.Equal(t, 2, attempts)
		})
	}
}

// TestD8ChildInputPreparationUsesPinnedConfiguredID covers the third batch
// consumer: the parent's spawn_workflow input preparation runs after a live
// profile edit and a key rotation, yet still calls the parent's configured ID
// through the persisted route exception.
func TestD8ChildInputPreparationUsesPinnedConfiguredID(t *testing.T) {
	for _, route := range []string{"azure", "bedrock"} {
		t.Run(route, func(t *testing.T) {
			family := provider.Canonicalize(route)
			sink := newD8ExecutionSink(t, family, false)
			sink.content = `{"topic":"x"}`
			ds, _, conn, p := bootD8RootWith(t, sink, route, func(dir string) {
				writeWorkflow(t, dir, "d8-child", `{"name":"d8-child","description":"child","inputs":[{"name":"topic","type":"string","description":"topic","required":true}],"nodes":[{"id":"c1","type":"transform","entrypoint":true,"config":{"operations":[{"variable":"done","operation":"set","value":true}]}}],"edges":[]}`)
				writeWorkflow(t, dir, "d8-parent", `{"name":"d8-parent","description":"call then spawn","inputs":[],"nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"pin-alias","templateFile":"templates/p.md"}},{"id":"spawn-1","type":"spawn_workflow","config":{"childWorkflow":"d8-child","spawnMode":"fire_and_forget","model_profile":"pin-alias"}}],"edges":[{"source":"ask","target":"spawn-1"}]}`)
				writePinTemplate(t, dir, "d8-parent")
			})
			ctx := adminCtxForLocalUser(t, ds)
			// The parent's first call fails upstream, so the run is parked on a
			// failed node; edits land before the retry reaches the preparer.
			sink.mu.Lock()
			sink.fail = true
			sink.mu.Unlock()
			id, _, err := ds.eng.Trigger(ctx, "d8-parent", nil, "normal")
			require.NoError(t, err)
			require.Equal(t, instance.StateFailed, waitTerminal(t, ds, id).State)
			p.Model = "configured-Y"
			_, _, err = ds.profileSvc.Update(ctx, p)
			require.NoError(t, err)
			secret := "rotated-secret"
			if family == provider.CanonicalBedrock {
				secret = `{"secret":"rotated-secret","session_token":"d8-session"}`
			}
			require.NoError(t, ds.connSvc.Update(ctx, conn, &secret))
			sink.mu.Lock()
			sink.key, sink.fail = "rotated-secret", false
			sink.mu.Unlock()
			ds.eng.Shutdown()
			require.NoError(t, ds.eng.Retry(ctx, id, nil))
			require.Equal(t, instance.StateCompleted, waitTerminal(t, ds, id).State)
			models, attempts := sink.seen()
			assert.Equal(t, []string{"configured-X", "configured-X", "configured-X"}, models,
				"failed call, retried call and child-input preparation all use the pinned ID")
			assert.Equal(t, 3, attempts)
			require.Len(t, childrenOf(t, ds, id), 1, "the preparer's reply must have produced the spawned child")
		})
	}
}
