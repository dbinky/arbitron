package lunaroute_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/stretchr/testify/require"
)

type authorSurface struct {
	text               string
	genericBackendList bool
}

func lunaRouteAuthorSurfaces(t *testing.T) map[string]authorSurface {
	t.Helper()
	guide, err := readRepoFile(repoRoot(t), "docs/setup/lunaroute.md")
	require.NoError(t, err)
	return map[string]authorSurface{
		"guide":        {text: string(guide)},
		"instructions": {text: adaptmcp.AuthoringGuidance, genericBackendList: true},
		"profile hint": {text: adaptmcp.LunaRouteBackendHint},
	}
}

// Appending a support claim must fail even if every required refusal phrase
// and every valid example is still present in the author-visible surface.
func TestLunaRouteAuthorSurfacesR7(t *testing.T) {
	for name, surface := range lunaRouteAuthorSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, validateLunaRouteAuthorScope(surface))
		})
	}
}

func TestLunaRouteAuthorSurfacesRejectContradictoryProse(t *testing.T) {
	for name, surface := range lunaRouteAuthorSurfaces(t) {
		t.Run(name, func(t *testing.T) {
			for mutation, change := range map[string]func(string) string{
				"appended claim": func(s string) string {
					return s + "\nLunaRoute works with claude-code and opencode agent backends."
				},
				"prepended claim": func(s string) string {
					return "LunaRoute supports opencode.\n" + s
				},
				"mixed case and markdown": func(s string) string {
					return s + "\nLunaRoute supports **Claude Code** and `OpenCode`."
				},
				"contradiction in refusal clause": func(s string) string {
					return strings.Replace(s, "are not yet supported", "are not yet supported but opencode works now", 1)
				},
				"removed negation": func(s string) string {
					return strings.Replace(s, "are not yet supported", "are supported", 1)
				},
				"empty feed": func(string) string { return "" },
			} {
				t.Run(mutation, func(t *testing.T) {
					changed := surface
					changed.text = change(surface.text)
					require.NotEqual(t, surface.text, changed.text, "mutation must change the real input")
					require.Error(t, validateLunaRouteAuthorScope(changed))
				})
			}
		})
	}
}

func validateLunaRouteAuthorScope(surface authorSurface) error {
	text := strings.ToLower(strings.Join(strings.Fields(strings.NewReplacer("`", "", "*", "").Replace(surface.text)), " "))
	if !strings.Contains(text, "lunaroute") {
		return fmt.Errorf("missing LunaRoute author guidance")
	}
	deferredBackend := regexp.MustCompile(`\b(?:claude[- ]?code|open[- ]?code)\b`)
	// Keep deferred names in complete, reviewed refusal/deferral clauses.
	// Checking for a nearby "not" would let a valid refusal mask an added
	// support claim elsewhere in the same sentence or document.
	for _, clause := range strings.FieldsFunc(text, func(r rune) bool { return strings.ContainsRune(".;!?", r) }) {
		clause = strings.TrimSpace(clause)
		if !deferredBackend.MatchString(clause) {
			continue
		}
		switch clause {
		case "lunaroute claude-code and opencode are not yet supported",
			"r7 defers claude code/opencode, forced-helper collectors and the full live mcp acceptance matrix":
			continue
		case "choose the model and, for agent profiles, the backend (claude-code, codex, opencode or pi) explicitly":
			// The server instructions describe every provider before specifying
			// LunaRoute's narrower scope. This is not allowed in the guide/hint.
			if surface.genericBackendList {
				continue
			}
		}
		return fmt.Errorf("deferred LunaRoute backend outside a reviewed refusal/deferral clause")
	}
	return nil
}
