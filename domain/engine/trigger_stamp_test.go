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

// PR #21 (public-run attribution drift): a public workflow ALWAYS stamps an
// EMPTY department, regardless of the triggerer's departments. The old behavior
// read Departments[0] (JWT-claim-first-seen order), which drifted across logins
// and silently corrupted persisted attribution; a public run's audience is
// everyone, so it belongs to no single department.
func TestResolveStamp_Public_NoOverride_EmptyDepartment(t *testing.T) {
	id := idWithDepts("engineering", "platform")
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "")
	require.NoError(t, err)
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "", got.Department, "a public run attributes to no department (cross-department audience)")
}

// A public workflow with no triggerer departments also stamps an empty department.
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

// A valid override on a public workflow is still ACCEPTED (the scope-widening
// guard passes for a member's own department) but, per PR #21, it does NOT
// attribute the public run to that department — a public run carries no
// department. This pins that the override cannot smuggle a public run back into a
// single department's dashboard bucket.
func TestResolveStamp_Override_Public_AcceptedButNoAttribution(t *testing.T) {
	id := idWithDepts("engineering")
	got, err := resolveStamp(id, instance.VisibilityPublic, "", "engineering")
	require.NoError(t, err, "a valid override on a public workflow still succeeds")
	assert.Equal(t, instance.VisibilityPublic, got.Visibility)
	assert.Equal(t, "", got.Department, "a public run carries no department even with a valid override")
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
