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

	// A later validation must reload the current contract, not reuse a stale
	// answer from the previous call.
	r.inputs["deploy"][0].Type = "number"
	errs = workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(r))
	assert.NotNil(t, grpFind(errs, "GROUP_INPUT_TYPE_MISMATCH", "east"), grpCodes(errs))
	assert.NotNil(t, grpFind(errs, "GROUP_INPUT_TYPE_MISMATCH", "west"), grpCodes(errs))
	assert.Equal(t, map[string]int{"deploy": 2}, r.calls)
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
	r := newGrpChildReader()
	wf := grpChildrenWF(
		workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: map[string]any{"region": "us"}},
		workflow.GroupTask{Key: "west", Workflow: "ghost"},
	)
	// Both name-only answers contradict the reader. Only the reader's
	// contracts may determine the final findings.
	errs := workflow.Validate(wf, nil, nil, nil,
		workflow.WithWorkflowChecker(grpNames{"ghost": true}), workflow.WithChildContractReader(r))
	require.Len(t, errs, 1, grpCodes(errs))
	assert.NotNil(t, grpFind(errs, "GROUP_UNKNOWN_CHILD_WORKFLOW", "west"))
	assert.Equal(t, map[string]int{"deploy": 1, "ghost": 1}, r.calls)
}

func TestValidateGroupChildren_BranchesGroupNeverConsultsReader(t *testing.T) {
	r := newGrpChildReader()
	errs := workflow.Validate(grpBasic(), grpFiles{}, grpProfiles{}, nil,
		workflow.WithChildContractReader(r), workflow.WithGroupCheckersRequired())
	assert.Empty(t, errs, grpCodes(errs))
	assert.Empty(t, r.calls)
}

func TestValidateGroupChildren_FailedLookupsAreCachedButFindingsArePerTask(t *testing.T) {
	for _, lookupErr := range []error{fmt.Errorf("hidden: %w", workflow.ErrNotFound), errors.New("store unavailable")} {
		t.Run(lookupErr.Error(), func(t *testing.T) {
			r := newGrpChildReader()
			r.fail["deploy"] = lookupErr
			wf := grpChildrenWF(
				workflow.GroupTask{Key: "east", Workflow: "deploy", Inputs: map[string]any{"bogus": 1}},
				workflow.GroupTask{Key: "west", Workflow: "deploy"},
			)
			errs := workflow.Validate(wf, nil, nil, nil, workflow.WithChildContractReader(r))
			require.Len(t, errs, 2, grpCodes(errs))
			code := "GROUP_CHECKER_UNAVAILABLE"
			if errors.Is(lookupErr, workflow.ErrNotFound) {
				code = "GROUP_UNKNOWN_CHILD_WORKFLOW"
			}
			assert.NotNil(t, grpFind(errs, code, "east"))
			assert.NotNil(t, grpFind(errs, code, "west"))
			assert.Equal(t, map[string]int{"deploy": 1}, r.calls)
		})
	}
}

func TestValidateGroupChildren_CheckerAvailabilityUsesFirstGroupEvenWithInvalidID(t *testing.T) {
	for _, firstID := range []string{"first", ""} {
		t.Run("id="+firstID, func(t *testing.T) {
			wf := grpBasic()
			wf.Nodes[1].ID = firstID
			// A malformed first group must not suppress missing-checker
			// findings or redirect them to a later group node.
			wf.Nodes = append(wf.Nodes, grpGroup("later", nil))
			errs := workflow.Validate(wf, nil, nil, nil, workflow.WithGroupCheckersRequired())
			var missing []workflow.ValidationError
			for _, e := range errs {
				if e.Code == "GROUP_CHECKER_UNAVAILABLE" {
					missing = append(missing, e)
					assert.Equal(t, firstID, e.NodeID)
				}
			}
			assert.Len(t, missing, 3, grpCodes(errs))
		})
	}
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
