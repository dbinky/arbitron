package layout

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)

// State is the durable lifecycle of one saved revision's arrangement job.
type State string

const (
	StatePending        State = "pending"
	StateInProgress     State = "in_progress"
	StatePassed         State = "passed"
	StateNeedsAttention State = "needs_attention"
	StateFailed         State = "failed"
	StateSuperseded     State = "superseded"
)

// Destination is a storage locator plus optional stable identity metadata.
type Destination struct {
	Scope      workflow.Scope
	Name       string
	WorkflowID uuid.UUID
}

// Record keeps immutable input and mutable, generation-fenced job status.
type Record struct {
	Revision         string
	Destination      Destination
	Reason           workflow.SaveReason
	Manifest         []byte
	Retained         workflow.RetainedLayout
	State            State
	Stage            Stage
	JobGeneration    int64
	AttemptsUsed     int
	AttemptLimit     int
	Verdict          Verdict
	Findings         []Finding
	Unreviewed       bool
	FailureStage     Stage
	FailureReason    string
	ResultGeneration int64
	CacheGeneration  int64
	LeaseOwner       string
	LeaseExpiresAt   *time.Time
	RequestedAt      time.Time
	UpdatedAt        time.Time
	StartedAt        *time.Time
	FinishedAt       *time.Time
}

// StoredResult is an immutable candidate, addressed by revision and generation.
type StoredResult struct {
	Revision   string
	Generation int64
	Geometry   Geometry
	Review     *Review
	Preflight  []Finding
	Verdict    Verdict
	Score      Score
	CreatedAt  time.Time
}

// NewRequest captures a committed save independently of subsequent source changes.
type NewRequest struct {
	Revision     string
	Destination  Destination
	Reason       workflow.SaveReason
	Manifest     []byte
	Retained     workflow.RetainedLayout
	AttemptLimit int
	RequestedAt  time.Time
}

// Fence identifies the job generation permitted to mutate a revision's status.
type Fence struct {
	Revision   string
	Generation int64
}

// Completion optionally inserts a candidate and atomically selects the best result.
type Completion struct {
	State         State
	AttemptsUsed  int
	FailureStage  Stage
	FailureReason string
	Result        *StoredResult
	ReplaceBest   bool
}

var (
	ErrRecordNotFound   = errors.New("layout: revision record not found")
	ErrStaleGeneration  = errors.New("layout: job generation is no longer current")
	ErrNotClaimable     = errors.New("layout: revision is not claimable")
	ErrRetryConflict    = errors.New("layout: revision is not eligible for retry")
	ErrStoreUnavailable = errors.New("layout: status store unavailable")
	// ErrStoreCorrupt identifies readable storage with undecodable authoritative
	// data. It must never authorize the outage-only cache fallback.
	ErrStoreCorrupt    = errors.New("layout: corrupt stored data")
	ErrNotCurrent      = errors.New("layout: revision is not the destination's current revision")
	ErrDestinationGone = errors.New("layout: destination workflow no longer exists")
)

// Store owns revision history. Implementations copy all mutable values, honor
// cancellation, preserve subsecond UTC timestamps, and fence writes atomically.
// Request never supersedes another revision; result history survives relocation.
type Store interface {
	Request(context.Context, NewRequest) (Record, bool, error)
	Get(context.Context, string) (Record, error)
	ListActive(context.Context, Destination) ([]Record, error)
	Claimable(context.Context, time.Time, int) ([]string, error)
	Claim(context.Context, string, string, time.Time, time.Time) (Record, error)
	Progress(context.Context, Fence, Stage, int, time.Time, time.Time) error
	Complete(context.Context, Fence, Completion, time.Time) error
	Checkpoint(context.Context, Fence, StoredResult, bool, time.Time) error
	Supersede(context.Context, string, time.Time) error
	Retry(context.Context, string, int, time.Time) (Record, error)
	Relocate(context.Context, Destination, Destination, time.Time) (int64, error)
	Result(context.Context, string, int64) (StoredResult, error)
	PendingCache(context.Context, int) ([]Record, error)
	MarkCached(context.Context, Fence, int64, Destination) error
	InvalidateResult(context.Context, Fence, int64, string, time.Time) error
}

const CacheFormatV1 = "arbitron.layout-cache/v1"

// CacheEntry is an optional publication, not the authoritative status or history.
type CacheEntry struct {
	Format        string           `json:"format"`
	Revision      string           `json:"revision"`
	Generation    int64            `json:"generation"`
	JobGeneration int64            `json:"jobGeneration"`
	Contract      RendererContract `json:"contract"`
	Verdict       Verdict          `json:"verdict"`
	Geometry      Geometry         `json:"geometry"`
}

// CachePublisher checks the fence while holding the destination mutation lock.
type CachePublisher interface {
	PublishCache(context.Context, Destination, CacheEntry, func(context.Context) error) error
}

// CacheReader provides outage-only fallback without asserting reviewed acceptance.
type CacheReader interface {
	ReadCache(context.Context, workflow.Scope, string) (CacheEntry, bool, error)
}

// LocatorKey ignores shared owners and stable IDs: only the storage locator identifies a destination.
func LocatorKey(scope workflow.Scope, name string) string {
	if scope.Kind == workflow.ScopeShared {
		return "shared/" + name
	}
	return "private/" + scope.OwnerID.String() + "/" + name
}
func (d Destination) Key() string { return LocatorKey(d.Scope, d.Name) }
func (s State) Terminal() bool {
	return s == StatePassed || s == StateNeedsAttention || s == StateFailed || s == StateSuperseded
}

// UnresolvedFindings returns independently owned findings, including nested occurrence IDs.
func (r StoredResult) UnresolvedFindings() []Finding {
	out := append([]Finding{}, r.Preflight...)
	if r.Review != nil {
		out = append(out, r.Review.Findings...)
	}
	for i := range out {
		out[i].NodeIDs = slices.Clone(out[i].NodeIDs)
		out[i].EdgeOrdinals = slices.Clone(out[i].EdgeOrdinals)
	}
	return out
}
func (r StoredResult) IsUnreviewed() bool { return !HasRenderedEvidence(r.Geometry, r.Review) }
