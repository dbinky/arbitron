package mcp

import (
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/schedule"
)

// scheduleSentinels is the complete set of exported schedule sentinels that may
// reach a transport. Adding a new exported sentinel to domain/schedule MUST add
// it here AND to scheduleResultError (MCP) / writeScheduleError (REST), or this
// coverage test fails — that is the point: it replaces the brittle classifier.
var scheduleSentinels = []struct {
	name string
	err  error
}{
	{"ErrNotFound", schedule.ErrNotFound},
	{"ErrExists", schedule.ErrExists},
	{"ErrCreatorUnresolved", schedule.ErrCreatorUnresolved},
	{"ErrInvalidCron", schedule.ErrInvalidCron},
	{"ErrInvalidTimezone", schedule.ErrInvalidTimezone},
	{"ErrEmptyWorkflow", schedule.ErrEmptyWorkflow},
	{"ErrForbidden", schedule.ErrForbidden},
	{"ErrNotAuthorizedWorkflow", schedule.ErrNotAuthorizedWorkflow},
	{"ErrDepartmentNotMember", schedule.ErrDepartmentNotMember},
	{"ErrQuotaExceeded", schedule.ErrQuotaExceeded},
	{"ErrIntervalTooFrequent", schedule.ErrIntervalTooFrequent},
}

// defaultMCPPrefix is the message prefix scheduleResultError emits for the
// catch-all (default) category. Every exported sentinel must map to a specific
// category, so none should produce this prefix.
const defaultMCPPrefix = "schedule operation failed"

func resultText(t *testing.T, res *gomcp.CallToolResult) string {
	t.Helper()
	require.NotNil(t, res)
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(gomcp.TextContent)
	require.True(t, ok, "expected TextContent")
	return tc.Text
}

// Every exported schedule sentinel must map to a NON-default MCP category.
func TestScheduleResultError_AllSentinelsNonDefault(t *testing.T) {
	for _, s := range scheduleSentinels {
		t.Run(s.name, func(t *testing.T) {
			text := resultText(t, scheduleResultError(s.err))
			require.False(t, strings.HasPrefix(text, defaultMCPPrefix),
				"sentinel %s fell through to the default MCP category: %q", s.name, text)
		})
	}
}

// ErrRunNowUnavailable maps to a distinct "run-now unavailable" message. It is
// checked separately because it intentionally shares the "schedule operation
// failed" prefix (it IS a service-level failure, not a client error) but must
// still be an explicit, specific mapping — not just the catch-all default.
func TestScheduleResultError_RunNowUnavailable_Explicit(t *testing.T) {
	text := resultText(t, scheduleResultError(schedule.ErrRunNowUnavailable))
	require.Contains(t, text, "run-now unavailable",
		"ErrRunNowUnavailable must carry an explicit 'run-now unavailable' message, not just the catch-all")
}
