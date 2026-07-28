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

// Failure/Error categories — recorded N/A decision (TDD coverage completeness).
//
// buildTeamsPayload is a pure function whose only fallible operation is
// json.Marshal of fixed-shape structs containing nothing but strings and slices
// of strings/structs — no channels, funcs, cyclic pointers, or json.Marshaler
// implementations that could fail. Go's encoder cannot return an error for such
// values, so a "failure" (caller-misuse) or "error" (internal fault) scenario is
// not reachable. Rather than leave that as an unexplained omission, this test
// pins the contract: across inputs that exercise every JSON-escaping path
// (quotes, backslashes, control characters, multibyte unicode, empty fields),
// the function still returns valid JSON and no error. If a future change adds a
// genuinely-fallible field, this test will start failing and force a real
// failure/error scenario to be written.
func TestBuildTeamsPayload_NeverErrors_RecordedNA(t *testing.T) {
	id := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	stressNames := []string{
		``,                     // empty workflow name
		`quote"and\backslash`,  // characters that must be JSON-escaped
		"tab\tand\nnewline",    // control characters
		"emoji-🚀-and-üñïçödé",  // multibyte unicode
		`</script><b>html</b>`, // HTML-significant runes (Go escapes <, >, &)
	}
	for _, name := range stressNames {
		n := &notification.Notification{
			ID:           id,
			InstanceID:   id,
			WorkflowName: name,
			Type:         notification.TypeNeedsInteraction,
			CreatedAt:    time.Date(2026, 6, 2, 15, 4, 5, 0, time.UTC),
		}
		raw, err := buildTeamsPayload(n, "http://localhost:8080")
		require.NoError(t, err, "buildTeamsPayload must never error (workflow=%q)", name)
		assert.True(t, json.Valid(raw), "payload must be valid JSON (workflow=%q)", name)
	}
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
