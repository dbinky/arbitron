// Package layout arranges saved workflow graphs top-to-bottom, checks the
// resulting geometry, and coordinates bounded arrange → render → review
// attempts. It is pure domain code: it imports no concrete adapters, and the
// only I/O it performs is through the Renderer and Reviewer ports.
package layout

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/arbitron/arbitron/domain/workflow"
)

// Typed errors for manifests that cannot be arranged. An invalid manifest stays
// saved; the arrangement pipeline reports the failure instead of panicking.
var (
	ErrEmptyGraph    = errors.New("layout: workflow has no nodes")
	ErrEmptyNodeID   = errors.New("layout: node has an empty id")
	ErrDuplicateNode = errors.New("layout: duplicate node id")
	ErrDanglingEdge  = errors.New("layout: edge references an unknown node")
	ErrInvalidGroups = errors.New("layout: invalid parallel group graph")
)

// GraphNode is the presentation-relevant view of one manifest node.
type GraphNode struct {
	// ID is the manifest node id.
	ID string
	// Text is the displayed primary text: Node.Label when non-empty, else ID.
	Text string
	// TypeCaption is the node type with underscores replaced by spaces.
	TypeCaption string
	// Entrypoint mirrors Node.Entrypoint.
	Entrypoint bool
}

// GraphEdge is one edge OCCURRENCE. Ordinal (the index in the manifest edge
// slice) is its identity; Source/Target/Output/Label are verification data.
type GraphEdge struct {
	Ordinal int
	Source  string
	Target  string
	Output  string
	Label   string
	// Text is the displayed branch text: Label when non-empty, else Output.
	// Empty means the edge has no branch label.
	Text string
}

// Graph is the immutable, normalised input to arrangement. Nodes keep manifest
// order; Edges keep manifest order with Edges[i].Ordinal == i.
type Graph struct {
	Nodes []GraphNode
	Edges []GraphEdge
	// Groups captures validated, deep-copied ownership/configs from the same
	// manifest; nil only when the manifest has no parallel groups.
	Groups *workflow.GroupGraph
	index  map[string]int
	// capturedGroups is independent of the exported map. Arrangement verifies
	// that ownership/configs still match the saved input, including task order.
	capturedGroups *workflow.GroupGraph
}

// BuildGraph normalises a saved manifest. It never mutates wf.
func BuildGraph(wf *workflow.Workflow) (Graph, error) {
	if wf == nil || len(wf.Nodes) == 0 {
		return Graph{}, ErrEmptyGraph
	}
	g := Graph{
		Nodes: make([]GraphNode, 0, len(wf.Nodes)),
		Edges: make([]GraphEdge, 0, len(wf.Edges)),
		index: make(map[string]int, len(wf.Nodes)),
	}
	for i, n := range wf.Nodes {
		if n.ID == "" {
			return Graph{}, fmt.Errorf("%w (node %d)", ErrEmptyNodeID, i)
		}
		if _, dup := g.index[n.ID]; dup {
			return Graph{}, fmt.Errorf("%w: %q", ErrDuplicateNode, n.ID)
		}
		g.index[n.ID] = i
		g.Nodes = append(g.Nodes, GraphNode{
			ID:          n.ID,
			Text:        displayText(n),
			TypeCaption: strings.ReplaceAll(string(n.Type), "_", " "),
			Entrypoint:  n.Entrypoint,
		})
	}
	for i, e := range wf.Edges {
		if _, ok := g.index[e.Source]; !ok {
			return Graph{}, fmt.Errorf("%w: edge %d source %q", ErrDanglingEdge, i, e.Source)
		}
		if _, ok := g.index[e.Target]; !ok {
			return Graph{}, fmt.Errorf("%w: edge %d target %q", ErrDanglingEdge, i, e.Target)
		}
		g.Edges = append(g.Edges, GraphEdge{
			Ordinal: i,
			Source:  e.Source,
			Target:  e.Target,
			Output:  e.Output,
			Label:   e.Label,
			Text:    branchText(e),
		})
	}
	if workflow.HasGroups(wf) {
		for _, n := range wf.Nodes {
			if n.Type == workflow.NodeTypeParallelGroup {
				if _, err := workflow.ParseGroupConfig(n.Config); err != nil {
					return Graph{}, fmt.Errorf("%w: node %q: %w", ErrInvalidGroups, n.ID, err)
				}
			}
		}
		groups, findings := workflow.AnalyzeGroups(wf)
		if len(findings) != 0 {
			return Graph{}, fmt.Errorf("%w: %v", ErrInvalidGroups, findings)
		}
		g.capturedGroups = groups
		var err error
		g.Groups, err = cloneGroupGraph(groups)
		if err != nil {
			return Graph{}, err
		}
	}
	return g, nil
}

func (g Graph) validateGroups() error {
	if !reflect.DeepEqual(g.Groups, g.capturedGroups) {
		return fmt.Errorf("%w: ownership/configs differ from captured manifest", ErrInvalidGroups)
	}
	return nil
}

// NodeIndex returns the manifest index of node id.
func (g Graph) NodeIndex(id string) (int, bool) {
	i, ok := g.index[id]
	return i, ok
}

func displayText(n workflow.Node) string {
	if n.Label != "" {
		return n.Label
	}
	return n.ID
}

func branchText(e workflow.Edge) string {
	if e.Label != "" {
		return e.Label
	}
	return e.Output
}
