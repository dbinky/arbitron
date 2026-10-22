# Workflow Auto-Arrangement — Phase 1 Task 1: Graph input normalisation

**Shared contracts:** [Cross-task contracts](2026-10-16-workflow-auto-arrangement-implementation-contracts.md), including §6 precedence, ownership, TDD and production completion gates.

**Current execution guidance (2026-10-19):** contracts §§6.18–6.19 reconcile the current
source and required completion work. Dated execution notes below are historical evidence;
recheck an unchecked gate against those contracts instead of skipping it for a producer
that now exists. P3T15–P3T20 are the required ordered group migration after the earlier phases;
contracts §§6.19–6.20 assign each producer, consumer and completion gate.
No box is checked by this docs review.

**Goal:** Turn one immutable saved `workflow.Workflow` into the normalised, read-only `layout.Graph` every later layout stage consumes: displayed node text, type captions, per-occurrence edge identity and branch text, plus typed errors for unusable manifests.

**Architecture:** New pure-Go package `domain/layout` (no adapter imports). `BuildGraph` copies only presentation-relevant data out of the manifest, so arrangement can never mutate execution behaviour. Edge identity is the ordinal in the manifest edge slice — `workflow.Edge` has no ID, and identical parallel edges must stay distinct.

**Tech Stack:** Go 1.25.5, stdlib `errors`/`fmt`/`strings`, testify `assert`/`require`; module `github.com/arbitron/arbitron`.

**Spec / Design:** `docs/specs/2026-10-16-workflow-auto-arrangement-spec.md`; `docs/designs/2026-10-16-workflow-auto-arrangement-design-phase-1.md` (§1 "Geometry and renderer contract": edge ordinal identity, displayed text rules; §Error handling).

---

### Task 1.1: `BuildGraph` and its typed errors

**Files:**
- Create: `domain/layout/graph.go`
- Test:   `domain/layout/graph_test.go`

**Interfaces:**
- Consumes: `workflow.Workflow`, `workflow.Node` (`ID`, `Type`, `Label`, `Entrypoint`), `workflow.Edge` (`Source`, `Target`, `Output`, `Label`) from `domain/workflow/workflow.go:160-246`.
- Produces (used by every later Phase 1 task, P2T5 `Reader`, P3T8 `QueryService`):
  ```go
  var ErrEmptyGraph, ErrEmptyNodeID, ErrDuplicateNode, ErrDanglingEdge error
  type GraphNode struct { ID, Text, TypeCaption string; Entrypoint bool }
  type GraphEdge struct { Ordinal int; Source, Target, Output, Label, Text string }
  type Graph struct { Nodes []GraphNode; Edges []GraphEdge /* + unexported index */ }
  func BuildGraph(wf *workflow.Workflow) (Graph, error)
  func (g Graph) NodeIndex(id string) (int, bool)
  ```

