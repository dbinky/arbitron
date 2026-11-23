package codex

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	pool "github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fixtureThread = "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

var fixtureReset = time.Unix(1795262400, 0).UTC()

func outcomeFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "outcomes", name))
	require.NoError(t, err)
	return b
}

func outcomeLines(t *testing.T, name string) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(outcomeFixture(t, name)))
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			out = append(out, sc.Text())
		}
	}
	return out
}

// installRollout places a fixture where findRollout looks for threadID.
func installRollout(t *testing.T, home, threadID, fixture string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "11", "20")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rollout-2026-11-20T15-04-05-"+threadID+".jsonl"), outcomeFixture(t, fixture), 0o600))
}

func readFixtureOutcome(t *testing.T, name string) (pool.SourceOutcome, bool, error) {
	return readRolloutOutcome(bytes.NewReader(outcomeFixture(t, name)), time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC))
}

func TestReadRolloutOutcome_Classes(t *testing.T) {
	cases := []struct {
		file  string
		class pool.OutcomeClass
		reset *time.Time
	}{
		{"rollout_usage_limit_u2019.jsonl", pool.OutcomeExhausted, &fixtureReset},
		{"rollout_unauthorized.jsonl", pool.OutcomeLoggedOut, nil},
		{"rollout_server_overloaded.jsonl", pool.OutcomeTransient, nil},
		{"rollout_rate_limit_exceeded.jsonl", pool.OutcomeTransient, nil},
		{"rollout_stream_disconnected.jsonl", pool.OutcomeTransient, nil},
		{"rollout_prose_mismatch.jsonl", pool.OutcomeTransient, nil},
		{"rollout_resets_in_seconds.jsonl", pool.OutcomeExhausted, &fixtureReset},
		{"rollout_unknown_info.jsonl", pool.OutcomeOther, nil},
		// Observed 0.171.0 shape and its status-code rule (DECISION RR-03, 2026-11-22).
		{"rollout_logged_out_401.jsonl", pool.OutcomeLoggedOut, nil},
		{"rollout_http_403.jsonl", pool.OutcomeLoggedOut, nil},
		{"rollout_http_502.jsonl", pool.OutcomeTransient, nil},
		{"rollout_error_then_task_complete.jsonl", pool.OutcomeLoggedOut, nil},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			o, ok, err := readFixtureOutcome(t, tc.file)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tc.class, o.Class)
			if tc.reset == nil {
				assert.Nil(t, o.ResetAt)
			} else {
				require.NotNil(t, o.ResetAt)
				assert.True(t, tc.reset.Equal(*o.ResetAt), "got %s", o.ResetAt)
			}
		})
	}
}

// Classification never reads prose: ASCII ' and U+2019 classify identically.
func TestReadRolloutOutcome_ApostropheVariantsIdentical(t *testing.T) {
	u, okU, errU := readFixtureOutcome(t, "rollout_usage_limit_u2019.jsonl")
	a, okA, errA := readFixtureOutcome(t, "rollout_usage_limit_ascii.jsonl")
	require.NoError(t, errU)
	require.NoError(t, errA)
	assert.True(t, okU && okA)
	assert.Equal(t, u, a)
	assert.Contains(t, string(outcomeFixture(t, "rollout_usage_limit_u2019.jsonl")), "You’ve")
	assert.Contains(t, string(outcomeFixture(t, "rollout_usage_limit_ascii.jsonl")), "You've")
}

// Windows are keyed by limit id + scope + duration: the per-model weekly bucket
// is not overwritten by the generic weekly bucket, and a later snapshot of the
// same key replaces the earlier one. Millisecond resets are normalized.
func TestReadRolloutOutcome_PerModelBucketsAndLatestWins(t *testing.T) {
	o, ok, err := readFixtureOutcome(t, "rollout_per_model_buckets.jsonl")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, pool.OutcomeExhausted, o.Class)
	require.Len(t, o.Windows, 2)
	byID := map[string]pool.LimitWindow{}
	for _, w := range o.Windows {
		byID[w.LimitID] = w
		assert.Equal(t, 10080, w.DurationMins)
	}
	assert.InDelta(t, 61.0, *byID["codex"].UsedPercent, 1e-9, "latest snapshot of the same key wins")
	assert.InDelta(t, 100.0, *byID["codex_pro"].UsedPercent, 1e-9)
	assert.Equal(t, "GPT-5.2 Pro", byID["codex_pro"].ModelScope)
	require.NotNil(t, o.ResetAt)
	assert.True(t, fixtureReset.Equal(*o.ResetAt), "reset comes from the exhausted (100%%) bucket, ms normalized")
}

