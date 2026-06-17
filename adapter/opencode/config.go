package opencode

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/provider"
)

const opencodeConfigSchema = "https://opencode.ai/config.json"

// ErrNoEffectiveTools is returned when a scoped AllowedTools list maps to zero
// opencode tools — a tool-less run is fail-closed, never silently launched.
var ErrNoEffectiveTools = fmt.Errorf("opencode: scoped allowlist maps to no opencode tools")

type opencodeConfig struct {
	Schema     string                   `json:"$schema"`
	Provider   map[string]providerBlock `json:"provider,omitempty"`
	Permission map[string]string        `json:"permission,omitempty"`
}

type providerBlock struct {
	NPM     string         `json:"npm,omitempty"`
	Name    string         `json:"name,omitempty"`
	Options map[string]any `json:"options,omitempty"`
}

// buildConfig generates the OPENCODE_CONFIG_CONTENT JSON for one run: a
// permission block from the posture (tool names mapped) and, when the provider
// has no built-in or carries a custom BaseURL, a provider block whose apiKey
// references the credential env var via {env:VAR} (secret stays in env).
func buildConfig(binding provider.ResolvedBinding, post Posture) (string, error) {
	d, ok := descriptorFor(binding.Provider)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownProvider, binding.Provider)
	}

	cfg := opencodeConfig{Schema: opencodeConfigSchema}

	switch {
	case len(post.AllowedTools) > 0:
		mapped, dropped := mapTools(post.AllowedTools)
		if len(dropped) > 0 {
			slog.Info("opencode: dropped unmapped tools", "provider", binding.Provider, "tools", dropped)
		}
		if len(mapped) == 0 {
			return "", ErrNoEffectiveTools
		}
		perm := map[string]string{"*": "deny"}
		for _, tool := range mapped {
			perm[tool] = "allow"
		}
		cfg.Permission = perm
	case post.AllowAll:
		cfg.Permission = map[string]string{"*": "allow"}
	default:
		// Closed default: omit permission; opencode's own default applies under
		// --dangerously-skip-permissions.
	}

	if d.alwaysConfig || (binding.BaseURL != "" && d.npmPackage != "") {
		opts := map[string]any{}
		if binding.BaseURL != "" {
			opts["baseURL"] = binding.BaseURL
		}
		if d.credEnvVar != "" {
			opts["apiKey"] = fmt.Sprintf("{env:%s}", d.credEnvVar)
		}
		if d.opencodeID == "azure" {
			if rn := binding.Extras["resource-name"]; rn != "" {
				opts["resourceName"] = rn
			}
			if av := binding.Extras["api-version"]; av != "" {
				opts["apiVersion"] = av
			}
		}
		if len(opts) > 0 || d.npmPackage != "" {
			cfg.Provider = map[string]providerBlock{
				d.opencodeID: {NPM: d.npmPackage, Options: opts},
			}
		}
	}

	out, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("opencode: marshal config: %w", err)
	}
	return string(out), nil
}
