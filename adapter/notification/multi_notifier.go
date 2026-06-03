package notification

import (
	"context"
	"log/slog"

	"github.com/arbitron/arbitron/domain/notification"
)

// Compile-time assertion that *MultiNotifier satisfies the port.
var _ notification.ExternalNotifier = (*MultiNotifier)(nil)

// MultiNotifier fans a notification out to each child ExternalNotifier in
// order. Individual child errors are logged and swallowed so one failing
// channel never prevents the others; Notify always returns nil.
type MultiNotifier struct {
	children []notification.ExternalNotifier
	logger   *slog.Logger
}

// NewMultiNotifier constructs a MultiNotifier over the given children. A nil
// logger defaults to a discard logger so the per-child failure path never panics.
func NewMultiNotifier(logger *slog.Logger, children ...notification.ExternalNotifier) *MultiNotifier {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &MultiNotifier{children: children, logger: logger}
}

// Notify invokes every child in order, logging and swallowing each error.
func (m *MultiNotifier) Notify(ctx context.Context, n *notification.Notification) error {
	for _, child := range m.children {
		if err := child.Notify(ctx, n); err != nil {
			m.logger.Warn("external notifier child failed", "error", err)
		}
	}
	return nil
}
