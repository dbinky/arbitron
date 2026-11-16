package modelidentity_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/stretchr/testify/assert"
)

func TestClassifyFamily(t *testing.T) {
	cases := []struct {
		route, model string
		want         modelidentity.Family
	}{
		{"", "claude-opus-5-5", "anthropic"},
		{"lunaroute", "claude-sonnet-5-lr1", modelidentity.FamilyUnresolved},
		{"lunaroute", "claude-sonnet-5-lr1-background", modelidentity.FamilyUnresolved},
		{"lunaroute", "claude-sonnet-5-lr999", modelidentity.FamilyUnresolved},
		{"lunaroute", "glm-5.3-flash", "zhipuai"},
		{"lunaroute", "deepseek-4.1-flash", "deepseek"},
		{"", "anthropic/claude-opus-5-5", "anthropic"},
		{"", "us.anthropic.claude-sonnet-5-5", "anthropic"},
		{"", "anthropic/us.openai.gpt-6.1", modelidentity.FamilyUnresolved},
		{"", "openai/anthropic.claude-opus-5-5", modelidentity.FamilyUnresolved},
		{"", "anthropic/us.anthropic.claude-opus-5-5", "anthropic"},
		{"", "openai/us.openai.gpt-6.1", "openai"},
		{"lunaroute", "us.unknownvendor.glm-5.3", modelidentity.FamilyUnresolved},
		{"bedrock", "anthropic.claude-opus-5-5-v1:0", "anthropic"},
		{"", "gpt-6.1-sol", "openai"},
		{"", "openai-codex/gpt-6.1-sol", "openai"},
		{"", "o3-mini", "openai"},
		{"lunaroute", "glm-5.3", "zhipuai"},
		{"lunaroute", "GLM-5.3", "zhipuai"},
		{"lunaroute", "lunaroute/glm-5.3", "zhipuai"},
		{"", "z-ai/glm-5", "zhipuai"},
		{"", "ollama/glm-5.1", "zhipuai"},
		{"", "deepseek-v4-pro", "deepseek"},
		{"", "Kimi-K3", "moonshotai"},
		{"", "moonshotai/kimi-k3", "moonshotai"},
		{"lunaroute", "muse-spark-open", modelidentity.FamilyUnresolved},
		{"lunaroute", "openai/claude-sonnet-5-5", modelidentity.FamilyUnresolved},
		{"lunaroute", "anthropic/glm-5.3", modelidentity.FamilyUnresolved},
		{"", "openrouter/anthropic/claude-opus-5-5", modelidentity.FamilyUnresolved},
		{"", "unknownvendor/glm-5.3", modelidentity.FamilyUnresolved},
		{"", "us.openai.claude-x", modelidentity.FamilyUnresolved},
		{"", "openai-codex/", modelidentity.FamilyUnresolved},
		{"", "/glm-5.3", modelidentity.FamilyUnresolved},
		{"", "", modelidentity.FamilyUnresolved},
		{"", "  ", modelidentity.FamilyUnresolved},
		{"", "(inherit)", modelidentity.FamilyUnresolved},
		{"", "—", modelidentity.FamilyUnresolved},
		{"", "-", modelidentity.FamilyUnresolved},
		{"", "--", modelidentity.FamilyUnresolved},
		{"", "latest", modelidentity.FamilyUnresolved},
	}
	for _, c := range cases {
		t.Run(c.route+"|"+c.model, func(t *testing.T) {
			assert.Equal(t, c.want, modelidentity.ClassifyFamily(c.route, c.model))
		})
	}
}

func TestClassifyFamily_NeverReturnsGateway(t *testing.T) {
	for _, m := range []string{"lunaroute", "lunaroute/x", "glm-5.3", "claude-sonnet-5-lr1"} {
		assert.NotEqual(t, modelidentity.Family("lunaroute"), modelidentity.ClassifyFamily("lunaroute", m))
	}
}

func TestFamilyUnreviewedClaudeAliasIsUnresolved(t *testing.T) {
	for _, route := range []string{"", "lunaroute", "openrouter"} {
		for _, model := range []string{"claude-sonnet-5-lr1", "claude-opus-lr29", "claude-haiku-lr37", "claude-sonnet-lr", "claude-sonnet-lr-next", "claude-sonnet-5-lr999", "claude-sonnet-5-lr1-background", "claude-sonnet-5-lr999-priority", "anthropic/claude-opus-lr29", "lunaroute/claude-opus-lr29", "us.anthropic.claude-opus-lr29"} {
			t.Run(route+"|"+model, func(t *testing.T) {
				assert.Equal(t, modelidentity.FamilyUnresolved, modelidentity.ClassifyFamily(route, model))
			})
		}
	}
}

func TestFamilyReviewedAliasContradictoryPrefixRefused(t *testing.T) {
	for _, route := range []string{"", "lunaroute"} {
		for _, model := range []string{"openai/claude-sonnet-5-lr1", "us.openai.claude-sonnet-5-lr1"} {
			t.Run(route+"|"+model, func(t *testing.T) {
				assert.Equal(t, modelidentity.FamilyUnresolved, modelidentity.ClassifyFamily(route, model))
			})
		}
	}
}

func TestFamilyNoRouteReviewedAliasMatchesLunaRoute(t *testing.T) {
	const model = "claude-sonnet-5-lr1"
	assert.Equal(t, modelidentity.FamilyUnresolved, modelidentity.ClassifyFamily("lunaroute", model))
	assert.Equal(t, modelidentity.ClassifyFamily("lunaroute", model), modelidentity.ClassifyFamily("", model))
}
