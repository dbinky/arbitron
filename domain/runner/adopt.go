// domain/runner/adopt.go
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// Adopter waits for a job that is already running on a runner (the Hub).
type Adopter interface {
	Adopt(ctx context.Context, a Assignment, job StepJob) (StepOutput, error)
}

type AdoptDeps struct {
	Ledger   Ledger
	Registry Registry
	Hub      Adopter
	Leases   ResourceLeaseStore // the shared, boot-bound lease repo (P3 T13)
	Stopper  BuiltInStopper     // builtin.ProcessSupervisor (Cycles 2a-2e)
	Events   *EventRecorder
	Status   *ExecutionStatusProjector
	Audit    AuditRecorder // *audit.Service; records D6 reconnect-absence takeovers
	Clock    func() time.Time
	Poll     time.Duration // re-check interval while waiting; default 5s
}

// JobAdopter is the dispatcher's branch for a job EnsureIntent returned
// offered/running: after a restart it is awaited, never dispatched again.
type JobAdopter struct{ d AdoptDeps }

func NewJobAdopter(d AdoptDeps) (*JobAdopter, error) {
	var missing []string
	for _, dep := range []struct {
		name string
		ok   bool
	}{
		{"Ledger", d.Ledger != nil}, {"Registry", d.Registry != nil}, {"Hub", d.Hub != nil}, {"Leases", d.Leases != nil},
		{"Stopper", d.Stopper != nil}, {"Events", d.Events != nil}, {"Status", d.Status != nil}, {"Audit", d.Audit != nil},
	} {
		if !dep.ok {
			missing = append(missing, dep.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("runner: JobAdopter needs %s", strings.Join(missing, ", "))
	}
	if d.Clock == nil {
		d.Clock = time.Now
	}
	if d.Poll <= 0 {
		d.Poll = 5 * time.Second
	}
	return &JobAdopter{d: d}, nil
}

// Resolve handles a job EnsureIntent returned offered/running:
//   - done, nil: the remote job finished; out is its output. The ledger is
//     untouched: the dispatcher completes it through completeStep (receipt).
//   - done, err: the adopted job ended with this classified failure (or ctx
//     ended); the dispatcher rolls the workspace back, then releases it.
//   - !done, nil: the job was requeued with takeover evidence; place it normally.
//   - !done, err: a takeover could not be proven; the step fails, nothing is re-placed.
func (a *JobAdopter) Resolve(ctx context.Context, j Job, job StepJob) (StepOutput, bool, error) {
	if !j.State.HoldsSlot() {
		return StepOutput{}, false, nil
	}
	if j.RunnerID == BuiltInRunnerID {
		return StepOutput{}, false, a.recover(ctx, j, "builtin_not_adoptable", SupersedeStopped)
	}
	fence := j.Fence()
	a.d.Status.Publish(ctx, j, instance.ExecRecovering)
	for {
		var out StepOutput
		var err error
		if job.Op == OpStream {
			out, err = a.d.Hub.Adopt(ctx, Assignment{Job: j, Fence: fence}, job) // returns at once; the stream lives on ctx
		} else {
			wctx, cancel := context.WithTimeout(ctx, a.d.Poll)
			out, err = a.d.Hub.Adopt(wctx, Assignment{Job: j, Fence: fence}, job)
			cancel()
		}
		if err == nil {
			a.event(ctx, j, EventJobAdopted, "server_restart")
			return out, true, nil
		}
		if ctx.Err() != nil {
			return StepOutput{}, true, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			cur, gerr := a.d.Ledger.Get(ctx, j.ID)
			if gerr != nil {
				return StepOutput{}, true, gerr
			}
			if cur.Generation != j.Generation || !cur.State.HoldsSlot() {
				return StepOutput{}, false, nil // recovered by someone else (lost-runner handler, revocation)
			}
			r, _, rerr := a.d.Registry.GetRunner(ctx, j.RunnerID)
			if rerr == nil && r.Liveness != LivenessLost {
				continue // online, suspect or offline-in-grace: keep waiting
			}
			if rerr != nil && !errors.Is(rerr, ErrRunnerNotFound) {
				return StepOutput{}, true, rerr
			}
			err = WithOutcome(fmt.Errorf("%w: %s", ErrRunnerLost, j.RunnerID), SourceOutcome{Class: OutcomeRunnerLost})
		}
		if errors.Is(err, ErrJobAbsentOnReconnect) { // decision D6: checked before the generic loss below
			return StepOutput{}, false, a.recover(ctx, j, "reconnect_absent", SupersedeReconnectAbsent)
		}
		if errors.Is(err, ErrRunnerRevoked) {
			return StepOutput{}, false, a.recover(ctx, j, "runner_revoked", SupersedeRevoked)
		}
		if errors.Is(err, ErrRunnerLost) || ClassifyError(err).Class == OutcomeRunnerLost {
			return StepOutput{}, false, a.recover(ctx, j, "runner_lost", SupersedeRunnerLost)
		}
		return StepOutput{}, true, err
	}
}

// recover fences the job, stops a built-in writer, takes its claim over on
// durable evidence and frees it, then records job.recovered.
func (a *JobAdopter) recover(ctx context.Context, j Job, reason string, kind SupersedeKind) error {
	f := j.Fence()
	if _, err := a.d.Ledger.Requeue(ctx, f, OutcomeRunnerLost); err != nil && !errors.Is(err, ErrFenced) {
		return fmt.Errorf("runner: recover job %s: %w", j.ID, err)
	}
	if j.RunnerID == BuiltInRunnerID {
		if err := a.d.Stopper.StopAndWait(ctx, f); err != nil {
			return fmt.Errorf("runner: recover job %s: stop: %w", j.ID, err)
		}
	}
	if err := takeOverJobClaim(ctx, a.d.Leases, j, kind); err != nil {
		return err
	}
	if kind == SupersedeReconnectAbsent {
		detail, _ := json.Marshal(map[string]any{"runnerId": j.RunnerID, "jobId": j.ID.String(), "generation": j.Generation,
			"runId": j.Key.RunID.String(), "nodeId": j.NodeID, "evidence": string(kind)})
		a.d.Audit.Record(ctx, audit.ActionRunnerJobReconnectAbsent, "job:"+j.ID.String(), string(detail))
	}
	a.event(ctx, j, EventJobRecovered, reason)
	return nil
}

// takeOverJobClaim supersedes and frees the run's workspace claim when THIS
// exact job generation holds it, so the requeued step can claim it again.
func takeOverJobClaim(ctx context.Context, leases ResourceLeaseStore, j Job, kind SupersedeKind) error {
	rec, ok, err := leases.Active(ctx, runKey(j.Key.RunID))
	if err != nil {
		return fmt.Errorf("runner: read workspace claim of run %s: %w", j.Key.RunID, err)
	}
	old := j.Fence()
	if !ok || rec.Job == nil || *rec.Job != old {
		return nil // hermetic op, or the claim is not this job's
	}
	ref, err := leases.Supersede(ctx, rec.Fence, SupersedeProof{Kind: kind, OldJob: &old}, recoveryHolder(j.Key.RunID), BuiltInRunnerID)
	if err != nil {
		return fmt.Errorf("runner: take over the workspace claim of job %s (%s): %w", j.ID, kind, err)
	}
	if err := leases.Release(ctx, ref); err != nil {
		return fmt.Errorf("runner: free the recovered workspace claim of run %s: %w", j.Key.RunID, err)
	}
	return nil
}

func (a *JobAdopter) event(ctx context.Context, j Job, kind, reason string) {
	run := j.Key.RunID
	detail := map[string]any{"jobId": j.ID.String(), "nodeId": j.NodeID, "generation": j.Generation, "reason": reason}
	cause := Cause{Kind: CauseSystem}
	if kind == EventJobRecovered {
		detail["notice"] = instance.RecoveryNoticeText
		cause.Outcome = OutcomeRunnerLost
	}
	a.d.Events.Record(ctx, PoolEvent{ID: uuid.New(), At: a.d.Clock(), Kind: kind, RunnerID: j.RunnerID, SourceID: j.SourceID,
		RunID: &run, Cause: cause, Detail: detail})
}
