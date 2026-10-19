package engine

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/arbitron/arbitron/domain/instance"
)

// The ancestor narrowing is caller-supplied and must survive the service-set
// visibility overwrite for every caller class.
func TestApplyListConstraint_PreservesActiveDescendantAncestors(t *testing.T) {
	uid := uuid.New()
	for name, lc := range map[string]listFilter{
		"admin":     {Unconstrained: true},
		"non-admin": {OwnerID: &uid, Departments: []string{"eng"}, IncludePublic: true},
	} {
		t.Run(name, func(t *testing.T) {
			got := applyListConstraint(instance.ListFilters{ActiveDescendantAncestors: true}, lc)
			assert.True(t, got.ActiveDescendantAncestors)
		})
	}
}
