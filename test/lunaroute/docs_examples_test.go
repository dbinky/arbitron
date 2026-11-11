package lunaroute_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type docProfile struct {
	Alias, Kind, Backend, Model, ConnectionRef, SelectionMode, ContextWindow, Effort string
}

func parseLunaRouteDocExamples(doc string) ([]docProfile, error) {
	var lines []string
	blocks, inFence, inExamples := 0, false, false
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			if !inFence {
				inExamples = line == "```text"
				if inExamples {
					blocks++
				}
				inFence = true
			} else {
				if line != "```" {
					return nil, fmt.Errorf("malformed example fence")
				}
				inFence, inExamples = false, false
			}
			continue
		}
		if inExamples {
			lines = append(lines, line)
		}
	}
	if inFence || blocks != 1 {
		return nil, fmt.Errorf("expected exactly one complete text example block")
	}
	connections, aliases := map[string]bool{}, map[string]bool{}
	var profiles []docProfile
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "connection" && fields[0] != "profile" {
			continue
		}
		if len(fields) < 2 {
			return nil, fmt.Errorf("example needs a name")
		}
		values := map[string]string{}
		allowed := map[string]bool{"provider": true, "base_url": true, "api_key": true}
		if fields[0] == "profile" {
			allowed = map[string]bool{"kind": true, "backend": true, "model": true, "connection": true, "selection_mode": true, "context_window": true, "effort": true}
		}
		for _, field := range fields[2:] {
			key, value, ok := strings.Cut(field, "=")
			if !ok || value == "" || !allowed[key] || values[key] != "" {
				return nil, fmt.Errorf("malformed, unknown or duplicate example field")
			}
			values[key] = value
		}
		name := fields[1]
		if fields[0] == "connection" {
			if connections[name] || values["provider"] != "lunaroute" {
				return nil, fmt.Errorf("duplicate or non-LunaRoute connection")
			}
			if _, err := provider.NormalizeLunaRouteBaseURL(values["base_url"]); err != nil {
				return nil, fmt.Errorf("invalid example destination")
			}
			connections[name] = true
			continue
		}
		if aliases[name] || values["kind"] == "" || values["model"] == "" || values["connection"] == "" {
			return nil, fmt.Errorf("duplicate alias or missing required profile field")
		}
		aliases[name] = true
		profiles = append(profiles, docProfile{Alias: name, Kind: values["kind"], Backend: values["backend"], Model: values["model"], ConnectionRef: values["connection"], SelectionMode: values["selection_mode"], ContextWindow: values["context_window"], Effort: values["effort"]})
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("example block has no profiles")
	}
	for _, p := range profiles {
		if !connections[p.ConnectionRef] {
			return nil, fmt.Errorf("profile references an undeclared connection")
		}
	}
	return profiles, nil
}

func validateLunaRouteDocProfile(p docProfile, caps provider.LunaRouteCapabilities) error {
	if p.SelectionMode != "" && p.SelectionMode != string(provider.SelectionPinned) || p.Kind == "chat" && p.Backend != "" {
		return provider.ErrInvalidSelection
	}
	if _, err := modelidentity.NewVersionResolver().SpecificVersion(context.Background(), modelidentity.VersionRequest{
		Model: p.Model, RouteProvider: "lunaroute", ConnectionRef: p.ConnectionRef, Backend: p.Backend,
	}); err != nil {
		return err
	}
	if modelidentity.ClassifyFamily("lunaroute", p.Model) == modelidentity.FamilyUnresolved {
		return provider.ErrLunaRouteFamilyUnresolved
	}
	return provider.CheckLunaRouteRoute(caps, provider.LunaRouteRoute{Kind: provider.ProfileKind(p.Kind), Model: p.Model, Backend: p.Backend,
		ContextWindow: provider.ContextWindow(p.ContextWindow), Effort: provider.Effort(p.Effort)})
}

const docExample = "```text\nconnection lr provider=lunaroute base_url=https://gw.lunaroute.com api_key=lr_…\nprofile chat kind=chat connection=lr model=glm-5.3-flash selection_mode=pinned\nprofile agent kind=agent backend=claude-code connection=lr model=glm-5.3 context_window=default effort=default\n```\n"

