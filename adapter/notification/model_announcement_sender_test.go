package notification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/outbound"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func announcementFixture() modelcatalog.AnnouncementPayload {
	return modelcatalog.AnnouncementPayload{
		EventID: "event-original", CanonicalID: "vendor/model-2", Provider: "vendor", Tier: "premium",
		FirstConfirmedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		Paths: []modelcatalog.UsabilityEvidence{
			{PathKey: "path-a", ConfigRevision: "config-a", ProofRef: "/private/account/secret-proof"},
			{PathKey: "path-b", ConfigRevision: "config-b", ProofRef: "https://catalog.invalid/?secret=proof"},
		},
		Changes: []provider.Advancement{
			{Alias: "writer", FromModel: "vendor/model-1", ToModel: "vendor/model-2"},
			{Alias: "reviewer", FromModel: "vendor/model-0", ToModel: "vendor/model-2"},
		},
	}
}

func decodeModelCard(t *testing.T, raw []byte) []cardFact {
	t.Helper()
	var msg teamsMessage
	require.NoError(t, json.Unmarshal(raw, &msg))
	require.Equal(t, "message", msg.Type)
	require.Len(t, msg.Attachments, 1)
	require.Equal(t, "application/vnd.microsoft.card.adaptive", msg.Attachments[0].ContentType)
	card := msg.Attachments[0].Content
	assert.Equal(t, "AdaptiveCard", card.Type)
	assert.Empty(t, card.Actions, "a card must not turn untrusted model names into actions")
	var facts []cardFact
	for _, block := range card.Body {
		facts = append(facts, block.Facts...)
	}
	return facts
}

func TestModelCardOriginalChanges(t *testing.T) {
	payload := announcementFixture()
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/exact/secret?sig=destination", r.URL.RequestURI())
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Empty(t, r.Header.Get("Idempotency-Key"))
		var err error
		received, err = io.ReadAll(r.Body)
		assert.NoError(t, err)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	outcome, err := NewTeamsModelSender(server.Client()).Send(context.Background(), server.URL+"/exact/secret?sig=destination", payload)
	require.NoError(t, err)
	assert.Equal(t, "confirmed", outcome.State)
	facts := decodeModelCard(t, received)
	for _, fact := range []cardFact{
		{Title: "Event", Value: payload.EventID}, {Title: "Provider", Value: payload.Provider},
		{Title: "Model", Value: payload.CanonicalID}, {Title: "Tier", Value: payload.Tier},
		{Title: "First confirmed", Value: payload.FirstConfirmedAt.Format(time.RFC3339Nano)},
		{Title: "Path", Value: "path-a (config-a)"}, {Title: "Path", Value: "path-b (config-b)"},
		{Title: "writer", Value: "vendor/model-1 → vendor/model-2"},
		{Title: "reviewer", Value: "vendor/model-0 → vendor/model-2"},
	} {
		assert.Contains(t, facts, fact)
	}
	for _, path := range payload.Paths {
		assert.NotContains(t, string(received), path.ProofRef)
	}
	assert.NotContains(t, string(received), "destination")
}

func TestModelCardNoFollowersAndHostileNames(t *testing.T) {
	p := announcementFixture()
	p.Tier = ""
	p.Changes = nil
	p.CanonicalID = "<script>[click](https://evil.invalid)_*"
	var raw []byte
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { raw, _ = io.ReadAll(r.Body); w.WriteHeader(204) }))
	defer s.Close()
	outcome, err := NewTeamsModelSender(s.Client()).Send(context.Background(), s.URL, p)
	require.NoError(t, err)
	assert.Equal(t, "confirmed", outcome.State)
	facts := decodeModelCard(t, raw)
	assert.Contains(t, facts, cardFact{Title: "Tier", Value: "unknown"})
	assert.Contains(t, facts, cardFact{Title: "Profiles", Value: "No profiles changed"})
	assert.Contains(t, facts, cardFact{Title: "Model", Value: `&lt;script&gt;\[click\]\(https://evil.invalid\)\_\*`})
}

