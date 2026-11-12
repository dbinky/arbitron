package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	piControlsSource   = "docs/evidence/lunaroute-provider/sources/lr08-model-controls.md"
	piControlsSnapshot = "docs/evidence/lunaroute-provider/sources/lr02-live-models-2026-11-11.json"
	piControlsVersion  = "1.5.2" // Reviewed experiment schema, not a production version gate.
	piControlsKey      = "lr_private_pi_model_controls"
	piControlsCommand  = "{\"id\":\"model-controls\",\"type\":\"get_state\"}\n"
)

// This is resolved RPC configuration, not gateway execution/authentication or
// evidence that the JSON mission, helpers, or enforced maxima work.
type piModelControlProof struct {
	Source                   string            `json:"source"`
	MetadataSource           string            `json:"metadata_source"`
	Scope                    string            `json:"scope"`
	Version                  string            `json:"version"`
	Model                    string            `json:"model"`
	Provider                 string            `json:"provider"`
	API                      string            `json:"api"`
	BaseURL                  string            `json:"base_url"`
	SessionID                string            `json:"session_id"`
	ContextTokens            int               `json:"context_tokens"`
	OutputTokens             int               `json:"output_tokens"`
	Reasoning                bool              `json:"reasoning"`
	ProtectedInputsUnchanged bool              `json:"protected_inputs_unchanged"`
	PrivateCleaned           bool              `json:"private_cleaned"`
	AuthCaseSessions         map[string]string `json:"auth_case_sessions,omitempty"`
}

func piControlFacts(model string, rules provider.CapabilityRules) (provider.LunaRouteModel, error) {
	facts, ok := rules.LunaRouteModel(model)
	_, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{RouteProvider: "lunaroute", Model: model})
	if err != nil || !ok || !facts.ContextTokens.Known() || !facts.OutputTokens.Known() || facts.Reasoning.Evidence == "" || facts.Reasoning.Support != provider.SupportSupported && facts.Reasoning.Support != provider.SupportUnsupported {
		return provider.LunaRouteModel{}, errBackendEvidence
	}
	return facts, nil
}

func parsePiModelControls(raw []byte, selected string, rules provider.CapabilityRules) (piModelControlProof, error) {
	fail := func() (piModelControlProof, error) { return piModelControlProof{}, errBackendEvidence }
	facts, err := piControlFacts(selected, rules)
	if err != nil {
		return fail()
	}
	var proof piModelControlProof
	records := 0
	err = backendJSONLines(raw, func(line []byte) error {
		records++
		var state struct {
			Type, ID, Command string
			Success           *bool
			Data              struct {
				Model struct {
					ID, Provider, API string
					BaseURL           string `json:"baseUrl"`
					Reasoning         *bool
					Input             []string
					ContextWindow     *int
					MaxTokens         *int
				}
				SessionID                         string
				SessionFile                       *string
				IsStreaming, IsCompacting         *bool
				MessageCount, PendingMessageCount *int
			}
		}
		if json.Unmarshal(line, &state) != nil || state.Type != "response" || state.ID != "model-controls" || state.Command != "get_state" || state.Success == nil || !*state.Success {
			return errBackendEvidence
		}
		d, m := state.Data, state.Data.Model
		if m.ID != selected || m.Provider != "lunaroute" || m.API != "openai-completions" || m.BaseURL != hostedRoot+"/v1" || m.Reasoning == nil || *m.Reasoning != (facts.Reasoning.Support == provider.SupportSupported) || !slices.Equal(m.Input, []string{"text"}) || m.ContextWindow == nil || *m.ContextWindow != facts.ContextTokens.Tokens || m.MaxTokens == nil || *m.MaxTokens != facts.OutputTokens.Tokens {
			return errBackendEvidence
		}
		if !safeAgentField(d.SessionID, piControlsKey) || d.SessionFile != nil || d.IsStreaming == nil || *d.IsStreaming || d.IsCompacting == nil || *d.IsCompacting || d.MessageCount == nil || *d.MessageCount != 0 || d.PendingMessageCount == nil || *d.PendingMessageCount != 0 {
			return errBackendEvidence
		}
		proof = piModelControlProof{Model: m.ID, Provider: m.Provider, API: m.API, BaseURL: m.BaseURL, SessionID: d.SessionID, ContextTokens: *m.ContextWindow, OutputTokens: *m.MaxTokens, Reasoning: *m.Reasoning}
		return nil
	})
	if err != nil || records != 1 {
		return fail()
	}
	return proof, nil
}

