package notification

import "context"

// ExternalNotifier is a port for sending notifications to external systems
// (e.g., email, Slack, Teams). Implementations live in the adapter layer.
type ExternalNotifier interface {
	Notify(ctx context.Context, notification *Notification) error
}
