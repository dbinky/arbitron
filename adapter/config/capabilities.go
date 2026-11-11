package config

import (
	_ "embed"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
	"go.yaml.in/yaml/v3"
)

//go:embed capabilities.default.yaml
var defaultCapabilitiesYAML []byte

// capFile is the on-disk shape of a capability ruleset file. Strings are kept
// raw here and validated into domain enums in parseCapFile so an unknown value
// fails fast with a message naming the offending backend/model/field.
type capFile struct {
	Backends map[string]struct {
		ContextTiers   []string `yaml:"context_tiers"`
		EffortLevels   []string `yaml:"effort_levels"`
		ModelOverrides map[string]struct {
			ContextTiers []string          `yaml:"context_tiers"`
			EffortLevels []string          `yaml:"effort_levels"`
			EffortFloor  map[string]string `yaml:"effort_floor"`
		} `yaml:"model_overrides"`
	} `yaml:"backends"`
	// Decode each model separately so schema errors name the offending id.
	LunaRouteModels map[string]yaml.Node `yaml:"lunaroute_models"`
}

type supportYAML struct {
	Support  string `yaml:"support"`
	Evidence string `yaml:"evidence"`
}

type limitYAML struct {
	Tokens   int    `yaml:"tokens"`
	Evidence string `yaml:"evidence"`
}

type effortYAML struct {
	Levels   []string `yaml:"levels"`
	Evidence string   `yaml:"evidence"`
}

type lunaRouteModelYAML struct {
	Tasks           map[string]supportYAML `yaml:"tasks"`
	Backends        map[string]supportYAML `yaml:"backends"`
	ContextTokens   *limitYAML             `yaml:"context_tokens"`
	OutputTokens    *limitYAML             `yaml:"output_tokens"`
	Efforts         map[string]effortYAML  `yaml:"efforts"`
	Reasoning       supportYAML            `yaml:"reasoning"`
	RequestSettings map[string]supportYAML `yaml:"request_settings"`
}

type parsedCaps struct {
	backends  map[string]provider.BackendCapability
	lunaroute map[string]provider.LunaRouteModel
}

// LoadCapabilityRules parses the embedded default ruleset, then overlays an
// optional override file. The overlay replaces entries per backend and per
// LunaRoute model: a key in the override fully replaces its embedded entry;
// absent keys keep the embedded default. An empty or non-existent
// overridePath yields the embedded default. Parsing is strict: an unknown enum
// value fails fast. Intended to be loaded ONCE at startup (no hot reload).
func LoadCapabilityRules(overridePath string) (provider.CapabilityRules, error) {
	base, err := parseCapFile(defaultCapabilitiesYAML)
	if err != nil {
		return provider.CapabilityRules{}, fmt.Errorf("embedded capabilities: %w", err)
	}
	if overridePath != "" {
		raw, err := os.ReadFile(overridePath)
		switch {
		case err == nil:
			ov, perr := parseCapFile(raw)
			if perr != nil {
				return provider.CapabilityRules{}, fmt.Errorf("override capabilities %q: %w", overridePath, perr)
			}
			for k, v := range ov.backends {
				base.backends[k] = v // per-backend replace
			}
			for k, v := range ov.lunaroute {
				base.lunaroute[k] = v // per-model replace
			}
		case os.IsNotExist(err):
			// Missing override is not an error; the embedded default stands.
		default:
			return provider.CapabilityRules{}, fmt.Errorf("reading %q: %w", overridePath, err)
		}
	}
	return provider.NewCapabilityRules(base.backends).WithLunaRouteModels(base.lunaroute), nil
}

