package opencode

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Keep the run/export schema fixture, substituting only subprocess timing.
type exportLifecycleRunner struct {
	*probeRunner
	runCtx     context.Context
	exportCtx  context.Context
	exportWait func(context.Context) error
}

func (r *exportLifecycleRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	lines, wait, err := r.probeRunner.Run(ctx, spec)
	switch spec.Args[0] {
	case "run":
		r.runCtx = ctx
	case "export":
		r.exportCtx = ctx
		return lines, func() error {
			if err := wait(); err != nil {
				return err
			}
			return r.exportWait(ctx)
		}, err
	}
	return lines, wait, err
}

// callMission runs a pinned mission, the only OpenCode entry that launches and
// reads a run's export (the discovery probe fails closed, G46).
func callMission(ctx context.Context, p *Provider) (string, error) {
	result, err := p.RunAgent(ctx, engine.AgentMission{Prompt: "say ok", WorkDir: "/tmp/probe", Model: p.binding.Model})
	if err != nil {
		return "", err
	}
	return result.Model, nil
}

// Passing runCtx to the export incorrectly rejects a successful run when its
// mission timer expires during the subsequent evidence read.
func TestModelProbeOpenCodeExportOutlivesMissionTimeout(t *testing.T) {
	r := &exportLifecycleRunner{probeRunner: exactProbeRunner()}
	r.exportWait = func(ctx context.Context) error {
		<-r.runCtx.Done() // The subprocess already exited successfully.
		return ctx.Err()
	}
	p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r, Timeout: 10 * time.Millisecond})
	got, err := callMission(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", got)
	require.NotNil(t, r.exportCtx)
	deadline, ok := r.exportCtx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(30*time.Second), deadline, time.Second)
	assert.ErrorIs(t, r.exportCtx.Err(), context.Canceled, "export resources must be released after verification")
}

// An independent mission budget must still preserve caller cancellation and
// caller deadlines, even when the export contains perfectly matching proof.
func TestModelProbeOpenCodeExportHonorsCallerContext(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			wantErr := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
				wantErr = context.DeadlineExceeded
			}
			defer cancel()
			r := &exportLifecycleRunner{probeRunner: exactProbeRunner()}
			r.exportWait = func(exportCtx context.Context) error {
				if !deadline {
					cancel()
				}
				select {
				case <-exportCtx.Done():
					return exportCtx.Err()
				case <-time.After(time.Second):
					t.Error("export lost caller cancellation/deadline")
					return nil
				}
			}
			p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r})
			got, err := callMission(ctx, p)
			require.ErrorIs(t, err, ErrModelExecutionEvidence)
			assert.ErrorIs(t, err, wantErr)
			assert.Empty(t, got, "canceled evidence must never accept a result")
		})
	}
}
