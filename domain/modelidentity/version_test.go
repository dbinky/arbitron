package modelidentity_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVersionResolverRecognizesExplicitVersions(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	cases := []struct {
		name string
		req  modelidentity.VersionRequest
	}{
		{"claude subscription full id", modelidentity.VersionRequest{Model: "claude-opus-5-5", Backend: "claude-cli"}},
		{"claude dated snapshot", modelidentity.VersionRequest{Model: "claude-haiku-4-5-20251001", Backend: "claude-code"}},
		{"claude shorthand with version", modelidentity.VersionRequest{Model: "opus-4.8", Backend: "claude-code"}},
		{"claude long context suffix", modelidentity.VersionRequest{Model: "claude-sonnet-4-6[1m]", Backend: "claude-cli"}},
		{"anthropic api", modelidentity.VersionRequest{Model: "claude-sonnet-4-6", RouteProvider: "anthropic", ConnectionRef: "a"}},
		{"openai", modelidentity.VersionRequest{Model: "gpt-6.1-sol", RouteProvider: "openai", ConnectionRef: "o"}},
		{"openrouter prefixed", modelidentity.VersionRequest{Model: "deepseek/deepseek-v4-flash", RouteProvider: "openrouter", ConnectionRef: "r", Backend: "opencode"}},
		{"opencode provider slash model", modelidentity.VersionRequest{Model: "ollama-cloud/glm-5.3", Backend: "opencode", RouteProvider: "ollama-cloud"}},
		{"bedrock fixed id", modelidentity.VersionRequest{Model: "anthropic.claude-sonnet-4-5-20250929-v1:0", RouteProvider: "amazon-bedrock", ConnectionRef: "b"}},
		{"bedrock inference profile", modelidentity.VersionRequest{Model: "us.anthropic.claude-sonnet-4-5-20250929-v1:0", RouteProvider: "bedrock", ConnectionRef: "b"}},
	}
	for _, c := range cases {
		got, err := r.SpecificVersion(context.Background(), c.req)
		require.NoError(t, err, c.name)
		assert.Equal(t, c.req.Model, got, "%s: executable spelling is preserved", c.name)
	}
}

func TestVersionAnthropicAPIRoute(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	for _, model := range []string{"claude-sonnet-4-6", "claude-haiku-4-5-20251001", "anthropic/claude-opus-5-5"} {
		t.Run(model, func(t *testing.T) {
			got, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{
				Model: model, RouteProvider: "anthropic-api", ConnectionRef: "native-api",
			})
			require.NoError(t, err)
			assert.Equal(t, model, got, "the route alias must not rewrite executable spelling")
		})
	}
	for _, model := range []string{"sonnet", "opus-4.8", "production-v2", "claude-sonnet-latest", "claude-opus-5-5-fast", "other/claude-sonnet-4-6", "anthropic/anthropic/claude-sonnet-4-6"} {
		t.Run("reject/"+model, func(t *testing.T) {
			got, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{
				Model: model, RouteProvider: "anthropic-api", ConnectionRef: "native-api",
			})
			require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
			assert.Empty(t, got)
		})
	}
}

func TestVersionResolverRejectsMutableAndOpaqueModels(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	for name, req := range map[string]modelidentity.VersionRequest{
		"tier alias sonnet":       {Model: "sonnet", Backend: "claude-cli"},
		"tier alias opus":         {Model: "opus", Backend: "claude-code"},
		"latest":                  {Model: "latest", RouteProvider: "openai", ConnectionRef: "o"},
		"default":                 {Model: "default", Backend: "claude-cli"},
		"explicit latest suffix":  {Model: "gpt-latest", RouteProvider: "openai", ConnectionRef: "o"},
		"claude latest":           {Model: "claude-sonnet-latest", RouteProvider: "anthropic", ConnectionRef: "a"},
		"azure deployment":        {Model: "gpt-4o-deploy", RouteProvider: "azure-openai", ConnectionRef: "az"},
		"foundry deployment":      {Model: "my-model-5", RouteProvider: "azure-foundry", ConnectionRef: "az"},
		"litellm alias":           {Model: "fast-model-2", RouteProvider: "litellm", ConnectionRef: "l"},
		"bedrock opaque":          {Model: "my-profile-arn-1", RouteProvider: "bedrock", ConnectionRef: "b"},
		"bedrock application arn": {Model: "arn:aws:bedrock:us-east-1:1:application-inference-profile/abc1", RouteProvider: "bedrock", ConnectionRef: "b"},
		"empty":                   {Model: "", RouteProvider: "openai", ConnectionRef: "o"},
		"whitespace":              {Model: "  ", RouteProvider: "openai", ConnectionRef: "o"},
		"inner whitespace":        {Model: "gpt 5", RouteProvider: "openai", ConnectionRef: "o"},
		"no version token":        {Model: "mistral-large", RouteProvider: "openrouter", ConnectionRef: "r"},
		"claude tier without ver": {Model: "claude-sonnet", RouteProvider: "anthropic", ConnectionRef: "a"},
		"claude shorthand no ver": {Model: "sonnet-latest", Backend: "claude-cli"},
	} {
		_, err := r.SpecificVersion(context.Background(), req)
		require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable, name)
		assert.Contains(t, err.Error(), "fixed", "%s: the error asks for a fixed starting model", name)
	}
}

