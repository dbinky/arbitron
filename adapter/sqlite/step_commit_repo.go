package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// StepCommitRepo implements engine.StepCommitter (RR-10) and
// engine.RetryProgressStore (contracts §24) over the server's one database:
// the SAME *sql.DB used by runner jobs, the checkpoint catalog, resource
// leases, instances, node executions and invocations. Every check and write of
// one commit uses one *sql.Tx; never call another repo through the *sql.DB
// while that transaction holds the single connection.
type StepCommitRepo struct {
	db  *sql.DB
	now func() time.Time
}

// NewStepCommitRepo creates a StepCommitRepo over db.
func NewStepCommitRepo(db *sql.DB) *StepCommitRepo {
	return &StepCommitRepo{db: db, now: time.Now}
}

// loadNodeExecution reads one execution through the P1 T14 reader (placement
// columns included). Call it only outside a transaction.
func loadNodeExecution(ctx context.Context, db *sql.DB, runID, execID uuid.UUID) (*instance.NodeExecution, error) {
	execs, err := NewNodeExecutionRepo(db).ListByInstance(ctx, runID)
	if err != nil {
		return nil, err
	}
	for i := range execs {
		if execs[i].ID == execID {
			x := execs[i]
			return &x, nil
		}
	}
	return nil, fmt.Errorf("node execution %s of run %s not found", execID, runID)
}
