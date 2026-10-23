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

func TestReadRollout_SizeLimit(t *testing.T) {
	const limit = 1 << 20
	prefix := metaLine(rolloutThread) + "\n" + ctxLine("a") + "\n" + ctxLine("b") + "\n"
	for _, tc := range []struct {
		name string
		size int
	}{
		{"below limit", limit - 1},
		{"exact limit", limit},
		{"one byte over limit", limit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "rollout.jsonl")
			// Short blank lines ensure the cap covers the whole file, including
			// delimiters, rather than just individually buffered JSON lines.
			require.NoError(t, os.WriteFile(p, []byte(prefix+strings.Repeat("\n", tc.size-len(prefix))), 0o600))
			ev, err := readRollout(p)
			if tc.size > limit {
				require.ErrorIs(t, err, ErrModelEvidence)
				assert.Empty(t, ev.ThreadID, "oversized input must return no partial evidence")
				assert.Empty(t, ev.TurnModels)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, rolloutThread, ev.ThreadID)
			assert.Equal(t, []string{"a", "b"}, ev.TurnModels)
		})
	}
	t.Run("oversized tool line is refused", func(t *testing.T) {
		huge := `{"type":"response_item","payload":{"text":"` + strings.Repeat("x", limit+10) + `"}}`
		p := writeRollout(t, t.TempDir(), rolloutThread, metaLine(rolloutThread), huge, ctxLine("a"))
		ev, err := readRollout(p)
		require.ErrorIs(t, err, ErrModelEvidence)
		assert.Empty(t, ev.ThreadID)
		assert.Empty(t, ev.TurnModels)
	})
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
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), strings.Repeat("\n", 1<<20))
		}, nil)
		require.ErrorIs(t, err, ErrModelEvidence)
		assert.Len(t, mr.calls, 1, "only the version probe ran; the resume was never launched")
	})
	t.Run("new turn exceeding rollout cap never completes", func(t *testing.T) {
		ev, err, mr := run(t, turnConfig{Model: "gpt-5", ResumeID: rolloutThread}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"))
		}, func(h string) {
			writeRollout(t, h, rolloutThread, metaLine(rolloutThread), ctxLine("gpt-5"), ctxLine("gpt-5"), strings.Repeat("\n", 1<<20))
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
