# Phase 2 — Task 1: `ChildInputPreparer` port, schema synthesis, and contract validation

**Goal:** Declare the `ChildInputPreparer` port and implement the two pure pieces behind it — synthesizing the output contract from the child's declared inputs, and deciding whether produced values satisfy that contract.

**Architecture:** The port lives in `domain/engine/ports.go` beside the other provider-selector ports; the pure logic lives in a new `domain/engine/child_input_contract.go`. Contract validation is the *authority* for the spec's "if required startup values cannot be produced, the spawn node fails" requirement, and it enforces exactly three rejections (missing required, required-but-null, not an object). It deliberately does NOT coerce types or strip extra keys: the trigger path is the single place input semantics are enforced, and its `SetInput` reserved-name guard already prevents a produced value from overwriting an engine-seeded variable such as `workspace`.

**Tech Stack:** Go 1.25.5, standard library `testing`/`errors`/`sort`, module `github.com/arbitron/arbitron`.

---

### Task 1: port + schema synthesis + contract check

**Files:**
- Create: `domain/engine/child_input_contract.go`
- Modify: `domain/engine/ports.go` (append the port declaration after `InteractiveStreamSelector`, ~line 78)
- Test:   `domain/engine/child_input_contract_test.go`

---

- [ ] **Step 1: Write the failing test**

Create `domain/engine/child_input_contract_test.go`:

```go
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
```

- [ ] **Step 2: Run it, confirm it fails**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
go test ./domain/engine/ -run 'TestChildInputSchema|TestValidateChildInputs'
```

Expected output:

```
# github.com/arbitron/arbitron/domain/engine [github.com/arbitron/arbitron/domain/engine.test]
domain/engine/child_input_contract_test.go:22:7: undefined: childInputSchema
domain/engine/child_input_contract_test.go:...: undefined: validateChildInputs
domain/engine/child_input_contract_test.go:...: undefined: ErrChildContractUnsatisfied
FAIL	github.com/arbitron/arbitron/domain/engine [build failed]
```

- [ ] **Step 3: Minimal implementation — the pure contract logic**

Create `domain/engine/child_input_contract.go`:

```go
package engine

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// ErrChildContractUnsatisfied is returned when the values produced for a child
// workflow do not satisfy its declared input contract. It is the spec's
// "required startup values cannot be produced" condition, made
// errors.Is-checkable so the executor and its tests can attribute the failure
// precisely rather than string-matching.
var ErrChildContractUnsatisfied = errors.New("engine: prepared startup values do not satisfy the child workflow's declared inputs")

// schemaTypeForInput maps a declared workflow input type onto its JSON-schema
// type. The child's declaration is the authority; an unrecognized or empty
// declared type yields ("", false) and the property is emitted WITHOUT a type
// rather than with a fabricated one, so no second source of truth about valid
// input types is created here.
func schemaTypeForInput(t string) (string, bool) {
	switch t {
	case "string", "number", "boolean", "object", "array":
		return t, true
	default:
		return "", false
	}
}

