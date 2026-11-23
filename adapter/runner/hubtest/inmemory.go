// adapter/runner/hubtest/inmemory.go
package hubtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/runner"
)

// ReplyFunc decides the streamed tokens and terminal result for a dispatch.
type ReplyFunc func(d runner.Outbound) (tokens []string, res runner.RemoteResult)

// DefaultReply answers "remote:<runner>:<model>"; stream jobs also emit tokens.
func DefaultReply(id runner.RunnerID) ReplyFunc {
	return func(d runner.Outbound) ([]string, runner.RemoteResult) {
		model := d.Dispatch.Chat.Model
		out := "remote:" + string(id) + ":" + model
		var tokens []string
		if d.Dispatch.Chat.Stream {
			tokens = []string{"remote:", string(id), ":" + model}
		}
		return tokens, runner.RemoteResult{OK: true, Chat: &runner.RemoteChatResult{Output: out, Model: model, TokensIn: 3, TokensOut: 2}}
	}
}

// InMemoryRunner is a protocol-faithful fake runner driving a real Hub: it
// dedupes dispatches per (job, generation), accepts, optionally holds work
// until released, streams sequenced output, sends the result, and kills held
// work on cancel/fenced/close.
type InMemoryRunner struct {
	ID   runner.RunnerID
	hub  *runner.Hub
	conn *FakeConn

	ready       chan struct{}
	mu          sync.Mutex
	session     *runner.Session
	acceptIf    func(gen int64) bool
	hold        bool
	reply       ReplyFunc
	seen        map[runner.Fence]bool
	gates       map[runner.Fence]chan struct{}
	killed      map[runner.Fence]bool
	completed   []runner.Fence
	inflight    int
	maxInflight int
}

type memConn struct{ r *InMemoryRunner }

func (c memConn) Send(ctx context.Context, m runner.Outbound) error {
	if err := c.r.conn.Send(ctx, m); err != nil {
		return err
	}
	switch m.Kind {
	case runner.MsgDispatch:
		go c.r.execute(m)
	case runner.MsgCancel, runner.MsgFenced:
		c.r.kill(runner.Fence{JobID: m.JobID, RunnerID: c.r.ID, Generation: m.Generation})
	}
	return nil
}

func (c memConn) Close(code int, reason string) error {
	c.r.killAll()
	return c.r.conn.Close(code, reason)
}

// ConnectInMemoryRunner admits a new in-memory runner on h.
func ConnectInMemoryRunner(t testing.TB, h *runner.Hub, key runner.RunnerKey, hello runner.HelloInfo) *InMemoryRunner {
	t.Helper()
	r := &InMemoryRunner{ID: key.RunnerID, hub: h, conn: NewFakeConn(), ready: make(chan struct{}),
		reply: DefaultReply(key.RunnerID), seen: map[runner.Fence]bool{}, gates: map[runner.Fence]chan struct{}{},
		killed: map[runner.Fence]bool{}}
	s, err := h.Admit(context.Background(), key, memConn{r: r}, hello)
	require.NoError(t, err)
	r.mu.Lock()
	r.session = s
	r.mu.Unlock()
	close(r.ready)
	return r
}

func (r *InMemoryRunner) execute(m runner.Outbound) {
	<-r.ready
	f := runner.Fence{JobID: m.JobID, RunnerID: r.ID, Generation: m.Generation}
	r.mu.Lock()
	if r.seen[f] {
		r.mu.Unlock()
		return
	}
	if r.acceptIf != nil && !r.acceptIf(m.Generation) {
		r.mu.Unlock()
		return
	}
	r.seen[f] = true
	gate := make(chan struct{})
	r.gates[f] = gate
	hold, s := r.hold, r.session
	r.inflight++
	if r.inflight > r.maxInflight {
		r.maxInflight = r.inflight
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inflight--
		delete(r.gates, f)
		r.mu.Unlock()
	}()

	ctx := context.Background()
	if err := r.hub.Handle(ctx, s, runner.Inbound{Kind: runner.MsgAccept, JobID: m.JobID, Generation: m.Generation, Accept: true}); err != nil {
		return
	}
	if hold {
		<-gate
	}
	r.mu.Lock()
	killed, reply := r.killed[f], r.reply
	r.mu.Unlock()
	if killed {
		return
	}
	tokens, res := reply(m)
	for i, tok := range tokens {
		seq := uint64(i + 1)
		if err := r.hub.Handle(ctx, s, runner.Inbound{Kind: runner.MsgOutput, JobID: m.JobID, Generation: m.Generation, Seq: seq,
			Output: &runner.OutputFrame{Seq: seq, Token: tok}}); err != nil {
			return
		}
	}
	if r.isKilled(f) {
		return
	}
	if err := r.hub.Handle(ctx, s, runner.Inbound{Kind: runner.MsgResult, JobID: m.JobID, Generation: m.Generation, Result: &res}); err != nil {
		return
	}
	r.mu.Lock()
	r.completed = append(r.completed, f)
	r.mu.Unlock()
}

func (r *InMemoryRunner) isKilled(f runner.Fence) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.killed[f]
}

// openGate releases a held job; caller holds r.mu.
func (r *InMemoryRunner) openGate(f runner.Fence) {
	if g, ok := r.gates[f]; ok {
		delete(r.gates, f)
		close(g)
	}
}

func (r *InMemoryRunner) kill(f runner.Fence) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.killed[f] = true
	r.openGate(f)
}

func (r *InMemoryRunner) killAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for f := range r.gates {
		r.killed[f] = true
		r.openGate(f)
	}
}

func (r *InMemoryRunner) SetHold(v bool)                 { r.mu.Lock(); r.hold = v; r.mu.Unlock() }
func (r *InMemoryRunner) SetAcceptIf(f func(int64) bool) { r.mu.Lock(); r.acceptIf = f; r.mu.Unlock() }
func (r *InMemoryRunner) SetReply(f ReplyFunc)           { r.mu.Lock(); r.reply = f; r.mu.Unlock() }

// Release lets every held generation of jobID finish.
func (r *InMemoryRunner) Release(jobID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for f := range r.gates {
		if f.JobID == jobID {
			r.openGate(f)
		}
	}
}

func (r *InMemoryRunner) ReleaseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for f := range r.gates {
		r.openGate(f)
	}
}

func (r *InMemoryRunner) Inflight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inflight
}

func (r *InMemoryRunner) MaxInflight() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxInflight
}

// AwaitInflight waits until exactly n jobs are accepted and running here.
func (r *InMemoryRunner) AwaitInflight(t testing.TB, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return r.Inflight() == n }, 2*time.Second, 2*time.Millisecond,
		"runner %s: want %d in flight, have %d", r.ID, n, r.Inflight())
}

func (r *InMemoryRunner) Completed() []runner.Fence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runner.Fence(nil), r.completed...)
}

func (r *InMemoryRunner) Dispatches() []runner.Outbound {
	var out []runner.Outbound
	for _, m := range r.conn.Sent() {
		if m.Kind == runner.MsgDispatch {
			out = append(out, m)
		}
	}
	return out
}

func (r *InMemoryRunner) Conn() *FakeConn { return r.conn }

func (r *InMemoryRunner) Session() *runner.Session {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}
