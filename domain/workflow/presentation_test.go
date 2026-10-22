package workflow_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPresentationValues(t *testing.T) {
	owner := uuid.New()
	assert.Equal(t, workflow.Shared(), workflow.ScopeFromPresentation(&workflow.Presentation{Scope: "shared", OwnerID: owner}))
	assert.Equal(t, workflow.Private(owner), workflow.ScopeFromPresentation(&workflow.Presentation{Scope: "private", OwnerID: owner}))
	assert.Equal(t, workflow.Private(uuid.Nil), workflow.ScopeFromPresentation(nil))
	assert.Equal(t, "shared", workflow.ScopeLabel(workflow.Shared()))
	assert.Equal(t, "private", workflow.ScopeLabel(workflow.Private(owner)))
	var absent *workflow.Workflow
	assert.Empty(t, absent.SavedRevision())
	assert.Empty(t, (&workflow.Workflow{}).SavedRevision())
	p := &workflow.Presentation{Revision: uuid.Must(uuid.NewV7()).String(), SavedAt: time.Date(2026, 10, 16, 1, 2, 3, 4, time.UTC), Reason: workflow.SaveReasonUpdate, Scope: "private", OwnerID: owner, Name: "flow", Retained: workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: "old", FromGeneration: 2, Positions: map[string]workflow.NodePosition{"a": {X: 12, Y: 34}}}}
	wf := &workflow.Workflow{Presentation: p}
	assert.Equal(t, p.Revision, wf.SavedRevision())
	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.JSONEq(t, `{"revision":"`+p.Revision+`","savedAt":"2026-10-16T01:02:03.000000004Z","reason":"update","scope":"private","ownerId":"`+owner.String()+`","name":"flow","retained":{"source":"layout","fromRevision":"old","fromGeneration":2,"positions":{"a":{"x":12,"y":34}}}}`, string(b))
	b, err = json.Marshal(workflow.RetainedLayout{Source: workflow.RetainedNone})
	require.NoError(t, err)
	assert.JSONEq(t, `{"source":"none"}`, string(b))
	b, err = json.Marshal(wf)
	require.NoError(t, err)
	var decoded workflow.Workflow
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, p, decoded.Presentation)
	var legacy workflow.Workflow
	require.NoError(t, json.Unmarshal([]byte(`{"name":"legacy","nodes":[{"id":"a","type":"script"}],"edges":[]}`), &legacy))
	b, err = json.Marshal(legacy)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "presentation")
}

func TestPresentationServerTokens(t *testing.T) {
	s, j, _, _, _ := stamperFixture(t)
	wf := stampWorkflow()
	wf.Presentation = &workflow.Presentation{Revision: "client", Retained: workflow.RetainedLayout{Source: workflow.RetainedFromLayout, FromRevision: "forged", FromGeneration: 99, Positions: map[string]workflow.NodePosition{"a": {X: 999}}}}
	seen := map[string]bool{}
	for i, label := range []string{"A", "B", "A", "A", "A"} {
		wf.Nodes[0].Label = label
		if i == 4 {
			wf.Nodes[0].Position = &workflow.NodePosition{X: 42, Y: 17}
		}
		req, err := s.Stamp(context.Background(), workflow.Shared(), wf.Name, wf, nil, workflow.SaveReasonCreate)
		require.NoError(t, err)
		token, err := uuid.Parse(req.Revision)
		require.NoError(t, err)
		assert.Equal(t, uuid.Version(7), token.Version())
		assert.False(t, seen[req.Revision])
		seen[req.Revision] = true
		assert.NotEqual(t, "client", req.Revision)
		assert.NotEqual(t, "forged", req.Retained.FromRevision)
		assert.NotEqual(t, int64(99), req.Retained.FromGeneration)
		if i < 4 {
			assert.Equal(t, workflow.RetainedNone, req.Retained.Source)
		} else {
			assert.Equal(t, 42.0, req.Retained.Positions["a"].X)
		}
		var captured workflow.Workflow
		require.NoError(t, json.Unmarshal(j.entries[req.Revision].Request.Manifest, &captured))
		assert.Equal(t, wf, &captured)
	}
}
