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
//   - public:     Visibility=public; Department = "" ALWAYS. A public run's
//     audience is everyone, so attributing it to any one department
//     is a category error. The previous firstDepartment(id) fallback
//     read JWT-claim-first-seen order — unstable across logins — so
//     the SAME user/same public workflow drifted between departments
//     across runs (PR #21 Must Fix). Public runs are surfaced via the
//     dashboard's explicit cross-department (public) lane, not a
//     department bucket. The override is still validated above (the
//     scope-widening guard) but never attributes a public run.
//   - department: Visibility=department; Department = override, else the
//     publication's department.
//   - private:    Visibility=private; Department = "" (override ignored for the
//     audience but still validated above).
//
// The override never widens beyond the caller's own departments.
func resolveStamp(id identity.Identity, pubVisibility instance.Visibility, pubDepartment, override string) (stamp, error) {
	if override != "" && !id.InDepartment(override) {
		return stamp{}, ErrInvalidDepartmentOverride
	}

	switch pubVisibility {
	case instance.VisibilityPublic:
		// Public runs attribute to NO department (cross-department audience).
		return stamp{Visibility: instance.VisibilityPublic, Department: ""}, nil
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
