package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rolloutThread  = "019f00b8-f154-7bb1-ae4b-2adb20c4dac9"
	rolloutThread2 = "019f00b8-aaaa-7bb1-ae4b-2adb20c4dac9"
)

func metaLine(id string) string {
	return `{"timestamp":"t","ordinal":0,"type":"session_meta","payload":{"id":"` + id + `","cwd":"/w"}}`
}
func ctxLine(model string) string {
	return `{"timestamp":"t","ordinal":1,"type":"turn_context","payload":{"turn_id":"x","model":"` + model + `"}}`
}

// writeRollout stores a rollout where codex does: <home>/sessions/Y/M/D/rollout-<ts>-<id>.jsonl.
func writeRollout(t *testing.T, home, id string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", "2026", "10", "01")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	p := filepath.Join(dir, "rollout-2026-10-01T10-00-00-"+id+".jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return p
}

func TestRolloutLunaRouteReportedEvidence(t *testing.T) {
	home := t.TempDir()
	v := &rolloutVerifier{home: home}
	writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine("glm-5.3"), `{"type":"turn_context","payload":{"model":"glm-5.3","model_provider":"lunaroute"}}`)
	ev, err := v.lunaRouteAfterTurn(rolloutThread, "glm-5.3")
	require.NoError(t, err)
	require.Len(t, ev, 2)
	assert.Equal(t, "glm-5.3", ev[0].Model)
	assert.Empty(t, ev[0].Provider, "never invent a provider from the selected binding")
	assert.Equal(t, "lunaroute", ev[1].Provider)
	assert.Equal(t, "codex_turn_context", ev[1].Source)
	for _, model := range []string{"", "glm-5.3-flash", "gpt-6.1-sol", "claude-sonnet-5-lr1", "glm-5.3 lr_private", "glm-5.3\u00a0", strings.Repeat("x", 257)} {
		writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine(model))
		ev, err := v.lunaRouteAfterTurn(rolloutThread, "glm-5.3")
		require.ErrorIs(t, err, ErrModelEvidence)
		assert.Empty(t, ev)
		assert.NotContains(t, err.Error(), "lr_private")
	}
	writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine("glm-5.3"))
	_, err = v.lunaRouteAfterTurn(rolloutThread, "claude-sonnet-5-lr1")
	require.ErrorIs(t, err, ErrModelEvidence, "revoked aliases cannot be selected even with canonical evidence")
}

func TestRolloutLunaRouteProviderValidationDoesNotChangeSubscription(t *testing.T) {
	home := t.TempDir()
	writeRollout(t, home, rolloutThread, metaLine(rolloutThread), `{"type":"turn_context","payload":{"model":"glm-5.3","model_provider":42}}`)
	v := &rolloutVerifier{home: home}
	require.NoError(t, v.afterTurn(rolloutThread, "glm-5.3", 0), "subscription proof still ignores unknown fields")
	_, err := v.lunaRouteAfterTurn(rolloutThread, "glm-5.3")
	require.ErrorIs(t, err, ErrModelEvidence, "LunaRoute rejects malformed identity fields")
}

func TestRolloutVerifier_BeforeResume(t *testing.T) {
	home := t.TempDir()
	v := &rolloutVerifier{home: home}

	t.Run("every recorded turn on the pin", func(t *testing.T) {
		writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), ctxLine("gpt-5"))
		n, err := v.beforeResume(rolloutThread, "gpt-5")
		require.NoError(t, err)
		assert.Equal(t, 2, n)
	})
	t.Run("an earlier turn on another model is refused", func(t *testing.T) {
		writeRollout(t, home, rolloutThread2, metaLine(rolloutThread2), ctxLine("gpt-5"), ctxLine("gpt-6"))
		_, err := v.beforeResume(rolloutThread2, "gpt-5")
		require.ErrorIs(t, err, ErrModelEvidence)
	})
	t.Run("missing rollout is refused", func(t *testing.T) {
		_, err := v.beforeResume("019f00b8-ffff-7bb1-ae4b-2adb20c4dac9", "gpt-5")
		require.ErrorIs(t, err, ErrModelEvidence)
	})
	t.Run("rollout without turn_context is refused", func(t *testing.T) {
		id := "019f00b8-bbbb-7bb1-ae4b-2adb20c4dac9"
		writeRollout(t, home, id, metaLine(id))
		_, err := v.beforeResume(id, "gpt-5")
		require.ErrorIs(t, err, ErrModelEvidence)
	})
	t.Run("rollout of a different session is refused", func(t *testing.T) {
		id := "019f00b8-cccc-7bb1-ae4b-2adb20c4dac9"
		writeRollout(t, home, id, metaLine(rolloutThread), ctxLine("gpt-5"))
		_, err := v.beforeResume(id, "gpt-5")
		require.ErrorIs(t, err, ErrModelEvidence)
	})
	t.Run("malformed thread id never reaches the filesystem", func(t *testing.T) {
		_, err := v.beforeResume("../../etc/*", "gpt-5")
		require.ErrorIs(t, err, ErrModelEvidence)
	})
}