**Rules pinned by the design (copy exactly):**
- Displayed node text = `Node.Label` when non-empty, otherwise `Node.ID` (mirrors `dashboard/src/graph/useGraphLayout.ts:111` `n.label || n.id`).
- Type caption = `Node.Type` with every `_` replaced by a space (mirrors `dashboard/src/graph/nodes/ArbitronNode.tsx:61` `data.nodeType.replace(/_/g, ' ')`; uppercase is CSS-only).
- Branch text = `Edge.Label` when non-empty, otherwise `Edge.Output` (mirrors `useGraphLayout.ts:177` `e.label ?? e.output`, with Go's `omitempty` meaning an empty label is absent); `""` means the edge carries no label.
- Every manifest node gets one `GraphNode` (manifest order); every edge occurrence gets one `GraphEdge` whose `Ordinal` is its slice index — identical parallel edges are NOT collapsed.

- [x] **Step 1: Write the failing test**

Create `domain/layout/graph_test.go`:

```go
package layout

import (
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildGraph_DisplayTextAndTypeCaption(t *testing.T) {
	wf := &workflow.Workflow{
		Nodes: []workflow.Node{
			{ID: "init", Type: workflow.NodeTypeScript, Label: "Init workspace", Entrypoint: true},
			{ID: "plan-review", Type: workflow.NodeTypeAgentCall},
			{ID: "gate", Type: workflow.NodeTypeConditional, Label: ""},
		},
	}

	g, err := BuildGraph(wf)
	require.NoError(t, err)
	require.Len(t, g.Nodes, 3)

	assert.Equal(t, GraphNode{ID: "init", Text: "Init workspace", TypeCaption: "script", Entrypoint: true}, g.Nodes[0])
	assert.Equal(t, "plan-review", g.Nodes[1].Text, "empty label falls back to the node id")
	assert.Equal(t, "agent call", g.Nodes[1].TypeCaption, "underscores become spaces like the dashboard caption")
	assert.Equal(t, "gate", g.Nodes[2].Text)
	assert.Equal(t, "conditional", g.Nodes[2].TypeCaption)
	assert.False(t, g.Nodes[2].Entrypoint)
}

func TestBuildGraph_EdgeOrdinalsAndBranchText(t *testing.T) {
	wf := &workflow.Workflow{
		Nodes: []workflow.Node{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Edges: []workflow.Edge{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "c", Output: "pass"},
			{Source: "b", Target: "a", Output: "fail", Label: "back-edge: retry"},
			{Source: "b", Target: "c", Output: "pass"}, // identical parallel edge
		},
	}

	g, err := BuildGraph(wf)
	require.NoError(t, err)
	require.Len(t, g.Edges, 4, "every edge occurrence is kept, including identical parallels")

	for i, e := range g.Edges {
		assert.Equal(t, i, e.Ordinal, "ordinal is the manifest slice index")
	}
	assert.Equal(t, "", g.Edges[0].Text, "no label and no output means no branch text")
	assert.Equal(t, "pass", g.Edges[1].Text, "output is used when label is empty")
	assert.Equal(t, "back-edge: retry", g.Edges[2].Text, "label wins over output")
	assert.Equal(t, "fail", g.Edges[2].Output, "verification data is preserved verbatim")
	assert.Equal(t, g.Edges[1].Source, g.Edges[3].Source)
	assert.Equal(t, g.Edges[1].Target, g.Edges[3].Target)
	assert.NotEqual(t, g.Edges[1].Ordinal, g.Edges[3].Ordinal, "parallel edges keep distinct identities")
}

func TestBuildGraph_NodeIndex(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "x"}, {ID: "y"}}})
	require.NoError(t, err)

	i, ok := g.NodeIndex("y")
	assert.True(t, ok)
	assert.Equal(t, 1, i)

	_, ok = g.NodeIndex("missing")
	assert.False(t, ok)
}

func TestBuildGraph_SingleNodeNoEdges(t *testing.T) {
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "only", Type: workflow.NodeTypeScript}}})
	require.NoError(t, err)
	assert.Len(t, g.Nodes, 1)
	assert.Empty(t, g.Edges)
}

func TestBuildGraph_Errors(t *testing.T) {
	cases := []struct {
		name string
		wf   *workflow.Workflow
		want error
	}{
		{name: "nil workflow", wf: nil, want: ErrEmptyGraph},
		{name: "no nodes", wf: &workflow.Workflow{}, want: ErrEmptyGraph},
		{name: "empty node id", wf: &workflow.Workflow{Nodes: []workflow.Node{{ID: ""}}}, want: ErrEmptyNodeID},
		{name: "duplicate node id", wf: &workflow.Workflow{Nodes: []workflow.Node{{ID: "a"}, {ID: "a"}}}, want: ErrDuplicateNode},
		{name: "dangling source", wf: &workflow.Workflow{
			Nodes: []workflow.Node{{ID: "a"}},
			Edges: []workflow.Edge{{Source: "ghost", Target: "a"}},
		}, want: ErrDanglingEdge},
		{name: "dangling target", wf: &workflow.Workflow{
			Nodes: []workflow.Node{{ID: "a"}},
			Edges: []workflow.Edge{{Source: "a", Target: "ghost"}},
		}, want: ErrDanglingEdge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildGraph(tc.wf)
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.want), "got %v, want %v", err, tc.want)
		})
	}
}

func TestBuildGraph_DoesNotMutateManifest(t *testing.T) {
	wf := &workflow.Workflow{
		Name:  "keep-me",
		Nodes: []workflow.Node{{ID: "a", Label: "A", Position: &workflow.NodePosition{X: 1, Y: 2}}},
		Edges: []workflow.Edge{{Source: "a", Target: "a", Output: "again"}},
	}
	before := *wf
	beforeNodes := append([]workflow.Node(nil), wf.Nodes...)
	beforeEdges := append([]workflow.Edge(nil), wf.Edges...)

	_, err := BuildGraph(wf)
	require.NoError(t, err)

	assert.Equal(t, before.Name, wf.Name)
	assert.Equal(t, beforeNodes, wf.Nodes)
	assert.Equal(t, beforeEdges, wf.Edges)
	assert.Equal(t, &workflow.NodePosition{X: 1, Y: 2}, wf.Nodes[0].Position, "manual coordinates are untouched")
}

func TestBuildGraph_UnicodeLabelsPreserved(t *testing.T) {
	label := "Überprüfung — 検証ステップ ✅ with a deliberately long label that must wrap, not ellipsize"
	g, err := BuildGraph(&workflow.Workflow{Nodes: []workflow.Node{{ID: "u", Label: label}}})
	require.NoError(t, err)
	assert.Equal(t, label, g.Nodes[0].Text, "full text is kept; wrapping happens in sizing/rendering")
}
```

- [x] **Step 2: Run it, confirm it fails**

Run: `go test ./domain/layout/ -run TestBuildGraph -v`
Expected: build failure — `undefined: BuildGraph`, `undefined: GraphNode`, `undefined: ErrEmptyGraph` (the package has no non-test files yet).

**Behavioral RED is required next:** add only the minimal compiling declarations/stubs,
rerun these tests and observe the intended assertions failing before implementing behavior.
A missing-symbol/compiler error alone does not complete this RED step.

- [x] **Step 3: Minimal implementation**

Create `domain/layout/graph.go`:

```go
// Package layout arranges saved workflow graphs top-to-bottom, checks the
// resulting geometry, and coordinates bounded arrange → render → review
// attempts. It is pure domain code: it imports no concrete adapters, and the
// only I/O it performs is through the Renderer and Reviewer ports.
package layout

import (
	"errors"
	"fmt"
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
	index map[string]int
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
	return g, nil
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
```

- [x] **Step 4: Run it, confirm it passes**

Run: `go test ./domain/layout/ -run TestBuildGraph -v`
Expected: `--- PASS` for `TestBuildGraph_DisplayTextAndTypeCaption`, `TestBuildGraph_EdgeOrdinalsAndBranchText`, `TestBuildGraph_NodeIndex`, `TestBuildGraph_SingleNodeNoEdges`, `TestBuildGraph_Errors` (6 subtests), `TestBuildGraph_DoesNotMutateManifest`, `TestBuildGraph_UnicodeLabelsPreserved`; final line `ok  	github.com/arbitron/arbitron/domain/layout`.

Then confirm the domain layering rule (no adapter imports): `go list -deps ./domain/layout/ | grep -c 'arbitron/adapter'`
Expected: `0`.

- [x] **Step 5: Commit**

```bash
gofmt -l domain/layout/   # expect no output
git add domain/layout/graph.go domain/layout/graph_test.go
git commit -m "feat(layout): normalise saved manifests into an arrangement graph" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

## Execution evidence

- RED: the prescribed test run first failed with missing symbols; minimal compiling stubs then failed node/edge coverage, indexing, error and Unicode assertions.
- GREEN: `go test ./domain/layout/ -run TestBuildGraph -v` passes all seven tests and six error subtests. `go test -race ./domain/layout/` and `go vet ./domain/layout/` pass; `gofmt -l domain/layout/` is empty; adapter dependency count is zero.
- Self-review (in order): confirmed the exact P1T1 contract with no added API; strengthened full edge-value and byte-for-byte manifest immutability assertions; checked neighboring Go conventions and formatting. Temporary mutations dropping `GraphEdge.Label` and clearing manifest config each failed the strengthened tests; both mutations were removed and verification rerun green.
- Production integration is not yet exercised: the named coordinator, saved-manifest capture, worker and composition-root tasks below remain pending. These unit checks do not establish end-to-end production acceptance.

## Wired-and-fed

- **Data source (production producer of the input):** the captured saved manifest — `workflow.RevisionStamper.Stamp` (P2T2) serialises the committed `*workflow.Workflow` into `ArrangementRequest.Manifest` at every save path (P2T3 `Service.Create/Update/Publish/Copy/Duplicate/Rename`, P2T4 `Seeder.seedOne`); `layout.Requester` (P2T5) stores it in `layout_revisions.input_manifest` (P2T6); `layout.Worker.ProcessRevision` (P2T8) unmarshals it and hands it to `Coordinator.Run` (P1T9), which calls `BuildGraph`.
- **Composition-root wiring:** `BuildGraph` is a pure function reached through `layout.Coordinator`, constructed in `cmd/arbitron/layout_wiring.go:newLayoutStack` (P2T9) and run by the worker started in `initDomain` for both `serve` and `mcp`. It is also called by `layout.Reader.View` (P2T5) to validate stored geometry for the views (P3T8/P3T9/P3T10).
- **Provenance probe:** P2T9's composition-root e2e test fails if a saved manifest does not reach the worker (its row's `Manifest` must unmarshal to the saved nodes and the run must reach a terminal state); `TestBuildGraph_Errors` proves empty/nil input is rejected rather than arranged as an empty layout.
