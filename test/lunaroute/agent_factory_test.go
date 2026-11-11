package lunaroute_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These executables speak the installed CLIs' protocols, but are not live
// contract evidence. The real Factory/runner must supply every launch input.
func agentFactoryFixture(t *testing.T, backend, mode string) (string, string, *inferenceCapture) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	bins := t.TempDir()
	titleSystem, err := os.ReadFile(filepath.Join(repoRoot(t), evidencePath, "sources/lr06-title-prompt.txt"))
	require.NoError(t, err)
	compactionSystem, err := os.ReadFile(filepath.Join(repoRoot(t), evidencePath, "sources/lr06-compaction-prompt.txt"))
	require.NoError(t, err)
	compactionTemplate, err := os.ReadFile(filepath.Join(repoRoot(t), evidencePath, "sources/lr06-compaction-template.txt"))
	require.NoError(t, err)
	var fixtureStarted time.Time
	var firstRequest sync.Once
	c := fixtureCapture(t, func(w http.ResponseWriter, r *http.Request) {
		firstRequest.Do(func() { fixtureStarted = time.Now().UTC() })
		var body struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		path := "/v1/messages"
		if backend == "opencode" {
			path = "/v1/chat/completions"
		}
		if r.Method != "POST" || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "glm-5.3" {
			t.Error("Factory mission did not feed authenticated model/prompt to inference")
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if backend == "opencode" && len(body.Messages) == 3 {
			prompt := agentMissionPromptFor(backend)
			if mode == "correlation-title-prompt" {
				prompt = "unrelated mission"
			}
			if body.Messages[0].Role != "system" || body.Messages[0].Content != string(titleSystem) || body.Messages[1].Role != "user" || body.Messages[1].Content != openCodeTitlePrefix || body.Messages[2].Role != "user" || body.Messages[2].Content != prompt || body.Stream != (mode == "correlation-title-stream") {
				t.Error("title fixture received incorrect source prefix or mission history")
				w.WriteHeader(400)
				return
			}
			title := "<think>private reasoning</think>\\n Private fixture title \\nignored"
			if mode == "correlation-title-empty" {
				title = " "
			}
			if mode == "correlation-title-secret" {
				title = captureFixtureKey
			}
			titleModel := "glm-5.3"
			if mode == "correlation-title-model" {
				titleModel = "glm-5.3-flash"
			}
			if mode == "correlation-title-stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Private fixture title\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			} else if mode == "correlation-title-malformed" {
				io.WriteString(w, `{"model":"glm-5.3","choices":[`)
			} else {
				io.WriteString(w, `{"model":"`+titleModel+`","choices":[{"index":0,"message":{"content":"`+title+`"},"finish_reason":"stop"}]}`)
			}
			return
		}
		if backend == "opencode" && len(body.Messages) == 2 {
			prompt := agentMissionPromptFor(backend)
			if mode == "correlation-compaction-prompt" {
				prompt = "unrelated mission"
			}
			want := openCodeCompactionPrefix + "[User]: " + prompt + "\n</conversation>\n\n" + openCodeCompactionInstruction + "\n\n" + string(compactionTemplate)
			if body.Messages[0].Role != "system" || body.Messages[0].Content != string(compactionSystem) || body.Messages[1].Role != "user" || body.Messages[1].Content != want || body.Stream != (mode == "correlation-compaction-stream") {
				t.Error("compaction fixture received incorrect source prompt or mission history")
				w.WriteHeader(400)
				return
			}
			if mode != "correlation-compaction-export-missing" {
				if err := writeFactoryCompactionExport(root, mode, fixtureStarted); err != nil {
					t.Error("cannot write private compaction fixture")
					w.WriteHeader(500)
					return
				}
			}
			summary, model := "Private compaction summary", "glm-5.3"
			if mode == "correlation-compaction-secret" {
				summary = captureFixtureKey
			}
			if mode == "correlation-compaction-model" {
				model = "glm-5.3-flash"
			}
			if mode == "correlation-compaction-stream" {
				w.Header().Set("Content-Type", "text/event-stream")
				if _, err := io.WriteString(w, "data: {\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Private compaction summary\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"); err != nil {
					t.Error("cannot write private compaction stream")
				}
			} else {
				if err := json.NewEncoder(w).Encode(map[string]any{"model": model, "choices": []any{map[string]any{"index": 0, "message": map[string]string{"content": summary}, "finish_reason": "stop"}}}); err != nil {
					t.Error("cannot write private compaction response")
				}
			}
			return
		}
		if len(body.Messages) != 1 || body.Messages[0].Role != "user" || body.Messages[0].Content != agentMissionPromptFor(backend) {
			t.Error("Factory mission did not feed exact user prompt")
			w.WriteHeader(400)
			return
		}
		if backend == "opencode" {
			if err := writeFactorySubagentExports(root, mode, fixtureStarted); err != nil {
				t.Error("cannot write private lifecycle fixtures")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			io.WriteString(w, `{"model":"glm-5.3","choices":[{"message":{"content":"done"},"finish_reason":"stop"}]}`)
		} else {
			io.WriteString(w, `{"type":"message","model":"glm-5.3","stop_reason":"end_turn"}`)
		}
	}, captureLimits{})
	c.backend = backend
	common := `#!/bin/sh
set -eu
if [ "${1-}" = --version ]; then
 if [ '@MODE@' = correlation-version ]; then echo '1.19.6'; else echo '1.19.5'; fi
 exit 0
fi
case "$PWD" in '@ROOT@'|'@ROOT@'/*) ;; *) exit 40 ;; esac
case "$*" in *'@KEY@'*) exit 41 ;; esac
`
	claude := `
[ "$(cat)" = '@PROMPT@' ] || exit 42
[ "$ANTHROPIC_BASE_URL" = https://gw.lunaroute.com ] || exit 43
[ "$ANTHROPIC_AUTH_TOKEN" = '@KEY@' ] || exit 44
[ "${ANTHROPIC_API_KEY-unset}" = unset ] || exit 45
for v in ANTHROPIC_MODEL ANTHROPIC_DEFAULT_HAIKU_MODEL ANTHROPIC_SMALL_FAST_MODEL ANTHROPIC_DEFAULT_SONNET_MODEL ANTHROPIC_DEFAULT_OPUS_MODEL CLAUDE_CODE_SUBAGENT_MODEL; do
 eval "value=\${$v-}"; [ "$value" = glm-5.3 ] || exit 46
done
[ "$CLAUDE_CODE_MAX_CONTEXT_TOKENS" = 202752 ] || exit 47
[ "$CLAUDE_CODE_MAX_OUTPUT_TOKENS" = 32768 ] || exit 48
case " $* " in *' --model glm-5.3 '*) ;; *) exit 49 ;; esac
case " $* " in *' --allowedTools Read,Write,Bash '*) ;; *) exit 50 ;; esac
prev=; isolated=0
for a in "$@"; do
 if [ "$prev" = --setting-sources ] && [ -z "$a" ]; then isolated=1; fi
 prev=$a
done
[ "$isolated" = 1 ] || exit 51
key=$ANTHROPIC_AUTH_TOKEN
path=messages
`
	opencode := `
[ "$LUNAROUTE_API_KEY" = '@KEY@' ] || exit 44
case "$OPENCODE_CONFIG_CONTENT" in *'@KEY@'*) exit 45 ;; esac
for field in '"baseURL":"https://gw.lunaroute.com/v1"' '"apiKey":"{env:LUNAROUTE_API_KEY}"' '"small_model":"lunaroute/glm-5.3"' '"model":"lunaroute/glm-5.3"' '"enabled_providers":["lunaroute"]' '"limit":{"context":202752,"output":32768}' '"tool_call":true' '"permission":{"*":"deny","bash":"allow","read":"allow","task":"allow","write":"allow"}'; do
 case "$OPENCODE_CONFIG_CONTENT" in *"$field"*) ;; *) exit 47 ;; esac
done
if [ "$1" = debug ]; then
 [ "$*" = 'debug config --pure' ] || exit 48
 printf '%s' "$*" > '@ROOT@/controls-marker'
 case '@MODE@' in
  controls-secret-exit) echo 'debug @KEY@' >&2; exit 1 ;;
  controls-overflow) /usr/bin/awk 'BEGIN { for(i=0;i<1048577;i++) printf "x"; print "" }'; exit 0 ;;
  controls-malformed) echo '{'; exit 0 ;;
  controls-helper) printf '%s' "$OPENCODE_CONFIG_CONTENT" | /usr/bin/sed 's/"small_model":"lunaroute\/glm-5.3"/"small_model":"foreign\/other"/;s/{env:LUNAROUTE_API_KEY}/***/'; exit 0 ;;
  controls-limit) printf '%s' "$OPENCODE_CONFIG_CONTENT" | /usr/bin/sed 's/202752/202753/;s/{env:LUNAROUTE_API_KEY}/***/'; exit 0 ;;
 esac
 printf '%s\n' "$OPENCODE_CONFIG_CONTENT" | /usr/bin/sed 's/{env:LUNAROUTE_API_KEY}/***/'
 exit 0
fi
if [ "$1" = export ]; then
 if [ "$*" = 'export ses_child --pure' ]; then
  printf ses_child > '@ROOT@/subagent-export-marker'
  case '@MODE@' in
   subagent-export-exit) echo 'export @KEY@' >&2; exit 1 ;;
   subagent-export-overflow) /usr/bin/awk 'BEGIN { for(i=0;i<1048577;i++) printf "x"; print "" }'; exit 0 ;;
  esac
  cat '@ROOT@/child-export.json'
 elif [ "$*" = 'export ses_factory --pure' ]; then
  cat '@ROOT@/parent-export.json'
 else exit 48; fi
 exit 0
fi
[ "$1" = run ] && [ "$2" = '@PROMPT@' ] || exit 42
case " $* " in *' -m lunaroute/glm-5.3 --dangerously-skip-permissions --format json --pure '*) ;; *) exit 49 ;; esac
key=$LUNAROUTE_API_KEY
path=chat/completions
`
	finish := `
printf launched > '@ROOT@/launch-marker'
case '@MODE@' in
 exit) echo 'upstream @KEY@' >&2; exit 1 ;;
 cancel) exec sleep 30 ;;
esac
session_header=
case '@MODE@' in correlation-*) session_header='X-Opencode-Session-Id: ses_factory' ;; esac
/usr/bin/curl --fail --silent --show-error --noproxy '' --cacert "$SSL_CERT_FILE" --proxy "$HTTPS_PROXY" -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -H "$session_header" -d '{"model":"glm-5.3","messages":[{"role":"user","content":"@PROMPT@"}]}' "https://gw.lunaroute.com/v1/$path" >/dev/null
case '@MODE@' in correlation-*)
 parent=ses_factory
 if [ '@MODE@' = correlation-parent ]; then parent=ses_other; fi
 /usr/bin/curl --fail --silent --show-error --noproxy '' --cacert "$SSL_CERT_FILE" --proxy "$HTTPS_PROXY" -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -H 'X-Opencode-Session-Id: ses_child' -H "X-Opencode-Parent-Session-Id: $parent" -d '{"model":"glm-5.3","messages":[{"role":"user","content":"@PROMPT@"}]}' "https://gw.lunaroute.com/v1/$path" >/dev/null
 if [ '@MODE@' != correlation-title-missing ]; then
  title_session=ses_factory
  if [ '@MODE@' = correlation-title-session ]; then title_session=ses_other; fi
  title_count=1
  if [ '@MODE@' = correlation-title-duplicate ]; then title_count=2; fi
  while [ "$title_count" -gt 0 ]; do
   /usr/bin/curl --fail --silent --show-error --noproxy '' --cacert "$SSL_CERT_FILE" --proxy "$HTTPS_PROXY" -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -H "X-Opencode-Session-Id: $title_session" -d @TITLE_BODY@ "https://gw.lunaroute.com/v1/$path" >/dev/null
   title_count=$((title_count-1))
  done
 fi
 if [ '@MODE@' != correlation-compaction-missing ]; then
  compact_session=ses_factory
  compact_parent=
  if [ '@MODE@' = correlation-compaction-session ]; then compact_session=ses_other; fi
  if [ '@MODE@' = correlation-compaction-parent ]; then compact_parent='X-Opencode-Parent-Session-Id: ses_other'; fi
  compact_count=1
  if [ '@MODE@' = correlation-compaction-duplicate ]; then compact_count=2; fi
  while [ "$compact_count" -gt 0 ]; do
   /usr/bin/curl --fail --silent --show-error --noproxy '' --cacert "$SSL_CERT_FILE" --proxy "$HTTPS_PROXY" -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -H "X-Opencode-Session-Id: $compact_session" -H "$compact_parent" -d @COMPACTION_BODY@ "https://gw.lunaroute.com/v1/$path" >/dev/null
   compact_count=$((compact_count-1))
  done
 fi
 sleep 1
 ;; esac
if [ '@MODE@' != missing-file ]; then printf LUNAROUTE-OK > lunaroute-proof.txt; fi
if [ '@MODE@' = wrong-file ]; then printf WRONG > lunaroute-proof.txt; fi
if [ '@MODE@' != missing-command ] && [ -f lunaroute-proof.txt ]; then wc -c < lunaroute-proof.txt > lunaroute-command.txt; fi
if [ '@MODE@' = wrong-command ]; then printf 0 > lunaroute-command.txt; fi
if [ "$path" = messages ]; then
 if [ '@MODE@' != no-tool ]; then echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_one","content":"done"}]}}'; fi
 echo '{"type":"result","is_error":false,"result":"done","session_id":"ses_factory","modelUsage":{"glm-5.3":{}}}'
else
 if [ '@MODE@' != no-tool ]; then echo '{"type":"tool_use","sessionID":"ses_factory","part":{"sessionID":"ses_factory","messageID":"msg_answer","type":"tool","tool":"bash","state":{"status":"completed"}}}'; fi
 echo '{"type":"text","sessionID":"ses_factory","part":{"id":"prt_text","messageID":"msg_answer","sessionID":"ses_factory","type":"text","text":"done"}}'
fi
`
	body, name := claude, "claude"
	if backend == "opencode" {
		body, name = opencode, "opencode"
	}
	titlePrompt := agentMissionPromptFor("opencode")
	if mode == "correlation-title-prompt" {
		titlePrompt = "unrelated mission"
	}
	titleBody, err := json.Marshal(map[string]any{"model": "glm-5.3", "stream": mode == "correlation-title-stream", "messages": []map[string]string{{"role": "system", "content": string(titleSystem)}, {"role": "user", "content": openCodeTitlePrefix}, {"role": "user", "content": titlePrompt}}})
	require.NoError(t, err)
	compactionPrompt := agentMissionPromptFor("opencode")
	if mode == "correlation-compaction-prompt" {
		compactionPrompt = "unrelated mission"
	}
	compactionBody, err := json.Marshal(map[string]any{"model": "glm-5.3", "stream": mode == "correlation-compaction-stream", "messages": []map[string]string{{"role": "system", "content": string(compactionSystem)}, {"role": "user", "content": openCodeCompactionPrefix + "[User]: " + compactionPrompt + "\n</conversation>\n\n" + openCodeCompactionInstruction + "\n\n" + string(compactionTemplate)}}})
	require.NoError(t, err)
	script := strings.NewReplacer("@ROOT@", root, "@KEY@", captureFixtureKey, "@PROMPT@", agentMissionPromptFor(backend), "@MODE@", mode, "@TITLE_BODY@", "'"+strings.ReplaceAll(string(titleBody), "'", "'\"'\"'")+"'", "@COMPACTION_BODY@", "'"+strings.ReplaceAll(string(compactionBody), "'", "'\"'\"'")+"'").Replace(common + body + finish)
	bin := filepath.Join(bins, name)
	require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
	wrapped := c.WrapBinary(t, bin)
	if backend == "opencode" {
		// The same executable name used by the production Factory resolves to the
		// capture wrapper, without adding a destination override to production.
		pathDir := t.TempDir()
		require.NoError(t, os.Symlink(wrapped, filepath.Join(pathDir, "opencode")))
		t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	return root, wrapped, c
}

func agentFactoryRules(t *testing.T) provider.CapabilityRules {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capabilities.yaml")
	require.NoError(t, os.WriteFile(path, []byte("lunaroute_models:\n  glm-5.3:\n    context_tokens: {tokens: 202752, evidence: fixture#context}\n    output_tokens: {tokens: 32768, evidence: fixture#output}\n"), 0600))
	t.Setenv("ARBITRON_LUNAROUTE_E2E_CAPABILITIES", path)
	return liveRules(t)
}

func TestAgentEvidenceFactoryMission(t *testing.T) {
	for _, backend := range []string{"claude-code", "opencode"} {
		t.Run(backend, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, backend, "success")
			factory := liveFactory(root, bin, agentFactoryRules(t))
			result, ws := runLiveMission(t, factory, liveBinding(captureFixtureKey, "glm-5.3", backend), root)
			require.Equal(t, "glm-5.3", result.Model)
			require.Equal(t, "ses_factory", result.SessionID)
			require.NotEmpty(t, result.ModelEvidence)
			assert.Equal(t, "glm-5.3", result.ModelEvidence[0].Model)
			assert.NotEqual(t, root, ws, "mission owns a fresh child workspace")
			proof, err := os.ReadFile(filepath.Join(ws, agentProofFile))
			require.NoError(t, err)
			assert.Equal(t, agentProofText, string(proof))
			command, err := os.ReadFile(filepath.Join(ws, agentCommandFile))
			require.NoError(t, err)
			assert.Equal(t, "12", strings.TrimSpace(string(command)))
			records := capture.Records()
			require.Len(t, records, 1, "env/config intent is not inference proof")
			require.NoError(t, validateCapturedInference(records, "glm-5.3", []string{"main"}))
			assert.Equal(t, "glm-5.3", records[0].RequestedModel)
			assert.Equal(t, []string{"glm-5.3"}, records[0].ReportedModels)
			assert.True(t, records[0].AuthMatchesSelected && records[0].Completed)
			marker, err := os.ReadFile(filepath.Join(root, "launch-marker"))
			require.NoError(t, err)
			assert.Equal(t, "launched", string(marker))
		})
	}
}

