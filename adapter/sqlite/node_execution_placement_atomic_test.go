package sqlite

import (
	"context"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNodeExecutionRepo_PlacementWritesAtomic(t *testing.T) {
	for _, operation := range []string{"create", "update", "session"} {
		t.Run(operation, func(t *testing.T) {
			db := setupTestDB(t)
			owner := seedUser(t, db, "owner-"+uuid.NewString()[:8])
			id := seedInstance(t, db, owner, "running")
			repo := NewNodeExecutionRepo(db.DB)
			ctx := context.Background()
			e := placedExec(id, "B", 1)
			if operation != "create" {
				require.NoError(t, repo.Create(ctx, e))
			}
			_, err := db.DB.Exec(`CREATE TRIGGER reject_placement BEFORE UPDATE OF placement_history ON node_executions BEGIN SELECT RAISE(ABORT, 'placement rejected'); END`)
			require.NoError(t, err)
			e.Status = instance.NodeStatusSkipped
			e.LLMConversation = map[string]any{"messages": "must not commit"}
			switch operation {
			case "create":
				err = repo.Create(ctx, e)
			case "update":
				err = repo.Update(ctx, e)
			case "session":
				_, err = repo.UpdateIfSessionOpen(ctx, e)
			}
			require.ErrorContains(t, err, "placement rejected")
			var count int
			require.NoError(t, db.DB.QueryRow(`SELECT COUNT(*) FROM node_executions WHERE status = ? OR llm_conversation IS NOT NULL`, string(instance.NodeStatusSkipped)).Scan(&count))
			require.Zero(t, count, "no partial trace or session mutation")
		})
	}
}
