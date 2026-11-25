package runner

import (
	"context"

	"github.com/arbitron/arbitron/domain/engine"
)

// ResourceCommitter atomically publishes a staged server-side group mutation,
// advances the committed home pointer and releases its exact resource claim.
// Job-held mutations must instead commit through the step transaction.
type ResourceCommitter interface {
	CommitResource(ctx context.Context, claim engine.ResourceFenceRef, staged StagedCheckpoint) error
}
