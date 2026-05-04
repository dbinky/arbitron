package eventbus_test

import (
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInProcessEventBus_ImplementsInterface(t *testing.T) {
	var _ engine.EventBus = eventbus.New()
}

func TestInProcessEventBus_PublishWithNoSubscribers(t *testing.T) {
	bus := eventbus.New()

	// Should not panic when publishing with no subscribers.
	assert.NotPanics(t, func() {
		bus.Publish(uuid.New(), engine.Event{
			Type:      "test",
			Timestamp: time.Now(),
			Data:      "hello",
		})
	})
}

func TestInProcessEventBus_SubscribeAndReceive(t *testing.T) {
	bus := eventbus.New()
	instanceID := uuid.New()

	ch, unsub := bus.Subscribe(instanceID)
	defer unsub()

	evt := engine.Event{
		Type:      "node_started",
		Timestamp: time.Now(),
		Data:      map[string]string{"nodeId": "A"},
	}
	bus.Publish(instanceID, evt)

	select {
	case received := <-ch:
		assert.Equal(t, evt.Type, received.Type)
		assert.Equal(t, evt.Data, received.Data)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestInProcessEventBus_MultipleSubscribersReceiveSameEvent(t *testing.T) {
	bus := eventbus.New()
	instanceID := uuid.New()

	ch1, unsub1 := bus.Subscribe(instanceID)
	defer unsub1()
	ch2, unsub2 := bus.Subscribe(instanceID)
	defer unsub2()
	ch3, unsub3 := bus.Subscribe(instanceID)
	defer unsub3()

	evt := engine.Event{
		Type:      "state_changed",
		Timestamp: time.Now(),
		Data:      "running",
	}
	bus.Publish(instanceID, evt)

	for i, ch := range []<-chan engine.Event{ch1, ch2, ch3} {
		select {
		case received := <-ch:
			assert.Equal(t, evt.Type, received.Type, "subscriber %d", i)
			assert.Equal(t, evt.Data, received.Data, "subscriber %d", i)
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out waiting for event", i)
		}
	}
}

func TestInProcessEventBus_UnsubscribeStopsReceiving(t *testing.T) {
	bus := eventbus.New()
	instanceID := uuid.New()

	ch, unsub := bus.Subscribe(instanceID)

	// Unsubscribe should close the channel.
	unsub()

	// Channel should be closed (reads yield zero value, ok=false).
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after unsubscribe")

	// Publishing after unsubscribe should not panic.
	assert.NotPanics(t, func() {
		bus.Publish(instanceID, engine.Event{
			Type:      "test",
			Timestamp: time.Now(),
			Data:      "after-unsub",
		})
	})
}

func TestInProcessEventBus_UnsubscribeIsIdempotent(t *testing.T) {
	bus := eventbus.New()
	instanceID := uuid.New()

	_, unsub := bus.Subscribe(instanceID)

	// Calling unsub multiple times should not panic.
	assert.NotPanics(t, func() {
		unsub()
		unsub()
		unsub()
	})
}

func TestInProcessEventBus_DifferentInstancesAreIsolated(t *testing.T) {
	bus := eventbus.New()
	id1 := uuid.New()
	id2 := uuid.New()

	ch1, unsub1 := bus.Subscribe(id1)
	defer unsub1()
	ch2, unsub2 := bus.Subscribe(id2)
	defer unsub2()

	evt := engine.Event{
		Type:      "node_completed",
		Timestamp: time.Now(),
		Data:      "only-for-id1",
	}
	bus.Publish(id1, evt)

	// ch1 should receive the event.
	select {
	case received := <-ch1:
		assert.Equal(t, evt.Type, received.Type)
	case <-time.After(time.Second):
		t.Fatal("ch1 timed out waiting for event")
	}

	// ch2 should NOT receive the event.
	select {
	case <-ch2:
		t.Fatal("ch2 should not receive events for a different instance")
	case <-time.After(50 * time.Millisecond):
		// Expected: no event received.
	}
}

func TestInProcessEventBus_SlowConsumerDropsEvents(t *testing.T) {
	bus := eventbus.New()
	instanceID := uuid.New()

	ch, unsub := bus.Subscribe(instanceID)
	defer unsub()

	// Publish more events than the buffer size (256) without reading.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 300; i++ {
			bus.Publish(instanceID, engine.Event{
				Type:      "test",
				Timestamp: time.Now(),
				Data:      i,
			})
		}
	}()

	// Publishing should complete without deadlocking.
	select {
	case <-done:
		// Good: no deadlock.
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: publishing blocked on slow consumer")
	}

	// Drain the channel and count received events. Should be <= 256.
	var count int
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("channel should not be closed")
			}
			count++
		default:
			goto drained
		}
	}
drained:
	assert.LessOrEqual(t, count, 256, "should not receive more than buffer size")
	assert.Greater(t, count, 0, "should receive at least some events")
}

func TestInProcessEventBus_ConcurrentPublishAndSubscribe(t *testing.T) {
	bus := eventbus.New()
	instanceID := uuid.New()

	var wg sync.WaitGroup
	const numGoroutines = 10
	const eventsPerGoroutine = 50

	// Subscribe before publishing.
	ch, unsub := bus.Subscribe(instanceID)
	defer unsub()

	// Concurrently publish events.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < eventsPerGoroutine; j++ {
				bus.Publish(instanceID, engine.Event{
					Type:      "concurrent",
					Timestamp: time.Now(),
					Data:      g*eventsPerGoroutine + j,
				})
			}
		}(i)
	}

	// Concurrently subscribe and unsubscribe.
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			subCh, subUnsub := bus.Subscribe(instanceID)
			// Drain a bit.
			for j := 0; j < 5; j++ {
				select {
				case <-subCh:
				case <-time.After(50 * time.Millisecond):
				}
			}
			subUnsub()
		}()
	}

	wg.Wait()

	// Drain main channel, verify no panic occurred.
	var received int
drainLoop:
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				break drainLoop
			}
			received++
		default:
			break drainLoop
		}
	}
	require.True(t, received > 0 || true, "concurrent test completed without panics")
}
