package queue

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The hook is OPTIONAL and nil-safe, the same pattern SetWebhookRegistry and
// SetScheduler already use. domain/queue cannot import domain/engine, so the
// composition root supplies the engine's wake as a function value.
func TestManager_ChildTerminalHook_IsNilSafe(t *testing.T) {
	m := &Manager{}
	// Must not panic with no hook wired.
	require.NotPanics(t, func() { m.notifyChildTerminal(context.Background(), uuid.New()) })
}

func TestManager_SetChildTerminalHook_IsInvoked(t *testing.T) {
	var got uuid.UUID
	calls := 0
	m := &Manager{}
	m.SetChildTerminalHook(func(_ context.Context, id uuid.UUID) {
		calls++
		got = id
	})

	want := uuid.New()
	m.notifyChildTerminal(context.Background(), want)

	assert.Equal(t, 1, calls)
	assert.Equal(t, want, got, "the hook must receive the id of the instance that went terminal")
}
