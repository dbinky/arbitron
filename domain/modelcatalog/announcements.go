package modelcatalog

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/settings"
)

var (
	ErrNoPendingAnnouncements = errors.New("model announcement: no due events")
	ErrAnnouncementClaimLost  = errors.New("model announcement: claim lost")
	ErrAnnouncementState      = errors.New("model announcement: invalid transition")
)

type announcementSettings interface {
	ReadInternal(context.Context) (settings.Values, error)
}
type announcementClock interface{ Now() time.Time }

const (
	announcementLease       = time.Minute
	announcementSendTimeout = 15 * time.Second
)

// AnnouncementService drains frozen durable events, never a report's delta.
// Its collaborators are mandatory; the root supplies the shared clock and raw
// internal settings reader, independently of discovery success.
type AnnouncementService struct {
	store    AnnouncementStore
	settings announcementSettings
	sender   ModelAnnouncementSender
	clock    announcementClock
	draining chan struct{}
}

func NewAnnouncementService(store AnnouncementStore, settings announcementSettings, sender ModelAnnouncementSender, clock announcementClock) *AnnouncementService {
	if store == nil || settings == nil || sender == nil || clock == nil {
		panic("model announcement: missing dependency")
	}
	return &AnnouncementService{store: store, settings: settings, sender: sender, clock: clock, draining: make(chan struct{}, 1)}
}

func (s *AnnouncementService) Announce(ctx context.Context, _ RefreshReport) error {
	return s.Drain(ctx)
}

func (s *AnnouncementService) Drain(ctx context.Context) error {
	select {
	case s.draining <- struct{}{}:
		defer func() { <-s.draining }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := s.store.RecoverClaims(ctx, s.clock.Now()); err != nil {
		return errors.New("model announcement: claim recovery failed")
	}
	values, err := s.settings.ReadInternal(ctx)
	if err != nil {
		return errors.New("model announcement: settings unavailable")
	}
	if err = s.store.ClassifyAwaiting(ctx, values.TeamsWebhookURL != ""); err != nil {
		return errors.New("model announcement: classification failed")
	}
	if values.TeamsWebhookURL == "" {
		return nil
	}
	var failures []error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		e, err := s.store.ClaimPending(ctx, s.clock.Now(), announcementLease)
		if errors.Is(err, ErrNoPendingAnnouncements) {
			return errors.Join(failures...)
		}
		if errors.Is(err, ErrAnnouncementPayload) {
			// The store has deferred this invalid payload with durable backoff.
			failures = append(failures, ErrAnnouncementPayload)
			continue
		}
		if err != nil {
			return errors.Join(append(failures, errors.New("model announcement: claim failed"))...)
		}
		paused, err := s.deliver(ctx, e)
		if err != nil {
			failures = append(failures, err)
		}
		if paused {
			return errors.Join(failures...)
		}
	}
}

func (s *AnnouncementService) deliver(ctx context.Context, e Event) (bool, error) {
	if e.Payload.EventID == "" || e.ClaimID == "" || e.State != EventInFlight || e.PartCount < 1 || len(e.Parts) != e.PartCount {
		return true, ErrAnnouncementState
	}
	for _, part := range e.Parts {
		if slices.Contains(e.ConfirmedParts, part.Index) {
			continue
		}
		if part.EventID != e.Payload.EventID || part.Count != e.PartCount || part.Index < 1 || part.Index > e.PartCount {
			return true, ErrAnnouncementState
		}
		values, err := s.settings.ReadInternal(ctx)
		if err != nil || values.TeamsWebhookURL == "" {
			// Neither a cleared destination nor a settings fault attempts a send.
			// Release the claim without charging send-failure backoff.
			outcome := SendOutcome{State: SendDeferred, Code: "destination_empty"}
			if err != nil {
				outcome = SendOutcome{State: SendDeferred, Code: "settings_unavailable"}
			}
			if completeErr := s.store.Complete(ctx, e.Payload.EventID, e.ClaimID, outcome, s.clock.Now()); completeErr != nil {
				return true, errors.New("model announcement: pause marker failed")
			}
			if err != nil {
				return true, errors.New("model announcement: settings unavailable")
			}
			return true, nil
		}
		if err = s.store.RenewClaim(ctx, e.Payload.EventID, e.ClaimID, s.clock.Now(), announcementLease); err != nil {
			return true, errors.New("model announcement: claim renewal failed")
		}
		sendCtx, cancel := context.WithTimeout(ctx, announcementSendTimeout)
		outcome, sendErr := s.sender.SendPart(sendCtx, values.TeamsWebhookURL, part)
		cancel()
		if (sendErr != nil && outcome.State == SendConfirmed) || (outcome.State != SendConfirmed && outcome.State != SendDefiniteFailure && outcome.State != SendAmbiguous) {
			outcome = SendOutcome{State: SendAmbiguous, Code: "transport_uncertain"}
		}
		if err = s.store.CompletePart(ctx, e.Payload.EventID, e.ClaimID, part.Index, outcome, s.clock.Now()); err != nil {
			return true, errors.New("model announcement: delivery marker failed")
		}
		if outcome.State != SendConfirmed {
			return false, errors.New("model announcement: delivery retry scheduled")
		}
	}
	return false, nil
}

// AnnouncementStore owns atomic delivery transitions independently of discovery.
// All attempt times come from the caller's shared clock.
type AnnouncementStore interface {
	ClassifyAwaiting(context.Context, bool) error
	ClaimPending(context.Context, time.Time, time.Duration) (Event, error)
	Complete(context.Context, string, string, SendOutcome, time.Time) error
	CompletePart(context.Context, string, string, int, SendOutcome, time.Time) error
	RecoverClaims(context.Context, time.Time) error
	RenewClaim(context.Context, string, string, time.Time, time.Duration) error
}
