package notification

import (
	"encoding/json"
	"time"

	"github.com/arbitron/arbitron/domain/notification"
)

// teamsMessage is the top-level Teams Workflows webhook envelope.
type teamsMessage struct {
	Type        string            `json:"type"`
	Attachments []teamsAttachment `json:"attachments"`
}

type teamsAttachment struct {
	ContentType string       `json:"contentType"`
	Content     adaptiveCard `json:"content"`
}

type adaptiveCard struct {
	Schema  string        `json:"$schema"`
	Type    string        `json:"type"`
	Version string        `json:"version"`
	Body    []cardElement `json:"body"`
	Actions []cardAction  `json:"actions,omitempty"`
}

type cardElement struct {
	Type   string     `json:"type"`
	Text   string     `json:"text,omitempty"`
	Size   string     `json:"size,omitempty"`
	Weight string     `json:"weight,omitempty"`
	Facts  []cardFact `json:"facts,omitempty"`
}

type cardFact struct {
	Title string `json:"title"`
	Value string `json:"value"`
}

type cardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// buildTeamsPayload renders a notification as a Teams Workflows Adaptive Card
// envelope. Pure: no I/O, no clock, no network. When baseURL is empty the
// Action.OpenUrl deep link is omitted and the card remains valid.
func buildTeamsPayload(n *notification.Notification, baseURL string) ([]byte, error) {
	title := "Workflow " + n.WorkflowName + ": " + string(n.Type)
	if n.Type == notification.TypeProfileRefusesTriggers {
		// WorkflowName carries "profile <alias>"; the reason is in the boot log.
		title = n.WorkflowName + " will refuse triggers: set a fixed model version (reason in the server log)"
	}

	card := adaptiveCard{
		Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
		Type:    "AdaptiveCard",
		Version: "1.4",
		Body: []cardElement{
			{Type: "TextBlock", Size: "Medium", Weight: "Bolder", Text: title},
			{Type: "FactSet", Facts: []cardFact{
				{Title: "Event", Value: string(n.Type)},
				{Title: "Workflow", Value: n.WorkflowName},
				{Title: "Instance", Value: n.InstanceID.String()},
				{Title: "Time", Value: n.CreatedAt.Format(time.RFC3339)},
			}},
		},
	}

	if baseURL != "" {
		card.Actions = []cardAction{{
			Type:  "Action.OpenUrl",
			Title: "Open in Arbitron",
			URL:   baseURL + "/instances/" + n.InstanceID.String(),
		}}
	}

	msg := teamsMessage{
		Type: "message",
		Attachments: []teamsAttachment{{
			ContentType: "application/vnd.microsoft.card.adaptive",
			Content:     card,
		}},
	}

	return json.Marshal(msg)
}
