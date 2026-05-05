package eventbus_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/eventbus"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestNoOpEventBus_Publish(t *testing.T) {
	bus := eventbus.NewNoOp()
	// Should not panic.
	bus.Publish(uuid.New(), engine.Event{
		Type:      "test",
		Timestamp: time.Now(),
		Data:      "hello",
	})
}

func TestNoOpEventBus_Subscribe(t *testing.T) {
	bus := eventbus.NewNoOp()
	ch, cancel := bus.Subscribe(uuid.New())
	defer cancel()

	// Channel should be closed immediately.
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed")
}

func TestNoOpEventBus_ImplementsInterface(t *testing.T) {
	var _ engine.EventBus = eventbus.NewNoOp()
}
