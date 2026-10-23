package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enricherProfileResolver resolves aliases from a map, or fails every call
// when err is set.
type enricherProfileResolver struct {
	bindings map[string]provider.ResolvedBinding
	err      error
}

func (r enricherProfileResolver) Resolve(_ context.Context, alias string) (provider.ResolvedBinding, error) {
	if r.err != nil {
		return provider.ResolvedBinding{}, r.err
	}
	b, ok := r.bindings[alias]
	if !ok {
		return provider.ResolvedBinding{}, provider.ErrUnknownProfile
	}
	return b, nil
}

var codexProfile = enricherProfileResolver{bindings: map[string]provider.ResolvedBinding{
	"coder": {Model: "gpt-6.1-sol", Effort: "high", Backend: "codex", Provider: "litellm"},
}}

// bindingWorkflow has an agent node, a script node, and a gitVariable.
func bindingWorkflow(gitVariable string) workflow.Workflow {
	return workflow.Workflow{
		Name:        "pr-review",
		GitVariable: gitVariable,
		Nodes: []workflow.Node{
			{ID: "review", Label: "Review", Type: workflow.NodeTypeAgentCall, Config: workflow.NodeConfig{ModelProfile: "coder"}},
			{ID: "init", Label: "Init", Type: workflow.NodeTypeScript},
		},
	}
}

func enrichOne(t *testing.T, r provider.ProfileResolver, s instance.InstanceSummary) instance.EnrichedInstanceSummary {
	t.Helper()
	d := newEnricherDeps()
	enr := engine.NewInstanceEnricher(d.instRepo, d.nodeExecRepo, d.userRepo, engine.NoopQueuePositionProvider{}, "claude-code")
	if r != nil {
		enr.SetProfileResolver(r)
	}
	got, err := enr.Enrich(context.Background(), []instance.InstanceSummary{s})
	require.NoError(t, err)
	require.Len(t, got, 1)
	return got[0]
}

func summaryAt(t *testing.T, state instance.State, current, failed string, vars map[string]any, gitVariable string) instance.InstanceSummary {
	s := instance.InstanceSummary{
		ID: uuid.New(), WorkflowName: "pr-review", OwnerID: uuid.New(), State: state,
		CreatedAt: time.Now(), Variables: vars, WorkflowSnapshot: snapshotMap(t, bindingWorkflow(gitVariable)),
	}
	if current != "" {
		s.CurrentNodeID = &current
	}
	if failed != "" {
		s.FailedNodeID = &failed
	}
	return s
}

func TestEnrich_RunningAgentNodeBindingFromProfile(t *testing.T) {
	row := enrichOne(t, codexProfile, summaryAt(t, instance.StateRunning, "review", "", nil, ""))
	assert.Equal(t, "Review", row.FriendlyStep)
	assert.Equal(t, "agent_call", row.NodeType)
	assert.Equal(t, "gpt-6.1-sol", row.Model)
	assert.Equal(t, "high", row.Effort)
	assert.Equal(t, "codex", row.Backend, "profile backend, not the deployment default")
	assert.Equal(t, "litellm", row.Provider)
}

func TestEnrich_RunningScriptNodeShowsTypeOnly(t *testing.T) {
	row := enrichOne(t, codexProfile, summaryAt(t, instance.StateRunning, "init", "", nil, ""))
	assert.Equal(t, "script", row.NodeType)
	assert.Empty(t, row.Model)
	assert.Empty(t, row.Backend)
	assert.Empty(t, row.Provider)
}

func TestEnrich_FailedRowUsesFailedNode(t *testing.T) {
	row := enrichOne(t, codexProfile, summaryAt(t, instance.StateFailed, "init", "review", nil, ""))
	assert.Equal(t, "agent_call", row.NodeType)
	assert.Equal(t, "gpt-6.1-sol", row.Model)
}

func TestEnrich_TerminalRowsHaveNoBinding(t *testing.T) {
	for _, st := range []instance.State{instance.StateCompleted, instance.StateCancelled} {
		row := enrichOne(t, codexProfile, summaryAt(t, st, "review", "", nil, ""))
		assert.Equal(t, "Review", row.FriendlyStep, "%s: step label unchanged", st)
		assert.Empty(t, row.NodeType, "%s", st)
		assert.Empty(t, row.Model, "%s", st)
		assert.Empty(t, row.Backend, "%s", st)
	}
}

func TestEnrich_QueuedRowEmptyBindingAndGit(t *testing.T) {
	row := enrichOne(t, codexProfile, summaryAt(t, instance.StateQueued, "", "", map[string]any{}, ""))
	assert.Empty(t, row.NodeType)
	assert.Empty(t, row.Model)
	assert.Empty(t, row.GitRef)
}

func TestEnrich_GitRefFromManifestVariable(t *testing.T) {
	row := enrichOne(t, codexProfile, summaryAt(t, instance.StateRunning, "review", "",
		map[string]any{"branch": "other", "pr_branch": "feat/a"}, "pr_branch"))
	assert.Equal(t, "feat/a", row.GitRef)
}

func TestEnrich_CompletedRowKeepsFinalBranch(t *testing.T) {
	row := enrichOne(t, codexProfile, summaryAt(t, instance.StateCompleted, "review", "",
		map[string]any{"branch": "feat/done"}, ""))
	assert.Equal(t, "feat/done", row.GitRef)
}

