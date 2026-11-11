package samples_test

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// Shared helpers for the sample tests. Every sample task builds on these, so
// they discover samples from disk rather than keeping a hand-written list.

// allSampleNames returns every samples/*/workflow.json directory, sorted. A
// harness that finds fewer than four has lost its discovery, so it fails.
func allSampleNames(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("*/workflow.json")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, m := range matches {
		names = append(names, filepath.Dir(m))
	}
	sort.Strings(names)
	if len(names) < 4 {
		t.Fatalf("discovered %d samples (%v), want at least 4", len(names), names)
	}
	return names
}

// sampleRoot copies every sample to users/<owner>/<name>/ and samples/_lib to
// shared/_lib in a temp base, mirroring a real workspace so @lib resolves.
func sampleRoot(t *testing.T) (base string, owner uuid.UUID) {
	t.Helper()
	owner = uuid.New()
	base = t.TempDir()
	for _, name := range allSampleNames(t) {
		dst := filepath.Join(base, "users", owner.String(), name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(dst, os.DirFS(name)); err != nil {
			t.Fatalf("copy sample %q: %v", name, err)
		}
	}
	lib := filepath.Join(base, "shared", workflow.LibraryFolder)
	if err := os.MkdirAll(filepath.Dir(lib), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(lib, os.DirFS("_lib")); err != nil {
		t.Fatalf("copy _lib: %v", err)
	}
	return base, owner
}

// profileRow is one row of a sample's PROFILES.md alias table.
type profileRow struct {
	Alias, Kind, Backend, Model string
}

// profileTable parses the table in samples/<name>/PROFILES.md whose header row
// begins `| alias |`, mapping columns by header name so added columns (such as
// context) do not shift the others. A sample with no PROFILES.md, or no such
// table, yields an empty map.
func profileTable(t *testing.T, name string) map[string]profileRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(name, "PROFILES.md"))
	if os.IsNotExist(err) {
		return map[string]profileRow{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return parseProfileTable(t, string(data))
}

func splitRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

func parseProfileTable(t *testing.T, doc string) map[string]profileRow {
	t.Helper()
	rows := map[string]profileRow{}
	var cols map[string]int
	for _, line := range strings.Split(doc, "\n") {
		trim := strings.TrimSpace(line)
		if cols == nil {
			if strings.HasPrefix(strings.ToLower(trim), "| alias |") {
				cols = map[string]int{}
				for i, h := range splitRow(trim) {
					cols[strings.ToLower(h)] = i
				}
			}
			continue
		}
		if !strings.HasPrefix(trim, "|") {
			break
		}
		cells := splitRow(trim)
		if strings.Trim(strings.Join(cells, ""), "-: ") == "" {
			continue // separator row
		}
		get := func(h string) string {
			if i, ok := cols[h]; ok && i < len(cells) {
				return strings.Trim(cells[i], "`")
			}
			return ""
		}
		r := profileRow{Alias: get("alias"), Kind: get("kind"), Backend: get("backend"), Model: get("model")}
		if r.Alias == "" {
			t.Fatalf("PROFILES.md row with empty alias: %q", line)
		}
		if _, dup := rows[r.Alias]; dup {
			t.Fatalf("PROFILES.md lists alias %q twice", r.Alias)
		}
		rows[r.Alias] = r
	}
	return rows
}

func findNode(wf *workflow.Workflow, id string) (workflow.Node, bool) {
	for _, n := range wf.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return workflow.Node{}, false
}

// nodeByID returns the node with the given id, or the zero Node if absent.
func nodeByID(wf *workflow.Workflow, id string) workflow.Node {
	n, _ := findNode(wf, id)
	return n
}

// route runs the real ConditionalExecutor on a conditional node and returns the
// output port. outputs maps node id -> that node's output object.
func route(t *testing.T, wf *workflow.Workflow, nodeID string, outputs map[string]any, inputs map[string]any) string {
	t.Helper()
	n, ok := findNode(wf, nodeID)
	if !ok {
		t.Fatalf("%s has no node %q", wf.Name, nodeID)
	}
	vars := variable.NewStore()
	for id, out := range outputs {
		vars.SetNodeOutput(id, out)
	}
	for k, v := range inputs {
		if err := vars.SetInput(k, v); err != nil {
			t.Fatal(err)
		}
	}
	res, err := engine.NewConditionalExecutor().Execute(context.Background(), n, vars, wf.Name)
	if err != nil {
		t.Fatal(err)
	}
	return res.OutputPort
}

// onCycle reports whether the node is reachable from itself along edges.
func onCycle(wf *workflow.Workflow, id string) bool {
	adj := map[string][]string{}
	for _, e := range wf.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
	}
	seen := map[string]bool{}
	queue := append([]string(nil), adj[id]...)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == id {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		queue = append(queue, adj[cur]...)
	}
	return false
}

func TestAllSampleNamesDiscoversShippedSamples(t *testing.T) {
	got := strings.Join(allSampleNames(t), ",")
	for _, want := range []string{"feature-pipeline", "harness-update", "legacy-feature-pipeline", "pr-review", "ralph-loop"} {
		if !strings.Contains(got, want) {
			t.Errorf("allSampleNames = %s, missing %s", got, want)
		}
	}
	if strings.Contains(got, "_lib") {
		t.Errorf("allSampleNames must not include _lib: %s", got)
	}
}

func TestSampleRootSeedsLibAndOwnerPartition(t *testing.T) {
	base, owner := sampleRoot(t)
	for _, p := range []string{
		filepath.Join("shared", "_lib", "manifest-append.sh"),
		filepath.Join("users", owner.String(), "ralph-loop", "workflow.json"),
	} {
		if _, err := os.Stat(filepath.Join(base, p)); err != nil {
			t.Errorf("sampleRoot missing %s: %v", p, err)
		}
	}
}

func TestProfileTableMapsColumnsByHeader(t *testing.T) {
	doc := "intro\n\n| alias | kind | backend | model | effort | context | tools | used by |\n" +
		"|---|---|---|---|---|---|---|---|\n" +
		"| a-one | agent | codex | gpt-6.1-sol | high | 1M | Read | x |\n" +
		"| c-two | chat | (inherit) | `claude-opus-5-5` | high | — | (none) | y |\n\ntrailing | not | table |\n"
	got := parseProfileTable(t, doc)
	if len(got) != 2 {
		t.Fatalf("parsed %d rows, want 2: %v", len(got), got)
	}
	if r := got["a-one"]; r.Kind != "agent" || r.Backend != "codex" || r.Model != "gpt-6.1-sol" {
		t.Errorf("a-one = %+v", r)
	}
	if r := got["c-two"]; r.Kind != "chat" || r.Model != "claude-opus-5-5" {
		t.Errorf("c-two = %+v", r)
	}
	if len(parseProfileTable(t, "no table here")) != 0 {
		t.Error("a document without the table must yield no rows")
	}
}

func TestProfileTableReadsRealSamples(t *testing.T) {
	r := profileTable(t, "ralph-loop")["critic-codex-high"]
	if r.Kind != "agent" || r.Backend != "codex" || r.Model != "gpt-6-astra" {
		t.Errorf("ralph-loop critic-codex-high = %+v", r)
	}
	if got := profileTable(t, "harness-update"); len(got) != 0 {
		t.Errorf("a sample without PROFILES.md must yield an empty table, got %v", got)
	}
}

func syntheticGraph() *workflow.Workflow {
	return &workflow.Workflow{
		Name: "synthetic",
		Nodes: []workflow.Node{
			{ID: "start", Type: workflow.NodeTypeScript},
			{ID: "body", Type: workflow.NodeTypeAgentCall},
			{ID: "gate", Type: workflow.NodeTypeConditional, Config: workflow.NodeConfig{
				Conditions: []workflow.Condition{
					{Field: "body.done", Operator: "eq", Value: true, Output: "exit"},
					{Field: "round", Operator: "gte", Value: float64(3), Output: "stop"},
				},
				DefaultOutput: "again",
			}},
			{ID: "end", Type: workflow.NodeTypeScript},
		},
		Edges: []workflow.Edge{
			{Source: "start", Target: "body"}, {Source: "body", Target: "gate"},
			{Source: "gate", Target: "body"}, {Source: "gate", Target: "end"},
		},
	}
}

func TestOnCycle(t *testing.T) {
	wf := syntheticGraph()
	for id, want := range map[string]bool{"start": false, "body": true, "gate": true, "end": false, "missing": false} {
		if got := onCycle(wf, id); got != want {
			t.Errorf("onCycle(%s) = %v, want %v", id, got, want)
		}
	}
	wf.Edges = append(wf.Edges, workflow.Edge{Source: "end", Target: "end"})
	if !onCycle(wf, "end") {
		t.Error("a self-edge is a cycle")
	}
}

func TestRouteRunsTheRealConditional(t *testing.T) {
	wf := syntheticGraph()
	cases := []struct {
		name    string
		outputs map[string]any
		inputs  map[string]any
		want    string
	}{
		{"first match wins", map[string]any{"body": map[string]any{"done": true}}, map[string]any{"round": float64(9)}, "exit"},
		{"second condition", map[string]any{"body": map[string]any{"done": false}}, map[string]any{"round": float64(3)}, "stop"},
		{"default", map[string]any{"body": map[string]any{"done": false}}, map[string]any{"round": float64(0)}, "again"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := route(t, wf, "gate", c.outputs, c.inputs); got != c.want {
				t.Errorf("route = %q, want %q", got, c.want)
			}
		})
	}
}

func TestNodeByID(t *testing.T) {
	wf := syntheticGraph()
	if nodeByID(wf, "gate").Type != workflow.NodeTypeConditional {
		t.Error("nodeByID(gate) wrong")
	}
	if nodeByID(wf, "nope").ID != "" {
		t.Error("nodeByID of a missing node must be the zero Node")
	}
}
