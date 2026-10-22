package layout

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBlockingCount(t *testing.T) {
	fs := []Finding{
		{Criterion: CriterionNodeOverlap, Severity: SeverityBlocking},
		{Criterion: CriterionCrossings, Severity: SeverityAdvisory},
		{Criterion: CriterionTextClipped, Severity: SeverityBlocking},
	}
	assert.Equal(t, 2, BlockingCount(fs))
	assert.Equal(t, 0, BlockingCount(nil))
}

func TestScoreOf(t *testing.T) {
	fs := []Finding{
		{Criterion: CriterionNodeOverlap, Severity: SeverityBlocking},
		{Criterion: CriterionCrossings, Severity: SeverityAdvisory},
		{Criterion: CriterionIndistinctBranch, Severity: SeverityAdvisory},
	}
	assert.Equal(t, Score{Blocking: 1, Advisory: 1, Crossings: 4, Attempt: 2}, ScoreOf(2, fs, 4),
		"the crossings finding is represented by the crossing count, not double-counted as advisory")
}

func TestScore_LessIsLexicographic(t *testing.T) {
	passCompactLater := Score{Blocking: 0, Advisory: 5, Crossings: 9, Attempt: 3}
	blockedCompact := Score{Blocking: 1, Advisory: 0, Crossings: 0, Attempt: 1}
	assert.True(t, passCompactLater.Less(blockedCompact), "a layout with unresolved blocking issues never beats one without")

	fewerCrossings := Score{Blocking: 0, Advisory: 0, Crossings: 2, Attempt: 4}
	moreCrossings := Score{Blocking: 0, Advisory: 0, Crossings: 3, Attempt: 1}
	assert.True(t, fewerCrossings.Less(moreCrossings))

	earlier := Score{Attempt: 1}
	later := Score{Attempt: 2}
	assert.True(t, earlier.Less(later), "ties break by deterministic attempt order")
	assert.False(t, later.Less(earlier))
	assert.False(t, earlier.Less(earlier))
}

func TestFinding_JSON(t *testing.T) {
	f := Finding{Criterion: CriterionMergedLines, Severity: SeverityBlocking, Source: SourceReview, NodeIDs: []string{"a"}, EdgeOrdinals: []int{0, 3}, Evidence: "x"}
	raw, err := json.Marshal(f)
	require.NoError(t, err)
	assert.JSONEq(t, `{"criterion":"merged_lines","severity":"blocking","source":"review","nodeIds":["a"],"edgeOrdinals":[0,3],"evidence":"x"}`, string(raw))
}
