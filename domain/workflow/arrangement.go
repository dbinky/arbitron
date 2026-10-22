package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ArrangementRequest captures the exact saved manifest, independently of later
// saves or deletion. Scope and Name may move without changing Manifest.
type ArrangementRequest struct {
	Revision    string
	Scope       Scope
	Name        string
	WorkflowID  uuid.UUID
	Reason      SaveReason
	Manifest    []byte
	Retained    RetainedLayout
	RequestedAt time.Time
}

// ArrangementRequester durably accepts idempotent requests keyed by revision.
type ArrangementRequester interface {
	RequestArrangement(context.Context, ArrangementRequest) error
	RelocateArrangements(context.Context, Scope, string, Scope, string) error
}

// AcknowledgedArrangementReader exposes the immutable input durably accepted by
// an ArrangementRequester. Missing revisions return ErrNotFound. Seed recovery
// can use this secondary port after acknowledgement has cleaned the journal.
type AcknowledgedArrangementReader interface {
	AcknowledgedArrangement(context.Context, string) (ArrangementRequest, error)
}

// CommittedRevisionSource returns only proven committed inputs, never prepares.
type CommittedRevisionSource interface {
	CommittedRequests(context.Context) ([]ArrangementRequest, error)
}

// ErrMutationPending prevents overwriting undecided durable commit evidence.
var ErrMutationPending = errors.New("workflow: destination has an undecided durable mutation")

// DestinationSettler resolves prior operations under an already-held mutation lock.
type DestinationSettler interface {
	Settle(context.Context, Scope, string) error
}

// MutationTarget addresses a workflow tree to lock.
type MutationTarget struct {
	Scope Scope
	Name  string
}

// MutationCoordinator locks all targets for the entire authoring protocol. Its
// callback context carries lock ownership for reentrant repository operations.
type MutationCoordinator interface {
	WithMutations(context.Context, []MutationTarget, func(context.Context) error) error
}

// DisplayedLayoutReader supplies the currently displayed result's fallback.
type DisplayedLayoutReader interface {
	DisplayedLayout(context.Context, string) (RetainedLayout, bool, error)
}

// JournalState distinguishes input preparation from durable commit proof.
type JournalState string

const (
	JournalPrepared  JournalState = "prepared"
	JournalCommitted JournalState = "committed"
)

// JournalEntry pairs immutable input with its durable receipt state.
type JournalEntry struct {
	Request ArrangementRequest
	State   JournalState
}

// RelocationIntent preserves both endpoints until registry authority is settled.
type RelocationIntent struct {
	ID         string
	From       Scope
	FromName   string
	To         Scope
	ToName     string
	WorkflowID uuid.UUID
	CreatedAt  time.Time
}

// RevisionJournal stores prepared input, commit receipts and relocation intents.
// List is for display only; it is not proof that an operation committed.
type RevisionJournal interface {
	Record(context.Context, ArrangementRequest) error
	MarkCommitted(context.Context, string) error
	Remove(context.Context, string) error
	Quarantine(context.Context, string, string) error
	Entries(context.Context) ([]JournalEntry, error)
	List(context.Context) ([]ArrangementRequest, error)
	BeginRelocation(context.Context, Scope, string, Scope, string, uuid.UUID) (RelocationIntent, error)
	// RelocationIntents returns every readable intent. An intent whose durable
	// key names its endpoints but whose body is unreadable is reported as a
	// *DestinationError and left in place; it is never treated as settled.
	// A record that cannot be keyed to endpoints fails closed with a plain error.
	RelocationIntents(context.Context) ([]RelocationIntent, error)
	// RelocationIntentsAt returns the intents keyed to (scope, name) as either
	// endpoint, without reading any other intent's body. A matching intent whose
	// body is unreadable yields a *DestinationError wrapping ErrMutationPending,
	// so both of its endpoints stay blocked. Unkeyable records fail closed.
	RelocationIntentsAt(context.Context, Scope, string) ([]RelocationIntent, error)
	Relocate(context.Context, Scope, string, Scope, string) error
	EndRelocation(context.Context, string) error
}

// TreeCommit identifies a durable rollback snapshot for a staged replacement.
type TreeCommit struct {
	ID            string
	Destination   MutationTarget
	Revision      string
	PriorRevision string
	HadPrior      bool
}

// TreeCommitter preserves every prior tree byte until the commit is decided.
type TreeCommitter interface {
	PrepareTreeCommit(context.Context, MutationTarget, string) (TreeCommit, error)
	FinishTreeCommit(context.Context, string) error
	RollbackTreeCommit(context.Context, string) error
	PendingTreeCommits(context.Context) ([]TreeCommit, error)
}

// RevisionPublicationStore atomically commits publication identity and exact
// revision proof. Metadata-only PublishedStore.Upsert preserves that proof.
type RevisionPublicationStore interface {
	PublishedStore
	CommitPublication(context.Context, string, Publication) error
	RenamePublication(context.Context, uuid.UUID, string, string, Publication) error
}

// ErrStamperMisconfigured indicates a missing required save-side collaborator.
var ErrStamperMisconfigured = errors.New("workflow: revision stamper requires a journal, a requester and a settler")
