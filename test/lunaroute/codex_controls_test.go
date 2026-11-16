package lunaroute_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func codexControlsResponse(home string) string {
	origins := map[string]any{}
	for _, k := range []string{"model", "review_model", "model_provider", "model_providers.lunaroute.name", "model_providers.lunaroute.base_url", "model_providers.lunaroute.env_key", "model_providers.lunaroute.wire_api"} {
		origins[k] = map[string]any{"name": map[string]any{"type": "sessionFlags"}, "version": "sha256:fixture"}
	}
	init, _ := json.Marshal(map[string]any{"id": 1, "result": map[string]any{"userAgent": "lunaroute_controls/0.167.1 (fixture)", "codexHome": home}})
	config, _ := json.Marshal(map[string]any{"id": 2, "result": map[string]any{"config": map[string]any{"model": "glm-5.3", "review_model": "glm-5.3", "model_provider": "lunaroute", "model_providers": map[string]any{"lunaroute": map[string]any{"name": "LunaRoute", "base_url": hostedRoot + "/v1", "env_key": "LUNAROUTE_API_KEY", "wire_api": "responses", "requires_openai_auth": false}}}, "origins": origins}})
	return string(init) + "\n" + string(config) + "\n"
}

func TestCodexConfigControlsProjection(t *testing.T) {
	home := "/private/fixture/agent"
	raw := codexControlsResponse(home)
	proof, err := parseCodexConfigControls([]byte(raw), "glm-5.3", "codex-cli 0.167.1", home)
	require.NoError(t, err)
	assert.Equal(t, "glm-5.3", proof.Model)
	assert.Equal(t, "glm-5.3", proof.ReviewModel)
	assert.Equal(t, hostedRoot+"/v1", proof.BaseURL)
	assert.Equal(t, "responses", proof.WireAPI)
	assert.Equal(t, "LUNAROUTE_API_KEY", proof.EnvKey)
	for name, bad := range map[string]string{
		"empty": "", "missing config": strings.Split(raw, "\n")[0],
		"duplicate":         raw + strings.Split(raw, "\n")[1] + "\n",
		"out of order":      strings.Split(raw, "\n")[1] + "\n" + strings.Split(raw, "\n")[0],
		"wrong home":        strings.ReplaceAll(raw, home, "/foreign"),
		"wrong version":     strings.ReplaceAll(raw, "0.167.1", "0.167.0"),
		"review drift":      strings.Replace(raw, `"review_model":"glm-5.3"`, `"review_model":"glm-5.3-flash"`, 1),
		"destination drift": strings.ReplaceAll(raw, "gw.lunaroute.com", "foreign.example"),
		"credential drift":  strings.ReplaceAll(raw, "LUNAROUTE_API_KEY", "OPENAI_API_KEY"),
		"login required":    strings.ReplaceAll(raw, `"requires_openai_auth":false`, `"requires_openai_auth":true`),
		"missing auth flag": strings.ReplaceAll(raw, `"requires_openai_auth":false,`, ""),
		"bearer literal":    strings.ReplaceAll(raw, `"name":"LunaRoute"`, `"name":"LunaRoute","experimental_bearer_token":"lr_private-secret"`),
		"headers":           strings.ReplaceAll(raw, `"name":"LunaRoute"`, `"name":"LunaRoute","http_headers":{"Authorization":"Bearer secret"}`),
		"wrong origin":      strings.ReplaceAll(raw, `"type":"sessionFlags"`, `"type":"user"`),
		"null origins":      strings.ReplaceAll(raw, `"origins":{`, `"origins":null,"discarded":{`),
		"duplicate member":  strings.ReplaceAll(raw, `"wire_api":"responses"`, `"wire_api":"other","wire_api":"responses"`),
		"error response":    `{"id":1,"error":{"message":"lr_private-secret"}}`,
		"malformed":         "{broken", "overflow": strings.Repeat("x", backendOutputLimit+1),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseCodexConfigControls([]byte(bad), "glm-5.3", "codex-cli 0.167.1", home)
			require.ErrorIs(t, err, errBackendEvidence)
			assert.Empty(t, got)
			assert.NotContains(t, err.Error(), "private-secret")
		})
	}
	_, err = parseCodexConfigControls([]byte(raw), "claude-sonnet-5-lr1", "codex-cli 0.167.1", home)
	require.ErrorIs(t, err, errBackendEvidence)
}

