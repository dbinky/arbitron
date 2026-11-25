package runneragent_test

import (
	"context"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type pendingModelVerifier struct {
	calls chan []string
	done  chan struct{}
}

func (v *pendingModelVerifier) VerifyModels(_ context.Context, models []string) <-chan struct{} {
	v.calls <- append([]string(nil), models...)
	return v.done
}

func TestAgent_AdmissionStartsModelVerificationWithoutBlocking(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "verifying", false: "optional"}[enabled], func(t *testing.T) {
			h := newHarness(t, 64)
			v := &pendingModelVerifier{calls: make(chan []string, 2), done: make(chan struct{})}
			var models runneragent.ModelVerifier
			if enabled {
				models = v
			}
			var err error
			h.agent, err = runneragent.New(testConfig(), runneragent.Deps{Conn: h.conn, Exec: h.exec, Sources: h.src, Telemetry: h.tel, Spool: h.spool, Identity: fakeIdentity{key: h.key}, Clock: h.clock, Models: models})
			require.NoError(t, err)
			h.start()
			s := h.session()
			s.push(runneragent.Inbound{Kind: runneragent.KindChallenge, Challenge: &runneragent.Challenge{KeyID: testKeyID, Nonce: []byte("n")}})
			s.expect(t, runneragent.KindChallengeResponse)
			s.expect(t, runneragent.KindHello)
			mark := h.clock.mark()
			s.push(runneragent.Inbound{Kind: runneragent.KindAdmitted, Admitted: &runneragent.Admission{RunnerID: "mac-1", HeartbeatSeconds: 1, ClaudeModels: []string{"claude-opus-5-5"}}})
			if enabled {
				select {
				case got := <-v.calls:
					assert.Equal(t, []string{"claude-opus-5-5"}, got)
				case <-time.After(time.Second):
					t.Fatal("verification not started")
				}
			}
			h.clock.awaitAndAdvance(t, mark, time.Second)
			s.expect(t, runneragent.KindHeartbeat)
			assert.Empty(t, v.calls, "only one pass per admission")
		})
	}
}
