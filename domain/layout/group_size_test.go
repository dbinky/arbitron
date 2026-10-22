package layout

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGroupSizing_ReservesCombinedLiveRows(t *testing.T) {
	for _, form := range []string{"branches", "workflows"} {
		t.Run(form, func(t *testing.T) {
			wf := capturedGroupWorkflow(form, false)
			// Task keys are identity: long but valid ASCII ([A-Za-z0-9_-]+), the
			// only form ParseGroupConfig accepts. Unicode lives in display strings
			// (amended 2026-10-21): workflow titles, branch node labels and the
			// task-port labels drawn on each lane.
			longKey := strings.Repeat("long_configured_key_", 20)
			wf.Nodes[1].Config.Group.Tasks[0].Key = longKey
			unicodeTitle := strings.Repeat("同じ子 ✅ ワークフロー · ", 6)
			for i := range wf.Nodes[1].Config.Group.Tasks {
				if wf.Nodes[1].Config.Group.Tasks[i].Workflow != "" {
					wf.Nodes[1].Config.Group.Tasks[i].Workflow = unicodeTitle + wf.Nodes[1].Config.Group.Tasks[i].Key
				}
			}
			for i := range wf.Nodes {
				if wf.Nodes[i].ID == "a" || wf.Nodes[i].ID == "b" {
					wf.Nodes[i].Label = "分岐 " + wf.Nodes[i].ID + " — długa etykieta ✨ " + strings.Repeat("ü", 40)
				}
			}
			for i := range wf.Edges {
				if wf.Edges[i].Output == workflow.GroupPortTask {
					wf.Edges[i].Label = "タスク · Ünïcødé port label 🚀"
				}
			}
			g, err := BuildGraph(wf)
			require.NoError(t, err, "long ASCII keys with Unicode display strings must reach the real producer")
			m, c := measured(t, g), DefaultContract()

			// The vocabulary must offer every row the live overlay can show: each
			// progress count 0..N for every kind, a winner row per task key (the
			// long key included) and, for workflow cards, the Unicode title and the
			// redacted title.
			cfg := g.Groups.Groups["fan"].Config
			keys := map[string]TextItem{}
			for _, item := range groupTextItems(g, c) {
				keys[item.Key] = item
			}
			footerID := GroupElementID("fan", "footer", "")
			for n := 0; n <= len(cfg.Tasks); n++ {
				for _, kind := range []string{"done", "active", "not started", "failed"} {
					require.Contains(t, keys, GroupTextKey(footerID, fmt.Sprintf("overlay:progress:%s:%d", kind, n)), "count %d of %s", n, kind)
				}
			}
			for _, task := range cfg.Tasks {
				require.Contains(t, keys, GroupTextKey(footerID, "overlay:winner:"+task.Key))
				if form == workflow.GroupFormWorkflows {
					card := GroupElementID("fan", "task", task.Key)
					require.Equal(t, unicodeTitle+task.Key, keys[GroupTextKey(card, "title")].Text, "the Unicode workflow title is measured verbatim")
					require.Contains(t, keys, GroupTextKey(card, "redactedTitle"))
				}
			}
			if form == workflow.GroupFormBranches {
				// Unicode branch labels are measured as real node text in the same request.
				req := MeasureRequestFor(g, c)
				texts := map[string]string{}
				for _, item := range req.Items {
					texts[item.Key] = item.Text
				}
				require.Contains(t, texts[NodeLabelKey("a")], "分岐 a")
			}
			for i, item := range groupTextItems(g, c) {
				var key []string
				require.NoError(t, json.Unmarshal([]byte(item.Key), &key))
				if strings.HasPrefix(key[2], "overlay:") || key[2] == "redactedTitle" {
					lines := 1 + i%4
					px := c.NodeTypeFontPx
					if item.Style == StyleEdgeLabel {
						px = c.EdgeLabelFontPx
					}
					if item.Style == StyleNodeLabel {
						px = c.NodeLabelFontPx
					}
					m.Items[item.Key] = TextMetrics{Width: c.NodeTextMaxWidth - float64(i%17), Height: float64(lines) * px * c.LineHeight, Lines: lines}
				}
			}
			before, err := json.Marshal(m)
			require.NoError(t, err)
			geo, err := Arrange(context.Background(), g, m, c, DefaultOptions())
			require.NoError(t, err)
			require.NotEmpty(t, geo.GroupElements, "measured group blocks must reach actual geometry")
			for _, e := range geo.GroupElements {
				if e.Role != "footer" && e.Role != "task" && e.Role != "lane" {
					continue
				}
				require.NotNil(t, e.OverlayBox)
				// Sum every concurrent row from deliberately different actual metrics;
				// alternatives include all counts, long valid task keys, winners and states.
				prefixes := []string{"overlay:status:", "overlay:frozen:", "overlay:badge:winner", "overlay:badge:non-blocking", "overlay:badge:cancel requested"}
				if e.Role == "footer" {
					prefixes = []string{"overlay:status:", "overlay:progress:done:", "overlay:progress:active:", "overlay:progress:not started:", "overlay:progress:failed:", "overlay:winner:"}
				}
				h, w := float64(len(prefixes)-1)*c.NodeTextGapPx, 0.0
				for _, prefix := range prefixes {
					rowH := 0.0
					for key, tm := range m.Items {
						var parts []string
						if json.Unmarshal([]byte(key), &parts) != nil || len(parts) != 3 || parts[1] != e.ID || !strings.HasPrefix(parts[2], prefix) {
							continue
						}
						rowH = math.Max(rowH, math.Ceil(tm.Height)+1)
						w = math.Max(w, math.Ceil(tm.Width)+1)
					}
					require.Positive(t, rowH)
					h += rowH
				}
				require.Equal(t, h, e.OverlayBox.H)
				require.Equal(t, w, e.OverlayBox.W)
				if e.Role == "task" {
					redacted := m.Items[GroupTextKey(e.ID, "redactedTitle")]
					for _, text := range e.Texts {
						if text.Key == GroupTextKey(e.ID, "title") {
							require.GreaterOrEqual(t, text.Box.H, math.Ceil(redacted.Height)+1)
						}
					}
				}
				require.True(t, e.Box.Contains(*e.OverlayBox))
			}
			after, err := json.Marshal(m)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			for _, item := range groupTextItems(g, c) {
				t.Run(item.Key, func(t *testing.T) {
					for _, bad := range []TextMetrics{{}, {Width: math.NaN(), Height: 1, Lines: 1}, {Width: 10, Height: math.Inf(1), Lines: 1}} {
						copy := m
						copy.Items = maps.Clone(m.Items)
						copy.Items[item.Key] = bad
						_, err := Arrange(context.Background(), g, copy, c, DefaultOptions())
						require.Error(t, err)
					}
					copy := m
					copy.Items = maps.Clone(m.Items)
					delete(copy.Items, item.Key)
					_, err := Arrange(context.Background(), g, copy, c, DefaultOptions())
					require.Error(t, err)
				})
			}
		})
	}
}

