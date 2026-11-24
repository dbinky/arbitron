package samples_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
)

// edgeTarget returns the target of the edge leaving source on port, or "" if
// there is none. An empty port matches an unnamed edge.
func edgeTarget(wf *workflow.Workflow, source, port string) string {
	for _, e := range wf.Edges {
		if e.Source == source && e.Output == port {
			return e.Target
		}
	}
	return ""
}

func hasEdge(wf *workflow.Workflow, source, target string) bool {
	for _, e := range wf.Edges {
		if e.Source == source && e.Target == target {
			return true
		}
	}
	return false
}

// reachableAvoiding reports whether to is reachable from from along edges
// without entering the node avoid.
func reachableAvoiding(wf *workflow.Workflow, from, to, avoid string) bool {
	adj := map[string][]string{}
	for _, e := range wf.Edges {
		adj[e.Source] = append(adj[e.Source], e.Target)
	}
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if next == avoid || seen[next] {
				continue
			}
			if next == to {
				return true
			}
			seen[next] = true
			queue = append(queue, next)
		}
	}
	return false
}

// routeTo runs a conditional and returns the node its chosen port leads to.
func routeTo(t *testing.T, wf *workflow.Workflow, nodeID string, outputs map[string]any) string {
	t.Helper()
	port := route(t, wf, nodeID, outputs, nil)
	target := edgeTarget(wf, nodeID, port)
	if target == "" {
		t.Fatalf("%s/%s: port %q has no edge", wf.Name, nodeID, port)
	}
	return target
}

// Every ralph pass must cross the tamper gate before its progress is scored.
// An assess node that still reached record-pass-commit directly would score a
// pass whose tests had been weakened.
func TestRalphTamperWiring(t *testing.T) {
	wf := loadSample(t, "ralph-loop")
	assess := []string{"assess-single-1", "assess-single-2", "assess-paired"}
	for _, a := range assess {
		if !hasEdge(wf, a, "tamper-gate") {
			t.Errorf("%s does not go to tamper-gate", a)
		}
		if hasEdge(wf, a, "record-pass-commit") {
			t.Errorf("%s still goes straight to record-pass-commit", a)
		}
		if reachableAvoiding(wf, a, "record-pass-commit", "tamper-gate") {
			t.Errorf("%s reaches record-pass-commit without crossing tamper-gate", a)
		}
	}
	for _, n := range []string{"tamper-void", "tamper-verdict"} {
		if !hasEdge(wf, n, "record-pass-commit") {
			t.Errorf("%s does not go to record-pass-commit", n)
		}
	}
	for _, j := range []string{"tamper-judge-codex", "tamper-judge-opus"} {
		if !hasEdge(wf, j, "tamper-verdict") {
			t.Errorf("%s does not go to tamper-verdict", j)
		}
	}
	if edgeTarget(wf, "pass-mark", "") != "work-router" || !hasEdge(wf, "select-work", "pass-mark") {
		t.Error("pass-mark must sit between select-work and work-router")
	}
}

// tamper-route is first-match: a forced void (source edited under
// --tests-only, or history rewritten) must win even when a judgeable finding
// also exists, because a judge cannot rule on a meaningless diff.
func TestTamperRouteFirstMatch(t *testing.T) {
	cases := []struct {
		workflow, voidNode, judgePath, cleanPath string
	}{
		{"ralph-loop", "tamper-void", "tamper-context", "record-pass-commit"},
		{"implement", "tamper-void", "tamper-context", "checkpoint-push-impl"},
		{"harden", "void-round", "tamper-context", "checkpoint-push-round"},
	}
	for _, c := range cases {
		t.Run(c.workflow, func(t *testing.T) {
			wf := loadSample(t, c.workflow)
			both := map[string]any{"tamper-gate": map[string]any{"force_void": true, "needs_judge": true}}
			if got := routeTo(t, wf, "tamper-route", both); got != c.voidNode {
				t.Errorf("force_void+needs_judge routed to %s, want %s", got, c.voidNode)
			}
			judge := map[string]any{"tamper-gate": map[string]any{"force_void": false, "needs_judge": true}}
			if got := routeTo(t, wf, "tamper-route", judge); got != c.judgePath {
				t.Errorf("needs_judge routed to %s, want %s", got, c.judgePath)
			}
			clean := map[string]any{"tamper-gate": map[string]any{"force_void": false, "needs_judge": false, "clean": true}}
			if got := routeTo(t, wf, "tamper-route", clean); got != c.cleanPath {
				t.Errorf("clean routed to %s, want %s", got, c.cleanPath)
			}
		})
	}
}

