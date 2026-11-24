package sources

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClaudeCodeTools_PinnedVocabulary(t *testing.T) {
	assert.Equal(t, []string{"Bash", "Edit", "Glob", "Grep", "LS", "Read", "WebFetch", "WebSearch", "Write"}, ClaudeCodeTools)
	assert.True(t, slices.IsSorted(ClaudeCodeTools))
	assert.Subset(t, ClaudeCodeTools, []string{"Read", "Grep"})
	for _, tool := range ClaudeCodeTools {
		assert.NotContains(t, tool, "(", "patterns are request-side, never advertised")
	}
}