// Distinctive limits are fixtures, not product facts. Deployed defaults are
// checked separately by the production-evidence/release gate tests.
func docFixtureCaps() provider.CapabilityRules {
	models := map[string]provider.LunaRouteModel{}
	for _, id := range []string{"glm-5.3", "glm-5.3-flash", "deepseek-4.1-flash"} {
		models[id] = provider.LunaRouteModel{ID: id,
			ContextTokens: provider.LimitFact{Tokens: 202752, Evidence: "doc-fixture#limits"},
			OutputTokens:  provider.LimitFact{Tokens: 32768, Evidence: "doc-fixture#limits"}}
	}
	return provider.NewCapabilityRules(nil).WithLunaRouteModels(models)
}

func TestParseLunaRouteDocExamples(t *testing.T) {
	got, err := parseLunaRouteDocExamples(docExample)
	require.NoError(t, err)
	require.Equal(t, []docProfile{
		{Alias: "chat", Kind: "chat", ConnectionRef: "lr", Model: "glm-5.3-flash", SelectionMode: "pinned"},
		{Alias: "agent", Kind: "agent", Backend: "claude-code", ConnectionRef: "lr", Model: "glm-5.3", ContextWindow: "default", Effort: "default"},
	}, got)
	for _, p := range got {
		require.NoError(t, validateLunaRouteDocProfile(p, docFixtureCaps()))
	}
}

func TestLunaRouteDocMalformedExamples(t *testing.T) {
	for name, doc := range map[string]string{
		"no block":              "no examples",
		"unterminated":          strings.TrimSuffix(docExample, "```\n"),
		"empty":                 "```text\nconnection lr provider=lunaroute\n```",
		"two blocks":            docExample + docExample,
		"duplicate alias":       strings.Replace(docExample, "profile agent", "profile chat", 1),
		"duplicate field":       strings.Replace(docExample, "kind=chat", "kind=chat kind=chat", 1),
		"unknown field":         strings.Replace(docExample, "kind=chat", "kind=chat typo=x", 1),
		"malformed field":       strings.Replace(docExample, "kind=chat", "kind", 1),
		"empty value":           strings.Replace(docExample, "kind=chat", "kind=", 1),
		"missing model":         strings.Replace(docExample, " model=glm-5.3-flash", "", 1),
		"missing connection":    strings.Replace(docExample, " connection=lr", "", 1),
		"undeclared connection": strings.Replace(docExample, " connection=lr", " connection=other", 1),
		"duplicate connection":  strings.Replace(docExample, "profile chat", "connection lr provider=lunaroute\nprofile chat", 1),
		"bad connection field":  strings.Replace(docExample, "provider=lunaroute", "provider", 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseLunaRouteDocExamples(doc)
			require.Error(t, err)
		})
	}
	// Other fenced blocks and explanatory lines do not count as profiles.
	got, err := parseLunaRouteDocExamples("```json\n{}\n```\n" + strings.Replace(docExample, "profile chat", "# commentary\nprofile chat", 1))
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestLunaRouteDocRouteRefusals(t *testing.T) {
	profiles, err := parseLunaRouteDocExamples(docExample)
	require.NoError(t, err)
	for _, tc := range []struct {
		name   string
		change func(*docProfile)
		want   error
	}{
		{"uppercase", func(p *docProfile) { p.Model = "GLM-5.3" }, provider.ErrSpecificVersionUnavailable},
		{"latest", func(p *docProfile) { p.Model = "latest" }, provider.ErrSpecificVersionUnavailable},
		{"numeric unreviewed", func(p *docProfile) { p.Model = "glm-999.9" }, provider.ErrSpecificVersionUnavailable},
		{"revoked alias", func(p *docProfile) { p.Model = "claude-sonnet-5-lr1" }, provider.ErrSpecificVersionUnavailable},
		{"weights name", func(p *docProfile) { p.Model = "deepseek-4.1-flash-1017" }, provider.ErrSpecificVersionUnavailable},
		{"codex effort none", func(p *docProfile) { p.Backend = "codex"; p.Effort = "none" }, provider.ErrLunaRouteAxisUnsupported},
		{"pi disabled", func(p *docProfile) { p.Backend = "pi" }, provider.ErrLunaRouteBackendUnsupported},
		{"implicit backend", func(p *docProfile) { p.Backend = "" }, provider.ErrLunaRouteBackendRequired},
		{"following", func(p *docProfile) { p.SelectionMode = "following" }, provider.ErrInvalidSelection},
		{"bad mode", func(p *docProfile) { p.SelectionMode = "typo" }, provider.ErrInvalidSelection},
		{"native context", func(p *docProfile) { p.ContextWindow = "200k" }, provider.ErrLunaRouteAxisUnsupported},
		{"unsupported effort", func(p *docProfile) { p.Backend = "opencode"; p.Effort = "low" }, provider.ErrLunaRouteAxisUnsupported},
		{"chat backend", func(p *docProfile) { p.Kind = "chat" }, provider.ErrInvalidSelection},
		{"unknown kind", func(p *docProfile) { p.Kind = "typo" }, provider.ErrLunaRouteIncompatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := profiles[1]
			tc.change(&p)
			require.ErrorIs(t, validateLunaRouteDocProfile(p, docFixtureCaps()), tc.want)
		})
	}
	for _, axis := range []string{"task", "backend"} {
		t.Run("incompatible "+axis, func(t *testing.T) {
			p := profiles[1]
			m, ok := docFixtureCaps().LunaRouteModel(p.Model)
			require.True(t, ok)
			unsupported := provider.SupportFact{Support: provider.SupportUnsupported, Evidence: "doc-fixture#incompatible"}
			if axis == "task" {
				m.Tasks = map[provider.ProfileKind]provider.SupportFact{provider.ProfileKindAgent: unsupported}
			} else {
				m.Backends = map[string]provider.SupportFact{p.Backend: unsupported}
			}
			caps := provider.NewCapabilityRules(nil).WithLunaRouteModels(map[string]provider.LunaRouteModel{p.Model: m})
			require.ErrorIs(t, validateLunaRouteDocProfile(p, caps), provider.ErrLunaRouteIncompatible)
		})
	}
	p := profiles[1]
	p.Backend = "codex"
	require.NoError(t, validateLunaRouteDocProfile(p, docFixtureCaps()))
	for _, backend := range []string{"claude-code", "opencode"} {
		p := profiles[1]
		p.Backend = backend
		require.NoError(t, validateLunaRouteDocProfile(p, docFixtureCaps()))
		require.ErrorIs(t, validateLunaRouteDocProfile(p, provider.NewCapabilityRules(nil)), provider.ErrLunaRouteLimitsUnknown)
	}
}

