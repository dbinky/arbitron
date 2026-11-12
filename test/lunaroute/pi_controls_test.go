package lunaroute_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	adapterconfig "github.com/arbitron/arbitron/adapter/config"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const piControlSnapshotFixture = `{"models":[{"id":"glm-5.3","context_window":202752,"max_output_tokens":32768,"capabilities":{"reasoning":false}}]}`

func piControlRules() provider.CapabilityRules {
	m := provider.LunaRouteModel{ID: "glm-5.3", ContextTokens: provider.LimitFact{Tokens: 202752, Evidence: "source#context"}, OutputTokens: provider.LimitFact{Tokens: 32768, Evidence: "source#output"}, Reasoning: provider.SupportFact{Support: provider.SupportUnsupported, Evidence: "source#reasoning"}}
	return provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{m.ID: m})
}

func piControlResponse() string {
	return `{"type":"response","id":"model-controls","command":"get_state","success":true,"data":{"model":{"id":"glm-5.3","provider":"lunaroute","api":"openai-completions","baseUrl":"https://gw.lunaroute.com/v1","reasoning":false,"input":["text"],"contextWindow":202752,"maxTokens":32768},"sessionId":"controls-session","isStreaming":false,"isCompacting":false,"messageCount":0,"pendingMessageCount":0}}`
}

func TestPiModelControlsStateProjection(t *testing.T) {
	proof, err := parsePiModelControls([]byte(piControlResponse()), "glm-5.3", piControlRules())
	require.NoError(t, err)
	assert.Equal(t, "glm-5.3", proof.Model)
	assert.Equal(t, 202752, proof.ContextTokens)
	assert.Equal(t, 32768, proof.OutputTokens)
	assert.False(t, proof.Reasoning, "known false is independent from unknown")
	assert.Equal(t, "controls-session", proof.SessionID)
	for name, raw := range map[string]string{
		"missing": "", "duplicate response": piControlResponse() + "\n" + piControlResponse(),
		"wrong id":            strings.ReplaceAll(piControlResponse(), "model-controls", "other"),
		"wrong command":       strings.ReplaceAll(piControlResponse(), "get_state", "set_model"),
		"failed":              strings.ReplaceAll(piControlResponse(), `"success":true`, `"success":false`),
		"model absent":        strings.ReplaceAll(piControlResponse(), `"id":"glm-5.3",`, ""),
		"foreign provider":    strings.ReplaceAll(piControlResponse(), `"provider":"lunaroute"`, `"provider":"openai"`),
		"foreign model":       strings.ReplaceAll(piControlResponse(), "glm-5.3", "glm-5.3-flash"),
		"foreign destination": strings.ReplaceAll(piControlResponse(), "gw.lunaroute.com", "foreign.example"),
		"wrong api":           strings.ReplaceAll(piControlResponse(), "openai-completions", "openai-responses"),
		"context drift":       strings.ReplaceAll(piControlResponse(), "202752", "128000"),
		"output drift":        strings.ReplaceAll(piControlResponse(), "32768", "16384"),
		"reasoning drift":     strings.ReplaceAll(piControlResponse(), `"reasoning":false`, `"reasoning":true`),
		"reasoning missing":   strings.ReplaceAll(piControlResponse(), `"reasoning":false,`, ""),
		"reasoning null":      strings.ReplaceAll(piControlResponse(), `"reasoning":false`, `"reasoning":null`),
		"duplicate key":       strings.ReplaceAll(piControlResponse(), `"reasoning":false`, `"reasoning":true,"reasoning":false`),
		"string limit":        strings.ReplaceAll(piControlResponse(), `"maxTokens":32768`, `"maxTokens":"32768"`),
		"null streaming":      strings.ReplaceAll(piControlResponse(), `"isStreaming":false`, `"isStreaming":null`),
		"active":              strings.ReplaceAll(piControlResponse(), `"isStreaming":false`, `"isStreaming":true`),
		"compacting":          strings.ReplaceAll(piControlResponse(), `"isCompacting":false`, `"isCompacting":true`),
		"messages":            strings.ReplaceAll(piControlResponse(), `"messageCount":0`, `"messageCount":1`),
		"pending":             strings.ReplaceAll(piControlResponse(), `"pendingMessageCount":0`, `"pendingMessageCount":1`),
		"unsafe session":      strings.ReplaceAll(piControlResponse(), "controls-session", "lr_private-secret"),
		"image input":         strings.ReplaceAll(piControlResponse(), `["text"]`, `["text","image"]`),
		"malformed":           "{broken", "oversized": strings.Repeat("x", backendOutputLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parsePiModelControls([]byte(raw), "glm-5.3", piControlRules())
			require.ErrorIs(t, err, errBackendEvidence)
			assert.Empty(t, got)
			assert.NotContains(t, err.Error(), "private-secret")
		})
	}
	for _, rules := range []provider.CapabilityRules{{}, provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": {ID: "glm-5.3"}})} {
		_, err := parsePiModelControls([]byte(piControlResponse()), "glm-5.3", rules)
		require.ErrorIs(t, err, errBackendEvidence)
	}
}