func TestAgentEvidenceFactoryMissionRejectsFalseSuccess(t *testing.T) {
	for _, backend := range []string{"claude-code", "opencode"} {
		for _, mode := range []string{"missing-file", "wrong-file", "missing-command", "wrong-command", "no-tool", "exit", "cancel"} {
			t.Run(backend+"/"+mode, func(t *testing.T) {
				root, bin, capture := agentFactoryFixture(t, backend, mode)
				client, _ := agentFactoryProbeClient(t)
				factory := liveFactoryWithClient(root, bin, agentFactoryRules(t), client)
				ctx := context.Background()
				if mode == "cancel" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, agentVersionTimeout)
					defer cancel()
					// Cancel only after the real mission subprocess starts. A
					// short wall-clock deadline could expire during preflight
					// under load and would not exercise runner cancellation.
					go func() {
						ticker := time.NewTicker(10 * time.Millisecond)
						defer ticker.Stop()
						for {
							if _, err := os.Stat(filepath.Join(root, "launch-marker")); err == nil {
								cancel()
								return
							}
							select {
							case <-ctx.Done():
								return
							case <-ticker.C:
							}
						}
					}()
				}
				result, err := executeAgentMission(ctx, factory, liveBinding(captureFixtureKey, "glm-5.3", backend), root)
				require.Error(t, err)
				assert.Nil(t, result)
				assert.NotContains(t, err.Error(), captureFixtureKey)
				_, markerErr := os.Stat(filepath.Join(root, "launch-marker"))
				require.NoError(t, markerErr, "a failed preflight cannot substitute for the requested subprocess failure")
				if mode != "cancel" && mode != "exit" {
					require.Len(t, capture.Records(), 1, "reject actual terminal responses without the required tool/file sink")
				}
			})
		}
	}
}