// Header supplements share the real NodeBox with the original node label/type.
// Reserving their measured blocks must affect the actual Arrange output, not
// only a measurement request or an unused group-specific sizing helper.
func TestArrange_GroupHeaderReservesMeasuredSupplements(t *testing.T) {
	for _, tc := range []struct {
		name, form string
		nested     bool
	}{{"branches", workflow.GroupFormBranches, false}, {"workflows", workflow.GroupFormWorkflows, false}, {"nested", workflow.GroupFormBranches, true}} {
		t.Run(tc.name, func(t *testing.T) {
			wf := capturedGroupWorkflow(tc.form, tc.nested)
			before, err := json.Marshal(wf)
			require.NoError(t, err)
			g, err := BuildGraph(wf)
			require.NoError(t, err)
			m, c, o := measured(t, g), DefaultContract(), DefaultOptions()
			for _, id := range g.Groups.Order {
				header := GroupElementID(id, "header", "")
				m.Items[GroupTextKey(header, "fanout")] = TextMetrics{Width: c.NodeTextMaxWidth, Height: 3 * c.NodeTypeFontPx * c.LineHeight, Lines: 3}
				m.Items[GroupTextKey(header, "metadata")] = TextMetrics{Width: c.NodeTextMaxWidth - 10, Height: 4 * c.EdgeLabelFontPx * c.LineHeight, Lines: 4}
			}
			geo, err := Arrange(context.Background(), g, m, c, o)
			require.NoError(t, err)
			require.NoError(t, geo.Validate(g))
			for _, id := range g.Groups.Order {
				label, caption := m.Items[NodeLabelKey(id)], m.Items[NodeTypeKey(id)]
				fanout := m.Items[GroupTextKey(GroupElementID(id, "header", ""), "fanout")]
				metadata := m.Items[GroupTextKey(GroupElementID(id, "header", ""), "metadata")]
				// Each measured block receives the same rounding slack as existing
				// node text, with one gap between each adjacent block.
				wantH := math.Ceil(2*c.NodeBorderPx + c.NodeTypeBarPx + 2*c.NodePaddingY +
					ceilPx(label.Height) + ceilPx(caption.Height) + ceilPx(fanout.Height) + ceilPx(metadata.Height) +
					3*c.NodeTextGapPx + o.NodeTextSlack)
				box := boxOf(geo, id)
				assert.Equal(t, wantH, box.H, "all four blocks must fit inside header %s", id)
				assert.GreaterOrEqual(t, box.W, math.Ceil(ceilPx(fanout.Width)+2*c.NodePaddingX+2*c.NodeBorderPx+o.NodeTextSlack))
			}
			assertNoNodeOverlap(t, geo)
			assertNoRouteThroughUnrelatedNodes(t, geo)
			after, err := json.Marshal(wf)
			require.NoError(t, err)
			assert.Equal(t, before, after)

			// Positive sensitivity: shrinking the actual measured blocks must
			// shrink the actual header. A constant reservation cannot pass.
			for _, id := range g.Groups.Order {
				header := GroupElementID(id, "header", "")
				m.Items[GroupTextKey(header, "fanout")] = TextMetrics{Width: 40, Height: c.NodeTypeFontPx * c.LineHeight, Lines: 1}
				m.Items[GroupTextKey(header, "metadata")] = TextMetrics{Width: 80, Height: c.EdgeLabelFontPx * c.LineHeight, Lines: 1}
			}
			compact, err := Arrange(context.Background(), g, m, c, o)
			require.NoError(t, err)
			for _, id := range g.Groups.Order {
				assert.Less(t, boxOf(compact, id).H, boxOf(geo, id).H)
			}
		})
	}
}