func TestPiModelControlsExperimentToPrivateSink(t *testing.T) {
	for _, mode := range []string{"success", "missing", "drift", "exit", "overflow", "cancel", "changed-home", "changed-project", "absent-drift", "absent-missing", "absent-exit", "absent-created-auth"} {
		t.Run(mode, func(t *testing.T) {
			root, parent := t.TempDir(), t.TempDir()
			for _, entry := range []struct{ path, body string }{{piControlsSource, "Reviewed RPC controls source"}, {piControlsSnapshot, piControlSnapshotFixture}} {
				path := filepath.Join(root, entry.path)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
				require.NoError(t, os.WriteFile(path, []byte(entry.body), 0600))
			}
			bin, err := os.Executable()
			require.NoError(t, err)
			wrapper, marker := filepath.Join(t.TempDir(), "pi"), filepath.Join(t.TempDir(), "queries")
			script := fmt.Sprintf("#!/bin/sh\nexec /usr/bin/env -u PWD -u SHLVL -u _ %q -test.run=^TestPiModelControlsProcessFixture$ -- %q %q \"$@\"\n", bin, mode, marker)
			require.NoError(t, os.WriteFile(wrapper, []byte(script), 0700))
			for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "PI_CODING_AGENT_DIR", "NODE_OPTIONS", "HTTPS_PROXY"} {
				t.Setenv(key, "ambient-forbidden")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				go func() {
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						if _, err := os.Stat(marker); err == nil {
							cancel()
							return
						}
						time.Sleep(time.Millisecond)
					}
					cancel()
				}()
			}
			err = publishPiModelControlExperiment(ctx, root, wrapper, "glm-5.3", piControlRules(), parent, "pi-controls.json")
			raw, readErr := os.ReadFile(filepath.Join(root, evidencePath, "observations/pi-controls.json"))
			if mode == "success" {
				require.NoError(t, err)
				require.NoError(t, readErr)
				var fields map[string]any
				require.NoError(t, json.Unmarshal(raw, &fields))
				assert.Equal(t, "glm-5.3", fields["model"])
				assert.Equal(t, "lunaroute", fields["provider"])
				assert.Equal(t, "openai-completions", fields["api"])
				assert.Equal(t, hostedRoot+"/v1", fields["base_url"])
				assert.Equal(t, float64(202752), fields["context_tokens"])
				assert.Equal(t, float64(32768), fields["output_tokens"])
				assert.Equal(t, false, fields["reasoning"])
				assert.Equal(t, "rpc_resolved_model_only", fields["scope"])
				assert.Equal(t, true, fields["protected_inputs_unchanged"])
				assert.Equal(t, true, fields["private_cleaned"])
				assert.Equal(t, map[string]any{"conflicting": "controls-conflicting", "absent": "controls-absent"}, fields["auth_case_sessions"])
				assert.Equal(t, piControlsSource, fields["source"])
				assert.Equal(t, piControlsSnapshot, fields["metadata_source"])
				for _, secret := range []string{"lr_", "Bearer", "ambient-forbidden", "foreign-model", parent, "models.json"} {
					assert.False(t, strings.Contains(string(raw), secret), "unsafe or private data reached observation")
				}
				queries, err := os.ReadFile(marker)
				require.NoError(t, err)
				assert.Equal(t, "conflicting:version\nconflicting:state\nabsent:version\nabsent:state\n", string(queries))
				roots, err := os.ReadFile(marker + "-roots")
				require.NoError(t, err)
				paths := strings.Split(strings.TrimSpace(string(roots)), "\n")
				require.Len(t, paths, 2)
				assert.NotEqual(t, paths[0], paths[1], "auth cases must use independent private homes")
				info, err := os.Stat(filepath.Join(root, evidencePath, "observations/pi-controls.json"))
				require.NoError(t, err)
				assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
			} else {
				require.ErrorIs(t, err, errBackendEvidence)
				assert.True(t, os.IsNotExist(readErr))
				assert.NotContains(t, err.Error(), "private-secret")
				_, err := os.Stat(marker)
				require.NoError(t, err, "must reach real process")
				if strings.HasPrefix(mode, "absent-") {
					queries, err := os.ReadFile(marker)
					require.NoError(t, err)
					assert.Equal(t, "conflicting:version\nconflicting:state\nabsent:version\nabsent:state\n", string(queries), "must exercise the second auth case before refusing publication")
				}
			}
			entries, err := os.ReadDir(parent)
			require.NoError(t, err)
			assert.Empty(t, entries, "cleanup on every outcome")
		})
	}
}

