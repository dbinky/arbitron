package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// idWith builds an identity carrying departments and (optionally) admin role.
func idWith(user uuid.UUID, admin bool, depts ...string) identity.Identity {
	role := identity.RoleUser
	if admin {
		role = identity.RoleAdmin
	}
	return identity.Identity{UserID: user, Username: "u", Role: role, Departments: depts}
}

func TestCanSeePublication(t *testing.T) {
	publisher := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	viewer := uuid.MustParse("00000000-0000-0000-0000-0000000000a2")

	pubPublic := workflow.Publication{Publisher: publisher, Visibility: workflow.VisibilityPublic}
	pubDeptEng := workflow.Publication{Publisher: publisher, Visibility: workflow.VisibilityDepartment, Department: "engineering"}

	t.Run("admin sees everything", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(viewer, true), pubDeptEng))
	})
	t.Run("publisher sees own department workflow", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(publisher, false), pubDeptEng))
	})
	t.Run("public visible to any member", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(viewer, false), pubPublic))
	})
	t.Run("department visible to a member of that department", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(viewer, false, "engineering"), pubDeptEng))
	})
	t.Run("department hidden from a non-member outsider", func(t *testing.T) {
		assert.False(t, workflow.CanSeePublication(idWith(viewer, false, "sales"), pubDeptEng))
	})
	t.Run("department hidden from a deptless outsider", func(t *testing.T) {
		assert.False(t, workflow.CanSeePublication(idWith(viewer, false), pubDeptEng))
	})
}
