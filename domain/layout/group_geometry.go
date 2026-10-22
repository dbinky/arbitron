package layout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// GroupText is one static, measured text block in a group element.
// All boxes use absolute canvas coordinates, including texts and overlays.
type GroupText struct {
	Key   string    `json:"key"`
	Text  string    `json:"text"`
	Style TextStyle `json:"style"`
	Box   Rect      `json:"box"`
	Lines int       `json:"lines"`
}

// GroupElement decorates a real group header or adds presentation-only content.
// It never becomes a node in the workflow's execution graph.
type GroupElement struct {
	ID          string      `json:"id"`
	GroupNodeID string      `json:"groupNodeId"`
	TaskKey     string      `json:"taskKey,omitempty"`
	Role        string      `json:"role"`
	Box         Rect        `json:"box"`
	Texts       []GroupText `json:"texts"`
	OverlayBox  *Rect       `json:"overlayBox,omitempty"`
}

// GroupLane records immediate ownership in manifest order, not descendant nodes.
type GroupLane struct {
	GroupNodeID string   `json:"groupNodeId"`
	TaskKey     string   `json:"taskKey"`
	Box         Rect     `json:"box"`
	NodeIDs     []string `json:"nodeIds"`
}

// GroupConnector is a routed presentation link, distinct from manifest edges.
type GroupConnector struct {
	ID              string     `json:"id"`
	GroupNodeID     string     `json:"groupNodeId"`
	TaskKey         string     `json:"taskKey"`
	Kind            string     `json:"kind"`
	SourceElementID string     `json:"sourceElementId"`
	TargetElementID string     `json:"targetElementId"`
	Points          []Point    `json:"points"`
	SourceAttach    Attachment `json:"sourceAttach"`
	TargetAttach    Attachment `json:"targetAttach"`
}

// GroupCrossing addresses an intersection with at least one connector. Real
// routes use ManifestRouteKey; presentation routes use their connector ID.
type GroupCrossing struct {
	Point Point  `json:"point"`
	Over  string `json:"over"`
	Under string `json:"under"`
}

// GroupElementID identifies a rendered group element without delimiter collisions.
// The real group header shares the ordinary node's element identity.
func GroupElementID(groupID, role, taskKey string) string {
	if role == "header" && taskKey == "" {
		return groupIDJSON("node", groupID)
	}
	return groupIDJSON("group", groupID, role, taskKey)
}

// GroupTextKey identifies one measured text field of an element.
func GroupTextKey(elementID, field string) string { return groupIDJSON("text", elementID, field) }

// ManifestRouteKey identifies a real edge occurrence in mixed route crossings.
func ManifestRouteKey(ordinal int) string { return "edge:" + strconv.Itoa(ordinal) }

func groupConnectorID(groupID, kind, taskKey, sourceID, targetID string) string {
	return groupIDJSON("connector", groupID, kind, taskKey, sourceID, targetID)
}

// CrossingCount includes real/real, real/connector and connector/connector gaps.
func CrossingCount(geo Geometry) int { return len(geo.Crossings) + len(geo.GroupCrossings) }

// Match JSON.stringify's unescaped HTML characters for cross-language IDs.
func groupIDJSON(parts ...string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	// String arrays contain no values JSON cannot encode.
	_ = enc.Encode(parts)
	encoded := bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
	var out strings.Builder
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '\\' && i+1 < len(encoded) {
			// encoding/json always escapes these two runes, even with
			// EscapeHTML disabled. JSON.stringify leaves them literal.
			if i+6 <= len(encoded) && (string(encoded[i:i+6]) == `\u2028` || string(encoded[i:i+6]) == `\u2029`) {
				if encoded[i+5] == '8' {
					out.WriteRune('\u2028')
				} else {
					out.WriteRune('\u2029')
				}
				i += 5
			} else {
				out.Write(encoded[i : i+2])
				i++
			}
		} else {
			out.WriteByte(encoded[i])
		}
	}
	return out.String()
}

// cloneGroupGraph isolates each external request from the captured graph and
// from the other ports. ParseGroupConfig preserves typed nested task inputs.
func cloneGroupGraph(g *workflow.GroupGraph) (*workflow.GroupGraph, error) {
	if g == nil {
		return nil, nil
	}
	out := &workflow.GroupGraph{Groups: make(map[string]*workflow.GroupScope, len(g.Groups)), Owner: maps.Clone(g.Owner), Order: slices.Clone(g.Order)}
	for id, scope := range g.Groups {
		cfg, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: &scope.Config})
		if err != nil {
			return nil, fmt.Errorf("%w: node %q: %w", ErrInvalidGroups, id, err)
		}
		copy := *scope
		copy.Config = cfg
		copy.Bodies = make(map[string][]string, len(scope.Bodies))
		for key, ids := range scope.Bodies {
			copy.Bodies[key] = slices.Clone(ids)
		}
		copy.Nested = slices.Clone(scope.Nested)
		if scope.Parent != nil {
			parent := *scope.Parent
			copy.Parent = &parent
		}
		out.Groups[id] = &copy
	}
	return out, nil
}

