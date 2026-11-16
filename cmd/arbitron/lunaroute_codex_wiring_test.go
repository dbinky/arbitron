package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeAgentWorkflow(t *testing.T, workspaceDir, name, alias string) {
	t.Helper()
	writeWorkflow(t, workspaceDir, name, fmt.Sprintf(`{"name":%q,"nodes":[{"id":"work","type":"agent_call","entrypoint":true,"config":{"model_profile":%q,"templateFile":"templates/p.md"}}],"edges":[]}`, name, alias))
	writePinTemplate(t, workspaceDir, name)
}

// This is the actual subprocess sink: no provider factory or runner is replaced.
// A successful rollout is emitted only after validating the selected route,
// credential, settings, private home, prompt and allocated instance workspace.
func fakeLunaRouteCodex(t *testing.T, dir, model string) (argvLog, homesLog, failFlag, versionFlag, driftFlag string) {
	t.Helper()
	argvLog, homesLog = filepath.Join(dir, "argv"), filepath.Join(dir, "homes")
	failFlag, versionFlag, driftFlag = filepath.Join(dir, "FAIL"), filepath.Join(dir, "BAD_VERSION"), filepath.Join(dir, "DRIFT")
	script := strings.NewReplacer("@ARGV@", argvLog, "@HOMES@", homesLog, "@FAIL@", failFlag, "@VERSION@", versionFlag, "@DRIFT@", driftFlag, "@EFFORT@", filepath.Join(dir, "EFFORT"), "@MODEL@", model, "@KEY@", lrRootKey).Replace(`#!/bin/sh
set -eu
[ -z "${OPENAI_API_KEY-}" ] && [ -z "${OPENAI_BASE_URL-}" ] || exit 41
[ -n "${CODEX_HOME-}" ] && [ -d "$CODEX_HOME" ] || exit 42
case "$CODEX_HOME" in */arbitron-codex-lunaroute-*) ;; *) exit 43 ;; esac
[ "$CODEX_HOME" != "$HOME/.codex" ] || exit 44
[ ! -e "$CODEX_HOME/auth.json" ] && [ ! -e "$CODEX_HOME/config.toml" ] || exit 45
permissions=$(LC_ALL=C ls -ld "$CODEX_HOME")
case "$permissions" in drwx------*) ;; *) exit 58 ;; esac
if [ "$1" = --version ]; then
 printf '%s\n' "$CODEX_HOME" >> '@HOMES@.version'
 [ -z "${LUNAROUTE_API_KEY-}" ] || exit 46
 if [ -f '@VERSION@' ]; then echo 'codex-cli 0.163.0'; else echo 'codex-cli 0.167.2'; fi
 exit 0
fi
[ "$LUNAROUTE_API_KEY" = '@KEY@' ] || exit 40
[ "$(cat)" = hello ] || exit 52
case "$PWD" in */instances/*) ;; *) exit 55 ;; esac
printf '%s\n' "$PWD" >> '@ARGV@.cwd'
printf '%s\n' "$*" >> '@ARGV@'
printf '%s\n' "$CODEX_HOME" >> '@HOMES@'
case "$*" in *'@KEY@'*) exit 51 ;; esac
case " $* " in *' exec --dangerously-bypass-approvals-and-sandbox '*) ;; *) exit 56 ;; esac
for cfg in 'model_provider="lunaroute"' 'model_providers.lunaroute.name="LunaRoute"' 'model_providers.lunaroute.base_url="https://gw.lunaroute.com/v1"' 'model_providers.lunaroute.env_key="LUNAROUTE_API_KEY"' 'model_providers.lunaroute.wire_api="responses"' 'review_model="@MODEL@"'; do
 case " $* " in *" -c $cfg "*) ;; *) exit 47 ;; esac
done
want=high; [ ! -f '@EFFORT@' ] || want=$(cat '@EFFORT@')
efforts=$(printf '%s\n' "$@" | grep '^model_reasoning_effort=' || true)
if [ -z "$want" ]; then [ -z "$efforts" ] || exit 48; else [ "$efforts" = "model_reasoning_effort=$want" ] || exit 48; case " $* " in *" -c model_reasoning_effort=$want "*) ;; *) exit 48 ;; esac; fi
m=; wd=; while [ "$#" -gt 0 ]; do
 case "$1" in -m) shift; m="$1" ;; --cd) shift; wd="$1" ;; esac
 shift
done
[ "$m" = '@MODEL@' ] && [ "$wd" = "$PWD" ] || exit 53
if [ -f '@FAIL@' ]; then printf checked-route > failure-reached; echo 'opaque process error @KEY@' >&2; exit 17; fi
if [ -f '@DRIFT@' ]; then m=glm-999.9; fi
id=lrcodex-session-0001
d="$CODEX_HOME/sessions/2026/11/11"
mkdir -p "$d"
printf '%s\n' '{"type":"session_meta","payload":{"id":"'"$id"'"}}' '{"type":"turn_context","payload":{"model":"'"$m"'","model_provider":"lunaroute"}}' > "$d/rollout-root-$id.jsonl"
echo '{"type":"thread.started","thread_id":"'"$id"'"}'
echo '{"type":"item.completed","item":{"type":"agent_message","text":"codex root ok"}}'
echo '{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}'
`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "codex"), []byte(script), 0o700))
	return
}

