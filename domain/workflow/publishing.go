package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// PublishRequest carries the parameters for a single publish-into-shared
// transaction handled by writeAndRecord.
type PublishRequest struct {
	// PublishedName is the shared/published name (the destination identity).
	PublishedName string
	// Publisher owns the publication record.
	Publisher uuid.UUID
	// SourceName is recorded as the origin of the published content.
	SourceName string
	// Visibility and Department are stored on the publication record.
	Visibility Visibility
	Department string
	// WriteTree materializes the shared workflow tree at PublishedName. It is
	// invoked once the idempotency decision is made and must be safe to re-run
	// on the overwrite/resume path (it is called with overwrite=true then).
	WriteTree func(ctx context.Context, overwrite bool) error
	// Rollback compensates a FRESH publish whose record step failed by removing
	// the just-written tree. It is never called on the overwrite/resume path.
	Rollback func(ctx context.Context) error
}

// WriteAndRecord is the single owner of the publish-into-shared invariant: a
// shared workflow tree and its published_workflows row must be written together;
// if recording the publication fails on a FRESH publish, the just-written tree
// is rolled back so no orphaned, unattributed shared copy is left behind.
//
// It also owns the idempotency/re-entrancy decision: a name already published by
// the same publisher is an overwrite (a re-publish or a crash-interrupted seed
// resuming), in which case WriteTree is invoked with overwrite=true and the tree
// is NOT rolled back if recording fails. A name published by a different user is
// rejected with ErrUnauthorized.
//
// Both Service.Publish and the samples seeder route through this helper
// so the invariant lives in exactly one place.
func WriteAndRecord(ctx context.Context, store PublishedStore, req PublishRequest) error {
	if store == nil {
		return fmt.Errorf("%w: publishing is not configured", ErrUnauthorized)
	}

	// Publisher-only-overwrite / resume decision.
	pubID, published, err := store.PublisherOf(ctx, req.PublishedName)
	if err != nil {
		return err
	}
	overwrite := false
	if published {
		if pubID != req.Publisher {
			return fmt.Errorf("%w: published name %q owned by another user", ErrUnauthorized, req.PublishedName)
		}
		overwrite = true // re-publish by the same publisher, or a resumed seed
	}

	if err := req.WriteTree(ctx, overwrite); err != nil {
		return err
	}
	// Record the publication after the tree is in place (write-then-record).
	if err := store.Upsert(ctx, req.PublishedName, req.Publisher, req.SourceName, req.Visibility, req.Department); err != nil {
		if !overwrite {
			// FRESH path: compensate by deleting the just-written tree. The
			// destination was empty before, so this restores the prior state. The
			// rollback is best-effort; if it also fails, surface the original Upsert
			// error joined with the rollback error so neither is lost.
			if delErr := req.rollback(ctx); delErr != nil {
				return errors.Join(
					fmt.Errorf("recording publication for %q: %w", req.PublishedName, err),
					fmt.Errorf("rolling back shared tree %q: %w", req.PublishedName, delErr),
				)
			}
		}
		// OVERWRITE path (or after a successful fresh rollback): surface the error
		// loudly; on overwrite we deliberately do NOT delete the shared copy.
		return fmt.Errorf("recording publication for %q: %w", req.PublishedName, err)
	}
	return nil
}

// rollback invokes the request's compensating delete (no-op when unset).
func (r PublishRequest) rollback(ctx context.Context) error {
	if r.Rollback == nil {
		return nil
	}
	return r.Rollback(ctx)
}
