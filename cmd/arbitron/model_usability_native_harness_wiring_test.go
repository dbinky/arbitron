package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nativeHarnessScript = `#!/bin/sh
if [ "$1" = '--version' ]; then echo '@VERSION@'; exit 0; fi
[ "$(dirname "$(pwd -P)")" = "$(cd '@WORKDIR@' && pwd -P)" ] || exit 40
@AUTH@
arg() { flag="$1"; shift; while [ "$#" -gt 0 ]; do if [ "$1" = "$flag" ]; then [ "$#" -gt 1 ] || return 1; printf '%s' "$2"; return; fi; shift; done; return 1; }
printf '%s\n' "$*" >> '@ARGV@'
case '@BACKEND@' in
claude-code)
 [ "$(cat)" = 'Reply with the single word: ok' ] || exit 41
 case " $* " in *' --fallback-model '*|*' --dangerously-skip-permissions '*) exit 42 ;; esac
 case " $* " in *' --allowedTools '* ) ;; *) exit 43 ;; esac
 m=$(arg --model "$@")
 [ "$m" = '@MODEL@' ] || exit 44
 [ "$(arg --output-format "$@")" = stream-json ] || exit 45
 [ '@OUTCOME@' != wrong ] || m=fallback
 [ '@OUTCOME@' != missing ] || m=''
 echo '{"type":"result","is_error":false,"result":"ok","session_id":"native-session","modelUsage":{"'"$m"'":{}}}'
 ;;
pi)
 [ "$(cat)" = 'Reply with the single word: ok' ] || exit 41
 case " $* " in *' --no-tools --no-mcp '*) ;; *) exit 42 ;; esac
 m=$(arg --model "$@")
 [ "$m" = '@MODEL@' ] || exit 44
 [ "$(arg --mode "$@")" = json ] || exit 45
 case " $* " in *' --print --mode json --no-session '*) ;; *) exit 46 ;; esac
 [ '@OUTCOME@' != wrong ] || m=fallback
 [ '@OUTCOME@' != missing ] || m=''
 echo '{"type":"session","version":3,"id":"pi-root-session"}'
 echo '{"type":"message_end","message":{"role":"assistant","provider":"anthropic","model":"'"$m"'","stopReason":"stop","content":[{"type":"text","text":"ok"}]}}'
 ;;
opencode)
 overlay=false
 case "$OPENCODE_CONFIG_CONTENT" in *'"hostile":{"enabled":false}'*) overlay=true ;; esac
 case "$1" in
 debug)
  [ "$2 $3" = 'config --pure' ] || exit 50
  enabled=true
  [ "$overlay" = true ] && enabled=false
  echo '{"mcp":{"hostile":{"type":"local","command":["touch","MARKER"],"enabled":'"$enabled"'}}}'
  ;;
 run)
  case " $* " in *' --pure '*) ;; *) exit 49 ;; esac
  [ "$overlay" = true ] || exit 51
  [ "$(arg -m "$@")" = 'anthropic/@MODEL@' ] || exit 44
  [ "$(arg --format "$@")" = json ] || exit 45
  case " $* " in *'Reply with the single word: ok'*) ;; *) exit 46 ;; esac
  case "$OPENCODE_CONFIG_CONTENT" in *'"baseURL":"https://api.anthropic.com"'*) ;; *) exit 47 ;; esac
  case "$OPENCODE_CONFIG_CONTENT" in *'"apiKey":"{env:ANTHROPIC_API_KEY}"'*) ;; *) exit 48 ;; esac
  case "$OPENCODE_CONFIG_CONTENT" in *'"*":"deny"'*) ;; *) exit 42 ;; esac
  echo '{"type":"text","sessionID":"ses_native","part":{"sessionID":"ses_native","messageID":"native-message","text":"ok"}}'
  ;;
 export)
  [ "$2" = ses_native ] || exit 43
  [ "$3" = --pure ] || exit 49
  m='@MODEL@'
  [ '@OUTCOME@' != wrong ] || m=fallback
  [ '@OUTCOME@' != missing ] || m=''
  echo '{"info":{"id":"ses_native"},"messages":[{"info":{"id":"native-message","sessionID":"ses_native","role":"assistant","providerID":"anthropic","modelID":"'"$m"'","time":{"created":1,"completed":2}},"parts":[]}]}'
  ;;
 esac
 ;;
esac
`

