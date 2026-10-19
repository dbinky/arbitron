# Parallel Workflow Gathering — Phase 1 Task 4: Child-contract checks and mandatory group checkers

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Validate every workflows-form task against its child workflow's declared inputs through a scoped `ChildContractReader` (unknown explicit keys, statically known literal types, required-input runtime advisory, invisible child, lookup failure), and add `WithGroupCheckersRequired` so that, for any group-bearing workflow, a missing child reader, profile checker or file checker is reported as `GROUP_CHECKER_UNAVAILABLE` instead of silently skipped.

**Architecture:** A new file `domain/workflow/validation_group_children.go` defines the `ChildContractReader` port, the two `ValidateOption`s, `validateGroupChildren` (one memoized lookup per distinct child per `Validate` call; name-only `WorkflowChecker` fallback when no reader is given) and `validateGroupCheckerAvailability`. `validateOptions` (`validation.go:59-61`) gains two fields and P1-T3's `validateGroups` gains two calls; workflows without groups never reach any of it.

**Tech Stack:** Go 1.25.5, standard library (`encoding/json`, `errors`, `reflect`, `sort`, `strconv`, `strings`), testify.

**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (Child tasks may reuse one workflow with different inputs; actionable authoring feedback); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §2 "Validation" (child contract via scoped read capability, required input explicitly mapped or present at runtime → advisory, statically known types) and §3 ("A missing checker or failed contract lookup must be reported, not silently skipped") — the validator half of **PLANNED DEPENDENCY P1-B** (the service half is P1-T5).

## Interfaces

**Consumes:**
- P1-T1: `NodeTypeParallelGroup`, `GroupFormWorkflows`, `GroupGatherResultsAndFiles`, `type GroupConfig`, `type GroupTask struct { Key, Entry, Workflow string; Inputs map[string]any }`, unexported `const groupReservedInputName = "workspace"`, `ValidationError.TaskKey`.
- P1-T3 (`domain/workflow/validation_group.go`): `func validateGroups(wf *Workflow, files FileChecker, profiles ProfileChecker, o validateOptions) []ValidationError` — its tail (lines 56-58) is extended here.
- P1-T2/P1-T3 test helpers (same test package): `grpT`, `grpEntryT`, `grpGroup`, `grpE`, `grpOK`, `grpFail`, `grpBasic()` (Name `"fan"`, branches group `g`), `grpCodes(errs) []string` (`group_ownership_test.go`); `grpProfiles` (alias→kind map implementing `ProfileChecker`), `grpFiles` (`"workflowName|path"`→bool implementing `FileChecker`) (`validation_group_test.go`).
- Existing: `domain/workflow/errors.go:19` `var ErrNotFound`; `domain/workflow/workflow.go:180-186` `type InputField struct { Name, Type, Description string; Default any; Required bool }` (types `string|number|boolean|object|array`, `validation.go:85-91`); `domain/workflow/validation.go:47` `type WorkflowChecker interface { WorkflowExists(name string) bool }`; `:57` `type ValidateOption func(*validateOptions)`; `:59-61` `type validateOptions struct { workflows WorkflowChecker }`; `:65` `WithWorkflowChecker`.
- Runtime semantics mirrored (not called): `domain/engine/engine.go:449-466` (trigger) — a required input missing or null fails the run; an optional null takes the declared default; extra keys are tolerated at run time (author time is stricter: unknown explicit keys are errors).

**Produces (exact signatures):**
```go
type ChildContractReader interface {
	// ChildInputs returns the declared inputs of a workflow VISIBLE to the author.
	// Invisible/absent => error wrapping ErrNotFound. Any other error => lookup failure.
	ChildInputs(name string) ([]InputField, error)
}
func WithChildContractReader(r ChildContractReader) ValidateOption
func WithGroupCheckersRequired() ValidateOption
// validateOptions gains:  children ChildContractReader; groupCheckersRequired bool
```
Codes produced: `GROUP_UNKNOWN_CHILD_WORKFLOW`, `GROUP_UNKNOWN_CHILD_INPUT`, `GROUP_INPUT_TYPE_MISMATCH`, `GROUP_CHECKER_UNAVAILABLE`, and the advisory `GROUP_REQUIRED_INPUT_RUNTIME` with `Severity: "warning"` (never gating).

Behaviour later tasks rely on:
- With a reader: each distinct child name is looked up ONCE per `Validate` call; `errors.Is(err, ErrNotFound)` → `GROUP_UNKNOWN_CHILD_WORKFLOW`; any other error → `GROUP_CHECKER_UNAVAILABLE` (message includes the error) and no input findings for that task.
- Without a reader but with `WithWorkflowChecker`: existence only (`GROUP_UNKNOWN_CHILD_WORKFLOW`). With both, the reader wins and nothing is double-reported.
- Types: only a WHOLE-value template (`"{{path}}"`) is exempt from the static type check; any other string (including `"n-{{x}}"`) is a string. `nil` is a mismatch for a required input and accepted for an optional one.
- `WithGroupCheckersRequired` follows the contract literally: when the workflow has at least one `parallel_group`, EACH absent checker — `ChildContractReader` (option), `ProfileChecker` and `FileChecker` (positional) — is one `GROUP_CHECKER_UNAVAILABLE` finding with `NodeID` = the first group node in `wf.Nodes` order. It is unconditional (a results-only branches group is rejected too), because a write path missing a checker is mis-wired; P1-T5 (`TestServiceGroup_SetGroupCheckers_NilUnwires`) and P1-T6 (`TestGroupOps_Upsert_RoutesThroughUpdateValidation`) depend on exactly this. Without the option, every absent checker is skipped as before.