func TestVersionRejectsNumericAliases(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	for name, req := range map[string]modelidentity.VersionRequest{
		"unknown provider":         {Model: "production-v2", RouteProvider: "custom-proxy", ConnectionRef: "proxy"},
		"known provider alias":     {Model: "production-v2", RouteProvider: "openai", ConnectionRef: "api"},
		"missing routing provider": {Model: "gpt-6-sol", ConnectionRef: "unknown"},
		"azure numeric deployment": {Model: "gpt-6.1-sol", RouteProvider: "azure-openai", ConnectionRef: "azure"},
		"foundry numeric alias":    {Model: "deployment-2026-10-01", RouteProvider: "azure-foundry", ConnectionRef: "azure"},
		"proxy numeric alias":      {Model: "gpt-6-sol", RouteProvider: "litellm", ConnectionRef: "proxy"},
		"unknown codex provider":   {Model: "gpt-6-sol", RouteProvider: "custom-proxy", Backend: "codex"},
		"mutable suffixed alias":   {Model: "gpt-6-sol-latest", RouteProvider: "openai", ConnectionRef: "api"},
		"unproved suffix":          {Model: "gpt-6-sol-production-v2", RouteProvider: "openai", ConnectionRef: "api"},
		"unproved claude variant":  {Model: "claude-opus-5-5-fast", RouteProvider: "anthropic", ConnectionRef: "api"},
		"wrong lab":                {Model: "glm-5.3", RouteProvider: "openai", ConnectionRef: "api"},
		"foreign router prefix":    {Model: "someone/deepseek-v4-flash", RouteProvider: "openrouter", ConnectionRef: "router"},
		"repeated router prefix":   {Model: "deepseek/deepseek/deepseek-v4-flash", RouteProvider: "openrouter", ConnectionRef: "router"},
		"opaque bedrock revision":  {Model: "custom.production-v2-v1:0", RouteProvider: "bedrock", ConnectionRef: "aws"},
		"foreign bedrock region":   {Model: "xx.anthropic.claude-sonnet-4-5-20250929-v1:0", RouteProvider: "bedrock", ConnectionRef: "aws"},
		"local ollama alias":       {Model: "glm-5.3", RouteProvider: "ollama", ConnectionRef: "local"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := r.SpecificVersion(context.Background(), req)
			require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), "fixed model version")
		})
	}
}

func TestVersionSupportedRoutesPreserveSpelling(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	for name, req := range map[string]modelidentity.VersionRequest{
		"anthropic prefixed": {Model: "anthropic/claude-opus-5-5", RouteProvider: "anthropic", ConnectionRef: "a"},
		"openai snapshot":    {Model: "gpt-4.1-2025-04-14", RouteProvider: "openai", ConnectionRef: "o"},
		"deepseek direct":    {Model: "deepseek-v4.1-flash", RouteProvider: "deepseek", ConnectionRef: "d"},
		"deepseek snapshot":  {Model: "deepseek-v4-flash:0731", RouteProvider: "ollama-cloud", ConnectionRef: "d"},
		"zhipuai direct":     {Model: "glm-5.3", RouteProvider: "zhipuai", ConnectionRef: "z", Backend: "pi"},
		"moonshotai direct":  {Model: "kimi-k2.7-code", RouteProvider: "moonshotai", ConnectionRef: "m", Backend: "opencode"},
		"codex subscription": {Model: "gpt-6.1-sol", Backend: "codex"},
		"claude cli route":   {Model: "claude-opus-5-5", RouteProvider: "claude-cli", Backend: "claude-code"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := r.SpecificVersion(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, req.Model, got)
		})
	}
}

