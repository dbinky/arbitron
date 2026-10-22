package engine_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Characterize the actual trigger -> persisted instance boundary. Rendering is
// still pending: only saved metadata, not a completed layout row, is available.
func TestEnginePendingPresentationSnapshot(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "stamped"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			f := newTestFixtures()
			// Keep the instance queued so snapshot assertions do not race execution.
			f.queueManager.mu.Lock()
			f.queueManager.enqueueFn = func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, nil }
			f.queueManager.mu.Unlock()
			wf := linearWorkflow()
			wf.ID = uuid.New()
			wf.Nodes[0].Position = &workflow.NodePosition{X: 123, Y: 456}
			if !legacy {
				wf.Presentation = &workflow.Presentation{Revision: uuid.Must(uuid.NewV7()).String(), SavedAt: time.Date(2026, 10, 16, 1, 2, 3, 4, time.UTC), Reason: workflow.SaveReasonUpdate, Scope: workflow.ScopeLabelPrivate, OwnerID: uuid.New(), Name: wf.Name, Retained: workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: uuid.Must(uuid.NewV7()).String(), FromGeneration: 3, Positions: map[string]workflow.NodePosition{"A": {X: 23, Y: 45}, "B": {X: 67, Y: 89}}}}
			}
			// This is the same serialization boundary as a committed repository manifest.
			saved, err := json.Marshal(wf)
			require.NoError(t, err)
			var loaded workflow.Workflow
			require.NoError(t, json.Unmarshal(saved, &loaded))
			f.workflowRepo.add(&loaded)
			id, _, err := f.engine.Trigger(context.Background(), wf.Name, nil, "normal")
			require.NoError(t, err)
			inst, err := f.instanceRepo.Get(context.Background(), id)
			require.NoError(t, err)
			frozen, err := json.Marshal(inst.WorkflowSnapshot)
			require.NoError(t, err)
			assert.JSONEq(t, string(saved), string(frozen), "every saved field reaches the instance sink")
			if legacy {
				assert.NotContains(t, inst.WorkflowSnapshot, "presentation")
			} else {
				p, ok := inst.WorkflowSnapshot["presentation"].(map[string]any)
				require.True(t, ok, "the saved metadata must use its wire key")
				assert.Equal(t, wf.SavedRevision(), p["revision"])
				assert.Equal(t, "update", p["reason"])
				assert.Equal(t, wf.Presentation.OwnerID.String(), p["ownerId"])
			}
			loaded.Nodes[0].Label = "changed after trigger"
			loaded.Nodes[0].Position.X = 9999
			if loaded.Presentation != nil {
				loaded.Presentation.Revision = "new-save"
				loaded.Presentation.Retained.Positions["A"] = workflow.NodePosition{X: 9999}
			}
			again, err := f.instanceRepo.Get(context.Background(), id)
			require.NoError(t, err)
			final, err := json.Marshal(again.WorkflowSnapshot)
			require.NoError(t, err)
			assert.JSONEq(t, string(saved), string(final), "later saves cannot mutate the frozen presentation or graph")
		})
	}
}
