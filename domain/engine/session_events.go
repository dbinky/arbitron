package engine

import "time"

const (
	EventTypeInteractiveTurnRestarted = "interactive.turn_restarted"
	EventTypeInteractiveContextNotice = "interactive.context_notice"
)

// PublishTurnRestarted marks a replay of the accepted turn. Incomplete
// identities are rejected so clients can always filter superseded frames.
func PublishTurnRestarted(bus EventBus, turn InteractiveTurnIdentity) {
	publishSessionEvent(bus, turn, EventTypeInteractiveTurnRestarted, nil)
}

// PublishContextNotice publishes the visible loss notice for an accepted turn.
func PublishContextNotice(bus EventBus, turn InteractiveTurnIdentity, notice string) {
	publishSessionEvent(bus, turn, EventTypeInteractiveContextNotice, map[string]any{
		"notice": notice, "lossy": true,
	})
}

func publishSessionEvent(bus EventBus, turn InteractiveTurnIdentity, kind string, extra map[string]any) {
	if turn.Validate() != nil {
		return
	}
	data := map[string]any{
		"instanceId": turn.RunID.String(), "nodeId": turn.NodeID,
		"invocationId": turn.InvocationID.String(), "requestId": turn.RequestID.String(),
		"turnIndex": turn.TurnIndex, "generation": turn.Generation,
	}
	for key, value := range extra {
		data[key] = value
	}
	bus.Publish(turn.RunID, Event{Type: kind, Timestamp: time.Now(), Data: data})
}