func TestLunaRouteGuideExamplesAreValid(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "docs/setup/lunaroute.md"))
	require.NoError(t, err)
	doc := string(raw)
	profiles, err := parseLunaRouteDocExamples(doc)
	require.NoError(t, err)
	kinds, backends := map[string]bool{}, map[string]bool{}
	for _, p := range profiles {
		kinds[p.Kind], backends[p.Backend] = true, true
		require.NoError(t, validateLunaRouteDocProfile(p, docFixtureCaps()), p.Alias)
		if p.Kind == "agent" {
			assert.ErrorIs(t, validateLunaRouteDocProfile(p, provider.NewCapabilityRules(nil)), provider.ErrLunaRouteLimitsUnknown, p.Alias)
		}
	}
	assert.True(t, kinds["chat"] && kinds["agent"])
	assert.True(t, backends["claude-code"] && backends["opencode"])
	for _, text := range []string{
		"claude-code and opencode need known context and output limits",
		"codex is enabled for keyed LunaRoute routes; pi is not yet enabled",
		"claude-*-lr* aliases are refused and never classified as Anthropic",
		"Live backend/helper verification and whole-release readiness remain pending",
		"sources/lr02-live-models-2026-11-11.json",
	} {
		assert.Contains(t, doc, text)
	}
	readme, err := os.ReadFile(filepath.Join(repoRoot(t), "README.md"))
	require.NoError(t, err)
	assert.Contains(t, string(readme), "[docs/setup/lunaroute.md](docs/setup/lunaroute.md)")
}