## Wiring & data provenance

- **Live path:** `Validate` → `validateGroups` (P1-T3) → `validateGroupChildren` / `validateGroupCheckerAvailability` (this task). The options are supplied by:
  - **P1-T5** `Service.GroupValidateOptions(ctx)` = `WithWorkflowChecker(VisibleNames)`, `WithChildContractReader(Service.ChildContracts(ctx))`, `WithGroupCheckersRequired()`; used by `Service.ValidateGroups` on Create/Update and by **P1-T6** group operations;
  - **P1-T7** appends `svc.GroupValidateOptions(ctx)` in `adapter/mcp/tools_workflow.go:workflowValidateHandler` (line 402) and `adapter/http/workflow_handler.go:WorkflowHandler.Validate` (line 213);
  - **P1-T8** wires the positional checkers for the service write path (`workflowSvc.SetGroupCheckers(profiles, files)` in `cmd/arbitron/main.go:initDomain`).
- **Producers of every input:**
  - `ChildContractReader`: `Service.ChildContracts(ctx)` (**P1-T5**, `domain/workflow/service_group.go`) — caller-scoped private-then-shared load through `domain/workflow/ports.go:Repository.Load` with the auditing visibility filter, the same resolution `domain/engine/engine_spawn.go:Engine.ChildInputs` (line 24) uses at run time; `InputField`s come from the child's `workflow.json` decoded by `adapter/filesystem/workflow_repo.go:WorkflowRepo.Load` (line 99).
  - `WorkflowChecker`: `domain/workflow/service.go:Service.VisibleNames` (line 443) → `NameSetChecker`.
  - `ProfileChecker` / `FileChecker`: `adapter/provider/checker.go:ResolverChecker.ProfileKind` (line 36); `adapter/filesystem/file_reader.go:FileReaderAdapter.FileExists` (line 113) bound to the caller's owner.
  - Task inputs: `GroupTask.Inputs` from the manifest (`ParseGroupConfig`, P1-T1).
- **Integration note for P1-T5/P1-T7 (gating):** `GROUP_REQUIRED_INPUT_RUNTIME` is the first finding `Validate` itself can return with `Severity == "warning"`, and only when a `ChildContractReader` is supplied. Every caller that supplies one must gate on findings whose `Severity != "warning"`; the existing `valid := len(errs) == 0` in `workflowValidateHandler`/`WorkflowHandler.Validate` must become "no non-warning finding" when P1-T7 adds `GroupValidateOptions`. P1-T5's contract already says "warnings retained, never gating".
- **Provenance probes:** `TestValidateGroupChildren_RequiredCheckersMissingAreReported` and `TestValidateGroupChildren_EveryAbsentCheckerIsReported` fail if a nil child reader / profile checker / file checker is silently skipped under `WithGroupCheckersRequired`; `TestValidateGroupChildren_EachTaskUsesItsOwnChildContract` fails if the reader's name argument is ignored (the fake is keyed by name); Step 4 shows eleven tests failing when the two calls are not wired into `validateGroups`.

### Task 4: validation_group_children.go