// childInputSchema synthesizes the output contract for the preparation call
// from the CHILD's declared input fields. Each declared input contributes its
// name, declared type, description, and required flag. The parent never names
// the fields — the child's declaration is the contract.
//
// `required` is sorted so the assembled prompt is stable across runs (an
// unstable prompt defeats provider-side caching and makes trace diffs noisy).
func childInputSchema(fields []workflow.InputField) map[string]any {
	props := make(map[string]any, len(fields))
	var required []string
	for _, f := range fields {
		p := map[string]any{}
		if t, ok := schemaTypeForInput(f.Type); ok {
			p["type"] = t
		}
		if f.Description != "" {
			p["description"] = f.Description
		}
		props[f.Name] = p
		if f.Required {
			required = append(required, f.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return schema
}

// validateChildInputs decides whether produced actually satisfies the child's
// declared contract. It is the AUTHORITY for the spec's failure requirement.
//
// It rejects exactly three things:
//   - a nil produced map (the response was not an object at all),
//   - a missing required input,
//   - a required input present but null.
//
// It deliberately ACCEPTS and passes through:
//   - absent OPTIONAL inputs — the child's own trigger-time defaulting handles
//     them, exactly as Engine.TriggerWithDepartment already does;
//   - extra keys not in the contract — the trigger path already tolerates and
//     seeds extra variables, and its SetInput reserved-name guard prevents a
//     produced value from overwriting an engine-seeded variable such as
//     "workspace". Re-implementing that rejection here would duplicate a guard
//     that already exists one layer down.
//
// Type coercion is NOT attempted. The declared types travel into the prompt as
// guidance and produced values pass through as parsed; the trigger path is the
// single place input semantics are enforced.
func validateChildInputs(childWorkflow string, fields []workflow.InputField, produced map[string]any) error {
	if produced == nil {
		return fmt.Errorf("%w: child %q: the model did not produce a JSON object",
			ErrChildContractUnsatisfied, childWorkflow)
	}

	var missing []string
	for _, f := range fields {
		if !f.Required {
			continue
		}
		v, present := produced[f.Name]
		if !present || v == nil {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	// The message is written to be actionable by a workflow author reading the
	// failed node's trace entry: the fix is almost always in the guidance prose
	// or in the parent's upstream nodes.
	return fmt.Errorf("%w: child %q is missing required input(s): %s",
		ErrChildContractUnsatisfied, childWorkflow, strings.Join(missing, ", "))
}
```

- [ ] **Step 4: Declare the port**

Append to `domain/engine/ports.go`, after the `InteractiveStreamSelector` declaration:

```go
// ChildInputRequest is everything the preparer needs to turn a parent's
// available context into startup values for a child workflow. It is a struct
// rather than a positional signature because the field set is the contract and
// is expected to grow (e.g. a per-node token budget) without breaking callers.
type ChildInputRequest struct {
	// NodeID is the spawn node being executed. It is used to attribute errors
	// and to assert the binding kind against the node type.
	NodeID string
	// ChildWorkflow is the child's name, used only for error messages and prompt
	// framing. The preparer never loads a workflow itself.
	ChildWorkflow string
	// Inputs is the CHILD's declared input contract, loaded by the caller. The
	// child's declaration is the authority; the parent never names the fields.
	Inputs []workflow.InputField
	// ParentContext is the parent run's flat variable snapshot (its inputs and
	// all prior node outputs).
	ParentContext map[string]any
	// Guidance is the optional author-supplied prose, already read by the
	// caller. Empty means no guidance was configured.
	Guidance string
	// ProfileAlias is the chat profile alias to resolve for the preparation call.
	ProfileAlias string
}

// ChildInputPreparer turns a parent's available context into startup values
// that satisfy a child workflow's declared input contract.
//
// GUARANTEES: on success, every REQUIRED input of the child is present and
// non-null. On failure, NO partial result is returned — the caller cannot
// accidentally spawn with half a payload.
//
// Because the return value is "values that satisfy the contract", the caller
// needs no knowledge of how they were obtained; a future non-LLM implementation
// (static mapping, cached result) satisfies the same port.
type ChildInputPreparer interface {
	Prepare(ctx context.Context, req ChildInputRequest) (map[string]any, error)
}
```

Add the workflow import to `domain/engine/ports.go`:

```go
import (
	"context"
	"time"

	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/queue"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
)
```

- [ ] **Step 5: Run it, confirm it passes**

```bash
cd ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work
gofmt -w domain/engine/
go test ./domain/engine/ -run 'TestChildInputSchema|TestValidateChildInputs' -v
```

Expected output:

```
--- PASS: TestChildInputSchema_ShapeAndRequired (0.00s)
--- PASS: TestChildInputSchema_ZeroInputs (0.00s)
--- PASS: TestChildInputSchema_UnknownTypeOmitsTypeKey (0.00s)
--- PASS: TestValidateChildInputs_AllRequiredPresent (0.00s)
--- PASS: TestValidateChildInputs_AbsentOptionalIsFine (0.00s)
--- PASS: TestValidateChildInputs_ExtraKeysPassThrough (0.00s)
--- PASS: TestValidateChildInputs_MissingRequired_NamesIt (0.00s)
--- PASS: TestValidateChildInputs_NullRequiredIsTreatedAsMissing (0.00s)
--- PASS: TestValidateChildInputs_MultipleMissing_NamesAllSorted (0.00s)
--- PASS: TestValidateChildInputs_NilMapIsRejected (0.00s)
--- PASS: TestValidateChildInputs_ZeroDeclaredInputsAcceptsEmptyMap (0.00s)
--- PASS: TestValidateChildInputs_EmptyStringAndZeroAreSatisfying (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/engine	0.4s
```

- [ ] **Step 6: Confirm the package builds and passes**

```bash
go build ./... && go test ./domain/engine/
```

Expected: build succeeds; `ok  	github.com/arbitron/arbitron/domain/engine`

- [ ] **Step 7: Commit**

```bash
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work add domain/engine/child_input_contract.go domain/engine/child_input_contract_test.go domain/engine/ports.go
git -C ~/.arbitron/workspaces/instances/c0c76804-f600-4662-887e-cb3a37d10874/work commit -m "feat(engine): add ChildInputPreparer port with schema synthesis and contract validation"
```
