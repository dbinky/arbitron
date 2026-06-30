package engine

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
)

func TestNodeLabelAndBackend(t *testing.T) {
	t.Run("label uses Label when set", func(t *testing.T) {
		n := workflow.Node{ID: "n1", Label: "Architect R1"}
		label, _ := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "Architect R1", label)
	})
	t.Run("label falls back to node id when Label empty", func(t *testing.T) {
		n := workflow.Node{ID: "n1"}
		label, _ := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "n1", label)
	})
	t.Run("backend uses node config when set", func(t *testing.T) {
		n := workflow.Node{ID: "n1", Config: workflow.NodeConfig{Backend: "codex"}}
		_, backend := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "codex", backend)
	})
	t.Run("backend falls back to default when unset", func(t *testing.T) {
		n := workflow.Node{ID: "n1"}
		_, backend := nodeLabelAndBackend(n, "claude-code")
		assert.Equal(t, "claude-code", backend)
	})
	t.Run("backend empty when unset and no default", func(t *testing.T) {
		n := workflow.Node{ID: "n1"}
		_, backend := nodeLabelAndBackend(n, "")
		assert.Equal(t, "", backend)
	})
}