func TestPiModelControlsSourceRefusalBeforeProcess(t *testing.T) {
	for _, source := range []string{"", `{broken`, strings.ReplaceAll(piControlSnapshotFixture, "202752", "128000"), strings.ReplaceAll(piControlSnapshotFixture, `"reasoning":false`, `"reasoning":null`), strings.ReplaceAll(piControlSnapshotFixture, `"reasoning":false`, `"reasoning":true,"reasoning":false`)} {
		root, parent := t.TempDir(), t.TempDir()
		for _, entry := range []struct{ path, body string }{{piControlsSource, "reviewed"}, {piControlsSnapshot, source}} {
			path := filepath.Join(root, entry.path)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
			require.NoError(t, os.WriteFile(path, []byte(entry.body), 0600))
		}
		err := publishPiModelControlExperiment(context.Background(), root, "/missing/never-launched", "glm-5.3", piControlRules(), parent, "refused.json")
		require.ErrorIs(t, err, errBackendEvidence)
		_, err = os.Stat(filepath.Join(root, evidencePath, "observations/refused.json"))
		assert.True(t, os.IsNotExist(err))
		entries, err := os.ReadDir(parent)
		require.NoError(t, err)
		assert.Empty(t, entries)
	}
}

func TestPiModelControlsInstalledSchema(t *testing.T) {
	bin := liveBinary(t, "pi")
	rules, err := adapterconfig.LoadCapabilityRules("")
	require.NoError(t, err)
	source, err := os.ReadFile(filepath.Join(repoRoot(t), piControlsSnapshot))
	require.NoError(t, err)
	for _, model := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			parent := t.TempDir()
			proof, err := collectPiModelControls(context.Background(), bin, model, rules, source, parent)
			require.NoError(t, err, "installed RPC schema and private model resolution only; no inference")
			facts, ok := rules.LunaRouteModel(model)
			require.True(t, ok)
			assert.Equal(t, model, proof.Model)
			assert.Equal(t, facts.ContextTokens.Tokens, proof.ContextTokens)
			assert.Equal(t, facts.OutputTokens.Tokens, proof.OutputTokens)
			assert.True(t, proof.Reasoning)
			require.Len(t, proof.AuthCaseSessions, 2)
			assert.NotEmpty(t, proof.AuthCaseSessions["absent"])
			assert.NotEmpty(t, proof.AuthCaseSessions["conflicting"])
			assert.NotEqual(t, proof.AuthCaseSessions["absent"], proof.AuthCaseSessions["conflicting"])
			entries, err := os.ReadDir(parent)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestPiModelControlsOperatorEntryPrerequisites(t *testing.T) {
	bin, err := os.Executable()
	require.NoError(t, err)
	for _, release := range []bool{false, true} {
		t.Run(fmt.Sprint(release), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "-test.run=^TestLivePiModelControls$", "-test.v")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "ARBITRON_LUNAROUTE_E2E_PI_MODEL" && key != "ARBITRON_LUNAROUTE_RELEASE_GATE" {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			if release {
				cmd.Env = append(cmd.Env, "ARBITRON_LUNAROUTE_RELEASE_GATE=1")
			}
			raw, err := cmd.CombinedOutput()
			if release {
				require.Error(t, err)
				assert.Contains(t, string(raw), "--- FAIL: TestLivePiModelControls")
			} else {
				require.NoError(t, err)
				assert.Contains(t, string(raw), "--- SKIP: TestLivePiModelControls")
			}
			assert.Contains(t, string(raw), "release acceptance remains pending")
		})
	}
}