func TestModelAnnouncementWaitsForResponse(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(201) }))
	defer s.Close()
	done := make(chan modelcatalog.SendOutcome, 1)
	go func() {
		out, _ := NewTeamsModelSender(s.Client()).Send(context.Background(), s.URL, announcementFixture())
		done <- out
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not reach the receiver")
	}
	select {
	case <-done:
		t.Error("sender returned before the HTTP outcome was known")
	default:
	}
	close(release)
	assert.Equal(t, "confirmed", (<-done).State)
}

func TestModelAnnouncementStatusAndResponseBounds(t *testing.T) {
	for _, status := range []int{200, 201, 202, 204, 302, 400, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/do-not-follow")
				w.WriteHeader(status)
				if status != 204 {
					_, _ = io.WriteString(w, strings.Repeat("response-body-secret", 4000))
				}
			}))
			defer s.Close()
			out, err := NewTeamsModelSender(s.Client()).Send(context.Background(), s.URL+"/?sig=private", announcementFixture())
			require.NoError(t, err)
			if status < 300 {
				assert.Equal(t, "confirmed", out.State)
			} else {
				assert.Equal(t, "definite_failure", out.State)
			}
			assert.EqualValues(t, 1, calls.Load(), "no redirect or automatic retry")
			assert.NotContains(t, out.Code, "response-body-secret")
		})
	}
}

type modelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type faultModelBody struct {
	reads  int
	bytes  int
	closed bool
}

func (b *faultModelBody) Read(p []byte) (int, error) {
	b.reads++
	b.bytes += len(p)
	return 0, errors.New("secret response read fault")
}
func (b *faultModelBody) Close() error {
	b.closed = true
	return errors.New("secret response close fault")
}

func TestModelAnnouncementConfirmedDespiteBodyFault(t *testing.T) {
	for _, status := range []int{202, 204} {
		body := &faultModelBody{}
		client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: body, Header: http.Header{}}, nil
		})}
		out, err := NewTeamsModelSender(client).Send(context.Background(), "https://receiver.invalid/secret", announcementFixture())
		require.NoError(t, err)
		assert.Equal(t, "confirmed", out.State)
		assert.True(t, body.closed)
		assert.LessOrEqual(t, body.bytes, modelcatalog.MaxAnnouncementBytes+1)
	}
}

func TestModelAnnouncementAcceptedThenLostMayDuplicate(t *testing.T) {
	bodies := make(chan []byte, 3)
	var attempts atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies <- raw
		if attempts.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			assert.NoError(t, err)
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		w.WriteHeader(202)
	}))
	defer s.Close()
	sender := NewTeamsModelSender(s.Client())
	first, err := sender.Send(context.Background(), s.URL+"/secret", announcementFixture())
	require.Error(t, err)
	assert.Equal(t, "ambiguous", first.State)
	assert.NotContains(t, err.Error(), s.URL)
	require.Len(t, bodies, 1, "no automatic POST retry")
	firstBody := <-bodies
	second, err := sender.Send(context.Background(), s.URL+"/secret", announcementFixture())
	require.NoError(t, err)
	assert.Equal(t, "confirmed", second.State)
	require.EqualValues(t, 2, attempts.Load(), "the receiver does not deduplicate EventID")
	assert.Equal(t, firstBody, <-bodies)
}

func TestModelAnnouncementTimeoutAndSecretFreeErrors(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release }))
	defer s.Close()
	defer close(release)
	client := s.Client()
	client.Timeout = 50 * time.Millisecond
	out, err := NewTeamsModelSender(client).Send(context.Background(), s.URL+"/secret?sig=private", announcementFixture())
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not reach the receiver")
	}
	require.Error(t, err)
	assert.Equal(t, "ambiguous", out.State)
	assert.NotContains(t, err.Error(), "private")
	assert.NotContains(t, err.Error(), s.URL)
}

