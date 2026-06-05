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

	// Robustness: admin who is ALSO the publisher must see the workflow (both
	// admin and publisher paths fire). This case is NOT an admin override because
	// the demoted identity (publisher without admin role) would also see it.
	t.Run("admin who is also publisher sees own dept workflow", func(t *testing.T) {
		assert.True(t, workflow.CanSeePublication(idWith(publisher, true), pubDeptEng))
	})
}

// TestCanSeePublication_ZeroPublication verifies that the zero value of
// Publication (no publisher, empty visibility) does not accidentally grant
// access to non-admin users. If PublicationOf returns (zero, false, nil) and a
// caller mistakenly uses the zero value, access must be denied.
func TestCanSeePublication_ZeroPublication(t *testing.T) {
	user := uuid.MustParse("00000000-0000-0000-0000-0000000000a3")
	zeroPub := workflow.Publication{} // zero value: no publisher, no visibility

	assert.False(t, workflow.CanSeePublication(idWith(user, false), zeroPub),
		"zero Publication must not grant access to a non-admin, non-publisher user")
	// An admin always sees it (admin overrides everything).
	assert.True(t, workflow.CanSeePublication(idWith(user, true), zeroPub),
		"admin always sees a publication regardless of visibility")
}
