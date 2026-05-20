package mcp_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
)

func TestSubscriptionManager_HappyPath(t *testing.T) {
	t.Run("subscribe to instance increases active count", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		id := uuid.New()
		err := mgr.SubscribeInstance(id)
		require.NoError(t, err)
		assert.Equal(t, 1, mgr.ActiveCount())
	})

	t.Run("subscribe to system increases active count", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		err := mgr.SubscribeSystem()
		require.NoError(t, err)
		assert.Equal(t, 1, mgr.ActiveCount())
	})

	t.Run("instance and system subscriptions both counted", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		require.NoError(t, mgr.SubscribeInstance(uuid.New()))
		require.NoError(t, mgr.SubscribeInstance(uuid.New()))
		require.NoError(t, mgr.SubscribeSystem())
		assert.Equal(t, 3, mgr.ActiveCount())
	})
}

func TestSubscriptionManager_Success(t *testing.T) {
	t.Run("unsubscribe instance stops counting", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		id := uuid.New()
		require.NoError(t, mgr.SubscribeInstance(id))
		assert.Equal(t, 1, mgr.ActiveCount())

		mgr.UnsubscribeInstance(id)
		assert.Equal(t, 0, mgr.ActiveCount())
	})

	t.Run("subscribe to same instance twice replaces subscription", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		id := uuid.New()
		require.NoError(t, mgr.SubscribeInstance(id))
		require.NoError(t, mgr.SubscribeInstance(id))
		assert.Equal(t, 1, mgr.ActiveCount(), "should not duplicate subscriptions")
	})

	t.Run("subscribe system twice replaces subscription", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		require.NoError(t, mgr.SubscribeSystem())
		require.NoError(t, mgr.SubscribeSystem())
		assert.Equal(t, 1, mgr.ActiveCount(), "should not duplicate system subscription")
	})
}

func TestSubscriptionManager_Failure(t *testing.T) {
	t.Run("unsubscribe non-existent instance is a no-op", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		// Should not panic or error.
		mgr.UnsubscribeInstance(uuid.New())
		assert.Equal(t, 0, mgr.ActiveCount())
	})

	t.Run("unsubscribe system when not subscribed is a no-op", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		// Should not panic or error.
		mgr.UnsubscribeSystem()
		assert.Equal(t, 0, mgr.ActiveCount())
	})
}

func TestSubscriptionManager_Error(t *testing.T) {
	t.Run("cleanup all releases everything", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		require.NoError(t, mgr.SubscribeInstance(uuid.New()))
		require.NoError(t, mgr.SubscribeInstance(uuid.New()))
		require.NoError(t, mgr.SubscribeSystem())
		assert.Equal(t, 3, mgr.ActiveCount())

		mgr.CleanupAll()
		assert.Equal(t, 0, mgr.ActiveCount())
	})

	t.Run("cleanup all is safe to call when empty", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		// Should not panic.
		mgr.CleanupAll()
		assert.Equal(t, 0, mgr.ActiveCount())
	})
}

func TestSubscriptionManager_Edge(t *testing.T) {
	t.Run("rapid subscribe and unsubscribe", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				id := uuid.New()
				_ = mgr.SubscribeInstance(id)
				mgr.UnsubscribeInstance(id)
			}()
		}
		wg.Wait()

		assert.Equal(t, 0, mgr.ActiveCount())
	})

	t.Run("OnEvent callback receives forwarded events", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		received := make(chan engine.Event, 10)
		mgr.OnEvent = func(_ uuid.UUID, evt engine.Event) {
			received <- evt
		}

		testID := uuid.New()
		require.NoError(t, mgr.SubscribeInstance(testID))

		sentEvent := engine.Event{
			Type:      engine.EventTypeNodeStarted,
			Timestamp: time.Now(),
			Data:      map[string]string{"node": "start"},
		}
		bus.Publish(testID, sentEvent)

		select {
		case got := <-received:
			assert.Equal(t, engine.EventTypeNodeStarted, got.Type)
			assert.Equal(t, sentEvent.Data, got.Data)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for forwarded event")
		}

		mgr.CleanupAll()
	})

	t.Run("OnEvent receives system events", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		received := make(chan engine.Event, 10)
		mgr.OnEvent = func(_ uuid.UUID, evt engine.Event) {
			received <- evt
		}

		require.NoError(t, mgr.SubscribeSystem())

		sentEvent := engine.Event{
			Type:      engine.EventTypeInstanceCompleted,
			Timestamp: time.Now(),
			Data:      map[string]string{"status": "done"},
		}
		bus.Publish(uuid.New(), sentEvent)

		select {
		case got := <-received:
			assert.Equal(t, engine.EventTypeInstanceCompleted, got.Type)
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for system event")
		}

		mgr.CleanupAll()
	})

	t.Run("events stop after unsubscribe", func(t *testing.T) {
		bus := eventbus.New()
		mgr := adaptmcp.NewSubscriptionManager(bus)

		received := make(chan engine.Event, 10)
		mgr.OnEvent = func(_ uuid.UUID, evt engine.Event) {
			received <- evt
		}

		testID := uuid.New()
		require.NoError(t, mgr.SubscribeInstance(testID))

		// Send first event — should be received.
		bus.Publish(testID, engine.Event{Type: engine.EventTypeNodeStarted})
		select {
		case <-received:
			// OK
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for first event")
		}

		// Unsubscribe, then send another event.
		mgr.UnsubscribeInstance(testID)

		// Give goroutine time to notice the closed channel.
		time.Sleep(50 * time.Millisecond)

		bus.Publish(testID, engine.Event{Type: engine.EventTypeNodeCompleted})

		select {
		case evt := <-received:
			t.Fatalf("should not have received event after unsubscribe, got %s", evt.Type)
		case <-time.After(200 * time.Millisecond):
			// Expected: no event received.
		}
	})
}
