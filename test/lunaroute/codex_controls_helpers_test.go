package lunaroute_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/modelidentity"
)

const codexControlsSource = "docs/evidence/lunaroute-provider/sources/lr07-config-controls.md"

// App-server configuration is not evidence of exec-mode authentication,
// effective mission controls, helper routing or completed tool work.
type codexConfigControlProof struct {
	Source                   string   `json:"source"`
	Scope                    string   `json:"scope"`
	Version                  string   `json:"version"`
	Model                    string   `json:"model"`
	ReviewModel              string   `json:"review_model"`
	Provider                 string   `json:"provider"`
	BaseURL                  string   `json:"base_url"`
	WireAPI                  string   `json:"wire_api"`
	EnvKey                   string   `json:"env_key"`
	InputCases               []string `json:"input_cases"`
	ProtectedInputsUnchanged bool     `json:"protected_inputs_unchanged"`
	PrivateCleaned           bool     `json:"private_cleaned"`
}

func codexControlSelection(model string) bool {
	_, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{RouteProvider: "lunaroute", Model: model})
	return err == nil
}

func parseCodexConfigControls(raw []byte, selected, version, home string) (codexConfigControlProof, error) {
	fail := func() (codexConfigControlProof, error) { return codexConfigControlProof{}, errBackendEvidence }
	if !codexControlSelection(selected) || !backendHelpVersion.MatchString(version) || !strings.HasPrefix(version, "codex-cli ") || home == "" {
		return fail()
	}
	initialized, configured := false, false
	var proof codexConfigControlProof
	err := backendJSONLines(raw, func(line []byte) error {
		var response struct {
			ID     *int
			Method string
			Error  json.RawMessage
			Result struct {
				UserAgent string `json:"userAgent"`
				Home      string `json:"codexHome"`
				Config    struct {
					Model       string
					ReviewModel string `json:"review_model"`
					Provider    string `json:"model_provider"`
					Providers   map[string]struct {
						Name         string
						BaseURL      string `json:"base_url"`
						EnvKey       string `json:"env_key"`
						WireAPI      string `json:"wire_api"`
						RequiresAuth *bool  `json:"requires_openai_auth"`
						Bearer       any    `json:"experimental_bearer_token"`
						Auth         any
						OAuth        any `json:"gateway_oauth"`
						Headers      any `json:"http_headers"`
						EnvHeaders   any `json:"env_http_headers"`
					} `json:"model_providers"`
				}
				Origins map[string]struct {
					Name    struct{ Type string }
					Version string
				}
			}
		}
		if json.Unmarshal(line, &response) != nil || len(response.Error) > 0 {
			return errBackendEvidence
		}
		if response.ID == nil {
			if !initialized || response.Method == "" {
				return errBackendEvidence
			}
			return nil // Unrelated notifications stay private, never enter the projection.
		}
		switch *response.ID {
		case 1:
			if initialized || configured || response.Result.Home != home || !strings.HasPrefix(response.Result.UserAgent, "lunaroute_controls/"+strings.TrimPrefix(version, "codex-cli ")+" ") {
				return errBackendEvidence
			}
			initialized = true
		case 2:
			c := response.Result.Config
			p, ok := c.Providers["lunaroute"]
			if !initialized || configured || !ok || len(c.Providers) != 1 || c.Model != selected || c.ReviewModel != selected || c.Provider != "lunaroute" || p.Name != "LunaRoute" || p.BaseURL != hostedRoot+"/v1" || p.EnvKey != "LUNAROUTE_API_KEY" || p.WireAPI != "responses" || p.RequiresAuth == nil || *p.RequiresAuth || p.Bearer != nil || p.Auth != nil || p.OAuth != nil || p.Headers != nil || p.EnvHeaders != nil {
				return errBackendEvidence
			}
			for _, key := range []string{"model", "review_model", "model_provider", "model_providers.lunaroute.name", "model_providers.lunaroute.base_url", "model_providers.lunaroute.env_key", "model_providers.lunaroute.wire_api"} {
				origin, ok := response.Result.Origins[key]
				if !ok || origin.Name.Type != "sessionFlags" || origin.Version == "" {
					return errBackendEvidence
				}
			}
			configured = true
			proof = codexConfigControlProof{Source: codexControlsSource, Scope: "app_server_resolved_config_only", Version: version, Model: c.Model, ReviewModel: c.ReviewModel, Provider: c.Provider, BaseURL: p.BaseURL, WireAPI: p.WireAPI, EnvKey: p.EnvKey}
		default:
			return errBackendEvidence
		}
		return nil
	})
	if err != nil || !initialized || !configured {
		return fail()
	}
	return proof, nil
}