func TestCodexConfigControlsExperimentToPrivateSink(t *testing.T) {
	for _, mode := range []string{"success", "missing", "drift", "exit", "overflow", "cancel", "changed-home", "changed-project", "absent-drift", "created-auth", "held-pipe"} {
		t.Run(mode, func(t *testing.T) {
			root, parent := t.TempDir(), t.TempDir()
			source := filepath.Join(root, codexControlsSource)
			require.NoError(t, os.MkdirAll(filepath.Dir(source), 0700))
			require.NoError(t, os.WriteFile(source, []byte("Reviewed config/read source"), 0600))
			exe, err := os.Executable()
			require.NoError(t, err)
			bin, marker := filepath.Join(t.TempDir(), "codex"), filepath.Join(t.TempDir(), "queries")
			script := fmt.Sprintf("#!/bin/sh\nexec /usr/bin/env -u PWD -u SHLVL -u _ %q -test.run=^TestCodexConfigControlsProcessFixture$ -- %q %q \"$@\"\n", exe, mode, marker)
			require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
			for _, key := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_HOME", "HTTPS_PROXY", "NODE_OPTIONS"} {
				t.Setenv(key, "ambient-forbidden")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Cancel only once the fixture reports the stage under test, so
			// process start-up under suite load cannot race the cancellation.
			cancelledAt := make(chan time.Time, 1)
			if token := map[string]string{"cancel": ":config", "held-pipe": ":held"}[mode]; token != "" {
				go func() {
					for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
						if raw, e := os.ReadFile(marker); e == nil && strings.Contains(string(raw), token) {
							break
						}
					}
					cancelledAt <- time.Now()
					cancel()
				}()
			}
			err = publishCodexConfigControls(ctx, root, bin, "glm-5.3", parent, "codex-controls.json")
			if mode == "held-pipe" {
				journal, e := os.ReadFile(marker)
				require.NoError(t, e)
				require.Contains(t, string(journal), ":held", "descendant must hold stdout before cancellation")
				assert.Less(t, time.Since(<-cancelledAt), 1500*time.Millisecond, "cancel must close stdout even if a descendant holds its pipe")
			}
			raw, readErr := os.ReadFile(filepath.Join(root, evidencePath, "observations/codex-controls.json"))
			if mode == "success" {
				require.NoError(t, err)
				require.NoError(t, readErr)
				var sink map[string]any
				require.NoError(t, json.Unmarshal(raw, &sink))
				assert.Equal(t, "glm-5.3", sink["model"])
				assert.Equal(t, "glm-5.3", sink["review_model"])
				assert.Equal(t, hostedRoot+"/v1", sink["base_url"])
				assert.Equal(t, "responses", sink["wire_api"])
				assert.Equal(t, "app_server_resolved_config_only", sink["scope"])
				assert.Equal(t, []any{"conflicting", "absent"}, sink["input_cases"])
				assert.Equal(t, true, sink["private_cleaned"])
				assert.Equal(t, true, sink["protected_inputs_unchanged"])
				assert.Equal(t, codexControlsSource, sink["source"])
				for _, secret := range []string{"lr_", "Bearer", parent, "foreign-model", "ambient-forbidden"} {
					assert.NotContains(t, string(raw), secret)
				}
				journal, e := os.ReadFile(marker)
				require.NoError(t, e)
				assert.Equal(t, "conflicting:version\nconflicting:config\nabsent:version\nabsent:config\n", string(journal))
				info, e := os.Stat(filepath.Join(root, evidencePath, "observations/codex-controls.json"))
				require.NoError(t, e)
				assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
			} else {
				require.ErrorIs(t, err, errBackendEvidence)
				assert.True(t, os.IsNotExist(readErr))
				assert.NotContains(t, err.Error(), "private-secret")
				_, e := os.Stat(marker)
				require.NoError(t, e, "must reach actual process")
			}
			entries, e := os.ReadDir(parent)
			require.NoError(t, e)
			assert.Empty(t, entries)
		})
	}
}

