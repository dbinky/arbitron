package samples_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
)

// A guard that cannot reach the path it guards.
//
// count-review-plan exists to make loop termination arithmetic rather than
// narrative: it counts the unticked approval boxes and emits plan_complete. Its
// own header cites run ae3ec85f, which shipped a PR whose review plan was 70%
// unexecuted. But the counter only ever informed the TOOLLESS llm_call assessor.
// The deterministic exit — refine-gate — never read it.
//
// So run a1aab074 ended at pass 161 of a 500 cap on the stagnation backstop,
// with count-review-plan.plan_complete=false and unchecked_boxes=98 recorded on
// that very pass: 3 of 52 single-system reviews and ALL 92 paired integration
// reviews outstanding. The counter was screaming and the gate could not hear it.
// It then opened a PR presenting that as a finished review.
//
// ralph-loop's gate omitted plan_complete entirely. feature-pipeline's gate did
// list it — but SHADOWED, at index 4, behind an unconditional
// `consecutive_no_progress >= 3 -> exit` at index 2. The engine's conditional is
// first-match (domain/engine/executor_conditional.go), so a listed-but-later
// condition is not a guard. Same class, two workflows.
//
// The invariant below is the fix expressed as a rule, so the next loop gate
// cannot reintroduce it:
//
//  1. A gate that can exit on stagnation must READ the arithmetic counter.
//  2. A stagnation condition may route to `exit` only at the workflow's declared
//     hard-wedge threshold — never at a bare literal. A withheld approval is a
//     normal, expected outcome of a review pass; three of them in a row is not
//     evidence of a wedged loop, and a1aab074 was not wedged when it was killed.
//  3. That threshold must be a declared input, so it is documented and tunable
//     without manifest surgery on a live run (instance snapshots are frozen).
//  4. Its default must be at least the approval-stall limit that
//     record-pass-commit uses to force progress_evidence=false. Below that, the
//     stagnation streak can hit its threshold before the corroborating stall
//     signal has even had the chance to trip, which is exactly how a healthy run
//     gets scored as wedged.
const (
	planCompleteField = "count-review-plan.plan_complete"
	stagnationField   = ".consecutive_no_progress"
	wedgeInput        = "max_no_progress_streak"

	// record-pass-commit.sh's APPROVAL_STALL_LIMIT. Keep in step with it.
	approvalStallLimit = 8
)

func isStagnation(c workflow.Condition) bool {
	return strings.HasSuffix(c.Field, stagnationField) || c.Field == wedgeInput
}

func referencesWedgeInput(v any) bool {
	s, ok := v.(string)
	return ok && strings.Contains(s, "{{"+wedgeInput+"}}")
}

// countsReviewPlan reports whether the workflow runs the arithmetic counter at
// all. A workflow without one (the minimal native-ralph-loop example) has no
// evidence for a gate to ignore, so the invariant does not apply to it.
func countsReviewPlan(wf *workflow.Workflow) bool {
	for _, n := range wf.Nodes {
		if n.ID == "count-review-plan" {
			return true
		}
	}
	return false
}

func TestLoopGateCannotExitOnStagnationAloneWhileWorkRemains(t *testing.T) {
	for _, name := range []string{"pr-review", "feature-pipeline", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			wf := loadSample(t, name)
			if !countsReviewPlan(wf) {
				return
			}

			gates := 0
			for _, n := range wf.Nodes {
				if n.Type != workflow.NodeTypeConditional {
					continue
				}
				var stagnationExits, readsPlanComplete bool
				for _, c := range n.Config.Conditions {
					if c.Field == planCompleteField {
						readsPlanComplete = true
					}
					if isStagnation(c) && c.Output == "exit" {
						stagnationExits = true
						// Rule 2: the wedge threshold is declared, not inlined.
						if !referencesWedgeInput(c.Value) {
							t.Errorf("%s/%s: stagnation condition exits at literal %v; "+
								"it must exit only at {{%s}} so the hard-wedge threshold is "+
								"explicit and tunable (a bare 3 killed run a1aab074 at 161 "+
								"passes with 98 approval boxes outstanding)",
								name, n.ID, c.Value, wedgeInput)
						}
					}
				}
				if !stagnationExits {
					continue
				}
				gates++
				// Rule 1: the guard must be able to reach what it guards.
				if !readsPlanComplete {
					t.Errorf("%s/%s: gate exits on stagnation but never reads %s; "+
						"the arithmetic completion evidence is unreachable from the "+
						"deterministic exit", name, n.ID, planCompleteField)
				}
			}
			if gates == 0 {
				t.Errorf("%s: runs count-review-plan but no conditional gate consults a "+
					"stagnation streak — the counter informs nothing deterministic", name)
			}
		})
	}
}

func TestWedgeThresholdIsDeclaredAndNotTighterThanTheStallSignal(t *testing.T) {
	for _, name := range []string{"feature-pipeline", "ralph-loop"} {
		t.Run(name, func(t *testing.T) {
			wf := loadSample(t, name)

			var found *workflow.InputField
			for i := range wf.Inputs {
				if wf.Inputs[i].Name == wedgeInput {
					found = &wf.Inputs[i]
				}
			}
			if found == nil {
				t.Fatalf("%s: no %q input declared; the hard-wedge threshold must be a "+
					"documented, tunable input rather than a literal buried in a gate",
					name, wedgeInput)
			}
			got, err := asNumber(found.Default)
			if err != nil {
				t.Fatalf("%s: %s default %v: %v", name, wedgeInput, found.Default, err)
			}
			// Rule 4: below the approval-stall limit the streak can trip before
			// record-pass-commit's corroborating signal has had a chance to.
			if got < approvalStallLimit {
				t.Errorf("%s: %s default = %v, below record-pass-commit's approval stall "+
					"limit of %d; a streak that can end a run before the stall flag could "+
					"even have tripped is scoring normal review withholds as a wedge",
					name, wedgeInput, got, approvalStallLimit)
			}
		})
	}
}

func asNumber(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("not numeric (%T)", v)
	}
}

// The counter must also be able to authorize an exit, not only veto one.
// ralph-loop reaches "nothing outstanding" through work-router's default port,
// which is correct but leaves the gate itself silent on completion — so a reader
// of refine-gate cannot tell that termination is arithmetic at all. Stating it in
// the gate makes the dominant condition legible where the exit actually happens.
func TestRalphGateExitsOnACompletedPlan(t *testing.T) {
	wf := loadSample(t, "ralph-loop")
	for _, n := range wf.Nodes {
		if n.ID != "refine-gate" {
			continue
		}
		for _, c := range n.Config.Conditions {
			if c.Field == planCompleteField && c.Operator == "eq" &&
				fmt.Sprintf("%v", c.Value) == "true" && c.Output == "exit" {
				return
			}
		}
		t.Fatalf("ralph-loop/refine-gate: no `%s eq true -> exit` condition; the "+
			"arithmetic completion signal cannot end the loop", planCompleteField)
	}
	t.Fatal("ralph-loop: no refine-gate node")
}
