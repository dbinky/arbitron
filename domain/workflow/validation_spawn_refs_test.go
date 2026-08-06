package workflow_test

import (
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
)

// stubConnChecker resolves one alias to one connection ref and answers
// existence from a fixed set.
type stubConnChecker struct {
	refs    map[string]string
	present map[string]bool
}

func (c stubConnChecker) ProfileConnectionRef(alias string) (string, bool) {
	ref, ok := c.refs[alias]
	return ref, ok
}
func (c stubConnChecker) ConnectionExists(name string) bool { return c.present[name] }

// stubProfileChecker reports every listed alias as chat-kind.
type stubProfileChecker map[string]string

func (c stubProfileChecker) ProfileKind(alias string) (string, bool) {
	k, ok := c[alias]
	return k, ok
}

func spawnRefWorkflow(childWorkflow, profileAlias string) *workflow.Workflow {
	return &workflow.Workflow{
		Name: "parent",
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeTypeTransform, Entrypoint: true,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "x", Operation: "set", Value: 1}}}},
			{ID: "spawn-1", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{
				ChildWorkflow: childWorkflow,
				SpawnMode:     workflow.SpawnModeFireAndForget,
				ModelProfile:  profileAlias,
			}},
			{ID: "done", Type: workflow.NodeTypeTransform,
				Config: workflow.NodeConfig{Operations: []workflow.Operation{{Variable: "y", Operation: "set", Value: 1}}}},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "spawn-1"},
			{Source: "spawn-1", Target: "done"},
		},
	}
}

func codesOf(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

// THE GAP. A spawn node's model_profile drives a real provider call (the
// child-input preparation), so a profile naming a deleted connection is exactly
// as broken here as on an llm_call. Before this, spawn_workflow was absent from
// the connection traversal, so such a workflow passed publish and failed only at
// run time — after the parent had already burned a queue slot and reached the
// spawn node.
func TestValidate_SpawnNodeReportsUnknownConnection(t *testing.T) {
	wf := spawnRefWorkflow("child", "prep")
	conn := stubConnChecker{
		refs:    map[string]string{"prep": "azure-prod"},
		present: map[string]bool{}, // the connection was deleted
	}

	errs := workflow.Validate(wf, nil, stubProfileChecker{"prep": "chat"}, conn)

	assert.Contains(t, codesOf(errs), "UNKNOWN_CONNECTION",
		"a spawn node's profile with an absent connection must fail at publish, not at run time")
}

// The no-key subscription path (empty ConnectionRef) stays valid with no
// existence check, exactly as it does for llm_call.
func TestValidate_SpawnNodeEmptyConnectionRefIsValid(t *testing.T) {
	wf := spawnRefWorkflow("child", "prep")
	conn := stubConnChecker{refs: map[string]string{"prep": ""}, present: map[string]bool{}}

	assert.NotContains(t, codesOf(workflow.Validate(wf, nil, stubProfileChecker{"prep": "chat"}, conn)),
		"UNKNOWN_CONNECTION")
}

// A spawn node's model_profile is legitimately OPTIONAL: a child declaring zero
// inputs never makes a preparation call, and validation cannot load the child to
// know. It must NOT become a validation error — the requirement is enforced at
// run time, named by node, out of ChildInputPreparer.Prepare.
func TestValidate_SpawnNodeMissingModelProfileIsNotAValidationError(t *testing.T) {
	wf := spawnRefWorkflow("child", "")
	conn := stubConnChecker{refs: map[string]string{}, present: map[string]bool{}}

	codes := codesOf(workflow.Validate(wf, nil, stubProfileChecker{}, conn))
	assert.NotContains(t, codes, "UNKNOWN_CONNECTION")
	assert.NotContains(t, codes, "MISSING_REQUIRED_CONFIG")
	assert.NotContains(t, codes, "UNKNOWN_PROFILE")
}

// Author-time existence check for the childWorkflow reference.
func TestValidate_SpawnNodeReportsUnknownChildWorkflow(t *testing.T) {
	wf := spawnRefWorkflow("does-not-exist", "")

	errs := workflow.Validate(wf, nil, nil, nil,
		workflow.WithWorkflowChecker(workflow.NameSetChecker{"child": true}))

	assert.Contains(t, codesOf(errs), "UNKNOWN_CHILD_WORKFLOW")
}

func TestValidate_SpawnNodeKnownChildWorkflowPasses(t *testing.T) {
	wf := spawnRefWorkflow("child", "")

	errs := workflow.Validate(wf, nil, nil, nil,
		workflow.WithWorkflowChecker(workflow.NameSetChecker{"child": true}))

	assert.Empty(t, errs)
}

// With no checker wired the pass is skipped entirely — the same nil-skippable
// contract every other checker has.
func TestValidate_NoWorkflowCheckerSkipsTheChildWorkflowPass(t *testing.T) {
	wf := spawnRefWorkflow("does-not-exist", "")

	assert.NotContains(t, codesOf(workflow.Validate(wf, nil, nil, nil)), "UNKNOWN_CHILD_WORKFLOW")
}
