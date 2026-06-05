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
