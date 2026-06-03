# Phase 2 — Task 1: Pure Adaptive Card Payload Builder

**Goal:** Implement `buildTeamsPayload(n, baseURL)`, a pure function producing the Teams Workflows Adaptive Card JSON envelope with title, FactSet, and an optional deep-link action.

**Architecture:** Hexagonal — lives in `adapter/notification/teams_payload.go`. Pure: no I/O, no clock, no network — it reads only the passed `*notification.Notification` and `baseURL`, so it is trivially unit-testable. The notifier (Task 2) calls it before POSTing. When `baseURL` is empty the `Action.OpenUrl` is omitted but the card stays valid.

**Tech Stack:** Go stdlib `encoding/json`, `time`. White-box tests (`package notification`) with `testify`.

---

## Context

- New file: `adapter/notification/teams_payload.go` (package `notification`)
- New test: `adapter/notification/teams_payload_test.go` — **white-box** `package notification` (the builder is unexported; the existing `log_notifier_test.go` is black-box `notification_test`, so this new white-box file coexists in the same directory).
- `domain/notification.Notification` fields: `ID uuid.UUID`, `InstanceID uuid.UUID`, `WorkflowName string`, `Type Type` (string), `CreatedAt time.Time`.
- Deep link shape (from design): `<baseURL>/instances/<instanceID>`.

## Steps

### 1. Write the failing test

- [x] Create `adapter/notification/teams_payload_test.go`:

```go
package notification

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/notification"
)

func testNotification() *notification.Notification {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	return &notification.Notification{
		ID:           uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		InstanceID:   id,
		WorkflowName: "my-workflow",
		Type:         notification.TypeFailed,
		CreatedAt:    time.Date(2026, 6, 2, 15, 4, 5, 0, time.UTC),
	}
}

// Happy-path: a well-formed Adaptive Card with title, facts, and deep link.
func TestBuildTeamsPayload_WellFormed(t *testing.T) {
	raw, err := buildTeamsPayload(testNotification(), "http://localhost:8080")
	require.NoError(t, err)
	require.True(t, json.Valid(raw), "payload must be valid JSON")

	var msg struct {
		Type        string `json:"type"`
		Attachments []struct {
			ContentType string `json:"contentType"`
			Content     struct {
				Type string `json:"type"`
				Body []struct {
					Type  string `json:"type"`
					Text  string `json:"text"`
					Facts []struct {
						Title string `json:"title"`
						Value string `json:"value"`
					} `json:"facts"`
				} `json:"body"`
				Actions []struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"actions"`
			} `json:"content"`
		} `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(raw, &msg))

	assert.Equal(t, "message", msg.Type)
	require.Len(t, msg.Attachments, 1)
	att := msg.Attachments[0]
	assert.Equal(t, "application/vnd.microsoft.card.adaptive", att.ContentType)
	assert.Equal(t, "AdaptiveCard", att.Content.Type)

	// Title TextBlock mentions workflow + type.
	require.GreaterOrEqual(t, len(att.Content.Body), 2)
	assert.Contains(t, att.Content.Body[0].Text, "my-workflow")
	assert.Contains(t, att.Content.Body[0].Text, "failed")

	// FactSet carries the instance ID and the timestamp.
	facts := att.Content.Body[1].Facts
	factMap := map[string]string{}
	for _, f := range facts {
		factMap[f.Title] = f.Value
	}
	assert.Equal(t, "my-workflow", factMap["Workflow"])
	assert.Equal(t, "failed", factMap["Event"])
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", factMap["Instance"])
	assert.Equal(t, "2026-06-02T15:04:05Z", factMap["Time"])

	// Deep-link action present and correct.
	require.Len(t, att.Content.Actions, 1)
	assert.Equal(t, "Action.OpenUrl", att.Content.Actions[0].Type)
	assert.Equal(t, "http://localhost:8080/instances/11111111-1111-1111-1111-111111111111", att.Content.Actions[0].URL)
}

// Edge-case: empty baseURL omits the OpenUrl action but the card stays valid.
func TestBuildTeamsPayload_NoBaseURL_OmitsAction(t *testing.T) {
	raw, err := buildTeamsPayload(testNotification(), "")
	require.NoError(t, err)
	require.True(t, json.Valid(raw))

	var msg map[string]any
	require.NoError(t, json.Unmarshal(raw, &msg))

	attachments := msg["attachments"].([]any)
	content := attachments[0].(map[string]any)["content"].(map[string]any)
	_, hasActions := content["actions"]
	assert.False(t, hasActions, "actions must be omitted when baseURL is empty")
}
```

### 2. Run — confirm it FAILS (does not compile)

- [x] Run:

```bash
go test ./adapter/notification/ -run TestBuildTeamsPayload
```

- [x] Expected: build failure, `undefined: buildTeamsPayload`.

### 3. Implement the builder

- [x] Create `adapter/notification/teams_payload.go`:

```go
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
```

### 4. Run — confirm it PASSES

- [x] Run:

```bash
go test ./adapter/notification/ -run TestBuildTeamsPayload
```

- [x] Expected: `ok`.

### 5. Run the full notification package (no regressions)

- [x] Run:

```bash
go test ./adapter/notification/
```

- [x] Expected: `ok` (existing `TestLogNotifier_*` still pass).

### 6. Commit

- [x] Run:

```bash
git add adapter/notification/teams_payload.go adapter/notification/teams_payload_test.go
git commit -m "feat(notification): pure Teams Adaptive Card payload builder"
```
