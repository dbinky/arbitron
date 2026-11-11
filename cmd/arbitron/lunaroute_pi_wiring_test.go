package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/pi"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const piRootContext = 202752
const piRootOutput = 32768

func piRootCaps(model string) string {
	return fmt.Sprintf(`lunaroute_models:
  %s:
    context_tokens: {tokens: %d, evidence: "root-fixture#context"}
    output_tokens: {tokens: %d, evidence: "root-fixture#output"}
    reasoning: {support: unsupported, evidence: "root-fixture#reasoning"}
`, model, piRootContext, piRootOutput)
}

// This is an actual subprocess, not a replaced runner/factory. It must decode
// and validate every load-bearing definition field before it emits success.
type piRootFixture struct {
	Model, Capture, Dirs, Argv, Cwd, Mode string
}

func piRoot(t *testing.T, model, caps string) (*domainStack, *agentListingGateway, piRootFixture) {
	t.Helper()
	dir := t.TempDir()
	f := piRootFixture{
		Model: model, Capture: filepath.Join(dir, "models.json"), Dirs: filepath.Join(dir, "dirs"),
		Argv: filepath.Join(dir, "argv"), Cwd: filepath.Join(dir, "cwd"), Mode: filepath.Join(dir, "mode"),
	}
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	config := filepath.Join(dir, "fixture.json")
	require.NoError(t, os.WriteFile(config, raw, 0600))
	exe, err := os.Executable()
	require.NoError(t, err)
	script := "#!/bin/sh\nexport ARBITRON_PI_ROOT_FIXTURE=" + strconv.Quote(config) + "\nexec " + strconv.Quote(exe) + " -test.run='^TestLunaRoutePiRootChild$' -- \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pi"), []byte(script), 0700))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	settings := filepath.Join(home, ".pi", "agent", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0700))
	const conflicting = `{"defaultProvider":"openai","defaultModel":"gpt-conflict"}`
	require.NoError(t, os.WriteFile(settings, []byte(conflicting), 0600))
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-ambient-must-not-reach")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Dir(settings))
	gw := &agentListingGateway{models: []string{model}}
	ds := bootLunaRouteRoot(t, "capabilitiespath: "+lunaRouteCapsFile(t, caps)+"\n", gw, func(ws string) { writeAgentWorkflow(t, ws, "lr-pi", "lr-pi-agent") })
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("connection_create", map[string]any{"name": "lr", "provider": "lunaroute", "api_key": lrRootKey})
	require.False(t, bad, text)
	t.Cleanup(func() {
		raw, err := os.ReadFile(settings)
		require.NoError(t, err)
		assert.Equal(t, conflicting, string(raw), "user settings byte-identical")
		entries, err := os.ReadDir(filepath.Dir(settings))
		require.NoError(t, err)
		require.Len(t, entries, 1, "no shared config/auth writes")
		paths, err := os.ReadFile(f.Dirs)
		if os.IsNotExist(err) {
			return
		}
		require.NoError(t, err)
		for _, path := range strings.Split(strings.TrimSpace(string(paths)), "\n") {
			_, err := os.Stat(path)
			assert.True(t, os.IsNotExist(err), "private agent dir removed: %s", path)
		}
	})
	return ds, gw, f
}

func savePiRoot(t *testing.T, ds *domainStack, model string) (bool, string) {
	t.Helper()
	return lunaRouteToolCaller(t, ds)("profile_create", map[string]any{"alias": "lr-pi-agent", "kind": "agent", "connection_ref": "lr", "model": model, "backend": "pi"})
}

