package layout

import (
	"context"
	"errors"
	"time"
)

// ArtifactReleaser is an optional Renderer capability. Screenshots are review
// inputs: once a candidate is discarded, rejected, abandoned (cancel or review
// error) or replaced by a newer published capture, Release lets the renderer
// that wrote them delete exactly that candidate's capture. Release is
// idempotent, must ignore artifacts it did not produce, and must never remove
// anything broader than the cited capture. Callers address captures only by
// the Artifacts the renderer returned; the domain never knows where they live.
type ArtifactReleaser interface {
	Release(ctx context.Context, artifacts []Artifact) error
}

// ReferencedArtifacts reports every artifact cited by any stored result of a
// revision (nil when the revision has no stored results).
type ReferencedArtifacts func(ctx context.Context, revision string) ([]Artifact, error)

// ArtifactReconciler is an optional Renderer capability: remove crash
// leftovers, i.e. captures no stored result cites. Implementations must not
// remove captures another live process may still be reviewing.
type ArtifactReconciler interface {
	ReconcileArtifacts(ctx context.Context, referenced ReferencedArtifacts) error
}

const artifactReleaseTimeout = 30 * time.Second

// releaseArtifacts is best-effort and survives caller cancellation: the cancel
// and review-error paths are exactly where captures would otherwise leak.
// Leftovers are removed by the startup reconcile.
func releaseArtifacts(ctx context.Context, r Renderer, artifacts []Artifact) error {
	rel, ok := r.(ArtifactReleaser)
	if !ok || len(artifacts) == 0 {
		return nil
	}
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactReleaseTimeout)
	defer cancel()
	return rel.Release(c, artifacts)
}

// bestArtifacts returns the artifacts Run's best candidate cites.
func bestArtifacts(r Result) []Artifact {
	if r.Best == nil || r.Best.Review == nil {
		return nil
	}
	return r.Best.Review.Evidence.Artifacts
}

// resultArtifacts returns the artifacts a stored result cites.
func resultArtifacts(r StoredResult) []Artifact {
	if r.Review == nil {
		return nil
	}
	return r.Review.Evidence.Artifacts
}