func TestRolloutVerifier_AfterTurnRequiresNewTurnOnPin(t *testing.T) {
	home := t.TempDir()
	v := &rolloutVerifier{home: home}
	writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), ctxLine("gpt-5"))

	require.NoError(t, v.afterTurn(rolloutThread, "gpt-5", 1), "second turn is new relative to 1 prior")
	err := v.afterTurn(rolloutThread, "gpt-5", 2)
	require.ErrorIs(t, err, ErrModelEvidence, "earlier turns must not satisfy the new turn's proof")

	writeRollout(t, home, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), ctxLine("gpt-fallback"))
	require.ErrorIs(t, v.afterTurn(rolloutThread, "gpt-5", 1), ErrModelEvidence, "new turn ran another model")
}

// padLine is a non-identity record of exactly n bytes (excluding the newline),
// standing in for the large tool output and event records of a long run.
func padLine(kind string, n int) string {
	head := `{"type":"` + kind + `","payload":{"text":"`
	tail := `"}}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// padding returns non-identity records totalling at least n bytes.
func padding(n int) []string {
	var lines []string
	for size := 0; size < n; {
		kind := "response_item"
		if len(lines)%2 == 1 {
			kind = "event_msg"
		}
		lines = append(lines, padLine(kind, 64<<10))
		size += 64<<10 + 1
	}
	return lines
}

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func requireRefused(t *testing.T, ev rolloutEvidence, err error) {
	t.Helper()
	require.ErrorIs(t, err, ErrModelEvidence)
	assert.Empty(t, ev.ThreadID, "a refused rollout must supply no partial evidence")
	assert.Empty(t, ev.TurnModels)
}

func TestReadRollout_LargeRolloutVerifies(t *testing.T) {
	home := t.TempDir()
	v := &rolloutVerifier{home: home}
	lines := concat([]string{metaLine(rolloutThread), ctxLine("gpt-5")}, padding(3<<20))
	p := writeRollout(t, home, rolloutThread, lines...)
	fi, err := os.Stat(p)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(1<<20), "fixture must exceed the old 1 MiB cap")

	n, err := v.beforeResume(rolloutThread, "gpt-5")
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	require.NoError(t, v.afterTurn(rolloutThread, "gpt-5", 0))
}

func TestReadRollout_Edges(t *testing.T) {
	t.Run("identity records after large padding are found", func(t *testing.T) {
		lines := concat(padding(2<<20), []string{metaLine(rolloutThread)}, padding(1<<20), []string{ctxLine("gpt-5")})
		ev, err := readRollout(writeRollout(t, t.TempDir(), rolloutThread, lines...))
		require.NoError(t, err)
		assert.Equal(t, rolloutThread, ev.ThreadID)
		assert.Equal(t, []string{"gpt-5"}, ev.TurnModels)
	})
	t.Run("blank lines and no trailing newline", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "rollout.jsonl")
		body := "\n\n" + metaLine(rolloutThread) + "\n  \n\r\n" + ctxLine("a") + "\n\n" + ctxLine("b")
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		ev, err := readRollout(p)
		require.NoError(t, err)
		assert.Equal(t, rolloutThread, ev.ThreadID)
		assert.Equal(t, []string{"a", "b"}, ev.TurnModels)
	})
	t.Run("empty file proves nothing", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "rollout.jsonl")
		require.NoError(t, os.WriteFile(p, nil, 0o600))
		ev, err := readRollout(p)
		require.NoError(t, err)
		assert.Empty(t, ev.ThreadID)
		assert.Empty(t, ev.TurnModels)
	})
	t.Run("identity is taken only from session_meta and turn_context", func(t *testing.T) {
		decoy := `{"type":"response_item","payload":{"id":"` + rolloutThread2 + `","model":"gpt-evil"}}`
		ev, err := readRollout(writeRollout(t, t.TempDir(), rolloutThread, metaLine(rolloutThread), decoy, ctxLine("gpt-5")))
		require.NoError(t, err)
		assert.Equal(t, rolloutThread, ev.ThreadID)
		assert.Equal(t, []string{"gpt-5"}, ev.TurnModels)
	})
}

func TestReadRollout_Errors(t *testing.T) {
	for _, tc := range []struct {
		name string
		bad  string
	}{
		{"malformed JSON past 1 MiB", `{"type":"response_item","payload":`},
		{"record without a type past 1 MiB", `{"payload":{"text":"x"}}`},
		{"contradictory session id past 1 MiB", metaLine(rolloutThread2)},
		{"turn_context without a payload past 1 MiB", `{"type":"turn_context"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lines := concat([]string{metaLine(rolloutThread), ctxLine("gpt-5")}, padding(2<<20), []string{tc.bad, ctxLine("gpt-5")})
			ev, err := readRollout(writeRollout(t, t.TempDir(), rolloutThread, lines...))
			requireRefused(t, ev, err)
		})
	}
}

