package workflow

import "sort"

// ChildInputPlan classifies a child workflow's declared inputs against the
// keys a spawn node maps explicitly.
type ChildInputPlan struct {
	// Unknown lists mapped keys the child does not declare, sorted.
	Unknown []string
	// Mapped lists declared keys the spawn node maps, sorted.
	Mapped []string
	// Defaults holds unmapped required inputs that carry a non-nil default
	// (deep copies, safe to mutate).
	Defaults map[string]any
	// Prepare lists unmapped required inputs with no default, in author order.
	Prepare []InputField
}

// PlanChildInputs classifies fields against the mapped keys. The reserved
// "workspace" key is skipped entirely; ParseSpawnConfig owns that error.
func PlanChildInputs(fields []InputField, mapped map[string]any) ChildInputPlan {
	plan := ChildInputPlan{Defaults: map[string]any{}}

	declared := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		declared[f.Name] = struct{}{}
	}

	for key := range mapped {
		if key == "workspace" {
			continue
		}
		if _, ok := declared[key]; ok {
			plan.Mapped = append(plan.Mapped, key)
		} else {
			plan.Unknown = append(plan.Unknown, key)
		}
	}
	sort.Strings(plan.Mapped)
	sort.Strings(plan.Unknown)

	for _, f := range fields {
		if _, isMapped := mapped[f.Name]; isMapped || !f.Required {
			continue
		}
		if f.Default != nil {
			plan.Defaults[f.Name] = cloneGroupValue(f.Default)
		} else {
			plan.Prepare = append(plan.Prepare, f)
		}
	}
	return plan
}