// The judge is the opposite family to the pass's doer: only single_2 has a
// GPT doer (coder-codex-high), so only it gets the Opus judge.
func TestRalphJudgePick(t *testing.T) {
	wf := loadSample(t, "ralph-loop")
	want := map[string]string{
		"single_1": "tamper-judge-codex",
		"single_2": "tamper-judge-opus",
		"paired":   "tamper-judge-codex",
	}
	for kind, judge := range want {
		out := map[string]any{"select-work": map[string]any{"next_kind": kind}}
		if got := routeTo(t, wf, "judge-pick", out); got != judge {
			t.Errorf("next_kind=%s routed to %s, want %s", kind, got, judge)
		}
	}
}

// R3: the baseline is committed and pushed before the first pass-mark, so the
// baseline files are never inside a gated range (where they would read as
// baseline_edited).
func TestBaselineCommittedBeforeFirstPass(t *testing.T) {
	cases := []struct {
		workflow, start, record, push, mark string
	}{
		{"ralph-loop", "init-workspace", "record-baseline", "checkpoint-push-baseline", "pass-mark"},
	}
	for _, c := range cases {
		t.Run(c.workflow, func(t *testing.T) {
			wf := loadSample(t, c.workflow)
			for _, id := range []string{c.start, c.record, c.push, c.mark} {
				if _, ok := findNode(wf, id); !ok {
					t.Fatalf("no node %q", id)
				}
			}
			if reachableAvoiding(wf, c.start, c.mark, c.record) {
				t.Errorf("%s reaches %s without %s", c.start, c.mark, c.record)
			}
			if reachableAvoiding(wf, c.record, c.mark, c.push) {
				t.Errorf("%s reaches %s without %s", c.record, c.mark, c.push)
			}
			if !reachableAvoiding(wf, c.start, c.mark, "") {
				t.Errorf("%s never reaches %s", c.start, c.mark)
			}
		})
	}
	t.Run("implement", implBaselineCommittedBeforeFirstPass)
}

// Every implement pass crosses the tamper gate before it is pushed or counted. The judge is
// a single node: the only doer is impl-task.
func TestImplTamperWiring(t *testing.T) {
	wf := loadSample(t, "implement")
	for _, chain := range [][]string{
		{"pass-mark", "impl-task", "commit-leftovers", "tamper-gate", "tamper-route"},
		{"tamper-context", "tamper-judge", "tamper-verdict", "voided?"},
		{"tamper-void", "checkpoint-push-void"},
		{"count-plan-steps", "stall-check", "impl-inc", "impl-gate"},
	} {
		for i := 0; i+1 < len(chain); i++ {
			if !hasEdge(wf, chain[i], chain[i+1]) {
				t.Errorf("missing edge %s -> %s", chain[i], chain[i+1])
			}
		}
	}
	for _, push := range []string{"checkpoint-push-impl", "checkpoint-push-void", "count-plan-steps"} {
		if reachableAvoiding(wf, "impl-task", push, "tamper-gate") {
			t.Errorf("impl-task reaches %s without crossing tamper-gate", push)
		}
	}
	// The agent's leftovers are committed inside the pass range before the gate
	// reads it, and the counter reads that same committed HEAD.
	if got := nodeByID(wf, "commit-leftovers").Config.ScriptFile; got != "@lib/commit-leftovers.sh" {
		t.Errorf("commit-leftovers script = %q, want @lib/commit-leftovers.sh", got)
	}
	if reachableAvoiding(wf, "impl-task", "tamper-gate", "commit-leftovers") {
		t.Error("impl-task reaches tamper-gate without crossing commit-leftovers")
	}
	if edgeTarget(wf, "impl-gate", "loop") != "pass-mark" {
		t.Error("impl-gate's loop port must return to pass-mark, the loop head")
	}
	want := map[string][]string{
		"tamper-gate":    {"{{pass-mark.sha}}", "HEAD"},
		"tamper-void":    {"{{pass-mark.sha}}", "--force-void", "--restore-json", "resolve-inputs.inputs.plan_paths"},
		"tamper-verdict": {"{{pass-mark.sha}}", "--restore-json", "resolve-inputs.inputs.plan_paths"},
	}
	for id, args := range want {
		if got := nodeByID(wf, id).Config.Args; !slices.Equal(got, args) {
			t.Errorf("%s args = %q, want %q", id, got, args)
		}
	}
	if got := nodeByID(wf, "tamper-judge").Config.TemplateFile; got != "@lib/tamper-judge.md" {
		t.Errorf("tamper-judge template = %q, want @lib/tamper-judge.md", got)
	}
}

