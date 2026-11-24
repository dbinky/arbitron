package samples_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// The plan stage sends the open dependency decisions to a person exactly once.
// The legacy loop (deps-gate -> resolve-deps -> deps-inc -> align-plans) put an
// interactive node on a cycle; here the one resolve pass is unrolled, so
// deps-gate offers the human the decisions and deps-gate-2 blocks the stage
// rather than asking again. Both gates route on deps-count (a script that
// fails closed), never on a field the LLM may or may not emit (run 5e86d796).
func TestPlanDepsGatesSendDecisionsToTheHumanOnce(t *testing.T) {
	wf := loadSample(t, "plan")
	count := func(id string, needs float64) map[string]any {
		return map[string]any{id: map[string]any{"needs_resolution": needs}}
	}
	// What run 5e86d796's deps-assess reported: nothing blocking, 7 open items.
	llmSaidNothingBlocks := map[string]any{
		"blocking_count": float64(0), "has_blocking": false,
		"unresolved_count": float64(7), "has_unresolved": true,
	}
	withAssess := func(out map[string]any, id string) map[string]any {
		out[id] = llmSaidNothingBlocks
		return out
	}

	if got := routeTo(t, wf, "deps-gate", withAssess(count("deps-count", 7), "deps-assess")); got != "resolve-deps" {
		t.Errorf("deps-gate with 7 decisions -> %q, want resolve-deps", got)
	}
	if got := routeTo(t, wf, "deps-gate-2", withAssess(count("deps-count-2", 7), "deps-assess-2")); got != "mark-unresolved" {
		t.Errorf("deps-gate-2 with 7 decisions -> %q, want mark-unresolved (no second ask)", got)
	}
	for _, gate := range []struct{ id, counter string }{{"deps-gate", "deps-count"}, {"deps-gate-2", "deps-count-2"}} {
		if got := routeTo(t, wf, gate.id, count(gate.counter, 0)); got != "mark-resolved" {
			t.Errorf("%s with 0 -> %q, want mark-resolved", gate.id, got)
		}
	}
	// deps-count fails closed with needs_resolution=1 on unreadable output.
	if got := routeTo(t, wf, "deps-gate", count("deps-count", 1)); got == "mark-resolved" {
		t.Errorf("deps-gate with unreadable assess output proceeded to %q", got)
	}
	// Gate 2 must read its own counter: a stale first-pass 0 cannot unblock it.
	stale := count("deps-count", 0)
	stale["deps-count-2"] = map[string]any{"needs_resolution": float64(3)}
	if got := routeTo(t, wf, "deps-gate-2", stale); got != "mark-unresolved" {
		t.Errorf("deps-gate-2 read the first-pass counter: -> %q, want mark-unresolved", got)
	}
}

// The second counter must count the second assessment.
func TestPlanDepsCountersReadTheirOwnAssessment(t *testing.T) {
	wf := loadSample(t, "plan")
	for id, want := range map[string][]string{"deps-count": {"deps-assess"}, "deps-count-2": {"deps-assess-2"}} {
		n := nodeByID(wf, id)
		if n.Config.ScriptFile != "scripts/deps-count.sh" || !slices.Equal(n.Config.Args, want) {
			t.Errorf("%s = %s %q, want scripts/deps-count.sh %q", id, n.Config.ScriptFile, n.Config.Args, want)
		}
	}
	for _, chain := range [][]string{
		{"align-plans", "deps-assess", "deps-count", "deps-gate"},
		{"resolve-deps", "push-resolution", "align-plans-2", "deps-assess-2", "deps-count-2", "deps-gate-2"},
		{"mark-resolved", "list-plans", "checkpoint-push", "finish"},
		{"mark-unresolved", "list-plans"},
	} {
		for i := 0; i+1 < len(chain); i++ {
			if !hasEdge(wf, chain[i], chain[i+1]) {
				t.Errorf("missing edge %s -> %s", chain[i], chain[i+1])
			}
		}
	}
}

