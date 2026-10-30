package layout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

var ErrWorkerMisconfigured = errors.New("layout: worker requires store, coordinator, journal, committed source, manifests, cache and requester")

// WorkerConfig supplies the saved-input pipeline and bounded runtime policy.
type WorkerConfig struct {
	Store         Store
	Coordinator   *Coordinator
	Journal       workflow.RevisionJournal
	Committed     workflow.CommittedRevisionSource
	Manifests     ManifestSource
	Cache         CachePublisher
	Requester     *Requester
	Owner         string
	Concurrency   int
	PollInterval  time.Duration
	LeaseDuration time.Duration
	JobTimeout    time.Duration
	StageTimeout  time.Duration
	Clock         func() time.Time
	Logger        *slog.Logger
}

const workerIOTimeout = 30 * time.Second

// Worker processes immutable saved revisions; notifications are only hints.
type Worker struct {
	cfg WorkerConfig
	mu  sync.Mutex
	// sweeping serializes the post-completion artifact sweep.
	sweeping sync.Mutex
	started  bool
	stopped  bool
	life     context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	slots    chan struct{}
	hints    map[string]struct{}
	wake     chan struct{}
}

// NewWorker requires every production port; there is no fallback renderer/store.
func NewWorker(cfg WorkerConfig) (*Worker, error) {
	if cfg.Store == nil || cfg.Coordinator == nil || cfg.Journal == nil || cfg.Committed == nil || cfg.Manifests == nil || cfg.Cache == nil || cfg.Requester == nil {
		return nil, ErrWorkerMisconfigured
	}
	if cfg.Owner == "" {
		host, _ := os.Hostname()
		cfg.Owner = fmt.Sprintf("%s:%d:%s", host, os.Getpid(), uuid.NewString())
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 2
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 15 * time.Second
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = 5 * time.Minute
	}
	if cfg.JobTimeout <= 0 {
		cfg.JobTimeout = 15 * time.Minute
	}
	if cfg.StageTimeout <= 0 {
		cfg.StageTimeout = defaultStageTimeout
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	life, cancel := context.WithCancel(context.Background())
	return &Worker{cfg: cfg, life: life, cancel: cancel, slots: make(chan struct{}, cfg.Concurrency), hints: make(map[string]struct{}), wake: make(chan struct{}, 1)}, nil
}
func workerIO(ctx context.Context, fn func(context.Context) error) error {
	bounded, cancel := context.WithTimeout(ctx, workerIOTimeout)
	defer cancel()
	return fn(bounded)
}

// ProcessRevision claims one revision, preserving the returned generation fence.
//
// Synchronous maintenance entry point; do not call concurrently with Start.
func (w *Worker) ProcessRevision(ctx context.Context, revision string) error {
	ctx, done, err := w.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return w.runRevision(ctx, revision)
}
func (w *Worker) runRevision(ctx context.Context, revision string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case w.slots <- struct{}{}:
	}
	defer func() { <-w.slots }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return w.processRevision(ctx, revision)
}
func (w *Worker) processRevision(ctx context.Context, revision string) error {
	var rec Record
	now := w.cfg.Clock()
	err := workerIO(ctx, func(c context.Context) error {
		var err error
		rec, err = w.cfg.Store.Claim(c, revision, w.cfg.Owner, now, now.Add(w.cfg.LeaseDuration))
		return err
	})
	if errors.Is(err, ErrRecordNotFound) || errors.Is(err, ErrNotClaimable) {
		return nil
	}
	if err != nil {
		return err
	}
	fence := Fence{Revision: rec.Revision, Generation: rec.JobGeneration}
	var wf workflow.Workflow
	var outcome Result
	if err = json.Unmarshal(rec.Manifest, &wf); err != nil || wf.SavedRevision() != rec.Revision {
		reason := "captured manifest revision does not match record"
		if err != nil {
			reason = "decoding captured manifest: " + err.Error()
		}
		outcome = Result{Verdict: VerdictFailed, Failure: &Failure{Stage: StageArranging, Reason: reason}}
	} else {
		jobCtx, cancel := context.WithTimeout(ctx, w.cfg.JobTimeout)
		// Coordinator options are per-job; the root's coordinator is shared.
		co := *w.cfg.Coordinator
		co.stageTimeout = w.cfg.StageTimeout
		var progressMu sync.Mutex
		var progressErr error
		latest := Progress{Stage: StageArranging}
		renew := func(p *Progress) {
			progressMu.Lock()
			defer progressMu.Unlock()
			if p != nil {
				latest = *p
			}
			if progressErr != nil || jobCtx.Err() != nil {
				return
			}
			now := w.cfg.Clock()
			err := workerIO(jobCtx, func(c context.Context) error {
				return w.cfg.Store.Progress(c, fence, latest.Stage, latest.Attempt, now, now.Add(w.cfg.LeaseDuration))
			})
			if err != nil && jobCtx.Err() == nil {
				progressErr = err
				cancel()
			}
		}
		heartbeatDone := make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(max(w.cfg.LeaseDuration/3, time.Nanosecond))
			defer ticker.Stop()
			for {
				select {
				case <-jobCtx.Done():
					return
				case <-ticker.C:
					renew(nil)
				}
			}
		}()
		outcome = co.Run(jobCtx, Job{Revision: rec.Revision, Manifest: &wf, MaxAttempts: rec.AttemptLimit}, func(p Progress) { renew(&p) })
		cancel()
		<-heartbeatDone
		if progressErr != nil && ctx.Err() == nil {
			var current Record
			err = workerIO(context.WithoutCancel(ctx), func(c context.Context) error {
				var err error
				current, err = w.cfg.Store.Get(c, rec.Revision)
				return err
			})
			if err != nil {
				return errors.Join(progressErr, err)
			}
			// Neither early return stores the outcome: release its capture.
			if current.JobGeneration != fence.Generation {
				w.release(ctx, bestArtifacts(outcome), "stale generation")
				return ErrStaleGeneration
			}
			if current.State != StateSuperseded {
				w.release(ctx, bestArtifacts(outcome), "lease lost")
				return progressErr
			}
			// Complete may retain evidence, but Store keeps an explicit superseded state.
			outcome.Verdict = VerdictFailed
			outcome.Failure = &Failure{Stage: latest.Stage, Reason: progressErr.Error()}
		}
	}
	completion := Completion{State: State(outcome.Verdict), AttemptsUsed: outcome.AttemptsUsed, FailureReason: outcome.ExhaustedReason}
	if outcome.Failure != nil {
		completion.FailureStage = outcome.Failure.Stage
		completion.FailureReason = outcome.Failure.Reason
	}
	var prior StoredResult
	if outcome.Best != nil {
		cand := outcome.Best
		verdict := VerdictNeedsAttention
		if !HasRenderedEvidence(cand.Geometry, cand.Review) {
			verdict = VerdictFailed
		} else if cand.Passed && BlockingCount(candidateFindings(cand)) == 0 {
			verdict = VerdictPassed
		}
		result := StoredResult{Revision: rec.Revision, Generation: rec.JobGeneration, Geometry: cand.Geometry, Review: cand.Review, Preflight: cand.Preflight, Verdict: verdict, Score: cand.Score, CreatedAt: w.cfg.Clock()}
		completion.Result = &result
		completion.ReplaceBest = true
		if rec.ResultGeneration > 0 {
			err = workerIO(context.WithoutCancel(ctx), func(c context.Context) error {
				var err error
				prior, err = w.cfg.Store.Result(c, rec.Revision, rec.ResultGeneration)
				return err
			})
			if err != nil && !errors.Is(err, ErrRecordNotFound) {
				return err
			}
			// Retry can repair a missing referenced result. Only an actual absence
			// permits replacement without comparison; storage outages still fail.
			// A pass that read truth rejects (the reason Retry invalidated it) is
			// no longer a best to keep, and neither is any result whose geometry
			// read truth never displays: its Score ranked another contract.
			completion.ReplaceBest = errors.Is(err, ErrRecordNotFound) || !retainable(&wf, prior) || storedBetter(prior, result)
		}
	}
	if ctx.Err() != nil {
		if completion.Result != nil {
			err = workerIO(context.WithoutCancel(ctx), func(c context.Context) error {
				return w.cfg.Store.Checkpoint(c, fence, *completion.Result, completion.ReplaceBest, w.cfg.Clock())
			})
			w.releaseUnselected(ctx, completion, prior, err)
		}
		return errors.Join(ctx.Err(), err)
	}
	err = workerIO(context.WithoutCancel(ctx), func(c context.Context) error { return w.cfg.Store.Complete(c, fence, completion, w.cfg.Clock()) })
	w.releaseUnselected(ctx, completion, prior, err)
	if err != nil {
		return err
	}
	// Bound published retention across saves: this revision once a newer save
	// replaced it, and the revision whose result it retained for display.
	w.releaseHistorical(ctx, rec.Revision)
	if rec.Retained.FromRevision != rec.Revision {
		w.releaseHistorical(ctx, rec.Retained.FromRevision)
	}
	// A save that replaced every node ID names no retained revision; the sweep
	// releases whatever this completion superseded.
	w.sweepArtifacts(ctx)
	return w.publish(ctx, fence)
}

