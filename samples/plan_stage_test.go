package samples_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

// planRound names the nodes of one alignment pass of the plan stage. Every pass
// is unrolled into its own nodes: the human rounds are interactive, and an
// interactive node must never sit on a back-edge.
type planRound struct {
	suffix string
	// open is where the pass's dependency gate sends unresolved gaps: the
	// next human round, or (after the last one) mark-unresolved.
	open string
}

func (r planRound) id(base string) string { return base + r.suffix }

var planRounds = []planRound{
	{"", "resolve-deps"},
	{"-2", "resolve-deps-2"},
	{"-3", "mark-unresolved"},
}

// The plan stage asks the person at most twice, then stops.
//
// Run af108303 lost ~6.5h: the second assessment found a NEW decision after
// the one human round, and deps-gate-2 sent the stage straight to
// mark-unresolved. Now the second gate opens a second human round
// (resolve-deps-2), and only the third gate blocks. Every gate routes on
// deps-count (a script that fails closed), never on a field the LLM may or may
// not emit (run 5e86d796).
func TestPlanDepsGatesAskTheHumanAtMostTwice(t *testing.T) {
	wf := loadSample(t, "plan")
	// What run 5e86d796's deps-assess reported: nothing blocking, 7 open items.
	llmSaidNothingBlocks := map[string]any{
		"blocking_count": float64(0), "has_blocking": false,
		"unresolved_count": float64(7), "has_unresolved": true,
	}
	for _, r := range planRounds {
		gate, counter, assess := r.id("deps-gate"), r.id("deps-count"), r.id("deps-assess")
		out := map[string]any{counter: map[string]any{"needs_resolution": float64(7)}, assess: llmSaidNothingBlocks}
		if got := routeTo(t, wf, gate, out); got != r.open {
			t.Errorf("%s with 7 decisions -> %q, want %q", gate, got, r.open)
		}
		if got := routeTo(t, wf, gate, map[string]any{counter: map[string]any{"needs_resolution": float64(0)}}); got != "mark-resolved" {
			t.Errorf("%s with 0 -> %q, want mark-resolved", gate, got)
		}
		// deps-count fails closed with needs_resolution=1 on unreadable output.
		if got := routeTo(t, wf, gate, map[string]any{counter: map[string]any{"needs_resolution": float64(1)}}); got == "mark-resolved" {
			t.Errorf("%s with unreadable assess output proceeded", gate)
		}
		// A missing counter output must not proceed either.
		if got := routeTo(t, wf, gate, map[string]any{}); got == "mark-resolved" {
			t.Errorf("%s with no counter output proceeded", gate)
		}
	}
	// A later gate reads its own counter: a stale earlier 0 cannot unblock it.
	stale := map[string]any{
		"deps-count":   map[string]any{"needs_resolution": float64(0)},
		"deps-count-2": map[string]any{"needs_resolution": float64(0)},
		"deps-count-3": map[string]any{"needs_resolution": float64(3)},
	}
	if got := routeTo(t, wf, "deps-gate-3", stale); got != "mark-unresolved" {
		t.Errorf("deps-gate-3 read an earlier counter: -> %q, want mark-unresolved", got)
	}
}