// markVars runs a mark-* transform over a store holding deps-assess-2's gaps.
func markVars(t *testing.T, wf *workflow.Workflow, id string) *variable.VariableStore {
	t.Helper()
	vars := variable.NewStore()
	vars.SetNodeOutput("deps-assess-2", map[string]any{"gaps": []any{"DECISION: a.md — x; decision: y?"}})
	if _, err := engine.NewTransformExecutor().Execute(context.Background(), nodeByID(wf, id), vars, wf.Name); err != nil {
		t.Fatal(err)
	}
	return vars
}

// The marks feed finish: plan_status is the manifest status, deps_resolved a
// bool, deps_gaps the JSON text of deps-assess-2.gaps when blocked (§3.3).
func TestPlanMarksRecordTheStageOutcome(t *testing.T) {
	wf := loadSample(t, "plan")

	v := markVars(t, wf, "mark-unresolved")
	st, _ := v.Get("plan_status")
	ok, _ := v.Get("deps_resolved")
	gaps, _ := v.Get("deps_gaps")
	if st != "blocked" || ok != false {
		t.Errorf("mark-unresolved: plan_status=%v deps_resolved=%#v, want blocked / false", st, ok)
	}
	s, isStr := gaps.(string)
	var parsed []string
	if !isStr || json.Unmarshal([]byte(s), &parsed) != nil || len(parsed) != 1 {
		t.Errorf("mark-unresolved deps_gaps = %#v, want the JSON text of deps-assess-2.gaps", gaps)
	}

	v = markVars(t, wf, "mark-resolved")
	st, _ = v.Get("plan_status")
	ok, _ = v.Get("deps_resolved")
	gaps, _ = v.Get("deps_gaps")
	if st != "done" || ok != true || gaps != "" {
		t.Errorf("mark-resolved: plan_status=%v deps_resolved=%#v deps_gaps=%#v, want done / true / \"\"", st, ok, gaps)
	}
}

// resolve-deps runs at most once: nothing in the plan graph may loop.
func TestPlanIsAcyclic(t *testing.T) {
	wf := loadSample(t, "plan")
	for _, n := range wf.Nodes {
		if onCycle(wf, n.ID) {
			t.Errorf("node %s sits on a cycle", n.ID)
		}
	}
	if _, ok := findNode(wf, "resolve-deps"); !ok {
		t.Fatal("plan has no resolve-deps node")
	}
}

