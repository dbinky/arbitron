package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
)

func inputs() []workflow.InputField {
	return []workflow.InputField{
		{Name: "repo", Type: "string", Description: "target repository", Required: true},
		{Name: "issueCount", Type: "number", Description: "how many issues", Required: true},
		{Name: "dryRun", Type: "boolean", Description: "skip writes", Required: false},
	}
}

// ---------- childInputSchema ----------

func TestChildInputSchema_ShapeAndRequired(t *testing.T) {
	s := childInputSchema(inputs())

	if s["type"] != "object" {
		t.Fatalf(`schema["type"] = %v, want "object"`, s["type"])
	}
	props, ok := s["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties must be a map, got %T", s["properties"])
	}
	if len(props) != 3 {
		t.Fatalf("expected 3 properties, got %d", len(props))
	}
	repo, ok := props["repo"].(map[string]any)
	if !ok {
		t.Fatalf("properties.repo must be a map, got %T", props["repo"])
	}
	if repo["type"] != "string" {
		t.Errorf("properties.repo.type = %v, want string", repo["type"])
	}
	if repo["description"] != "target repository" {
		t.Errorf("properties.repo.description = %v", repo["description"])
	}

	req, ok := s["required"].([]string)
	if !ok {
		t.Fatalf("schema required must be []string, got %T", s["required"])
	}
	if len(req) != 2 || req[0] != "issueCount" || req[1] != "repo" {
		t.Fatalf("required = %v, want sorted [issueCount repo] (sorted for a stable prompt)", req)
	}
}

func TestChildInputSchema_ZeroInputs(t *testing.T) {
	s := childInputSchema(nil)
	props, _ := s["properties"].(map[string]any)
	if len(props) != 0 {
		t.Fatalf("expected no properties for a child with no declared inputs, got %v", props)
	}
	if _, present := s["required"]; present {
		t.Error("a schema with no required inputs must omit the required key entirely")
	}
}

// An unrecognized or empty declared type is carried as guidance-free rather
// than fabricated: the child's declaration is the authority, and inventing a
// type here would create a second source of truth about what a valid input is.
func TestChildInputSchema_UnknownTypeOmitsTypeKey(t *testing.T) {
	s := childInputSchema([]workflow.InputField{
		{Name: "weird", Type: "", Description: "no declared type", Required: false},
		{Name: "bogus", Type: "quaternion", Required: false},
	})
	props := s["properties"].(map[string]any)
	for _, name := range []string{"weird", "bogus"} {
		p := props[name].(map[string]any)
		if _, present := p["type"]; present {
			t.Errorf("properties.%s must omit type for an unrecognized declared type, got %v", name, p["type"])
		}
	}
}

// ---------- validateChildInputs ----------

func TestValidateChildInputs_AllRequiredPresent(t *testing.T) {
	err := validateChildInputs("child", inputs(), map[string]any{
		"repo":       "arbitron/arbitron",
		"issueCount": float64(3),
	})
	if err != nil {
		t.Fatalf("expected the contract to be satisfied, got %v", err)
	}
}

func TestValidateChildInputs_AbsentOptionalIsFine(t *testing.T) {
	// An optional input absent from the response is left ABSENT rather than
	// filled with a zero value, so the child's own trigger-time defaulting
	// applies.
	produced := map[string]any{"repo": "r", "issueCount": float64(1)}
	if err := validateChildInputs("child", inputs(), produced); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if _, present := produced["dryRun"]; present {
		t.Error("validateChildInputs must not inject a value for an absent optional input")
	}
}

// A required-and-null input is rejected (TestValidateChildInputs_
// NullRequiredIsTreatedAsMissing); this pins the OPTIONAL counterpart: an
// optional input present with an explicit null is accepted (the "rejects
// exactly three things" list above does not include it) and passed through
// UNCHANGED rather than stripped to match the absent case. Downstream,
// Engine.TriggerWithDepartment treats a present-nil variable identically to
// an absent one (engine.go: `!provided || val == nil`), so this key surviving
// as an explicit nil is what lets the child's own default apply exactly as it
// would for an absent key — a different code path reaching the same outcome,
// worth pinning independently since a future change here could silently start
// rejecting or stripping it.
func TestValidateChildInputs_NullOptionalPassesThroughUnchanged(t *testing.T) {
	produced := map[string]any{"repo": "r", "issueCount": float64(1), "dryRun": nil}
	if err := validateChildInputs("child", inputs(), produced); err != nil {
		t.Fatalf("a present-but-null OPTIONAL input must satisfy the contract, got %v", err)
	}
	v, present := produced["dryRun"]
	if !present {
		t.Fatal("validateChildInputs must not strip a present-but-null optional key")
	}
	if v != nil {
		t.Errorf("validateChildInputs must not coerce a null optional value, got %v (%T)", v, v)
	}
}