// A harden round may change tests only: the gate runs --tests-only, so a
// source change is a forced void. Every round crosses the gate before its
// claims are judged or its mutants rescored.
func TestHardenTamperWiring(t *testing.T) {
	wf := loadSample(t, "harden")
	if got := nodeByID(wf, "tamper-gate").Config.Args; !slices.Equal(got, []string{"{{pass-mark.sha}}", "HEAD", "--tests-only"}) {
		t.Errorf("tamper-gate args = %q, want the pass range with --tests-only", got)
	}
	// harden-inc is left out: a dirty round reaches it through void-dirty
	// without the gate, and that round is voided (TestHardenDirtyTreeVoidsRound).
	for _, next := range []string{"checkpoint-push-round", "count-claims", "judge-equivalent", "rescore"} {
		if reachableAvoiding(wf, "strengthen", next, "tamper-gate") {
			t.Errorf("strengthen reaches %s without crossing tamper-gate", next)
		}
	}
	if got := nodeByID(wf, "tamper-judge").Config.TemplateFile; got != "@lib/tamper-judge.md" {
		t.Errorf("tamper-judge template = %q, want @lib/tamper-judge.md", got)
	}
	if edgeTarget(wf, "harden-gate", "loop") != "pass-mark" {
		t.Error("harden-gate's loop port must return to pass-mark, the loop head")
	}
}