// parseCapFile unmarshals raw YAML and validates every axis string into its
// domain enum, returning backend rules and curated per-model facts.
func parseCapFile(raw []byte) (parsedCaps, error) {
	var f capFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return parsedCaps{}, err
	}
	out := make(map[string]provider.BackendCapability, len(f.Backends))
	for name, b := range f.Backends {
		bc := provider.BackendCapability{Backend: name}
		var err error
		if bc.ContextTiers, err = parseTiers(b.ContextTiers); err != nil {
			return parsedCaps{}, fmt.Errorf("%s: %w", name, err)
		}
		if bc.EffortLevels, err = parseEfforts(b.EffortLevels); err != nil {
			return parsedCaps{}, fmt.Errorf("%s: %w", name, err)
		}
		if len(b.ModelOverrides) > 0 {
			bc.ModelOverrides = make(map[string]provider.ModelCapability, len(b.ModelOverrides))
			for m, mo := range b.ModelOverrides {
				mc := provider.ModelCapability{}
				if mc.ContextTiers, err = parseTiers(mo.ContextTiers); err != nil {
					return parsedCaps{}, fmt.Errorf("%s/%s: %w", name, m, err)
				}
				if mc.EffortLevels, err = parseEfforts(mo.EffortLevels); err != nil {
					return parsedCaps{}, fmt.Errorf("%s/%s: %w", name, m, err)
				}
				if len(mo.EffortFloor) > 0 {
					mc.EffortFloor = make(map[provider.Effort]provider.Effort, len(mo.EffortFloor))
					for from, to := range mo.EffortFloor {
						fe, ferr := provider.ParseEffort(from)
						if ferr != nil {
							return parsedCaps{}, fmt.Errorf("%s/%s effort_floor key: %w", name, m, ferr)
						}
						te, terr := provider.ParseEffort(to)
						if terr != nil {
							return parsedCaps{}, fmt.Errorf("%s/%s effort_floor value: %w", name, m, terr)
						}
						mc.EffortFloor[fe] = te
					}
				}
				bc.ModelOverrides[m] = mc
			}
		}
		out[name] = bc
	}
	lr, err := parseLunaRouteModels(f.LunaRouteModels)
	if err != nil {
		return parsedCaps{}, err
	}
	return parsedCaps{backends: out, lunaroute: lr}, nil
}

func parseSupport(model, field, key string, s supportYAML) (provider.SupportFact, error) {
	switch provider.Support(s.Support) {
	case provider.SupportUnknown, "":
		return provider.SupportFact{Support: provider.SupportUnknown}, nil
	case provider.SupportSupported, provider.SupportUnsupported:
		if strings.TrimSpace(s.Evidence) == "" {
			return provider.SupportFact{}, fmt.Errorf("lunaroute_models/%s/%s/%s: a known fact needs an evidence reference", model, field, key)
		}
		return provider.SupportFact{Support: provider.Support(s.Support), Evidence: s.Evidence}, nil
	default:
		return provider.SupportFact{}, fmt.Errorf("lunaroute_models/%s/%s/%s: support must be supported, unsupported or unknown, got %q", model, field, key, s.Support)
	}
}

func parseLimit(model, field string, l *limitYAML) (provider.LimitFact, error) {
	if l == nil {
		return provider.LimitFact{}, nil
	}
	if l.Tokens <= 0 {
		return provider.LimitFact{}, fmt.Errorf("lunaroute_models/%s/%s: a known limit must be positive (omit the field when unknown)", model, field)
	}
	if strings.TrimSpace(l.Evidence) == "" {
		return provider.LimitFact{}, fmt.Errorf("lunaroute_models/%s/%s: a known limit needs an evidence reference", model, field)
	}
	return provider.LimitFact{Tokens: l.Tokens, Evidence: l.Evidence}, nil
}