func agentFactoryProbeClient(t *testing.T) (*http.Client, *int) {
	t.Helper()
	calls := 0
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != hostedRoot+"/v1/models" || r.Header.Get("Authorization") != "Bearer "+captureFixtureKey {
			t.Error("diagnosis probe did not consume the selected connection")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"glm-5.3"}]}`))}, nil
	})}, &calls
}

func TestAgentEvidenceFactoryDiagnosis(t *testing.T) {
	for _, backend := range []string{"claude-code", "opencode"} {
		t.Run(backend, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, backend, "exit")
			client, calls := agentFactoryProbeClient(t)
			result, err := executeAgentMission(context.Background(), liveFactoryWithClient(root, bin, agentFactoryRules(t), client), liveBinding(captureFixtureKey, "glm-5.3", backend), root)
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Equal(t, 1, *calls, "failure must use the real shared probe selector, never outbound test traffic")
			assert.Empty(t, capture.Records())
			assert.NotContains(t, err.Error(), captureFixtureKey)
		})
	}
}

func TestAgentEvidenceFactoryMissionPreflight(t *testing.T) {
	for _, mode := range []string{"precreated", "foreign-key", "missing-rules"} {
		t.Run(mode, func(t *testing.T) {
			root, bin, capture := agentFactoryFixture(t, "claude-code", "success")
			rules := agentFactoryRules(t)
			binding := liveBinding(captureFixtureKey, "glm-5.3", "claude-code")
			switch mode {
			case "precreated":
				require.NoError(t, os.WriteFile(filepath.Join(root, agentProofFile), []byte(agentProofText), 0600))
			case "foreign-key":
				binding = liveBinding("sk-foreign", "glm-5.3", "claude-code")
			case "missing-rules":
				rules = provider.CapabilityRules{}
			}
			result, err := executeAgentMission(context.Background(), liveFactory(root, bin, rules), binding, root)
			require.Error(t, err)
			assert.Nil(t, result)
			assert.Empty(t, capture.Records())
			_, err = os.Stat(filepath.Join(root, "launch-marker"))
			assert.True(t, os.IsNotExist(err))
		})
	}
}