// Each counter counts its own assessment and the plan audits of its own pass,
// and each pass's chain is wired end to end.
func TestPlanRoundsAreWired(t *testing.T) {
	wf := loadSample(t, "plan")
	for _, r := range planRounds {
		n := nodeByID(wf, r.id("deps-count"))
		want := []string{r.id("deps-assess"), r.id("plan-audit"), r.id("recheck-plans")}
		if n.Config.ScriptFile != "scripts/deps-count.sh" || !slices.Equal(n.Config.Args, want) {
			t.Errorf("%s = %s %q, want scripts/deps-count.sh %q", n.ID, n.Config.ScriptFile, n.Config.Args, want)
		}
	}
	for _, chain := range [][]string{
		{"checkpoint-push-plans", "align-plans", "plan-audit", "steps-gate"},
		{"deps-assess", "deps-count", "deps-gate"},
		{"resolve-deps", "push-resolution", "align-plans-2", "plan-audit-2", "steps-gate-2"},
		{"deps-assess-2", "deps-count-2", "deps-gate-2"},
		{"resolve-deps-2", "push-resolution-2", "align-plans-3", "plan-audit-3", "steps-gate-3"},
		{"deps-assess-3", "deps-count-3", "deps-gate-3"},
		{"mark-resolved", "list-plans", "checkpoint-push", "finish"},
		{"mark-unresolved", "list-plans"},
	} {
		for i := 0; i+1 < len(chain); i++ {
			if !hasEdge(wf, chain[i], chain[i+1]) {
				t.Errorf("missing edge %s -> %s", chain[i], chain[i+1])
			}
		}
	}
	// The second human round is the first one, pointed at the second pass.
	r1, r2 := nodeByID(wf, "resolve-deps"), nodeByID(wf, "resolve-deps-2")
	if r2.Type != workflow.NodeTypeInteractiveAgent || r2.Config.ModelProfile != r1.Config.ModelProfile ||
		r2.Config.OrchestrationFile != r1.Config.OrchestrationFile || r2.Config.TemplateFile != "templates/resolve-deps-2.md" {
		t.Errorf("resolve-deps-2 = %s %q %q %q, want an interactive_agent like resolve-deps with templates/resolve-deps-2.md",
			r2.Type, r2.Config.ModelProfile, r2.Config.OrchestrationFile, r2.Config.TemplateFile)
	}
	if nodeByID(wf, "push-resolution-2").Config.ScriptFile != "@lib/checkpoint-push.sh" {
		t.Error("push-resolution-2 must push the second round's commits")
	}
}

// Every alignment pass is followed by the deterministic step audit, and no
// path reaches the pass's dependency assessment without crossing it. A plan
// left without checkbox steps gets ONE restore attempt, re-audited; if it is
// still stepless the stage stops. It never passes.
//
// WHY (remote-runners, 2026-11-23): align-plans rewrote full TDD plans into
// checkbox-less prose and align-plans-2 added tasks without steps; implement
// skipped them as done and 47 tasks blocked on what they should have built.
func TestPlanStepAuditGuardsEveryAlignPass(t *testing.T) {
	wf := loadSample(t, "plan")
	okAudit := func(ok bool) map[string]any { return map[string]any{"ok": ok} }
	for _, r := range planRounds {
		align, audit, gate := r.id("align-plans"), r.id("plan-audit"), r.id("steps-gate")
		restore, recheck, rgate := r.id("restore-steps"), r.id("recheck-plans"), r.id("restore-gate")
		assess, stop := r.id("deps-assess"), r.id("mark-stepless")

		for _, id := range []string{audit, recheck} {
			if n := nodeByID(wf, id); n.Type != workflow.NodeTypeScript || n.Config.ScriptFile != "scripts/plan-audit.sh" {
				t.Errorf("%s = %s %q, want script scripts/plan-audit.sh", id, n.Type, n.Config.ScriptFile)
			}
		}
		if !hasEdge(wf, align, audit) || !hasEdge(wf, audit, gate) {
			t.Errorf("want %s -> %s -> %s", align, audit, gate)
		}
		if reachableAvoiding(wf, align, assess, audit) {
			t.Errorf("%s reaches %s without crossing %s", align, assess, audit)
		}
		if got := routeTo(t, wf, gate, map[string]any{audit: okAudit(true)}); got != assess {
			t.Errorf("%s with ok audit -> %q, want %s", gate, got, assess)
		}
		if got := routeTo(t, wf, gate, map[string]any{audit: okAudit(false)}); got != restore {
			t.Errorf("%s with stepless plans -> %q, want %s", gate, got, restore)
		}
		if got := routeTo(t, wf, gate, map[string]any{}); got != restore {
			t.Errorf("%s with no audit output -> %q, want %s (fail closed)", gate, got, restore)
		}
		if !hasEdge(wf, restore, recheck) || !hasEdge(wf, recheck, rgate) {
			t.Errorf("want %s -> %s -> %s", restore, recheck, rgate)
		}
		if got := routeTo(t, wf, rgate, map[string]any{recheck: okAudit(true)}); got != assess {
			t.Errorf("%s with restored steps -> %q, want %s", rgate, got, assess)
		}
		if got := routeTo(t, wf, rgate, map[string]any{recheck: okAudit(false)}); got != stop {
			t.Errorf("%s still stepless -> %q, want %s", rgate, got, stop)
		}
		if got := routeTo(t, wf, rgate, map[string]any{}); got != stop {
			t.Errorf("%s with no re-audit output -> %q, want %s (fail closed)", rgate, got, stop)
		}
		if !hasEdge(wf, stop, "list-plans") {
			t.Errorf("want %s -> list-plans", stop)
		}
		if n := nodeByID(wf, restore); n.Type != workflow.NodeTypeAgentCall || n.Config.TemplateFile != "templates/restore-steps.md" {
			t.Errorf("%s = %s %q, want agent_call templates/restore-steps.md", restore, n.Type, n.Config.TemplateFile)
		}
	}
}