func TestEnrich_ResolverErrorDegradesOnlyBinding(t *testing.T) {
	s := summaryAt(t, instance.StateRunning, "review", "", map[string]any{"branch": "b"}, "")
	row := enrichOne(t, enricherProfileResolver{err: errors.New("db down")}, s)
	assert.Equal(t, "agent_call", row.NodeType)
	assert.Empty(t, row.Model)
	assert.Equal(t, "claude-code", row.Backend, "falls back to deployment default")
	assert.Equal(t, "b", row.GitRef)
	assert.NotEmpty(t, row.RunName)
}

func TestEnrich_NoResolverWiredUsesNoop(t *testing.T) {
	row := enrichOne(t, nil, summaryAt(t, instance.StateRunning, "review", "", nil, ""))
	assert.Equal(t, "agent_call", row.NodeType)
	assert.Empty(t, row.Model)
}

func TestSetProfileResolver_NilPanics(t *testing.T) {
	d := newEnricherDeps()
	assert.Panics(t, func() { d.enricher().SetProfileResolver(nil) })
}

// countingResolver counts Resolve calls per alias.
type countingResolver struct {
	enricherProfileResolver
	calls map[string]int
}

func (c *countingResolver) Resolve(ctx context.Context, alias string) (provider.ResolvedBinding, error) {
	c.calls[alias]++
	return c.enricherProfileResolver.Resolve(ctx, alias)
}

func TestEnrich_ResolvesEachProfileOncePerPage(t *testing.T) {
	for _, tc := range []struct {
		name string
		base enricherProfileResolver
	}{
		{"resolvable profile", codexProfile},
		{"failing profile", enricherProfileResolver{err: errors.New("connection gone")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &countingResolver{enricherProfileResolver: tc.base, calls: map[string]int{}}
			d := newEnricherDeps()
			enr := engine.NewInstanceEnricher(d.instRepo, d.nodeExecRepo, d.userRepo, engine.NoopQueuePositionProvider{}, "claude-code")
			enr.SetProfileResolver(r)
			page := []instance.InstanceSummary{
				summaryAt(t, instance.StateRunning, "review", "", nil, ""),
				summaryAt(t, instance.StateFailed, "init", "review", nil, ""),
				summaryAt(t, instance.StateRunning, "review", "", nil, ""),
			}
			got, err := enr.Enrich(context.Background(), page)
			require.NoError(t, err)
			require.Len(t, got, 3)
			assert.Equal(t, 1, r.calls["coder"], "one Resolve per alias per page, not per row")
			for _, row := range got {
				assert.Equal(t, "agent_call", row.NodeType)
			}
		})
	}
}

// pinnedSummary is a running row on the "coder" agent node with the given pin
// record.
func pinnedSummary(t *testing.T, version int, pins, routes map[string]string) instance.InstanceSummary {
	s := summaryAt(t, instance.StateRunning, "review", "", nil, "")
	s.ModelPinsVersion, s.ModelPins, s.ModelPinRoutes = version, pins, routes
	return s
}

// A pinned row lists the model its run executes, not the live profile, while
// effort, backend and provider stay live (G23). A legacy row lists the live
// model; a missing or unreadable pin never falls back to it.
func TestEnrich_PinnedRowShowsPinnedModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		summary instance.InstanceSummary
		model   string
	}{
		{"snapshot pin", pinnedSummary(t, instance.ModelPinsVersionSnapshot, map[string]string{"coder": "gpt-6.0-sol"}, nil), "gpt-6.0-sol"},
		{"legacy run", pinnedSummary(t, instance.ModelPinsVersionLegacy, nil, nil), "gpt-6.1-sol"},
		{"snapshot without the alias", pinnedSummary(t, instance.ModelPinsVersionSnapshot, map[string]string{}, nil), ""},
		{"unreadable pins", pinnedSummary(t, instance.ModelPinsVersionUnreadable, nil, nil), ""},
		{"invalid native route", pinnedSummary(t, instance.ModelPinsVersionSnapshot, map[string]string{"coder": "gpt-6.0-sol"}, map[string]string{"coder": "openai"}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := enrichOne(t, codexProfile, tc.summary)
			assert.Equal(t, "agent_call", row.NodeType)
			assert.Equal(t, tc.model, row.Model)
			if tc.model != "" {
				assert.Equal(t, "high", row.Effort)
				assert.Equal(t, "codex", row.Backend)
				assert.Equal(t, "litellm", row.Provider)
			}
		})
	}
}

// Overlaying pins keeps the page to one live Resolve per alias, and each row
// gets its own pin, not the first row's.
func TestEnrich_PinnedRowsShareOneLiveLookupPerAlias(t *testing.T) {
	r := &countingResolver{enricherProfileResolver: codexProfile, calls: map[string]int{}}
	d := newEnricherDeps()
	enr := engine.NewInstanceEnricher(d.instRepo, d.nodeExecRepo, d.userRepo, engine.NoopQueuePositionProvider{}, "claude-code")
	enr.SetProfileResolver(r)
	page := []instance.InstanceSummary{
		pinnedSummary(t, instance.ModelPinsVersionSnapshot, map[string]string{"coder": "gpt-5.9-sol"}, nil),
		pinnedSummary(t, instance.ModelPinsVersionSnapshot, map[string]string{"coder": "gpt-6.0-sol"}, nil),
		pinnedSummary(t, instance.ModelPinsVersionLegacy, nil, nil),
	}
	got, err := enr.Enrich(context.Background(), page)
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, 1, r.calls["coder"], "one live Resolve per alias per page")
	assert.Equal(t, []string{"gpt-5.9-sol", "gpt-6.0-sol", "gpt-6.1-sol"}, []string{got[0].Model, got[1].Model, got[2].Model})
}