// ReconcileCache retries publication independently of successful stored reviews.
//
// Synchronous maintenance entry point; do not call concurrently with Start.
func (w *Worker) ReconcileCache(ctx context.Context) error {
	ctx, done, err := w.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return w.reconcileCache(ctx)
}
func (w *Worker) reconcileCache(ctx context.Context) error {
	var rows []Record
	err := workerIO(ctx, func(c context.Context) error { var err error; rows, err = w.cfg.Store.PendingCache(c, 0); return err })
	if err != nil {
		return err
	}
	var errs []error
	for _, row := range rows {
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
		if err := w.publish(ctx, Fence{Revision: row.Revision, Generation: row.JobGeneration}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
func (w *Worker) publish(ctx context.Context, fence Fence) error {
	return workerIO(ctx, func(c context.Context) error {
		row, err := w.cfg.Store.Get(c, fence.Revision)
		if err != nil {
			return err
		}
		if row.JobGeneration != fence.Generation {
			return ErrStaleGeneration
		}
		if !row.State.Terminal() || row.ResultGeneration < 1 {
			return nil
		}
		dest := row.Destination
		gen := row.ResultGeneration
		// Settling a historical row also needs the exact checked destination/fences.
		settle := func() error { return w.cfg.Store.MarkCached(c, fence, gen, dest) }
		rev, exists, err := w.cfg.Manifests.CurrentRevision(c, dest.Scope, dest.Name)
		if err != nil {
			return err
		}
		if !exists || rev != row.Revision {
			return settle()
		}
		result, err := w.cfg.Store.Result(c, row.Revision, gen)
		if err != nil {
			return err
		}
		var wf workflow.Workflow
		if err = json.Unmarshal(row.Manifest, &wf); err != nil {
			return err
		}
		graph, err := BuildGraph(&wf)
		if err != nil {
			return err
		}
		if wf.SavedRevision() != row.Revision || result.Revision != row.Revision || result.Generation != gen {
			return fmt.Errorf("layout: incompatible cache result")
		}
		// A previous renderer contract's result is re-arranged when viewed;
		// there is nothing to publish until then, and nothing to report.
		if contractStale(result.Geometry, graph) {
			return settle()
		}
		if !Compatible(result.Geometry, graph) {
			return fmt.Errorf("layout: incompatible cache result")
		}
		if result.Verdict != VerdictPassed && result.Verdict != VerdictNeedsAttention && result.Verdict != VerdictFailed {
			return fmt.Errorf("layout: invalid cache verdict")
		}
		if result.Verdict == VerdictPassed {
			if problem := passProblem(&wf, result); problem != "" {
				return errors.New(problem)
			}
		}
		entry := CacheEntry{Format: CacheFormatV1, Revision: row.Revision, Generation: gen, JobGeneration: row.JobGeneration, Contract: result.Geometry.Contract, Verdict: result.Verdict, Geometry: result.Geometry}
		err = w.cfg.Cache.PublishCache(c, dest, entry, func(owned context.Context) error {
			return workerIO(owned, func(check context.Context) error {
				current, err := w.cfg.Store.Get(check, row.Revision)
				if err != nil {
					return err
				}
				if current.JobGeneration != fence.Generation || current.ResultGeneration != gen || current.Destination != dest || !current.State.Terminal() {
					return ErrStaleGeneration
				}
				return nil
			})
		})
		if err != nil && !errors.Is(err, ErrNotCurrent) && !errors.Is(err, ErrDestinationGone) {
			return err
		}
		return settle()
	})
}

// begin registers an operation under the same lock Stop uses before Wait.
func (w *Worker) begin(ctx context.Context) (context.Context, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return nil, nil, context.Canceled
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.life, cancel)
	w.wg.Add(1)
	return child, func() { stop(); cancel(); w.wg.Done() }, nil
}

const workerHintCapacity = 256

// Notify coalesces bounded hints without starting work on the caller's goroutine.
func (w *Worker) Notify(revision string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || revision == "" {
		return
	}
	if len(w.hints) < workerHintCapacity {
		w.hints[revision] = struct{}{}
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start launches one dispatcher; repeated calls do not launch more dispatchers.
func (w *Worker) Start(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started || w.stopped {
		return
	}
	w.started = true
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.life, cancel)
	w.wg.Add(2)
	go func() { defer w.wg.Done(); defer cancel(); defer stop(); w.dispatch(child) }()
	// Startup reconcile of crash-leftover render captures, alongside dispatch.
	go func() {
		defer w.wg.Done()
		if err := w.reconcileArtifacts(child); err != nil && child.Err() == nil {
			w.cfg.Logger.Warn("layout worker: reconciling render artifacts", "error", err)
		}
	}()
}

// Stop cancels and joins dispatch, jobs, heartbeats and public operations.
func (w *Worker) Stop() {
	w.mu.Lock()
	w.stopped = true
	w.cancel()
	w.mu.Unlock()
	w.wg.Wait()
}
func (w *Worker) dispatch(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	pending := map[string]struct{}{}
	active := map[string]struct{}{}
	type finished struct {
		revision string
		err      error
	}
	results := make(chan finished, w.cfg.Concurrency)
	var jobs sync.WaitGroup
	defer jobs.Wait()
	logError := func(err error) {
		if err != nil && ctx.Err() == nil {
			w.cfg.Logger.Warn("layout worker sweep", "error", err)
		}
	}
	sweep := func() {
		logError(w.recover(ctx))
		logError(w.reconcileCache(ctx))
		var revs []string
		err := workerIO(ctx, func(c context.Context) error {
			var err error
			revs, err = w.cfg.Store.Claimable(c, w.cfg.Clock(), 0)
			return err
		})
		logError(err)
		for _, rev := range revs {
			if _, ok := active[rev]; !ok {
				pending[rev] = struct{}{}
			}
		}
	}
	takeHints := func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		for rev := range w.hints {
			if _, ok := active[rev]; !ok {
				pending[rev] = struct{}{}
			}
		}
		clear(w.hints)
	}
	sweep()
	for {
		if ctx.Err() != nil {
			return
		}
		takeHints()
		for rev := range pending {
			if len(active) >= w.cfg.Concurrency {
				break
			}
			if ctx.Err() != nil {
				return
			}
			delete(pending, rev)
			active[rev] = struct{}{}
			jobs.Add(1)
			go func(rev string) { defer jobs.Done(); err := w.runRevision(ctx, rev); results <- finished{rev, err} }(rev)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case result := <-results:
			delete(active, result.revision)
			logError(result.err)
		case <-ticker.C:
			sweep()
		}
	}
}

// Drain processes the finite claimable snapshot, sharing the worker's job bound.
//
// Synchronous maintenance entry point; do not call concurrently with Start.
func (w *Worker) Drain(ctx context.Context) error {
	ctx, done, err := w.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	var revs []string
	err = workerIO(ctx, func(c context.Context) error {
		var err error
		revs, err = w.cfg.Store.Claimable(c, w.cfg.Clock(), 0)
		return err
	})
	if err != nil {
		return err
	}
	jobs := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for range min(w.cfg.Concurrency, len(revs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rev := range jobs {
				if err := w.runRevision(ctx, rev); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}()
	}
send:
	for _, rev := range revs {
		select {
		case <-ctx.Done():
			break send
		case jobs <- rev:
		}
	}
	close(jobs)
	wg.Wait()
	return errors.Join(append(errs, ctx.Err())...)
}

// Recover replays only proven committed requests and removes evidence after ack.
//
// Synchronous maintenance entry point; do not call concurrently with Start.
func (w *Worker) Recover(ctx context.Context) error {
	ctx, done, err := w.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return w.recover(ctx)
}
func (w *Worker) recover(ctx context.Context) error {
	var requests []workflow.ArrangementRequest
	sourceErr := workerIO(ctx, func(c context.Context) error {
		var err error
		requests, err = w.cfg.Committed.CommittedRequests(c)
		return err
	})
	errs := []error{sourceErr}
	for _, req := range requests {
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
		if err := workerIO(ctx, func(c context.Context) error { return w.cfg.Requester.RequestArrangement(c, req) }); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := workerIO(ctx, func(c context.Context) error { return w.cfg.Journal.Remove(c, req.Revision) }); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// retainable reports whether prior can stay the best: Reader.View serves it.
func retainable(wf *workflow.Workflow, prior StoredResult) bool {
	if prior.Verdict == VerdictPassed {
		return passProblem(wf, prior) == ""
	}
	g, err := BuildGraph(wf)
	return err == nil && Compatible(prior.Geometry, g)
}

// storedBetter applies the shared evidence/score ordering across generations.
func storedBetter(prior, next StoredResult) bool {
	if a, b := !prior.IsUnreviewed(), !next.IsUnreviewed(); a != b {
		return b
	}
	if a, b := prior.Verdict == VerdictPassed, next.Verdict == VerdictPassed; a != b {
		return b
	}
	return next.Score.Less(prior.Score)
}
