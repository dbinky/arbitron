package modelcatalog

import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
)

const (
	UsabilityConfirmed   = "confirmed"
	UsabilityUnavailable = "unavailable"
	UsabilityUnknown     = "unknown"
)

// UsabilityEvidence references an exact-model execution on a particular path
// configuration. Persistence consumes evidence; it does not manufacture proof.
type UsabilityEvidence struct {
	CanonicalID, PathKey, NativeModel, ConfigRevision, ProofRef string
	// Provider is the catalog provider of the entry this path checked. It is
	// provenance for announcements, not part of the proof key; it is empty on
	// evidence stored before migration 036.
	Provider  string
	CheckedAt time.Time
	State     string
	// Failures counts consecutive non-confirming checks of this exact
	// (model, path, configuration). It is scheduling state only: it drives
	// retry backoff and retirement, never eligibility.
	Failures int
}

// Confirmed requires every load-bearing field of the persisted execution proof.
func (e UsabilityEvidence) Confirmed() bool {
	return e.State == UsabilityConfirmed && e.CanonicalID != "" && e.PathKey != "" && e.NativeModel != "" && e.ConfigRevision != "" && e.ProofRef != "" && !e.CheckedAt.IsZero()
}

// UsabilityProbe checks whether one catalog entry executes on one path. A
// listing, an installed binary or a request echo never confirms: only a
// verified exact-model execution does. A failed or unverifiable check is
// reported as unknown evidence (nil error); an error means the check could not
// be attempted or the caller's context ended.
type UsabilityProbe interface {
	Check(context.Context, ExecutionPath, modelidentity.Entry) (UsabilityEvidence, error)
}
