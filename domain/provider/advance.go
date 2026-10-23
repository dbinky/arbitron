package provider

import (
	"context"
	"errors"
	"time"
)

// AdvanceRequest captures the entire expected profile and path-evidence revision.
// The request ID is stable across retries of the same atomic advancement.
// ExecutionRevision is the inventory's token for PathRevision: the store
// validates its persisted policy and connection revisions inside the commit
// transaction, so a revision bumped by another process after the gate's
// in-process recheck still blocks the advancement.
type AdvanceRequest struct {
	ID                string
	RefreshID         string
	Expected          Profile
	Model             string
	CanonicalID       string
	PathRevision      string
	ExecutionRevision ExecutionRevision
	// Evidence names every execution path that consumes the alias at the
	// inventory snapshot, with its configuration revision. The commit requires
	// confirmed usability evidence for exactly these (path, revision) pairs, so
	// proof gathered before a credential or launch-configuration change cannot
	// carry an advancement.
	Evidence []PathEvidenceRef
	At       time.Time
}

// PathEvidenceRef identifies one execution path configuration.
type PathEvidenceRef struct{ PathKey, ConfigRevision string }

// Advancement is the durable result of an atomic model/revision change.
type Advancement struct {
	ID, RefreshID, Alias, FromModel, ToModel, CanonicalID string
	Revision                                              int64
	At                                                    time.Time
}

var ErrAdvanceConflict = errors.New("provider: advancement expected profile changed")

// AdvanceValidator checks replacement configuration and route versions without
// SQL, network, lease acquisition or mutation. Automatic selection uses the
// same validation as direct advancement before proposing a conditional write.
type AdvanceValidator interface {
	ValidateAdvance(context.Context, AdvanceRequest) error
}

// AdvanceStore commits a conditional model update and journal entry together.
type AdvanceStore interface {
	// ReplayAdvance reads an already committed result without checking current
	// execution evidence or writing a model. Reusing an ID with a different
	// alias, from/to model, canonical identity or expected revision conflicts.
	ReplayAdvance(context.Context, AdvanceRequest) (Advancement, bool, error)
	CommitAdvance(context.Context, AdvanceRequest) (Advancement, error)
}

// EligibleCandidate identifies one exact model and the prospective execution
// configuration on which its eligibility was proved.
type EligibleCandidate struct {
	Model, CanonicalID, PathRevision string
	ExecutionRevision                ExecutionRevision
}

// Clone returns a candidate whose execution proof is owned by the caller.
func (c EligibleCandidate) Clone() EligibleCandidate {
	c.ExecutionRevision = c.ExecutionRevision.Clone()
	return c
}

// EligibleCandidateSource supplies evidenced versions, never guessed aliases.
// Successful selection returns the exact model, canonical identity, non-empty
// prospective PathRevision and its ExecutionRevision token.
type EligibleCandidateSource interface {
	Newest(context.Context, Profile) (EligibleCandidate, error)
	// ValidateStart proves the supplied Model without substituting a newer one.
	ValidateStart(context.Context, Profile) (EligibleCandidate, error)
	// VersionRequests reads actual prospective routing metadata, including the
	// connection's routing provider and effective backend, without fetch/probe.
	// Success requires non-empty requests; the follow provider is not a route.
	VersionRequests(context.Context, Profile) ([]VersionRequest, error)
	// RevalidateStart checks inventory/configuration/policy/evidence against the
	// exact prospective profile and candidate. It is read-only, does no network
	// work, takes no gate/coordinator lock and never mutates a profile.
	RevalidateStart(context.Context, Profile, EligibleCandidate) error
}