func TestModelAnnouncementGuardsAndPayloadLimit(t *testing.T) {
	var calls int
	client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("transport secret ?sig=private")
	})}
	sender := NewTeamsModelSender(client)
	for _, destination := range []string{"", "://invalid?sig=private", "https://user:private@receiver.invalid/secret"} {
		out, err := sender.Send(context.Background(), destination, announcementFixture())
		require.Error(t, err)
		assert.Equal(t, "definite_failure", out.State)
		assert.NotContains(t, err.Error(), "private")
	}
	p := announcementFixture()
	p.Changes[0].Alias = strings.Repeat("x", modelcatalog.MaxAnnouncementBytes)
	out, err := sender.Send(context.Background(), "https://receiver.invalid/secret", p)
	require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
	assert.Equal(t, "definite_failure", out.State)
	assert.Zero(t, calls, "invalid or oversized input must not send")
	for _, destination := range []string{"http://127.0.0.1/secret?sig=private", "http://169.254.169.254/secret", "file:///private"} {
		out, err := NewTeamsModelSender(outbound.NewGuardedClient(time.Second, 0)).Send(context.Background(), destination, announcementFixture())
		require.Error(t, err)
		assert.Equal(t, "definite_failure", out.State)
		assert.NotContains(t, err.Error(), "secret")
		assert.NotContains(t, err.Error(), "private")
	}
}