func TestIdentityPolicyCannotCertifyNumericAlias(t *testing.T) {
	src := initialPolicy()
	src.policy.Rules = append(src.policy.Rules, modelidentity.PatternRule{
		Providers: []string{"openai"}, Tier: "production", Pattern: `production-v(\d+)`, VersionGroups: []int{1},
	})
	e := mapOne(t, newMapper(t, src), "openai", "production-v2", "")
	assert.Equal(t, "production", e.Tier, "editable tier membership is not fixed-version evidence")
	assert.False(t, e.Immutable)
	assert.Empty(t, e.SpecificModel)
}

// A known fixed model keeps resolving when everything that could be read live
// (policy, catalog) is down: the resolver reads neither.
func TestVersionResolverNeedsNoPolicyOrCatalog(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	got, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: "claude-opus-5-5", Backend: "claude-cli"})
	require.NoError(t, err)
	assert.Equal(t, "claude-opus-5-5", got)
}

func TestVersionResolverHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := modelidentity.NewVersionResolver().SpecificVersion(ctx, modelidentity.VersionRequest{Model: "claude-opus-5-5", Backend: "claude-cli"})
	assert.ErrorIs(t, err, context.Canceled)
}

// OpenRouter accepts exactly one recognized vendor prefix followed by that
// vendor's fixed spelling, plus the bare DeepSeek spelling it always accepted.
func TestVersionOpenRouterRecognizedVendorIDs(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	req := func(model string) modelidentity.VersionRequest {
		return modelidentity.VersionRequest{Model: model, RouteProvider: "openrouter", ConnectionRef: "r", Backend: "opencode"}
	}
	for _, model := range []string{
		"anthropic/claude-sonnet-4.5",
		"anthropic/claude-opus-5-5",
		"anthropic/claude-haiku-4-5-20251001",
		"openai/gpt-6.1-sol",
		"openai/gpt-4.1-2025-04-14",
		"deepseek/deepseek-v4-pro",
		"deepseek-v4-flash",
		"z-ai/glm-5.3",
		"moonshotai/kimi-k2.5",
	} {
		got, err := r.SpecificVersion(context.Background(), req(model))
		require.NoError(t, err, model)
		assert.Equal(t, model, got)
	}
	for _, model := range []string{
		"anthropic/claude-sonnet",
		"anthropic/claude-sonnet-4.5[1m]",
		"openai/gpt-4o",
		"openai/gpt-5",
		"mistralai/mistral-large",
		"anthropic/anthropic/claude-sonnet-4.5",
		"~anthropic/claude-sonnet-latest",
		"claude-sonnet-4.5",
	} {
		_, err := r.SpecificVersion(context.Background(), req(model))
		require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable, model)
	}
}