type piControlSourceModel struct {
	ID           string
	Context      *int `json:"context_window"`
	Output       *int `json:"max_output_tokens"`
	Capabilities struct{ Reasoning *bool }
}

func validatePiControlSource(raw []byte, model string, rules provider.CapabilityRules) error {
	facts, err := piControlFacts(model, rules)
	if err != nil {
		return errBackendEvidence
	}
	var snapshot struct{ Models []piControlSourceModel }
	if len(raw) == 0 || len(raw) > backendOutputLimit || !uniqueJSONFields(json.NewDecoder(bytes.NewReader(raw))) || json.Unmarshal(raw, &snapshot) != nil {
		return errBackendEvidence
	}
	return matchPiControlSource(snapshot.Models, facts)
}

func matchPiControlSource(models []piControlSourceModel, facts provider.LunaRouteModel) error {
	seen := map[string]bool{}
	found := false
	for _, m := range models {
		if m.ID == "" || seen[m.ID] {
			return errBackendEvidence
		}
		seen[m.ID] = true
		if m.ID != facts.ID {
			continue
		}
		if m.Context == nil || *m.Context != facts.ContextTokens.Tokens || m.Output == nil || *m.Output != facts.OutputTokens.Tokens || m.Capabilities.Reasoning == nil || *m.Capabilities.Reasoning != (facts.Reasoning.Support == provider.SupportSupported) {
			return errBackendEvidence
		}
		found = true
	}
	if !found {
		return errBackendEvidence
	}
	return nil
}

func collectPiModelControls(ctx context.Context, bin, model string, rules provider.CapabilityRules, snapshot []byte, parent string) (piModelControlProof, error) {
	conflicting, err := collectPiModelControlCase(ctx, bin, model, rules, snapshot, parent, true)
	if err != nil {
		return piModelControlProof{}, errBackendEvidence
	}
	absent, err := collectPiModelControlCase(ctx, bin, model, rules, snapshot, parent, false)
	if err != nil || absent.SessionID == conflicting.SessionID {
		return piModelControlProof{}, errBackendEvidence
	}
	conflicting.AuthCaseSessions = map[string]string{"conflicting": conflicting.SessionID, "absent": absent.SessionID}
	return conflicting, nil
}