// referencedArtifacts walks every generation of revision's stored results.
// A finished revision its destination no longer serves references nothing,
// unless the current revision is still pending and displays its retained
// layout: completion would release it (releaseHistorical), and a save that
// replaced every node ID retains no layout to name it, so without this rule
// the reconcile kept superseded captures forever.
func referencedArtifacts(s Store, m ManifestSource) ReferencedArtifacts {
	return func(ctx context.Context, revision string) ([]Artifact, error) {
		var rec Record
		err := workerIO(ctx, func(c context.Context) error { var err error; rec, err = s.Get(c, revision); return err })
		if errors.Is(err, ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if rec.State.Terminal() {
			superseded, err := supersededAndUndisplayed(ctx, s, m, rec)
			if err != nil || superseded {
				return nil, err
			}
		}
		var out []Artifact
		for gen := int64(1); gen <= rec.JobGeneration; gen++ {
			var res StoredResult
			err := workerIO(ctx, func(c context.Context) error { var err error; res, err = s.Result(c, revision, gen); return err })
			if errors.Is(err, ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, resultArtifacts(res)...)
		}
		return out, nil
	}
}

// supersededAndUndisplayed reports whether a finished revision is no longer
// its destination's current revision and is not the retained layout a pending
// current revision still displays. Read errors fail closed (the caller keeps
// the captures).
func supersededAndUndisplayed(ctx context.Context, s Store, m ManifestSource, rec Record) (bool, error) {
	var current string
	var exists bool
	err := workerIO(ctx, func(c context.Context) error {
		var err error
		current, exists, err = m.CurrentRevision(c, rec.Destination.Scope, rec.Destination.Name)
		return err
	})
	if err != nil {
		return false, err
	}
	if exists && current == rec.Revision {
		return false, nil
	}
	if !exists {
		return true, nil
	}
	var cur Record
	err = workerIO(ctx, func(c context.Context) error { var err error; cur, err = s.Get(c, current); return err })
	if errors.Is(err, ErrRecordNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return cur.State.Terminal() || cur.Retained.FromRevision != rec.Revision, nil
}

// ReconcileArtifacts runs the renderer's crash-leftover reconcile against the
// stored results (a no-op for renderers without artifacts). Start runs it once.
func (w *Worker) ReconcileArtifacts(ctx context.Context) error {
	ctx, done, err := w.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return w.reconcileArtifacts(ctx)
}

func (w *Worker) reconcileArtifacts(ctx context.Context) error {
	rec, ok := w.cfg.Coordinator.renderer.(ArtifactReconciler)
	if !ok {
		return nil
	}
	return rec.ReconcileArtifacts(ctx, referencedArtifacts(w.cfg.Store, w.cfg.Manifests))
}

// sweepArtifacts runs the reconcile after a completion so superseded captures
// leave the disk once past the renderer's grace period, without a restart.
// One sweep runs at a time; a busy or failed sweep is left to the next one.
func (w *Worker) sweepArtifacts(ctx context.Context) {
	if !w.sweeping.TryLock() {
		return
	}
	defer w.sweeping.Unlock()
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactReleaseTimeout)
	defer cancel()
	if err := w.reconcileArtifacts(c); err != nil {
		w.cfg.Logger.Warn("layout worker: sweeping superseded render artifacts", "error", err)
	}
}

// release logs instead of failing: retention is bounded by the reconcile.
func (w *Worker) release(ctx context.Context, artifacts []Artifact, why string) {
	if err := releaseArtifacts(ctx, w.cfg.Coordinator.renderer, artifacts); err != nil {
		w.cfg.Logger.Warn("layout worker: releasing render artifacts", "reason", why, "error", err)
	}
}

// releaseHistorical releases the selected capture of revision once it is no
// longer its destination's current revision (a newer save superseded it, or
// the workflow is gone). Read truth never reopens screenshots, so historical
// results keep their evidence metadata but not the files.
func (w *Worker) releaseHistorical(ctx context.Context, revision string) {
	if revision == "" {
		return
	}
	var res StoredResult
	err := workerIO(context.WithoutCancel(ctx), func(c context.Context) error {
		row, err := w.cfg.Store.Get(c, revision)
		if err != nil || !row.State.Terminal() || row.ResultGeneration < 1 {
			return err
		}
		current, exists, err := w.cfg.Manifests.CurrentRevision(c, row.Destination.Scope, row.Destination.Name)
		if err != nil || exists && current == revision {
			return err
		}
		res, err = w.cfg.Store.Result(c, revision, row.ResultGeneration)
		return err
	})
	if err != nil {
		if !errors.Is(err, ErrRecordNotFound) {
			w.cfg.Logger.Warn("layout worker: checking superseded render artifacts", "revision", revision, "error", err)
		}
		return
	}
	w.release(ctx, resultArtifacts(res), "superseded")
}

// releaseUnselected keeps exactly one capture per revision: the selected
// result's. After a successful write it releases whichever of the new result
// and the prior selection lost; a fenced-out write releases the new capture,
// which no stored result cites. Other write errors leave the outcome unknown,
// so the startup reconcile decides.
func (w *Worker) releaseUnselected(ctx context.Context, completion Completion, prior StoredResult, writeErr error) {
	if completion.Result == nil {
		return
	}
	switch {
	case errors.Is(writeErr, ErrStaleGeneration):
		w.release(ctx, resultArtifacts(*completion.Result), "stale generation")
	case writeErr != nil:
	case !completion.ReplaceBest:
		w.release(ctx, resultArtifacts(*completion.Result), "not selected")
	case prior.Revision != "":
		w.release(ctx, resultArtifacts(prior), "replaced")
	}
}
