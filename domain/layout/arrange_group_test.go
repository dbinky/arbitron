package layout

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parallelGroupWorkflow is a branches-form group as #76 writes it: task edges
// to each branch entry, then the group's success and failure continuations.
func parallelGroupWorkflow() *workflow.Workflow {
	return &workflow.Workflow{
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeTypeScript, Entrypoint: true},
			{ID: "fan", Type: workflow.NodeTypeParallelGroup, Label: "Fan out", Config: workflow.NodeConfig{Group: &workflow.GroupConfig{
				Form:               workflow.GroupFormBranches,
				Tasks:              []workflow.GroupTask{{Key: "a", Entry: "a1"}, {Key: "b", Entry: "b1"}},
				CompletionRule:     workflow.GroupRuleAllSucceed,
				UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
				GatherMode:         workflow.GroupGatherResults,
			}}},
			{ID: "a1", Type: workflow.NodeTypeAgentCall},
			{ID: "b1", Type: workflow.NodeTypeAgentCall},
			{ID: "done", Type: workflow.NodeTypeTransform},
			{ID: "oops", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "fan"},
			{Source: "fan", Target: "a1", Output: workflow.GroupPortTask, Label: "task:a"},
			{Source: "fan", Target: "b1", Output: workflow.GroupPortTask, Label: "task:b"},
			{Source: "fan", Target: "done", Output: "success"},
			{Source: "fan", Target: "oops", Output: "failure"},
		},
	}
}

// Group geometry must keep the actual header and complete body below it.
func TestArrange_ParallelGroupIsMeasuredGeometry(t *testing.T) {
	g, err := BuildGraph(parallelGroupWorkflow())
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())
	require.Len(t, geo.Nodes, 6)
	require.Len(t, geo.GroupElements, 5)
	require.Len(t, geo.GroupLanes, 2)
	require.Len(t, geo.GroupConnectors, 2)
	fan := boxOf(geo, "fan")
	assert.Greater(t, fan.Y, boxOf(geo, "start").Bottom(), "the group follows its predecessor")
	for _, id := range []string{"a1", "b1", "done", "oops"} {
		assert.Greater(t, boxOf(geo, id).Y, fan.Bottom(), "%s is below the group", id)
	}
	assertNoNodeOverlap(t, geo)
	assertNoRouteThroughUnrelatedNodes(t, geo)
}

func TestArrangeGroups_DownwardAndComplete(t *testing.T) {
	for _, tc := range []struct {
		name, form string
		nested     bool
	}{{"branches", "branches", false}, {"workflows", "workflows", false}, {"nested", "branches", true}, {"loop", "branches", false}, {"parallel", "branches", false}, {"closed loop", "branches", false}, {"nested branches", "branches", true}, {"disjoint", "branches", false}, {"self loops", "branches", false}} {
		t.Run(tc.name, func(t *testing.T) {
			wf := capturedGroupWorkflow(tc.form, tc.nested)
			wf.Nodes[1].Label = strings.Repeat("完整 Unicode ✅ label ", 12)
			if tc.name == "nested branches" {
				inner := capturedGroupWorkflow("branches", false)
				wf.Nodes[4].Config = inner.Nodes[1].Config
				for i := range wf.Nodes[4].Config.Group.Tasks {
					wf.Nodes[4].Config.Group.Tasks[i].Entry = "nested-" + wf.Nodes[4].Config.Group.Tasks[i].Entry
				}
				wf.Nodes = append(wf.Nodes, workflow.Node{ID: "nested-a"}, workflow.Node{ID: "nested-b"})
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "nested-a", Output: "task"}, workflow.Edge{Source: "a", Target: "nested-b", Output: "task"})
			}
			if tc.name == "disjoint" {
				other := capturedGroupWorkflow("workflows", false)
				for _, n := range other.Nodes {
					n.ID = "disjoint-" + n.ID
					n.Entrypoint = false
					wf.Nodes = append(wf.Nodes, n)
				}
				for _, e := range other.Edges {
					e.Source = "disjoint-" + e.Source
					e.Target = "disjoint-" + e.Target
					wf.Edges = append(wf.Edges, e)
				}
			}
			if tc.name == "self loops" {
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "a", Label: "Repeat this task with a complete long label"}, workflow.Edge{Source: "a", Target: "a", Label: "Second repeated task with its complete long label"})
			}
			if tc.name == "loop" || tc.name == "parallel" || tc.name == "closed loop" {
				wf.Nodes = append(wf.Nodes, workflow.Node{ID: "a2"}, workflow.Node{ID: "a3"})
				wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "a2"}, workflow.Edge{Source: "a2", Target: "a3"})
				if tc.name == "parallel" {
					wf.Edges = append(wf.Edges, workflow.Edge{Source: "a", Target: "a2"})
				} else {
					wf.Edges = append(wf.Edges, workflow.Edge{Source: "a2", Target: "a"})
				}
				if tc.name == "closed loop" {
					wf.Edges = wf.Edges[:len(wf.Edges)-2]
					wf.Edges = append(wf.Edges, workflow.Edge{Source: "a2", Target: "a"})
				}
			}
			before, err := json.Marshal(wf)
			require.NoError(t, err)
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			m := measured(t, g)
			geo, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
			require.NoError(t, err)
			require.NoError(t, geo.Validate(g))
			if tc.name == "closed loop" {
				for _, e := range geo.GroupConnectors {
					assert.NotEqual(t, "second", e.TaskKey, "a closed loop has no invented gather exit")
				}
			}
			require.Empty(t, blockingOnly(Preflight(g, geo, m)))
			again, err := Arrange(context.Background(), g, m, DefaultContract(), DefaultOptions())
			require.NoError(t, err)
			assert.Equal(t, geo, again)
			after, err := json.Marshal(wf)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			for _, gid := range g.Groups.Order {
				footer := elementOf(t, geo, GroupElementID(gid, "footer", ""))
				for _, l := range geo.GroupLanes {
					if l.GroupNodeID == gid {
						require.Greater(t, footer.Box.Y, l.Box.Bottom())
					}
				}
			}
			for i, e := range geo.Edges {
				assert.Equal(t, i, e.Ordinal)
				assert.Equal(t, wf.Edges[i].Source, e.Source)
				assert.Equal(t, wf.Edges[i].Target, e.Target)
			}
		})
	}
}

