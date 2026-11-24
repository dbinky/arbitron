package runneragent_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/require"
)

func TestAgentDefaultCadence(t *testing.T) {
	h := newHarness(t, 64)
	cfg := testConfig()
	cfg.Heartbeat = 0
	cfg.ProbeInterval = 0
	cfg.BackoffBase = 0
	cfg.BackoffCap = 0
	a, err := runneragent.New(cfg, runneragent.Deps{Conn: h.conn, Exec: h.exec, Sources: h.src, Telemetry: h.tel, Spool: h.spool, Identity: fakeIdentity{key: h.key}, Clock: h.clock})
	require.NoError(t, err)
	h.agent = a
	h.start()
	mark := h.clock.mark()
	s := h.admitted()
	h.clock.awaitAndAdvance(t, mark, 5*time.Second)
	require.NotNil(t, s.expect(t, runneragent.KindHeartbeat).Heartbeat.Telemetry.CPUPercent)
}

func TestNewRejectsTypedNilPort(t *testing.T) {
	h := newHarness(t, 64)
	var conn *fakeConn
	_, err := runneragent.New(testConfig(), runneragent.Deps{Conn: conn, Exec: h.exec, Sources: h.src, Telemetry: h.tel, Spool: h.spool, Identity: fakeIdentity{key: h.key}})
	require.ErrorContains(t, err, "Conn")
}

func TestAgentAckedGenerationNeverRunsAgain(t *testing.T) {
	h := newHarness(t, 64)
	h.start()
	s := h.admitted()
	ref := runneragent.JobRef{JobID: "acked", Generation: 2}
	s.push(dispatch(ref, "anthropic_subscription"))
	s.expect(t, runneragent.KindAccept)
	s.expect(t, runneragent.KindOutput)
	s.expect(t, runneragent.KindResult)
	s.push(runneragent.Inbound{Kind: runneragent.KindAck, JobID: ref.JobID, Generation: ref.Generation, AckResult: true})
	require.Eventually(t, func() bool { p, _ := h.spool.Pending(context.Background()); return len(p) == 0 }, time.Second, time.Millisecond)
	s.push(dispatch(ref, "anthropic_subscription"))
	s.none(t, runneragent.KindAccept, 100*time.Millisecond)
	require.Equal(t, 1, h.exec.callCount())
}

func TestAgentStreamsOutputAndResult(t *testing.T) {
	h := newHarness(t, 64)
	ref := runneragent.JobRef{JobID: "sink", Generation: 4}
	h.exec.script(ref, func(ctx context.Context, d runneragent.Dispatch, emit runneragent.EmitFunc) runneragent.Result {
		require.Equal(t, "claude-sonnet-4-5", d.Chat.Model)
		require.Equal(t, "hi", d.Chat.Prompt)
		require.NoError(t, emit(ctx, d.Chat.Prompt))
		return runneragent.Result{OK: true, Chat: &runneragent.ChatResult{Output: d.Chat.Model + ":" + d.Chat.Prompt}}
	})
	h.start()
	s := h.admitted()
	s.push(dispatch(ref, "anthropic_subscription"))
	s.expect(t, runneragent.KindAccept)
	require.Equal(t, "hi", s.expect(t, runneragent.KindOutput).Output.Token)
	require.Equal(t, "claude-sonnet-4-5:hi", s.expect(t, runneragent.KindResult).Result.Chat.Output)
}
