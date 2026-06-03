package notification

import (
	"log/slog"

	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/notification"
)

// NewDefaultExternalNotifier builds the standard fan-out external notifier: the
// development LogNotifier plus a best-effort TeamsNotifier. The TeamsNotifier
// reads cfg live, so an admin toggle takes effect on the next notification.
func NewDefaultExternalNotifier(cfg *config.Config, logger *slog.Logger) notification.ExternalNotifier {
	logNotifier := NewLogNotifier(logger)
	teamsNotifier := NewTeamsNotifier(cfg, cfg.Server.PublicBaseURL, nil, logger)
	return NewMultiNotifier(logger, logNotifier, teamsNotifier)
}
