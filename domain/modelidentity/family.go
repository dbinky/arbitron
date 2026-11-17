package modelidentity

import (
	"regexp"
	"strings"
	"unicode"
)

// Family is a model's actual vendor lab ("anthropic", "openai", "deepseek",
// "zhipuai", "moonshotai", …) or FamilyUnresolved. It is never the gateway or
// backend the model is reached through.
type Family string

// FamilyUnresolved marks a model whose vendor cannot be proved. Safeguards
// treat it as a refusal, never as "different from everything".
const FamilyUnresolved Family = "unresolved"

// reviewedAliasVendors maps route provider → exact (lowercased) model id →
// lab for opaque gateway aliases whose vendor the prefix rules cannot see.
// Operator ruling R3 revokes the Claude-shaped mapping: those aliases route to
// open-weight models, not Anthropic. No opaque alias is currently reviewed.
var reviewedAliasVendors = map[string]map[string]string{}

// routerPrefixes are routing namespaces (not vendors) that may precede an id.
var routerPrefixes = map[string]bool{
	"openai-codex": true, "ollama": true, "ollama-cloud": true, "openrouter": true, "lunaroute": true,
}

var (
	// bedrockQualifierRE matches Bedrock-style "us.anthropic." / "anthropic."
	// qualifiers; a hyphen before the first dot (gpt-6.1, glm-5.3) cannot match.
	bedrockQualifierRE = regexp.MustCompile(`^(?:(?:us|eu|apac|global)\.)?([a-z]+)\.`)
	openAIReasoningRE  = regexp.MustCompile(`^o\d`)
	lunaRouteAliasRE   = regexp.MustCompile(`-lr[0-9]+(?:-|$)`)
)

// lunaRouteClaudeAlias recognizes the entire revoked claude-*-lr* namespace,
// not just today's numeric suffixes. R3 forbids inferring Anthropic identity
// from these gateway-assigned names, even if a stale review entry remains.
func lunaRouteClaudeAlias(model string) bool {
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "claude-") && strings.Contains(model, "-lr")
}

func labForAlias(alias string) string {
	for _, f := range families {
		if contains(f.labAliases, alias) {
			return f.lab
		}
	}
	return ""
}

func labForName(id string) string {
	for _, f := range families {
		if strings.HasPrefix(id, f.namePrefix) {
			return f.lab
		}
	}
	if openAIReasoningRE.MatchString(id) {
		return "openai"
	}
	return ""
}

// ClassifyFamily returns the vendor lab of model on routeProvider ("" for a
// no-connection/sample input). It strips at most one routing or vendor prefix,
// rejects a vendor prefix that contradicts the id, and resolves opaque aliases
// only through reviewedAliasVendors.
func ClassifyFamily(routeProvider, model string) Family {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" || strings.HasPrefix(m, "(") || !strings.ContainsFunc(m, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) {
		return FamilyUnresolved
	}
	route := strings.ToLower(strings.TrimSpace(routeProvider))
	aliasRoute := route
	if aliasRoute == "" {
		aliasRoute = "lunaroute" // Sample inputs have no routing metadata.
	}
	hint := ""
	if prefix, rest, ok := strings.Cut(m, "/"); ok {
		if prefix == "" || rest == "" || strings.Contains(rest, "/") {
			return FamilyUnresolved
		}
		switch {
		case routerPrefixes[prefix]:
		case labForAlias(prefix) != "":
			hint = labForAlias(prefix)
		default:
			return FamilyUnresolved
		}
		m = rest
	}
	if sub := bedrockQualifierRE.FindStringSubmatch(m); sub != nil {
		lab := labForAlias(sub[1])
		if lab == "" || hint != "" && hint != lab {
			return FamilyUnresolved
		}
		hint = lab
		m = m[len(sub[0]):]
	}
	if lunaRouteClaudeAlias(m) {
		return FamilyUnresolved
	}
	// Look up aliases AFTER prefix consistency has been established. A vendor-
	// shaped alias is never authorized by the generic claude- prefix.
	if lab, ok := reviewedAliasVendors[aliasRoute][m]; ok {
		if hint != "" && hint != lab {
			return FamilyUnresolved
		}
		return Family(lab)
	}
	if lunaRouteAliasRE.MatchString(m) {
		return FamilyUnresolved
	}
	lab := labForName(m)
	if lab == "" || hint != "" && hint != lab {
		return FamilyUnresolved
	}
	return Family(lab)
}