// The steps gate reads the real audit: a committed plan with no checkbox
// steps sends the pass to the restore, a fully stepped one proceeds.
func TestPlanStepsGateRoutesOnTheRealAudit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	wf := loadSample(t, "plan")
	for _, c := range []struct {
		name, body, want string
	}{
		{"stepped", "# T\n- [ ] Step 1: write the failing test\n", "deps-assess"},
		{"prose", "# T\nThe producer is wired into the server.\n", "restore-steps"},
	} {
		t.Run(c.name, func(t *testing.T) {
			work := t.TempDir()
			dir := filepath.Join(work, "docs", "plans", "x")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "2026-11-24-phase-1-task-1.md"), []byte(c.body), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{
				{"init", "-q", "-b", "main"}, {"add", "-A"},
				{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "plans"},
			} {
				if out, err := exec.Command("git", append([]string{"-C", work}, args...)...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			in, _ := json.Marshal(map[string]any{"work_dir": work, "plan_dir": "docs/plans/x", "run_date": "2026-11-24"})
			cmd := exec.Command("sh", "plan/scripts/plan-audit.sh")
			cmd.Stdin = strings.NewReader(string(in))
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("plan-audit.sh: %v", err)
			}
			var audit map[string]any
			if err := json.Unmarshal(out, &audit); err != nil {
				t.Fatalf("plan-audit output %q: %v", out, err)
			}
			if got := routeTo(t, wf, "steps-gate", map[string]any{"plan-audit": audit}); got != c.want {
				t.Errorf("audit %v routed %q, want %q", audit, got, c.want)
			}
		})
	}
}

// Human-only plan steps found by the audit reach the person, even when the
// assessor classed nothing as open. Each gate runs the real deps-count.sh with
// its node's own args.
//
// WHY (lunaroute implement runs a5259bec / c028678b / 8fc37529): plans held
// operator observations and live vendor evidence no agent can produce, and
// implement stalled on them for hours.
func TestPlanHumanOnlyStepsReachTheHuman(t *testing.T) {
	wf := loadSample(t, "plan")
	for _, r := range planRounds {
		in := map[string]any{
			r.id("deps-assess"): map[string]any{"gaps": []any{}, "has_unresolved": false, "unresolved_count": 0},
			r.id("plan-audit"): map[string]any{"ok": true, "human_only": 1,
				"human_only_lines": []any{"docs/p.md:4: - [ ] Record operator observations of the banner"}},
		}
		counted := runDepsCount(t, nodeByID(wf, r.id("deps-count")).Config.Args, in)
		if got := routeTo(t, wf, r.id("deps-gate"), map[string]any{r.id("deps-count"): counted}); got != r.open {
			t.Errorf("%s with one human-only step -> %q, want %q", r.id("deps-gate"), got, r.open)
		}
	}
}

// runDepsCount runs plan/scripts/deps-count.sh with the given args over the
// given snapshot and returns its output.
func runDepsCount(t *testing.T, args []string, snapshot map[string]any) map[string]any {
	t.Helper()
	in, _ := json.Marshal(snapshot)
	cmd := exec.Command("sh", append([]string{"plan/scripts/deps-count.sh"}, args...)...)
	cmd.Stdin = strings.NewReader(string(in))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("deps-count.sh %v: %v", args, err)
	}
	var counted map[string]any
	if err := json.Unmarshal(out, &counted); err != nil {
		t.Fatalf("deps-count output %q: %v", out, err)
	}
	return counted
}

