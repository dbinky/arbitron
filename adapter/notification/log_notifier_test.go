package notification_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	adaptnotif "github.com/arbitron/arbitron/adapter/notification"
	"github.com/arbitron/arbitron/domain/notification"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogNotifier_Notify_LogsStructuredFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	notifier := adaptnotif.NewLogNotifier(logger)

	n := &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   uuid.New(),
		WorkflowName: "test-workflow",
		Type:         notification.TypeCompleted,
		CreatedAt:    time.Now(),
	}

	err := notifier.Notify(context.Background(), n)
	require.NoError(t, err)

	// Parse the JSON log line and verify structured fields.
	var logEntry map[string]any
	err = json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	assert.Equal(t, "INFO", logEntry["level"])
	assert.Equal(t, "external notification", logEntry["msg"])
	assert.Equal(t, n.ID.String(), logEntry["notification_id"])
	assert.Equal(t, n.InstanceID.String(), logEntry["instance_id"])
	assert.Equal(t, "test-workflow", logEntry["workflow_name"])
	assert.Equal(t, "completed", logEntry["type"])
	assert.NotEmpty(t, logEntry["timestamp"])
}

func TestLogNotifier_Notify_ReturnsNil(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	notifier := adaptnotif.NewLogNotifier(logger)

	n := &notification.Notification{
		ID:           uuid.New(),
		InstanceID:   uuid.New(),
		WorkflowName: "any-workflow",
		Type:         notification.TypeFailed,
		CreatedAt:    time.Now(),
	}

	err := notifier.Notify(context.Background(), n)
	assert.Nil(t, err, "LogNotifier.Notify must always return nil")
}

func TestLogNotifier_ImplementsExternalNotifier(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	notifier := adaptnotif.NewLogNotifier(logger)

	// Compile-time check: *LogNotifier satisfies ExternalNotifier.
	var _ notification.ExternalNotifier = notifier
}

func TestLogNotifier_Notify_AllNotificationTypes(t *testing.T) {
	types := []notification.Type{
		notification.TypeNeedsInteraction,
		notification.TypeFailed,
		notification.TypeCompleted,
	}

	for _, notifType := range types {
		t.Run(string(notifType), func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, nil))
			notifier := adaptnotif.NewLogNotifier(logger)

			n := &notification.Notification{
				ID:           uuid.New(),
				InstanceID:   uuid.New(),
				WorkflowName: "wf-" + string(notifType),
				Type:         notifType,
				CreatedAt:    time.Now(),
			}

			err := notifier.Notify(context.Background(), n)
			require.NoError(t, err)

			var logEntry map[string]any
			err = json.Unmarshal(buf.Bytes(), &logEntry)
			require.NoError(t, err)

			assert.Equal(t, string(notifType), logEntry["type"])
		})
	}
}