func TestReadRolloutOutcome_MalformedFailsClosed(t *testing.T) {
	_, ok, err := readFixtureOutcome(t, "rollout_malformed.jsonl")
	assert.False(t, ok)
	assert.ErrorIs(t, err, errMalformedRolloutOutcome)
}

func TestReadRolloutOutcome_NoSignal(t *testing.T) {
	_, ok, err := readRolloutOutcome(strings.NewReader(`{"timestamp":"2026-11-20T15:04:05Z","type":"session_meta","payload":{"id":"x"}}`+"\n"), time.Now())
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestReadRolloutOutcome_RecordCapRefuses(t *testing.T) {
	huge := `{"type":"event_msg","payload":{"type":"error","message":"` + strings.Repeat("x", maxRolloutRecordBytes+1) + `"}}`
	_, ok, err := readRolloutOutcome(strings.NewReader(huge), time.Now())
	assert.False(t, ok)
	assert.ErrorIs(t, err, errMalformedRolloutOutcome)
}

func TestAttachRolloutOutcome(t *testing.T) {
	home := t.TempDir()
	installRollout(t, home, fixtureThread, "rollout_usage_limit_u2019.jsonl")
	env := []string{"CODEX_HOME=" + home}
	base := errors.New("exit status 1")

	err := attachRolloutOutcome(env, fixtureThread, base, time.Now())
	assert.ErrorIs(t, err, base)
	assert.Equal(t, pool.OutcomeExhausted, pool.ClassifyError(err).Class)

	// No thread, missing rollout, nil error: unchanged (=> other), never a panic.
	assert.Same(t, base, attachRolloutOutcome(env, "", base, time.Now()))
	assert.Same(t, base, attachRolloutOutcome(env, "0199ffff-ffff-7fff-8fff-ffffffffffff", base, time.Now()))
	assert.Nil(t, attachRolloutOutcome(env, fixtureThread, nil, time.Now()))
	assert.Same(t, base, attachRolloutOutcome(nil, fixtureThread, base, time.Now()), "no resolvable home")
}

func TestThreadIDFromLines(t *testing.T) {
	assert.Equal(t, fixtureThread, threadIDFromLines(outcomeLines(t, "exec_usage_limit.jsonl")))
	assert.Equal(t, "", threadIDFromLines([]string{`{"type":"turn.started"}`, "garbage"}))
}

func TestClassifiesRollout_NeverLunaRoute(t *testing.T) {
	assert.True(t, classifiesRollout(provider.ResolvedBinding{Provider: "openai"}))
	assert.False(t, classifiesRollout(provider.ResolvedBinding{Provider: provider.CanonicalLunaRoute}))
}

// Integration through Provider.RunAgent: an exhausted subscription turn carries
// the outcome even when sanitized (pinned), and the reset is the rollout's.
func TestRunAgent_AttachesRolloutOutcome(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unpinned", true: "pinned"}[pinned], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CODEX_HOME", home)
			installRollout(t, home, fixtureThread, "rollout_usage_limit_u2019.jsonl")
			mr := &multiRunner{scripts: []runScript{
				compatibleProbe(),
				{lines: outcomeLines(t, "exec_usage_limit.jsonl"), waitErr: &claudecli.StderrError{Err: errors.New("exit status 1"), Tail: []byte("secret-tail")}},
			}}
			b := codexBinding("gpt-5.2-codex", nil)
			b.Pinned = pinned
			_, err := New(Config{Binding: b, BinaryPath: "codex", Runner: mr}).RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: t.TempDir()})
			require.Error(t, err)
			o := pool.ClassifyError(err)
			assert.Equal(t, pool.OutcomeExhausted, o.Class)
			require.NotNil(t, o.ResetAt)
			assert.True(t, fixtureReset.Equal(*o.ResetAt))
			assert.NotContains(t, err.Error(), "secret-tail")
		})
	}
}