func TestPreflight_GroupHeaderRequiresMeasuredSupplementClearance(t *testing.T) {
	g, err := BuildGraph(capturedGroupWorkflow(workflow.GroupFormBranches, true))
	require.NoError(t, err)
	c := DefaultContract()
	for _, tc := range []string{"missing", "height", "width"} {
		t.Run(tc, func(t *testing.T) {
			m := measured(t, g)
			geo, err := Arrange(context.Background(), g, m, c, DefaultOptions())
			require.NoError(t, err)
			for _, finding := range Preflight(g, geo, m) {
				assert.NotEqual(t, CriterionTextClipped, finding.Criterion, "the complete measured reservation must fit before corruption")
				assert.NotEqual(t, CriterionCoverage, finding.Criterion, "all original header measurements are present")
			}
			key := GroupTextKey(GroupElementID("a", "header", ""), "metadata")
			tm := m.Items[key]
			switch tc {
			case "missing":
				delete(m.Items, key)
			case "height":
				tm.Lines += 10
				tm.Height = float64(tm.Lines) * c.EdgeLabelFontPx * c.LineHeight
				m.Items[key] = tm
			case "width":
				tm.Width = c.NodeTextMaxWidth
				m.Items[key] = tm
			}
			var found bool
			for _, finding := range Preflight(g, geo, m) {
				if finding.Severity == SeverityBlocking &&
					(finding.Criterion == CriterionTextClipped || finding.Criterion == CriterionCoverage) &&
					len(finding.NodeIDs) == 1 && finding.NodeIDs[0] == "a" {
					found = true
				}
			}
			assert.True(t, found, "preflight must check the nested header's complete measured text, not only its primary label/type")
		})
	}
}

func TestArrange_GroupHeaderRejectsMissingOrCorruptSupplements(t *testing.T) {
	for _, field := range []string{"fanout", "metadata"} {
		for _, edit := range []string{"missing", "zero", "wrong line height", "too wide"} {
			t.Run(field+"/"+edit, func(t *testing.T) {
				g, err := BuildGraph(capturedGroupWorkflow(workflow.GroupFormBranches, true))
				require.NoError(t, err)
				m, c := measured(t, g), DefaultContract()
				key := GroupTextKey(GroupElementID("a", "header", ""), field)
				tm := m.Items[key]
				switch edit {
				case "missing":
					delete(m.Items, key)
				case "zero":
					m.Items[key] = TextMetrics{}
				case "wrong line height":
					tm.Height++
					m.Items[key] = tm
				case "too wide":
					tm.Width = c.NodeTextMaxWidth + 1
					m.Items[key] = tm
				}
				geo, err := Arrange(context.Background(), g, m, c, DefaultOptions())
				require.Error(t, err, "a required nested header block cannot be ignored")
				assert.Contains(t, err.Error(), key)
				assert.Equal(t, Geometry{}, geo)
			})
		}
	}
}

// A failed lookup must never use Go's zero map value to resize another node.
// Arrange already rejects a damaged index; pin the sizing boundary as well.
func TestGroupHeader_MissingNodeIndexCannotResizeFirstNode(t *testing.T) {
	for _, missing := range []string{"group index", "nil index"} {
		t.Run(missing, func(t *testing.T) {
			g, err := BuildGraph(capturedGroupWorkflow(workflow.GroupFormWorkflows, false))
			require.NoError(t, err)
			m, c, o := measured(t, g), DefaultContract(), DefaultOptions()
			sizes := make([]nodeSize, len(g.Nodes))
			for i := range sizes {
				sizes[i] = nodeSize{W: 200, H: 100, TextLines: 1}
			}
			before := append([]nodeSize(nil), sizes...)
			if missing == "nil index" {
				g.index = nil
			} else {
				delete(g.index, "fan")
			}
			err = reserveGroupHeaderText(g, m, c, o, sizes)
			assert.ErrorIs(t, err, ErrInvalidGroups)
			assert.Equal(t, before, sizes, "missing group must not resize the ordinary first node")
			geo, err := Arrange(context.Background(), g, m, c, o)
			assert.ErrorIs(t, err, ErrInvalidGeometry)
			assert.Equal(t, Geometry{}, geo, "invalid input must not publish a candidate")
		})
	}
}
