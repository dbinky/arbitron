package runner_test

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAdoptionBook_LossBeforeWaitIsNotMissed(t *testing.T) {
	var b runner.AdoptionBook
	f := runner.Fence{JobID: uuid.New(), RunnerID: "mac-1", Generation: 1}
	b.FailRunner(f.RunnerID, runner.ErrRunnerLost)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := b.Wait(ctx, f)
	require.ErrorIs(t, err, runner.ErrRunnerLost)
}
