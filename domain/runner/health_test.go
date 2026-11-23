package runner_test

import (
	"encoding/json"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSourceHealth_RevisionRoundtrip(t *testing.T) {
	source := runner.Source{ID: "builtin/lunaroute/prod", Health: runner.SourceHealth{Revision: 42, ConfirmationModel: "gpt-5.1", Capacity: runner.CapacityExhausted}}
	data, err := json.Marshal(source)
	require.NoError(t, err)
	var restored runner.Source
	require.NoError(t, json.Unmarshal(data, &restored))
	assert.Equal(t, source, restored)
	assert.Equal(t, uint64(42), restored.Health.Revision)
	assert.Zero(t, runner.SourceHealth{}.Revision)
}
