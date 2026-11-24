package samples_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
)

// authGate is one auth preflight in the parent: auth-<name> runs
// @lib/auth-preflight.sh over the CLIs the next stage uses, auth-<name>-ok?
// routes on its ok, and fail-auth-<name> names the reason for notify-failed.
type authGate struct {
	name, from, next string
	clis             []string
}

// Run 68225980: an expired Claude OAuth surfaced as an opaque "harness process
// failed" and was retried for ~2h. Each unattended stage now checks the logins
// first; the start gate covers ideate, spec and design, which follow it at once
// with a person present.
var authGates = []authGate{
	{"start", "promote-check", "record-idea?", []string{"claude", "codex"}},
	{"plan", "pre-plan", "plan", []string{"claude", "codex"}},
	{"implement", "pre-implement", "implement", []string{"claude", "pi:openai-codex"}},
	{"harden", "pre-harden", "harden", []string{"claude", "codex"}},
	{"refine", "pre-refine", "refine", []string{"claude", "codex"}},
	{"review", "pre-review", "review", []string{"claude", "codex"}},
}

func TestParentAuthPreflightGatesEveryUnattendedStage(t *testing.T) {
	wf := loadSample(t, parentName)
	toStepFailed := workflow.ErrorBehavior{Type: "route", ErrorNodeID: "notify-step-failed"}
	for _, g := range authGates {
		t.Run(g.name, func(t *testing.T) {
			id, gate, fail := "auth-"+g.name, "auth-"+g.name+"-ok?", "fail-auth-"+g.name
			n, ok := findNode(wf, id)
			if !ok {
				t.Fatalf("no %s node", id)
			}
			if n.Type != workflow.NodeTypeScript || n.Config.ScriptFile != "@lib/auth-preflight.sh" {
				t.Errorf("%s = %s %s, want a script running @lib/auth-preflight.sh", id, n.Type, n.Config.ScriptFile)
			}
			if !slices.Equal(n.Config.Args, g.clis) {
				t.Errorf("%s args = %q, want %q", id, n.Config.Args, g.clis)
			}
			if n.Config.TimeoutSeconds <= 0 || n.Config.TimeoutSeconds > 180 {
				t.Errorf("%s timeoutSeconds = %d, want a short bound", id, n.Config.TimeoutSeconds)
			}
			if n.ErrorBehavior != toStepFailed {
				t.Errorf("%s errorBehavior = %+v, want %+v", id, n.ErrorBehavior, toStepFailed)
			}
			for _, e := range [][2]string{{g.from, id}, {id, gate}, {fail, "notify-failed"}} {
				if !hasEdge(wf, e[0], e[1]) {
					t.Errorf("missing edge %s -> %s", e[0], e[1])
				}
			}
			if hasEdge(wf, g.from, g.next) {
				t.Errorf("%s still goes straight to %s, past the preflight", g.from, g.next)
			}
			if es := outEdges(wf, id); len(es) != 1 {
				t.Errorf("%s has %d out edges, want only %s", id, len(es), gate)
			}
			// ok routes on; not ok, or no output at all, stops (fail closed).
			for _, c := range []struct {
				out  map[string]any
				want string
			}{
				{map[string]any{id: map[string]any{"ok": true}}, g.next},
				{map[string]any{id: map[string]any{"ok": false}}, fail},
				{map[string]any{id: map[string]any{}}, fail},
				{map[string]any{}, fail},
			} {
				if got := routeTo(t, wf, gate, c.out); got != c.want {
					t.Errorf("%s(%v) -> %s, want %s", gate, c.out, got, c.want)
				}
			}
			vars := parentStore(t, wf, nil)
			vars.SetNodeOutput(id, map[string]any{"ok": false, "summary": "agent CLI auth preflight failed: claude: expired"})
			runTransform(t, wf, fail, vars)
			for k, want := range map[string]string{"failed_stage": g.name, "failed_outcome": "blocked"} {
				if got, _ := vars.Get(k); fmt.Sprint(got) != want {
					t.Errorf("%s set %s = %v, want %s", fail, k, got, want)
				}
			}
			if got, _ := vars.Get("failed_reason"); !strings.Contains(fmt.Sprint(got), "claude: expired") {
				t.Errorf("%s failed_reason = %v, want the preflight summary", fail, got)
			}
		})
	}
}

// The notify fallback shows failed / blocked / done: every way into
// notify-failed sets failed_outcome, and notify-failed passes it on.
func TestParentNotifyFailedCarriesTheOutcome(t *testing.T) {
	wf := loadSample(t, parentName)
	n := nodeByID(wf, "notify-failed")
	if !slices.Contains(n.Config.Args, "Outcome={{failed_outcome}}") {
		t.Errorf("notify-failed args = %q, want an Outcome={{failed_outcome}} fact", n.Config.Args)
	}
	into := 0
	for _, e := range wf.Edges {
		if e.Target != "notify-failed" {
			continue
		}
		into++
		vars := parentStore(t, wf, nil)
		runTransform(t, wf, e.Source, vars)
		got, _ := vars.Get("failed_outcome")
		want := "failed"
		if strings.HasSuffix(e.Source, "-blocked") || strings.HasPrefix(e.Source, "fail-auth-") {
			want = "blocked"
		}
		if fmt.Sprint(got) != want {
			t.Errorf("%s sets failed_outcome = %v, want %s", e.Source, got, want)
		}
	}
	if into < len(parentStages)+len(authGates) {
		t.Errorf("%d edges into notify-failed, want every stage failure, block and auth stop", into)
	}
}

// After the added nodes, no two nodes share a canvas position.
func TestParentLayoutHasNoOverlaps(t *testing.T) {
	wf := loadSample(t, parentName)
	seen := map[workflow.NodePosition]string{}
	for _, n := range wf.Nodes {
		if n.Position == nil {
			t.Errorf("%s has no position", n.ID)
			continue
		}
		if other, dup := seen[*n.Position]; dup {
			t.Errorf("%s and %s share position %+v", n.ID, other, *n.Position)
		}
		seen[*n.Position] = n.ID
	}
}