// implGate runs the transforms on one pass's tail (lift-assess on a pass that
// stood, skip-assess on a voided one) over the given outputs and inputs, then
// the real impl-gate, and returns the node impl-gate routes to.
func implGate(t *testing.T, wf *workflow.Workflow, tail string, outputs, inputs map[string]any) string {
	t.Helper()
	vars := variable.NewStore()
	for id, out := range outputs {
		vars.SetNodeOutput(id, out)
	}
	for k, v := range inputs {
		if err := vars.SetInput(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.NewTransformExecutor().Execute(context.Background(), nodeByID(wf, tail), vars, wf.Name); err != nil {
		t.Fatal(err)
	}
	res, err := engine.NewConditionalExecutor().Execute(context.Background(), nodeByID(wf, "impl-gate"), vars, wf.Name)
	if err != nil {
		t.Fatal(err)
	}
	target := edgeTarget(wf, "impl-gate", res.OutputPort)
	if target == "" {
		t.Fatalf("impl-gate port %q has no edge", res.OutputPort)
	}
	return target
}

// planSteps is a count-plan-steps output.
func planSteps(complete bool, unchecked float64, unreadable []any, noBoxes bool) map[string]any {
	return map[string]any{
		"all_tasks_complete": complete, "unchecked_steps": unchecked, "checked_steps": float64(2),
		"tasks_remaining": unchecked, "unreadable": unreadable, "no_checkboxes": noBoxes,
	}
}

// withStepless names plans that hold no checkbox of their own.
func withStepless(steps map[string]any, paths ...string) map[string]any {
	list := make([]any, 0, len(paths))
	for _, p := range paths {
		list = append(list, p)
	}
	steps["stepless"] = list
	return steps
}

// Before the first pass the plans are counted once: a plan with no checkbox
// steps (or an unreadable one) stops the stage before any agent runs, because
// the agent picks the first task with an unticked box and would skip a
// stepless plan as finished while later tasks wait on what it builds
// (remote-runners 2026-11-23). Usable plans go on to pass-mark.
func TestImplPreflightStopsOnUnusablePlans(t *testing.T) {
	wf := loadSample(t, "implement")
	if !hasEdge(wf, "record-baseline", "plan-preflight") || !hasEdge(wf, "plan-preflight", "plans-usable?") {
		t.Fatal("record-baseline must reach plans-usable? through plan-preflight")
	}
	if hasEdge(wf, "record-baseline", "pass-mark") {
		t.Error("record-baseline bypasses the preflight")
	}
	if n := nodeByID(wf, "plan-preflight"); n.Config.ScriptFile != "scripts/count-plan-steps.sh" {
		t.Errorf("plan-preflight runs %v, want the same counter as count-plan-steps", n.Config.ScriptFile)
	}
	pre := func(steps map[string]any) map[string]any { return map[string]any{"plan-preflight": steps} }
	for name, c := range map[string]struct {
		steps map[string]any
		want  string
	}{
		"usable":        {planSteps(false, 9, []any{}, false), "pass-mark"},
		"stepless":      {withStepless(planSteps(false, 9, []any{}, false), "docs/plans/x/prose.md"), "mark-unusable-preflight"},
		"unreadable":    {planSteps(false, 9, []any{"docs/plans/x/gone.md"}, false), "mark-unusable-preflight"},
		"no checkboxes": {planSteps(false, 0, []any{}, true), "mark-unusable-preflight"},
	} {
		if got := routeTo(t, wf, "plans-usable?", pre(c.steps)); got != c.want {
			t.Errorf("%s -> %s, want %s", name, got, c.want)
		}
	}

	vars := variable.NewStore()
	vars.SetNodeOutput("plan-preflight", withStepless(planSteps(false, 9, []any{}, false), "docs/plans/x/prose.md"))
	if _, err := engine.NewTransformExecutor().Execute(context.Background(), nodeByID(wf, "mark-unusable-preflight"), vars, wf.Name); err != nil {
		t.Fatal(err)
	}
	st, _ := vars.Get("impl_status")
	why, _ := vars.Get("blocked_reason")
	if want := `plans unusable before the first pass: unreadable=[] stepless=["docs/plans/x/prose.md"] no_checkboxes=false; give each plan its checkbox steps and rerun`; st != "blocked" || why != want {
		t.Errorf("mark-unusable-preflight: %v / %q, want blocked / %q", st, why, want)
	}
	if !hasEdge(wf, "mark-unusable-preflight", "finish") {
		t.Error("mark-unusable-preflight does not go to finish")
	}
}

// impl-gate decides completion by arithmetic. count-plan-steps comes first, so
// an assessor claiming completion over unticked boxes cannot end the stage, and
// a completed plan wins even if the assessor also says blocked. Degenerate
// plans (unreadable, or no checkboxes) stop the stage at once instead of
// spinning to the cap. The advisory all_remaining_blocked counts only when
// present and true (its schema key is not enforced), and the cap ends the
// stage as blocked, not done.
func TestImplGateCompletionIsArithmetic(t *testing.T) {
	wf := loadSample(t, "implement")
	under := map[string]any{"impl_iteration": float64(5), "max_impl_iterations": float64(200)}
	atCap := map[string]any{"impl_iteration": float64(200), "max_impl_iterations": float64(200)}
	working := planSteps(false, 3, []any{}, false)
	pass := func(steps, assess map[string]any) map[string]any {
		return map[string]any{"count-plan-steps": steps, "impl-assess": assess}
	}

	claimsDone := pass(working, map[string]any{"all_tasks_complete": true, "tasks_remaining": float64(0), "notes": "all tasks complete"})
	if got := implGate(t, wf, "lift-assess", claimsDone, under); got != "pass-mark" {
		t.Errorf("assessor claims done over unticked boxes -> %s, want pass-mark", got)
	}
	noBlockedKey := pass(working, map[string]any{"all_tasks_complete": false, "notes": "2 tasks remain"})
	if got := implGate(t, wf, "lift-assess", noBlockedKey, under); got != "pass-mark" {
		t.Errorf("absent all_remaining_blocked -> %s, want pass-mark", got)
	}
	if got := implGate(t, wf, "lift-assess", noBlockedKey, atCap); got != "mark-cap" {
		t.Errorf("impl_iteration=200 of 200 with work left -> %s, want mark-cap", got)
	}
	blocked := pass(working, map[string]any{"all_remaining_blocked": true, "notes": "task 3 blocked"})
	if got := implGate(t, wf, "lift-assess", blocked, under); got != "mark-blocked?" {
		t.Errorf("all_remaining_blocked:true -> %s, want mark-blocked?", got)
	}
	completeAndBlocked := pass(planSteps(true, 0, []any{}, false), map[string]any{"all_remaining_blocked": true, "notes": "x"})
	if got := implGate(t, wf, "lift-assess", completeAndBlocked, atCap); got != "mark-done" {
		t.Errorf("complete plan with blocked:true at the cap -> %s, want mark-done (first-match)", got)
	}

	// Degenerate plans beat the assessor and the cap.
	for name, steps := range map[string]map[string]any{
		"one plan unreadable": planSteps(false, 3, []any{"docs/plans/x/gone.md"}, false),
		"no checkboxes":       planSteps(false, 0, []any{}, true),
		"one plan stepless":   withStepless(planSteps(false, 3, []any{}, false), "docs/plans/x/prose.md"),
	} {
		if got := implGate(t, wf, "lift-assess", pass(steps, map[string]any{"all_remaining_blocked": true, "notes": "x"}), atCap); got != "mark-unusable" {
			t.Errorf("%s -> %s, want mark-unusable", name, got)
		}
	}

	// A voided pass never reaches the assessor, so a previous pass's
	// all_remaining_blocked:true still in the store must not block the stage.
	stale := pass(working, map[string]any{"all_remaining_blocked": true, "notes": "an earlier pass"})
	if got := implGate(t, wf, "skip-assess", stale, under); got != "pass-mark" {
		t.Errorf("voided pass with a stale blocked assessor -> %s, want pass-mark", got)
	}
}

// A voided pass skips impl-assess: the assessor would otherwise judge a report
// whose work was just undone. Both void routes rejoin the loop through
// skip-assess, which overrides any stale assessor verdict.
func TestImplVoidedPassSkipsAssess(t *testing.T) {
	wf := loadSample(t, "implement")
	if got := routeTo(t, wf, "voided?", map[string]any{"tamper-verdict": map[string]any{"voided": true}}); got != "checkpoint-push-void" {
		t.Errorf("voided verdict -> %s, want checkpoint-push-void", got)
	}
	if got := routeTo(t, wf, "voided?", map[string]any{"tamper-verdict": map[string]any{"voided": false}}); got != "checkpoint-push-impl" {
		t.Errorf("standing verdict -> %s, want checkpoint-push-impl", got)
	}
	if !hasEdge(wf, "tamper-verdict", "voided?") || !hasEdge(wf, "tamper-void", "checkpoint-push-void") {
		t.Error("both void nodes must lead to the void path")
	}
	for _, chain := range [][]string{
		{"checkpoint-push-void", "skip-assess", "count-plan-steps"},
		{"checkpoint-push-impl", "impl-assess", "lift-assess", "count-plan-steps"},
	} {
		for i := 0; i+1 < len(chain); i++ {
			if !hasEdge(wf, chain[i], chain[i+1]) {
				t.Errorf("missing edge %s -> %s", chain[i], chain[i+1])
			}
		}
	}
	if reachableAvoiding(wf, "checkpoint-push-void", "impl-assess", "pass-mark") {
		t.Error("a voided pass reaches impl-assess before the next pass-mark")
	}
	if reachableAvoiding(wf, "impl-assess", "impl-gate", "lift-assess") {
		t.Error("impl-assess reaches impl-gate without lift-assess")
	}
}

// The marks feed finish: mark-done records done, mark-blocked carries the
// assessor's blocker (or a fixed fallback when it gave none), mark-unusable
// names the degenerate plans, and mark-cap names the cap and the unticked step
// count, so the parent's failure alert says what went wrong.
func TestImplMarksRecordTheStageOutcome(t *testing.T) {
	wf := loadSample(t, "implement")
	run := func(id string) (any, any) {
		t.Helper()
		vars := variable.NewStore()
		if err := vars.SetInput("max_impl_iterations", float64(200)); err != nil {
			t.Fatal(err)
		}
		vars.SetNodeOutput("count-plan-steps", withStepless(planSteps(false, 7, []any{"docs/plans/x/gone.md"}, false), "docs/plans/x/prose.md"))
		vars.SetNodeOutput("impl-assess", map[string]any{"notes": "task 3 blocked: no producer"})
		if _, err := engine.NewTransformExecutor().Execute(context.Background(), nodeByID(wf, id), vars, wf.Name); err != nil {
			t.Fatal(err)
		}
		st, _ := vars.Get("impl_status")
		why, _ := vars.Get("blocked_reason")
		return st, why
	}
	want := map[string][2]string{
		"mark-done":                {"done", ""},
		"mark-blocked":             {"blocked", "task 3 blocked: no producer"},
		"mark-blocked-unexplained": {"blocked", "all remaining tasks blocked (see impl-assess)"},
		"mark-cap":                 {"blocked", "iteration cap 200 reached; 7 unchecked plan steps remain"},
		"mark-unusable":            {"blocked", `plan_paths unreadable or contain no checkboxes: unreadable=["docs/plans/x/gone.md"] stepless=["docs/plans/x/prose.md"] no_checkboxes=false`},
	}
	for id, w := range want {
		if st, why := run(id); st != w[0] || why != w[1] {
			t.Errorf("%s: %v / %q, want %s / %q", id, st, why, w[0], w[1])
		}
		if !hasEdge(wf, id, "finish") {
			t.Errorf("%s does not go to finish", id)
		}
	}

	// mark-blocked? falls back when the assessor's notes are missing or empty.
	for name, c := range map[string]struct {
		assess map[string]any
		want   string
	}{
		"notes present": {map[string]any{"notes": "task 3 blocked"}, "mark-blocked"},
		"notes missing": {map[string]any{"all_remaining_blocked": true}, "mark-blocked-unexplained"},
		"notes empty":   {map[string]any{"notes": ""}, "mark-blocked-unexplained"},
	} {
		if got := routeTo(t, wf, "mark-blocked?", map[string]any{"impl-assess": c.assess}); got != c.want {
			t.Errorf("%s -> %s, want %s", name, got, c.want)
		}
	}
}

// The implement loop holds only agent, chat, script and routing nodes.
func TestImplLoopHasNoInteractiveNode(t *testing.T) {
	wf := loadSample(t, "implement")
	if !onCycle(wf, "impl-task") {
		t.Fatal("impl-task is not on the loop")
	}
	for _, n := range wf.Nodes {
		if workflow.IsInteractive(n.Type) {
			t.Errorf("node %s is %s; implement has no human step", n.ID, n.Type)
		}
	}
}

// hardenGate runs harden-inc (from round before) and then harden-gate over the
// given outputs, returning the node harden-gate routes to.
func hardenGate(t *testing.T, wf *workflow.Workflow, before float64, outputs map[string]any) string {
	t.Helper()
	vars := variable.NewStore()
	for id, out := range outputs {
		vars.SetNodeOutput(id, out)
	}
	for k, v := range map[string]any{"harden_round": before, "max_rounds": float64(3)} {
		if err := vars.SetInput(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := engine.NewTransformExecutor().Execute(context.Background(), nodeByID(wf, "harden-inc"), vars, wf.Name); err != nil {
		t.Fatal(err)
	}
	res, err := engine.NewConditionalExecutor().Execute(context.Background(), nodeByID(wf, "harden-gate"), vars, wf.Name)
	if err != nil {
		t.Fatal(err)
	}
	target := edgeTarget(wf, "harden-gate", res.OutputPort)
	if target == "" {
		t.Fatalf("harden-gate port %q has no edge", res.OutputPort)
	}
	return target
}

// scoreOut is a score.sh output.
func scoreOut(score any, passed bool, denominator float64, reason string) map[string]any {
	return map[string]any{"score": score, "gate_passed": passed, "denominator": denominator, "reason": reason}
}

// A passing score ends the stage at once; a miss loops until the round cap,
// after which report records the miss (the stage is still done). A voided
// round leaves score-r absent or stale, which never reads as passed. A run in
// which every mutant was excluded (a broken mutation build: score null, gate
// failed, reason "all N mutants excluded ...") goes straight to report: the
// survivors list is empty, so strengthen would have nothing to target.
func TestHardenGateBoundary(t *testing.T) {
	wf := loadSample(t, "harden")
	allExcluded := scoreOut(nil, false, 0, "all 12 mutants excluded (compile/runtime errors or unknown statuses) — mutation build likely broken")

	gate0 := []struct {
		name  string
		score map[string]any
		want  string
	}{
		{"passed", scoreOut(85.5, true, 20, ""), "report"},
		{"no mutants", scoreOut(nil, true, 0, "no mutants"), "report"},
		{"all excluded", allExcluded, "report"},
		{"below threshold", scoreOut(79.99, false, 20, ""), "pass-mark"},
	}
	for _, c := range gate0 {
		if got := routeTo(t, wf, "gate-0", map[string]any{"score-0": c.score}); got != c.want {
			t.Errorf("gate-0 %s -> %s, want %s", c.name, got, c.want)
		}
	}

	miss := map[string]any{"score-r": scoreOut(60.0, false, 20, "")}
	if got := hardenGate(t, wf, 1, miss); got != "pass-mark" {
		t.Errorf("harden-gate round 2 of 3 failing -> %s, want pass-mark", got)
	}
	if got := hardenGate(t, wf, 2, miss); got != "report" {
		t.Errorf("harden-gate round 3 of 3 failing -> %s, want report", got)
	}
	if got := hardenGate(t, wf, 0, map[string]any{}); got != "pass-mark" {
		t.Errorf("harden-gate round 1 with no score-r (voided) -> %s, want pass-mark", got)
	}
	if got := hardenGate(t, wf, 0, map[string]any{"score-r": scoreOut(80.0, true, 20, "")}); got != "report" {
		t.Errorf("harden-gate round 1 passing -> %s, want report", got)
	}
	if got := hardenGate(t, wf, 0, map[string]any{"score-r": allExcluded}); got != "report" {
		t.Errorf("harden-gate round 1 all excluded -> %s, want report", got)
	}
}

// No runnable language (nothing changed, or no registered tool) skips straight
// to report, which records result skipped and gate passed.
func TestHardenHasWorkSkips(t *testing.T) {
	wf := loadSample(t, "harden")
	if got := routeTo(t, wf, "has-work?", map[string]any{"detect": map[string]any{"runnable_count": float64(0), "skip_reason": "no changed source files"}}); got != "report" {
		t.Errorf("runnable_count 0 -> %s, want report", got)
	}
	if got := routeTo(t, wf, "has-work?", map[string]any{"detect": map[string]any{"runnable_count": float64(2)}}); got != "mutate" {
		t.Errorf("runnable_count 2 -> %s, want mutate", got)
	}
	if reachableAvoiding(wf, "init", "mutate", "has-work?") {
		t.Error("init reaches mutate without has-work?")
	}
}

// Each round: strengthen, the tests-only tamper gate, equivalence claims,
// rescore, then the gate. A voided round still counts (harden-inc) and skips
// the claims and the rescore. No human step sits in the loop.
func TestHardenRoundWiring(t *testing.T) {
	wf := loadSample(t, "harden")
	for _, chain := range [][]string{
		{"init", "promote", "resolve-inputs", "scope", "classify", "detect", "has-work?"},
		{"mutate", "score-0", "gate-0"},
		{"pass-mark", "state-snapshot", "strengthen", "clean-check", "state-check", "dirty?", "tamper-gate", "tamper-route"},
		{"tamper-context", "tamper-judge", "tamper-verdict", "voided?"},
		{"checkpoint-push-round", "count-claims", "claims?", "judge-equivalent", "record-equivalence", "rescore", "score-r", "harden-inc", "harden-gate"},
		{"void-round", "harden-inc"},
		{"void-dirty", "harden-inc"},
		{"void-state", "harden-inc"},
		{"report", "finish"},
	} {
		for i := 0; i+1 < len(chain); i++ {
			if !hasEdge(wf, chain[i], chain[i+1]) {
				t.Errorf("missing edge %s -> %s", chain[i], chain[i+1])
			}
		}
	}
	if got := routeTo(t, wf, "claims?", map[string]any{"count-claims": map[string]any{"claim_count": float64(0)}}); got != "rescore" {
		t.Errorf("no claims -> %s, want rescore", got)
	}
	if got := routeTo(t, wf, "claims?", map[string]any{"count-claims": map[string]any{"claim_count": float64(2)}}); got != "judge-equivalent" {
		t.Errorf("2 claims -> %s, want judge-equivalent", got)
	}
	if got := routeTo(t, wf, "voided?", map[string]any{"tamper-verdict": map[string]any{"voided": true}}); got != "harden-inc" {
		t.Errorf("voided verdict -> %s, want harden-inc", got)
	}
	if got := routeTo(t, wf, "voided?", map[string]any{"tamper-verdict": map[string]any{"voided": false}}); got != "checkpoint-push-round" {
		t.Errorf("standing verdict -> %s, want checkpoint-push-round", got)
	}
	for _, id := range []string{"strengthen", "judge-equivalent", "tamper-judge", "rescore"} {
		if !onCycle(wf, id) {
			t.Errorf("%s is not on the round loop", id)
		}
	}
	for _, n := range wf.Nodes {
		if workflow.IsInteractive(n.Type) {
			t.Errorf("node %s is %s; harden has no human step", n.ID, n.Type)
		}
	}
	for id, args := range map[string][]string{
		"mutate":  {},
		"rescore": {"--survivors-only"},
	} {
		n := nodeByID(wf, id)
		if n.Config.ScriptFile != "scripts/mutate.sh" || !slices.Equal(n.Config.Args, args) || n.Config.TimeoutSeconds < 3600 {
			t.Errorf("%s = %s %q timeout %d, want scripts/mutate.sh %q with timeout >= 3600", id, n.Config.ScriptFile, n.Config.Args, n.Config.TimeoutSeconds, args)
		}
		// Adapter failures are deterministic (a missing tool, report drift):
		// a retry would rerun an hour-long mutation run to the same failure.
		if n.ErrorBehavior.Type != "halt" {
			t.Errorf("%s errorBehavior = %+v, want halt (no retry)", id, n.ErrorBehavior)
		}
	}
}

// The mutation adapters refuse a dirty tree, so a strengthen round that leaves
// uncommitted changes would fail the whole stage at rescore. clean-check
// discards them and the round is voided (it still counts), so the stage still
// ends done.
func TestHardenDirtyTreeVoidsRound(t *testing.T) {
	wf := loadSample(t, "harden")
	n := nodeByID(wf, "clean-check")
	if n.Config.ScriptFile != "scripts/clean-check.sh" || !slices.Equal(n.Config.Args, []string{"--discard"}) {
		t.Errorf("clean-check = %s %q, want scripts/clean-check.sh [--discard]", n.Config.ScriptFile, n.Config.Args)
	}
	if got := routeTo(t, wf, "dirty?", map[string]any{"clean-check": map[string]any{"dirty": true, "discarded": true}}); got != "void-dirty" {
		t.Errorf("dirty tree -> %s, want void-dirty", got)
	}
	if got := routeTo(t, wf, "dirty?", map[string]any{"clean-check": map[string]any{"dirty": false}}); got != "tamper-gate" {
		t.Errorf("clean tree -> %s, want tamper-gate", got)
	}
	for _, next := range []string{"tamper-gate", "rescore", "harden-inc"} {
		if reachableAvoiding(wf, "strengthen", next, "clean-check") {
			t.Errorf("strengthen reaches %s without clean-check", next)
		}
	}
	if reachableAvoiding(wf, "void-dirty", "rescore", "pass-mark") {
		t.Error("a dirty round reaches rescore before the next pass-mark")
	}
}

// Each standing round is pushed once the tamper gate has ruled, so a crash
// mid-loop keeps the tests it added. Voids reset to the round base, which the
// previous round already pushed, so they need no push.
func TestHardenStandingRoundIsPushed(t *testing.T) {
	wf := loadSample(t, "harden")
	if got := nodeByID(wf, "checkpoint-push-round").Config.ScriptFile; got != "@lib/checkpoint-push.sh" {
		t.Errorf("checkpoint-push-round script = %q, want @lib/checkpoint-push.sh", got)
	}
	if reachableAvoiding(wf, "strengthen", "checkpoint-push-round", "tamper-gate") {
		t.Error("strengthen reaches checkpoint-push-round without crossing tamper-gate")
	}
	if reachableAvoiding(wf, "pass-mark", "rescore", "checkpoint-push-round") {
		t.Error("a round reaches rescore without checkpoint-push-round")
	}
	for _, v := range []string{"void-round", "void-dirty", "harden-inc"} {
		if reachableAvoiding(wf, v, "checkpoint-push-round", "pass-mark") {
			t.Errorf("%s reaches checkpoint-push-round before the next pass-mark", v)
		}
	}
}

// strengthen can write {{workspace}}/harden, and rescore keeps earlier kills,
// so the scoring state is hashed before it runs and verified after. An edit
// restores the state and voids the round as a whole; the void's reason names
// it, and a dirty tree still wins (its void resets the same way).
func TestHardenStateIsGuarded(t *testing.T) {
	wf := loadSample(t, "harden")
	snap, check := nodeByID(wf, "state-snapshot"), nodeByID(wf, "state-check")
	if snap.Config.ScriptFile != "scripts/state-guard.sh" || !slices.Equal(snap.Config.Args, []string{"--snapshot"}) {
		t.Errorf("state-snapshot = %s %q, want scripts/state-guard.sh [--snapshot]", snap.Config.ScriptFile, snap.Config.Args)
	}
	if check.Config.ScriptFile != "scripts/state-guard.sh" || !slices.Equal(check.Config.Args, []string{"--verify", "state-snapshot"}) {
		t.Errorf("state-check = %s %q, want scripts/state-guard.sh [--verify state-snapshot]", check.Config.ScriptFile, check.Config.Args)
	}
	if reachableAvoiding(wf, "pass-mark", "strengthen", "state-snapshot") {
		t.Error("a round reaches strengthen without state-snapshot")
	}
	for _, next := range []string{"tamper-gate", "rescore", "harden-inc", "report"} {
		if reachableAvoiding(wf, "strengthen", next, "state-check") {
			t.Errorf("strengthen reaches %s without state-check", next)
		}
	}
	tampered := map[string]any{"clean-check": map[string]any{"dirty": false}, "state-check": map[string]any{"tampered": true}}
	if got := routeTo(t, wf, "dirty?", tampered); got != "void-state" {
		t.Errorf("tampered state -> %s, want void-state", got)
	}
	both := map[string]any{"clean-check": map[string]any{"dirty": true}, "state-check": map[string]any{"tampered": true}}
	if got := routeTo(t, wf, "dirty?", both); got != "void-dirty" {
		t.Errorf("dirty tree and tampered state -> %s, want void-dirty", got)
	}
	clean := map[string]any{"clean-check": map[string]any{"dirty": false}, "state-check": map[string]any{"tampered": false}}
	if got := routeTo(t, wf, "dirty?", clean); got != "tamper-gate" {
		t.Errorf("clean round -> %s, want tamper-gate", got)
	}
	want := map[string][]string{
		"void-dirty": {"{{pass-mark.sha}}", "--force-void", "--reason", "uncommitted_changes_discarded", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"},
		"void-state": {"{{pass-mark.sha}}", "--force-void", "--reason", "score_state_edited", "--reset", "--clear", "harden/equivalence-claims.json", "--clear", "harden/equivalence-verdicts.json"},
	}
	for id, args := range want {
		n := nodeByID(wf, id)
		if n.Config.ScriptFile != "@lib/tamper-verdict.sh" || !slices.Equal(n.Config.Args, args) {
			t.Errorf("%s = %s %q, want @lib/tamper-verdict.sh %q", id, n.Config.ScriptFile, n.Config.Args, args)
		}
	}
	if reachableAvoiding(wf, "void-state", "rescore", "pass-mark") {
		t.Error("a voided state round reaches rescore before the next pass-mark")
	}
}
