package instance_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestDeriveRunName(t *testing.T) {
	wfName := "feature-pipeline"
	id := uuid.MustParse("12345678-1234-5678-1234-567812345678")

	t.Run("title variable wins", func(t *testing.T) {
		vars := map[string]any{"title": "Refactor checkout flow"}
		inputs := []instance.RunNameInput{{Name: "title"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "Refactor checkout flow — feature-pipeline", got)
	})

	t.Run("name variable when no title", func(t *testing.T) {
		vars := map[string]any{"name": "nightly-backfill"}
		inputs := []instance.RunNameInput{{Name: "name"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "nightly-backfill — feature-pipeline", got)
	})

	t.Run("slug variable when no title or name", func(t *testing.T) {
		vars := map[string]any{"slug": "user-reports-q2"}
		inputs := []instance.RunNameInput{{Name: "slug"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "user-reports-q2 — feature-pipeline", got)
	})

	t.Run("first required input when no title/name/slug", func(t *testing.T) {
		vars := map[string]any{"feature": "dark-mode", "region": "eu"}
		inputs := []instance.RunNameInput{
			{Name: "region"},
			{Name: "feature", Required: true},
		}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "dark-mode — feature-pipeline", got)
	})

	t.Run("first non-empty input when no required inputs", func(t *testing.T) {
		vars := map[string]any{"region": "eu", "env": "staging"}
		inputs := []instance.RunNameInput{
			{Name: "region"},
			{Name: "env"},
		}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "eu — feature-pipeline", got)
	})

	t.Run("short id when no usable variables", func(t *testing.T) {
		vars := map[string]any{}
		inputs := []instance.RunNameInput{}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "feature-pipeline 12345678", got)
	})

	t.Run("short id when variables are non-string", func(t *testing.T) {
		vars := map[string]any{"count": 42, "enabled": true}
		inputs := []instance.RunNameInput{{Name: "count"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.Equal(t, "feature-pipeline 12345678", got)
	})

	t.Run("empty workflow name still yields short id", func(t *testing.T) {
		got := instance.DeriveRunName("", map[string]any{}, nil, id)
		assert.Equal(t, "12345678", got)
	})

	t.Run("very long title is truncated", func(t *testing.T) {
		longTitle := string([]byte{0x61})
		for i := 0; i < 200; i++ {
			longTitle += "a"
		}
		vars := map[string]any{"title": longTitle}
		inputs := []instance.RunNameInput{{Name: "title"}}
		got := instance.DeriveRunName(wfName, vars, inputs, id)
		assert.LessOrEqual(t, len(got), 80, "long titles are truncated")
		assert.Contains(t, got, "feature-pipeline")
	})
}

func TestEnrichedInstanceSummary_EmbedsSummary(t *testing.T) {
	sum := instance.InstanceSummary{
		ID:           uuid.New(),
		WorkflowName: "wf",
		State:        instance.StateRunning,
		Priority:     instance.PriorityNormal,
	}
	enr := instance.EnrichedInstanceSummary{
		InstanceSummary: sum,
		RunName:         "my-run — wf",
		Submitter:       instance.Submitter{Name: "alice", AccountIdentity: "alice@example.com"},
		FriendlyStep:    "Generate draft",
		Model:           "gpt-4o",
		Backend:         "claude-code",
		RetryCount:      2,
		QueuePosition:   3,
	}
	assert.Equal(t, sum.ID, enr.ID, "embedded summary fields are accessible")
	assert.Equal(t, "my-run — wf", enr.RunName)
	assert.Equal(t, "alice", enr.Submitter.Name)
	assert.Equal(t, "alice@example.com", enr.Submitter.AccountIdentity)
	assert.Equal(t, "Generate draft", enr.FriendlyStep)
	assert.Equal(t, "gpt-4o", enr.Model)
	assert.Equal(t, "claude-code", enr.Backend)
	assert.Equal(t, 2, enr.RetryCount)
	assert.Equal(t, 3, enr.QueuePosition)
}

func TestEnrichedInstanceSummary_ZeroValueDegrades(t *testing.T) {
	var enr instance.EnrichedInstanceSummary
	assert.Equal(t, "", enr.RunName, "missing run name is empty, not nil")
	assert.Equal(t, 0, enr.QueuePosition, "non-queued rows carry position 0")
	assert.Equal(t, "", enr.Submitter.Name)
}
