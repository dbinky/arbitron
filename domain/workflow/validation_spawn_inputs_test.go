package workflow_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// spnReader is a ChildContractReader that counts calls per name, returns its
// configured error for a name in fail, and ErrNotFound for unknown names.
type spnReader struct {
	inputs map[string][]workflow.InputField
	fail   map[string]error
	calls  map[string]int
}

func newSpnReader() *spnReader {
	return &spnReader{
		inputs: map[string][]workflow.InputField{
			"child": {
				{Name: "topic", Type: "string", Required: true},
				{Name: "count", Type: "number"},
				{Name: "flag", Type: "boolean"},
				{Name: "depth", Type: "number", Required: true, Default: float64(3)},
				{Name: "scope", Type: "string", Required: true},
			},
		},
		fail:  map[string]error{},
		calls: map[string]int{},
	}
}

func (r *spnReader) ChildInputs(name string) ([]workflow.InputField, error) {
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

func spnNode(id, child string, inputs map[string]any, profile string) workflow.Node {
	return workflow.Node{ID: id, Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
		ChildWorkflow: child, SpawnMode: workflow.SpawnModeFireAndForget, Inputs: inputs, ModelProfile: profile,
	}}
}

func spnWF(spawns ...workflow.Node) *workflow.Workflow {
	nodes := []workflow.Node{{ID: "entry", Type: workflow.NodeTypeTransform, Entrypoint: true,
		Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "x", Operation: "set", Value: 1}}}}}
	var edges []workflow.Edge
	prev := "entry"
	for _, s := range spawns {
		nodes = append(nodes, s)
		edges = append(edges, workflow.Edge{Source: prev, Target: s.ID})
		prev = s.ID
	}
	return &workflow.Workflow{Name: "parent", Nodes: nodes, Edges: edges}
}

func spnFind(errs []workflow.ValidationError, code, nodeID string) *workflow.ValidationError {
	for i := range errs {
		if errs[i].Code == code && errs[i].NodeID == nodeID {
			return &errs[i]
		}
	}
	return nil
}

func spnCount(errs []workflow.ValidationError, code string) int {
	n := 0
	for _, e := range errs {
		if e.Code == code {
			n++
		}
	}
	return n
}

func spnValidate(wf *workflow.Workflow, opts ...workflow.ValidateOption) []workflow.ValidationError {
	return workflow.Validate(wf, nil, nil, nil, opts...)
}

// ---------- happy path ----------

func TestValidateSpawnInputs_FullyMappedValid_NoFindings(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t", "scope": "{{entry_scope}}", "count": 2}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	for _, e := range errs {
		assert.False(t, strings.HasPrefix(e.Code, "SPAWN_"), "unexpected %s: %s", e.Code, e.Message)
	}
}

func TestValidateSpawnInputs_PreparedInputsAdvisory(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t"}, "chat-p"))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	require.Equal(t, 1, spnCount(errs, "SPAWN_PREPARED_INPUTS"))
	f := spnFind(errs, "SPAWN_PREPARED_INPUTS", "sp")
	require.NotNil(t, f)
	assert.Equal(t, "warning", f.Severity)
	assert.Contains(t, f.Message, "scope")
	assert.NotContains(t, f.Message, "depth", "a required input with a default is not prepared")
	assert.Zero(t, spnCount(errs, "SPAWN_PREPARER_PROFILE_MISSING"))
}

// ---------- error / failure ----------

func TestValidateSpawnInputs_ReservedInput(t *testing.T) {
	wf := spnWF(spnNode("sp", "child", map[string]any{"workspace": "/x"}, ""))
	errs := spnValidate(wf) // no reader: reserved check needs none
	f := spnFind(errs, "SPAWN_RESERVED_INPUT", "sp")
	require.NotNil(t, f)
	assert.Contains(t, f.Message, "workspace")
	assert.Empty(t, f.Severity)
}

func TestValidateSpawnInputs_UnknownKey(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t", "scope": "s", "tpoic": "x"}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	f := spnFind(errs, "SPAWN_UNKNOWN_CHILD_INPUT", "sp")
	require.NotNil(t, f)
	assert.Contains(t, f.Message, "tpoic")
	assert.Contains(t, f.Message, "declared: ")
}

func TestValidateSpawnInputs_LiteralTypeMismatch(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t", "scope": "s", "flag": "yes"}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	f := spnFind(errs, "SPAWN_INPUT_TYPE_MISMATCH", "sp")
	require.NotNil(t, f)
	assert.Contains(t, f.Message, "flag")
}

func TestValidateSpawnInputs_NullLiteralForRequired(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": nil, "scope": "s"}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	f := spnFind(errs, "SPAWN_INPUT_TYPE_MISMATCH", "sp")
	require.NotNil(t, f)
	assert.Contains(t, f.Message, "topic")
}