func TestModelAnnouncementPartitionsReachReceiverWithoutLoss(t *testing.T) {
	p := announcementFixture()
	p.Paths, p.Changes = nil, nil
	for i := 0; i < 350; i++ {
		p.Paths = append(p.Paths, modelcatalog.UsabilityEvidence{PathKey: fmt.Sprintf("path-%03d-%s", i, strings.Repeat("a", 70)), ConfigRevision: "config", ProofRef: "secret-proof"})
		p.Changes = append(p.Changes, provider.Advancement{Alias: fmt.Sprintf("alias-%03d", i), FromModel: strings.Repeat(`<old>\`, 15), ToModel: "new"})
	}
	parts, err := PartitionAnnouncement(p, modelcatalog.MaxAnnouncementBytes)
	require.NoError(t, err)
	require.Greater(t, len(parts), 1)
	again, err := PartitionAnnouncement(p, modelcatalog.MaxAnnouncementBytes)
	require.NoError(t, err)
	assert.Equal(t, parts, again, "partitioning must be deterministic")
	var gotChanges, gotPaths []cardFact
	var count int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		raw, _ := io.ReadAll(r.Body)
		assert.LessOrEqual(t, len(raw), modelcatalog.MaxAnnouncementBytes)
		assert.NotContains(t, string(raw), "secret-proof")
		facts := decodeModelCard(t, raw)
		assert.Contains(t, facts, cardFact{Title: "Event", Value: p.EventID})
		assert.Contains(t, facts, cardFact{Title: "Part", Value: fmt.Sprintf("%d/%d", count, len(parts))})
		for _, f := range facts {
			if strings.HasPrefix(f.Title, "alias-") {
				gotChanges = append(gotChanges, f)
			}
			if f.Title == "Path" {
				gotPaths = append(gotPaths, f)
			}
		}
		w.WriteHeader(200)
	}))
	defer s.Close()
	sender := NewTeamsModelSender(s.Client())
	for i, part := range parts {
		assert.Equal(t, p.EventID, part.EventID)
		assert.Equal(t, i+1, part.Index)
		assert.Equal(t, len(parts), part.Count)
		out, err := sender.SendPart(context.Background(), s.URL+"/exact-secret", part)
		require.NoError(t, err)
		require.Equal(t, "confirmed", out.State)
	}
	require.Len(t, gotChanges, len(p.Changes))
	for i, change := range p.Changes {
		assert.Equal(t, cardFact{Title: change.Alias, Value: strings.Repeat(`&lt;old&gt;\\`, 15) + " → new"}, gotChanges[i])
	}
	require.Len(t, gotPaths, len(p.Paths))
	for i, path := range p.Paths {
		assert.Equal(t, cardFact{Title: "Path", Value: path.PathKey + " (config)"}, gotPaths[i])
	}
}

func TestModelAnnouncementClientBoundsAndResponseReadCap(t *testing.T) {
	body := &countModelBody{remaining: 2 * modelcatalog.MaxAnnouncementBytes}
	originalRedirect := func(*http.Request, []*http.Request) error { return errors.New("original redirect policy") }
	client := &http.Client{Timeout: time.Minute, CheckRedirect: originalRedirect, Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		assert.Nil(t, r.GetBody, "POST must not be replayable by the transport")
		assert.LessOrEqual(t, time.Until(deadlineOf(r.Context())), 15*time.Second)
		return &http.Response{StatusCode: 202, Body: body, Header: http.Header{}}, nil
	})}
	sender := NewTeamsModelSender(client)
	assert.Equal(t, 15*time.Second, sender.client.Timeout)
	assert.Equal(t, time.Minute, client.Timeout, "constructor must not mutate a shared client")
	assert.EqualError(t, client.CheckRedirect(nil, nil), "original redirect policy")
	out, err := sender.Send(context.Background(), "https://receiver.invalid/secret", announcementFixture())
	require.NoError(t, err)
	assert.Equal(t, "confirmed", out.State)
	assert.LessOrEqual(t, body.read, modelcatalog.MaxAnnouncementBytes)
	assert.True(t, body.closed)
	assert.Panics(t, func() { NewTeamsModelSender(nil) }, "a missing guarded client is a construction error")
}

func deadlineOf(ctx context.Context) time.Time {
	deadline, _ := ctx.Deadline()
	return deadline
}

type countModelBody struct {
	remaining, read int
	closed          bool
}

func (b *countModelBody) Read(p []byte) (int, error) {
	n := min(len(p), b.remaining)
	clear(p[:n])
	b.remaining -= n
	b.read += n
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}
func (b *countModelBody) Close() error { b.closed = true; return nil }

func TestModelAnnouncementRejectsMissingPayloadAndRedactsTransport(t *testing.T) {
	var calls int
	client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, &url.Error{Op: "Post", URL: r.URL.String(), Err: errors.New("raw transport secret")}
	})}
	sender := NewTeamsModelSender(client)
	for _, field := range []string{"event", "canonical", "provider", "time", "path", "config", "alias", "from", "to"} {
		p := announcementFixture()
		switch field {
		case "event":
			p.EventID = ""
		case "canonical":
			p.CanonicalID = ""
		case "provider":
			p.Provider = ""
		case "time":
			p.FirstConfirmedAt = time.Time{}
		case "path":
			p.Paths[0].PathKey = ""
		case "config":
			p.Paths[0].ConfigRevision = ""
		case "alias":
			p.Changes[0].Alias = ""
		case "from":
			p.Changes[0].FromModel = ""
		case "to":
			p.Changes[0].ToModel = ""
		}
		_, err := sender.Send(context.Background(), "https://receiver.invalid/secret?sig=private", p)
		require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload, field)
	}
	assert.Zero(t, calls)
	out, err := sender.Send(context.Background(), "https://receiver.invalid/secret?sig=private", announcementFixture())
	require.Error(t, err)
	assert.Equal(t, "ambiguous", out.State)
	assert.Equal(t, 1, calls)
	assert.NotContains(t, err.Error(), "secret")
	assert.NotContains(t, err.Error(), "private")
	assert.NotContains(t, err.Error(), "receiver.invalid")
}

func TestModelAnnouncementPartitionBoundsAndPartValidation(t *testing.T) {
	p := announcementFixture()
	parts, err := PartitionAnnouncement(p, modelcatalog.MaxAnnouncementBytes)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, p, parts[0].Payload, "single part preserves the immutable payload")
	for _, limit := range []int{0, 20} {
		_, err := PartitionAnnouncement(p, limit)
		require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
	}
	for _, field := range []string{"path", "change"} {
		large := announcementFixture()
		if field == "path" {
			large.Paths[0].PathKey = strings.Repeat("x", modelcatalog.MaxAnnouncementBytes)
		} else {
			large.Changes[0].Alias = strings.Repeat("x", modelcatalog.MaxAnnouncementBytes)
		}
		_, err := PartitionAnnouncement(large, modelcatalog.MaxAnnouncementBytes)
		require.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
		assert.Contains(t, err.Error(), "indivisible")
		assert.NotContains(t, err.Error(), strings.Repeat("x", 100), "diagnostic must not echo oversized/secret content")
	}
	sender := NewTeamsModelSender(&http.Client{Transport: modelRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Error("invalid part sent")
		return nil, errors.New("unexpected")
	})})
	for _, part := range []modelcatalog.AnnouncementPart{
		{EventID: p.EventID, Index: 0, Count: 1, Payload: p},
		{EventID: p.EventID, Index: 2, Count: 1, Payload: p},
		{EventID: "wrong", Index: 1, Count: 1, Payload: p},
	} {
		_, err := sender.SendPart(context.Background(), "https://receiver.invalid/secret", part)
		assert.ErrorIs(t, err, modelcatalog.ErrAnnouncementPayload)
	}
}
