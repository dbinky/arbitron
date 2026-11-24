package engine

import (
	"context"

	"github.com/google/uuid"
)

// SessionKey addresses one turn's session checkpoint.
type SessionKey struct {
	RunID        uuid.UUID
	InvocationID uuid.UUID
	TurnIndex    int
}

// SessionCheckpointStore keeps checksummed session bundles. Put and Get verify
// the checksum; Prune keeps keepRef (the committed checkpoint) plus the
// store's retention of older turns and deletes everything else for the
// invocation, including orphans of uncommitted turns.
type SessionCheckpointStore interface {
	Put(ctx context.Context, key SessionKey, b SessionBundle) (ref string, err error)
	Get(ctx context.Context, ref string) (SessionBundle, error)
	Prune(ctx context.Context, runID, invocationID uuid.UUID, keepRef string) error
}