func TestPiModelControlsProcessFixture(t *testing.T) {
	idx := slices.Index(os.Args, "--")
	if idx < 0 {
		return
	}
	args := os.Args[idx+1:]
	if len(args) < 3 {
		os.Exit(2)
	}
	mode, marker, query := args[0], args[1], args[2:]
	allowed := []string{"PATH", "HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "PI_CODING_AGENT_DIR", "PI_OFFLINE", "LUNAROUTE_API_KEY"}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(allowed, k) || strings.Contains(kv, "ambient-forbidden") {
			os.Exit(3)
		}
	}
	cwd, err := os.Getwd()
	if err != nil || filepath.Base(cwd) != "workspace" || os.Getenv("LUNAROUTE_API_KEY") != piControlsKey || os.Getenv("PI_OFFLINE") != "1" {
		os.Exit(4)
	}
	for _, key := range []string{"HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "PI_CODING_AGENT_DIR"} {
		p := os.Getenv(key)
		info, err := os.Stat(p)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			os.Exit(5)
		}
		actual, err := filepath.EvalSymlinks(p)
		if err != nil || filepath.Dir(actual) != filepath.Dir(cwd) {
			os.Exit(6)
		}
	}
	authCase := "absent"
	for i, entry := range []struct{ path, body string }{
		{filepath.Join(os.Getenv("HOME"), ".pi/agent/settings.json"), `{"defaultProvider":"openai","defaultModel":"foreign-model"}`},
		{filepath.Join(os.Getenv("HOME"), ".pi/agent/auth.json"), `{"openai":{"type":"api_key","key":"conflicting-subscription-secret"}}`},
		{filepath.Join(cwd, ".pi/settings.json"), `{"defaultProvider":"openai","defaultModel":"foreign-project-model"}`},
	} {
		raw, err := os.ReadFile(entry.path)
		if i == 0 && err == nil {
			authCase = "conflicting"
		}
		if (authCase == "absent" && !os.IsNotExist(err)) || (authCase == "conflicting" && (err != nil || string(raw) != entry.body)) {
			os.Exit(21)
		}
	}
	if _, err := os.Lstat(filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "auth.json")); !os.IsNotExist(err) {
		os.Exit(22)
	}
	path := filepath.Join(os.Getenv("PI_CODING_AGENT_DIR"), "models.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		os.Exit(7)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		os.Exit(8)
	}
	var config struct {
		Providers map[string]struct {
			BaseURL    string           `json:"baseUrl"`
			API        string           `json:"api"`
			APIKey     string           `json:"apiKey"`
			AuthHeader bool             `json:"authHeader"`
			Models     []map[string]any `json:"models"`
		}
	}
	if json.Unmarshal(raw, &config) != nil || len(config.Providers) != 1 {
		os.Exit(9)
	}
	p := config.Providers["lunaroute"]
	if p.BaseURL != "https://gw.lunaroute.com/v1" || p.API != "openai-completions" || p.APIKey != "$LUNAROUTE_API_KEY" || !p.AuthHeader || len(p.Models) != 1 {
		os.Exit(10)
	}
	if p.Models[0]["contextWindow"] != float64(202752) || p.Models[0]["maxTokens"] != float64(32768) || p.Models[0]["reasoning"] != false {
		os.Exit(11)
	}
	label := "state"
	if slices.Equal(query, []string{"--version"}) {
		label = "version"
	} else if !slices.Equal(query, []string{"--mode", "rpc", "--no-session", "--model", "lunaroute/glm-5.3"}) {
		os.Exit(12)
	}
	f, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		os.Exit(13)
	}
	if _, err = fmt.Fprintln(f, authCase+":"+label); err != nil || f.Close() != nil {
		os.Exit(14)
	}
	if label == "version" {
		f, err := os.OpenFile(marker+"-roots", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(23)
		}
		if _, err = fmt.Fprintln(f, os.Getenv("HOME")); err != nil || f.Close() != nil {
			os.Exit(24)
		}
	}
	if mode == "cancel" {
		time.Sleep(10 * time.Second)
		os.Exit(15)
	}
	if label == "version" {
		fmt.Println("1.5.2")
		os.Exit(0)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil || string(input) != "{\"id\":\"model-controls\",\"type\":\"get_state\"}\n" {
		os.Exit(16)
	}
	if authCase == "absent" {
		switch mode {
		case "absent-exit":
			fmt.Fprintln(os.Stderr, "lr_private-secret Bearer private-secret")
			os.Exit(25)
		case "absent-missing":
			os.Exit(0)
		case "absent-created-auth":
			if os.WriteFile(filepath.Join(os.Getenv("HOME"), ".pi/agent/auth.json"), []byte(`{"openai":{"type":"api_key","key":"private-secret"}}`), 0600) != nil {
				os.Exit(26)
			}
		}
	}
	switch mode {
	case "exit":
		fmt.Fprintln(os.Stderr, "lr_private-secret Bearer private-secret")
		os.Exit(17)
	case "missing":
		os.Exit(0)
	case "overflow":
		fmt.Print(strings.Repeat("x", backendOutputLimit+1))
		os.Exit(0)
	case "changed-home":
		if os.WriteFile(filepath.Join(os.Getenv("HOME"), ".pi/agent/settings.json"), []byte(`{}`), 0600) != nil {
			os.Exit(18)
		}
	case "changed-project":
		if os.WriteFile(filepath.Join(cwd, ".pi/settings.json"), []byte(`{}`), 0600) != nil {
			os.Exit(19)
		}
	}
	model := p.Models[0]
	model["provider"] = "lunaroute"
	model["api"] = p.API
	model["baseUrl"] = p.BaseURL
	if mode == "drift" || (mode == "absent-drift" && authCase == "absent") {
		model["maxTokens"] = 16384
	}
	if json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "response", "id": "model-controls", "command": "get_state", "success": true, "data": map[string]any{"model": model, "sessionId": "controls-" + authCase, "isStreaming": false, "isCompacting": false, "messageCount": 0, "pendingMessageCount": 0}}) != nil {
		os.Exit(20)
	}
	os.Exit(0)
}
