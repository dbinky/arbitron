package codex

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStdin struct {
	bytes.Buffer
	closed bool
}

func (w *fakeStdin) Close() error { w.closed = true; return nil }

// fakeAppServer replays stdout lines. Observed rule (codex-app-server.md): stdin
// EOF before the id-1 answer => the server exits 0 without answering id 1.
type fakeAppServer struct {
	stdin   fakeStdin
	lines   []string
	i       int
	buf     []byte
	hang    bool // never answer; only Kill unblocks the read
	once    sync.Once
	done    chan struct{}
	killed  atomic.Bool // Kill may run on RateLimitsRead's deadline goroutine
	waited  bool
	bin     string
	env     []string
	starts  int
	startEr error
}

func newFakeAppServer(lines []string) *fakeAppServer {
	return &fakeAppServer{lines: lines, done: make(chan struct{})}
}

func (f *fakeAppServer) start(_ context.Context, bin string, env []string) (appServerProc, error) {
	f.starts++
	f.bin, f.env = bin, env
	if f.startEr != nil {
		return nil, f.startEr
	}
	return f, nil
}
func (f *fakeAppServer) Stdin() io.WriteCloser { return &f.stdin }
func (f *fakeAppServer) Stdout() io.Reader     { return f }
func (f *fakeAppServer) Read(b []byte) (int, error) {
	if f.hang {
		<-f.done
		return 0, io.EOF
	}
	for len(f.buf) == 0 {
		if f.i >= len(f.lines) {
			return 0, io.EOF
		}
		l := f.lines[f.i]
		if strings.Contains(l, `"id":1`) && f.stdin.closed {
			return 0, io.EOF // exited on stdin EOF without answering id 1
		}
		f.i++
		f.buf = []byte(l + "\n")
	}
	n := copy(b, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}
func (f *fakeAppServer) Kill() error {
	f.killed.Store(true)
	f.once.Do(func() { close(f.done) })
	return nil
}
func (f *fakeAppServer) Wait() error {
	f.waited = true
	if f.killed.Load() {
		return errors.New("signal: killed") // irrelevant once answered
	}
	return nil
}

func readWith(t *testing.T, f *fakeAppServer) (RateLimitsSnapshot, error) {
	t.Helper()
	return RateLimitsRead(context.Background(), Config{BinaryPath: "/bin/codex", appServer: f.start})
}

// Observed 0.171.0 logged-in exchange: requests written once, stdin held open,
// notification skipped, integer usedPercent decoded, process stopped and reaped.
func TestRateLimitsRead_ObservedLoggedIn(t *testing.T) {
	f := newFakeAppServer(outcomeLines(t, "app_server_observed_logged_in.jsonl"))
	snap, err := readWith(t, f)
	require.NoError(t, err)
	require.NotNil(t, snap.OrdinaryUsageAllowed)
	assert.True(t, *snap.OrdinaryUsageAllowed)
	require.Len(t, snap.Windows, 1, "rateLimitsByLimitId wins over the duplicate rateLimits bucket")
	w := snap.Windows[0]
	assert.Equal(t, "codex", w.LimitID)
	assert.Equal(t, 10080, w.DurationMins)
	assert.InDelta(t, 30.0, *w.UsedPercent, 1e-9)
	assert.True(t, time.Unix(1795565327, 0).UTC().Equal(*w.ResetAt))

	assert.Equal(t, "/bin/codex", f.bin)
	in := f.stdin.String()
	assert.Equal(t, 3, strings.Count(in, "\n"), "initialize, initialized, account/rateLimits/read")
	assert.Contains(t, in, `"method":"initialize"`)
	assert.Contains(t, in, `"method":"initialized"`)
	assert.Contains(t, in, `"id":1,"method":"account/rateLimits/read"`)
	assert.NotContains(t, in, `"jsonrpc"`)
	for _, kv := range f.env {
		assert.False(t, strings.HasPrefix(kv, "OPENAI_API_KEY="), "never a key in the env")
	}
	assert.True(t, f.stdin.closed && f.killed.Load() && f.waited, "stdin closed, process stopped and reaped after the answer")
}

func TestRateLimitsRead_TriState(t *testing.T) {
	cases := []struct {
		file    string
		allowed *bool
		windows int
	}{
		{"app_server_observed_logged_in.jsonl", boolPtr(true), 1},
		{"app_server_allowed_false.jsonl", boolPtr(false), 1},
		{"app_server_allowed_null.jsonl", nil, 2},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			snap, err := readWith(t, newFakeAppServer(outcomeLines(t, tc.file)))
			require.NoError(t, err)
			if tc.allowed == nil {
				assert.Nil(t, snap.OrdinaryUsageAllowed, "null stays unknown regardless of percentages")
			} else {
				require.NotNil(t, snap.OrdinaryUsageAllowed)
				assert.Equal(t, *tc.allowed, *snap.OrdinaryUsageAllowed)
			}
			assert.Len(t, snap.Windows, tc.windows)
		})
	}
}

