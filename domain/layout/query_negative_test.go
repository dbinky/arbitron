package layout_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestQueryService_NilCollaboratorsFailClosed(t *testing.T) {
	h := newQueryHarness(t)
	wf := queryStamped("wf", queryRev1, workflow.ScopeLabelPrivate, "a")
	scope := workflow.Private(queryOwner)
	h.workflows.readable["wf"] = queryAccessEntry{wf, scope}
	h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
	id := uuid.New()
	h.instances.viewable[id], h.instances.retryable[id] = wf, wf
	reader := layout.NewReader(h.store, nil, nil)
	requester := layout.NewRequester(h.store, 3, nil)
	gate := &queryMutationGate{t: t, access: h.workflows}
	var absentWorkflows *fakeWorkflowAccess
	var absentInstances *fakeInstanceAccess
	for name, q := range map[string]*layout.QueryService{
		"nil service":     nil,
		"reader":          layout.NewQueryService(nil, requester, h.workflows, h.instances, gate),
		"requester":       layout.NewQueryService(reader, nil, h.workflows, h.instances, gate),
		"workflows":       layout.NewQueryService(reader, requester, nil, h.instances, gate),
		"instances":       layout.NewQueryService(reader, requester, h.workflows, nil, gate),
		"mutations":       layout.NewQueryService(reader, requester, h.workflows, h.instances, nil),
		"typed workflows": layout.NewQueryService(reader, requester, absentWorkflows, h.instances, gate),
		"typed instances": layout.NewQueryService(reader, requester, h.workflows, absentInstances, gate),
		"reader store":    layout.NewQueryService(layout.NewReader(nil, nil, nil), requester, h.workflows, h.instances, gate),
		"requester store": layout.NewQueryService(reader, layout.NewRequester(nil, 3, nil), h.workflows, h.instances, gate),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotPanics(t, func() {
				// Missing retry-only collaborators need not disable reads.
				_, e1 := q.WorkflowLayout(context.Background(), "wf")
				_, e2 := q.InstanceLayout(context.Background(), id)
				_, e3 := q.RetryWorkflow(context.Background(), "wf", queryRev1)
				_, e4 := q.RetryInstance(context.Background(), id, queryRev1)
				if name == "nil service" || name == "reader" || name == "reader store" {
					assert.Error(t, e1)
					assert.Error(t, e2)
				}
				if name == "workflows" || name == "typed workflows" {
					assert.Error(t, e1)
					assert.Error(t, e3)
				}
				if name == "instances" || name == "typed instances" {
					assert.Error(t, e2)
					assert.Error(t, e4)
				}
				if name == "requester" || name == "requester store" {
					assert.Error(t, e3)
					assert.Error(t, e4)
				}
				if name == "mutations" {
					assert.Error(t, e3)
					assert.ErrorIs(t, e4, layout.ErrStoreUnavailable, "a historical insert must not run unlocked")
				}
			})
		})
	}
}

func TestQueryService_NilOrMalformedAccessibleDefinition(t *testing.T) {
	for name, wf := range map[string]*workflow.Workflow{
		"nil":      nil,
		"empty":    {},
		"dangling": {Nodes: []workflow.Node{{ID: "a"}}, Edges: []workflow.Edge{{Source: "a", Target: "missing"}}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newQueryHarness(t)
			scope := workflow.Private(queryOwner)
			h.workflows.readable["wf"] = queryAccessEntry{wf, scope}
			h.workflows.writable["wf"] = queryAccessEntry{wf, scope}
			id := uuid.New()
			h.instances.viewable[id], h.instances.retryable[id] = wf, wf
			_, err := h.q.WorkflowLayout(context.Background(), "wf")
			require.Error(t, err)
			_, err = h.q.InstanceLayout(context.Background(), id)
			require.Error(t, err)
			_, err = h.q.RetryWorkflow(context.Background(), "wf", queryRev1)
			require.Error(t, err)
			_, err = h.q.RetryInstance(context.Background(), id, queryRev1)
			require.Error(t, err)
			_, err = h.store.Get(context.Background(), queryRev1)
			assert.ErrorIs(t, err, layout.ErrRecordNotFound)
		})
	}
}
