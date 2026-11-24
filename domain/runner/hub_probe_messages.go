package runner

import (
	"context"

	"github.com/google/uuid"
)

const (
	MsgProbe       = "probe"
	MsgProbeResult = "probe_result"
)

// ProbePayload requests one fresh read-only credential observation.
type ProbePayload struct {
	RequestID      uuid.UUID
	HealthRevision uint64
	SourceKind     CredentialKind
	CredentialRef  string
}

// ProbeReply correlates a fresh observation with its request.
type ProbeReply struct {
	RequestID      uuid.UUID
	HealthRevision uint64
	Source         SourceReport
}

// HubHealth serializes observations with classified execution outcomes.
type HubHealth interface {
	HealthTransitioner
	ObserveProbe(context.Context, SourceID, ProbeResult) (SourceHealth, error)
}
