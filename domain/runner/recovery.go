// domain/runner/recovery.go
package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// Pool event kinds added in Phase 3 (contracts §9).
const (
	EventJobRecovered       = "job.recovered"
	EventJobAdopted         = "job.adopted"
	EventJobRecoveryBlocked = "job.recovery_blocked"
)

// BuiltInRestorer restores the last COMMITTED checkpoint into the built-in
// working directory (WorkspaceSync.RestoreBuiltIn, P3 T6; P3 T16 non-portable branch).
type BuiltInRestorer interface {
	RestoreBuiltIn(ctx context.Context, runID uuid.UUID, dir string) error
}

// RestartGraceSetter keeps the hub from declaring runners lost during the
// restart grace (Hub.BeginRestartGrace).
type RestartGraceSetter interface{ BeginRestartGrace(until time.Time) }

type RecoveryDeps struct {
	Inbox        RemoteInbox        // durable receipts (P2 T4)
	Leases       ResourceLeaseStore // the shared, boot-bound lease repo (P3 T3, wired by P3 T13)
	Stopper      BuiltInStopper     // builtin.ProcessSupervisor (Cycles 2a-2e)
	Ledger       Ledger
	Homes        HomeStore
	Restorer     BuiltInRestorer
	Commits      engine.StepCommitter
	Grace        RestartGraceSetter
	Events       *EventRecorder
	Status       *ExecutionStatusProjector
	Clock        func() time.Time
	RestartGrace time.Duration
}

// PoolRecovery is the engine.RecoveryPolicy for server restarts (design §3.5).
type PoolRecovery struct{ d RecoveryDeps }

