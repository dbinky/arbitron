package runner_test

import (
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/assert"
)

func received(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func TestPoolNotifier_FanOutAndCoalesce(t *testing.T) {
	n := runner.NewPoolNotifier()
	a, unsubA := n.Subscribe()
	b, unsubB := n.Subscribe()
	defer unsubB()

	n.Notify()
	n.Notify() // never blocks; coalesces into the pending signal
	assert.True(t, received(a))
	assert.True(t, received(b))
	assert.False(t, received(a), "two notifies before a read coalesce into one wake")

	unsubA()
	n.Notify()
	assert.False(t, received(a), "unsubscribed channels are not signalled")
	assert.True(t, received(b))
}

func TestPoolNotifier_NotifyWithoutSubscribersIsSafe(t *testing.T) {
	assert.NotPanics(t, func() { runner.NewPoolNotifier().Notify() })
}
