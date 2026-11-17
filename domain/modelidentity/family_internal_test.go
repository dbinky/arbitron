package modelidentity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReviewedAliasVendors(t *testing.T) {
	assert.Empty(t, reviewedAliasVendors["lunaroute"], "R3 revokes the Claude-shaped vendor mapping")
	saved := reviewedAliasVendors
	t.Cleanup(func() { reviewedAliasVendors = saved })
	reviewedAliasVendors = map[string]map[string]string{"lunaroute": {"muse-spark-open": "meta"}}
	assert.Equal(t, Family("meta"), ClassifyFamily("lunaroute", "muse-spark-open"))
	assert.Equal(t, Family("meta"), ClassifyFamily(" LunaRoute ", "Muse-Spark-Open"))
	assert.Equal(t, Family("meta"), ClassifyFamily("", "muse-spark-open"), "samples consume the same reviewed table")
	assert.Equal(t, FamilyUnresolved, ClassifyFamily("", "claude-sonnet-5-lr1"), "samples cannot infer an alias removed from the reviewed table")
	assert.Equal(t, FamilyUnresolved, ClassifyFamily("openrouter", "muse-spark-open"), "mappings are per route")
}

// The alias has no name-derived vendor. A contradictory prefix must not
// override its reviewed vendor, and a matching prefix must remain usable.
func TestReviewedAliasVendorPrefixConsistency(t *testing.T) {
	saved := reviewedAliasVendors
	t.Cleanup(func() { reviewedAliasVendors = saved })
	reviewedAliasVendors = map[string]map[string]string{"lunaroute": {"reviewed-model": "zhipuai"}}
	for _, route := range []string{"", "lunaroute"} {
		for _, tc := range []struct {
			model string
			want  Family
		}{
			{"reviewed-model", "zhipuai"},
			{"zhipuai/reviewed-model", "zhipuai"},
			{"us.zhipuai.reviewed-model", "zhipuai"},
			{"openai/reviewed-model", FamilyUnresolved},
			{"us.openai.reviewed-model", FamilyUnresolved},
		} {
			t.Run(route+"|"+tc.model, func(t *testing.T) {
				assert.Equal(t, tc.want, ClassifyFamily(route, tc.model))
			})
		}
	}
}

func TestLunaRouteClaudeAliasesCannotBeReauthorized(t *testing.T) {
	savedVendors, savedIDs := reviewedAliasVendors, lunaRouteReviewedIDs
	t.Cleanup(func() { reviewedAliasVendors, lunaRouteReviewedIDs = savedVendors, savedIDs })
	for _, id := range []string{"claude-sonnet-5-lr1", "claude-opus-lr29", "claude-haiku-lr-next"} {
		reviewedAliasVendors = map[string]map[string]string{"lunaroute": {id: "anthropic"}}
		lunaRouteReviewedIDs = map[string]lunaRouteFixedID{id: {Vendor: "anthropic", FixedTarget: "alias:" + id, Evidence: "fixture#revoked"}}
		assert.Equal(t, FamilyUnresolved, ClassifyFamily("lunaroute", id))
		assert.Equal(t, FamilyUnresolved, ClassifyFamily("", "anthropic/"+id))
		_, err := resolveLunaRoute(id, "")
		assert.ErrorIs(t, err, ErrSpecificVersionUnavailable)
		assert.False(t, LunaRouteExecutedModelAccepted(id, "claude-sonnet-5-6"))
	}
}
