package notification

import "context"

// ExternalNotifier is a port for sending notifications to external systems
// (e.g., email, Slack, Teams). Implementations live in the adapter layer.
type ExternalNotifier interface {
	Notify(ctx context.Context, notification *Notification) error
}

// TeamsSettings is the narrow read port the Teams notifier depends on: it needs
// only the live webhook URL and enable flag, not the entire domain Config. The
// synchronized config.SettingsStore satisfies it, so the accessor reads under
// the store mutex and the notifier's former live-read race is gone. Keeping the
// dependency this small (ISP/DIP) also makes the notifier unit-testable with a
// one-line fake.
type TeamsSettings interface {
	TeamsWebhook() (url string, enabled bool)
}
