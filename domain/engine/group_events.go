package engine

import (
	"strings"
	"time"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
)

// resolutionEvents carries provenance even before the session row exists and
// after the parent has left the group node. Each Publish copies the payload;
// concurrent subscribers must never see each other's authorization edits.
type resolutionEvents struct {
	EventBus
	runID  string
	nodeID string
}

func (b resolutionEvents) Publish(id uuid.UUID, evt Event) {
	if strings.HasPrefix(evt.Type, "interactive.") {
		data := map[string]any{}
		if original, ok := evt.Data.(map[string]any); ok {
			for k, v := range original {
				data[k] = v
			}
		}
		data["sessionKind"] = ConversationKindGroupResolution
		data["groupRunId"] = b.runID
		data["nodeId"] = b.nodeID
		evt.Data = data
	}
	b.EventBus.Publish(id, evt)
}

func conversationEvents(bus EventBus, conversation map[string]any, nodeID string) EventBus {
	if !isGroupResolutionConversation(conversation) {
		return bus
	}
	runID, _ := conversation[ConversationKeyGroupRunID].(string)
	return resolutionEvents{EventBus: bus, runID: runID, nodeID: nodeID}
}

// Parallel-group lifecycle events. They are published on the PARENT instance
// id (the run whose graph contains the group node), so a viewer of the parent
// sees its groups move. Data always carries "groupRunId" and "nodeId"; task
// events add "taskKey" and "status".
const (
	EventTypeGroupLaunched    = "group.launched"
	EventTypeGroupTaskUpdated = "group.task_updated"
	EventTypeGroupDecided     = "group.decided"
	EventTypeGroupGathering   = "group.gathering" // Phase 3
	EventTypeGroupConflict    = "group.conflict"  // Phase 3
	EventTypeGroupResolved    = "group.resolved"  // Phase 3
	EventTypeGroupCompleted   = "group.completed"
)

// publishGroupEvent is the ONE place a group event is shaped. A nil bus or run
// publishes nothing.
func publishGroupEvent(bus EventBus, run *instance.GroupRun, eventType string, extra map[string]any, at time.Time) {
	if bus == nil || run == nil {
		return
	}
	data := map[string]any{"groupRunId": run.ID.String(), "nodeId": run.NodeID}
	for k, v := range extra {
		data[k] = v
	}
	bus.Publish(run.ParentInstanceID, Event{Type: eventType, Timestamp: at, Data: data})
}

// decisionEventData is the payload of group.decided.
func decisionEventData(d *instance.GroupDecision) map[string]any {
	if d == nil {
		return map[string]any{}
	}
	return map[string]any{"outcome": d.Outcome, "winner": d.WinnerKey, "reason": d.Reason}
}