func groupDisplayState(state string) string { return strings.ReplaceAll(state, "_", " ") }

// groupHeaderTextItems is shared by measurement requests and header sizing so
// the actual requested strings, styles and wrap widths remain identical.
func groupHeaderTextItems(id string, count int, form string, c RendererContract) []TextItem {
	header := GroupElementID(id, "header", "")
	tasks := "tasks"
	if count == 1 {
		tasks = "task"
	}
	return []TextItem{
		{Key: GroupTextKey(header, "fanout"), Text: "fan-out", Style: StyleNodeType, MaxWidth: c.NodeTextMaxWidth},
		{Key: GroupTextKey(header, "metadata"), Text: fmt.Sprintf("%d %s · %s", count, tasks, form), Style: StyleEdgeLabel, MaxWidth: c.NodeTextMaxWidth},
	}
}

// groupTextItems owns the complete deterministic group measurement vocabulary.
// Overlay alternatives are measurement inputs, not simultaneous static labels.
func groupTextItems(g Graph, c RendererContract) []TextItem {
	if g.Groups == nil {
		return nil
	}
	var items []TextItem
	add := func(element, field, text string, style TextStyle) {
		items = append(items, TextItem{Key: GroupTextKey(element, field), Text: text, Style: style, MaxWidth: c.NodeTextMaxWidth})
	}
	for _, id := range g.Groups.Order {
		scope := g.Groups.Groups[id]
		cfg := scope.Config
		footer := GroupElementID(id, "footer", "")
		form := "Parallel branches"
		if cfg.Form == workflow.GroupFormWorkflows {
			form = "Parallel workflows"
		}
		rule, captionRule := "All succeed", "all succeed"
		if cfg.CompletionRule == workflow.GroupRuleFirstSuccess {
			rule, captionRule = "First success", "first success"
		}
		add(GroupElementID(id, "container", ""), "caption", form+" · "+captionRule, StyleEdgeLabel)
		items = append(items, groupHeaderTextItems(id, len(cfg.Tasks), cfg.Form, c)...)
		add(footer, "title", "gather · "+rule, StyleNodeLabel)
		gather := "results"
		if cfg.GatherMode == workflow.GroupGatherResultsAndFiles {
			gather = "results + files"
		}
		add(footer, "metadata", groupDisplayState(cfg.UnfinishedBehavior)+" · "+gather, StyleEdgeLabel)
		for _, state := range []string{"not_started", "launching", "running", "decided", "gathering", "resolving_conflicts", "done", "unknown"} {
			add(footer, "overlay:status:"+state, groupDisplayState(state), StyleNodeType)
		}
		for n := 0; n <= len(cfg.Tasks); n++ {
			add(footer, fmt.Sprintf("overlay:progress:done:%d", n), fmt.Sprintf("%d/%d done", n, len(cfg.Tasks)), StyleEdgeLabel)
			for _, kind := range []string{"active", "not started", "failed"} {
				add(footer, fmt.Sprintf("overlay:progress:%s:%d", kind, n), fmt.Sprintf("%d %s", n, kind), StyleEdgeLabel)
			}
		}
		for i, task := range cfg.Tasks {
			role := "lane"
			if cfg.Form == workflow.GroupFormWorkflows {
				role = "task"
			}
			element := GroupElementID(id, role, task.Key)
			add(element, "caption", fmt.Sprintf("task %d · %s", i+1, task.Key), StyleNodeType)
			if cfg.Form == workflow.GroupFormWorkflows {
				add(element, "title", task.Workflow, StyleNodeLabel)
				add(element, "redactedTitle", "hidden dependency", StyleNodeLabel)
			}
			add(footer, "overlay:winner:"+task.Key, "winner: "+task.Key, StyleNodeType)
			for _, state := range []string{"not_started", "queued", "running", "waiting", "paused", "completed", "failed", "cancelled", "unfinished", "unknown"} {
				add(element, "overlay:status:"+state, groupDisplayState(state), StyleNodeType)
				if state != "unknown" {
					add(element, "overlay:frozen:"+state, "gathered as "+groupDisplayState(state), StyleEdgeLabel)
				}
			}
			for _, badge := range []string{"winner", "non-blocking", "cancel requested", "hidden dependency"} {
				add(element, "overlay:badge:"+badge, badge, StyleNodeType)
			}
		}
	}
	return items
}
