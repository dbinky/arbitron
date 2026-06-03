package notification

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
)

// Compile-time assertion that *TeamsNotifier satisfies the port.
var _ notification.ExternalNotifier = (*TeamsNotifier)(nil)

// defaultTeamsTimeout bounds a single webhook POST so a hung endpoint cannot
// stall delivery indefinitely.
const defaultTeamsTimeout = 10 * time.Second

// TeamsNotifier forwards notifications to a Microsoft Teams Workflows webhook.
// Delivery is best-effort and silent: gating and failures never produce an
// error, so the engine's notification path is never blocked.
type TeamsNotifier struct {
	settings      notification.TeamsSettings // narrow live-read port (synchronized)
	publicBaseURL string
	client        *http.Client
	logger        *slog.Logger

	wg sync.WaitGroup
	// dispatch runs a delivery unit of work. It defaults to a goroutine so the
	// webhook POST never blocks the caller (the engine run loop); tests may swap
	// in a synchronous implementation for determinism.
	dispatch func(func())
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
		dispatch:      func(work func()) { go work() },
	}
}

// Notify reads the live Teams config and, when enabled with a URL, dispatches a
// best-effort POST of an Adaptive Card to the webhook OFF the caller goroutine.
// Disabled/empty is a clean no-op. The gating and payload build run
// synchronously; the network I/O is fire-and-forget so a slow or hung webhook
// never delays the engine's instance-completion signalling or workspace
// teardown. Notify always returns nil.
func (t *TeamsNotifier) Notify(_ context.Context, n *notification.Notification) error {
	webhookURL, enabled := t.settings.TeamsWebhook()
	if !enabled || webhookURL == "" {
		return nil
	}

	payload, err := buildTeamsPayload(n, t.publicBaseURL)
	if err != nil {
		t.logger.Warn("teams: failed to build payload", "error", err)
		return nil
	}

	t.wg.Add(1)
	t.dispatch(func() {
		defer t.wg.Done()
		t.deliver(payload, webhookURL)
	})
	return nil
}

// Wait blocks until all in-flight best-effort deliveries finish. The engine
// never calls it (delivery is fire-and-forget); it exists as a deterministic
// sync seam for the test suite and the -race gate.
func (t *TeamsNotifier) Wait() {
	t.wg.Wait()
}

// deliver performs one webhook POST on its own detached, bounded context so
// engine-context cancellation (e.g. instance completion / workspace cleanup)
// never aborts an in-flight delivery, and a hung endpoint is still bounded.
func (t *TeamsNotifier) deliver(payload []byte, webhookURL string) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTeamsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		// SECURITY: the webhook URL is a bearer secret (the auth token lives in
		// the path/query, e.g. ?sig=...). Never log the raw error — *url.Error
		// embeds the full URL — and req may be nil here, so emit a static message.
		t.logger.Warn("teams: failed to build request")
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		// SECURITY: scrub to host only. http.Client.Do returns a *url.Error whose
		// Error() preserves the ?sig=... token verbatim (net/http masks only
		// userinfo passwords), so logging the raw error leaks the secret into the
		// log file — precisely when the endpoint is unreachable.
		t.logger.Warn("teams: webhook delivery failed", "host", req.URL.Hostname())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.logger.Warn("teams: webhook returned non-success status", "status", resp.StatusCode)
	}
}
