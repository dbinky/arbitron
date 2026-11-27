package runner_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPoolRecovery_FreshStartupDoesNotDelayLossOfNewWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		builtin bool
	}{
		{name: "empty ledger"},
		{name: "only builtin work", builtin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			if tc.builtin {
				f.job(uuid.New(), "edit", runner.BuiltInRunnerID, runner.JobRunning)
			}
			require.NoError(t, f.rec.Recover(context.Background()))
			assert.Empty(t, f.grace.until, "restart grace protects retained remote jobs, not runners added after a fresh boot")
		})
	}
}
