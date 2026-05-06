// Package eventbus provides EventBus adapter implementations.
package eventbus

import (
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
)

// NoOpEventBus is a no-op EventBus implementation suitable for testing.
type NoOpEventBus struct{}

// NewNoOp creates a new NoOpEventBus.
func NewNoOp() *NoOpEventBus {
	return &NoOpEventBus{}
}

// Publish does nothing.
func (b *NoOpEventBus) Publish(_ uuid.UUID, _ engine.Event) {}

// Subscribe returns a closed channel and a no-op cancel function.
func (b *NoOpEventBus) Subscribe(_ uuid.UUID) (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}

// SubscribeAll returns a closed channel and a no-op cancel function.
func (b *NoOpEventBus) SubscribeAll() (<-chan engine.Event, func()) {
	ch := make(chan engine.Event)
	close(ch)
	return ch, func() {}
}
