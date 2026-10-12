package engine

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
)

func TestNodeLabel(t *testing.T) {
	t.Run("uses Label when set", func(t *testing.T) {
		assert.Equal(t, "Architect R1", nodeLabel(workflow.Node{ID: "n1", Label: "Architect R1"}))
	})
	t.Run("falls back to node id when Label empty", func(t *testing.T) {
		assert.Equal(t, "n1", nodeLabel(workflow.Node{ID: "n1"}))
	})
}

func TestSnapshotGitVariable(t *testing.T) {
	assert.Equal(t, "pr_branch", snapshotGitVariable(map[string]any{"gitVariable": "pr_branch"}))
	assert.Equal(t, "", snapshotGitVariable(map[string]any{"gitVariable": 3}))
	assert.Equal(t, "", snapshotGitVariable(nil))
}
