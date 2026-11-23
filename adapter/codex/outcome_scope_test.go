package codex

import (
	"context"
	"errors"
	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	pool "github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rolloutWritingRunner struct {
	runner *multiRunner
	write  func()
}

func (r *rolloutWritingRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	if len(spec.Args) > 0 && spec.Args[0] == "exec" {
		r.write()
	}
	return r.runner.Run(ctx, spec)
}
func TestExecSession_ResumeOutcomeSink(t *testing.T) {
	for _, appendCurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-current-evidence", true: "current-logged-out"}[appendCurrent], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			installRollout(t, home, fixtureThread, "rollout_usage_limit_u2019.jsonl")
			r := &rolloutWritingRunner{runner: &multiRunner{scripts: []runScript{compatibleProbe(), {lines: outcomeLines(t, "exec_usage_limit.jsonl"), waitErr: errors.New("exit")}}}, write: func() {
				if !appendCurrent {
					return
				}
				path, err := findRollout(home, fixtureThread)
				require.NoError(t, err)
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				require.NoError(t, err)
				_, err = f.WriteString(currentStart + `{"type":"event_msg","payload":{"type":"task_complete","error":{"codex_error_info":{"http_connection_failed":{"http_status_code":401}}}}}` + "\n")
				require.NoError(t, err)
				require.NoError(t, f.Close())
			}}
			c := newCore(Config{BinaryPath: "codex", Runner: r})
			stream, err := newExecSession(&c).Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "continue", WorkDir: t.TempDir(), ResumeID: fixtureThread})
			require.NoError(t, err)
			events, err := drainStream(t, context.Background(), stream)
			require.NoError(t, err)
			require.NotEmpty(t, events)
			last := events[len(events)-1]
			require.Equal(t, turnFailed, last.Kind)
			want := pool.OutcomeOther
			if appendCurrent {
				want = pool.OutcomeLoggedOut
			}
			assert.Equal(t, want, pool.ClassifyError(last.Err).Class)
		})
	}
}

func TestRunAgent_ZeroExitFailedOutcome(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		installRollout(t, home, fixtureThread, "rollout_unauthorized.jsonl")
		b := codexBinding("gpt-5", nil)
		b.Pinned = pinned
		mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: outcomeLines(t, "exec_usage_limit.jsonl")}}}
		_, err := New(Config{Binding: b, Runner: mr}).RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: t.TempDir()})
		require.Error(t, err)
		assert.Equal(t, pool.OutcomeLoggedOut, pool.ClassifyError(err).Class)
	}
}

func TestOutcomeResetBounds(t *testing.T) {
	for _, v := range []float64{math.Inf(1), math.NaN(), 1e30} {
		assert.Nil(t, normalizeResetEpoch(&v))
	}
	for _, raw := range []string{
		`{"primary":{"used_percent":100,"window_minutes":-1,"resets_at":1795262400}}`,
		`{"primary":{"used_percent":100,"window_minutes":300,"resets_in_seconds":1e30}}`,
	} {
		snap, ok := decodeRolloutRateLimits([]byte(raw), time.Now())
		assert.False(t, ok)
		assert.Empty(t, snap.windows)
	}
}

const oldExhaustion = `{"type":"event_msg","payload":{"type":"error","codex_error_info":"usage_limit_exceeded"}}` + "\n"
const currentStart = `{"type":"event_msg","payload":{"type":"task_started","turn_id":"current"}}` + "\n"

func TestReadRolloutOutcome_OldExhaustionThenCurrentOther(t *testing.T) {
	o, ok, err := readRolloutOutcome(strings.NewReader(oldExhaustion+currentStart+`{"type":"event_msg","payload":{"type":"error","codex_error_info":"context_window_exceeded"}}`), time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, pool.OutcomeOther, o.Class)
}
func TestReadRolloutOutcome_CurrentUnknownErrorClearsPriorClass(t *testing.T) {
	o, ok, err := readRolloutOutcome(strings.NewReader(oldExhaustion+`{"type":"event_msg","payload":{"type":"error","message":"unknown"}}`), time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, pool.OutcomeOther, o.Class)
}
func TestReadRolloutOutcome_TruncatedCurrentTurnFailsClosed(t *testing.T) {
	_, ok, err := readRolloutOutcome(strings.NewReader(oldExhaustion+currentStart), time.Now())
	require.NoError(t, err)
	assert.False(t, ok)
}
func TestReadRolloutOutcome_CurrentWindowScope(t *testing.T) {
	prior := string(outcomeFixture(t, "rollout_usage_limit_u2019.jsonl"))
	o, ok, err := readRolloutOutcome(strings.NewReader(prior+currentStart+oldExhaustion), time.Now())
	require.NoError(t, err)
	require.True(t, ok)
	assert.Nil(t, o.ResetAt)
	assert.Empty(t, o.Windows)
}
func TestClassifiesRollout_NoConnections(t *testing.T) {
	assert.False(t, classifiesRollout(provider.ResolvedBinding{Provider: "openai", ConnectionRef: "account"}))
}
func TestResumeOutcomeOnlyReadsNewBytes(t *testing.T) {
	home := t.TempDir()
	installRollout(t, home, fixtureThread, "rollout_usage_limit_u2019.jsonl")
	classify := rolloutOutcomeAttacher([]string{"CODEX_HOME=" + home}, fixtureThread)
	base := errors.New("exit")
	assert.Same(t, base, classify(fixtureThread, base), "old exhaustion must not classify a new failed resume")
	path, err := findRollout(home, fixtureThread)
	require.NoError(t, err)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(currentStart + `{"type":"event_msg","payload":{"type":"error","codex_error_info":"unauthorized"}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	assert.Equal(t, pool.OutcomeLoggedOut, pool.ClassifyError(classify(fixtureThread, base)).Class)
}
