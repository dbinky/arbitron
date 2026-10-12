package http_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

func TestInstanceListREST_SerializesBindingAndGitRef(t *testing.T) {
	row := instance.EnrichedInstanceSummary{
		InstanceSummary: instance.InstanceSummary{ID: uuid.New(), State: instance.StateRunning},
		Model:           "gpt-6.1-sol", Effort: "high", Backend: "codex", Provider: "litellm",
		NodeType: "agent_call", GitRef: "feat/a",
	}
	h, _ := newParentListHandler([]instance.EnrichedInstanceSummary{row})
	code, rows := doList(t, h, "")
	require.Equal(t, http.StatusOK, code)
	require.Len(t, rows, 1)
	assert.Equal(t, "gpt-6.1-sol", rows[0]["model"])
	assert.Equal(t, "high", rows[0]["effort"])
	assert.Equal(t, "codex", rows[0]["backend"])
	assert.Equal(t, "litellm", rows[0]["provider"])
	assert.Equal(t, "agent_call", rows[0]["nodeType"])
	assert.Equal(t, "feat/a", rows[0]["gitRef"])
}

func TestInstanceListREST_OmitsEmptyBindingAndGitRef(t *testing.T) {
	row := instance.EnrichedInstanceSummary{InstanceSummary: instance.InstanceSummary{ID: uuid.New(), State: instance.StateCompleted}}
	h, _ := newParentListHandler([]instance.EnrichedInstanceSummary{row})
	_, rows := doList(t, h, "")
	require.Len(t, rows, 1)
	for _, k := range []string{"effort", "provider", "nodeType", "gitRef"} {
		_, present := rows[0][k]
		assert.False(t, present, "%s should be omitted when empty", k)
	}
}