func TestValidateSpawnInputs_PreparerProfileMissing(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t"}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	f := spnFind(errs, "SPAWN_PREPARER_PROFILE_MISSING", "sp")
	require.NotNil(t, f)
	assert.Empty(t, f.Severity)
	assert.Contains(t, f.Message, "scope")
}

func TestValidateSpawnInputs_ContractUnavailableIsWarning(t *testing.T) {
	r := newSpnReader()
	r.fail["child"] = errors.New("store down")
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t"}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	f := spnFind(errs, "SPAWN_CHILD_CONTRACT_UNAVAILABLE", "sp")
	require.NotNil(t, f)
	assert.Equal(t, "warning", f.Severity)
	assert.Contains(t, f.Message, "store down")
	assert.Zero(t, spnCount(errs, "UNKNOWN_CHILD_WORKFLOW"))
}

// ---------- boundary ----------

func TestValidateSpawnInputs_WholeTemplateNotTypeChecked(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t", "scope": "s", "count": "{{n}}", "flag": "{{f}}"}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	assert.Zero(t, spnCount(errs, "SPAWN_INPUT_TYPE_MISMATCH"))
}

func TestValidateSpawnInputs_InterpolatedStringIsStringLiteral(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(spnNode("sp", "child", map[string]any{"topic": "t", "scope": "s", "count": "x-{{a}}"}, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	f := spnFind(errs, "SPAWN_INPUT_TYPE_MISMATCH", "sp")
	require.NotNil(t, f)
	assert.Contains(t, f.Message, "count")
}

func TestValidateSpawnInputs_RequiredWithDefaultNoAdvisory(t *testing.T) {
	r := newSpnReader()
	r.inputs["child"] = []workflow.InputField{{Name: "depth", Type: "number", Required: true, Default: float64(3)}}
	wf := spnWF(spnNode("sp", "child", nil, ""))
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	assert.Zero(t, spnCount(errs, "SPAWN_PREPARED_INPUTS"))
	assert.Zero(t, spnCount(errs, "SPAWN_PREPARER_PROFILE_MISSING"))
}

func TestValidateSpawnInputs_UnknownChildReportedOnce_WithAndWithoutNameChecker(t *testing.T) {
	wf := spnWF(spnNode("sp", "ghost", map[string]any{"a": 1}, ""))

	r := newSpnReader()
	errs := spnValidate(wf, workflow.WithChildContractReader(r))
	assert.Equal(t, 1, spnCount(errs, "UNKNOWN_CHILD_WORKFLOW"), "reader only")

	r = newSpnReader()
	errs = spnValidate(wf, workflow.WithChildContractReader(r), workflow.WithWorkflowChecker(grpNames{}))
	assert.Equal(t, 1, spnCount(errs, "UNKNOWN_CHILD_WORKFLOW"), "reader and name checker")
}

// ---------- edge ----------

func TestValidateSpawnInputs_ChildLoadedOncePerCall(t *testing.T) {
	r := newSpnReader()
	wf := spnWF(
		spnNode("sp1", "child", map[string]any{"topic": "t", "scope": "s"}, ""),
		spnNode("sp2", "child", map[string]any{"topic": "t", "scope": "s"}, ""),
	)
	spnValidate(wf, workflow.WithChildContractReader(r))
	assert.Equal(t, 1, r.calls["child"])
}

func TestValidateSpawnInputs_NoReaderSkipsContractChecks(t *testing.T) {
	wf := spnWF(spnNode("sp", "child", map[string]any{"workspace": "x", "bogus": "y"}, ""))
	errs := spnValidate(wf)
	for _, e := range errs {
		if strings.HasPrefix(e.Code, "SPAWN_") {
			assert.Equal(t, "SPAWN_RESERVED_INPUT", e.Code)
		}
	}
	assert.NotNil(t, spnFind(errs, "SPAWN_RESERVED_INPUT", "sp"))
}

// ---------- integration ----------

// Regression pin (may be green on arrival): spawn codes are not group codes,
// so they never gate group writes.
func TestIsGroupValidationCode_FalseForSpawnCodes(t *testing.T) {
	for _, c := range []string{
		"SPAWN_RESERVED_INPUT", "SPAWN_UNKNOWN_CHILD_INPUT", "SPAWN_INPUT_TYPE_MISMATCH",
		"SPAWN_PREPARER_PROFILE_MISSING", "SPAWN_PREPARED_INPUTS", "SPAWN_CHILD_CONTRACT_UNAVAILABLE",
		"INVALID_LIB_PATH",
	} {
		assert.False(t, workflow.IsGroupValidationCode(c), c)
	}
}