func TestUsabilityConfirmedExactModel(t *testing.T) {
	for _, backend := range []string{"claude-code", "pi", "opencode", "codex"} {
		modes := []string{modelcatalog.ExecutionModeBatch}
		if backend == "claude-code" || backend == "codex" {
			modes = append(modes, modelcatalog.ExecutionModeInteractive)
		}
		for _, mode := range modes {
			for _, outcome := range []string{"exact", "wrong", "missing", "account edit", "config edit"} {
				t.Run(backend+"/"+mode+"/"+outcome, func(t *testing.T) {
					dir := t.TempDir()
					home := filepath.Join(dir, "account")
					require.NoError(t, os.MkdirAll(home, 0o700))
					t.Setenv("HOME", home)
					t.Setenv(codexInteractiveVerifiedEnv, "true")
					binary, version, old, model := backend, "1.3.0", "claude-sonnet-4-6", "vendor/native-preview-20261018"
					if backend == "claude-code" {
						binary, version, model = "claude", "2.1.301", "claude-native-preview-20261018"
						t.Setenv("CLAUDE_CONFIG_DIR", home)
					}
					if backend == "opencode" {
						version = "opencode 1.19.3"
						t.Setenv("XDG_CONFIG_HOME", home)
					}

					if backend == "codex" {
						version, old, model = "codex-cli 0.171.0", "gpt-5-sol", "gpt-native-preview-20261018"
						t.Setenv("CODEX_HOME", home)
						writeCodexAccountModels(t, home, model)
					}
					log := filepath.Join(dir, "argv")
					// Install the binary before boot so the real inventory fingerprints it.
					bin := filepath.Join(dir, binary)
					require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho '"+version+"'\n"), 0o700))
					t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
					cfg := "profiles: []\nllm:\n  defaultagentbackend: " + backend + "\n"
					if backend == "claude-code" {
						cfg += "  binarypath: " + bin + "\n"
					}
					ds := bootSinkRoot(t, cfg, func(ws string) {
						kind := "agent_call"
						if mode == modelcatalog.ExecutionModeInteractive {
							kind = "interactive_agent"
						}
						writeWorkflow(t, ws, "native-agent", fmt.Sprintf(`{"name":"native-agent","nodes":[{"id":"ask","type":%q,"entrypoint":true,"config":{"model_profile":"native-agent","templateFile":"templates/p.md"}}],"edges":[]}`, kind))
						writePinTemplate(t, ws, "native-agent")
					})
					ctx := adminCtxForLocalUser(t, ds)
					conn := ""
					auth := "[ \"$ANTHROPIC_API_KEY\" = 'native-harness-secret' ] || exit 44"
					if backend == "codex" {
						auth = "[ \"$CODEX_HOME\" = '" + home + "' ] || exit 44"
					} else {
						conn = "native-harness"
						require.NoError(t, ds.connSvc.Create(ctx, provider.Connection{Name: conn, Provider: provider.CanonicalAnthropicAPI, BaseURL: "https://api.anthropic.com"}, "native-harness-secret"))
					}
					_, _, err := ds.profileSvc.Create(ctx, provider.Profile{Alias: "native-agent", Kind: provider.ProfileKindAgent, Backend: backend, ConnectionRef: conn, Model: old})
					require.NoError(t, err)
					fallback := filepath.Join(dir, "fallback")
					script := strings.NewReplacer("@BACKEND@", backend, "@VERSION@", version, "@MODEL@", model, "@OUTCOME@", outcome, "@ARGV@", log, "@WORKDIR@", ds.refreshDeps.ProbeDir, "@AUTH@", auth).Replace(nativeHarnessScript)
					if backend == "codex" {
						script = strings.NewReplacer("@ARGV@", log, "@FALLBACK@", fallback).Replace(codexProbeScript)
						script = strings.Replace(script, "m=$(echo", auth+"\n[ \"$(cat)\" = 'Reply with only your model identity on one line. Do not use tools or take any other action.' ] || exit 45\n[ \"$(dirname \"$(pwd -P)\")\" = \"$(cd '"+ds.refreshDeps.ProbeDir+"' && pwd -P)\" ] || exit 46\ncase \" $* \" in *' --sandbox read-only '*|*' -s read-only '*) ;; *) exit 47 ;; esac\nm=$(echo", 1)
						if outcome == "wrong" || outcome == "missing" {
							actual := "fallback"
							if outcome == "missing" {
								actual = ""
							}
							require.NoError(t, os.WriteFile(fallback, []byte(actual), 0o600))
						}
					}
					require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
					snap, err := ds.pathInventory.Snapshot(ctx)
					require.NoError(t, err)
					var path modelcatalog.ExecutionPath
					for _, p := range snap.Paths {
						if p.Backend == backend && p.Mode == mode {
							for _, a := range p.Aliases {
								if a == "native-agent" {
									path = p
								}
							}
						}
					}
					require.NotEmpty(t, path.Key)
					if outcome == "account edit" || outcome == "config edit" {
						name := "settings.json"
						if outcome == "account edit" {
							name = ".credentials.json"
						}
						if backend == "codex" {
							name = "config.toml"
							if outcome == "account edit" {
								name = "auth.json"
							}
						}
						if backend == "pi" && outcome == "account edit" {
							name = "auth.json"
						}
						if backend == "pi" {
							name = filepath.Join(".pi", "agent", name)
						}
						if backend == "opencode" {
							name = filepath.Join(".config", "opencode", "opencode.json")
							if outcome == "account edit" {
								name = filepath.Join(".local", "share", "opencode", "auth.json")
							}
						}
						require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(home, name)), 0o700))
						require.NoError(t, os.WriteFile(filepath.Join(home, name), []byte(`{"changed":true}`), 0o600))
					}
					refresh, err := newModelRefresh(ds.refreshDeps)
					require.NoError(t, err)
					defer refresh.stop()
					e := modelidentity.Entry{CanonicalID: "anthropic/" + model, Provider: "anthropic", NativeID: model}
					if backend == "codex" {
						e.Provider = "openai"
						e.CanonicalID = "openai/" + model
					}
					ev, err := refresh.probe.Check(ctx, path, e)
					require.NoError(t, err)
					// OpenCode probes under D12: every resolved MCP server is
					// disabled and verified, and the run/export use --pure (the
					// fake exits non-zero otherwise), so it confirms like the others.
					assert.Equal(t, outcome == "exact", ev.Confirmed(), fmt.Sprintf("native probe evidence: %+v", ev))
					assert.Equal(t, model, ev.NativeModel)
					if outcome == "exact" {
						require.NotEmpty(t, ev.ProofRef)
						// The durable proof names the harness's own session (G13).
						if session, ok := map[string]string{"claude-code": "native-session", "pi": "pi-root-session", "opencode": "ses_native"}[backend]; ok && mode == modelcatalog.ExecutionModeBatch {
							assert.Equal(t, "agent-exec:"+backend+":"+model+":"+session, ev.ProofRef)
						}
					} else {
						assert.Empty(t, ev.ProofRef)
					}
					if outcome == "account edit" || outcome == "config edit" {
						_, err := os.Stat(log)
						assert.True(t, os.IsNotExist(err), "a stale account/config must not launch")
					}
				})
			}
		}
	}
}
