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
	// Thinking is the exact --thinking value the child must receive; empty means the flag must be absent.
	Thinking string
	// Output and Reasoning are the models.json values the child must receive;
	// zero Output means piRootOutput (the piRootCaps fixture).
	Output    int
	Reasoning bool
}

func piRoot(t *testing.T, model, caps string) (*domainStack, *agentListingGateway, piRootFixture) {
	return piRootThinking(t, model, caps, "")
}

func piRootThinking(t *testing.T, model, caps, thinking string) (*domainStack, *agentListingGateway, piRootFixture) {
	return piRootWith(t, piRootFixture{Model: model, Thinking: thinking}, caps)
}

func piRootWith(t *testing.T, want piRootFixture, caps string) (*domainStack, *agentListingGateway, piRootFixture) {
	t.Helper()
	model := want.Model
	dir := t.TempDir()
	f := piRootFixture{
		Thinking: want.Thinking, Output: want.Output, Reasoning: want.Reasoning,
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

// paired:1 seam (Area 3 save <-> Area 5 pi launch). One override file sets
// glm-5.3's output to 4096 and its pi efforts; the effort saved through MCP must
// reach the real child's argv exactly (none -> off, never floored or remapped)
// and models.json must carry the same limits/reasoning save validated. The child
// fails the run on any other argv or definition.
func TestLunaRoutePiWiringEffortReachesArgv(t *testing.T) {
	const output = 4096
	caps := fmt.Sprintf(`lunaroute_models:
  glm-5.3:
    context_tokens: {tokens: %d, evidence: "root-fixture#context"}
    output_tokens: {tokens: %d, evidence: "root-fixture#output"}
    reasoning: {support: supported, evidence: "root-fixture#reasoning"}
    efforts:
      pi: {levels: [none, low, high], evidence: "root-fixture#effort"}
`, piRootContext, output)
	for _, tc := range []struct{ effort, thinking string }{{"none", "off"}, {"low", "low"}, {"high", "high"}} {
		t.Run(tc.effort, func(t *testing.T) {
			ds, _, f := piRootWith(t, piRootFixture{Model: "glm-5.3", Thinking: tc.thinking, Output: output, Reasoning: true}, caps)
			bad, text := lunaRouteToolCaller(t, ds)("profile_create", map[string]any{"alias": "lr-pi-agent", "kind": "agent", "connection_ref": "lr", "model": "glm-5.3", "backend": "pi", "effort": tc.effort})
			require.False(t, bad, text)
			assert.Contains(t, text, tc.effort, "save echoes the requested effort")
			id, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "lr-pi", nil, "normal")
			require.NoError(t, err)
			inst := waitTerminal(t, ds, id)
			if inst.State != instance.StateCompleted {
				t.Fatalf("run ended %s: %s", inst.State, nodeError(t, ds, id))
			}
			argv := readArgvLog(t, f.Argv)
			require.Len(t, argv, 1)
			assert.Equal(t, "--print --mode json --no-session --model lunaroute/glm-5.3 --thinking "+tc.thinking, argv[0])
			raw, err := os.ReadFile(f.Capture)
			require.NoError(t, err)
			var def map[string]any
			require.NoError(t, json.Unmarshal(raw, &def))
			assert.Equal(t, piRootDefinition(f), def, "override limits and reasoning reached the subprocess file")
		})
	}
}

// A table whose reasoning fact is unsupported makes pi's definition say
// reasoning:false, and pi then clamps --thinking to off. Save must refuse a
// reasoning effort there instead of letting the run silently drop it; none
// (-> off) stays exact and still saves.
func TestLunaRoutePiWiringReasoningEffortNeedsReasoningModel(t *testing.T) {
	caps := piRootCaps("glm-5.3") + "    efforts:\n      pi: {levels: [none, low], evidence: \"root-fixture#effort\"}\n"
	ds, _, f := piRootThinking(t, "glm-5.3", caps, "off")
	call := lunaRouteToolCaller(t, ds)
	bad, text := call("profile_create", map[string]any{"alias": "lr-pi-agent", "kind": "agent", "connection_ref": "lr", "model": "glm-5.3", "backend": "pi", "effort": "low"})
	require.True(t, bad, text)
	assert.Contains(t, text, "reasoning unsupported")
	assert.Contains(t, text, "root-fixture#reasoning")
	_, err := os.Stat(f.Argv)
	assert.True(t, os.IsNotExist(err), "no launch after a refused save")
	bad, text = call("profile_create", map[string]any{"alias": "lr-pi-agent", "kind": "agent", "connection_ref": "lr", "model": "glm-5.3", "backend": "pi", "effort": "none"})
	require.False(t, bad, text)
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
			assert.Equal(t, piRootDefinition(f), def, "loaded operator facts reached the actual subprocess file")
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
	// paired:5 seam (Area 2 diagnosis <-> Area 5 pi launch): local preparation
	// failures (private dir, version probe) carry the local marker and never
	// probe the gateway; opaque post-launch failures (failed turn with exit 0,
	// drift, non-zero exit) probe exactly once.
	for _, mode := range []string{"failed-turn", "drift", "wrong-definition", "private-dir", "version-old", "version-exit"} {
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
			case "version-old":
				assert.Contains(t, msg, "lunaroute version")
			case "version-exit":
				assert.Contains(t, msg, "lunaroute version")
			}
			local := mode == "private-dir" || strings.HasPrefix(mode, "version-")
			if local {
				assert.NotContains(t, msg, "gateway check after the failure", "a local fault never carries a gateway observation")
			} else {
				assert.Contains(t, msg, "gateway check after the failure", "an opaque failure is diagnosed")
			}
			var n int
			require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM node_executions WHERE instance_id=? AND status='succeeded'`, id.String()).Scan(&n))
			assert.Zero(t, n)
			if local {
				assert.Empty(t, gw.seen(), "local preparation failure never diagnoses")
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

func piRootDefinition(f piRootFixture) map[string]any {
	model, output := f.Model, f.Output
	if output == 0 {
		output = piRootOutput
	}
	return map[string]any{"providers": map[string]any{"lunaroute": map[string]any{
		"baseUrl": "https://gw.lunaroute.com/v1", "api": "openai-completions", "apiKey": "$LUNAROUTE_API_KEY", "authHeader": true,
		"models": []any{map[string]any{"id": model, "name": model, "reasoning": f.Reasoning, "input": []any{"text"}, "contextWindow": float64(piRootContext), "maxTokens": float64(output)}},
	}}}
}

func wantArgs(f piRootFixture) []string {
	args := []string{"--print", "--mode", "json", "--no-session", "--model", "lunaroute/" + f.Model}
	if f.Thinking != "" {
		args = append(args, "--thinking", f.Thinking)
	}
	return args
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
		switch mode, _ := os.ReadFile(f.Mode); string(mode) {
		case "version-old":
			fmt.Println("0.0.1 " + lrRootKey)
			os.Exit(0)
		case "version-exit":
			fmt.Fprintln(os.Stderr, "version failed "+lrRootKey)
			os.Exit(3)
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
		!reflect.DeepEqual(args, wantArgs(f)) {
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
	if json.Unmarshal(raw, &def) != nil || !reflect.DeepEqual(def, piRootDefinition(f)) {
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