func TestReadRollout_RecordCap(t *testing.T) {
	assert.Equal(t, 16<<20, maxRolloutRecordBytes)
	for _, tc := range []struct {
		name string
		size int
		ok   bool
	}{
		{"record exactly at the cap", maxRolloutRecordBytes, true},
		{"record one byte over the cap", maxRolloutRecordBytes + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := writeRollout(t, t.TempDir(), rolloutThread, metaLine(rolloutThread), padLine("response_item", tc.size), ctxLine("a"))
			ev, err := readRollout(p)
			if !tc.ok {
				requireRefused(t, ev, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, rolloutThread, ev.ThreadID)
			assert.Equal(t, []string{"a"}, ev.TurnModels)
		})
	}
	t.Run("final record over the cap without a newline", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "rollout.jsonl")
		body := metaLine(rolloutThread) + "\n" + ctxLine("a") + "\n" + padLine("response_item", maxRolloutRecordBytes+1)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		ev, err := readRollout(p)
		requireRefused(t, ev, err)
	})
}

func TestReadRollout_TotalCap(t *testing.T) {
	assert.Equal(t, 256<<20, maxRolloutBytes)
	prefix := metaLine(rolloutThread) + "\n" + ctxLine("a") + "\n" + ctxLine("b") + "\n"
	const limit = 4 << 10
	for _, tc := range []struct {
		name string
		size int
	}{
		{"below limit", limit - 1},
		{"exact limit", limit},
		{"one byte over limit", limit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Blank lines ensure the cap covers the whole stream, including
			// delimiters, rather than just the JSON records.
			body := prefix + strings.Repeat("\n", tc.size-len(prefix))
			ev, err := readRolloutFrom(strings.NewReader(body), limit, 1<<10)
			if tc.size > limit {
				requireRefused(t, ev, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, rolloutThread, ev.ThreadID)
			assert.Equal(t, []string{"a", "b"}, ev.TurnModels)
		})
	}
	t.Run("identity past the cap is never trusted", func(t *testing.T) {
		body := strings.Repeat("\n", limit) + metaLine(rolloutThread) + "\n" + ctxLine("a") + "\n"
		ev, err := readRolloutFrom(strings.NewReader(body), limit, 1<<10)
		requireRefused(t, ev, err)
	})
	t.Run("a record straddling the cap reports the cap", func(t *testing.T) {
		body := prefix + strings.Repeat("\n", limit-len(prefix)-10) + ctxLine("c") + "\n"
		ev, err := readRolloutFrom(strings.NewReader(body), limit, 1<<10)
		requireRefused(t, ev, err)
		assert.Contains(t, err.Error(), "exceeds")
	})
}

// TestReadRollout_B9f9b29aRegression replays the shape that failed pr-review
// instance b9f9b29a at architect-r1: a ~2 MB rollout from a long agent run with
// one turn_context near the start and another after the bulk of tool output.
func TestReadRollout_B9f9b29aRegression(t *testing.T) {
	home := t.TempDir()
	v := &rolloutVerifier{home: home}
	const pin = "gpt-5.5"
	lines := concat(
		[]string{metaLine(rolloutThread), ctxLine(pin)},
		padding(1<<20),
		[]string{ctxLine(pin)},
		padding(900<<10),
	)
	p := writeRollout(t, home, rolloutThread, lines...)
	fi, err := os.Stat(p)
	require.NoError(t, err)
	require.Greater(t, fi.Size(), int64(1900<<10))

	require.NoError(t, v.afterTurn(rolloutThread, pin, 1))
	n, err := v.beforeResume(rolloutThread, pin)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}

func TestResolveCodexHome(t *testing.T) {
	h, err := resolveCodexHome([]string{"HOME=/h", "CODEX_HOME=/c"})
	require.NoError(t, err)
	assert.Equal(t, "/c", h)
	h, err = resolveCodexHome([]string{"HOME=/h"})
	require.NoError(t, err)
	assert.Equal(t, "/h/.codex", h)
	_, err = resolveCodexHome([]string{"PATH=/bin"})
	require.ErrorIs(t, err, ErrModelEvidence)
}

