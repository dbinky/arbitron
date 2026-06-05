package engine

import (
	"testing"

	"github.com/arbitron/arbitron/domain/audit"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// PR #21: the engine's audit action names must be the SAME single-source values
// declared in domain/audit, not independent string literals — otherwise a rename
// in one place silently partitions the persisted audit log.
func TestAuditActionConstants_SingleSourceFromAuditPackage(t *testing.T) {
	assert.Equal(t, audit.ActionAdminOverride, AuditActionAdminOverride)
	assert.Equal(t, audit.ActionLeadIntervention, AuditActionLeadIntervention)
}

func mkInst2(owner uuid.UUID, dept string) *instance.Instance {
	return &instance.Instance{ID: uuid.New(), OwnerID: owner, Visibility: instance.VisibilityDepartment, Department: dept}
}

// Owner self-action => no audit action (empty string => caller skips emission).
func TestAuditAction_OwnerSelf_None(t *testing.T) {
	o := uuid.New()
	id := identity.Identity{UserID: o, Role: identity.RoleUser, Departments: []string{"engineering"}}
	assert.Equal(t, "", auditActionFor(id, mkInst2(o, "engineering")))
}

// A lead of the run's department intervening on a non-owned run => lead_intervention.
func TestAuditAction_Lead_LeadIntervention(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleUser, Departments: []string{"engineering"}, LedDepartments: []string{"engineering"}}
	assert.Equal(t, AuditActionLeadIntervention, auditActionFor(id, mkInst2(uuid.New(), "engineering")))
}

// An admin acting on a non-owned run => admin.override.
func TestAuditAction_Admin_Override(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin}
	assert.Equal(t, AuditActionAdminOverride, auditActionFor(id, mkInst2(uuid.New(), "engineering")))
}

// An admin who also owns the run is a self-action => no audit.
func TestAuditAction_AdminOwner_None(t *testing.T) {
	o := uuid.New()
	id := identity.Identity{UserID: o, Role: identity.RoleAdmin}
	assert.Equal(t, "", auditActionFor(id, mkInst2(o, "engineering")))
}

// Admin takes precedence over lead when both apply (admin overriding their own
// led department is still classified admin.override — admin is the broader power).
func TestAuditAction_AdminAndLead_AdminWins(t *testing.T) {
	id := identity.Identity{UserID: uuid.New(), Role: identity.RoleAdmin, LedDepartments: []string{"engineering"}}
	assert.Equal(t, AuditActionAdminOverride, auditActionFor(id, mkInst2(uuid.New(), "engineering")))
}