// Refusals name the spellings the route accepts, so an operator knows what to
// set the profile to.
func TestVersionRefusalNamesAcceptedSpelling(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	cases := map[string]struct {
		req  modelidentity.VersionRequest
		want string
	}{
		"subscription": {modelidentity.VersionRequest{Model: "sonnet", Backend: "claude-cli"}, "claude-sonnet-5-5"},
		"openai":       {modelidentity.VersionRequest{Model: "gpt-4o", RouteProvider: "openai", ConnectionRef: "o"}, "gpt-N[.N]-{sol,astra,luna,terra}"},
		"openrouter":   {modelidentity.VersionRequest{Model: "openai/gpt-5", RouteProvider: "openrouter", ConnectionRef: "r"}, "anthropic/claude-sonnet-4.5"},
		"litellm":      {modelidentity.VersionRequest{Model: "gpt-4o", RouteProvider: "litellm", ConnectionRef: "l"}, "cannot run pinned workflows"},
		"local ollama": {modelidentity.VersionRequest{Model: "llama3.1:8b", RouteProvider: "ollama", ConnectionRef: "l"}, "cannot run pinned workflows"},
	}
	for name, c := range cases {
		_, err := r.SpecificVersion(context.Background(), c.req)
		require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable, name)
		assert.Contains(t, err.Error(), "accepted spellings: ", name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

// A pi profile with no connection names its vendor in the model prefix
// ("openai-codex/gpt-6.1-sol"). The resolver arrives either with the
// no-connection stamp "claude-cli" (live route, path inventory) or with no route
// provider at all (trigger-time request), and must apply the prefixed vendor's
// grammar rather than the Claude subscription one.
func TestVersionPiSubscriptionPrefixedModels(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	ok := []string{
		"openai-codex/gpt-6.1-sol",
		"openai-codex/gpt-5-astra",
		"openai-codex/gpt-4.1-2025-04-14",
	}
	for _, stamp := range []string{"claude-cli", ""} {
		for _, model := range ok {
			t.Run(stamp+"/"+model, func(t *testing.T) {
				got, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: model, RouteProvider: stamp, Backend: "pi"})
				require.NoError(t, err)
				assert.Equal(t, model, got, "the spelling pi routes on is preserved")
			})
		}
	}
	got, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: "openai-codex/gpt-6.1-sol", RouteProvider: "claude-cli", Backend: "PI"})
	require.NoError(t, err, "backend matching is case-insensitive like every other route")
	assert.Equal(t, "openai-codex/gpt-6.1-sol", got)
}

func TestVersionPiSubscriptionFailsClosed(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	for name, model := range map[string]string{
		"codex mutable alias":         "openai-codex/gpt-6-latest",
		"codex numeric alias":         "openai-codex/gpt-6.1",
		"anthropic claude sonnet":     "anthropic/claude-sonnet-5-5",
		"anthropic claude haiku":      "anthropic/claude-haiku-4-5-20251001",
		"anthropic claude opus 1m":    "anthropic/claude-opus-4-6[1m]",
		"claude tier alias":           "anthropic/sonnet",
		"claude shorthand":            "anthropic/sonnet-4.5",
		"claude on codex prefix":      "openai-codex/claude-opus-5-5",
		"gpt on anthropic prefix":     "anthropic/gpt-6.1-sol",
		"unknown vendor prefix":       "google/gemini-3-pro",
		"key-based openai prefix":     "openai/gpt-6.1-sol",
		"no prefix":                   "gpt-6.1-sol",
		"bare claude id":              "claude-sonnet-5-5",
		"repeated prefix":             "openai-codex/openai-codex/gpt-6.1-sol",
		"empty prefix":                "/gpt-6.1-sol",
		"empty model after prefix":    "openai-codex/",
		"prefix case differs":         "OpenAI-Codex/gpt-6.1-sol",
		"whitespace around separator": "openai-codex/ gpt-6.1-sol",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: model, RouteProvider: "claude-cli", Backend: "pi"})
			require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable)
			assert.Contains(t, err.Error(), "backend pi", "the refusal names the pi route, not the Claude subscription")
			assert.NotContains(t, err.Error(), "provider claude-cli")
			assert.Contains(t, err.Error(), "openai-codex/gpt-N[.N]-{sol,astra,luna,terra}")
			assert.NotContains(t, err.Error(), "anthropic/claude-{", "pi rejects the anthropic prefix at exec time, so it is not offered as an accepted spelling")
		})
	}
}

// A pi profile WITH a connection keeps the connection's grammar: the prefix
// fallback is only for the no-connection path.
func TestVersionPiConnectionRouteUnchanged(t *testing.T) {
	r := modelidentity.NewVersionResolver()
	_, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: "openai-codex/gpt-6.1-sol", RouteProvider: "openai", ConnectionRef: "o", Backend: "pi"})
	require.ErrorIs(t, err, modelidentity.ErrSpecificVersionUnavailable, "an openai connection does not accept pi's openai-codex prefix")
	got, err := r.SpecificVersion(context.Background(), modelidentity.VersionRequest{Model: "claude-sonnet-4-5", RouteProvider: "anthropic-api", ConnectionRef: "a", Backend: "pi"})
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-4-5", got)
}
