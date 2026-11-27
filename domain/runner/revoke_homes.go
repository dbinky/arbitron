// domain/runner/revoke_homes.go
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/arbitron/arbitron/domain/engine"
)

type revocationHomeReleaser struct {
	inner  SessionRevoker
	homes  HomeStore
	leases ResourceLeaseStore
}

// NewRevocationHomeReleaser decorates the hub's SessionRevoker. After the hub
// has marked the runner lost (fencing its jobs), each home it owned is taken
// over by durable proof (Supersede: revoked key, else lost runner) or a free
// claim, released to server storage at the read generation, and the claim is
// given back. A hub failure releases nothing; an unproven takeover leaves the
// home owned; a home that moved meanwhile is never clobbered.
func NewRevocationHomeReleaser(inner SessionRevoker, homes HomeStore, leases ResourceLeaseStore) SessionRevoker {
	if inner == nil || homes == nil || leases == nil {
		panic("runner: NewRevocationHomeReleaser requires a SessionRevoker, a HomeStore and a ResourceLeaseStore")
	}
	return &revocationHomeReleaser{inner: inner, homes: homes, leases: leases}
}

func (r *revocationHomeReleaser) RevokeRunner(ctx context.Context, id RunnerID) error {
	if err := r.inner.RevokeRunner(ctx, id); err != nil {
		return fmt.Errorf("revoking %s: %w; its workspace homes stay owned until its jobs are fenced", id, err)
	}
	homes, err := r.homes.ListByRunner(ctx, id)
	if err != nil {
		return err
	}
	var errs []error
	for _, h := range homes {
		if err := r.release(ctx, id, h); err != nil {
			errs = append(errs, fmt.Errorf("run %s: %w", h.RunID, err))
		}
	}
	return errors.Join(errs...)
}

func (r *revocationHomeReleaser) release(ctx context.Context, id RunnerID, h Home) error {
	key, holder := runKey(h.RunID), "revoke:"+string(id)
	rec, active, err := r.leases.Active(ctx, key)
	if err != nil {
		return err
	}
	var claim engine.ResourceFenceRef
	if active {
		if rec.Fence.Owner != string(id) {
			return fmt.Errorf("%w: claimed by %q, not the revoked runner", ErrResourceBusy, rec.Fence.Owner)
		}
		claim, err = r.supersede(ctx, rec, holder)
		if errors.Is(err, ErrResourceFenced) {
			// The stopped job may have released its claim after Active. Claim
			// atomically succeeds only if nobody else has acquired the resource.
			claim, err = r.leases.Claim(ctx, key, holder, BuiltInRunnerID)
		}
		if err != nil {
			return err
		}
	} else if claim, err = r.leases.Claim(ctx, key, holder, BuiltInRunnerID); err != nil {
		return err
	}
	defer func() {
		if err := r.leases.Release(context.WithoutCancel(ctx), claim); err != nil {
			slog.Warn("releasing revocation claim", "run", h.RunID, "error", err)
		}
	}()
	cause := HomeMoveCause{OperationID: holder, FromGeneration: h.Generation, ToGeneration: h.Generation + 1}
	_, err = r.homes.HandOff(ctx, h.RunID, ServerHome, "revoked: "+string(id), 0, h.Generation, cause)
	if errors.Is(err, ErrHomeGenerationConflict) {
		return nil // recovery or another move already won; never clobber it
	}
	return err
}

func (r *revocationHomeReleaser) supersede(ctx context.Context, rec LeaseRecord, holder string) (engine.ResourceFenceRef, error) {
	var errs []error
	for _, kind := range []SupersedeKind{SupersedeRevoked, SupersedeRunnerLost} {
		ref, err := r.leases.Supersede(ctx, rec.Fence, SupersedeProof{Kind: kind, OldJob: rec.Job}, holder, BuiltInRunnerID)
		if err == nil {
			return ref, nil
		}
		if !errors.Is(err, ErrSupersedeUnproven) {
			return engine.ResourceFenceRef{}, err
		}
		errs = append(errs, err)
	}
	return engine.ResourceFenceRef{}, errors.Join(errs...)
}
