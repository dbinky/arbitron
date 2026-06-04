package eventbus_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubscribeAll_ReceivesEventsFromAnyInstance(t *testing.T) {
	bus := eventbus.New()

	ch, unsub := bus.SubscribeAll()
	defer unsub()

	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()

	evt1 := engine.Event{Type: "node_started", Timestamp: time.Now(), Data: "from-id1"}
	evt2 := engine.Event{Type: "node_completed", Timestamp: time.Now(), Data: "from-id2"}
	evt3 := engine.Event{Type: "state_changed", Timestamp: time.Now(), Data: "from-id3"}

	bus.Publish(id1, evt1)
	bus.Publish(id2, evt2)
	bus.Publish(id3, evt3)

	for i, tc := range []struct {
		evt engine.Event
		id  uuid.UUID
	}{{evt1, id1}, {evt2, id2}, {evt3, id3}} {
		select {
		case received := <-ch:
			assert.Equal(t, tc.evt.Type, received.Event.Type, "event %d type", i)
			assert.Equal(t, tc.evt.Data, received.Event.Data, "event %d data", i)
			assert.Equal(t, tc.id, received.InstanceID, "event %d instance ID is carried on the ScopedEvent", i)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func TestSubscribeAll_UnsubStopsDeliveryAndClosesChannel(t *testing.T) {
	bus := eventbus.New()

	ch, unsub := bus.SubscribeAll()

	// Unsubscribe should close the channel.
	unsub()

	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after unsubscribe")

	// Publishing after unsubscribe should not panic.
	assert.NotPanics(t, func() {
		bus.Publish(uuid.New(), engine.Event{
			Type:      "test",
			Timestamp: time.Now(),
			Data:      "after-unsub",
		})
	})
}

func TestSubscribeAll_UnsubIsIdempotent(t *testing.T) {
	bus := eventbus.New()

	_, unsub := bus.SubscribeAll()

	assert.NotPanics(t, func() {
		unsub()
		unsub()
		unsub()
	})
}

func TestSubscribeAll_CoexistsWithPerInstanceSubscribe(t *testing.T) {
	bus := eventbus.New()

	instanceID := uuid.New()
	otherID := uuid.New()

	allCh, allUnsub := bus.SubscribeAll()
	defer allUnsub()

	instCh, instUnsub := bus.Subscribe(instanceID)
	defer instUnsub()

	// Publish to instanceID — both channels should receive.
	evt := engine.Event{Type: "test", Timestamp: time.Now(), Data: "shared"}
	bus.Publish(instanceID, evt)

	select {
	case received := <-allCh:
		assert.Equal(t, "test", received.Event.Type)
		assert.Equal(t, instanceID, received.InstanceID)
	case <-time.After(time.Second):
		t.Fatal("allCh timed out")
	}

	select {
	case received := <-instCh:
		assert.Equal(t, "test", received.Type)
	case <-time.After(time.Second):
		t.Fatal("instCh timed out")
	}

	// Publish to otherID — only allCh should receive.
	evt2 := engine.Event{Type: "other", Timestamp: time.Now(), Data: "only-all"}
	bus.Publish(otherID, evt2)

	select {
	case received := <-allCh:
		assert.Equal(t, "other", received.Event.Type)
		assert.Equal(t, otherID, received.InstanceID)
	case <-time.After(time.Second):
		t.Fatal("allCh timed out for other instance")
	}

	select {
	case <-instCh:
		t.Fatal("instCh should not receive events for a different instance")
	case <-time.After(50 * time.Millisecond):
		// Expected.
	}
}

func TestSubscribeAll_MultipleAllSubscribers(t *testing.T) {
	bus := eventbus.New()

	ch1, unsub1 := bus.SubscribeAll()
	defer unsub1()
	ch2, unsub2 := bus.SubscribeAll()
	defer unsub2()

	evt := engine.Event{Type: "broadcast", Timestamp: time.Now(), Data: "hello"}
	bus.Publish(uuid.New(), evt)

	for i, ch := range []<-chan engine.ScopedEvent{ch1, ch2} {
		select {
		case received := <-ch:
			assert.Equal(t, "broadcast", received.Event.Type, "subscriber %d", i)
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}

func TestSubscribeAll_DeliversScopedEventWithInstanceID(t *testing.T) {
	bus := eventbus.New()

	allCh, allUnsub := bus.SubscribeAll()
	defer allUnsub()

	instanceID := uuid.New()
	instCh, instUnsub := bus.Subscribe(instanceID)
	defer instUnsub()

	evt := engine.Event{Type: "node_started", Timestamp: time.Now(), Data: "payload"}
	bus.Publish(instanceID, evt)

	// SubscribeAll yields a ScopedEvent carrying the instance ID + the event.
	select {
	case sev := <-allCh:
		assert.Equal(t, instanceID, sev.InstanceID)
		assert.Equal(t, evt.Type, sev.Event.Type)
		assert.Equal(t, evt.Data, sev.Event.Data)
	case <-time.After(time.Second):
		t.Fatal("SubscribeAll timed out")
	}

	// The per-instance Subscribe channel still yields a bare Event (unchanged).
	select {
	case received := <-instCh:
		assert.Equal(t, evt.Type, received.Type)
		assert.Equal(t, evt.Data, received.Data)
	case <-time.After(time.Second):
		t.Fatal("Subscribe timed out")
	}
}

func TestNoOpEventBus_SubscribeAll(t *testing.T) {
	bus := eventbus.NewNoOp()
	ch, cancel := bus.SubscribeAll()
	defer cancel()

	_, ok := <-ch
	assert.False(t, ok, "channel should be closed")
}

func TestSubscribeAll_ImplementsInterface(t *testing.T) {
	// Verify that InProcessEventBus still satisfies the EventBus interface
	// now that SubscribeAll is required.
	var _ engine.EventBus = eventbus.New()
	var _ engine.EventBus = eventbus.NewNoOp()

	// Also verify SubscribeAll returns the expected types.
	bus := eventbus.New()
	ch, unsub := bus.SubscribeAll()
	require.NotNil(t, ch)
	require.NotNil(t, unsub)
	unsub()
}
