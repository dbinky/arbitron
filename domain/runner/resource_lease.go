package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/domain/engine"
)

// LeaseRecord is the durable state of one active resource claim.
type LeaseRecord struct {
	Fence  engine.ResourceFenceRef // exact active key/owner/generation/token
	Holder string                  // durable operation identity
	Job    *Fence                  // exact P1 ledger fence of the writing job; nil for server-side holders
	BootID string                  // server boot that took a server-side (Job == nil) claim; "" for job claims
}

// SupersedeKind names the evidence that lets recovery take over a claim.
type SupersedeKind string

const (
	SupersedeRunnerLost    SupersedeKind = "runner_lost"    // remote owner: runners.liveness = 'lost'
	SupersedeRevoked       SupersedeKind = "revoked"        // remote owner: runner_keys.revoked_at IS NOT NULL
	SupersedeStopped       SupersedeKind = "stopped"        // cancellation + confirmed stop of the old job's process
	SupersedeServerRestart SupersedeKind = "server_restart" // server-side holder from an earlier boot (design D7)
	// SupersedeReconnectAbsent: remote owner reconnected and its hello did not
	// list the job (runner_job_absences row; decision D6).
	SupersedeReconnectAbsent SupersedeKind = "reconnect_absent"
)

// SupersedeProof is the caller's claim of why the old holder can no longer
// write. The store re-reads every fact in its own transaction; the proof only
// selects which evidence must hold.
type SupersedeProof struct {
	Kind   SupersedeKind
	OldJob *Fence // must equal LeaseRecord.Job exactly when the lease has a job; nil otherwise
}

// ResourceLeaseStore is the single-writer gate over a resource such as a
// run workspace ("run:<uuid>"). There is no TTL and no liveness-only
// takeover: a suspect owner keeps its claim until Supersede proves loss,
// revocation, a confirmed stop or a server restart.
type ResourceLeaseStore interface {
	// Claim takes a free resource for a server-side holder (group mutation,
	// drain/maintenance, recovery restore) and records the current boot ID.
	Claim(ctx context.Context, key, holder string, owner RunnerID) (engine.ResourceFenceRef, error)
	// ClaimJob takes a free resource for a dispatched job whose exact fence is
	// active; owner = job.RunnerID. The same exact job fence and holder
	// re-claiming returns the existing ref (adoption/idempotent retry).
	ClaimJob(ctx context.Context, key, holder string, job Fence) (engine.ResourceFenceRef, error)
	Check(ctx context.Context, f engine.ResourceFenceRef) error
	Active(ctx context.Context, key string) (LeaseRecord, bool, error)
	Release(ctx context.Context, f engine.ResourceFenceRef) error
	Supersede(ctx context.Context, old engine.ResourceFenceRef, proof SupersedeProof, holder string, owner RunnerID) (engine.ResourceFenceRef, error)
}

// JobHolder is the durable holder identity of a dispatched job's claim.
func JobHolder(j Job) string {
	return fmt.Sprintf("job:%s/inv:%s/op:%s/attempt:%d", j.ID, j.Key.InvocationID, j.Key.OperationID, j.Key.Attempt)
}

var (
	ErrResourceBusy      = errors.New("runner: resource is held by another writer")
	ErrResourceFenced    = errors.New("runner: stale or unknown resource claim")
	ErrSupersedeUnproven = errors.New("runner: resource takeover is not proven by durable evidence")
)