func TestCodexConfigControlsProcessFixture(t *testing.T) {
	sep := -1
	for i, s := range os.Args {
		if s == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		return
	}
	args := os.Args[sep+1:]
	if len(args) < 3 {
		os.Exit(80)
	}
	mode, marker, argv := args[0], args[1], args[2:]
	env := map[string]string{}
	for _, s := range os.Environ() {
		k, v, _ := strings.Cut(s, "=")
		env[k] = v
	}
	allowed := []string{"PATH", "HOME", "CODEX_HOME", "TMPDIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME"}
	if len(env) != len(allowed) {
		os.Exit(81)
	}
	for _, k := range allowed {
		if env[k] == "" || strings.Contains(env[k], "ambient-forbidden") {
			os.Exit(82)
		}
		if k != "PATH" {
			info, e := os.Stat(env[k])
			if e != nil || info.Mode().Perm() != 0700 {
				os.Exit(83)
			}
		}
	}
	ws, _ := os.Getwd()
	root := filepath.Dir(env["CODEX_HOME"])
	expected, _ := filepath.EvalSymlinks(filepath.Join(root, "workspace"))
	if ws != expected {
		os.Exit(84)
	}
	auth := filepath.Join(env["HOME"], ".codex/auth.json")
	inputCase := "absent"
	if _, e := os.Stat(auth); e == nil {
		inputCase = "conflicting"
	}
	for path, want := range map[string]string{
		auth: `{"OPENAI_API_KEY":"conflicting-subscription-secret"}`,
		filepath.Join(env["HOME"], ".codex/config.toml"): "model_provider = \"openai\"\nmodel = \"foreign-model\"\n",
		filepath.Join(ws, ".codex/config.toml"):          "model_provider = \"openai\"\nmodel = \"foreign-project-model\"\n",
	} {
		raw, err := os.ReadFile(path)
		if inputCase == "conflicting" {
			info, statErr := os.Stat(path)
			if err != nil || string(raw) != want || statErr != nil || info.Mode().Perm() != 0600 {
				os.Exit(94)
			}
		} else if !os.IsNotExist(err) {
			os.Exit(95)
		}
	}
	for _, name := range []string{"auth.json", "config.toml"} {
		if _, err := os.Lstat(filepath.Join(env["CODEX_HOME"], name)); !os.IsNotExist(err) {
			os.Exit(96)
		}
	}
	f, e := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		os.Exit(85)
	}
	if len(argv) == 1 && argv[0] == "--version" {
		fmt.Fprintln(f, inputCase+":version")
		f.Close()
		fmt.Println("codex-cli 0.167.1")
		os.Exit(0)
	}
	// The fixture consumes every load-bearing override, not a canned config reply.
	expectedArgs := []string{"app-server", "--stdio"}
	for _, s := range []string{`model="glm-5.3"`, `model_provider="lunaroute"`, `model_providers.lunaroute.name="LunaRoute"`, `model_providers.lunaroute.base_url="https://gw.lunaroute.com/v1"`, `model_providers.lunaroute.env_key="LUNAROUTE_API_KEY"`, `model_providers.lunaroute.wire_api="responses"`, `review_model="glm-5.3"`} {
		expectedArgs = append(expectedArgs, "-c", s)
	}
	if strings.Join(argv, "\x00") != strings.Join(expectedArgs, "\x00") {
		os.Exit(86)
	}
	fmt.Fprintln(f, inputCase+":config")
	f.Close()
	if mode == "held-pipe" {
		child := exec.Command("/bin/sleep", "2")
		child.Stdout = os.Stdout
		if child.Start() != nil {
			os.Exit(93)
		}
		if f, e = os.OpenFile(marker, os.O_APPEND|os.O_WRONLY, 0600); e != nil {
			os.Exit(85)
		}
		fmt.Fprintln(f, inputCase+":held")
		f.Close()
		os.Exit(0)
	}
	if mode == "cancel" {
		time.Sleep(time.Minute)
		os.Exit(87)
	}
	if mode == "exit" {
		fmt.Fprintln(os.Stderr, "lr_private-secret")
		os.Exit(1)
	}
	dec := json.NewDecoder(os.Stdin)
	var msg map[string]any
	if dec.Decode(&msg) != nil || msg["method"] != "initialize" || msg["id"] != float64(1) {
		os.Exit(88)
	}
	initParams, _ := msg["params"].(map[string]any)
	client, _ := initParams["clientInfo"].(map[string]any)
	if client["name"] != "lunaroute_controls" || client["version"] != "1.0" {
		os.Exit(97)
	}
	canonicalHome, e := filepath.EvalSymlinks(env["CODEX_HOME"])
	if e != nil {
		os.Exit(92)
	}
	raw := codexControlsResponse(canonicalHome)
	lines := strings.Split(raw, "\n")
	fmt.Println(lines[0])
	if dec.Decode(&msg) != nil || msg["method"] != "initialized" {
		os.Exit(89)
	}
	if dec.Decode(&msg) != nil || msg["method"] != "config/read" || msg["id"] != float64(2) {
		os.Exit(90)
	}
	params, _ := msg["params"].(map[string]any)
	cwd, _ := filepath.EvalSymlinks(fmt.Sprint(params["cwd"]))
	if cwd != ws || params["includeLayers"] != false {
		os.Exit(91)
	}
	if mode == "missing" {
		os.Exit(0)
	}
	if mode == "overflow" {
		fmt.Print(strings.Repeat("x", backendOutputLimit+1))
		os.Exit(0)
	}
	if mode == "changed-home" {
		os.WriteFile(auth, []byte("changed"), 0600)
	}
	if mode == "changed-project" {
		os.WriteFile(filepath.Join(ws, ".codex/config.toml"), []byte("changed"), 0600)
	}
	if mode == "created-auth" {
		os.WriteFile(filepath.Join(env["CODEX_HOME"], "auth.json"), []byte("{}"), 0600)
	}
	if mode == "drift" || mode == "absent-drift" && inputCase == "absent" {
		lines[1] = strings.ReplaceAll(lines[1], "gw.lunaroute.com", "foreign.example")
	}
	fmt.Println(lines[1])
	os.Exit(0)
}

