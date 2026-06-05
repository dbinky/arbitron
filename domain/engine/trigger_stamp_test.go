package engine

import (
	"testing"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func idWithDepts(depts ...string) identity.Identity {
	return identity.Identity{UserID: uuid.New(), Username: "u", Role: identity.RoleUser, Departments: depts}
}

// A public workflow: no override -> visibility public, department = the
// triggerer's stamping department (first effective dept) for dashboard
// attribution.
func TestResolveStamp_Public_NoOverride_UsesTriggererDept(t *testing.T) {
	id := idWithDepts("engineering")
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "engineering", got.Department, "public run stamped with triggerer's dept for attribution")
}

// A public workflow with no triggerer departments stamps an empty department.
func TestResolveStamp_Public_NoDepts_EmptyDepartment(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser}
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "", got.Department)
}

// A department workflow: department comes from the publication.
func TestResolveStamp_Department_UsesPublicationDept(t *testing.T) {
	id := idWithDepts("engineering", "platform")
	got, err := resolveStamp(id, instance.VisibilityDepartment, "platform", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityDepartment, got.Visibility)
	assert.Equal(t, "platform", got.Department)
}

// A private workflow: visibility private, no department, override ignored on the
// audience but still validated when present.
func TestResolveStamp_Private_NoDepartment(t *testing.T) {
	id := idWithDepts("engineering")
	got, err := resolveStamp(id, instance.VisibilityPrivate, "", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPrivate, got.Visibility)
	assert.Equal(t, "", got.Department)
}

// Override within the caller's effective departments is accepted and replaces
// the stamped department for a department-visible run.
func TestResolveStamp_Override_Accepted(t *testing.T) {
	id := idWithDepts("engineering", "platform")
	got, err := resolveStamp(id, instance.VisibilityDepartment, "engineering", "platform")
	require.NoError(t, err)
	assert.Equal(t, "platform", got.Department, "override within caller's depts replaces stamped department")
}

// Override naming a department the caller does NOT belong to is rejected — a
// caller can never widen beyond their own departments.
func TestResolveStamp_Override_OutsideDepartments_Rejected(t *testing.T) {
	id := idWithDepts("engineering")
	_, err := resolveStamp(id, instance.VisibilityPublic, "", "finance")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidDepartmentOverride)
}

// Override is normalized/compared case-insensitively the same way membership is
// (Phase 1 normalizes to lowercase); an exact-case member match is accepted.
func TestResolveStamp_Override_Public_Accepted(t *testing.T) {
	id := idWithDepts("engineering")
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "engineering")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "engineering", got.Department, "override sets the stamped attribution department for a public run")
}

// ---------------------------------------------------------------------------
// FA #15 Pass 2: robustness
// ---------------------------------------------------------------------------

// Override on a private workflow: the override is still validated (caller MUST
// belong to the named department), but since private runs carry no department
// audience the Department field stays empty regardless of a valid override.
// This prevents the override from silently "upgrading" a private run's scope.
func TestResolveStamp_Override_PrivateWorkflow_DeptRemainsEmpty(t *testing.T) {
	id := idWithDepts("engineering")
	// override is valid (caller is in engineering) but the workflow is private.
	got, err := resolveStamp(id, instance.VisibilityPrivate, "", "engineering")
	require.NoError(t, err, "a valid override on a private workflow must still succeed")
	assert.Equal(t, instance.VisibilityPrivate, got.Visibility)
	assert.Equal(t, "", got.Department,
		"private run must never carry a department even with a valid override (audience is owner-only)")
}

// An override that names a department outside the caller's set is rejected on
// a DEPARTMENT-visibility workflow too (not only public). The scope-widening
// guard fires before the stamp switch is reached.
func TestResolveStamp_Override_DeptWorkflow_OutsideDepts_Rejected(t *testing.T) {
	id := idWithDepts("engineering") // no "sales"
	_, err := resolveStamp(id, instance.VisibilityDepartment, "engineering", "sales")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidDepartmentOverride,
		"scope-widening via override must be rejected on department-visibility workflows too")
}
