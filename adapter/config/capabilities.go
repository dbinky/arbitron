package config

import (
	_ "embed"
	"fmt"
	"os"

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
}

// LoadCapabilityRules parses the embedded default ruleset, then overlays an
// optional override file. The overlay replaces entries per-backend: a backend
// key present in the override fully replaces the embedded default for that
// backend; absent keys keep the embedded default. An empty or non-existent
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
			for k, v := range ov {
				base[k] = v // per-backend replace
			}
		case os.IsNotExist(err):
			// Missing override is not an error; the embedded default stands.
		default:
			return provider.CapabilityRules{}, fmt.Errorf("reading %q: %w", overridePath, err)
		}
	}
	return provider.NewCapabilityRules(base), nil
}

// parseCapFile unmarshals raw YAML and validates every axis string into its
// domain enum, returning a per-backend map ready for NewCapabilityRules.
func parseCapFile(raw []byte) (map[string]provider.BackendCapability, error) {
	var f capFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	out := make(map[string]provider.BackendCapability, len(f.Backends))
	for name, b := range f.Backends {
		bc := provider.BackendCapability{Backend: name}
		var err error
		if bc.ContextTiers, err = parseTiers(b.ContextTiers); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if bc.EffortLevels, err = parseEfforts(b.EffortLevels); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if len(b.ModelOverrides) > 0 {
			bc.ModelOverrides = make(map[string]provider.ModelCapability, len(b.ModelOverrides))
			for m, mo := range b.ModelOverrides {
				mc := provider.ModelCapability{}
				if mc.ContextTiers, err = parseTiers(mo.ContextTiers); err != nil {
					return nil, fmt.Errorf("%s/%s: %w", name, m, err)
				}
				if mc.EffortLevels, err = parseEfforts(mo.EffortLevels); err != nil {
					return nil, fmt.Errorf("%s/%s: %w", name, m, err)
				}
				if len(mo.EffortFloor) > 0 {
					mc.EffortFloor = make(map[provider.Effort]provider.Effort, len(mo.EffortFloor))
					for from, to := range mo.EffortFloor {
						fe, ferr := provider.ParseEffort(from)
						if ferr != nil {
							return nil, fmt.Errorf("%s/%s effort_floor key: %w", name, m, ferr)
						}
						te, terr := provider.ParseEffort(to)
						if terr != nil {
							return nil, fmt.Errorf("%s/%s effort_floor value: %w", name, m, terr)
						}
						mc.EffortFloor[fe] = te
					}
				}
				bc.ModelOverrides[m] = mc
			}
		}
		out[name] = bc
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
