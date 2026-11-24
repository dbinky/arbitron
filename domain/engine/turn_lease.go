package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// ErrInteractiveTurnInProgress is the unchanged overlap contract: the same
// sentinel the HTTP/MCP/WS surfaces already map to 409.
var ErrInteractiveTurnInProgress = instance.ErrInteractiveTurnInProgress

// ErrTurnLeaseLost means the holder can no longer prove lease ownership.
// The holder must stop; expiry alone never authorizes another holder.
var ErrTurnLeaseLost = errors.New("engine: turn lease lost to another holder")

// DefaultTurnLeaseTTL is the legacy default; production passes
// runners.turnleaseseconds.
const DefaultTurnLeaseTTL = 60 * time.Second

// TurnLease is the single-owner right to run one interactive operation on a
// session. Generation grows on moves and proof-gated replay recovery, never on
// a clean re-acquire or merely because the lease expired.
type TurnLease struct {
	Key        string // "run:<id>" or "resolution:<identity>"
	Token      uuid.UUID
	Generation int64
	ExpiresAt  time.Time
}

// Turn intent kinds.
const (
	TurnIntentFirst   = "first"
	TurnIntentMessage = "message"
	TurnIntentEnd     = "end"
	TurnIntentAbandon = "abandon"
	TurnIntentMove    = "move"
)

// TurnIntent is the immutable, durable resend source for an accepted operation.
type TurnIntent struct {
	ID                                   uuid.UUID
	RequestID                            uuid.UUID
	Binding                              AcceptedTurnBinding
	LeaseKey                             string
	RunID, InvocationID                  uuid.UUID
	NodeID                               string
	TurnIndex                            int
	Kind                                 string
	Prompt, SystemPrompt, Nonce, Message string
	Model                                string
	Tools                                []string
	PriorCheckpointRef                   string
	SessionGeneration                    int64
	OperationID                          string
	CreatedAt                            time.Time
	CompletedAt                          *time.Time
}

// TurnIntentStore persists accepted operations before dispatch or acknowledgement.
type TurnIntentStore interface {
	Persist(context.Context, TurnIntent) error
	Complete(context.Context, uuid.UUID) error
	Pending(context.Context) ([]TurnIntent, error)
	PendingFor(context.Context, string) (*TurnIntent, error)
}

// TurnLeaseStore is the durable lease port.
type TurnLeaseStore interface {
	Acquire(ctx context.Context, key string, ttl time.Duration) (TurnLease, error)
	Renew(ctx context.Context, l TurnLease, ttl time.Duration) (TurnLease, error)
	Release(ctx context.Context, l TurnLease) error
	BumpGeneration(ctx context.Context, l TurnLease) (TurnLease, error)
	Current(ctx context.Context, key string) (TurnLease, bool, error)
}

// RunLeaseKey is the lease key of an ordinary interactive session.
func RunLeaseKey(runID uuid.UUID) string { return "run:" + runID.String() }

// ResolutionLeaseKey is the lease key of a group conflict-resolution session.
func ResolutionLeaseKey(id GroupResolutionIdentity) string {
	return fmt.Sprintf("resolution:%s/%d/%d", id.GroupRunID, id.MergeIndex, id.Attempt)
}

type turnLeaseCtxKey struct{}

// ContextWithTurnLease carries the held lease so a mover called inside the turn
// reuses it instead of acquiring recursively.
func ContextWithTurnLease(ctx context.Context, l TurnLease) context.Context {
	return context.WithValue(ctx, turnLeaseCtxKey{}, l)
}

// TurnLeaseFromContext returns the lease held by the current operation.
func TurnLeaseFromContext(ctx context.Context) (TurnLease, bool) {
	if h, ok := ctx.Value(turnLeaseCtxKey{}).(*turnLeaseHold); ok {
		return h.Lease(), true
	}
	l, ok := ctx.Value(turnLeaseCtxKey{}).(TurnLease)
	return l, ok
}

// memoryTurnLeaseStore reproduces the pre-Phase-4 in-process guard. Leases
// never expire. Production always replaces it via SetTurnLeases.
type memoryTurnLeaseStore struct {
	mu   sync.Mutex
	held map[string]TurnLease
	gens map[string]int64
}

