package engine_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
)

// PR #21: instance.VisibilityFromWorkflow must be TOTAL over the workflow
// visibility model — every workflow.Visibility value maps to the matching
// instance.Visibility (NOT silently defaulted to private). This is the stronger
// guard the reviewers asked for: if a fourth workflow visibility level is ever
// added, this exhaustive table fails until the mapping is extended, rather than
// the old inline switch silently downgrading it.
func TestVisibilityFromWorkflow_TotalOverWorkflowValues(t *testing.T) {
	cases := []struct {
		in   workflow.Visibility
		want instance.Visibility
	}{
		{workflow.VisibilityPrivate, instance.VisibilityPrivate},
		{workflow.VisibilityDepartment, instance.VisibilityDepartment},
		{workflow.VisibilityPublic, instance.VisibilityPublic},
	}
	for _, tc := range cases {
		t.Run(string(tc.in), func(t *testing.T) {
			got := instance.VisibilityFromWorkflow(string(tc.in))
			assert.Equal(t, tc.want, got)
			assert.True(t, got.Valid(), "mapped instance visibility must be a defined level")
		})
	}
}

// An unrecognized workflow visibility value fails closed to private (owner-only).
func TestVisibilityFromWorkflow_UnknownFailsClosed(t *testing.T) {
	assert.Equal(t, instance.VisibilityPrivate, instance.VisibilityFromWorkflow("bogus"))
	assert.Equal(t, instance.VisibilityPrivate, instance.VisibilityFromWorkflow(""))
}
