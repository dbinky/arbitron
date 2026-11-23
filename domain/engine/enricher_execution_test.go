package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeExecProvider answers only for the run IDs it was asked about.
type fakeExecProvider struct {
	byRun map[uuid.UUID][]instance.ExecutionStatus
	err   error
	asked []uuid.UUID
}

func (p *fakeExecProvider) ExecutionStatuses(_ context.Context, ids []uuid.UUID) (map[uuid.UUID][]instance.ExecutionStatus, error) {
	p.asked = append(p.asked, ids...)
	if p.err != nil {
		return nil, p.err
	}
	out := map[uuid.UUID][]instance.ExecutionStatus{}
	for _, id := range ids {
		if s, ok := p.byRun[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func TestEnricher_FillsExecutionFromProvider(t *testing.T) {
	d := newEnricherDeps()
	a, b := uuid.New(), uuid.New()
	status := instance.ExecutionStatus{RunID: a, NodeID: "draft", State: instance.ExecWaitingCapacity, Group: "anthropic_subscription:anthropic", Reason: "waiting · capacity"}
	prov := &fakeExecProvider{byRun: map[uuid.UUID][]instance.ExecutionStatus{a: {status}}}
	e := d.enricher()
	e.SetExecutionStatusProvider(prov)

	rows, err := e.Enrich(context.Background(), []instance.InstanceSummary{{ID: a, WorkflowName: "wf", State: instance.StateRunning}, {ID: b, WorkflowName: "wf", State: instance.StateRunning}})
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, []instance.ExecutionStatus{status}, rows[0].Execution)
	assert.Empty(t, rows[1].Execution)
	assert.ElementsMatch(t, []uuid.UUID{a, b}, prov.asked, "the provider is asked only about the authorized page")
	assert.Equal(t, instance.StateRunning, rows[0].State, "execution status never changes the run state (D6)")
}

func TestEnricher_ExecutionProviderFailureDegradesOnlyThatField(t *testing.T) {
	d := newEnricherDeps()
	e := d.enricher()
	e.SetExecutionStatusProvider(&fakeExecProvider{err: errors.New("db locked")})
	id := uuid.New()
	rows, err := e.Enrich(context.Background(), []instance.InstanceSummary{{ID: id, WorkflowName: "wf", State: instance.StateRunning}})
	require.NoError(t, err, "the list never fails")
	require.Len(t, rows, 1)
	assert.Empty(t, rows[0].Execution)
	assert.NotEmpty(t, rows[0].RunName, "other fields still render")
}

func TestEnricher_DefaultExecutionProviderIsNoop(t *testing.T) {
	rows, err := newEnricherDeps().enricher().Enrich(context.Background(), []instance.InstanceSummary{{ID: uuid.New(), WorkflowName: "wf"}})
	require.NoError(t, err)
	assert.Empty(t, rows[0].Execution)
	got, err := engine.NoopExecutionStatusProvider{}.ExecutionStatuses(context.Background(), []uuid.UUID{uuid.New()})
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Panics(t, func() { newEnricherDeps().enricher().SetExecutionStatusProvider(nil) })
}