func codexControlArgs(model string) []string {
	args := []string{"app-server", "--stdio"}
	for _, s := range []string{`model="` + model + `"`, `model_provider="lunaroute"`, `model_providers.lunaroute.name="LunaRoute"`, `model_providers.lunaroute.base_url="` + hostedRoot + `/v1"`, `model_providers.lunaroute.env_key="LUNAROUTE_API_KEY"`, `model_providers.lunaroute.wire_api="responses"`, `review_model="` + model + `"`} {
		args = append(args, "-c", s)
	}
	return args
}

// App-server shuts down immediately at stdin EOF. Keep the pipe open through
// initialization and config/read, then close it and require successful exit.
func queryCodexControlConfig(ctx context.Context, bin, model string, env []string, cwd string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, agentVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, bin, codexControlArgs(model)...)
	cmd.Env = env
	cmd.Dir = cwd
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errBackendEvidence
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errBackendEvidence
	}
	if cmd.Start() != nil {
		return nil, errBackendEvidence
	}
	stopClose := context.AfterFunc(bounded, func() { _ = stdout.Close() })
	defer stopClose()
	encode := json.NewEncoder(stdin)
	sendErr := encode.Encode(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "lunaroute_controls", "version": "1.0"}}})
	if sendErr != nil {
		cancel()
	}
	var raw bytes.Buffer
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), backendOutputLimit)
	initialized, configured := false, false
	for scanner.Scan() {
		line := scanner.Bytes()
		if raw.Len()+len(line)+1 > backendOutputLimit {
			sendErr = errBackendEvidence
			cancel()
			break
		}
		raw.Write(line)
		raw.WriteByte('\n')
		var message struct{ ID *int }
		if !uniqueJSONFields(json.NewDecoder(bytes.NewReader(line))) || json.Unmarshal(line, &message) != nil {
			sendErr = errBackendEvidence
			cancel()
			break
		}
		if message.ID != nil && *message.ID == 1 && !initialized {
			initialized = true
			if encode.Encode(map[string]any{"method": "initialized"}) != nil || encode.Encode(map[string]any{"id": 2, "method": "config/read", "params": map[string]any{"includeLayers": false, "cwd": cwd}}) != nil {
				sendErr = errBackendEvidence
				cancel()
				break
			}
		}
		if message.ID != nil && *message.ID == 2 && !configured {
			configured = true
			if stdin.Close() != nil {
				sendErr = errBackendEvidence
			}
		}
	}
	if scanner.Err() != nil {
		sendErr = errBackendEvidence
		cancel()
	}
	waitErr := cmd.Wait()
	if sendErr != nil || waitErr != nil || !initialized || !configured || bounded.Err() != nil {
		return nil, errBackendEvidence
	}
	return raw.Bytes(), nil
}

func collectCodexConfigControls(ctx context.Context, bin, model, parent string) (codexConfigControlProof, error) {
	var proof codexConfigControlProof
	for _, conflicting := range []bool{true, false} {
		next, err := collectCodexConfigControlCase(ctx, bin, model, parent, conflicting)
		if err != nil {
			return codexConfigControlProof{}, errBackendEvidence
		}
		if conflicting {
			proof = next
		} else if !reflect.DeepEqual(proof, next) {
			return codexConfigControlProof{}, errBackendEvidence
		}
	}
	proof.InputCases = []string{"conflicting", "absent"}
	return proof, nil
}

