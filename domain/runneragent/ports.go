package runneragent

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
)

// Clock is the agent's time seam; tests inject a gating fake.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// SystemClock is the production Clock.
type SystemClock struct{}

// Now returns the wall clock.
func (SystemClock) Now() time.Time { return time.Now() }

// After is time.After.
func (SystemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Session is one live connection to the server.
type Session interface {
	Send(ctx context.Context, m Outbound) error
	Recv(ctx context.Context) (Inbound, error)
	Close() error
}

// Connection dials the server.
type Connection interface {
	Dial(ctx context.Context) (Session, error)
}

// EmitFunc delivers one output token. It blocks while the spool is full
// (backpressure) and returns ctx's error when the job is cancelled or fenced.
type EmitFunc func(ctx context.Context, token string) error

// JobExecutor runs dispatched jobs with the linked harness adapters.
type JobExecutor interface {
	// Supports reports whether this runner can execute d; reason is shown to
	// the server on reject.
	Supports(d Dispatch) (ok bool, reason string)
	// Execute runs d to completion. emit is never called concurrently for one
	// job. The returned Result carries the classified outcome.
	Execute(ctx context.Context, d Dispatch, emit EmitFunc) Result
}

// LocalSources reports the runner's capacity sources.
type LocalSources interface {
	Sources(ctx context.Context) []Source
	Probe(ctx context.Context) []Source
}

// FreshSourceProber checks exactly one local source now, never a cached sample.
type FreshSourceProber interface {
	ProbeSource(context.Context, runner.CredentialKind, string) (Source, error)
}

var ErrUnknownSource = errors.New("runneragent: no such local source")

// SpooledJob is a job's retained unacknowledged state.
type SpooledJob struct {
	Ref       JobRef
	LastAcked uint64
	Frames    []OutputFrame // unacked, ascending Seq
	Result    *Result       // terminal result awaiting server acknowledgment
}

// Spool persists unacked output frames and terminal results until the server
// acknowledges them. It is bounded by unacked frame count; a full spool
// refuses (ErrSpoolFull) and never drops.
type Spool interface {
	AppendOutput(ctx context.Context, ref JobRef, f OutputFrame) error
	PutResult(ctx context.Context, ref JobRef, r Result) error
	Ack(ctx context.Context, ref JobRef, seq uint64) error
	AckResult(ctx context.Context, ref JobRef) error
	Drop(ctx context.Context, ref JobRef) error
	Get(ctx context.Context, ref JobRef) (SpooledJob, bool, error)
	Pending(ctx context.Context) ([]SpooledJob, error)
}

// IdentityStore holds the machine ed25519 keypair (D8).
type IdentityStore interface {
	Key(ctx context.Context) (ed25519.PrivateKey, error)
}

var (
	// ErrSpoolFull is returned by AppendOutput at capacity.
	ErrSpoolFull = errors.New("runneragent: output spool full")
	// ErrSpoolSequence is returned for a non-contiguous output sequence.
	ErrSpoolSequence = errors.New("runneragent: output sequence not contiguous")
	// ErrRefused matches every *RefusedError.
	ErrRefused = errors.New("runneragent: admission refused")
)

// RefusedError is a typed admission refusal from the server.
type RefusedError struct {
	Code   string
	Reason string
}

func (e *RefusedError) Error() string {
	return "runneragent: admission refused: " + e.Code + ": " + e.Reason
}

// Is makes errors.Is(err, ErrRefused) true.
func (e *RefusedError) Is(target error) bool { return target == ErrRefused }
