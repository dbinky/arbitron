package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type terminalRunner struct {
	chunks []engine.ChatChunk
	reaped chan struct{}
}

func (r terminalRunner) Run(ctx context.Context, _ Assignment, _ StepJob) (StepOutput, error) {
	ch := make(chan engine.ChatChunk)
	go func() {
		defer close(ch)
		defer close(r.reaped)
		for _, c := range r.chunks {
			select {
			case ch <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return StepOutput{Stream: ch}, nil
}
func TestHealthReportingRunnerSet_TerminalFailuresAndReaping(t *testing.T) {
	for name, chunks := range map[string][]engine.ChatChunk{
		"eof":           {{Token: "partial"}},
		"error":         {{Token: "partial"}, {Error: errors.New("failed"), Done: true}},
		"terminal-tail": {{Done: true}, {Token: "trailing"}},
	} {
		t.Run(name, func(t *testing.T) {
			reaped := make(chan struct{})
			h := &hrTransitioner{}
			r, _ := NewHealthReportingRunnerSet(StaticRunnerSet{BuiltInRunnerID: terminalRunner{chunks: chunks, reaped: reaped}}, h).For(BuiltInRunnerID)
			out, err := r.Run(context.Background(), Assignment{Job: Job{SourceID: "source"}}, StepJob{Op: OpStream})
			require.NoError(t, err)
			for range out.Stream {
			}
			select {
			case <-reaped:
			case <-time.After(time.Second):
				t.Fatal("upstream not reaped")
			}
			if name != "terminal-tail" {
				h.mu.Lock()
				assert.Empty(t, h.got)
				h.mu.Unlock()
			}
		})
	}
}