func collectCodexConfigControlCase(ctx context.Context, bin, model, parent string, conflicting bool) (codexConfigControlProof, error) {
	if !filepath.IsAbs(bin) || os.Getenv("PATH") == "" || !codexControlSelection(model) {
		return codexConfigControlProof{}, errBackendEvidence
	}
	root, err := os.MkdirTemp(parent, "codex-controls-")
	if err != nil {
		return codexConfigControlProof{}, errBackendEvidence
	}
	collect := func() (codexConfigControlProof, error) {
		env := []string{"PATH=" + os.Getenv("PATH")}
		for _, entry := range []struct{ key, name string }{{"HOME", "home"}, {"CODEX_HOME", "agent"}, {"TMPDIR", "tmp"}, {"XDG_CONFIG_HOME", "config"}, {"XDG_CACHE_HOME", "cache"}, {"XDG_DATA_HOME", "data"}} {
			p := filepath.Join(root, entry.name)
			if os.Mkdir(p, 0700) != nil {
				return codexConfigControlProof{}, errBackendEvidence
			}
			env = append(env, entry.key+"="+p)
		}
		home, ws, agent := filepath.Join(root, "home"), filepath.Join(root, "workspace"), filepath.Join(root, "agent")
		if os.Mkdir(ws, 0700) != nil {
			return codexConfigControlProof{}, errBackendEvidence
		}
		for name, body := range map[string]string{"home/.codex/auth.json": `{"OPENAI_API_KEY":"conflicting-subscription-secret"}`, "home/.codex/config.toml": "model_provider = \"openai\"\nmodel = \"foreign-model\"\n", "workspace/.codex/config.toml": "model_provider = \"openai\"\nmodel = \"foreign-project-model\"\n"} {
			path := filepath.Join(root, name)
			if os.MkdirAll(filepath.Dir(path), 0700) != nil {
				return codexConfigControlProof{}, errBackendEvidence
			}
			if conflicting && os.WriteFile(path, []byte(body), 0600) != nil {
				return codexConfigControlProof{}, errBackendEvidence
			}
		}
		targets := []isolationPath{{home, ".codex"}, {ws, ".codex"}}
		privateInputsAbsent := func() bool {
			for _, name := range []string{"auth.json", "config.toml"} {
				if _, err := os.Lstat(filepath.Join(agent, name)); !os.IsNotExist(err) {
					return false
				}
			}
			return true
		}
		if !privateInputsAbsent() {
			return codexConfigControlProof{}, errBackendEvidence
		}
		before, err := snapshotIsolationPaths(home, targets)
		if err != nil {
			return codexConfigControlProof{}, errBackendEvidence
		}
		versionCtx, cancel := context.WithTimeout(ctx, agentVersionTimeout)
		defer cancel()
		lines, err := runCapturedLines(versionCtx, claudecli.NewExecRunner(), claudecli.RunSpec{Bin: bin, Args: []string{"--version"}, Env: env, Dir: ws, MaxResponseBytes: agentVersionLimit})
		if err != nil {
			return codexConfigControlProof{}, errBackendEvidence
		}
		version := strings.TrimSpace(strings.Join(lines, "\n"))
		raw, err := queryCodexControlConfig(ctx, bin, model, env, ws)
		if err != nil {
			return codexConfigControlProof{}, errBackendEvidence
		}
		// initialize reports canonical CODEX_HOME on macOS temporary-directory links.
		canonical, err := filepath.EvalSymlinks(agent)
		if err != nil {
			return codexConfigControlProof{}, errBackendEvidence
		}
		proof, err := parseCodexConfigControls(raw, model, version, canonical)
		if err != nil {
			return codexConfigControlProof{}, errBackendEvidence
		}
		after, err := snapshotIsolationPaths(home, targets)
		if err != nil || !reflect.DeepEqual(before, after) || !privateInputsAbsent() {
			return codexConfigControlProof{}, errBackendEvidence
		}
		proof.ProtectedInputsUnchanged = true
		return proof, nil
	}
	proof, collectErr := collect()
	if err := os.RemoveAll(root); err != nil || collectErr != nil {
		return codexConfigControlProof{}, errBackendEvidence
	}
	proof.PrivateCleaned = true
	return proof, nil
}

func publishCodexConfigControls(ctx context.Context, root, bin, model, parent, name string) error {
	if _, err := readRepoFile(root, codexControlsSource); err != nil {
		return errBackendEvidence
	}
	proof, err := collectCodexConfigControls(ctx, bin, model, parent)
	if err != nil {
		return errBackendEvidence
	}
	if _, err = saveObservation(root, "", name, proof); err != nil {
		return errBackendEvidence
	}
	return nil
}
