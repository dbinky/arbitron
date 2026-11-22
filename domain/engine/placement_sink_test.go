package engine_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlacementSink_RecordsInOrderAndReturnsCopy(t *testing.T) {
	s := engine.NewPlacementSink()
	assert.Empty(t, s.Facts())
	a := engine.PlacementFact{JobID: uuid.New(), RunnerID: "builtin", SourceID: "builtin/anthropic_subscription/-", Reason: "rank", Generation: 1, Outcome: "exhausted", At: time.Unix(10, 0)}
	b := a
	b.Generation, b.Outcome = 3, "completed"
	s.Record(a)
	s.Record(b)
	facts := s.Facts()
	require.Equal(t, []engine.PlacementFact{a, b}, facts)
	facts[0].RunnerID = "mutated"
	assert.Equal(t, "builtin", s.Facts()[0].RunnerID, "Facts returns a copy")
}

func TestPlacementSink_ConcurrentRecord(t *testing.T) {
	s := engine.NewPlacementSink()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Record(engine.PlacementFact{Outcome: "completed"}) }()
	}
	wg.Wait()
	assert.Len(t, s.Facts(), 50)
}

func TestPlacementSink_ContextRoundTrip(t *testing.T) {
	_, ok := engine.PlacementSinkFromContext(context.Background())
	assert.False(t, ok)
	s := engine.NewPlacementSink()
	got, ok := engine.PlacementSinkFromContext(engine.ContextWithPlacementSink(context.Background(), s))
	require.True(t, ok)
	assert.Same(t, s, got)
}

func TestPlacementFact_JSONIsCamelCase(t *testing.T) {
	raw, err := json.Marshal(engine.PlacementFact{RunnerID: "r", Reason: "x", Generation: 2, Outcome: "completed"})
	require.NoError(t, err)
	for _, key := range []string{`"jobId"`, `"runnerId"`, `"reason"`, `"generation"`, `"outcome"`, `"at"`} {
		assert.Contains(t, string(raw), key)
	}
	assert.NotContains(t, string(raw), `"sourceId"`, "empty sourceId is omitted")
}