func parseLunaRouteModels(in map[string]yaml.Node) (map[string]provider.LunaRouteModel, error) {
	out := make(map[string]provider.LunaRouteModel, len(in))
	for id, node := range in {
		var y lunaRouteModelYAML
		if err := node.Decode(&y); err != nil {
			return nil, fmt.Errorf("lunaroute_models/%s: %w", id, err)
		}
		m := provider.LunaRouteModel{ID: id}
		if len(y.Tasks) > 0 {
			m.Tasks = map[provider.ProfileKind]provider.SupportFact{}
			for k, s := range y.Tasks {
				kind := provider.ProfileKind(k)
				if kind != provider.ProfileKindChat && kind != provider.ProfileKindAgent {
					return nil, fmt.Errorf("lunaroute_models/%s/tasks: unknown task kind %q (chat or agent)", id, k)
				}
				f, err := parseSupport(id, "tasks", k, s)
				if err != nil {
					return nil, err
				}
				m.Tasks[kind] = f
			}
		}
		if len(y.Backends) > 0 {
			m.Backends = map[string]provider.SupportFact{}
			for b, s := range y.Backends {
				if !slices.Contains(provider.LunaRouteBackends, b) {
					return nil, fmt.Errorf("lunaroute_models/%s/backends: unknown backend %q", id, b)
				}
				f, err := parseSupport(id, "backends", b, s)
				if err != nil {
					return nil, err
				}
				m.Backends[b] = f
			}
		}
		var err error
		if m.ContextTokens, err = parseLimit(id, "context_tokens", y.ContextTokens); err != nil {
			return nil, err
		}
		if m.OutputTokens, err = parseLimit(id, "output_tokens", y.OutputTokens); err != nil {
			return nil, err
		}
		if len(y.Efforts) > 0 {
			m.Efforts = map[string]provider.EffortFact{}
			for axis, e := range y.Efforts {
				if axis != provider.LunaRouteChatAxis && !slices.Contains(provider.LunaRouteBackends, axis) {
					return nil, fmt.Errorf("lunaroute_models/%s/efforts: unknown axis %q (chat or a backend)", id, axis)
				}
				if strings.TrimSpace(e.Evidence) == "" {
					return nil, fmt.Errorf("lunaroute_models/%s/efforts/%s: a known effort fact needs an evidence reference", id, axis)
				}
				levels := make([]provider.Effort, 0, len(e.Levels))
				for _, l := range e.Levels {
					v, err := provider.ParseEffort(l)
					if err != nil || v == provider.EffortDefault {
						return nil, fmt.Errorf("lunaroute_models/%s/efforts/%s: invalid level %q", id, axis, l)
					}
					levels = append(levels, v)
				}
				m.Efforts[axis] = provider.EffortFact{Known: true, Levels: levels, Evidence: e.Evidence}
			}
		}
		if m.Reasoning, err = parseSupport(id, "reasoning", "", y.Reasoning); err != nil {
			return nil, err
		}
		m.RequestSettings = map[string]provider.SupportFact{}
		for setting, value := range y.RequestSettings {
			if setting != "temperature" && setting != "output_schema" {
				return nil, fmt.Errorf("lunaroute_models/%s/request_settings: unknown setting %q", id, setting)
			}
			fact, err := parseSupport(id, "request_settings", setting, value)
			if err != nil {
				return nil, err
			}
			m.RequestSettings[setting] = fact
		}
		out[id] = m
	}
	return out, nil
}

// parseTiers validates a slice of context-window strings into domain enums.
// A nil input yields a nil slice (the model-override "inherit" sentinel).
func parseTiers(ss []string) ([]provider.ContextWindow, error) {
	if ss == nil {
		return nil, nil
	}
	out := make([]provider.ContextWindow, 0, len(ss))
	for _, s := range ss {
		v, err := provider.ParseContextWindow(s)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// parseEfforts validates a slice of effort strings into domain enums. A nil
// input yields a nil slice (the model-override "inherit" sentinel).
func parseEfforts(ss []string) ([]provider.Effort, error) {
	if ss == nil {
		return nil, nil
	}
	out := make([]provider.Effort, 0, len(ss))
	for _, s := range ss {
		v, err := provider.ParseEffort(s)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
