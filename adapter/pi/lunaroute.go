package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// LR-08 defines a private agent directory and an env-referenced Bearer key.
// Live verification is separate from implementing this decided contract.
const (
	LunaRouteKeyVar    = "LUNAROUTE_API_KEY"
	PiAgentDirVar      = "PI_CODING_AGENT_DIR"
	lunaRouteDirPrefix = "arbitron-pi-lunaroute-"
)

type piModelsFile struct {
	Providers map[string]piCustomProvider `json:"providers"`
}
type piCustomProvider struct {
	BaseURL    string          `json:"baseUrl"`
	API        string          `json:"api"`
	APIKey     string          `json:"apiKey"`
	AuthHeader bool            `json:"authHeader"`
	Models     []piCustomModel `json:"models"`
}
type piCustomModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Reasoning     bool     `json:"reasoning"`
	Input         []string `json:"input"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
}

func lunaRouteModelsJSON(b provider.ResolvedBinding, rules provider.LunaRouteCapabilities) ([]byte, error) {
	base, err := provider.LunaRouteOpenAIBaseURL(b.BaseURL)
	if err != nil {
		return nil, err
	}
	model := strings.TrimSpace(b.Model)
	if model == "" {
		return nil, ErrModelRequired
	}
	var m provider.LunaRouteModel
	if rules != nil {
		m, _ = rules.LunaRouteModel(model)
	}
	if !m.ContextTokens.Known() || !m.OutputTokens.Known() {
		return nil, &provider.LunaRouteRefusal{Reason: provider.ErrLunaRouteLimitsUnknown, Model: model, Backend: "pi", Detail: "pi on LunaRoute needs known context and output limits for this model in the capability table (lunaroute_models)"}
	}
	if m.Reasoning.Support != provider.SupportSupported && m.Reasoning.Support != provider.SupportUnsupported {
		return nil, &provider.LunaRouteRefusal{Reason: provider.ErrLunaRouteLimitsUnknown, Model: model, Backend: "pi", Detail: "pi on LunaRoute needs known reasoning metadata from an independent evidence-backed reasoning fact"}
	}
	return json.MarshalIndent(piModelsFile{Providers: map[string]piCustomProvider{"lunaroute": {
		BaseURL: base, API: "openai-completions", APIKey: "$" + LunaRouteKeyVar, AuthHeader: true,
		Models: []piCustomModel{{ID: model, Name: model, Reasoning: m.Reasoning.Support == provider.SupportSupported, Input: []string{"text"}, ContextWindow: m.ContextTokens.Tokens, MaxTokens: m.OutputTokens.Tokens}},
	}}}, "", "  ")
}

type piLaunch struct {
	args, env []string
	dir       string
	cleanup   func()
}

// The private seam permits deterministic write/permission failures without
// process-global filesystem mutations. Production always uses the os functions.
type piFileSystem struct {
	mkdirTemp func(string, string) (string, error)
	chmod     func(string, os.FileMode) error
	writeFile func(string, []byte, os.FileMode) error
	removeAll func(string) error
}

func prepareLunaRoutePiLaunch(ctx context.Context, b provider.ResolvedBinding, rules provider.LunaRouteCapabilities) (piLaunch, error) {
	return prepareLunaRoutePiLaunchWithFS(ctx, b, rules, piFileSystem{os.MkdirTemp, os.Chmod, os.WriteFile, os.RemoveAll})
}
func prepareLunaRoutePiLaunchWithFS(ctx context.Context, b provider.ResolvedBinding, rules provider.LunaRouteCapabilities, fs piFileSystem) (piLaunch, error) {
	local := func(stage string, err error) (piLaunch, error) {
		return piLaunch{}, provider.LocalLunaRouteError(stage, err)
	}
	if err := provider.CheckLunaRouteAdapterLaunch(rules, b, 0); err != nil {
		return local("preflight", err)
	}
	def, err := lunaRouteModelsJSON(b, rules)
	if err != nil {
		return local("definition", err)
	}
	selected := b
	selected.Model = "lunaroute/" + b.Model
	args, err := buildArgs(selected)
	if err != nil {
		return local("arguments", err)
	}
	guarded := provider.GuardLunaRouteReveal(b)
	env, err := procenv.CredentialEnv(ctx, procenv.CredentialSpec{Runner: "pi: lunaroute", KeyVar: LunaRouteKeyVar, Reveal: guarded.RevealKey, EmptyKey: procenv.EmptyKeyReject})
	if err != nil {
		return local("environment", err)
	}
	if err := ctx.Err(); err != nil {
		return local("preparation", err)
	}
	dir, err := fs.mkdirTemp("", lunaRouteDirPrefix)
	if err != nil {
		return local("private agent dir", err)
	}
	cleanup := func() { _ = fs.removeAll(dir) }
	complete := false
	defer func() {
		if !complete {
			cleanup()
		}
	}()
	if err := fs.chmod(dir, 0700); err != nil {
		return local("secure private agent dir", err)
	}
	if err := fs.writeFile(filepath.Join(dir, "models.json"), def, 0600); err != nil {
		return local("write provider definition", err)
	}
	env = procenv.RemoveVar(env, PiAgentDirVar)
	env = append(env, PiAgentDirVar+"="+dir)
	complete = true
	return piLaunch{args: args, env: env, dir: dir, cleanup: cleanup}, nil
}
func (p *Provider) safeProcessErrors() bool {
	return p.binding.Pinned || provider.IsLunaRoute(p.binding.Provider)
}
func (p *Provider) localError(stage string, err error) error {
	if provider.IsLunaRoute(p.binding.Provider) {
		return provider.LocalLunaRouteError(stage, err)
	}
	return err
}
func (p *Provider) ensureLunaRouteCompatible(ctx context.Context, env []string) error {
	p.probeOnce.Do(func() {
		p.version, p.probeErr = probeVersion(ctx, p.runner, p.binPath, procenv.RemoveVar(env, LunaRouteKeyVar))
	})
	return p.localError("version", claudecli.SanitizeProcessError(p.probeErr))
}