func collectPiModelControlCase(ctx context.Context, bin, model string, rules provider.CapabilityRules, snapshot []byte, parent string, conflicting bool) (piModelControlProof, error) {
	if !filepath.IsAbs(bin) || os.Getenv("PATH") == "" || validatePiControlSource(snapshot, model, rules) != nil {
		return piModelControlProof{}, errBackendEvidence
	}
	root, err := os.MkdirTemp(parent, "pi-controls-")
	if err != nil {
		return piModelControlProof{}, errBackendEvidence
	}
	collect := func() (piModelControlProof, error) {
		env := []string{"PATH=" + os.Getenv("PATH"), "PI_OFFLINE=1", "LUNAROUTE_API_KEY=" + piControlsKey}
		for _, entry := range []struct{ key, name string }{{"HOME", "home"}, {"TMPDIR", "tmp"}, {"XDG_CONFIG_HOME", "config"}, {"XDG_CACHE_HOME", "cache"}, {"XDG_DATA_HOME", "data"}, {"PI_CODING_AGENT_DIR", "agent"}} {
			path := filepath.Join(root, entry.name)
			if os.Mkdir(path, 0700) != nil {
				return piModelControlProof{}, errBackendEvidence
			}
			env = append(env, entry.key+"="+path)
		}
		home, ws, agent := filepath.Join(root, "home"), filepath.Join(root, "workspace"), filepath.Join(root, "agent")
		if os.Mkdir(ws, 0700) != nil {
			return piModelControlProof{}, errBackendEvidence
		}
		seeds := map[string]string{
			"home/.pi/agent/settings.json": `{"defaultProvider":"openai","defaultModel":"foreign-model"}`,
			"home/.pi/agent/auth.json":     `{"openai":{"type":"api_key","key":"conflicting-subscription-secret"}}`,
			"workspace/.pi/settings.json":  `{"defaultProvider":"openai","defaultModel":"foreign-project-model"}`,
		}
		for name, body := range seeds {
			path := filepath.Join(root, name)
			if os.MkdirAll(filepath.Dir(path), 0700) != nil {
				return piModelControlProof{}, errBackendEvidence
			}
			if conflicting && os.WriteFile(path, []byte(body), 0600) != nil {
				return piModelControlProof{}, errBackendEvidence
			}
		}
		facts, err := piControlFacts(model, rules)
		if err != nil {
			return piModelControlProof{}, errBackendEvidence
		}
		definition := map[string]any{"providers": map[string]any{"lunaroute": map[string]any{
			"baseUrl": hostedRoot + "/v1", "api": "openai-completions", "apiKey": "$LUNAROUTE_API_KEY", "authHeader": true,
			"models": []any{map[string]any{
				"id": model, "name": model, "reasoning": facts.Reasoning.Support == provider.SupportSupported,
				"input": []string{"text"}, "contextWindow": facts.ContextTokens.Tokens, "maxTokens": facts.OutputTokens.Tokens,
			}},
		}}}
		raw, err := json.Marshal(definition)
		if err != nil || os.WriteFile(filepath.Join(agent, "models.json"), raw, 0600) != nil {
			return piModelControlProof{}, errBackendEvidence
		}
		targets := []isolationPath{{home, ".pi"}, {ws, ".pi"}, {agent, "models.json"}}
		before, err := snapshotIsolationPaths(home, targets)
		_, authErr := os.Lstat(filepath.Join(agent, "auth.json"))
		if err != nil || !os.IsNotExist(authErr) {
			return piModelControlProof{}, errBackendEvidence
		}
		query := func(args []string, stdin string, cap int64) ([]byte, error) {
			bounded, cancel := context.WithTimeout(ctx, agentVersionTimeout)
			defer cancel()
			lines, err := runCapturedLines(bounded, claudecli.NewExecRunner(), claudecli.RunSpec{Bin: bin, Args: args, Env: env, Dir: ws, Stdin: stdin, MaxResponseBytes: cap})
			if err != nil {
				return nil, errBackendEvidence
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
		version, err := query([]string{"--version"}, "", agentVersionLimit)
		if err != nil || strings.TrimSpace(string(version)) != piControlsVersion {
			return piModelControlProof{}, errBackendEvidence
		}
		raw, err = query([]string{"--mode", "rpc", "--no-session", "--model", "lunaroute/" + model}, piControlsCommand, backendOutputLimit)
		if err != nil {
			return piModelControlProof{}, errBackendEvidence
		}
		proof, err := parsePiModelControls(raw, model, rules)
		if err != nil {
			return piModelControlProof{}, errBackendEvidence
		}
		after, err := snapshotIsolationPaths(home, targets)
		if err != nil || !reflect.DeepEqual(before, after) {
			return piModelControlProof{}, errBackendEvidence
		}
		proof.Source, proof.MetadataSource, proof.Scope, proof.Version = piControlsSource, piControlsSnapshot, "rpc_resolved_model_only", piControlsVersion
		proof.ProtectedInputsUnchanged = true
		return proof, nil
	}
	proof, collectErr := collect()
	if err := os.RemoveAll(root); err != nil || collectErr != nil {
		return piModelControlProof{}, errBackendEvidence
	}
	proof.PrivateCleaned = true
	return proof, nil
}

func publishPiModelControlExperiment(ctx context.Context, root, bin, model string, rules provider.CapabilityRules, parent, name string) error {
	if _, err := readRepoFile(root, piControlsSource); err != nil {
		return errBackendEvidence
	}
	snapshot, err := readRepoFile(root, piControlsSnapshot)
	if err != nil {
		return errBackendEvidence
	}
	proof, err := collectPiModelControls(ctx, bin, model, rules, snapshot, parent)
	if err != nil {
		return errBackendEvidence
	}
	if _, err = saveObservation(root, piControlsKey, name, proof); err != nil {
		return errBackendEvidence
	}
	return nil
}
