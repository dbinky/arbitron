package provider

import (
	"context"
	"errors"
	"fmt"
)

// ErrAdvancementPortsMissing is returned when AdvanceModel runs before the
// store, publisher, gate and publication journal were injected. A missing port
// is a wiring fault and must never read as a successful no-op.
var ErrAdvancementPortsMissing = errors.New("provider: advancement ports not configured")

// ErrPublicationPending marks an advancement that is durable but whose new
// model has not been confirmed in the live cache or its publication could not be
// acknowledged. The committed Advancement is returned for publication recovery.
var ErrPublicationPending = errors.New("provider: advancement committed but publication pending")

// AdvancementPublisher makes committed advancements visible to new runs and
// confirms it: it returns nil only after the live view carries every committed
// model and revision.
type AdvancementPublisher interface {
	PublishCommitted(context.Context, []Advancement) error
}

// SetAdvancementDependencies injects the conditional store, the confirming
// publisher and the shared execution gate that AdvanceModel needs. It is called
// once from the composition root after all three exist.
func (s *ProfileService) SetAdvancementDependencies(store AdvanceStore, pub AdvancementPublisher, gate *ExecutionGate) {
	s.advance, s.publisher, s.gate = store, pub, gate
}

// SetPublicationJournal supplies durable acknowledgement for direct advances,
// including changes that have no catalog report. The composition root injects
// the store on the same database as the advancement repository.
func (s *ProfileService) SetPublicationJournal(journal PublicationJournal) {
	s.journal = journal
}

// ValidateAdvance checks selection shape, replacement capabilities and fixed
// versions on every actual route. It does not acquire a lease, discover/probe,
// or mutate state. Same-target identity, newer ordering and usability are the
// selector's responsibility; the store separately enforces expected-row CAS.
func (s *ProfileService) ValidateAdvance(ctx context.Context, req AdvanceRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	next, err := NormalizeSelection(req.Expected)
	if err != nil {
		return err
	}
	if next.SelectionMode != SelectionFollowing {
		return fmt.Errorf("%w: profile %q is not following", ErrAdvanceConflict, next.Alias)
	}
	next.Model = req.Model
	// Keep requested axes unchanged; runtime emission applies effort flooring.
	if _, err := s.validateAxes(next); err != nil {
		return err
	}
	return checkRouteVersions(ctx, next, s.versions, s.source)
}

// AdvanceModel is the trusted, unauthenticated automatic path (never
// registered on a public surface). Under the shared gate it rechecks that the
// path evidence req.PathRevision is still current (req.ExecutionRevision must
// be that snapshot's token, re-validated by the store in SQL), then commits the model
// change and its journal row in one conditional store write; only after that
// commit does it publish, so a failed or stale advancement leaves the working
// model in both the database and the cache. A publication failure returns the
// committed Advancement together with ErrPublicationPending. Acknowledgement
// follows confirmed publication; an acknowledgement fault has the same pending
// result and never causes a second model CAS.
// A retry first reads the journal by request identity, so publication can be
// repaired even after the original commit changed the inventory revision.
func (s *ProfileService) AdvanceModel(ctx context.Context, req AdvanceRequest) (Advancement, error) {
	if s.advance == nil || s.publisher == nil || s.gate == nil || s.journal == nil {
		return Advancement{}, ErrAdvancementPortsMissing
	}
	if req.Expected.SelectionMode != SelectionFollowing {
		return Advancement{}, fmt.Errorf("%w: profile %q is not following", ErrAdvanceConflict, req.Expected.Alias)
	}
	// The store validates req.ExecutionRevision inside its transaction; it must
	// be the token of the very snapshot the gate rechecks below.
	if req.ExecutionRevision.ID == "" || req.ExecutionRevision.ID != req.PathRevision {
		return Advancement{}, fmt.Errorf("%w: execution revision token does not match path revision", ErrExecutionRevisionUnavailable)
	}
	var adv Advancement
	var replayed bool
	// A committed change itself moves the inventory revision. Read its bound
	// journal result before requiring fresh evidence for a new write. The read
	// lease also honors shutdown/cancellation without re-entering the gate.
	err := s.gate.WithRead(ctx, func(ctx context.Context) error {
		var err error
		adv, replayed, err = s.advance.ReplayAdvance(ctx, req)
		return err
	})
	if err != nil {
		return Advancement{}, err
	}
	if !replayed {
		// Replays repair the original committed publication even if route
		// validation is now unavailable. Only new writes need validation.
		if err := s.ValidateAdvance(ctx, req); err != nil {
			return Advancement{}, err
		}
		err = s.gate.WithCurrent(ctx, req.PathRevision, func(ctx context.Context) error {
			var err error
			adv, err = s.advance.CommitAdvance(ctx, req)
			return err
		})
		if err != nil {
			return Advancement{}, err
		}
	}
	if err := s.publisher.PublishCommitted(ctx, []Advancement{adv}); err != nil {
		return adv, fmt.Errorf("%w: %w", ErrPublicationPending, err)
	}
	if err := s.journal.Acknowledge(ctx, []string{adv.ID}); err != nil {
		return adv, fmt.Errorf("%w: %w", ErrPublicationPending, err)
	}
	return adv, nil
}
