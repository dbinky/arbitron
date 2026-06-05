package instance_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/instance"
	"github.com/stretchr/testify/assert"
)

func TestVisibilityConstants(t *testing.T) {
	assert.Equal(t, instance.Visibility("private"), instance.VisibilityPrivate)
	assert.Equal(t, instance.Visibility("department"), instance.VisibilityDepartment)
	assert.Equal(t, instance.Visibility("public"), instance.VisibilityPublic)
}

func TestInstance_HasDepartmentAndVisibilityFields(t *testing.T) {
	inst := instance.Instance{
		Department: "engineering",
		Visibility: instance.VisibilityDepartment,
	}
	assert.Equal(t, "engineering", inst.Department)
	assert.Equal(t, instance.VisibilityDepartment, inst.Visibility)
}

// Zero value is private with empty department — matching the owner-only default
// of pre-existing rows.
func TestInstance_ZeroValueIsPrivate(t *testing.T) {
	var inst instance.Instance
	assert.Equal(t, "", inst.Department)
	assert.Equal(t, instance.Visibility(""), inst.Visibility)
}
