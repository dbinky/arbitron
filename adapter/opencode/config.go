package opencode

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/arbitron/arbitron/domain/provider"
)

const opencodeConfigSchema = "https://opencode.ai/config.json"

// ErrNoEffectiveTools is returned when a scoped AllowedTools list maps to zero
// opencode tools — a tool-less run is fail-closed, never silently launched.
var ErrNoEffectiveTools = fmt.Errorf("opencode: scoped allowlist maps to no opencode tools")

type opencodeConfig struct {
	Schema           string                   `json:"$schema"`
	Provider         map[string]providerBlock `json:"provider,omitempty"`
	Permission       map[string]string        `json:"permission,omitempty"`
	Model            string                   `json:"model,omitempty"`
	SmallModel       string                   `json:"small_model,omitempty"`
	EnabledProviders []string                 `json:"enabled_providers,omitempty"`
}

type providerBlock struct {
	NPM     string                    `json:"npm,omitempty"`
	Name    string                    `json:"name,omitempty"`
	Options map[string]any            `json:"options,omitempty"`
	Models  map[string]map[string]any `json:"models,omitempty"`
}

// buildConfig generates the OPENCODE_CONFIG_CONTENT JSON for one run: a
// permission block from the posture (tool names mapped) and, when the provider
// needs one, a provider block whose apiKey references the credential env var via
// {env:VAR} (secret stays in env). It takes an already-resolved descriptor (the
// single resolution happens in RunAgent) so the three builders cannot disagree
// about a provider string.
//
// dropped is the set of requested tools with no opencode 1.17.x equivalent; it is
// returned so RunAgent can surface the (security-relevant) trimming in the
// AgentResult/trace, not only in server logs.
func buildConfig(d providerDescriptor, binding provider.ResolvedBinding, post Posture, rules provider.CapabilityRules) (cfg string, dropped []string, err error) {
	doc := opencodeConfig{Schema: opencodeConfigSchema}

	perm, dropped, err := buildPermissionBlock(post)
	if err != nil {
		return "", dropped, err
	}
	if len(dropped) > 0 {
		// A dropped tool silently narrows the effective security posture, so this
		// is Warn-level, not Info.
		slog.Warn("opencode: dropped unmapped tools (effective tool posture narrowed)",
			"provider", binding.Provider, "tools", dropped)
	}
	doc.Permission = perm

	if needsProviderBlock(d, binding) {
		baseURL := binding.BaseURL
		lunaroute := provider.IsLunaRoute(binding.Provider)
		if lunaroute {
			baseURL, err = provider.LunaRouteOpenAIBaseURL(baseURL)
			if err != nil {
				return "", dropped, err
			}
		}
		// Retain the general credential-egress backstop as well as the
		// hosted-only LunaRoute guard above.
		if baseURL != "" {
			checked := binding
			checked.BaseURL = baseURL
			if err := validateBaseURL(checked); err != nil {
				return "", dropped, err
			}
		}
		opts := map[string]any{}
		if baseURL != "" {
			opts["baseURL"] = baseURL
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
		block := providerBlock{NPM: d.npmPackage, Options: opts}
		if lunaroute {
			leaf := modelLeaf(strings.TrimSpace(binding.Model), d.opencodeID)
			if leaf == "" {
				return "", dropped, ErrModelRequired
			}
			m, _ := rules.LunaRouteModel(leaf)
			if !m.ContextTokens.Known() || !m.OutputTokens.Known() {
				return "", dropped, &provider.LunaRouteRefusal{Reason: provider.ErrLunaRouteLimitsUnknown, Model: leaf, Backend: "opencode",
					Detail: "opencode on LunaRoute needs known context and output limits for this model in the capability table (lunaroute_models)"}
			}
			block.Models = map[string]map[string]any{leaf: {
				"name": leaf, "tool_call": true,
				"limit": map[string]any{"context": m.ContextTokens.Tokens, "output": m.OutputTokens.Tokens},
			}}
			ref := d.opencodeID + "/" + leaf
			doc.Model, doc.SmallModel = ref, ref
			doc.EnabledProviders = []string{d.opencodeID}
		}
		doc.Provider = map[string]providerBlock{d.opencodeID: block}
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return "", dropped, fmt.Errorf("opencode: marshal config: %w", err)
	}
	return string(out), dropped, nil
}

// buildPermissionBlock translates a resolved Posture into an opencode permission
// map. It is the security-relevant policy, isolated for standalone testing:
//   - scoped allowlist => deny-all then allow the mapped (deduplicated) set; an
//     allowlist that maps to NO opencode tool fails closed (ErrNoEffectiveTools).
//   - allow-all => {"*":"allow"}.
//   - neither => {"*":"deny"} (explicit deny-all, NOT nil). args.go ALWAYS passes
//     --dangerously-skip-permissions, so a nil/absent permission block would let
//     opencode apply its OWN (open) default — a fail-OPEN run in a secrets-bearing
//     workspace. The most-restrictive posture must be explicitly closed.
//
// dropped carries the requested tools with no opencode equivalent so the caller
// can surface the trimming.
func buildPermissionBlock(post Posture) (perm map[string]string, dropped []string, err error) {
	switch {
	case len(post.AllowedTools) > 0:
		mapped, dropped := mapTools(post.AllowedTools)
		if len(mapped) == 0 {
			return nil, dropped, ErrNoEffectiveTools
		}
		perm := map[string]string{"*": "deny"}
		for _, tool := range mapped {
			perm[tool] = "allow"
		}
		return perm, dropped, nil
	case post.AllowAll:
		return map[string]string{"*": "allow"}, nil, nil
	default:
		// Most-restrictive posture (allow-all disabled, no per-profile allowlist).
		// Emit an explicit deny-all: because --dangerously-skip-permissions is
		// always on the argv, a nil block would fail OPEN to opencode's default.
		return map[string]string{"*": "deny"}, nil, nil
	}
}

// needsProviderBlock is the SOLE gate for emitting a provider.<id> config block.
// A block is needed when the provider always requires structured options
// (litellm/ollama-local/azure: alwaysConfig) OR the binding carries a custom
// BaseURL for a provider arbitron must register by npm package.
//
// The standalone `npmPackage != ""` DISJUNCT was the redundant-npm artifact (it
// emitted an npm-only block for a keyed provider on its built-in path, no custom
// URL) and is dropped. The npmPackage CONJUNCT on the BaseURL branch is kept and
// load-bearing: a fully built-in provider (npmPackage == "", e.g. ollama-cloud
// whose binding carries the canonical base URL) must NOT get a provider block —
// emitting one with an empty npm would break opencode's provider loading.
func needsProviderBlock(d providerDescriptor, binding provider.ResolvedBinding) bool {
	return d.alwaysConfig || (binding.BaseURL != "" && d.npmPackage != "")
}

// validateBaseURL re-validates a binding's custom BaseURL against the production
// StrictURLPolicy, preserving the narrow local-Ollama loopback carve-out. It is
// defense-in-depth on the credential-egress sink; the authoritative guard is at
// the connection write path.
func validateBaseURL(binding provider.ResolvedBinding) error {
	conn := provider.Connection{
		Name:     binding.Provider,
		Provider: binding.Provider,
		BaseURL:  binding.BaseURL,
	}
	if err := provider.ValidateConnectionURL(conn, provider.StrictURLPolicy); err != nil {
		return fmt.Errorf("opencode: base url rejected: %w", err)
	}
	return nil
}