// Per-limit buckets are kept separately even with the same duration; ms resets normalize.
func TestRateLimitsRead_PerLimitBuckets(t *testing.T) {
	snap, err := readWith(t, newFakeAppServer(outcomeLines(t, "app_server_multi_limit.jsonl")))
	require.NoError(t, err)
	byID := map[string]float64{}
	for _, w := range snap.Windows {
		byID[w.LimitID] = *w.UsedPercent
		assert.Equal(t, 10080, w.DurationMins)
	}
	assert.Equal(t, map[string]float64{"codex": 42, "codex_pro": 100}, byID)

	snap, err = readWith(t, newFakeAppServer(outcomeLines(t, "app_server_allowed_null.jsonl")))
	require.NoError(t, err)
	for _, w := range snap.Windows {
		if w.DurationMins == 300 {
			assert.True(t, time.Unix(1795262400, 0).UTC().Equal(*w.ResetAt))
			assert.InDelta(t, 12.5, *w.UsedPercent, 1e-9)
		}
	}
}

// Every failure is capacity UNKNOWN (ErrRateLimitsUnavailable), never logged out
// and never available — including the observed -32600 logged-out answer.
func TestRateLimitsRead_Failures(t *testing.T) {
	for name, f := range map[string]*fakeAppServer{
		"observed logged out -32600": newFakeAppServer(outcomeLines(t, "app_server_observed_logged_out.jsonl")),
		"method not found":           newFakeAppServer(outcomeLines(t, "app_server_method_not_found.jsonl")),
		"observed EOF before id 1":   newFakeAppServer(outcomeLines(t, "app_server_eof_before_answer.jsonl")),
		"garbled id-1 result":        newFakeAppServer([]string{"not json", `{"id":1,"result":"nope"}`}),
		"start failure":              {startEr: errors.New("exec: \"codex\": executable file not found"), done: make(chan struct{})},
		"response over cap":          newFakeAppServer([]string{`{"id":0,"result":{"pad":"` + strings.Repeat("x", int(probeMaxResponseBytes)) + `"}}`}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := readWith(t, f)
			assert.ErrorIs(t, err, ErrRateLimitsUnavailable)
			if f.starts > 0 && f.startEr == nil {
				assert.True(t, f.killed.Load() && f.waited, "always stopped and reaped")
			}
		})
	}
}

// A server that never answers is killed at the deadline; the call is unknown.
func TestRateLimitsRead_TimeoutKillsAndReaps(t *testing.T) {
	f := newFakeAppServer(nil)
	f.hang = true
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := RateLimitsRead(ctx, Config{BinaryPath: "codex", appServer: f.start})
	assert.ErrorIs(t, err, ErrRateLimitsUnavailable)
	assert.True(t, f.killed.Load() && f.waited)
}

// Closing stdin before reading the answer reproduces the observed failure: the
// fake (like 0.171.0) exits without answering. Guards against a regression to a
// fixed-stdin CommandRunner call.
func TestRateLimitsRead_EarlyStdinCloseGetsNoAnswer(t *testing.T) {
	f := newFakeAppServer(outcomeLines(t, "app_server_observed_logged_in.jsonl"))
	f.stdin.closed = true
	_, err := readWith(t, f)
	assert.ErrorIs(t, err, ErrRateLimitsUnavailable)
}

func boolPtr(b bool) *bool { return &b }