func codexRoot(t *testing.T, model string, gw *agentListingGateway) (*domainStack, string, string, string, string, string) {
	t.Helper()
	return codexRootWith(t, model, gw, fmt.Sprintf(`lunaroute_models:
  %s:
    efforts:
      codex: {levels: [high], evidence: "root-fixture#exact-codex-high"}
`, model), "high")
}

// codexRootWith boots the real root against the fake codex with the given
// capability override and saves the pinned codex profile with effort through MCP.
func codexRootWith(t *testing.T, model string, gw *agentListingGateway, capsYAML, effort string) (*domainStack, string, string, string, string, string) {
	t.Helper()
	dir := t.TempDir()
	argv, homes, fail, version, drift := fakeLunaRouteCodex(t, dir, model)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex"), 0o700))
	for name, data := range map[string]string{"auth.json": `{"tokens":{"access_token":"subscription-token"}}`, "config.toml": "model_provider = \"openai\"\n"} {
		path := filepath.Join(home, ".codex", name)
		require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
		t.Cleanup(func() { got, err := os.ReadFile(path); require.NoError(t, err); assert.Equal(t, data, string(got)) })
	}
	t.Setenv("HOME", home)
	operator := t.TempDir()
	t.Setenv("CODEX_HOME", operator)
	t.Setenv("OPENAI_API_KEY", "sk-ambient-must-not-reach")
	t.Setenv("OPENAI_BASE_URL", "https://foreign.example/v1")
	caps := lunaRouteCapsFile(t, capsYAML)
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+caps+"\n", gw, func(ws string) { writeAgentWorkflow(t, ws, "lr-codex", "lr-codex-agent") })
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": lrRootKey})
	require.False(t, bad, text)
	for _, m := range []string{"gpt-6.1-sol", "glm-999.9", "claude-sonnet-5-lr1"} {
		bad, text = call("profile_create", map[string]any{"alias": "lr-codex-wrong", "kind": "agent", "connection_ref": "lr", "model": m, "backend": "codex"})
		require.True(t, bad, text)
		assert.Contains(t, text, "reviewed fixed LunaRoute id")
		var n int
		require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias='lr-codex-wrong'`).Scan(&n))
		assert.Zero(t, n)
	}
	bad, text = call("profile_create", map[string]any{"alias": "lr-codex-agent", "kind": "agent", "connection_ref": "lr", "model": model, "backend": "codex", "effort": effort})
	require.False(t, bad, text)
	t.Cleanup(func() {
		for _, root := range []string{filepath.Join(home, ".codex"), operator} {
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			if root == operator {
				assert.Empty(t, entries)
			} else {
				assert.Len(t, entries, 2, "no shared subscription session writes")
			}
		}
		for _, log := range []string{homes, homes + ".version"} {
			raw, err := os.ReadFile(log)
			if errors.Is(err, os.ErrNotExist) {
				continue // Local pre-launch failures have no corresponding subprocess.
			}
			require.NoError(t, err)
			for _, path := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				_, err = os.Stat(path)
				assert.True(t, os.IsNotExist(err), "private home removed: %s", path)
			}
		}
	})
	return ds, argv, homes, fail, version, drift
}

func TestLunaRouteCodexWiring(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			gw := &agentListingGateway{models: []string{model}}
			ds, argv, homes, _, _, _ := codexRoot(t, model, gw)
			ctx := adminCtxForLocalUser(t, ds)
			id, _, err := ds.eng.Trigger(ctx, "lr-codex", nil, "normal")
			require.NoError(t, err)
			inst := waitTerminal(t, ds, id)
			if inst.State != instance.StateCompleted {
				t.Fatalf("run ended %s: %s", inst.State, nodeError(t, ds, id))
			}
			assert.Equal(t, map[string]string{"lr-codex-agent": model}, inst.ModelPins)
			assert.Equal(t, map[string]any{"response": "codex root ok"}, successfulNodeOutput(t, ds, id, "work"))
			require.Len(t, readArgvLog(t, argv), 1)
			assert.NotContains(t, readArgvLog(t, argv)[0], lrRootKey)
			cwd, err := os.ReadFile(argv + ".cwd")
			require.NoError(t, err)
			assert.Equal(t, inst.Variables["workspace"], strings.TrimSpace(string(cwd)))
			paths := readArgvLog(t, homes)
			require.Len(t, paths, 1)
			assert.Equal(t, paths, readArgvLog(t, homes+".version"), "version and inference share only the private home")
			_, err = os.Stat(paths[0])
			assert.True(t, os.IsNotExist(err))
			var raw string
			require.NoError(t, ds.db.QueryRow(`SELECT llm_conversation FROM node_executions WHERE instance_id=? AND node_id='work'`, id.String()).Scan(&raw))
			var trace struct {
				Evidence []engine.ModelEvidence `json:"model_evidence"`
			}
			require.NoError(t, json.Unmarshal([]byte(raw), &trace))
			assert.Equal(t, []engine.ModelEvidence{{Model: model, Provider: "lunaroute", Source: "codex_turn_context", Kind: engine.EvidenceRoutingIntent}}, trace.Evidence)
			assert.Empty(t, gw.seen())

			call := lunaRouteToolCaller(t, ds)
			bad, text := call("connection_create", map[string]any{"name": "or", "provider": "openrouter", "base_url": "https://openrouter.ai/api/v1", "api_key": "sk-or-v1-never-revealed"})
			require.False(t, bad, text)
			bad, text = call("profile_create", map[string]any{"alias": "or-codex", "kind": "agent", "connection_ref": "or", "model": "gpt-6.1-sol", "backend": "codex"})
			require.False(t, bad, text)
			snap, err := ds.pathInventory.Snapshot(ctx)
			require.NoError(t, err)
			seen := map[string]bool{}
			for _, p := range snap.Paths {
				if p.Backend != "codex" {
					continue
				}
				switch p.ConnectionRef {
				case "lr":
					key, err := p.Binding.RevealKey(ctx)
					require.NoError(t, err)
					assert.Equal(t, lrRootKey, key)
					assert.Equal(t, "lunaroute", p.RouteProvider)
					seen["lr"] = true
				case "or":
					key, err := p.Binding.RevealKey(ctx)
					require.NoError(t, err)
					assert.Empty(t, key)
					seen["or"] = true
				}
			}
			assert.True(t, seen["lr"] && seen["or"], "both real inventory paths exist")
		})
	}
}

// paired:3 — Area 3 save <-> Area 4 codex launch. One override file lists
// glm-5.3's codex efforts as [none, low, xhigh, max]. Every effort saved through
// MCP (create and update) must reach the real codex argv as exactly one
// `-c model_reasoning_effort=<saved>`, with review_model and -m naming the saved
// pin; default sends none. The fake child exits non-zero on any other argv, so a
// dropped, duplicated, floored or remapped effort fails the run. none/max are in
// the table but have no exact codex emission, and medium is absent from the
// table: all three must be refused at save, leaving the stored effort intact.
func TestLunaRouteCodexWiringMCPSavedEffortReachesArgv(t *testing.T) {
	const model = "glm-5.3"
	caps := `lunaroute_models:
  glm-5.3:
    efforts:
      codex: {levels: [none, low, xhigh, max], evidence: "root-fixture#paired-3"}
`
	gw := &agentListingGateway{models: []string{model}}
	ds, argv, _, _, _, _ := codexRootWith(t, model, gw, caps, "low")
	call := lunaRouteToolCaller(t, ds)
	stored := func() string {
		var effort, m string
		require.NoError(t, ds.db.QueryRow(`SELECT effort, model FROM profiles WHERE alias='lr-codex-agent'`).Scan(&effort, &m))
		require.Equal(t, model, m)
		return effort
	}
	runs := 0
	runExpecting := func(effort string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(argv), "EFFORT"), []byte(effort), 0o600))
		id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-codex", nil, "normal")
		require.NoError(t, err)
		inst := waitTerminal(t, ds, id)
		if inst.State != instance.StateCompleted {
			t.Fatalf("effort %q: run ended %s: %s", effort, inst.State, nodeError(t, ds, id))
		}
		assert.Equal(t, map[string]string{"lr-codex-agent": model}, inst.ModelPins)
		runs++
		lines := readArgvLog(t, argv)
		require.Len(t, lines, runs)
		got := " " + lines[runs-1] + " "
		assert.Contains(t, got, ` -c review_model="`+model+`" `)
		assert.Contains(t, got, " -m "+model+" ")
		if effort == "" {
			assert.NotContains(t, got, "model_reasoning_effort")
		} else {
			assert.Equal(t, 1, strings.Count(got, "model_reasoning_effort"))
			assert.Contains(t, got, " -c model_reasoning_effort="+effort+" ")
		}
	}

	require.Equal(t, "low", stored())
	runExpecting("low")

	bad, text := call("profile_update", map[string]any{"alias": "lr-codex-agent", "effort": "xhigh"})
	require.False(t, bad, text)
	require.Equal(t, "xhigh", stored())
	runExpecting("xhigh")

	for _, tc := range []struct{ effort, why string }{{"none", "no exact emission on codex"}, {"max", "no exact emission on codex"}, {"medium", "not known to be supported"}} {
		bad, text = call("profile_update", map[string]any{"alias": "lr-codex-agent", "effort": tc.effort})
		require.True(t, bad, "effort %s must be refused at save", tc.effort)
		assert.Contains(t, text, tc.why)
		require.Equal(t, "xhigh", stored(), "a refused update leaves the saved effort intact")
	}
	runExpecting("xhigh")

	bad, text = call("profile_update", map[string]any{"alias": "lr-codex-agent", "effort": "default"})
	require.False(t, bad, text)
	runExpecting("")
	assert.Empty(t, gw.seen(), "no diagnosis probe on successful runs")
}

func TestLunaRouteCodexWiringOpaqueFailure(t *testing.T) {
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds, _, _, fail, _, _ := codexRoot(t, "glm-5.3", gw)
	require.NoError(t, os.WriteFile(fail, nil, 0o600))
	id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-codex", nil, "normal")
	require.NoError(t, err)
	inst := waitTerminal(t, ds, id)
	require.Equal(t, instance.StateFailed, inst.State)
	marker, err := os.ReadFile(filepath.Join(inst.Variables["workspace"].(string), "failure-reached"))
	require.NoError(t, err)
	assert.Equal(t, "checked-route", string(marker))
	msg := nodeError(t, ds, id)
	assert.Contains(t, msg, `codex: mission failed (model \"glm-5.3\"): harness process failed`)
	assert.Contains(t, msg, "a current observation, not proof of the original cause")
	assert.Contains(t, msg, "the credential was accepted and the model is listed now, so the cause is unknown")
	assert.NotContains(t, msg, lrRootKey)
	assert.Equal(t, []string{"GET https://gw.lunaroute.com/v1/models auth=ok"}, gw.seen())
	log, err := os.ReadFile(ds.cfg.Logging.File)
	require.NoError(t, err)
	assert.NotContains(t, string(log), lrRootKey)
}

func TestLunaRouteCodexWiringLocalFailureNeverProbes(t *testing.T) {
	for _, reason := range []string{"version", "private-home", "reveal"} {
		t.Run(reason, func(t *testing.T) {
			gw := &agentListingGateway{models: []string{"glm-5.3"}}
			ds, argv, homes, _, version, _ := codexRoot(t, "glm-5.3", gw)
			switch reason {
			case "version":
				require.NoError(t, os.WriteFile(version, nil, 0o600))
			case "private-home":
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-parent"))
			case "reveal":
				_, err := ds.db.Exec(`UPDATE connections SET api_key_cipher=X'00' WHERE name='lr'`)
				require.NoError(t, err)
			}
			id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-codex", nil, "normal")
			require.NoError(t, err)
			inst := waitTerminal(t, ds, id)
			require.Equal(t, instance.StateFailed, inst.State)
			msg := nodeError(t, ds, id)
			assert.NotContains(t, msg, lrRootKey)
			switch reason {
			case "version":
				assert.Contains(t, msg, "version")
				require.Len(t, readArgvLog(t, homes+".version"), 1)
			case "private-home":
				assert.Contains(t, msg, "private home")
			case "reveal":
				assert.Contains(t, msg, "environment")
			}
			_, err = os.Stat(argv)
			assert.True(t, os.IsNotExist(err), "no inference subprocess")
			assert.Empty(t, gw.seen(), "local failures must not diagnose")
		})
	}
}

func TestLunaRouteCodexWiringRejectsRolloutDrift(t *testing.T) {
	gw := &agentListingGateway{models: []string{"glm-5.3"}}
	ds, argv, _, _, _, drift := codexRoot(t, "glm-5.3", gw)
	require.NoError(t, os.WriteFile(drift, nil, 0o600))
	id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-codex", nil, "normal")
	require.NoError(t, err)
	inst := waitTerminal(t, ds, id)
	require.Equal(t, instance.StateFailed, inst.State)
	msg := nodeError(t, ds, id)
	assert.Contains(t, msg, "executed-model evidence rejected")
	// paired:4: the turn completed on the gateway; a rollout that cannot prove
	// the route is a local codex fault and must not trigger a gateway probe.
	assert.Contains(t, msg, "rollout evidence")
	assert.NotContains(t, msg, "current observation")
	assert.Empty(t, gw.seen(), "local rollout failures must not diagnose")
	require.Len(t, readArgvLog(t, argv), 1)
	assert.Equal(t, map[string]string{"lr-codex-agent": "glm-5.3"}, inst.ModelPins)
}
