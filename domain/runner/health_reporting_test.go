package runner

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type hrTransitioner struct {
	mu  sync.Mutex
	got map[SourceID][]SourceOutcome
}

func (h *hrTransitioner) ApplyOutcome(_ context.Context, id SourceID, o SourceOutcome) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.got == nil {
		h.got = map[SourceID][]SourceOutcome{}
	}
	h.got[id] = append(h.got[id], o)
	return nil
}

// hrRunner behaves per op and reports a headroom hint through the observer.
type hrRunner struct{ err error }

func (r hrRunner) Run(ctx context.Context, a Assignment, job StepJob) (StepOutput, error) {
	if r.err != nil {
		return StepOutput{}, r.err
	}
	hint := 0.3
	ObserveOutcome(ctx, SourceOutcome{HeadroomHint: &hint})
	switch job.Op {
	case OpType("stream"):
		ch := make(chan engine.ChatChunk, 2)
		ch <- engine.ChatChunk{Token: "t"}
		ch <- engine.ChatChunk{Done: true}
		close(ch)
		return StepOutput{Stream: ch}, nil
	case OpType("process"):
		return StepOutput{Process: &engine.ProcessResult{}}, nil
	}
	return StepOutput{Chat: &engine.ChatResponse{}}, nil
}

func hrAssign(src SourceID) Assignment {
	return Assignment{Job: Job{RunnerID: BuiltInRunnerID, SourceID: src}}
}

func TestHealthReportingRunnerSet_SuccessReportsNoneWithHint(t *testing.T) {
	h := &hrTransitioner{}
	set := NewHealthReportingRunnerSet(StaticRunnerSet{BuiltInRunnerID: hrRunner{}}, h)
	r, ok := set.For(BuiltInRunnerID)
	require.True(t, ok)
	_, err := r.Run(context.Background(), hrAssign("builtin/anthropic_subscription/-"), StepJob{Op: OpType("chat")})
	require.NoError(t, err)
	got := h.got["builtin/anthropic_subscription/-"]
	require.Len(t, got, 1)
	assert.Equal(t, OutcomeNone, got[0].Class)
	assert.InDelta(t, 0.3, *got[0].HeadroomHint, 1e-9)
}

func TestHealthReportingRunnerSet_StreamReportsAfterDone(t *testing.T) {
	h := &hrTransitioner{}
	r, _ := NewHealthReportingRunnerSet(StaticRunnerSet{BuiltInRunnerID: hrRunner{}}, h).For(BuiltInRunnerID)
	out, err := r.Run(context.Background(), hrAssign("s"), StepJob{Op: OpType("stream")})
	require.NoError(t, err)
	h.mu.Lock()
	assert.Empty(t, h.got["s"], "not before the terminal chunk")
	h.mu.Unlock()
	var n int
	for range out.Stream {
		n++
	}
	assert.Equal(t, 2, n, "chunks forwarded unchanged")
	h.mu.Lock()
	defer h.mu.Unlock()
	assert.Len(t, h.got["s"], 1)
}

func TestHealthReportingRunnerSet_FailuresAndProcessNotReported(t *testing.T) {
	h := &hrTransitioner{}
	r, _ := NewHealthReportingRunnerSet(StaticRunnerSet{BuiltInRunnerID: hrRunner{err: errors.New("boom")}}, h).For(BuiltInRunnerID)
	_, err := r.Run(context.Background(), hrAssign("s"), StepJob{Op: OpType("chat")})
	require.Error(t, err)
	r2, _ := NewHealthReportingRunnerSet(StaticRunnerSet{BuiltInRunnerID: hrRunner{}}, h).For(BuiltInRunnerID)
	_, err = r2.Run(context.Background(), hrAssign("builtin/none/-"), StepJob{Op: OpType("process")})
	require.NoError(t, err)
	assert.Empty(t, h.got, "failures belong to the dispatcher; process slots have no source health")
}

func TestHealthReportingRunnerSet_UnknownRunnerAndNilDeps(t *testing.T) {
	set := NewHealthReportingRunnerSet(StaticRunnerSet{}, &hrTransitioner{})
	_, ok := set.For("laptop")
	assert.False(t, ok)
	assert.Panics(t, func() { NewHealthReportingRunnerSet(nil, &hrTransitioner{}) })
	assert.Panics(t, func() { NewHealthReportingRunnerSet(StaticRunnerSet{}, nil) })
}
