package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
)

// Requirements returns the job's complete persisted ExecRequirements.
func (r *RunnerLedgerRepo) Requirements(ctx context.Context, jobID uuid.UUID) (runner.ExecRequirements, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT requirements FROM runner_jobs WHERE id = ?`, uuidToString(jobID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return runner.ExecRequirements{}, fmt.Errorf("%w: %s", runner.ErrJobNotFound, jobID)
	}
	if err != nil {
		return runner.ExecRequirements{}, fmt.Errorf("reading job requirements: %w", err)
	}
	var req runner.ExecRequirements
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		return runner.ExecRequirements{}, fmt.Errorf("decoding job requirements: %w", err)
	}
	return req, nil
}
