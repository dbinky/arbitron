package layout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupGeometry_CanonicalIDs(t *testing.T) {
	assert.Equal(t, `["node","group:<界>\"\\"]`, GroupElementID("group:<界>\"\\", "header", ""))
	assert.Equal(t, `["group","g","task","k"]`, GroupElementID("g", "task", "k"))
	assert.Equal(t, `["text","[\"group\",\"g\",\"task\",\"k\"]","title"]`, GroupTextKey(GroupElementID("g", "task", "k"), "title"))
	assert.Equal(t, "edge:17", ManifestRouteKey(17))
	assert.Equal(t, "[\"node\",\"a\u2028b\u2029c\"]", GroupElementID("a\u2028b\u2029c", "header", ""), "JSON.stringify preserves line separator runes")
	assert.Equal(t, `["node","literal\\u2028"]`, GroupElementID(`literal\u2028`, "header", ""), "a literal escape must not become a separator")
	assert.NotEqual(t, GroupElementID("g:task", "task", "k"), GroupElementID("g", "task", "task:k"))
}

func TestMeasureRequest_GroupTextMatchesSharedGolden(t *testing.T) {
	var cases []struct{ Form, CompletionRule, UnfinishedBehavior, GatherMode, Workflow, Caption, Rule, Unfinished, Gather, FooterNotStarted string }
	raw, err := os.ReadFile(filepath.Join("..", "..", "dashboard", "src", "graph", "arranged", "groupText.contract.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &cases))
	for _, tc := range cases {
		t.Run(tc.Form+"/"+tc.CompletionRule+"/"+tc.UnfinishedBehavior, func(t *testing.T) {
			wf := capturedGroupWorkflow(tc.Form, false)
			cfg := wf.Nodes[1].Config.Group
			cfg.CompletionRule = tc.CompletionRule
			cfg.UnfinishedBehavior = tc.UnfinishedBehavior
			cfg.GatherMode = tc.GatherMode
			if cfg.GatherMode == workflow.GroupGatherResultsAndFiles {
				cfg.ResolverProfile = "resolver"
			}
			if cfg.Form == workflow.GroupFormWorkflows {
				for i := range cfg.Tasks {
					cfg.Tasks[i].Workflow = tc.Workflow
				}
			}
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			c := DefaultContract()
			req := MeasureRequestFor(g, c)
			items := map[string]TextItem{}
			for _, item := range req.Items {
				require.NotContains(t, items, item.Key, "measurement IDs must be unique")
				items[item.Key] = item
			}
			want := func(role, key, field, text string, style TextStyle) {
				t.Helper()
				id := GroupTextKey(GroupElementID("fan", role, key), field)
				require.Contains(t, items, id, "group text must reach the real measurement request")
				assert.Equal(t, TextItem{Key: id, Text: text, Style: style, MaxWidth: c.NodeTextMaxWidth}, items[id])
			}
			want("container", "", "caption", tc.Caption, StyleEdgeLabel)
			want("header", "", "fanout", "fan-out", StyleNodeType)
			want("header", "", "metadata", "2 tasks · "+tc.Form, StyleEdgeLabel)
			want("footer", "", "title", "gather · "+tc.Rule, StyleNodeLabel)
			want("footer", "", "overlay:status:not_started", tc.FooterNotStarted, StyleNodeType)
			want("footer", "", "metadata", tc.Unfinished+" · "+tc.Gather, StyleEdgeLabel)
			role := "lane"
			if tc.Form == workflow.GroupFormWorkflows {
				role = "task"
			}
			want(role, "second", "caption", "task 1 · second", StyleNodeType)
			want(role, "first", "caption", "task 2 · first", StyleNodeType)
			if tc.Form == workflow.GroupFormWorkflows {
				want(role, "second", "title", tc.Workflow, StyleNodeLabel)
				want(role, "first", "title", tc.Workflow, StyleNodeLabel)
			}
			want("footer", "", "overlay:winner:second", "winner: second", StyleNodeType)
			want("footer", "", "overlay:winner:first", "winner: first", StyleNodeType)
			for _, state := range []string{"not_started", "queued", "running", "waiting", "paused", "completed", "failed", "cancelled", "unfinished", "unknown"} {
				want(role, "second", "overlay:status:"+state, strings.ReplaceAll(state, "_", " "), StyleNodeType)
				if state != "unknown" {
					want(role, "second", "overlay:frozen:"+state, "gathered as "+strings.ReplaceAll(state, "_", " "), StyleEdgeLabel)
				}
			}
			for _, state := range []string{"launching", "running", "decided", "gathering", "resolving_conflicts", "done", "unknown"} {
				want("footer", "", "overlay:status:"+state, strings.ReplaceAll(state, "_", " "), StyleNodeType)
			}
			for _, badge := range []string{"winner", "non-blocking", "cancel requested", "hidden dependency"} {
				want(role, "second", "overlay:badge:"+badge, badge, StyleNodeType)
			}
			// Ordinary text is still measured exactly once; supplemental text does not
			// substitute for the primary real header label/type.
			require.Contains(t, items, NodeLabelKey("fan"))
			require.Contains(t, items, NodeTypeKey("fan"))
			g.Groups = nil
			noGroups := MeasureRequestFor(g, c)
			for _, item := range noGroups.Items {
				assert.NotContains(t, item.Key, `["text"`, "missing group input cannot produce the expected group measurement keys")
			}
		})
	}
}

func TestMeasureRequest_NestedGroupsKeepTheirOwnTextIdentities(t *testing.T) {
	g, err := BuildGraph(capturedGroupWorkflow(workflow.GroupFormBranches, true))
	require.NoError(t, err)
	req := MeasureRequestFor(g, DefaultContract())
	got := map[string]string{}
	for _, item := range req.Items {
		require.NotContains(t, got, item.Key)
		got[item.Key] = item.Text
	}
	assert.Equal(t, "Parallel branches · all succeed", got[GroupTextKey(GroupElementID("fan", "container", ""), "caption")])
	assert.Equal(t, "Parallel workflows · all succeed", got[GroupTextKey(GroupElementID("a", "container", ""), "caption")])
}
