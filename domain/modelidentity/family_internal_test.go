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