func TestLunaRoutePiWiring(t *testing.T) {
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			caps := piRootCaps(model)
			if model == "glm-5.3" {
				caps += "  deepseek-4.1-flash: {}\n"
			}
			ds, gw, f := piRoot(t, model, caps)
			if model == "glm-5.3" {
				unknown, ok := ds.capabilityRules.LunaRouteModel("deepseek-4.1-flash")
				require.True(t, ok)
				assert.False(t, unknown.ContextTokens.Known())
				assert.False(t, unknown.OutputTokens.Known())
				bad, text := savePiRoot(t, ds, "deepseek-4.1-flash")
				require.True(t, bad)
				assert.Contains(t, text, "pi on LunaRoute needs known context and output limits")
			}
			bad, text := savePiRoot(t, ds, model)
			require.False(t, bad, text)
			id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-pi", nil, "normal")
			require.NoError(t, err)
			inst := waitTerminal(t, ds, id)
			if inst.State != instance.StateCompleted {
				t.Fatalf("run ended %s: %s", inst.State, nodeError(t, ds, id))
			}
			assert.Equal(t, map[string]string{"lr-pi-agent": model}, inst.ModelPins)
			assert.Equal(t, map[string]any{"response": "done"}, successfulNodeOutput(t, ds, id, "work"))
			raw, err := os.ReadFile(f.Capture)
			require.NoError(t, err)
			var def map[string]any
			require.NoError(t, json.Unmarshal(raw, &def))
			assert.Equal(t, piRootDefinition(model), def, "loaded operator facts reached the actual subprocess file")
			assert.NotContains(t, string(raw), lrRootKey)
			require.Len(t, readArgvLog(t, f.Argv), 1)
			assert.NotContains(t, readArgvLog(t, f.Argv)[0], lrRootKey)
			cwd, err := os.ReadFile(f.Cwd)
			require.NoError(t, err)
			assert.Equal(t, inst.Variables["workspace"], string(cwd))
			require.Len(t, readArgvLog(t, f.Dirs), 1)
			var traceRaw string
			require.NoError(t, ds.db.QueryRow(`SELECT llm_conversation FROM node_executions WHERE instance_id=? AND node_id='work'`, id.String()).Scan(&traceRaw))
			var trace struct {
				Evidence []engine.ModelEvidence `json:"model_evidence"`
			}
			require.NoError(t, json.Unmarshal([]byte(traceRaw), &trace))
			assert.Equal(t, []engine.ModelEvidence{{Model: model, Provider: "lunaroute", Source: "pi_message_end"}}, trace.Evidence)
			assert.Empty(t, gw.seen())
		})
	}
}

func TestLunaRoutePiWiringMissingMetadata(t *testing.T) {
	for _, missing := range []string{"empty", "context", "output", "reasoning", "wrong-model"} {
		t.Run(missing, func(t *testing.T) {
			caps := piRootCaps("glm-5.3")
			want := "pi on LunaRoute needs known context and output limits"
			switch missing {
			case "empty":
				caps = "lunaroute_models:\n  glm-5.3: {}\n"
			case "context":
				caps = strings.ReplaceAll(caps, fmt.Sprintf("    context_tokens: {tokens: %d, evidence: \"root-fixture#context\"}\n", piRootContext), "")
			case "output":
				caps = strings.ReplaceAll(caps, fmt.Sprintf("    output_tokens: {tokens: %d, evidence: \"root-fixture#output\"}\n", piRootOutput), "")
			case "reasoning":
				caps = strings.ReplaceAll(caps, "    reasoning: {support: unsupported, evidence: \"root-fixture#reasoning\"}\n", "")
				want = "pi on LunaRoute needs known reasoning metadata"
			case "wrong-model":
				caps = "lunaroute_models:\n  glm-5.3: {}\n  glm-5.3-flash:\n    context_tokens: {tokens: 202752, evidence: fixture}\n    output_tokens: {tokens: 32768, evidence: fixture}\n    reasoning: {support: unsupported, evidence: fixture}\n"
			}
			ds, gw, f := piRoot(t, "glm-5.3", caps)
			m, ok := ds.capabilityRules.LunaRouteModel("glm-5.3")
			require.True(t, ok)
			switch missing {
			case "reasoning":
				assert.Equal(t, provider.SupportUnknown, m.Reasoning.Support)
			case "output":
				assert.False(t, m.OutputTokens.Known())
			default:
				assert.False(t, m.ContextTokens.Known())
			}
			bad, text := savePiRoot(t, ds, "glm-5.3")
			require.True(t, bad)
			assert.Contains(t, text, want)
			var n int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profiles WHERE alias='lr-pi-agent'`).Scan(&n))
			assert.Zero(t, n)
			_, err := os.Stat(f.Argv)
			assert.True(t, os.IsNotExist(err))
			assert.Empty(t, gw.seen(), "metadata refusal never launches or diagnoses")
		})
	}
}

func TestLunaRoutePiWiringFailures(t *testing.T) {
	for _, mode := range []string{"failed-turn", "drift", "wrong-definition", "private-dir"} {
		t.Run(mode, func(t *testing.T) {
			caps := piRootCaps("glm-5.3")
			if mode == "wrong-definition" {
				caps = strings.ReplaceAll(caps, "tokens: 202752", "tokens: 202753")
			}
			ds, gw, f := piRoot(t, "glm-5.3", caps)
			bad, text := savePiRoot(t, ds, "glm-5.3")
			require.False(t, bad, text)
			require.NoError(t, os.WriteFile(f.Mode, []byte(mode), 0600))
			if mode == "private-dir" {
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-parent"))
			}
			id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-pi", nil, "normal")
			require.NoError(t, err)
			inst := waitTerminal(t, ds, id)
			require.Equal(t, instance.StateFailed, inst.State)
			msg := nodeError(t, ds, id)
			assert.NotContains(t, msg, lrRootKey)
			switch mode {
			case "failed-turn":
				assert.Contains(t, msg, pi.ErrTurnFailed.Error())
			case "drift":
				assert.Contains(t, msg, pi.ErrModelEvidence.Error())
			case "wrong-definition":
				assert.Contains(t, msg, "harness process failed")
			case "private-dir":
				assert.Contains(t, msg, "private agent dir")
			}
			var n int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM node_executions WHERE instance_id=? AND status='succeeded'`, id.String()).Scan(&n))
			assert.Zero(t, n)
			if mode == "private-dir" {
				assert.Empty(t, gw.seen(), "local definition preparation failure never diagnoses")
				_, err := os.Stat(f.Argv)
				assert.True(t, os.IsNotExist(err))
			} else {
				require.Len(t, readArgvLog(t, f.Argv), 1)
				assert.Equal(t, []string{"GET https://gw.lunaroute.com/v1/models auth=ok"}, gw.seen())
			}
			log, err := os.ReadFile(ds.cfg.Logging.File)
			require.NoError(t, err)
			assert.NotContains(t, string(log), lrRootKey)
		})
	}
}