func TestArrangeGroups_AuthorOrderNeverWraps(t *testing.T) {
	wf := capturedGroupWorkflow("workflows", false)
	wf.Nodes[1].Config.Group.Tasks = nil
	for i := 0; i < 12; i++ {
		wf.Nodes[1].Config.Group.Tasks = append(wf.Nodes[1].Config.Group.Tasks, workflow.GroupTask{Key: fmt.Sprintf("task-%02d", i), Workflow: "重复する子 ✅"})
	}
	g, err := BuildGraph(wf)
	require.NoError(t, err)
	m := measured(t, g)
	geo := arrangeFor(t, g, DefaultOptions())
	require.Len(t, geo.GroupLanes, 12)
	for i, l := range geo.GroupLanes {
		assert.Equal(t, wf.Nodes[1].Config.Group.Tasks[i].Key, l.TaskKey)
		assert.Equal(t, geo.GroupLanes[0].Box.Y, l.Box.Y)
		if i > 0 {
			assert.Greater(t, l.Box.X, geo.GroupLanes[i-1].Box.Right())
		}
	}
	require.Empty(t, blockingOnly(Preflight(g, geo, m)))
}

func TestPreflightGroups_RejectsIncompleteAndCollidingGeometry(t *testing.T) {
	g, err := BuildGraph(crossingGroupWorkflow())
	require.NoError(t, err)
	m := measured(t, g)
	geo := arrangeFor(t, g, DefaultOptions())
	require.NotEmpty(t, geo.GroupCrossings)
	raw, err := json.Marshal(geo)
	require.NoError(t, err)
	for _, tc := range []struct {
		name      string
		criterion Criterion
		mutate    func(*Geometry)
	}{
		{"footer", CriterionCoverage, func(b *Geometry) {
			for i, e := range b.GroupElements {
				if e.Role == "footer" {
					b.GroupElements = append(b.GroupElements[:i], b.GroupElements[i+1:]...)
					return
				}
			}
		}},
		{"owner", CriterionCoverage, func(b *Geometry) { b.GroupLanes[0].NodeIDs = nil }},
		{"connector", CriterionCoverage, func(b *Geometry) { b.GroupConnectors = b.GroupConnectors[1:] }},
		{"mixed crossing", CriterionCoverage, func(b *Geometry) { b.GroupCrossings = nil }},
		{"sibling card", CriterionEdgeThroughNode, func(b *Geometry) {
			p := &b.GroupConnectors[0]
			target := elementOf(t, *b, GroupElementID("fan", "lane", "first"))
			x := target.Texts[0].Box.CenterX()
			y := target.Texts[0].Box.CenterY()
			a, z := p.Points[0], p.Points[len(p.Points)-1]
			p.Points = simplify([]Point{a, {a.X, y}, {x, y}, {x, z.Y - 20}, {z.X, z.Y - 20}, z})
			b.GroupCrossings = findGroupCrossings(*b)
		}},
		{"detached label", CriterionIndistinctBranch, func(b *Geometry) { b.Edges[1].Text.Box.Y = b.Canvas.H - 30 }},
		{"marker canvas", CriterionCoverage, func(b *Geometry) {
			e := &b.Edges[1]
			for i := range b.Nodes {
				if b.Nodes[i].ID == e.Target {
					b.Nodes[i].Box.X = 0
				}
			}
			a, z := e.Points[0], e.Points[len(e.Points)-1]
			z.X = 2
			e.TargetAttach.Point = z
			e.Points = simplify([]Point{a, {a.X, z.Y - 30}, {z.X, z.Y - 30}, z})
			b.Crossings = findCrossings(b.Edges)
			b.GroupCrossings = findGroupCrossings(*b)
		}},
		{"port direction", CriterionArrowDirection, func(b *Geometry) {
			e := &b.Edges[0]
			a, z := e.Points[0], e.Points[len(e.Points)-1]
			e.Points = simplify([]Point{a, {a.X + 20, a.Y}, {a.X + 20, a.Y + 20}, {z.X, a.Y + 20}, z})
			b.GroupCrossings = findGroupCrossings(*b)
		}},
		{"shared port", CriterionAmbiguousAttachment, func(b *Geometry) {
			p := &b.GroupConnectors[1]
			q := b.GroupConnectors[0]
			last := len(p.Points) - 1
			p.Points[last].X = q.TargetAttach.Point.X
			p.TargetAttach.Point = p.Points[last]
			p.Points[last-1].X = p.Points[last].X
			p.Points = simplify(p.Points)
			b.GroupCrossings = findGroupCrossings(*b)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b Geometry
			require.NoError(t, json.Unmarshal(raw, &b))
			tc.mutate(&b)
			fs := Preflight(g, b, m)
			require.NotEmpty(t, fs)
			found := false
			for _, f := range fs {
				if f.Criterion == tc.criterion && f.Severity == SeverityBlocking {
					found = true
					require.NotEmpty(t, f.NodeIDs)
					for _, id := range f.NodeIDs {
						require.Contains(t, g.Groups.Groups, id)
					}
				}
			}
			require.True(t, found, "%+v", fs)
		})
	}
	t.Run("card", func(t *testing.T) {
		g, err := BuildGraph(capturedGroupWorkflow("workflows", false))
		require.NoError(t, err)
		geo := arrangeFor(t, g, DefaultOptions())
		for i, e := range geo.GroupElements {
			if e.Role == "task" {
				geo.GroupElements = append(geo.GroupElements[:i], geo.GroupElements[i+1:]...)
				break
			}
		}
		fs := Preflight(g, geo, measured(t, g))
		require.NotEmpty(t, fs)
		require.Equal(t, CriterionCoverage, fs[0].Criterion)
	})
	// Findings must alter the actual grouped candidate, not only ordinary options.
	next, changed := Refine(geo.Options, []Finding{{Criterion: CriterionTextCollision, Severity: SeverityBlocking}})
	require.True(t, changed)
	refined, err := Arrange(context.Background(), g, m, geo.Contract, next)
	require.NoError(t, err)
	assert.NotEqual(t, geo.GroupLanes, refined.GroupLanes)
	assert.Greater(t, refined.GroupLanes[1].Box.X-refined.GroupLanes[0].Box.Right(), geo.GroupLanes[1].Box.X-geo.GroupLanes[0].Box.Right())
}