// NewMemoryTurnLeaseStore returns the in-process store (legacy unit tests only).
func NewMemoryTurnLeaseStore() TurnLeaseStore {
	return &memoryTurnLeaseStore{held: map[string]TurnLease{}, gens: map[string]int64{}}
}

func (s *memoryTurnLeaseStore) Acquire(_ context.Context, key string, ttl time.Duration) (TurnLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, busy := s.held[key]; busy {
		return TurnLease{}, fmt.Errorf("turn lease %s: %w", key, ErrInteractiveTurnInProgress)
	}
	gen := s.gens[key]
	if gen == 0 {
		gen = 1
		s.gens[key] = gen
	}
	l := TurnLease{Key: key, Token: uuid.New(), Generation: gen, ExpiresAt: time.Now().Add(ttl)}
	s.held[key] = l
	return l, nil
}

func (s *memoryTurnLeaseStore) Renew(_ context.Context, l TurnLease, ttl time.Duration) (TurnLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.held[l.Key]
	if !ok || cur.Token != l.Token {
		return TurnLease{}, ErrTurnLeaseLost
	}
	cur.ExpiresAt = time.Now().Add(ttl)
	s.held[l.Key] = cur
	return cur, nil
}

func (s *memoryTurnLeaseStore) Release(_ context.Context, l TurnLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.held[l.Key]; ok && cur.Token == l.Token {
		delete(s.held, l.Key)
	}
	return nil
}

func (s *memoryTurnLeaseStore) BumpGeneration(_ context.Context, l TurnLease) (TurnLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.held[l.Key]
	if !ok || cur.Token != l.Token {
		return TurnLease{}, ErrTurnLeaseLost
	}
	s.gens[l.Key]++
	cur.Generation = s.gens[l.Key]
	s.held[l.Key] = cur
	return cur, nil
}

func (s *memoryTurnLeaseStore) Current(_ context.Context, key string) (TurnLease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.held[key]
	return l, ok, nil
}

// turnLeaseHold owns one acquired lease for the duration of an operation: it
// renews every ttl/3 and cancels the operation's context (cause
// ErrTurnLeaseLost) the moment the lease is lost, so a holder that cannot
// prove ownership stops instead of committing beside a new owner.
type turnLeaseHold struct {
	store  TurnLeaseStore
	ttl    time.Duration
	cancel context.CancelCauseFunc
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
	local  func() // non-nil => no store: no lease, no renewal; Release runs local once

	mu    sync.Mutex
	lease TurnLease
}

// localTurnHold is the hold used when no lease store is wired (legacy tests):
// no token, generation 0, no renewal; Release runs release once.
func localTurnHold(key string, release func()) *turnLeaseHold {
	return &turnLeaseHold{local: release, lease: TurnLease{Key: key}}
}

// holdTurnLease acquires key and starts renewal. A nil store (SetTurnLeases
// never called) yields a local no-op hold: pre-Phase-4 behaviour.
func holdTurnLease(ctx context.Context, store TurnLeaseStore, key string, ttl time.Duration) (context.Context, *turnLeaseHold, error) {
	if store == nil {
		return ctx, localTurnHold(key, func() {}), nil
	}
	l, err := store.Acquire(ctx, key, ttl)
	if err != nil {
		return ctx, nil, err
	}
	turnCtx, cancel := context.WithCancelCause(ctx)
	h := &turnLeaseHold{store: store, ttl: ttl, cancel: cancel, stop: make(chan struct{}), done: make(chan struct{}), lease: l}
	go h.renewLoop(turnCtx)
	// Keep derived attempt contexts attached to the held lease, not a stale
	// generation snapshot captured before a move.
	return context.WithValue(turnCtx, turnLeaseCtxKey{}, h), h, nil
}

