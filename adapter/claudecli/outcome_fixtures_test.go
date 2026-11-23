package claudecli

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	pool "github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fixtureLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			lines = append(lines, sc.Text())
		}
	}
	require.NoError(t, sc.Err())
	return lines
}

var fixtureReset = time.Unix(1795262400, 0).UTC()

type fixtureCase struct {
	file  string
	class pool.OutcomeClass // OutcomeNone = turn succeeds
	reset *time.Time
	hint  *float64
}

func claudeFixtureCases() []fixtureCase {
	h15 := 0.15
	return []fixtureCase{
		{"authentication_failed.jsonl", pool.OutcomeLoggedOut, nil, nil},
		{"invalid_api_key_401.jsonl", pool.OutcomeLoggedOut, nil, nil},
		{"overloaded_529.jsonl", pool.OutcomeTransient, nil, nil},
		{"server_throttle_429.jsonl", pool.OutcomeTransient, nil, nil},
		{"usage_limit_seven_day.jsonl", pool.OutcomeExhausted, &fixtureReset, nil},
		{"usage_limit_five_hour_ms.jsonl", pool.OutcomeExhausted, &fixtureReset, nil},
		{"allowed_warning.jsonl", pool.OutcomeNone, nil, &h15},
		{"allowed_warning_no_utilization.jsonl", pool.OutcomeNone, nil, nil},
		{"malformed_fields.jsonl", pool.OutcomeOther, nil, nil},
		{"malformed_line.jsonl", pool.OutcomeOther, nil, nil},
	}
}

func assertOutcome(t *testing.T, tc fixtureCase, err error, seen []pool.SourceOutcome) {
	t.Helper()
	if tc.class == pool.OutcomeNone {
		require.NoError(t, err)
		require.Len(t, seen, 1)
		if tc.hint == nil {
			assert.Nil(t, seen[0].HeadroomHint)
		} else {
			require.NotNil(t, seen[0].HeadroomHint)
			assert.InDelta(t, *tc.hint, *seen[0].HeadroomHint, 1e-9)
		}
		return
	}
	require.Error(t, err)
	o := pool.ClassifyError(err)
	assert.Equal(t, tc.class, o.Class)
	if tc.reset != nil {
		require.NotNil(t, o.ResetAt)
		assert.True(t, tc.reset.Equal(*o.ResetAt), "reset normalized to %s, got %s", tc.reset, o.ResetAt)
	}
	assert.Empty(t, seen, "failed turns never report success hints")
}

// Collect path (agent/chat non-streaming), unverified: plain errors with outcome.
func TestClaudeOutcomeFixtures_CollectPath(t *testing.T) {
	for _, tc := range claudeFixtureCases() {
		t.Run(tc.file, func(t *testing.T) {
			var seen []pool.SourceOutcome
			ctx := pool.ContextWithOutcomeObserver(context.Background(), func(o pool.SourceOutcome) { seen = append(seen, o) })
			fr := &fakeRunner{lines: fixtureLines(t, filepath.Join("testdata", "outcomes", tc.file))}
			_, _, err := collectResultWithPolicy(ctx, fr, RunSpec{}, "", collectPolicy{})
			assertOutcome(t, tc, err, seen)
		})
	}
}

// Stream path (interactive / chat streaming): the terminal chunk carries the outcome.
func TestClaudeOutcomeFixtures_StreamPath(t *testing.T) {
	for _, tc := range claudeFixtureCases() {
		t.Run(tc.file, func(t *testing.T) {
			var seen []pool.SourceOutcome
			ctx := pool.ContextWithOutcomeObserver(context.Background(), func(o pool.SourceOutcome) { seen = append(seen, o) })
			st := streamState{ctx: ctx}
			var last engine.ChatChunk
			send := func(c engine.ChatChunk) bool { last = c; return true }
			for _, l := range fixtureLines(t, filepath.Join("testdata", "outcomes", tc.file)) {
				st.Handle(l, send)
			}
			st.Finalize(nil, send)
			assertOutcome(t, tc, last.Error, seen)
		})
	}
}

// Verified (pinned) path with a non-zero exit: the error is sanitized yet the
// outcome survives via errors.As, and the raw stderr never surfaces.
func TestClaudeOutcome_SurvivesSanitizationOnExit(t *testing.T) {
	lines := fixtureLines(t, filepath.Join("testdata", "outcomes", "usage_limit_seven_day.jsonl"))
	raw := &StderrError{Err: errors.New("exit status 1"), Summary: "token=sk-secret", Tail: []byte("sk-secret")}
	fr := &fakeRunner{lines: lines[:len(lines)-1], waitErr: raw}

	_, _, err := collectResultWithPolicy(context.Background(), fr, RunSpec{}, "claude-sonnet-4-5", collectPolicy{})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "sk-secret")
	assert.Contains(t, err.Error(), "harness process failed")

	var oe *pool.OutcomeError
	require.True(t, errors.As(err, &oe))
	assert.Equal(t, pool.OutcomeExhausted, oe.Outcome.Class)
	assert.Equal(t, "harness process failed", oe.Err.Error(), "the outcome copy wraps only the sanitized error")
	var se *StderrError
	assert.False(t, errors.As(err, &se), "raw stderr stays unreachable")
	assert.Equal(t, pool.OutcomeExhausted, pool.ClassifyError(err).Class)

	// Same through the stream Finalize path.
	st := streamState{wantModel: "claude-sonnet-4-5"}
	var last engine.ChatChunk
	for _, l := range lines[:len(lines)-1] {
		st.Handle(l, func(c engine.ChatChunk) bool { last = c; return true })
	}
	st.Finalize(raw, func(c engine.ChatChunk) bool { last = c; return true })
	require.Error(t, last.Error)
	assert.NotContains(t, last.Error.Error(), "sk-secret")
	assert.Equal(t, pool.OutcomeExhausted, pool.ClassifyError(last.Error).Class)
}

// A sanitized error without an outcome still refuses As (no regression).
func TestSanitizeProcessError_AsWithoutOutcome(t *testing.T) {
	err := SanitizeProcessError(&StderrError{Err: errors.New("exit 1"), Tail: []byte("secret")})
	var oe *pool.OutcomeError
	assert.False(t, errors.As(err, &oe))
	var se *StderrError
	assert.False(t, errors.As(err, &se))
	assert.Equal(t, pool.OutcomeOther, pool.ClassifyError(err).Class)
}