func NewPoolRecovery(d RecoveryDeps) (*PoolRecovery, error) {
	var missing []string
	for _, dep := range []struct {
		name string
		ok   bool
	}{
		{"Inbox", d.Inbox != nil}, {"Leases", d.Leases != nil}, {"Stopper", d.Stopper != nil}, {"Ledger", d.Ledger != nil},
		{"Homes", d.Homes != nil}, {"Restorer", d.Restorer != nil}, {"Commits", d.Commits != nil}, {"Grace", d.Grace != nil},
		{"Events", d.Events != nil}, {"Status", d.Status != nil},
	} {
		if !dep.ok {
			missing = append(missing, dep.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: PoolRecovery needs %s", strings.Join(missing, ", "))
	}
	if d.RestartGrace <= 0 {
		return nil, errors.New("runner: PoolRecovery needs a positive RestartGrace")
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	return &PoolRecovery{d: d}, nil
}

// recoveryHolder is the durable holder identity of recovery's own server-side claim.
func recoveryHolder(runID uuid.UUID) string { return "recovery:" + runKey(runID) }

// Recover runs once at startup, before any run re-enters: remote leases are
// extended by the restart grace and the hub's grace window opens, so a
// runner that reconnects in time keeps (and reports) its jobs.
func (p *PoolRecovery) Recover(ctx context.Context) error {
	until := p.d.Clock().Add(p.d.RestartGrace)
	jobs, err := p.d.Ledger.ListActive(ctx)
	if err != nil {
		return fmt.Errorf("runner: recovery: list active jobs: %w", err)
	}
	retainedRemote := false
	for _, j := range jobs {
		if !j.State.HoldsSlot() || j.RunnerID == "" || j.RunnerID == BuiltInRunnerID {
			continue
		}
		if err := p.d.Ledger.ExtendLease(ctx, j.Fence(), until); err != nil {
			if errors.Is(err, ErrFenced) {
				continue
			}
			return fmt.Errorf("runner: recovery: extend lease of %s: %w", j.ID, err)
		}
		retainedRemote = true
	}
	if retainedRemote {
		p.d.Grace.BeginRestartGrace(until)
	}
	return nil
}

// PrepareRun implements engine.RecoveryPolicy. See the order in Step 3's lead-in.
func (p *PoolRecovery) PrepareRun(ctx context.Context, inst *instance.Instance) (engine.RecoveryAction, error) {
	jobs, err := p.d.Ledger.ListByRun(ctx, inst.ID)
	if err != nil {
		return "", fmt.Errorf("runner: recovery: list jobs of %s: %w", inst.ID, err)
	}
	node := ""
	if inst.CurrentNodeID != nil {
		node = *inst.CurrentNodeID
	}
	var latest *Job
	var interrupted []Job
	for i := range jobs {
		j := jobs[i]
		if j.NodeID == node && (latest == nil || !j.CreatedAt.Before(latest.CreatedAt)) {
			latest = &jobs[i]
		}
		if !j.State.HoldsSlot() {
			continue
		}
		if j.RunnerID == BuiltInRunnerID {
			interrupted = append(interrupted, j)
			continue
		}
		// Remote: lease extended by Recover; job AND resource claim are kept
		// through suspect/restart grace. JobAdopter adopts or, after confirmed
		// loss/revocation, recovers it.
		retained, err := p.resultRetained(ctx, j)
		if err != nil {
			return "", err
		}
		if !retained {
			p.d.Status.Publish(ctx, j, instance.ExecRecovering)
		}
	}
	// 1. Fence every interrupted built-in operation.
	for _, j := range interrupted {
		if _, err := p.d.Ledger.Requeue(ctx, j.Fence(), OutcomeRunnerLost); err != nil && !errors.Is(err, ErrFenced) {
			return "", fmt.Errorf("runner: recovery: fence built-in job %s: %w", j.ID, err)
		}
		p.record(ctx, j, "server_restart")
	}
	// 2. Stop and wait for each exact process tree. An unconfirmed stop returns
	// here with the old claim untouched: no restore and no retry can follow.
	stopped := map[Fence]bool{}
	for _, j := range interrupted {
		if err := p.d.Stopper.StopAndWait(ctx, j.Fence()); err != nil {
			return "", fmt.Errorf("runner: recovery: stop built-in job %s: %w", j.ID, err)
		}
		stopped[j.Fence()] = true
	}
	// 3. Take the workspace over on durable evidence.
	claim, held, err := p.takeOver(ctx, inst.ID, len(interrupted) > 0, stopped)
	if err != nil {
		return "", err
	}
	release := func() error {
		if !held {
			return nil
		}
		if err := p.d.Leases.Release(context.WithoutCancel(ctx), claim); err != nil && !errors.Is(err, ErrResourceFenced) {
			return fmt.Errorf("runner: recovery: release workspace of run %s: %w", inst.ID, err)
		}
		return nil
	}
	// 4. A committed step replays: no restore, no provider call.
	if latest != nil {
		_, committed, err := p.d.Commits.CommittedResult(ctx, latest.Key.InvocationID)
		if err != nil {
			return "", errors.Join(fmt.Errorf("runner: recovery: committed result of %s: %w", latest.Key.InvocationID, err), release())
		}
		if committed {
			return engine.RecoveryReplayCommitted, release()
		}
	}
	// 5. Restore the last committed base, then free the workspace for the repeat.
	if held {
		if err := p.restore(ctx, inst); err != nil {
			rerr := release()
			if errors.Is(err, ErrNoRecoveryBase) {
				run := inst.ID
				p.d.Events.Record(ctx, PoolEvent{ID: uuid.New(), At: p.d.Clock(), Kind: EventJobRecoveryBlocked, RunID: &run,
					Cause:  Cause{Kind: CauseSystem},
					Detail: map[string]any{"reason": err.Error(), "notice": instance.RecoveryNoticeText}})
				return engine.RecoveryFail, errors.Join(fmt.Errorf("runner: recovery blocked for run %s: %w", inst.ID, err), rerr)
			}
			return "", errors.Join(err, rerr)
		}
	}
	if err := release(); err != nil {
		return "", err
	}
	return engine.RecoveryResume, nil
}

// takeOver supersedes the run's workspace claim when recovery may prove the
// old holder can no longer write; held reports whether recovery now holds it.
func (p *PoolRecovery) takeOver(ctx context.Context, runID uuid.UUID, interrupted bool, stopped map[Fence]bool) (engine.ResourceFenceRef, bool, error) {
	key := runKey(runID)
	rec, ok, err := p.d.Leases.Active(ctx, key)
	if err != nil {
		return engine.ResourceFenceRef{}, false, fmt.Errorf("runner: recovery: read workspace claim of run %s: %w", runID, err)
	}
	if !ok {
		if !interrupted {
			return engine.ResourceFenceRef{}, false, nil
		}
		ref, err := p.d.Leases.Claim(ctx, key, recoveryHolder(runID), BuiltInRunnerID)
		if err != nil {
			return engine.ResourceFenceRef{}, false, fmt.Errorf("runner: recovery: claim workspace of run %s: %w", runID, err)
		}
		return ref, true, nil
	}
	var proof SupersedeProof
	switch {
	case rec.Job == nil: // server-side holder from an earlier boot (design D7)
		proof = SupersedeProof{Kind: SupersedeServerRestart}
	case rec.Job.RunnerID == BuiltInRunnerID: // includes a fenced-but-unreleased built-in claim
		old := *rec.Job
		if !stopped[old] {
			if err := p.d.Stopper.StopAndWait(ctx, old); err != nil {
				return engine.ResourceFenceRef{}, false, fmt.Errorf("runner: recovery: stop claim holder %s: %w", old.JobID, err)
			}
		}
		proof = SupersedeProof{Kind: SupersedeStopped, OldJob: &old}
	default: // a remote writer: only JobAdopter, after confirmed loss/revocation, takes it over
		return engine.ResourceFenceRef{}, false, nil
	}
	ref, err := p.d.Leases.Supersede(ctx, rec.Fence, proof, recoveryHolder(runID), BuiltInRunnerID)
	if err != nil {
		return engine.ResourceFenceRef{}, false, fmt.Errorf("runner: recovery: take over workspace of run %s (%s): %w", runID, proof.Kind, err)
	}
	return ref, true, nil
}

func (p *PoolRecovery) restore(ctx context.Context, inst *instance.Instance) error {
	home, err := p.d.Homes.GetHome(ctx, inst.ID)
	if err != nil {
		return fmt.Errorf("runner: recovery: home of %s: %w", inst.ID, err)
	}
	if !isServerLocal(home.RunnerID) {
		return nil // a remote home is re-pulled on its runner, never restored on the server
	}
	dir, _ := inst.Variables[engine.ReservedVarWorkspace].(string)
	if dir == "" {
		return nil
	}
	if err := p.d.Restorer.RestoreBuiltIn(ctx, inst.ID, dir); err != nil {
		return fmt.Errorf("runner: recovery: restore workspace of %s: %w", inst.ID, err)
	}
	return nil
}

func (p *PoolRecovery) resultRetained(ctx context.Context, j Job) (bool, error) {
	r, ok, err := p.d.Inbox.Receipt(ctx, j.Fence())
	if err != nil {
		return false, fmt.Errorf("runner: recovery: receipt of job %s: %w", j.ID, err)
	}
	return ok && r.Result != nil, nil
}

func (p *PoolRecovery) record(ctx context.Context, j Job, reason string) {
	run := j.Key.RunID
	p.d.Events.Record(ctx, PoolEvent{
		ID: uuid.New(), At: p.d.Clock(), Kind: EventJobRecovered, RunnerID: j.RunnerID, SourceID: j.SourceID, RunID: &run,
		Cause:  Cause{Kind: CauseSystem, Outcome: OutcomeRunnerLost},
		Detail: map[string]any{"jobId": j.ID.String(), "nodeId": j.NodeID, "reason": reason, "notice": instance.RecoveryNoticeText},
	})
}
