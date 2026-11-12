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
			Model     string `json:"model"`
			Stream    bool   `json:"stream"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
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
		if backend == "claude-code" {
			wantBudget := 32768
			if mode == "limits-output-absent" {
				wantBudget = 0
			}
			if mode == "limits-output-drift" {
				wantBudget = 32767
			}
			if body.MaxTokens != wantBudget {
				t.Error("Claude request did not feed the emitted output budget")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
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
		if backend == "claude-code" {
			memory, err := os.ReadFile(filepath.Join(root, "mission/CLAUDE.md"))
			if err == nil {
				if len(body.Messages) == 2 {
					want := "Contents of " + filepath.Join(root, "mission/CLAUDE.md") + " (project instructions, checked into the codebase):\n\n" + strings.TrimSpace(string(memory))
					role := "user"
					if mode == "memory-drift" {
						want += "-substituted"
					}
					if mode == "memory-assistant" {
						role = "assistant"
					}
					if body.Messages[0].Role != role || body.Messages[0].Content != want {
						t.Error("Claude fixture did not feed exact private memory input")
						w.WriteHeader(400)
						return
					}
					body.Messages = body.Messages[1:]
				} else if mode != "memory-absent" && mode != "memory-late" {
					t.Error("Claude fixture omitted seeded project memory")
					w.WriteHeader(400)
					return
				}
			} else if !os.IsNotExist(err) {
				t.Error("cannot read private memory fixture")
				w.WriteHeader(500)
				return
			}
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
 if [ '@BACKEND@' = claude-code ]; then
  case "$PWD" in '@ROOT@'|'@ROOT@'/*) ;; *) exit 40 ;; esac
  [ "$(cat)" = '' ] || exit 42
  [ "$ANTHROPIC_BASE_URL" = https://gw.lunaroute.com ] || exit 43
  [ "$ANTHROPIC_AUTH_TOKEN" = '@KEY@' ] || exit 44
  [ "$ANTHROPIC_MODEL" = glm-5.3 ] || exit 46
  printf queried > '@ROOT@/version-marker'
  if [ '@MODE@' = startup-version-query ]; then echo 'version @KEY@' >&2; exit 1; fi
  echo '2.1.322 (Claude Code)'
 else
  if [ '@MODE@' = correlation-version ]; then echo '1.19.6'; else echo '1.19.5'; fi
 fi
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
case '@MODE@' in correlation-force-*) limit='"context":202752';; *) limit='"limit":{"context":202752,"output":32768}';; esac
for field in '"baseURL":"https://gw.lunaroute.com/v1"' '"apiKey":"{env:LUNAROUTE_API_KEY}"' '"small_model":"lunaroute/glm-5.3"' '"model":"lunaroute/glm-5.3"' '"enabled_providers":["lunaroute"]' "$limit" '"tool_call":true' '"permission":{"*":"deny","bash":"allow","read":"allow","task":"allow","write":"allow"}'; do
 case "$OPENCODE_CONFIG_CONTENT" in *"$field"*) ;; *) exit 47 ;; esac
done
if [ "$1" = debug ]; then
 [ "$*" = 'debug config --pure' ] || exit 48
 printf '%s' "$*" > '@ROOT@/controls-marker'
 case '@MODE@' in
  controls-secret-exit) echo 'debug @KEY@' >&2; exit 1 ;;
  controls-overflow) /usr/bin/awk 'BEGIN { for(i=0;i<1048577;i++) printf "x"; print "" }'; exit 0 ;;
  controls-malformed) echo '{'; exit 0 ;;
  correlation-force-controls-input) case "$OPENCODE_CONFIG_CONTENT" in *'"reserved":'*) printf '%s' "$OPENCODE_CONFIG_CONTENT" | /usr/bin/sed 's/"input":202752/"input":202753/;s/{env:LUNAROUTE_API_KEY}/***/'; exit 0 ;; esac ;;
  correlation-force-controls-auto) case "$OPENCODE_CONFIG_CONTENT" in *'"reserved":'*) printf '%s' "$OPENCODE_CONFIG_CONTENT" | /usr/bin/sed 's/"auto":true/"auto":false/;s/{env:LUNAROUTE_API_KEY}/***/'; exit 0 ;; esac ;;
  correlation-force-controls-reserve) case "$OPENCODE_CONFIG_CONTENT" in *'"reserved":'*) printf '%s' "$OPENCODE_CONFIG_CONTENT" | /usr/bin/sed 's/"reserved":200704/"reserved":0/;s/{env:LUNAROUTE_API_KEY}/***/'; exit 0 ;; esac ;;
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
if [ "$1" = run ] && [ "$2" = '@CONTINUATION@' ]; then
 [ "$*" = 'run @CONTINUATION@ -m lunaroute/glm-5.3 --session ses_factory --dangerously-skip-permissions --format json --pure' ] || exit 52
 reserve='"reserved":200704'
 if [ '@MODE@' = correlation-force-usage-total ]; then reserve='"reserved":198656'; fi
 for field in '"input":202752' '"output":32768' "$reserve" '"auto":true'; do
  case "$OPENCODE_CONFIG_CONTENT" in *"$field"*) ;; *) exit 53 ;; esac
 done
 printf resumed > '@ROOT@/resume-marker'
 if [ '@MODE@' = correlation-force-resume-exit ]; then echo '@KEY@' >&2; exit 1; fi
 if [ '@MODE@' != correlation-force-no-traffic ]; then
  /usr/bin/curl --fail --silent --show-error --noproxy '' --cacert "$SSL_CERT_FILE" --proxy "$HTTPS_PROXY" -H "Authorization: Bearer $LUNAROUTE_API_KEY" -H 'Content-Type: application/json' -H 'X-Opencode-Session-Id: ses_factory' -d @COMPACTION_BODY@ https://gw.lunaroute.com/v1/chat/completions >/dev/null
  sleep 1
 fi
 echo '{"type":"text","sessionID":"ses_factory","part":{"type":"text","text":"acknowledged"}}'
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
request_body='{"model":"glm-5.3",@CLAUDE_BUDGET@"messages":[{"role":"user","content":"@PROMPT@"}]}'
make_memory_body() {
 memory=$(cat CLAUDE.md)
 role=user
 if [ '@MODE@' = memory-drift ]; then memory="$memory-substituted"; fi
 if [ '@MODE@' = memory-assistant ]; then role=assistant; fi
 printf '{"model":"glm-5.3",@CLAUDE_BUDGET@"messages":[{"role":"%s","content":"Contents of %s/CLAUDE.md (project instructions, checked into the codebase):\\n\\n%s"},{"role":"user","content":"@PROMPT@"}]}' "$role" "$PWD" "$memory"
}
if [ "$path" = messages ] && [ -f CLAUDE.md ]; then
 case '@MODE@' in memory-absent|memory-late) ;; *) request_body=$(make_memory_body) ;; esac
fi
/usr/bin/curl --fail --silent --show-error --noproxy '' --cacert "$SSL_CERT_FILE" --proxy "$HTTPS_PROXY" -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -H "$session_header" -d "$request_body" "https://gw.lunaroute.com/v1/$path" >/dev/null
if [ '@MODE@' = memory-late ]; then
 request_body=$(make_memory_body)
 /usr/bin/curl --fail --silent --show-error --noproxy '' --cacert "$SSL_CERT_FILE" --proxy "$HTTPS_PROXY" -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -d "$request_body" "https://gw.lunaroute.com/v1/$path" >/dev/null
fi
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
  case '@MODE@' in correlation-force-*) compact_count=0 ;; *) compact_count=1 ;; esac
  compact_session=ses_factory
  compact_parent=
  if [ '@MODE@' = correlation-compaction-session ]; then compact_session=ses_other; fi
  if [ '@MODE@' = correlation-compaction-parent ]; then compact_parent='X-Opencode-Parent-Session-Id: ses_other'; fi
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
 emit_startup() {
  model=glm-5.3; version=2.1.322; cwd=$PWD; session=ses_factory
  case '@MODE@' in
   startup-model) model=glm-5.3-flash ;;
   startup-version) version=2.1.321 ;;
   startup-cwd) cwd=/unrelated ;;
   startup-session) session=ses_other ;;
   startup-malformed) echo '{'; return ;;
  esac
  printf '{"type":"system","subtype":"init","model":"%s","claude_code_version":"%s","cwd":"%s","session_id":"%s"}\n' "$model" "$version" "$cwd" "$session"
 }
 case '@MODE@' in startup-absent|startup-late) ;; *) emit_startup ;; esac
 if [ '@MODE@' = startup-duplicate ]; then emit_startup; fi
 if [ '@MODE@' != no-tool ]; then echo '{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_one","content":"done"}]}}'; fi
 if [ '@MODE@' = startup-late ]; then emit_startup; fi
 echo '{"type":"result","is_error":false,"result":"done","session_id":"ses_factory","modelUsage":{"glm-5.3":{@CLAUDE_CONTEXT@}}}'
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
	budget, contextField := "", `"contextWindow":202752,"maxOutputTokens":8192`
	if backend == "claude-code" {
		budget = `"max_tokens":32768,`
	}
	switch mode {
	case "limits-output-absent":
		budget = ""
	case "limits-output-drift":
		budget = `"max_tokens":32767,`
	case "limits-context-absent":
		contextField = `"maxOutputTokens":8192`
	case "limits-context-drift":
		contextField = `"contextWindow":202753,"maxOutputTokens":8192`
	case "limits-context-secret":
		contextField = `"contextWindow":"` + captureFixtureKey + `"`
	}
	script := strings.NewReplacer("@CLAUDE_BUDGET@", budget, "@CLAUDE_CONTEXT@", contextField, "@BACKEND@", backend, "@ROOT@", root, "@KEY@", captureFixtureKey, "@PROMPT@", agentMissionPromptFor(backend), "@CONTINUATION@", openCodeCompactionContinuation, "@MODE@", mode, "@TITLE_BODY@", "'"+strings.ReplaceAll(string(titleBody), "'", "'\"'\"'")+"'", "@COMPACTION_BODY@", "'"+strings.ReplaceAll(string(compactionBody), "'", "'\"'\"'")+"'").Replace(common + body + finish)
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
