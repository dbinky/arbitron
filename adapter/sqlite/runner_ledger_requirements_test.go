// adapter/sqlite/runner_ledger_requirements_test.go
package sqlite

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ runner.JobRequirements = (*RunnerLedgerRepo)(nil)

func TestRunnerLedgerRepo_RequirementsReturnsThePersistedRequest(t *testing.T) {
	ctx := context.Background()
	repo := NewRunnerLedgerRepo(setupTestDB(t).DB)
	run := uuid.New()
	req := runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"},
		Provider: "anthropic", Model: "claude-opus-5-5", Backend: "claude-code", Op: runner.OpAgent, Affinity: runner.AffinityWorkspace,
		RunID: run, WorkspaceBytes: 4096, Tools: []string{"Read", "Edit"}}
	j, err := repo.EnsureIntent(ctx, runner.JobKey{RunID: run, InvocationID: uuid.New(), OperationID: "edit", Attempt: 1}, "edit", req)
	require.NoError(t, err)

	got, err := repo.Requirements(ctx, j.ID)
	require.NoError(t, err)
	assert.Equal(t, req, got)

	_, err = repo.Requirements(ctx, uuid.New())
	require.ErrorIs(t, err, runner.ErrJobNotFound)
}
