package engine

import (
	"errors"

	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/instance"
)

// ErrInvalidDepartmentOverride is returned when a per-trigger department
// override names a department the triggering caller does not belong to. It is a
// validation error (maps to HTTP 400 at the adapter seam), NOT a forbidden/authz
// failure — the caller is asking for an impossible scope, not a privileged one.
var ErrInvalidDepartmentOverride = errors.New("engine: department override must be one of the caller's departments")

// stamp is the (visibility, department) pair the engine writes onto a new
// instance at trigger time.
type stamp struct {
	Visibility instance.Visibility
	Department string
}

// resolveStamp computes the instance visibility+department stamp for a trigger.
//
//   - pubVisibility/pubDepartment: the resolved workflow publication's audience
//     (private => VisibilityPrivate with empty department).
//   - override: the optional per-trigger department from the request ("" when
//     absent). When non-empty it MUST be one of the caller's effective
//     departments, else ErrInvalidDepartmentOverride.
//
// Stamping rules (spec §Instance Visibility, design §Trigger Stamping):
//   - public:     Visibility=public; Department = override, else the triggerer's
//                 first effective department (dashboard attribution), else "".
//   - department: Visibility=department; Department = override, else the
//                 publication's department.
//   - private:    Visibility=private; Department = "" (override ignored for the
//                 audience but still validated above).
//
// The override never widens beyond the caller's own departments.
func resolveStamp(id identity.Identity, pubVisibility instance.Visibility, pubDepartment, override string) (stamp, error) {
	if override != "" && !id.InDepartment(override) {
		return stamp{}, ErrInvalidDepartmentOverride
	}

	switch pubVisibility {
	case instance.VisibilityPublic:
		dept := override
		if dept == "" {
			dept = firstDepartment(id)
		}
		return stamp{Visibility: instance.VisibilityPublic, Department: dept}, nil
	case instance.VisibilityDepartment:
		dept := override
		if dept == "" {
			dept = pubDepartment
		}
		return stamp{Visibility: instance.VisibilityDepartment, Department: dept}, nil
	default: // private (or unknown -> fail closed to private/owner-only)
		return stamp{Visibility: instance.VisibilityPrivate, Department: ""}, nil
	}
}

// firstDepartment returns the caller's first effective department, or "" when
// they belong to none (claimless callers -> default group is supplied by Phase 1,
// so this is "" only for an identity with no Departments slice).
func firstDepartment(id identity.Identity) string {
	if len(id.Departments) == 0 {
		return ""
	}
	return id.Departments[0]
}
