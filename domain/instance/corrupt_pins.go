package instance

import (
	"fmt"

	"github.com/google/uuid"
)

// CorruptPinsError reports execution rows whose stored model pin record cannot
// be decoded. Queue-admission and recovery readers return it alongside the
// healthy rows they could read; they never change the corrupt rows themselves.
// The caller owns the lifecycle and must drive each ID through the engine's
// complete terminal path (fail, instance.failed event, pin-cache release, queue
// capacity), since a corrupt run can never execute.
type CorruptPinsError struct {
	IDs []uuid.UUID
}

func (e *CorruptPinsError) Error() string {
	return fmt.Sprintf("instance: %d run(s) with corrupt model pins: %v", len(e.IDs), e.IDs)
}
