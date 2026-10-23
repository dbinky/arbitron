package notification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/arbitron/arbitron/adapter/outbound"
	"github.com/arbitron/arbitron/domain/modelcatalog"
)

const modelAnnouncementTimeout = 15 * time.Second

var _ modelcatalog.ModelAnnouncementSender = (*TeamsModelSender)(nil)

// TeamsModelSender performs one bounded synchronous attempt. Retries and durable
// delivery markers belong to the announcement service, not this HTTP adapter.
type TeamsModelSender struct{ client *http.Client }

// NewTeamsModelSender requires a client carrying the shared outbound guard in
// production; tests may substitute the I/O leaf. Clone the client so enforcing
// no redirects and a deadline cannot change other users of the shared client.
func NewTeamsModelSender(client *http.Client) *TeamsModelSender {
	if client == nil {
		panic("model announcement: nil guarded HTTP client")
	}
	bounded := *client
	if bounded.Timeout <= 0 || bounded.Timeout > modelAnnouncementTimeout {
		bounded.Timeout = modelAnnouncementTimeout
	}
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &TeamsModelSender{client: &bounded}
}

func (s *TeamsModelSender) Send(ctx context.Context, destination string, p modelcatalog.AnnouncementPayload) (modelcatalog.SendOutcome, error) {
	return s.SendPart(ctx, destination, modelcatalog.AnnouncementPart{EventID: p.EventID, Index: 1, Count: 1, Payload: p})
}

func (s *TeamsModelSender) SendPart(ctx context.Context, destination string, part modelcatalog.AnnouncementPart) (modelcatalog.SendOutcome, error) {
	failure := modelcatalog.SendOutcome{State: modelcatalog.SendDefiniteFailure}
	if destination == "" {
		failure.Code = "destination_empty"
		return failure, errors.New("model announcement: destination empty")
	}
	raw, err := buildModelAnnouncement(part)
	if err != nil {
		failure.Code = "payload_invalid"
		return failure, err
	}
	if len(raw) > modelcatalog.MaxAnnouncementBytes {
		failure.Code = "payload_too_large"
		return failure, modelcatalog.ErrAnnouncementPayload
	}
	ctx, cancel := context.WithTimeout(ctx, modelAnnouncementTimeout)
	defer cancel()
	// No GetBody: net/http must not replay a POST, including on a reused connection.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destination, io.NopCloser(bytes.NewReader(raw)))
	if err != nil || req.URL.Hostname() == "" || (req.URL.Scheme != "http" && req.URL.Scheme != "https") || req.URL.User != nil {
		failure.Code = "destination_invalid"
		return failure, errors.New("model announcement: destination invalid")
	}
	req.ContentLength = int64(len(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, outbound.ErrBlockedDialTarget) || errors.Is(err, outbound.ErrUnsafeURL) {
			failure.Code = "destination_blocked"
			return failure, errors.New("model announcement: destination blocked")
		}
		// Do not expose url.Error or a transport's message: both can contain the
		// bearer destination. Without a status, possible write/acceptance is unknown.
		return modelcatalog.SendOutcome{State: modelcatalog.SendAmbiguous, Code: "transport_uncertain"}, errors.New("model announcement: transport outcome uncertain")
	}
	// Headers alone establish D7 confirmation. Never interpret the response as a
	// receipt; body size, read and close faults cannot revoke a received 2xx.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, modelcatalog.MaxAnnouncementBytes))
	_ = resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return modelcatalog.SendOutcome{State: modelcatalog.SendConfirmed, Code: "http_2xx"}, nil
	}
	return modelcatalog.SendOutcome{State: modelcatalog.SendDefiniteFailure, Code: fmt.Sprintf("http_%d", resp.StatusCode)}, nil
}