// Real 0.171.0 logged-out shape (docs/evidence/remote-runners/codex-logged-out.md):
// stdout is prose only and the rollout's task_complete carries the 401. Without
// the rollout the same prose never classifies.
func TestRunAgent_ObservedLoggedOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	installRollout(t, home, fixtureThread, "rollout_logged_out_401.jsonl")
	run := func() error {
		mr := &multiRunner{scripts: []runScript{
			compatibleProbe(),
			{lines: outcomeLines(t, "exec_stdout_logged_out.jsonl"), waitErr: errors.New("exit status 1")},
		}}
		_, err := New(Config{Binding: codexBinding("gpt-5.2-codex", nil), BinaryPath: "codex", Runner: mr}).RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: t.TempDir()})
		return err
	}
	err := run()
	require.Error(t, err)
	assert.Equal(t, pool.OutcomeLoggedOut, pool.ClassifyError(err).Class)
	assert.Equal(t, fixtureThread, threadIDFromLines(outcomeLines(t, "exec_stdout_logged_out.jsonl")))

	t.Setenv("CODEX_HOME", t.TempDir()) // no rollout: prose alone is never evidence
	err = run()
	require.Error(t, err)
	assert.Equal(t, pool.OutcomeOther, pool.ClassifyError(err).Class)
}

// StreamProvider Chat and StreamChat failure paths carry the outcome too.
func TestStreamProvider_AttachesRolloutOutcome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	installRollout(t, home, fixtureThread, "rollout_unauthorized.jsonl")

	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: outcomeLines(t, "exec_usage_limit.jsonl"), waitErr: errors.New("exit status 1")}}}
	_, err := NewStreamProvider(Config{Binding: codexBinding("gpt-5.2-codex", nil), BinaryPath: "codex", Runner: mr}).
		Chat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: t.TempDir()})
	require.Error(t, err)
	assert.Equal(t, pool.OutcomeLoggedOut, pool.ClassifyError(err).Class)

	// Exit 0 but a failed turn (turn.failed folded) on the stream path.
	mr = &multiRunner{scripts: []runScript{compatibleProbe(), {lines: outcomeLines(t, "exec_usage_limit.jsonl")}}}
	ch, err := NewStreamProvider(Config{Binding: codexBinding("gpt-5.2-codex", nil), BinaryPath: "codex", Runner: mr}).
		StreamChat(context.Background(), engine.ChatRequest{Prompt: "x", WorkDir: t.TempDir()})
	require.NoError(t, err)
	var last engine.ChatChunk
	for c := range ch {
		last = c
	}
	require.Error(t, last.Error)
	assert.Equal(t, pool.OutcomeLoggedOut, pool.ClassifyError(last.Error).Class)
}

// Interactive transport: foldExecStream applies the classifier on exit failure.
func TestFoldExecStream_Classifies(t *testing.T) {
	home := t.TempDir()
	installRollout(t, home, fixtureThread, "rollout_server_overloaded.jsonl")
	in := make(chan string)
	go func() {
		defer close(in)
		for _, l := range outcomeLines(t, "exec_usage_limit.jsonl") {
			in <- l
		}
	}()
	events := make(chan turnEvent, 16)
	classify := rolloutOutcomeAttacher([]string{"CODEX_HOME=" + home}, "")
	foldExecStream(context.Background(), in, func() error { return errors.New("exit status 1") }, events, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, classify)
	var last turnEvent
	for ev := range events {
		last = ev
	}
	require.Equal(t, turnFailed, last.Kind)
	assert.Equal(t, pool.OutcomeTransient, pool.ClassifyError(last.Err).Class)
}
