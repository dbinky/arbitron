package notification

import (
	"log/slog"

	"github.com/arbitron/arbitron/domain/notification"
)

// NewDefaultExternalNotifier builds the standard fan-out external notifier: the
// development LogNotifier plus a best-effort TeamsNotifier. The TeamsNotifier
// reads the live webhook config through the synchronized settings port, so an
// admin toggle takes effect on the next notification. publicBaseURL is static
// (set via config/env only) and used to build deep links in the Teams card.
func NewDefaultExternalNotifier(settings notification.TeamsSettings, publicBaseURL string, logger *slog.Logger) notification.ExternalNotifier {
	logNotifier := NewLogNotifier(logger)
	teamsNotifier := NewTeamsNotifier(settings, publicBaseURL, nil, logger)
	return NewMultiNotifier(logger, logNotifier, teamsNotifier)
}
