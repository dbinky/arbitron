package modelidentity

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrSpecificVersionUnavailable marks a model that does not name a fixed
// executable version: a mutable alias (`sonnet`, `latest`), an Azure deployment
// name or an opaque Bedrock/proxy id.
var ErrSpecificVersionUnavailable = errors.New("no specific model version available")

var (
	// claudeSubscriptionRE is the Claude CLI spelling when no connection routes the
	// call: the full id or the tier-version shorthand, an optional dated snapshot
	// and the long-context suffix.
	claudeSubscriptionRE = regexp.MustCompile(`^(?:claude-)?(?:opus|sonnet|haiku|fable)-\d+(?:[.-]\d+)?(?:-\d{8})?(?:\[1m\])?$`)
	// These full matches implement the recorded native spellings, not editable
	// tier rules. See docs/designs/latest-model-profiles-source-evidence.md.
	claudeNativeRE  = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable)-\d+(?:-\d+)?(?:-\d{8})?(?:\[1m\])?$`)
	openaiFixedRE   = regexp.MustCompile(`^(?:gpt-\d+(?:\.\d+)?-(?:sol|astra|luna|terra)|gpt-4\.1-2025-04-14)$`)
	deepseekFixedRE = regexp.MustCompile(`^deepseek-v\d+(?:\.\d+)?-(?:flash|pro)(?::\d{4})?$`)
	glmFixedRE      = regexp.MustCompile(`^glm-\d+(?:\.\d+)?(?:-flash)?$`)
	kimiFixedRE     = regexp.MustCompile(`^kimi-k\d+(?:\.\d+)?(?:-code)?$`)
	// Only the documented Anthropic foundation-model spelling and its known
	// geographic inference-profile prefixes are recognized. A revision suffix
	// alone cannot prove that an arbitrary Bedrock id is a fixed model.
	bedrockFixedRE = regexp.MustCompile(`^(?:(?:us|eu|apac|global)\.)?anthropic\.claude-(?:opus|sonnet|haiku)-\d+(?:-\d+)?(?:-\d{8})?-v\d+:\d+$`)
)

// opaqueProviders name models with a user-chosen deployment or alias that this
// package has no mapping for.
var opaqueProviders = map[string]bool{
	"azure-openai":  true,
	"azure-foundry": true,
	"litellm":       true,
}

// openrouterClaudeRE is OpenRouter's Anthropic spelling: the full Claude id with
// a dotted or dashed minor version and an optional dated snapshot.
var openrouterClaudeRE = regexp.MustCompile(`^claude-(?:opus|sonnet|haiku|fable)-\d+(?:[.-]\d+)?(?:-\d{8})?$`)

// openrouterVendors maps each recognized OpenRouter vendor prefix to that
// vendor's fixed-version grammar. Exactly one prefix is removed; an unknown or
// repeated prefix matches nothing.
var openrouterVendors = map[string]*regexp.Regexp{
	"anthropic":  openrouterClaudeRE,
	"openai":     openaiFixedRE,
	"deepseek":   deepseekFixedRE,
	"z-ai":       glmFixedRE,
	"moonshotai": kimiFixedRE,
}

// piSubscriptionVendors maps each pi provider prefix a no-connection pi profile
// may name onto that vendor's fixed-version grammar. pi routes on the prefix
// ("openai-codex/gpt-6.1-sol"), so exactly one recognized prefix is removed; an
// unknown, repeated or empty prefix matches nothing. anthropic/ is
// deliberately absent: the pi runtime rejects it at exec time.
var piSubscriptionVendors = map[string]*regexp.Regexp{
	"openai-codex": openaiFixedRE,
}

// Accepted spellings per route, named in refusals so an operator can see what
// to set the profile to.
const (
	acceptClaudeSubscription = "[claude-]{opus,sonnet,haiku,fable}-N[.N|-N][-YYYYMMDD][[1m]], e.g. claude-sonnet-5-5"
	acceptClaudeNative       = "[anthropic/]claude-{opus,sonnet,haiku,fable}-N[-N][-YYYYMMDD][[1m]], e.g. claude-sonnet-5-5"
	acceptOpenAI             = "[openai/]gpt-N[.N]-{sol,astra,luna,terra} or gpt-4.1-2025-04-14"
	acceptCodex              = "gpt-N[.N]-{sol,astra,luna,terra} or gpt-4.1-2025-04-14"
	acceptDeepSeek           = "[deepseek/]deepseek-vN[.N]-{flash,pro}[:MMDD]"
	acceptOpenRouter         = "vendor/model with vendor anthropic (claude-{tier}-N[.N][-YYYYMMDD]), openai, deepseek, z-ai (glm) or moonshotai (kimi) and that vendor's fixed spelling, e.g. anthropic/claude-sonnet-4.5"
	acceptGLM                = "[zhipuai/]glm-N[.N][-flash]"
	acceptKimi               = "[moonshotai/]kimi-kN[.N][-code]"
	acceptOllamaCloud        = "[ollama-cloud/]deepseek-vN[.N]-{flash,pro}, glm-N[.N][-flash] or kimi-kN[.N][-code]"
	acceptBedrock            = "[us.|eu.|apac.|global.]anthropic.claude-{opus,sonnet,haiku}-N[-N][-YYYYMMDD]-vN:M"
	acceptPiSubscription     = "openai-codex/gpt-N[.N]-{sol,astra,luna,terra} (or openai-codex/gpt-4.1-2025-04-14), e.g. openai-codex/gpt-6.1-sol"
	acceptNone               = "none: this route cannot run pinned workflows in this release (LiteLLM, local Ollama and unrecognized providers have no fixed-version identity)"
)

type lunaRouteFixedID struct {
	Vendor, FixedTarget, Evidence string
}

var lunaRouteReviewedIDs = map[string]lunaRouteFixedID{
	// docs/evidence/lunaroute-provider/model-identities.md#glm-5.3
	"glm-5.3": {Vendor: "zhipuai", FixedTarget: "glm-5.3", Evidence: "docs/evidence/lunaroute-provider/model-identities.md#glm-5.3"},
	// docs/evidence/lunaroute-provider/model-identities.md#glm-5.3-flash (operator ruling R3)
	"glm-5.3-flash": {Vendor: "zhipuai", FixedTarget: "glm-5.3-flash", Evidence: "docs/evidence/lunaroute-provider/model-identities.md#glm-5.3-flash"},
	// docs/evidence/lunaroute-provider/model-identities.md#deepseek-4.1-flash (operator ruling R3)
	"deepseek-4.1-flash": {Vendor: "deepseek", FixedTarget: "deepseek-4.1-flash", Evidence: "docs/evidence/lunaroute-provider/model-identities.md#deepseek-4.1-flash"},
}

func lunaRouteFixed(model string) bool {
	entry, ok := lunaRouteReviewedIDs[model]
	return model != "" && !lunaRouteClaudeAlias(model) && ok && entry.Vendor != "" && entry.FixedTarget != "" && entry.Evidence != ""
}

// LunaRouteExecutedModelAccepted checks reported execution evidence against a
// reviewed selection. Fixed ids require exact equality; reviewed aliases allow
// drift only within their reviewed vendor family.
func LunaRouteExecutedModelAccepted(selected, reported string) bool {
	if !lunaRouteFixed(selected) {
		return false
	}
	entry := lunaRouteReviewedIDs[selected]
	if strings.HasPrefix(entry.FixedTarget, "alias:") {
		return reported != "" && ClassifyFamily("lunaroute", reported) == Family(entry.Vendor)
	}
	return reported == selected
}

func unavailable(req VersionRequest, why string) error {
	return fmt.Errorf("%w: model %q (%s) %s; set the profile to a fixed model version", ErrSpecificVersionUnavailable, req.Model, routeLabel(req), why)
}

func routeLabel(req VersionRequest) string {
	switch {
	case isPiSubscription(req):
		return "backend pi"
	case req.RouteProvider != "":
		return "provider " + req.RouteProvider
	case req.Backend != "":
		return "backend " + req.Backend
	}
	return "claude subscription"
}

// isPiSubscription reports a pi profile that names no connection. The resolver
// stamps the Claude subscription id ("claude-cli") on every no-connection
// binding regardless of backend, and a trigger-time request carries no route
// provider at all; for pi both mean only "the vendor is in the model prefix".
func isPiSubscription(req VersionRequest) bool {
	provider := strings.ToLower(strings.TrimSpace(req.RouteProvider))
	return strings.EqualFold(strings.TrimSpace(req.Backend), "pi") && req.ConnectionRef == "" &&
		(provider == "" || provider == "claude-cli")
}

func piSubscriptionFixed(model string) bool {
	vendor, native, ok := strings.Cut(model, "/")
	if !ok {
		return false
	}
	re := piSubscriptionVendors[vendor]
	return re != nil && re.MatchString(native)
}

func openrouterFixed(model string) bool {
	if deepseekFixedRE.MatchString(model) {
		return true
	}
	vendor, native, ok := strings.Cut(model, "/")
	if !ok {
		return false
	}
	re := openrouterVendors[vendor]
	return re != nil && re.MatchString(native)
}

// explicitVersion applies the shipped grammar. It reads no policy and no catalog,
// so a policy or refresh outage cannot stop a run on a model that was already
// working. The model is returned with its spelling unchanged.
func explicitVersion(req VersionRequest) (string, error) {
	model := req.Model
	if model == "" || strings.TrimSpace(model) == "" {
		return "", unavailable(req, "is empty")
	}
	provider := strings.ToLower(strings.TrimSpace(req.RouteProvider))
	backend := strings.ToLower(strings.TrimSpace(req.Backend))

	var fixed bool
	accepted := acceptNone
	switch {
	case opaqueProviders[provider]:
		return "", unavailable(req, "is a deployment or proxy alias with no known fixed version; accepted spellings: "+acceptNone)
	case provider == "bedrock" || provider == "amazon-bedrock":
		fixed, accepted = bedrockFixedRE.MatchString(model), acceptBedrock
	case isPiSubscription(req):
		fixed, accepted = piSubscriptionFixed(model), acceptPiSubscription
	case provider == "claude-cli" || provider == "" && req.ConnectionRef == "" && (backend == "" || backend == "claude-cli" || backend == "claude-code"):
		fixed, accepted = claudeSubscriptionRE.MatchString(model), acceptClaudeSubscription
	case provider == "" && req.ConnectionRef == "" && backend == "codex":
		fixed, accepted = openaiFixedRE.MatchString(model), acceptCodex
	case provider == "anthropic" || provider == "anthropic-api":
		fixed, accepted = claudeNativeRE.MatchString(strings.TrimPrefix(model, "anthropic/")), acceptClaudeNative
	case provider == "openai":
		fixed, accepted = openaiFixedRE.MatchString(strings.TrimPrefix(model, "openai/")), acceptOpenAI
	case provider == "deepseek":
		fixed, accepted = deepseekFixedRE.MatchString(strings.TrimPrefix(model, "deepseek/")), acceptDeepSeek
	case provider == "lunaroute":
		fixed, accepted = lunaRouteFixed(model), "use a reviewed fixed LunaRoute id from connection_models and the reviewed-id table"
	case provider == "openrouter":
		fixed, accepted = openrouterFixed(model), acceptOpenRouter
	case provider == "zhipuai":
		fixed, accepted = glmFixedRE.MatchString(strings.TrimPrefix(model, "zhipuai/")), acceptGLM
	case provider == "moonshotai":
		fixed, accepted = kimiFixedRE.MatchString(strings.TrimPrefix(model, "moonshotai/")), acceptKimi
	case provider == "ollama-cloud":
		native := strings.TrimPrefix(model, "ollama-cloud/")
		fixed = deepseekFixedRE.MatchString(native) || glmFixedRE.MatchString(native) || kimiFixedRE.MatchString(native)
		accepted = acceptOllamaCloud
	}
	if !fixed {
		return "", unavailable(req, "has no supported fixed-version spelling on this route; accepted spellings: "+accepted)
	}
	return model, nil
}

type versionResolver struct{}

// NewVersionResolver returns the grammar-based resolver. It needs no policy,
// catalog or store.
func NewVersionResolver() VersionResolver { return versionResolver{} }

func (versionResolver) SpecificVersion(ctx context.Context, req VersionRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return explicitVersion(req)
}
