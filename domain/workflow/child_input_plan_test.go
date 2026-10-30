package workflow

import (
	"reflect"
	"testing"
)

func planNames(fs []InputField) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Name)
	}
	return out
}

func TestPlanChildInputs_Classifies(t *testing.T) {
	fields := []InputField{
		{Name: "a", Required: true},
		{Name: "b", Required: true, Default: "main"},
		{Name: "c"},
		{Name: "d", Required: true},
	}
	plan := PlanChildInputs(fields, map[string]any{"a": "{{x}}"})

	if !reflect.DeepEqual(plan.Mapped, []string{"a"}) {
		t.Errorf("Mapped = %v", plan.Mapped)
	}
	if !reflect.DeepEqual(plan.Defaults, map[string]any{"b": "main"}) {
		t.Errorf("Defaults = %v", plan.Defaults)
	}
	if !reflect.DeepEqual(planNames(plan.Prepare), []string{"d"}) {
		t.Errorf("Prepare = %v", planNames(plan.Prepare))
	}
	if len(plan.Unknown) != 0 {
		t.Errorf("Unknown = %v", plan.Unknown)
	}
	if _, ok := plan.Defaults["c"]; ok {
		t.Error("optional c must not appear in Defaults")
	}
}

func TestPlanChildInputs_FullyMapped(t *testing.T) {
	fields := []InputField{{Name: "a", Required: true}, {Name: "b", Required: true, Default: 1}}
	plan := PlanChildInputs(fields, map[string]any{"a": "x", "b": "y"})
	if len(plan.Prepare) != 0 || len(plan.Defaults) != 0 {
		t.Errorf("Prepare=%v Defaults=%v", plan.Prepare, plan.Defaults)
	}
	if !reflect.DeepEqual(plan.Mapped, []string{"a", "b"}) {
		t.Errorf("Mapped = %v", plan.Mapped)
	}
}

func TestPlanChildInputs_NilAndEmptyMapAreEquivalent(t *testing.T) {
	fields := []InputField{{Name: "a", Required: true}, {Name: "b", Required: true, Default: "d"}}
	n := PlanChildInputs(fields, nil)
	e := PlanChildInputs(fields, map[string]any{})
	if !reflect.DeepEqual(n, e) {
		t.Errorf("nil %+v != empty %+v", n, e)
	}
	if n.Defaults == nil {
		t.Error("Defaults must be non-nil")
	}
}

func TestPlanChildInputs_ZeroDeclaredInputsReportsAllMappedAsUnknown(t *testing.T) {
	plan := PlanChildInputs(nil, map[string]any{"y": 1, "x": 2})
	if !reflect.DeepEqual(plan.Unknown, []string{"x", "y"}) {
		t.Errorf("Unknown = %v", plan.Unknown)
	}
	if len(plan.Mapped) != 0 || len(plan.Prepare) != 0 || plan.Defaults == nil || len(plan.Defaults) != 0 {
		t.Errorf("unexpected plan %+v", plan)
	}
}

func TestPlanChildInputs_PrepareKeepsAuthorOrder(t *testing.T) {
	fields := []InputField{
		{Name: "z", Required: true}, {Name: "a", Required: true}, {Name: "m", Required: true},
	}
	plan := PlanChildInputs(fields, nil)
	if !reflect.DeepEqual(planNames(plan.Prepare), []string{"z", "a", "m"}) {
		t.Errorf("Prepare = %v", planNames(plan.Prepare))
	}
}

func TestPlanChildInputs_FalsyDefaultsCountAsDefaults(t *testing.T) {
	fields := []InputField{
		{Name: "f", Required: true, Default: false},
		{Name: "n", Required: true, Default: 0.0},
		{Name: "s", Required: true, Default: ""},
	}
	plan := PlanChildInputs(fields, nil)
	want := map[string]any{"f": false, "n": 0.0, "s": ""}
	if !reflect.DeepEqual(plan.Defaults, want) {
		t.Errorf("Defaults = %v", plan.Defaults)
	}
	if len(plan.Prepare) != 0 {
		t.Errorf("Prepare = %v", planNames(plan.Prepare))
	}
}

func TestPlanChildInputs_NullDefaultIsNoDefault(t *testing.T) {
	plan := PlanChildInputs([]InputField{{Name: "a", Required: true, Default: nil}}, nil)
	if !reflect.DeepEqual(planNames(plan.Prepare), []string{"a"}) {
		t.Errorf("Prepare = %v", planNames(plan.Prepare))
	}
	if len(plan.Defaults) != 0 {
		t.Errorf("Defaults = %v", plan.Defaults)
	}
}

func TestPlanChildInputs_DefaultsAreDeepCopies(t *testing.T) {
	fields := []InputField{{Name: "list", Required: true, Default: []any{"orig"}}}
	plan := PlanChildInputs(fields, nil)
	plan.Defaults["list"].([]any)[0] = "mutated"
	if fields[0].Default.([]any)[0] != "orig" {
		t.Error("field Default was mutated through plan.Defaults")
	}
}

func TestPlanChildInputs_WorkspaceKeyIgnored(t *testing.T) {
	plan := PlanChildInputs([]InputField{{Name: "a"}}, map[string]any{"workspace": "/tmp", "a": "x"})
	for _, s := range append(append([]string{}, plan.Unknown...), plan.Mapped...) {
		if s == "workspace" {
			t.Errorf("workspace leaked into plan: %+v", plan)
		}
	}
	if !reflect.DeepEqual(plan.Mapped, []string{"a"}) {
		t.Errorf("Mapped = %v", plan.Mapped)
	}
}
