package layout_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryService_RetryWorkflow_InvalidResolutionTargetsPreserveRow(t *testing.T) {
	private := workflow.Private(queryOwner)
	for name, targets := range map[string][]workflow.MutationTarget{
		"missing":     nil,
		"empty":       {},
		"wrong name":  {{Scope: private, Name: "other"}},
		"wrong scope": {{Scope: workflow.Shared(), Name: "wf"}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newQueryHarness(t)
			wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
			h.workflows.writable["wf"] = queryAccessEntry{wf, private}
			h.workflows.lockTargets["wf"] = targets
			h.completeFailed(t, wf, private)
			before := h.get(t, queryRev1)
			_, err := h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
			require.Error(t, err, "invalid resolution input must fail before mutating a generation")
			assert.Equal(t, before, h.get(t, queryRev1))
		})
	}
}

// The callback context is authority to re-enter the repository under its lock,
// not just tracing metadata. The success assertion depends on the second access
// accepting that exact marker and on the Store accepting the generation.
func TestQueryService_RetryWorkflow_CoordinatorContextReachesGeneration(t *testing.T) {
	h := newQueryHarness(t)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a", "b")
	scope := workflow.Private(queryOwner)
	h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
	h.completeFailed(t, wf, scope)
	before := h.get(t, queryRev1)
	gate := &queryMutationGate{t: t, access: h.workflows}
	q := layout.NewQueryService(layout.NewReader(h.store, nil, nil), layout.NewRequester(h.store, 3, nil), h.workflows, h.instances, gate)
	ack, err := q.RetryWorkflow(context.Background(), "wf", queryRev1)
	require.NoError(t, err)
	after := h.get(t, queryRev1)
	assert.Equal(t, before.JobGeneration+1, after.JobGeneration)
	assert.Equal(t, after.JobGeneration, ack.JobGeneration)
	assert.Equal(t, layout.StatePending, after.State)
	assert.Equal(t, before.Manifest, after.Manifest)
	assert.Equal(t, [][]workflow.MutationTarget{{
		{Scope: scope, Name: "wf"}, {Scope: workflow.Shared(), Name: "wf"},
	}}, gate.workflowTargets)
}