func (h *turnLeaseHold) renewLoop(ctx context.Context) {
	defer close(h.done)
	interval := h.ttl / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		cur := h.Lease()
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), interval)
		renewed, err := h.store.Renew(rctx, cur, h.ttl)
		cancel()
		switch {
		case err == nil:
			h.mu.Lock()
			// A move may have adopted a newer generation while Renew was in flight.
			if h.lease == cur {
				h.lease = renewed
			}
			h.mu.Unlock()
		case errors.Is(err, ErrTurnLeaseLost):
			h.cancel(ErrTurnLeaseLost)
			return
		default:
			// A transient store error: keep the turn only while the lease we
			// last proved is unexpired. Expiry stops this turn but does not
			// authorize another holder to acquire the occupied lease.
			if !time.Now().Before(cur.ExpiresAt) {
				h.cancel(ErrTurnLeaseLost)
				return
			}
		}
	}
}

// Lease returns the currently held lease (renewals refresh ExpiresAt).
func (h *turnLeaseHold) Lease() TurnLease {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lease
}

// Adopt accepts a move's reserved generation only for this same lease owner.
func (h *turnLeaseHold) Adopt(l TurnLease) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if l.Token == h.lease.Token && l.Key == h.lease.Key && l.Generation >= h.lease.Generation {
		h.lease = l
	}
}

// Release stops renewal and releases the lease. Idempotent.
func (h *turnLeaseHold) Release() {
	if h.local != nil {
		h.once.Do(h.local)
		return
	}
	h.once.Do(func() {
		close(h.stop)
		<-h.done
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.store.Release(ctx, h.Lease())
		h.cancel(nil)
	})
}

// turnLeaseErr re-labels a turn failure caused by a lost lease so callers can
// tell "fenced" from an ordinary provider error.
func turnLeaseErr(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); errors.Is(cause, ErrTurnLeaseLost) && !errors.Is(err, ErrTurnLeaseLost) {
		if err == nil {
			return ErrTurnLeaseLost
		}
		return fmt.Errorf("%w: %v", ErrTurnLeaseLost, err)
	}
	return err
}

// ErrTurnRecoveryProofInvalid rejects a recovery proof that does not establish
// adoption of the exact live owner or a fenced, stopped previous owner.
var ErrTurnRecoveryProofInvalid = errors.New("engine: turn recovery proof is invalid")

// TurnRecoveryProof is built ONLY by P4 T10's TurnRecoveryCoordinator from
// durable job/resource/stop records. Elapsed TTL is never part of it.
type TurnRecoveryProof struct {
	// StaleHolder proves an earlier boot accepted an intent but never dispatched it.
	StaleHolder bool
	IntentID    uuid.UUID
	Adopted     bool
	Jobs        []JobFenceRef
	Resource    *ResourceFenceRef
}

// Validate checks the proof's shape; the store re-checks every fact in SQL.
func (p TurnRecoveryProof) Validate() error {
	if p.StaleHolder {
		if p.IntentID == uuid.Nil || p.Adopted || len(p.Jobs) != 0 || p.Resource != nil {
			return fmt.Errorf("%w: a stale-holder proof names only the intent", ErrTurnRecoveryProofInvalid)
		}
		return nil
	}
	if p.IntentID == uuid.Nil {
		return fmt.Errorf("%w: no intent", ErrTurnRecoveryProofInvalid)
	}
	if len(p.Jobs) == 0 {
		return fmt.Errorf("%w: no job evidence (expiry alone is not proof)", ErrTurnRecoveryProofInvalid)
	}
	if p.Adopted && len(p.Jobs) != 1 {
		return fmt.Errorf("%w: adoption names exactly one live job", ErrTurnRecoveryProofInvalid)
	}
	for _, j := range p.Jobs {
		if j.JobID == uuid.Nil || j.RunnerID == "" || j.Generation < 1 {
			return fmt.Errorf("%w: incomplete job fence", ErrTurnRecoveryProofInvalid)
		}
	}
	if p.Resource != nil {
		if err := p.Resource.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrTurnRecoveryProofInvalid, err)
		}
	}
	return nil
}

// TurnLeaseRecoveryStore reclaims an occupied lease from durable proof only.
// Adoption keeps the generation; replay reserves the next one.
type TurnLeaseRecoveryStore interface {
	RecoverTurnLease(ctx context.Context, old TurnLease, proof TurnRecoveryProof, ttl time.Duration) (TurnLease, error)
}
