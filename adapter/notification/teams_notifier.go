package notification

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
)

// Compile-time assertion that *TeamsNotifier satisfies the port.
var _ notification.ExternalNotifier = (*TeamsNotifier)(nil)

// defaultTeamsTimeout bounds a single webhook POST so a hung endpoint cannot
// stall the notification path.
const defaultTeamsTimeout = 10 * time.Second

// TeamsNotifier forwards notifications to a Microsoft Teams Workflows webhook.
// Delivery is best-effort and silent: gating and failures never produce an
// error, so the engine's notification path is never blocked.
type TeamsNotifier struct {
	settings      notification.TeamsSettings // narrow live-read port (synchronized)
	publicBaseURL string
	client        *http.Client
	logger        *slog.Logger
}

// NewTeamsNotifier constructs a TeamsNotifier reading its live webhook config
// through the narrow settings port. A nil client gets a default client with a
// bounded timeout; a nil logger defaults to a discard logger so the best-effort
// failure paths never panic.
func NewTeamsNotifier(settings notification.TeamsSettings, publicBaseURL string, client *http.Client, logger *slog.Logger) *TeamsNotifier {
	if client == nil {
		client = &http.Client{Timeout: defaultTeamsTimeout}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &TeamsNotifier{
		settings:      settings,
		publicBaseURL: publicBaseURL,
		client:        client,
		logger:        logger,
	}
}

// Notify reads the live Teams config and, when enabled with a URL, POSTs an
// Adaptive Card to the webhook. Disabled/empty is a clean no-op. Any build,
// transport, or non-2xx error is logged and swallowed; Notify always nil.
func (t *TeamsNotifier) Notify(ctx context.Context, n *notification.Notification) error {
	webhookURL, enabled := t.settings.TeamsWebhook()
	if !enabled || webhookURL == "" {
		return nil
	}

	payload, err := buildTeamsPayload(n, t.publicBaseURL)
	if err != nil {
		t.logger.Warn("teams: failed to build payload", "error", err)
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		// SECURITY: the webhook URL is a bearer secret (the auth token lives in
		// the path/query, e.g. ?sig=...). Never log the raw error — *url.Error
		// embeds the full URL — and req may be nil here, so emit a static message.
		t.logger.Warn("teams: failed to build request")
		return nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// SECURITY: scrub to host only. http.Client.Do returns a *url.Error whose
		// Error() preserves the ?sig=... token verbatim (net/http masks only
		// userinfo passwords), so logging the raw error leaks the secret into the
		// log file — precisely when the endpoint is unreachable.
		t.logger.Warn("teams: webhook delivery failed", "host", req.URL.Hostname())
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.logger.Warn("teams: webhook returned non-success status", "status", resp.StatusCode)
		return nil
	}
	return nil
}
