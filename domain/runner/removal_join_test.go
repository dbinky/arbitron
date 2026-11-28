package runner_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingDeleter struct {
	runs []uuid.UUID
	err  error
}

func (d *recordingDeleter) Delete(_ context.Context, run uuid.UUID) error {
	d.runs = append(d.runs, run)
	return d.err
}

func TestJoinCheckpointDeleters_DeletesEveryStoreAndJoinsErrors(t *testing.T) {
	a, b := &recordingDeleter{err: errors.New("archive busy")}, &recordingDeleter{}
	run := uuid.New()
	err := runner.JoinCheckpointDeleters(a, b).Delete(context.Background(), run)
	require.ErrorContains(t, err, "archive busy")
	assert.Equal(t, []uuid.UUID{run}, a.runs)
	assert.Equal(t, []uuid.UUID{run}, b.runs, "a failing store does not stop the local copies being removed")
}