func blockingOnly(fs []Finding) []Finding {
	out := []Finding{}
	for _, f := range fs {
		if f.Severity == SeverityBlocking {
			out = append(out, f)
		}
	}
	return out
}

func elementOf(t *testing.T, geo Geometry, id string) GroupElement {
	t.Helper()
	for _, e := range geo.GroupElements {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("missing element %s", id)
	return GroupElement{}
}

// SUCCESS — the group's caption is its node type, like every other node.
func TestBuildGraph_ParallelGroupCaption(t *testing.T) {
	g, err := BuildGraph(parallelGroupWorkflow())
	require.NoError(t, err)
	assert.Equal(t, GraphNode{ID: "fan", Text: "Fan out", TypeCaption: "parallel group"}, g.Nodes[1])
}

// EDGE — the group's four outgoing edges keep distinct labelled routes.
func TestArrange_ParallelGroupOutputsKeepTheirLabels(t *testing.T) {
	g, err := BuildGraph(parallelGroupWorkflow())
	require.NoError(t, err)
	geo := arrangeFor(t, g, DefaultOptions())
	labels := map[string]bool{}
	for _, e := range geo.Edges {
		if e.Text != nil {
			labels[e.Text.Text] = true
		}
	}
	for _, want := range []string{"task:a", "task:b", "success", "failure"} {
		assert.True(t, labels[want], "edge label %q is drawn", want)
	}
}