func TestCodexConfigControlsOperatorPrerequisites(t *testing.T) {
	exe, err := os.Executable()
	require.NoError(t, err)
	for _, release := range []bool{false, true} {
		t.Run(fmt.Sprint(release), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestLiveCodexConfigControls$", "-test.v")
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key != "ARBITRON_LUNAROUTE_E2E_CODEX_MODEL" && key != "ARBITRON_LUNAROUTE_RELEASE_GATE" {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			if release {
				cmd.Env = append(cmd.Env, "ARBITRON_LUNAROUTE_RELEASE_GATE=1")
			}
			out, err := cmd.CombinedOutput()
			assert.NotContains(t, string(out), "no tests to run")
			if release {
				require.Error(t, err)
				assert.Contains(t, string(out), "--- FAIL: TestLiveCodexConfigControls")
			} else {
				require.NoError(t, err)
				assert.Contains(t, string(out), "--- SKIP: TestLiveCodexConfigControls")
			}
		})
	}
}

func TestCodexConfigControlsInstalledSchema(t *testing.T) {
	proof, err := collectCodexConfigControls(context.Background(), liveBinary(t, "codex"), "glm-5.3", t.TempDir())
	require.NoError(t, err, "installed app-server config only; no inference or auth claim")
	assert.Equal(t, []string{"conflicting", "absent"}, proof.InputCases)
	assert.Equal(t, "glm-5.3", proof.Model)
	assert.Equal(t, "glm-5.3", proof.ReviewModel)
	assert.Equal(t, "LUNAROUTE_API_KEY", proof.EnvKey)
	assert.True(t, proof.PrivateCleaned)
}