**Files:**
- Create: `domain/workflow/validation_group_children.go`
- Modify: `domain/workflow/validation.go:59-61` (`validateOptions` — add `children`, `groupCheckersRequired`)
- Modify: `domain/workflow/validation_group.go:56-58` (P1-T3's `validateGroups` tail — add the two calls)
- Test: `domain/workflow/validation_group_children_test.go` (package `workflow_test`, testify)

- [x] Step 1: Write the failing test. Create `domain/workflow/validation_group_children_test.go`:

```go
package workflow_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grpChildReader is a ChildContractReader keyed by workflow NAME: each name
// returns its own declared inputs, a name in fail returns that error, and any
// other name is invisible (ErrNotFound). calls counts lookups per name so
// tests can prove which child was consulted and that lookups are cached.
type grpChildReader struct {
	inputs map[string][]workflow.InputField
	fail   map[string]error
	calls  map[string]int
}

func newGrpChildReader() *grpChildReader {
	return &grpChildReader{
		inputs: map[string][]workflow.InputField{
			"deploy": {
				{Name: "region", Type: "string", Required: true},
				{Name: "replicas", Type: "number"},
				{Name: "dry_run", Type: "boolean"},
				{Name: "labels", Type: "object"},
				{Name: "targets", Type: "array"},
			},
			"notify": {
				{Name: "channel", Type: "string", Required: true},
			},
		},
		fail:  map[string]error{},
		calls: map[string]int{},
	}
}

func (r *grpChildReader) ChildInputs(name string) ([]workflow.InputField, error) {
	r.calls[name]++
	if err, ok := r.fail[name]; ok {
		return nil, err
	}
	in, ok := r.inputs[name]
	if !ok {
		return nil, fmt.Errorf("child %q: %w", name, workflow.ErrNotFound)
	}
	return in, nil
}

// grpNames is a name-only WorkflowChecker.
type grpNames map[string]bool

func (n grpNames) WorkflowExists(name string) bool { return n[name] }

// grpChildrenWF is a valid workflows-form group running the given tasks.
func grpChildrenWF(tasks ...workflow.GroupTask) *workflow.Workflow {
	cfg := &workflow.GroupConfig{
		Form:               workflow.GroupFormWorkflows,
		Tasks:              tasks,
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
		GatherMode:         workflow.GroupGatherResults,
	}
	return &workflow.Workflow{
		Name: "fan",
		Nodes: []workflow.Node{
			grpEntryT("start"), grpGroup("g", cfg), grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{grpE("start", "g"), grpOK("g", "done"), grpFail("g", "fail")},
	}
}

func grpFind(errs []workflow.ValidationError, code, taskKey string) *workflow.ValidationError {
	for i := range errs {
		if errs[i].Code == code && errs[i].NodeID == "g" && errs[i].TaskKey == taskKey {
			return &errs[i]
		}
	}
	return nil
}

// ---------- happy path ----------

func TestValidateGroupChildren_MappedInputsAreValidAndLookupsCached(t *testing.T) {
	r := newGrpChildReader()
	wf := grpChildrenWF(
		workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: map[string]any{
			"region": "us-east-1", "replicas": float64(3), "dry_run": true,
			"labels": map[string]any{"team": "core"}, "targets": []any{"a", "b"},
		}},
		workflow.GroupTask{Key: "west", Workflow: "deploy", Inputs: map[string]any{"region": "us-west-2", "replicas": 2}},
	)
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(r))
	assert.Empty(t, errs, grpCodes(errs))
	assert.Equal(t, map[string]int{"deploy": 1}, r.calls, "one lookup per distinct child per Validate call")
}

// The reader is keyed by name: each task is checked against ITS child's
// contract, not the first one loaded.
func TestValidateGroupChildren_EachTaskUsesItsOwnChildContract(t *testing.T) {
	r := newGrpChildReader()
	wf := grpChildrenWF(
		workflow.GroupTask{Key: "ship", Workflow: "deploy", Inputs: map[string]any{"region": "eu"}},
		workflow.GroupTask{Key: "ping", Workflow: "notify", Inputs: map[string]any{"region": "eu"}},
	)
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(r))
	assert.Nil(t, grpFind(errs, "GROUP_UNKNOWN_CHILD_INPUT", "ship"), grpCodes(errs))
	assert.NotNil(t, grpFind(errs, "GROUP_UNKNOWN_CHILD_INPUT", "ping"), grpCodes(errs))
	assert.NotNil(t, grpFind(errs, "GROUP_REQUIRED_INPUT_RUNTIME", "ping"), "notify requires channel")
	assert.Equal(t, map[string]int{"deploy": 1, "notify": 1}, r.calls)
}

// ---------- failure ----------

func TestValidateGroupChildren_UnknownExplicitInputKey(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy",
		Inputs: map[string]any{"region": "x", "regoin": "typo"}})
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(newGrpChildReader()))
	e := grpFind(errs, "GROUP_UNKNOWN_CHILD_INPUT", "east")
	require.NotNil(t, e, grpCodes(errs))
	assert.Contains(t, e.Message, `"regoin"`)
	assert.Contains(t, e.Message, "deploy")
	assert.Empty(t, e.Severity, "an unknown key is a hard error")
}

func TestValidateGroupChildren_LiteralTypeMismatch(t *testing.T) {
	cases := []struct {
		name  string
		input string
		value any
	}{
		{"string for number", "replicas", "three"},
		{"number for string", "region", float64(1)},
		{"string for boolean", "dry_run", "true"},
		{"array for object", "labels", []any{"x"}},
		{"object for array", "targets", map[string]any{"a": 1}},
		{"null for required", "region", nil},
		// Only a WHOLE-value template keeps its type at run time; any other
		// string containing a template interpolates to a string.
		{"interpolated string for number", "replicas", "n-{{count}}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inputs := map[string]any{"region": "us"}
			inputs[tc.input] = tc.value
			wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: inputs})
			errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(newGrpChildReader()))
			e := grpFind(errs, "GROUP_INPUT_TYPE_MISMATCH", "east")
			require.NotNil(t, e, grpCodes(errs))
			assert.Contains(t, e.Message, tc.input)
		})
	}
}

// A whole-value template resolves to a typed value at run time; its type is
// not knowable statically, so it is never type-checked here.
func TestValidateGroupChildren_TemplatesAreNotTypeChecked(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: map[string]any{
		"region": "{{target_region}}", "replicas": "{{count}}", "dry_run": "{{ flags.dry }}",
		"labels": "{{label_map}}", "targets": []any{"{{first}}", "static"},
	}})
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(newGrpChildReader()))
	assert.Empty(t, errs, grpCodes(errs))
}

func TestValidateGroupChildren_NullForOptionalInputFallsBackToDefault(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy",
		Inputs: map[string]any{"region": "us", "replicas": nil}})
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(newGrpChildReader()))
	assert.Empty(t, errs, grpCodes(errs))
}

// ---------- edge: inherited required input is a runtime advisory ----------

func TestValidateGroupChildren_UnmappedRequiredInputIsAdvisoryOnly(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy"})
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(newGrpChildReader()))
	require.Len(t, errs, 1, grpCodes(errs))
	assert.Equal(t, "GROUP_REQUIRED_INPUT_RUNTIME", errs[0].Code)
	assert.Equal(t, "warning", errs[0].Severity, "inherited group-start variables may satisfy it; never gating")
	assert.Equal(t, "east", errs[0].TaskKey)
	assert.Contains(t, errs[0].Message, "region")
}

func TestValidateGroupChildren_UnmappedOptionalInputIsSilent(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: map[string]any{"region": "us"}})
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(newGrpChildReader()))
	assert.Empty(t, errs, grpCodes(errs))
}

// ---------- error: lookup failures ----------

func TestValidateGroupChildren_InvisibleChildIsUnknown(t *testing.T) {
	r := newGrpChildReader()
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "private-of-someone-else"})
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(r))
	assert.NotNil(t, grpFind(errs, "GROUP_UNKNOWN_CHILD_WORKFLOW", "east"), grpCodes(errs))
	assert.Equal(t, 1, r.calls["private-of-someone-else"])
}

func TestValidateGroupChildren_ReaderFailureIsCheckerUnavailable(t *testing.T) {
	r := newGrpChildReader()
	r.fail["deploy"] = errors.New("disk on fire")
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: map[string]any{"bogus": 1}})
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(r))
	e := grpFind(errs, "GROUP_CHECKER_UNAVAILABLE", "east")
	require.NotNil(t, e, grpCodes(errs))
	assert.Contains(t, e.Message, "disk on fire")
	assert.Nil(t, grpFind(errs, "GROUP_UNKNOWN_CHILD_INPUT", "east"), "no contract, no input findings")
}

func TestValidateGroupChildren_RequiredCheckersMissingAreReported(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy"})
	errs := workflow.Validate(wf, grpFiles{}, grpProfiles{}, nil, workflow.WithGroupCheckersRequired())
	e := grpFind(errs, "GROUP_CHECKER_UNAVAILABLE", "")
	require.NotNil(t, e, grpCodes(errs))
	assert.Contains(t, e.Message, "child workflow contract reader")
}

// Each absent checker is its own finding, and the rule applies to EVERY
// group-bearing workflow — even a results-only branches group that would not
// consult the checker — because a write path missing a checker is mis-wired.
func TestValidateGroupChildren_EveryAbsentCheckerIsReported(t *testing.T) {
	errs := workflow.Validate(grpBasic(), nil, nil, nil, workflow.WithGroupCheckersRequired())
	var msgs []string
	for _, e := range errs {
		if e.Code == "GROUP_CHECKER_UNAVAILABLE" {
			assert.Equal(t, "g", e.NodeID)
			msgs = append(msgs, e.Message)
		}
	}
	require.Len(t, msgs, 3, grpCodes(errs))
	assert.Contains(t, msgs[0], "child workflow contract reader")
	assert.Contains(t, msgs[1], "profile checker")
	assert.Contains(t, msgs[2], "file checker")
}

func TestValidateGroupChildren_AllCheckersSuppliedSatisfiesRequirement(t *testing.T) {
	wf := grpBasic()
	cfg := wf.Nodes[1].Config.Group
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles
	cfg.ResolverProfile = "merger"
	cfg.ResolverPromptFile = "prompts/resolve.md"
	errs := workflow.Validate(wf, grpFiles{"fan|prompts/resolve.md": true}, grpProfiles{"merger": "agent"}, nil,
		workflow.WithChildContractReader(newGrpChildReader()), workflow.WithGroupCheckersRequired())
	assert.Empty(t, errs, grpCodes(errs))
}

// Without WithGroupCheckersRequired a nil reader is skipped, like every other
// optional checker.
func TestValidateGroupChildren_NilReaderNotRequiredIsSkipped(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: map[string]any{"bogus": 1}})
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.Empty(t, errs, grpCodes(errs))
}

func TestValidateGroupChildren_NameOnlyCheckerFallback(t *testing.T) {
	wf := grpChildrenWF(
		workflow.GroupTask{Key: "east", Workflow: "deploy"},
		workflow.GroupTask{Key: "west", Workflow: "ghost"},
	)
	errs := workflow.Validate(wf, nil, nil, nil, workflow.WithWorkflowChecker(grpNames{"deploy": true}))
	assert.Nil(t, grpFind(errs, "GROUP_UNKNOWN_CHILD_WORKFLOW", "east"))
	assert.NotNil(t, grpFind(errs, "GROUP_UNKNOWN_CHILD_WORKFLOW", "west"), grpCodes(errs))
}

func TestValidateGroupChildren_ReaderWinsOverNameCheckerNoDoubleReport(t *testing.T) {
	wf := grpChildrenWF(workflow.GroupTask{Key: "west", Workflow: "ghost"})
	errs := workflow.Validate(wf, nil, nil, nil,
		workflow.WithWorkflowChecker(grpNames{}), workflow.WithChildContractReader(newGrpChildReader()))
	n := 0
	for _, e := range errs {
		if e.Code == "GROUP_UNKNOWN_CHILD_WORKFLOW" {
			n++
		}
	}
	assert.Equal(t, 1, n, grpCodes(errs))
}

func TestValidateGroupChildren_BranchesGroupNeverConsultsReader(t *testing.T) {
	r := newGrpChildReader()
	errs := workflow.Validate(grpBasic(), grpFiles{}, grpProfiles{}, nil,
		workflow.WithChildContractReader(r), workflow.WithGroupCheckersRequired())
	assert.Empty(t, errs, grpCodes(errs))
	assert.Empty(t, r.calls)
}

// ---------- legacy regression ----------

// A workflow without groups produces byte-identical findings with or without
// the new options, and the child reader is never consulted.
func TestValidateGroupChildren_LegacyWorkflowsIdenticalWithNewOptions(t *testing.T) {
	legacy := []*workflow.Workflow{
		{Name: "ok", Nodes: []workflow.Node{grpEntryT("s"), grpT("x")}, Edges: []workflow.Edge{grpE("s", "x")}},
		{Name: "broken", Nodes: []workflow.Node{
			grpEntryT("s"),
			{ID: "sp", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{ChildWorkflow: "nope", SpawnMode: workflow.SpawnModeFireAndForget}},
			grpT("x"), grpT("orphan"),
		}, Edges: []workflow.Edge{grpE("s", "sp"), grpE("sp", "x")}},
	}
	for _, wf := range legacy {
		names := grpNames{"ok": true}
		before := workflow.Validate(wf, nil, nil, nil, workflow.WithWorkflowChecker(names))
		r := newGrpChildReader()
		after := workflow.Validate(wf, nil, nil, nil, workflow.WithWorkflowChecker(names),
			workflow.WithChildContractReader(r), workflow.WithGroupCheckersRequired())
		assert.Equal(t, before, after, wf.Name)
		assert.Empty(t, r.calls, wf.Name)
	}
}
```

- [x] Step 2: Run it and confirm it fails to compile.

```bash
go test ./domain/workflow/ -run 'TestValidateGroupChildren' -v
```
Expected:
```
# github.com/arbitron/arbitron/domain/workflow_test [github.com/arbitron/arbitron/domain/workflow.test]
domain/workflow/validation_group_children_test.go:97:56: undefined: workflow.WithChildContractReader
domain/workflow/validation_group_children_test.go:110:56: undefined: workflow.WithChildContractReader
domain/workflow/validation_group_children_test.go:122:56: undefined: workflow.WithChildContractReader
...
FAIL	github.com/arbitron/arbitron/domain/workflow [build failed]
```

- [x] Step 3: Add the option fields. In `domain/workflow/validation.go`, replace lines 59-61:

```go
type validateOptions struct {
	workflows WorkflowChecker
}
```
with:
```go
type validateOptions struct {
	workflows WorkflowChecker
	// children loads child workflow contracts for workflows-form group tasks
	// (WithChildContractReader, validation_group_children.go).
	children ChildContractReader
	// groupCheckersRequired turns an absent, needed group checker into
	// GROUP_CHECKER_UNAVAILABLE (WithGroupCheckersRequired).
	groupCheckersRequired bool
}
```

- [x] Step 4: Minimal implementation. Create `domain/workflow/validation_group_children.go`:

```go
package workflow

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// ChildContractReader loads the declared inputs of a child workflow for
// author-time validation of workflows-form parallel_group tasks. It is the
// group analogue of WorkflowChecker, which answers existence only and cannot
// supply a contract.
//
// Implementations MUST be scoped to the author: resolution follows the same
// private-then-shared, visibility-filtered lookup the run will use, so a
// workflow the author cannot see is indistinguishable from one that does not
// exist.
type ChildContractReader interface {
	// ChildInputs returns the declared inputs of a workflow VISIBLE to the author.
	// Invisible/absent => error wrapping ErrNotFound. Any other error => lookup failure.
	ChildInputs(name string) ([]InputField, error)
}

// WithChildContractReader enables author-time checks of every workflows-form
// task against its child's declared inputs: unknown explicit input keys,
// statically known literal types, and an advisory for required inputs that
// are not explicitly mapped.
func WithChildContractReader(r ChildContractReader) ValidateOption {
	return func(o *validateOptions) { o.children = r }
}

// WithGroupCheckersRequired makes the group checkers mandatory: when the
// workflow has at least one parallel_group, an absent ChildContractReader,
// ProfileChecker or FileChecker is reported as GROUP_CHECKER_UNAVAILABLE
// instead of the corresponding check being silently skipped. Write paths
// (Service.Create/Update, the group operations, the validate endpoints) pass
// it; a workflow without groups is unaffected.
func WithGroupCheckersRequired() ValidateOption {
	return func(o *validateOptions) { o.groupCheckersRequired = true }
}

// groupAdvisorySeverity marks a non-gating group finding.
const groupAdvisorySeverity = "warning"

// childContract is one memoized ChildInputs answer.
type childContract struct {
	fields []InputField
	err    error
}

// validateGroupChildren checks every workflows-form task's child reference.
// With a ChildContractReader it loads each distinct child once per call and
// checks the task's explicit inputs; without one it falls back to the
// name-only WorkflowChecker (existence) when that is supplied. Without either,
// nothing is checked here (validateGroupCheckerAvailability reports that when
// checkers are required).
func validateGroupChildren(wf *Workflow, o validateOptions) []ValidationError {
	if o.children == nil && o.workflows == nil {
		return nil
	}
	cache := map[string]childContract{}
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeParallelGroup || n.Config.Group == nil || n.Config.Group.Form != GroupFormWorkflows {
			continue
		}
		for _, t := range n.Config.Group.Tasks {
			if t.Workflow == "" {
				continue // GROUP_INVALID_TASK_TARGET already reported by the config pass
			}
			if o.children == nil {
				if !o.workflows.WorkflowExists(t.Workflow) {
					errs = append(errs, unknownChildWorkflow(n.ID, t))
				}
				continue
			}
			c, ok := cache[t.Workflow]
			if !ok {
				fields, err := o.children.ChildInputs(t.Workflow)
				c = childContract{fields: fields, err: err}
				cache[t.Workflow] = c
			}
			switch {
			case c.err != nil && errors.Is(c.err, ErrNotFound):
				errs = append(errs, unknownChildWorkflow(n.ID, t))
			case c.err != nil:
				errs = append(errs, ValidationError{
					Code: "GROUP_CHECKER_UNAVAILABLE",
					Message: "parallel_group " + n.ID + " task " + strconv.Quote(t.Key) + ": could not load the input contract of child workflow " +
						strconv.Quote(t.Workflow) + " (" + c.err.Error() + "); retry validation once the workflow store is reachable",
					NodeID:  n.ID,
					TaskKey: t.Key,
				})
			default:
				errs = append(errs, checkTaskInputs(n.ID, t, c.fields)...)
			}
		}
	}
	return errs
}

func unknownChildWorkflow(groupID string, t GroupTask) ValidationError {
	return ValidationError{
		Code: "GROUP_UNKNOWN_CHILD_WORKFLOW",
		Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " references unknown workflow " +
			strconv.Quote(t.Workflow) + "; use the name of a workflow you can see (workflow_list)",
		NodeID:  groupID,
		TaskKey: t.Key,
	}
}

// checkTaskInputs checks one task's explicit inputs against its child's
// declared fields. Keys are visited in sorted order so findings are stable.
func checkTaskInputs(groupID string, t GroupTask, fields []InputField) []ValidationError {
	declared := make(map[string]InputField, len(fields))
	for _, f := range fields {
		declared[f.Name] = f
	}
	keys := make([]string, 0, len(t.Inputs))
	for k := range t.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var errs []ValidationError
	for _, k := range keys {
		if k == groupReservedInputName {
			continue // GROUP_RESERVED_INPUT already reported by the config pass
		}
		f, ok := declared[k]
		if !ok {
			errs = append(errs, ValidationError{
				Code: "GROUP_UNKNOWN_CHILD_INPUT",
				Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " sets input " + strconv.Quote(k) +
					", which child workflow " + strconv.Quote(t.Workflow) + " does not declare (declared: " + declaredNames(fields) + ")",
				NodeID:  groupID,
				TaskKey: t.Key,
			})
			continue
		}
		if problem := groupLiteralTypeProblem(f, t.Inputs[k]); problem != "" {
			errs = append(errs, ValidationError{
				Code: "GROUP_INPUT_TYPE_MISMATCH",
				Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " input " + strconv.Quote(k) +
					" " + problem + "; child workflow " + strconv.Quote(t.Workflow) + " declares it as " + f.Type,
				NodeID:  groupID,
				TaskKey: t.Key,
			})
		}
	}

	for _, f := range fields {
		if !f.Required {
			continue
		}
		if _, mapped := t.Inputs[f.Name]; mapped {
			continue
		}
		errs = append(errs, ValidationError{
			Code: "GROUP_REQUIRED_INPUT_RUNTIME",
			Message: "parallel_group " + groupID + " task " + strconv.Quote(t.Key) + " does not map required input " +
				strconv.Quote(f.Name) + " of child workflow " + strconv.Quote(t.Workflow) +
				"; it must be present in the group-start variables when the group runs (checked at run time), or map it explicitly in the task's inputs",
			NodeID:   groupID,
			TaskKey:  t.Key,
			Severity: groupAdvisorySeverity,
		})
	}
	return errs
}

func declaredNames(fields []InputField) string {
	if len(fields) == 0 {
		return "none"
	}
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// groupLiteralTypeProblem returns "" when v is acceptable for field f, else a
// description of the mismatch. A WHOLE-value template ("{{path}}") resolves to
// a typed value at run time and is never checked. A null literal is accepted
// for an optional input (the child's default applies, exactly as at trigger
// time) and rejected for a required one. An unrecognized declared type is not
// checked (validateInputTypes owns that finding on the child).
func groupLiteralTypeProblem(f InputField, v any) string {
	if s, ok := v.(string); ok && isWholeTemplate(s) {
		return ""
	}
	if v == nil {
		if f.Required {
			return "is null but the input is required"
		}
		return ""
	}
	var ok bool
	switch f.Type {
	case "string":
		_, ok = v.(string)
	case "number":
		switch v.(type) {
		case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
			ok = true
		}
	case "boolean":
		_, ok = v.(bool)
	case "object":
		rv := reflect.ValueOf(v)
		ok = rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String
	case "array":
		k := reflect.ValueOf(v).Kind()
		ok = k == reflect.Slice || k == reflect.Array
	default:
		return ""
	}
	if ok {
		return ""
	}
	return "is a " + literalKind(v) + " literal"
}

// isWholeTemplate reports whether s is exactly one "{{path}}" reference.
func isWholeTemplate(s string) bool {
	if len(s) < 4 || !strings.HasPrefix(s, "{{") || !strings.HasSuffix(s, "}}") {
		return false
	}
	inner := s[2 : len(s)-2]
	return !strings.Contains(inner, "{{") && !strings.Contains(inner, "}}") && strings.TrimSpace(inner) != ""
}

func literalKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return "number"
	}
	switch reflect.ValueOf(v).Kind() {
	case reflect.Map:
		return "object"
	case reflect.Slice, reflect.Array:
		return "array"
	}
	return reflect.TypeOf(v).String()
}

// validateGroupCheckerAvailability enforces WithGroupCheckersRequired: when
// the workflow has at least one parallel_group, each absent group checker —
// the child contract reader, the profile checker, the file checker — is one
// GROUP_CHECKER_UNAVAILABLE finding, attributed to the first group node. The
// rule is deliberately unconditional (not "only when this group needs it"): a
// write path that is missing a checker is mis-wired, and every group write is
// refused loudly until the composition root supplies all three.
func validateGroupCheckerAvailability(wf *Workflow, files FileChecker, profiles ProfileChecker, o validateOptions) []ValidationError {
	if !o.groupCheckersRequired {
		return nil
	}
	first := ""
	for _, n := range wf.Nodes {
		if n.Type == NodeTypeParallelGroup {
			first = n.ID
			break
		}
	}
	if first == "" {
		return nil
	}
	var errs []ValidationError
	unavailable := func(what string) {
		errs = append(errs, ValidationError{
			Code: "GROUP_CHECKER_UNAVAILABLE",
			Message: "parallel_group definitions in this workflow cannot be fully validated: " + what +
				" is not configured on this server, so the workflow is rejected rather than accepted unverified (operator fix: wire the group checkers)",
			NodeID: first,
		})
	}
	if o.children == nil {
		unavailable("the child workflow contract reader")
	}
	if profiles == nil {
		unavailable("the profile checker (resolverProfile)")
	}
	if files == nil {
		unavailable("the workflow file checker (resolverPromptFile)")
	}
	return errs
}
```

Then prove the wiring matters before adding it:

```bash
go test ./domain/workflow/ -run 'TestValidateGroupChildren'
```
Expected (compiles; the checks exist but `validateGroups` does not call them yet):
```
--- FAIL: TestValidateGroupChildren_MappedInputsAreValidAndLookupsCached (0.00s)
--- FAIL: TestValidateGroupChildren_EachTaskUsesItsOwnChildContract (0.00s)
--- FAIL: TestValidateGroupChildren_UnknownExplicitInputKey (0.00s)
--- FAIL: TestValidateGroupChildren_LiteralTypeMismatch (0.00s)
--- FAIL: TestValidateGroupChildren_UnmappedRequiredInputIsAdvisoryOnly (0.00s)
--- FAIL: TestValidateGroupChildren_InvisibleChildIsUnknown (0.00s)
--- FAIL: TestValidateGroupChildren_ReaderFailureIsCheckerUnavailable (0.00s)
--- FAIL: TestValidateGroupChildren_RequiredCheckersMissingAreReported (0.00s)
--- FAIL: TestValidateGroupChildren_EveryAbsentCheckerIsReported (0.00s)
--- FAIL: TestValidateGroupChildren_NameOnlyCheckerFallback (0.00s)
--- FAIL: TestValidateGroupChildren_ReaderWinsOverNameCheckerNoDoubleReport (0.00s)
FAIL
FAIL	github.com/arbitron/arbitron/domain/workflow	0.3s
```

- [x] Step 5: Wire it. In `domain/workflow/validation_group.go`, replace lines 56-58 of `validateGroups`:

```go
	_, graphErrs := AnalyzeGroups(wf)
	errs = append(errs, graphErrs...)
	return errs
```
with:
```go
	_, graphErrs := AnalyzeGroups(wf)
	errs = append(errs, graphErrs...)
	errs = append(errs, validateGroupChildren(wf, o)...)
	errs = append(errs, validateGroupCheckerAvailability(wf, files, profiles, o)...)
	return errs
```

Both calls sit inside `validateGroups`, after its `HasGroups(wf)` early return, so a workflow without groups never consults the reader (`TestValidateGroupChildren_LegacyWorkflowsIdenticalWithNewOptions`).

- [x] Step 6: Run the tests and confirm they pass.

```bash
gofmt -l domain/workflow/
go test ./domain/workflow/ -run 'TestValidateGroupChildren' -v
```
Expected (`gofmt -l` prints nothing):
```
--- PASS: TestValidateGroupChildren_MappedInputsAreValidAndLookupsCached (0.00s)
--- PASS: TestValidateGroupChildren_EachTaskUsesItsOwnChildContract (0.00s)
--- PASS: TestValidateGroupChildren_UnknownExplicitInputKey (0.00s)
--- PASS: TestValidateGroupChildren_LiteralTypeMismatch (0.00s)
    --- PASS: TestValidateGroupChildren_LiteralTypeMismatch/string_for_number (0.00s)
    --- PASS: TestValidateGroupChildren_LiteralTypeMismatch/number_for_string (0.00s)
    --- PASS: TestValidateGroupChildren_LiteralTypeMismatch/string_for_boolean (0.00s)
    --- PASS: TestValidateGroupChildren_LiteralTypeMismatch/array_for_object (0.00s)
    --- PASS: TestValidateGroupChildren_LiteralTypeMismatch/object_for_array (0.00s)
    --- PASS: TestValidateGroupChildren_LiteralTypeMismatch/null_for_required (0.00s)
    --- PASS: TestValidateGroupChildren_LiteralTypeMismatch/interpolated_string_for_number (0.00s)
--- PASS: TestValidateGroupChildren_TemplatesAreNotTypeChecked (0.00s)
--- PASS: TestValidateGroupChildren_NullForOptionalInputFallsBackToDefault (0.00s)
--- PASS: TestValidateGroupChildren_UnmappedRequiredInputIsAdvisoryOnly (0.00s)
--- PASS: TestValidateGroupChildren_UnmappedOptionalInputIsSilent (0.00s)
--- PASS: TestValidateGroupChildren_InvisibleChildIsUnknown (0.00s)
--- PASS: TestValidateGroupChildren_ReaderFailureIsCheckerUnavailable (0.00s)
--- PASS: TestValidateGroupChildren_RequiredCheckersMissingAreReported (0.00s)
--- PASS: TestValidateGroupChildren_EveryAbsentCheckerIsReported (0.00s)
--- PASS: TestValidateGroupChildren_AllCheckersSuppliedSatisfiesRequirement (0.00s)
--- PASS: TestValidateGroupChildren_NilReaderNotRequiredIsSkipped (0.00s)
--- PASS: TestValidateGroupChildren_NameOnlyCheckerFallback (0.00s)
--- PASS: TestValidateGroupChildren_ReaderWinsOverNameCheckerNoDoubleReport (0.00s)
--- PASS: TestValidateGroupChildren_BranchesGroupNeverConsultsReader (0.00s)
--- PASS: TestValidateGroupChildren_LegacyWorkflowsIdenticalWithNewOptions (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/workflow	0.3s
```

- [x] Step 7: Regression — the whole package (P1-T1..T3 tests and every legacy validation test), vet, the adapters, and the engine (which imports the package).

> Verified: initial undefined-option compile failure; eleven expected unwired test failures; focused tests green after wiring. Self-review completed in order: spec compliance, anti-green-theater, code quality. Strengthened reader-precedence and cache probes; added a red/green regression ensuring an empty first group ID cannot suppress mandatory-checker findings. Updated validation documentation for non-gating warnings. All commands below passed again after review, as did formatting and `git diff --check`. Scoped service/endpoint/composition-root producers remain assigned to P1-T5/P1-T7/P1-T8 as specified above.

```bash
go vet ./domain/workflow/
go test ./domain/workflow/ ./adapter/mcp/ ./adapter/http/ ./domain/engine/
go build ./...
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/workflow	0.3s
ok  	github.com/arbitron/arbitron/adapter/mcp	0.8s
ok  	github.com/arbitron/arbitron/adapter/http	6.5s
ok  	github.com/arbitron/arbitron/domain/engine	2.6s
```

- [x] Step 8: Commit.

```bash
git add domain/workflow/validation_group_children.go domain/workflow/validation_group_children_test.go domain/workflow/validation.go domain/workflow/validation_group.go && git commit -m "feat(workflow): child-contract checks and mandatory group checkers for parallel_group" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