func TestValidateChildInputs_ExtraKeysPassThrough(t *testing.T) {
	produced := map[string]any{"repo": "r", "issueCount": float64(1), "note": "extra"}
	if err := validateChildInputs("child", inputs(), produced); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if produced["note"] != "extra" {
		t.Error("extra keys must be passed through, not stripped: the trigger path already " +
			"tolerates and seeds them, and its reserved-name guard is the one authority")
	}
}

func TestValidateChildInputs_MissingRequired_NamesIt(t *testing.T) {
	err := validateChildInputs("child", inputs(), map[string]any{"repo": "r"})
	if err == nil {
		t.Fatal("expected an error for a missing required input")
	}
	if !errors.Is(err, ErrChildContractUnsatisfied) {
		t.Fatalf("error must wrap ErrChildContractUnsatisfied, got %v", err)
	}
	if !strings.Contains(err.Error(), "issueCount") {
		t.Fatalf("error must name the missing input so an author can fix the guidance, got: %v", err)
	}
	if !strings.Contains(err.Error(), "child") {
		t.Fatalf("error must name the child workflow, got: %v", err)
	}
}

func TestValidateChildInputs_NullRequiredIsTreatedAsMissing(t *testing.T) {
	err := validateChildInputs("child", inputs(), map[string]any{"repo": "r", "issueCount": nil})
	if err == nil {
		t.Fatal("a required input present but null must be rejected exactly like a missing one")
	}
	if !strings.Contains(err.Error(), "issueCount") {
		t.Fatalf("error must name issueCount, got: %v", err)
	}
}

func TestValidateChildInputs_MultipleMissing_NamesAllSorted(t *testing.T) {
	err := validateChildInputs("child", inputs(), map[string]any{})
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "issueCount") || !strings.Contains(msg, "repo") {
		t.Fatalf("error must name every missing required input, got: %v", err)
	}
	if strings.Index(msg, "issueCount") > strings.Index(msg, "repo") {
		t.Fatalf("missing inputs must be listed in sorted order for a stable message, got: %v", err)
	}
}

func TestValidateChildInputs_NilMapIsRejected(t *testing.T) {
	err := validateChildInputs("child", inputs(), nil)
	if err == nil {
		t.Fatal("a nil produced map must be rejected")
	}
	if !errors.Is(err, ErrChildContractUnsatisfied) {
		t.Fatalf("error must wrap ErrChildContractUnsatisfied, got %v", err)
	}
}

func TestValidateChildInputs_ZeroDeclaredInputsAcceptsEmptyMap(t *testing.T) {
	if err := validateChildInputs("child", nil, map[string]any{}); err != nil {
		t.Fatalf("a child with no declared inputs is satisfied by an empty map, got %v", err)
	}
}

// A required input present as an EMPTY STRING or ZERO is satisfied: the
// preparer does not second-guess a legitimately empty value. Only absence and
// explicit null are failures.
func TestValidateChildInputs_EmptyStringAndZeroAreSatisfying(t *testing.T) {
	err := validateChildInputs("child", inputs(), map[string]any{"repo": "", "issueCount": float64(0)})
	if err != nil {
		t.Fatalf("empty string / zero must satisfy a required input, got %v", err)
	}
}

// A required input present with a value of the WRONG declared type (a string
// where the child declares "number") is satisfied and passed through
// UNCHANGED: no type check and no coercion is attempted here by design — the
// declared type is prompt guidance only, and this pins that a future change
// cannot silently start rejecting or mutating a type-mismatched value without
// this test failing first.
func TestValidateChildInputs_TypeMismatchIsNeitherRejectedNorCoerced(t *testing.T) {
	produced := map[string]any{"repo": "r", "issueCount": "three"}
	if err := validateChildInputs("child", inputs(), produced); err != nil {
		t.Fatalf("a required input of the wrong declared type must still satisfy the contract, got %v", err)
	}
	if produced["issueCount"] != "three" {
		t.Errorf(`validateChildInputs must not coerce issueCount, got %v (%T)`, produced["issueCount"], produced["issueCount"])
	}
}
