package http_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

type wireEvent struct {
	InstanceID uuid.UUID       `json:"instanceId"`
	Type       string          `json:"type"`
	Data       json.RawMessage `json:"data"`
}

func dialSystemStream(t *testing.T, url string) (*websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, "ws"+url[len("http"):]+"/ws", nil)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(websocket.StatusNormalClosure, "") })
	time.Sleep(50 * time.Millisecond) // let the handler subscribe
	return conn, ctx
}

func readWire(t *testing.T, conn *websocket.Conn, ctx context.Context) wireEvent {
	t.Helper()
	_, msg, err := conn.Read(ctx)
	require.NoError(t, err)
	var ev wireEvent
	require.NoError(t, json.Unmarshal(msg, &ev))
	return ev
}

// nopLedger satisfies runner.Ledger for a projector that only publishes.
type nopLedger struct{ runner.Ledger }

func TestSystemStream_PoolEventsReachEveryAuthenticatedUser(t *testing.T) {
	bus := eventbus.New()
	repo := newMockInstanceRepo()
	user := identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser}
	srv := scopedSysStreamServer(bus, repo, user)
	defer srv.Close()
	conn, ctx := dialSystemStream(t, srv.URL)

	// Produced by the real recorder, exactly as the health monitor does.
	rec := runner.NewEventRecorder(&memLog{}, bus, 100, time.Now)
	rec.Record(ctx, runner.PoolEvent{Kind: runner.EventSourceLoggedOut, RunnerID: runner.BuiltInRunnerID, SourceID: "builtin/anthropic_subscription/-",
		Cause: runner.Cause{Kind: runner.CauseOutcome, Outcome: runner.OutcomeLoggedOut}})

	ev := readWire(t, conn, ctx)
	assert.Equal(t, "source.logged_out", ev.Type)
	assert.Equal(t, uuid.Nil, ev.InstanceID)
	assert.Contains(t, string(ev.Data), `"sourceId":"builtin/anthropic_subscription/-"`)
}

func TestSystemStream_OnlyPoolPrefixesBypassOwnership(t *testing.T) {
	bus := eventbus.New()
	repo := newMockInstanceRepo()
	user := identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser}
	own := ownedInstanceForOwner(repo, user.UserID)
	srv := scopedSysStreamServer(bus, repo, user)
	defer srv.Close()
	conn, ctx := dialSystemStream(t, srv.URL)

	bus.Publish(uuid.Nil, engine.Event{Type: "execution.status", Timestamp: time.Now(), Data: map[string]any{}}) // not a pool prefix: dropped
	bus.Publish(uuid.Nil, engine.Event{Type: "queue.changed", Timestamp: time.Now(), Data: map[string]any{}})    // dropped as before
	bus.Publish(own.ID, engine.Event{Type: "node.completed", Timestamp: time.Now(), Data: map[string]any{}})
	assert.Equal(t, "node.completed", readWire(t, conn, ctx).Type)

	bus.Publish(uuid.Nil, engine.Event{Type: "pool.changed", Timestamp: time.Now(), Data: map[string]any{}})
	assert.Equal(t, "pool.changed", readWire(t, conn, ctx).Type)
}

func TestSystemStream_HiddenRunExecutionStatusNeverReachesAnotherUser(t *testing.T) {
	bus := eventbus.New()
	repo := newMockInstanceRepo()
	viewer := identity.Identity{UserID: uuid.New(), Username: "viewer", Role: identity.RoleUser}
	mine := ownedInstanceForOwner(repo, viewer.UserID)
	hidden := ownedInstanceForOwner(repo, uuid.New())
	srv := scopedSysStreamServer(bus, repo, viewer)
	defer srv.Close()
	conn, ctx := dialSystemStream(t, srv.URL)

	projector := runner.NewExecutionStatusProjector(nopLedger{})
	projector.AttachBus(bus)
	jobFor := func(run uuid.UUID) runner.Job {
		return runner.Job{ID: uuid.New(), Key: runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "node", Attempt: 1},
			NodeID: "secret-node", Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}, State: runner.JobQueued,
			Wait: &runner.WaitReason{Kind: runner.WaitCapacity, Detail: "waiting · capacity"}}
	}
	projector.Publish(ctx, jobFor(hidden.ID), instance.ExecWaitingCapacity) // must be dropped
	projector.Publish(ctx, jobFor(mine.ID), instance.ExecWaitingCapacity)   // must be delivered

	ev := readWire(t, conn, ctx)
	assert.Equal(t, "execution.status", ev.Type)
	assert.Equal(t, mine.ID, ev.InstanceID, "the hidden run's status was never delivered")
	assert.NotContains(t, string(ev.Data), hidden.ID.String())
}

// memLog is a minimal runner.EventLog for the recorder in HTTP tests.
type memLog struct{ events []runner.PoolEvent }

func (l *memLog) Append(_ context.Context, ev runner.PoolEvent) error {
	l.events = append(l.events, ev)
	return nil
}
func (l *memLog) List(context.Context, int) ([]runner.PoolEvent, error) { return l.events, nil }
func (l *memLog) Prune(context.Context, int) error                      { return nil }