// A pinned turn through execSession.Start: the stream is clean, but the stored
// rollout decides whether the turn is done or refused.
func TestExecSession_PinnedModelEvidence(t *testing.T) {
	run := func(t *testing.T, cfg turnConfig, setup func(home string), onWait func(home string)) ([]turnEvent, error, *multiRunner) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		if setup != nil {
			setup(home)
		}
		mr := &multiRunner{scripts: []runScript{
			compatibleProbe(),
			{lines: execLines(rolloutThread, "ok", 1, 1), onWait: func() {
				if onWait != nil {
					onWait(home)
				}
			}},
		}}
		c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
		cfg.WorkDir, cfg.Prompt, cfg.VerifyModel = "/w", "p", true
		stream, err := newExecSession(&c).Start(context.Background(), cfg)
		if err != nil {
			return nil, err, mr
		}
		events, derr := drainStream(t, context.Background(), stream)
		require.NoError(t, derr)
		return events, nil, mr
	}
	lastKind := func(ev []turnEvent) turnEvent { return ev[len(ev)-1] }

	t.Run("fresh turn proven by its rollout completes", func(t *testing.T) {
		ev, err, _ := run(t, turnConfig{Model: "gpt-5"}, nil, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"))
		})
		require.NoError(t, err)
		assert.Equal(t, turnDone, lastKind(ev).Kind)
	})
	t.Run("fresh turn that ran another model is refused", func(t *testing.T) {
		ev, err, _ := run(t, turnConfig{Model: "gpt-5"}, nil, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-fallback"))
		})
		require.NoError(t, err)
		require.Equal(t, turnFailed, lastKind(ev).Kind)
		assert.ErrorIs(t, lastKind(ev).Err, ErrModelEvidence)
	})
	t.Run("fresh turn without any rollout is refused", func(t *testing.T) {
		ev, err, _ := run(t, turnConfig{Model: "gpt-5"}, nil, nil)
		require.NoError(t, err)
		require.Equal(t, turnFailed, lastKind(ev).Kind)
		assert.ErrorIs(t, lastKind(ev).Err, ErrModelEvidence)
	})
	t.Run("resume of a session already on another model spawns nothing", func(t *testing.T) {
		_, err, mr := run(t, turnConfig{Model: "gpt-5", ResumeID: rolloutThread}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-6"))
		}, nil)
		require.ErrorIs(t, err, ErrModelEvidence)
		assert.Len(t, mr.calls, 1, "only the version probe ran; the resume was never launched")
	})
	t.Run("oversized stored rollout refuses resume before spawn", func(t *testing.T) {
		_, err, mr := run(t, turnConfig{Model: "gpt-5", ResumeID: rolloutThread}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), padLine("response_item", maxRolloutRecordBytes+1))
		}, nil)
		require.ErrorIs(t, err, ErrModelEvidence)
		assert.Len(t, mr.calls, 1, "only the version probe ran; the resume was never launched")
	})
	t.Run("new turn exceeding rollout cap never completes", func(t *testing.T) {
		ev, err, mr := run(t, turnConfig{Model: "gpt-5", ResumeID: rolloutThread}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"))
		}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), ctxLine("gpt-5"), padLine("response_item", maxRolloutRecordBytes+1))
		})
		require.NoError(t, err)
		require.Len(t, mr.calls, 2)
		require.NotEmpty(t, ev)
		require.Equal(t, turnFailed, lastKind(ev).Kind)
		assert.ErrorIs(t, lastKind(ev).Err, ErrModelEvidence)
		for _, event := range ev {
			assert.NotEqual(t, turnDone, event.Kind, "oversized new-turn evidence must never be accepted")
		}
	})
	t.Run("resume: the earlier turn cannot prove the new one", func(t *testing.T) {
		ev, err, mr := run(t, turnConfig{Model: "gpt-5", ResumeID: rolloutThread}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"))
		}, nil) // codex wrote no new turn_context
		require.NoError(t, err)
		require.Len(t, mr.calls, 2)
		require.Equal(t, turnFailed, lastKind(ev).Kind)
		assert.ErrorIs(t, lastKind(ev).Err, ErrModelEvidence)
	})
	t.Run("resume proven by a new turn on the pin completes", func(t *testing.T) {
		ev, err, _ := run(t, turnConfig{Model: "gpt-5", ResumeID: rolloutThread}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"))
		}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), ctxLine("gpt-5"))
		})
		require.NoError(t, err)
		assert.Equal(t, turnDone, lastKind(ev).Kind)
	})
	t.Run("unpinned run is not verified", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: execLines(rolloutThread, "ok", 1, 1)}}}
		c := newCore(Config{BinaryPath: "/x/codex", Runner: mr})
		stream, err := newExecSession(&c).Start(context.Background(), turnConfig{Model: "gpt-5", Prompt: "p", WorkDir: "/w"})
		require.NoError(t, err)
		ev, derr := drainStream(t, context.Background(), stream)
		require.NoError(t, derr)
		assert.Equal(t, turnDone, lastKind(ev).Kind)
	})
}
