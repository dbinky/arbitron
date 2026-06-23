package audit_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/stretchr/testify/assert"
)

// The audit action vocabulary is a persisted WIRE CONTRACT — forensic/compliance
// queries filter on these exact strings. They must be declared ONCE here (PR #21):
// a future rename in a second declaration would silently partition the audit log
// into two values with no compile error. This test pins the persisted values.
func TestActionVocabulary_WireValues(t *testing.T) {
	assert.Equal(t, "workflow.publish", audit.ActionWorkflowPublish)
	assert.Equal(t, "workflow.visibility_change", audit.ActionWorkflowVisibilityChange)
	assert.Equal(t, "admin.override", audit.ActionAdminOverride)
	assert.Equal(t, "instance.lead_intervention", audit.ActionLeadIntervention)
}

// TestActionVocabulary_LifecycleWireValues pins the persisted string values for
// every lifecycle-management action constant. These are written to the audit log
// and queried by external tools — a silent rename would partition the log.
func TestActionVocabulary_LifecycleWireValues(t *testing.T) {
	assert.Equal(t, "workflow.create", audit.ActionWorkflowCreate)
	assert.Equal(t, "workflow.delete", audit.ActionWorkflowDelete)
	assert.Equal(t, "workflow.unpublish", audit.ActionWorkflowUnpublish)
	assert.Equal(t, "workflow.rename", audit.ActionWorkflowRename)
	assert.Equal(t, "workflow.duplicate", audit.ActionWorkflowDuplicate)
	assert.Equal(t, "workflow.archive", audit.ActionWorkflowArchive)
	assert.Equal(t, "workflow.unarchive", audit.ActionWorkflowUnarchive)
	assert.Equal(t, "workflow.transfer_ownership", audit.ActionWorkflowTransfer)
	assert.Equal(t, "workflow.edit_shared", audit.ActionWorkflowEditShared)
}