// runMark runs a mark-* transform over the given node outputs.
func runMark(t *testing.T, wf *workflow.Workflow, id string, outputs map[string]any) *variable.VariableStore {
	t.Helper()
	vars := variable.NewStore()
	for k, v := range outputs {
		vars.SetNodeOutput(k, v)
	}
	if _, err := engine.NewTransformExecutor().Execute(context.Background(), nodeByID(wf, id), vars, wf.Name); err != nil {
		t.Fatal(err)
	}
	return vars
}

// The marks feed finish: plan_status is the manifest status, deps_resolved a
// bool, deps_gaps the JSON text of the last counter's gaps when blocked (§3.3),
// or of the re-audit's gaps when a pass stopped on stepless plans.
func TestPlanMarksRecordTheStageOutcome(t *testing.T) {
	wf := loadSample(t, "plan")
	check := func(id string, outputs map[string]any, wantStatus string, wantOK bool, wantGaps int) {
		t.Helper()
		v := runMark(t, wf, id, outputs)
		st, _ := v.Get("plan_status")
		ok, _ := v.Get("deps_resolved")
		gaps, _ := v.Get("deps_gaps")
		if st != wantStatus || ok != wantOK {
			t.Errorf("%s: plan_status=%v deps_resolved=%#v, want %s / %v", id, st, ok, wantStatus, wantOK)
		}
		if wantGaps == 0 {
			if gaps != "" {
				t.Errorf("%s: deps_gaps = %#v, want \"\"", id, gaps)
			}
			return
		}
		s, isStr := gaps.(string)
		var parsed []string
		if !isStr || json.Unmarshal([]byte(s), &parsed) != nil || len(parsed) != wantGaps {
			t.Errorf("%s: deps_gaps = %#v, want the JSON text of %d gap lines", id, gaps, wantGaps)
		}
	}
	check("mark-unresolved", map[string]any{"deps-count-3": map[string]any{
		"gaps": []any{"DECISION: a.md — x; decision: y?", "HUMAN-ONLY: p.md:3: - [ ] Manually verify"}}}, "blocked", false, 2)
	check("mark-resolved", nil, "done", true, 0)
	for _, r := range planRounds {
		check(r.id("mark-stepless"), map[string]any{r.id("recheck-plans"): map[string]any{
			"gaps": []any{"STEPLESS: p.md has no checkbox steps"}}}, "blocked", false, 1)
	}
}

// Human rounds are bounded by unrolling, never by a loop: nothing in the plan
// graph may cycle.
func TestPlanIsAcyclic(t *testing.T) {
	wf := loadSample(t, "plan")
	for _, n := range wf.Nodes {
		if onCycle(wf, n.ID) {
			t.Errorf("node %s sits on a cycle", n.ID)
		}
	}
	interactive := 0
	for _, n := range wf.Nodes {
		if n.Type == workflow.NodeTypeInteractiveAgent || n.Type == workflow.NodeTypeInteractiveLLM {
			interactive++
		}
	}
	if interactive != 2 {
		t.Errorf("plan has %d interactive nodes, want 2 (resolve-deps, resolve-deps-2)", interactive)
	}
}

// Agent timeouts are sized for features with 5+ design phases: produce-plans
// needed ~2.5h on lunaroute and timed out 3 x 3600s. The timeout is static
// (timeoutSeconds is an int, not a template), so it is set generously.
func TestPlanAgentTimeoutsFitLargeFeatures(t *testing.T) {
	wf := loadSample(t, "plan")
	min := map[string]int{"produce-plans": 14400}
	for _, r := range planRounds {
		min[r.id("align-plans")] = 7200
		min[r.id("restore-steps")] = 7200
	}
	for id, want := range min {
		if got := nodeByID(wf, id).Config.TimeoutSeconds; got < want {
			t.Errorf("%s timeoutSeconds = %d, want >= %d", id, got, want)
		}
	}
}
