package runner

import (
	"context"
	"errors"
)

// BuiltInProcessState is the exact value persisted in 044
// builtin_processes.state.
type BuiltInProcessState string

const (
	// ProcessAbsent is the "from" state for a job that has no row at all.
	ProcessAbsent BuiltInProcessState = ""
	// ProcessPermitted is the durable launch permit. It is written before the
	// guardian is spawned. The guardian cannot do real work until it is released.
	ProcessPermitted BuiltInProcessState = "permitted"
	// ProcessRunning means the guardian PID (= process-group ID) and its start
	// marker are recorded, and the guardian may be released.
	ProcessRunning BuiltInProcessState = "running"
	// ProcessStopped is written only after confirmed whole-group exit (or for a
	// permit that was never released). ResourceLeaseRepo.Supersede(kind
	// "stopped") for a built-in owner reads exactly this value.
	ProcessStopped BuiltInProcessState = "stopped"
)

// BuiltInProcess is the durable identity of one supervised built-in job
// generation's process group.
type BuiltInProcess struct {
	Fence       Fence
	GuardianPID int
	StartMarker string
	State       BuiltInProcessState
}

// BuiltInProcessStore persists supervised process identity. Production:
// adapter/sqlite.BuiltInProcessRepo. Every method rejects a non-built-in or
// incomplete fence with ErrFenced.
type BuiltInProcessStore interface {
	// Permit moves absent|stopped -> permitted, in the same transaction that
	// checks the ledger fence is live (builtin, offered|running). A fenced job
	// is ErrFenced. A permitted or running row is ErrProcessActive.
	Permit(ctx context.Context, f Fence) error
	// Record moves permitted -> running with guardianPID > 1 and a non-empty
	// start marker. Any other current state is ErrProcessStateChanged.
	Record(ctx context.Context, f Fence, guardianPID int, startMarker string) error
	// MarkStopped moves from -> stopped (from == ProcessAbsent inserts a stopped
	// row). If the row is not in `from`, it returns ErrProcessStateChanged.
	MarkStopped(ctx context.Context, f Fence, from BuiltInProcessState) error
	Get(ctx context.Context, f Fence) (BuiltInProcess, bool, error)
}

// BuiltInStopper stops and waits for the exact process tree of one built-in
// job generation, then durably records `stopped`. Callers fence the job in
// the ledger first. An error means the stop is unconfirmed: never restore,
// supersede or retry on it. Producer:
// adapter/runner/builtin.ProcessSupervisor. Consumers: P3 T10
// RecoveryDeps.Stopper and P4 T10 TurnStopEvidenceDeps.Stopper.
type BuiltInStopper interface {
	StopAndWait(ctx context.Context, f Fence) error
}

var (
	ErrProcessActive       = errors.New("runner: a supervised process is already permitted or running for this job generation")
	ErrProcessStateChanged = errors.New("runner: supervised process state changed concurrently")
)