func piRootDefinition(model string) map[string]any {
	return map[string]any{"providers": map[string]any{"lunaroute": map[string]any{
		"baseUrl": "https://gw.lunaroute.com/v1", "api": "openai-completions", "apiKey": "$LUNAROUTE_API_KEY", "authHeader": true,
		"models": []any{map[string]any{"id": model, "name": model, "reasoning": false, "input": []any{"text"}, "contextWindow": float64(piRootContext), "maxTokens": float64(piRootOutput)}},
	}}}
}

func TestLunaRoutePiRootChild(t *testing.T) {
	config := os.Getenv("ARBITRON_PI_ROOT_FIXTURE")
	if config == "" {
		return
	}
	fail := func() { os.Exit(41) }
	var f piRootFixture
	raw, err := os.ReadFile(config)
	if err != nil || json.Unmarshal(raw, &f) != nil {
		fail()
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0700 || dir == filepath.Join(os.Getenv("HOME"), ".pi", "agent") {
		fail()
	}
	if reflect.DeepEqual(args, []string{"--version"}) {
		if os.Getenv("LUNAROUTE_API_KEY") != "" {
			fail()
		}
		fmt.Println("1.5.1")
		os.Exit(0)
	}
	// Log the attempted launch, even if a subsequent sink assertion refuses it.
	appendLog := func(path, value string) {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			fail()
		}
		_, err = fmt.Fprintln(file, value)
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			fail()
		}
	}
	appendLog(f.Argv, strings.Join(args, " "))
	appendLog(f.Dirs, dir)
	if os.Getenv("LUNAROUTE_API_KEY") != lrRootKey || os.Getenv("OPENAI_API_KEY") != "" ||
		!reflect.DeepEqual(args, []string{"--print", "--mode", "json", "--no-session", "--model", "lunaroute/" + f.Model}) {
		fail()
	}
	cwd, err := os.Getwd()
	if err != nil || !strings.Contains(cwd, "/instances/") {
		fail()
	}
	if os.WriteFile(f.Cwd, []byte(cwd), 0600) != nil {
		fail()
	}
	input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024))
	if err != nil || string(input) != "hello" {
		fail()
	}
	path := filepath.Join(dir, "models.json")
	st, err = os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		fail()
	}
	raw, err = os.ReadFile(path)
	if err != nil || strings.Contains(string(raw), lrRootKey) {
		fail()
	}
	var def map[string]any
	if json.Unmarshal(raw, &def) != nil || !reflect.DeepEqual(def, piRootDefinition(f.Model)) {
		fail()
	}
	if os.WriteFile(f.Capture, raw, 0600) != nil {
		fail()
	}
	mode, err := os.ReadFile(f.Mode)
	if err != nil && !os.IsNotExist(err) {
		fail()
	}
	model, stop := f.Model, "stop"
	if string(mode) == "drift" {
		model = "glm-999.9"
	}
	if string(mode) == "failed-turn" {
		stop = "error"
	}
	fmt.Printf("{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"provider\":\"lunaroute\",\"model\":%q,\"stopReason\":%q,\"content\":[{\"type\":\"text\",\"text\":\"done\"}],\"usage\":{\"input\":1,\"output\":1}}}\n", model, stop)
	os.Exit(0)
}
