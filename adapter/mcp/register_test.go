package mcp_test

import (
	"testing"

	adaptmcp "github.com/arbitron/arbitron/adapter/mcp"
	"github.com/arbitron/arbitron/domain/schedule"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fullyWiredServer returns a server whose deps include a non-nil ScheduleService
// so the conditionally-registered schedule_* tools are advertised. The service
// is constructed with nil ports because these tests only assert the registered
// tool SET, never invoke a handler.
func fullyWiredServer(t *testing.T) *adaptmcp.Server {
	t.Helper()
	return adaptmcp.NewServerWithDeps("test", "0.0.0", &adaptmcp.Deps{
		ScheduleService: schedule.NewService(nil, nil),
	})
}

// expectedTools is the complete API surface per the Phase 5 design doc.
// Any rename, addition, or removal must update both this list and the code.
var expectedTools = []string{
	// Workflow management (16)
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
	"workflow_publish",
	"workflow_set_visibility",
	"workflow_copy",
	"workflow_group_upsert",
	"workflow_group_get",
	"workflow_group_delete",
	// Admin-gated lifecycle operations (workflow-management Phase 3 Task 4) (7)
	"workflow_rename",
	"workflow_unpublish",
	"workflow_duplicate",
	"workflow_archive",
	"workflow_unarchive",
	"workflow_transfer_ownership",
	"workflow_audit",
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
	// Execution trace & variables (5)
	"trace_get",
	"trace_get_node",
	"variables_get",
	"variables_history",
	"trace_interventions",
	// Model identity policy (2)
	"model_identity_policy_get",
	"model_identity_policy_update",
	// Model catalog (2)
	"model_catalog_list",
	"model_catalog_refresh",
	// System, queue & notifications (10)
	"system_settings_get",
	"system_settings_update",
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
	// Interactive session (4)
	"interactive_send_message",
	"interactive_end_session",
	"interactive_get_history",
	"interactive_abandon_session",
	// Connection management and discovery (6)
	"connection_create",
	"connection_update",
	"connection_delete",
	"connection_get",
	"connection_list",
	"connection_models",
	// Profile management (6)
	"profile_create",
	"profile_update",
	"profile_delete",
	"profile_get",
	"profile_list",
	"provider_capabilities",
	// Personal API tokens (3)
	"token_create",
	"token_list",
	"token_revoke",
	// User/role management (2)
	"user_list",
	"user_set_role",
	// Layout arrangement retry (2)
	"workflow_layout_retry",
	"instance_layout_retry",
	// Recurring schedules (5)
	"schedule_create",
	"schedule_list",
	"schedule_get",
	"schedule_update",
	"schedule_delete",
	// Runner pool (7).
	"runner_list",
	"runner_get",
	"runner_events",
	"runner_update",
	"runner_cordon",
	"runner_uncordon",
	"runner_source_probe",
}

func TestRegisterAllTools_Count(t *testing.T) {
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)
	assert.Len(t, srv.ListTools(), len(expectedTools),
		"RegisterAllTools must register exactly %d tools", len(expectedTools))
}

func TestRegisterAllTools_NamesAreUnique(t *testing.T) {
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)

	seen := make(map[string]bool, len(expectedTools))
	for _, tool := range srv.ListTools() {
		assert.False(t, seen[tool.Name], "duplicate tool name registered: %q", tool.Name)
		seen[tool.Name] = true
	}
}

func TestRegisterAllTools_APISurface(t *testing.T) {
	// Verifies that every tool named in the Phase 5 design doc is registered.
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)

	registered := make(map[string]bool, len(expectedTools))
	for _, tool := range srv.ListTools() {
		registered[tool.Name] = true
	}

	for _, name := range expectedTools {
		assert.True(t, registered[name], "expected tool %q not found in RegisterAllTools", name)
	}
}

// Conditional registration: when no ScheduleService is wired, the five
// schedule_* tools are NOT advertised (the handlers can therefore assume a
// non-nil service — there is no dead "unavailable" branch).
func TestRegisterScheduleTools_NotRegisteredWhenUnwired(t *testing.T) {
	srv := adaptmcp.NewServer("test", "0.0.0") // nil deps, no ScheduleService
	adaptmcp.RegisterAllTools(srv)
	for _, tool := range srv.ListTools() {
		assert.NotContains(t, tool.Name, "schedule_",
			"schedule tools must not register without a wired ScheduleService")
	}
}

func TestRegisterAllTools_Idempotent(t *testing.T) {
	// Registering twice on the same server doubles the tool count. Callers
	// must not call RegisterAllTools more than once per server instance.
	// This test documents the behaviour rather than asserting idempotence.
	srv := fullyWiredServer(t)
	adaptmcp.RegisterAllTools(srv)
	first := len(srv.ListTools())
	adaptmcp.RegisterAllTools(srv)
	second := len(srv.ListTools())
	require.Equal(t, first*2, second,
		"calling RegisterAllTools twice doubles the tool count — callers must avoid this")
}
