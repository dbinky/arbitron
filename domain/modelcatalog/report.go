package modelcatalog

import (
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/provider"
)

// RefreshReport records observations and the actual committed profile changes,
// not planned proposals. PublicationPending is cleared only by acknowledgement.
type RefreshReport struct {
	ID                      string
	At                      time.Time
	SliceStatus, PathStatus map[string]string
	NewlyUsable             []Observation
	Changes                 []provider.Advancement
	PublicationPending      bool
	AnnouncementError       string
}

type Observation struct {
	Entry            modelidentity.Entry
	Paths            []UsabilityEvidence
	FirstConfirmedAt time.Time
}

type AnnouncementPayload struct {
	EventID, CanonicalID, Provider, Tier string
	FirstConfirmedAt                     time.Time
	Paths                                []UsabilityEvidence
	Changes                              []provider.Advancement
}

const (
	EventAwaitingSetting = "awaiting_setting"
	EventPending         = "pending"
	EventInFlight        = "in_flight"
	EventDeliveryUnknown = "delivery_unknown"
	EventAnnounced       = "announced"
	EventSuppressed      = "suppressed"
)

// Event's payload is immutable after first confirmation. Delivery transitions
// are separate from catalog refresh and publication acknowledgement.
type Event struct {
	Payload        AnnouncementPayload
	State          string
	ClaimID        string
	LeaseUntil     time.Time
	Attempts       int
	NextAttemptAt  time.Time
	LastCode       string
	Parts          []AnnouncementPart
	PartCount      int
	ConfirmedParts []int
}
