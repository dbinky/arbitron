package modelcatalog

import (
	"context"
	"errors"
)

// MaxAnnouncementBytes bounds each fully serialized card and response read.
const MaxAnnouncementBytes = 28 * 1024

const (
	SendConfirmed       = "confirmed"
	SendDefiniteFailure = "definite_failure"
	SendAmbiguous       = "ambiguous"
	// SendDeferred releases a claim when a prerequisite prevents the next send.
	// It is a service/store outcome, never an HTTP sender result.
	SendDeferred = "deferred"
)

var ErrAnnouncementPayload = errors.New("model announcement: invalid or oversized payload")

type SendOutcome struct{ State, Code string }

// AnnouncementPart retains one event's identity across numbered cards. Index is
// one-based. Delivery acknowledgements belong to the durable announcement store.
type AnnouncementPart struct {
	EventID      string
	Index, Count int
	Payload      AnnouncementPayload
}

// ModelAnnouncementSender reports one synchronous HTTP attempt, not receiver
// idempotency. An ambiguous attempt can be delivered again by the retry owner.
type ModelAnnouncementSender interface {
	Send(context.Context, string, AnnouncementPayload) (SendOutcome, error)
	SendPart(context.Context, string, AnnouncementPart) (SendOutcome, error)
}
