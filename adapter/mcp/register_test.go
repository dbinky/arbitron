package mcp_test

import (
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expectedTools is the complete API surface per the Phase 5 design doc.
// Any rename, addition, or removal must update both this list and the code.
var expectedTools = []string{
	// Workflow management (10)
	"workflow_list",
	"workflow_get",
	"workflow_create",
	"workflow_update",
	"workflow_delete",
	"workflow_validate",
	"workflow_list_files",
	"workflow_read_file",
	"workflow_write_file",
	"workflow_delete_file",
	// Instance operations (9)
	"instance_list",
	"instance_get",
	"instance_trigger",
	"instance_pause",
	"instance_resume",
	"instance_cancel",
	"instance_retry",
	"instance_rerun",
	"instance_set_priority",
	// Execution trace & variables (4)
	"trace_get",
	"trace_get_node",
	"variables_get",
	"variables_history",
	// System, queue & notifications (8)
	"system_config",
	"system_update_queue_config",
	"system_update_teams_config",
	"notification_list",
	"notification_unread_count",
	"notification_mark_read",
	"notification_dismiss",
	"workflow_stats",
	// Event streaming (4)
	"instance_subscribe",
	"instance_unsubscribe",
	"system_subscribe",
	"system_unsubscribe",
	// Interactive session (3)
	"interactive_send_message",
	"interactive_end_session",
	"interactive_get_history",
	// Connection management (5)
	"connection_create",
	"connection_update",
	"connection_delete",
	"connection_get",
	"connection_list",
	// Personal API tokens (3)
	"token_create",
	"token_list",
	"token_revoke",
	// User/role management (2)
	"user_list",
	"user_set_role",
}

func TestRegisterAllTools_Count(t *testing.T) {
	srv := adaptmcp.NewServer("test", "0.0.0")
	adaptmcp.RegisterAllTools(srv)
	assert.Len(t, srv.ListTools(), len(expectedTools),
		"RegisterAllTools must register exactly %d tools", len(expectedTools))
}

func TestRegisterAllTools_NamesAreUnique(t *testing.T) {
	srv := adaptmcp.NewServer("test", "0.0.0")
	adaptmcp.RegisterAllTools(srv)

	seen := make(map[string]bool, len(expectedTools))
	for _, tool := range srv.ListTools() {
		assert.False(t, seen[tool.Name], "duplicate tool name registered: %q", tool.Name)
		seen[tool.Name] = true
	}
}

func TestRegisterAllTools_APISurface(t *testing.T) {
	// Verifies that every tool named in the Phase 5 design doc is registered.
	srv := adaptmcp.NewServer("test", "0.0.0")
	adaptmcp.RegisterAllTools(srv)

	registered := make(map[string]bool, len(expectedTools))
	for _, tool := range srv.ListTools() {
		registered[tool.Name] = true
	}

	for _, name := range expectedTools {
		assert.True(t, registered[name], "expected tool %q not found in RegisterAllTools", name)
	}
}

func TestRegisterAllTools_Idempotent(t *testing.T) {
	// Registering twice on the same server doubles the tool count. Callers
	// must not call RegisterAllTools more than once per server instance.
	// This test documents the behaviour rather than asserting idempotence.
	srv := adaptmcp.NewServer("test", "0.0.0")
	adaptmcp.RegisterAllTools(srv)
	first := len(srv.ListTools())
	adaptmcp.RegisterAllTools(srv)
	second := len(srv.ListTools())
	require.Equal(t, first*2, second,
		"calling RegisterAllTools twice doubles the tool count — callers must avoid this")
}
