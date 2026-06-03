package notification_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/notification"
)

// stubNotifier records calls and optionally returns an error.
type stubNotifier struct {
	calls int
	err   error
}

func (s *stubNotifier) Notify(_ context.Context, _ *notification.Notification) error {
	s.calls++
	return s.err
}

// Success: every child notifier is invoked exactly once.
func TestMultiNotifier_Notify_InvokesEveryChild(t *testing.T) {
	a := &stubNotifier{}
	b := &stubNotifier{}
	m := adaptnotif.NewMultiNotifier(discardLogger(), a, b)

	require.NoError(t, m.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
}

// Error: a child erroring is logged and swallowed; later children still run.
func TestMultiNotifier_Notify_OneChildErrorStillRunsOthers(t *testing.T) {
	a := &stubNotifier{err: errors.New("boom")}
	b := &stubNotifier{}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	m := adaptnotif.NewMultiNotifier(logger, a, b)

	require.NoError(t, m.Notify(context.Background(), sampleNotification()))
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
	assert.Contains(t, logBuf.String(), "boom")
}

// Edge-case: no children is a clean no-op.
func TestMultiNotifier_Notify_NoChildren(t *testing.T) {
	m := adaptnotif.NewMultiNotifier(discardLogger())
	require.NoError(t, m.Notify(context.Background(), sampleNotification()))
}