// R8: a voided round must not leave its equivalence claims or verdicts behind
// for the next round's judge, so every void node clears that scratch.
// void-round takes no --force-void: it runs only on this round's fresh
// findings.json with force_void set, so tamper-verdict voids anyway and names
// the real reason (source_changed or history_rewritten) in must-fix.md.
// void-dirty and void-state run before the gate (clean-check has removed the
// previous round's findings.json), so they force the void and name why.
func TestHardenVoidNodesClearScratch(t *testing.T) {
	wf := loadSample(t, "harden")
	want := map[string][]string{
		"void-round":     {"{{pass-mark.sha}}", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"},
		"void-dirty":     {"{{pass-mark.sha}}", "--force-void", "--reason", "uncommitted_changes_discarded", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"},
		"void-state":     {"{{pass-mark.sha}}", "--force-void", "--reason", "score_state_edited", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"},
		"tamper-verdict": {"{{pass-mark.sha}}", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"},
	}
	for id, args := range want {
		n := nodeByID(wf, id)
		if n.Config.ScriptFile != "@lib/tamper-verdict.sh" || !slices.Equal(n.Config.Args, args) {
			t.Errorf("%s = %s %q, want @lib/tamper-verdict.sh %q", id, n.Config.ScriptFile, n.Config.Args, args)
		}
	}
}

// R3 for implement: a measured baseline is persisted and pushed, an imported
// one is already committed, and both are lifted by record-baseline before the
// first pass-mark. A committed baseline is never re-measured.
func implBaselineCommittedBeforeFirstPass(t *testing.T) {
	wf := loadSample(t, "implement")
	if got := routeTo(t, wf, "baseline-source?", map[string]any{"baseline-check": map[string]any{"present": true}}); got != "import-baseline" {
		t.Errorf("present baseline routed to %s, want import-baseline", got)
	}
	if got := routeTo(t, wf, "baseline-source?", map[string]any{"baseline-check": map[string]any{"present": false}}); got != "baseline-tests" {
		t.Errorf("absent baseline routed to %s, want baseline-tests", got)
	}
	if reachableAvoiding(wf, "init", "pass-mark", "record-baseline") {
		t.Error("init reaches pass-mark without record-baseline")
	}
	if reachableAvoiding(wf, "baseline-tests", "pass-mark", "persist-baseline") {
		t.Error("the measure path reaches pass-mark without persist-baseline")
	}
	if reachableAvoiding(wf, "persist-baseline", "pass-mark", "checkpoint-push-baseline") {
		t.Error("the measure path reaches pass-mark without checkpoint-push-baseline")
	}
	if reachableAvoiding(wf, "baseline-tests", "record-baseline", "checkpoint-push-baseline") {
		t.Error("the measure path reaches record-baseline before the baseline is pushed")
	}
	if !hasEdge(wf, "import-baseline", "record-baseline") || !hasEdge(wf, "record-baseline", "plan-preflight") ||
		!hasEdge(wf, "plan-preflight", "plans-usable?") || !hasEdge(wf, "plans-usable?", "pass-mark") {
		t.Error("the import path must go import-baseline -> record-baseline -> plan-preflight -> plans-usable? -> pass-mark")
	}
}

func TestReachableAvoiding(t *testing.T) {
	wf := syntheticGraph()
	for _, e := range wf.Edges {
		if !reachableAvoiding(wf, e.Source, e.Target, "") {
			t.Errorf("direct edge %s->%s not reachable", e.Source, e.Target)
		}
	}
	line := &workflow.Workflow{Edges: []workflow.Edge{{Source: "a", Target: "b"}, {Source: "b", Target: "c"}}}
	if reachableAvoiding(line, "a", "c", "b") {
		t.Error("a->b->c reachable while avoiding b")
	}
	if !reachableAvoiding(line, "a", "c", "x") {
		t.Error("a->b->c not reachable avoiding an unrelated node")
	}
}

// A ralph pass is pushed only after the tamper gate has ruled. The doers and
// approvers commit only; checkpoint-push-pass (after record-pass-commit)
// publishes the pass, including tamper-verdict's revert. An agent push would
// put an ungated pass on the branch whenever the run died before the verdict.
func TestRalphPushesOnlyAfterTheGate(t *testing.T) {
	for _, f := range []string{"refine-single.md", "refine-paired.md", "assess-single.md", "assess-paired.md"} {
		data, err := os.ReadFile("ralph-loop/templates/" + f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "git push") || strings.Contains(string(data), "work push") {
			t.Errorf("%s pushes inside the pass", f)
		}
	}
	wf := loadSample(t, "ralph-loop")
	for _, chain := range [][]string{{"record-pass-commit", "checkpoint-push-pass", "count-review-plan"}} {
		for i := 0; i+1 < len(chain); i++ {
			if !hasEdge(wf, chain[i], chain[i+1]) {
				t.Errorf("missing edge %s -> %s", chain[i], chain[i+1])
			}
		}
	}
	if got := nodeByID(wf, "checkpoint-push-pass").Config.ScriptFile; got != "@lib/checkpoint-push.sh" {
		t.Errorf("checkpoint-push-pass script = %q, want @lib/checkpoint-push.sh", got)
	}
	for _, doer := range []string{"refine-single-1", "refine-single-2", "refine-paired"} {
		if reachableAvoiding(wf, doer, "checkpoint-push-pass", "tamper-gate") {
			t.Errorf("%s reaches checkpoint-push-pass without crossing tamper-gate", doer)
		}
	}
	if reachableAvoiding(wf, "pass-mark", "refine-assess", "checkpoint-push-pass") {
		t.Error("a pass reaches refine-assess without checkpoint-push-pass")
	}
}
