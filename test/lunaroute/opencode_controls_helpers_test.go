package lunaroute_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
)

const (
	openCodeControlsLimit   = 1 << 20
	openCodeControlsTimeout = 30 * time.Second
)

// debug config uses Config.Service.get and redacts apiKey as "***" in 1.19.5.
// See sources/lr06-effective-controls.md. This projection proves resolved
// controls only; Bearer auth and helper traffic still require captured requests.
type openCodeControlProof struct {
	Model         string `json:"model"`
	ContextTokens int    `json:"context_tokens"`
	OutputTokens  int    `json:"output_tokens"`
}

type openCodeControlConfig struct {
	Model      string   `json:"model"`
	SmallModel string   `json:"small_model"`
	Enabled    []string `json:"enabled_providers"`
	Providers  map[string]struct {
		NPM     string `json:"npm"`
		Options struct {
			BaseURL string `json:"baseURL"`
			APIKey  string `json:"apiKey"`
		} `json:"options"`
		Models map[string]struct {
			Name     string `json:"name"`
			ToolCall bool   `json:"tool_call"`
			Limit    struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		} `json:"models"`
	} `json:"provider"`
}

func decodeOpenCodeControls(raw []byte) (openCodeControlConfig, error) {
	var config openCodeControlConfig
	if len(raw) == 0 || len(raw) > openCodeControlsLimit {
		return config, errAgentEvidence
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if !uniqueJSONFields(d) {
		return config, errAgentEvidence
	}
	var extra any
	if d.Decode(&extra) != io.EOF || json.Unmarshal(raw, &config) != nil {
		return openCodeControlConfig{}, errAgentEvidence
	}
	return config, nil
}

func openCodeControlValues(config openCodeControlConfig, keyForm string) (openCodeControlProof, error) {
	model := strings.TrimPrefix(config.Model, "lunaroute/")
	if config.Model != "lunaroute/"+model || !safeCaptureModel(model) || config.SmallModel != config.Model || len(config.Enabled) != 1 || config.Enabled[0] != "lunaroute" {
		return openCodeControlProof{}, errAgentEvidence
	}
	if _, err := explicitLiveModel(func(string) (string, bool) { return model, true }, "model"); err != nil {
		return openCodeControlProof{}, errAgentEvidence
	}
	p, ok := config.Providers["lunaroute"]
	m, registered := p.Models[model]
	if !ok || !registered || p.NPM != "@ai-sdk/openai-compatible" || p.Options.BaseURL != hostedRoot+"/v1" || p.Options.APIKey != keyForm || m.Name != model || !m.ToolCall || m.Limit.Context <= 0 || m.Limit.Output <= 0 {
		return openCodeControlProof{}, errAgentEvidence
	}
	return openCodeControlProof{Model: model, ContextTokens: m.Limit.Context, OutputTokens: m.Limit.Output}, nil
}

func validateOpenCodeEffectiveControls(emitted, effective []byte) (openCodeControlProof, error) {
	overlay, err := decodeOpenCodeControls(emitted)
	if err != nil {
		return openCodeControlProof{}, errAgentEvidence
	}
	expected, err := openCodeControlValues(overlay, "{env:LUNAROUTE_API_KEY}")
	if err != nil {
		return openCodeControlProof{}, errAgentEvidence
	}
	resolved, err := decodeOpenCodeControls(effective)
	if err != nil {
		return openCodeControlProof{}, errAgentEvidence
	}
	actual, err := openCodeControlValues(resolved, "***")
	if err != nil || actual != expected {
		return openCodeControlProof{}, errAgentEvidence
	}
	return actual, nil
}

// The experiment's composition root decorates the real runner, so debug config
// receives the actual adapter-emitted binary, env and cwd, not os.Environ or a
// rebuilt overlay. Invalid controls refuse before the mission subprocess starts.
type openCodeControlsRunner struct{ next claudecli.CommandRunner }

func (r openCodeControlsRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	if len(spec.Args) == 0 || spec.Args[0] != "run" {
		return r.next.Run(ctx, spec)
	}
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "LUNAROUTE_API_KEY=") {
			if _, err := queryOpenCodeEffectiveControls(ctx, r.next, spec); err != nil {
				return nil, nil, provider.LocalLunaRouteError("effective controls", errAgentEvidence)
			}
			return r.next.Run(ctx, spec)
		}
	}
	return r.next.Run(ctx, spec)
}

func queryOpenCodeEffectiveControls(ctx context.Context, runner claudecli.CommandRunner, spec claudecli.RunSpec) (openCodeControlProof, error) {
	if runner == nil || len(spec.Args) == 0 || spec.Args[0] != "run" {
		return openCodeControlProof{}, errAgentEvidence
	}
	var key, overlay string
	keyCount, overlayCount := 0, 0
	for _, entry := range spec.Env {
		if strings.HasPrefix(entry, "LUNAROUTE_API_KEY=") {
			keyCount++
			key = strings.TrimPrefix(entry, "LUNAROUTE_API_KEY=")
		}
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			overlayCount++
			overlay = strings.TrimPrefix(entry, "OPENCODE_CONFIG_CONTENT=")
		}
	}
	refuse := func() (openCodeControlProof, error) {
		return openCodeControlProof{}, errAgentEvidence
	}
	if keyCount != 1 || overlayCount != 1 || !provider.ValidLunaRouteKey(key) || spec.Dir == "" {
		return refuse()
	}
	config, err := decodeOpenCodeControls([]byte(overlay))
	if err != nil {
		return refuse()
	}
	if _, err = openCodeControlValues(config, "{env:LUNAROUTE_API_KEY}"); err != nil {
		return refuse()
	}
	pure, models := 0, 0
	for i, arg := range spec.Args {
		if arg == "--pure" {
			pure++
		}
		if arg == "-m" {
			models++
			if i+1 >= len(spec.Args) || spec.Args[i+1] != config.Model {
				return refuse()
			}
		}
	}
	if pure != 1 || models != 1 {
		return refuse()
	}
	query := spec
	query.Args = []string{"debug", "config", "--pure"}
	query.Stdin = ""
	query.MaxResponseBytes = openCodeControlsLimit
	queryCtx, cancel := context.WithTimeout(ctx, openCodeControlsTimeout)
	defer cancel()
	lines, err := runCapturedLines(queryCtx, runner, query)
	if err != nil {
		return refuse()
	}
	return validateOpenCodeEffectiveControls([]byte(overlay), []byte(strings.Join(lines, "\n")))
}
