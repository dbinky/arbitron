package modelidentity

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReviewedAliasVendors(t *testing.T) {
	assert.Equal(t, map[string]string{"claude-sonnet-5-lr1": "anthropic"}, reviewedAliasVendors["lunaroute"], "LR-03 decision: exactly the reviewed release alias")
	saved := reviewedAliasVendors
	t.Cleanup(func() { reviewedAliasVendors = saved })
	reviewedAliasVendors = map[string]map[string]string{"lunaroute": {"muse-spark-open": "meta"}}
	assert.Equal(t, Family("meta"), ClassifyFamily("lunaroute", "muse-spark-open"))
	assert.Equal(t, Family("meta"), ClassifyFamily(" LunaRoute ", "Muse-Spark-Open"))
	assert.Equal(t, Family("meta"), ClassifyFamily("", "muse-spark-open"), "samples consume the same reviewed table")
	assert.Equal(t, FamilyUnresolved, ClassifyFamily("", "claude-sonnet-5-lr1"), "samples cannot infer an alias removed from the reviewed table")
	assert.Equal(t, FamilyUnresolved, ClassifyFamily("openrouter", "muse-spark-open"), "mappings are per route")
}
