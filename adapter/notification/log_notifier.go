// Package notification provides adapter implementations for notification ports.
package notification

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/notification"
)

// LogNotifier is an ExternalNotifier that logs notifications at Info level.
// It is intended as a default/development adapter and always returns nil.
type LogNotifier struct {
	logger *slog.Logger
}

// NewLogNotifier constructs a LogNotifier with the given structured logger.
func NewLogNotifier(logger *slog.Logger) *LogNotifier {
	return &LogNotifier{logger: logger}
}

// Notify logs the notification with structured fields and returns nil.
func (l *LogNotifier) Notify(_ context.Context, n *notification.Notification) error {
	l.logger.Info("external notification",
		"notification_id", n.ID.String(),
		"instance_id", n.InstanceID.String(),
		"workflow_name", n.WorkflowName,
		"type", string(n.Type),
		"timestamp", n.CreatedAt.Format("2006-01-02T15:04:05.000Z07:00"),
	)
	return nil
}
