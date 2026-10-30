package main

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Public spawn entry points must refuse the internal mode before persistence:
// a complete link has no roster and cannot finish; a partial link can silently
// become an unlinked root. Removing public-mode rejection breaks this test.
func TestSpawnE2E_PublicEntryPointsRefuseGroupTaskBeforePersistence(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{"Spawn", "Trigger", "TriggerWithDepartment"} {
		for _, complete := range []bool{true, false} {
			name := entry + "/complete"
			if !complete {
				name = entry + "/partial"
			}
			t.Run(name, func(t *testing.T) {
				ds := bootSpawnStack(t)
				ctx := adminCtxForLocalUser(t, ds)
				parentID := uuid.New()
				nodeID := "spawn-1"
				require.NoError(t, ds.instanceRepo.Create(ctx, &instance.Instance{
					ModelPins:        map[string]string{},
					ModelPinsVersion: instance.ModelPinsVersionSnapshot,
					ID:               parentID, OwnerID: localDefaultUser, WorkflowName: "spawn-parent",
					State: instance.StateWaiting, Priority: instance.PriorityNormal,
					CurrentNodeID: &nodeID,
				}))
				link := instance.SpawnLink{
					ParentInstanceID: parentID, ParentNodeID: nodeID,
					Mode:       instance.SpawnModeGroupTask,
					GroupRunID: uuid.New(), GroupTaskKey: "unrostered", GroupTaskKind: instance.GroupTaskKindWorkflow,
				}
				if !complete {
					link.GroupRunID = uuid.Nil
				}

				var id uuid.UUID
				var started bool
				var err error
				switch entry {
				case "Spawn":
					id, err = ds.eng.Spawn(ctx, engine.SpawnRequest{
						ChildWorkflow: "spawn-child", Priority: "normal", Link: link,
					})
				case "Trigger":
					id, started, err = ds.eng.Trigger(instance.WithSpawnLink(ctx, link), "spawn-child", nil, "normal")
				case "TriggerWithDepartment":
					id, started, err = ds.eng.TriggerWithDepartment(instance.WithSpawnLink(ctx, link), "spawn-child", nil, "normal", "")
				}
				assert.Error(t, err, "group_task is internal to roster-backed starts")
				assert.Equal(t, uuid.Nil, id)
				assert.False(t, started)

				// Inspect every row, including roots: a partial link must not be
				// accepted by dropping its parent and creating an independent run.
				rows, readErr := ds.instanceRepo.List(context.Background(), instance.ListFilters{Unconstrained: true})
				require.NoError(t, readErr)
				require.Len(t, rows, 1, "rejected spawn must leave only the original parent")
				assert.Equal(t, parentID, rows[0].ID)
				assert.Equal(t, instance.StateWaiting, rows[0].State)
			})
		}
	}
}
