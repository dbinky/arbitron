package instance

import "testing"

// The terminal-state definition lives in ONE place so the resumption
// short-circuit, the child-terminal wake, and the startup reconciler cannot
// disagree about when a child is "done".
func TestStateIsTerminal(t *testing.T) {
	cases := map[State]bool{
		StateCompleted: true,
		StateFailed:    true,
		StateCancelled: true,
		StateQueued:    false,
		StateRunning:   false,
		StateWaiting:   false,
		StatePaused:    false,
		State("bogus"): false,
	}
	for st, want := range cases {
		if got := st.IsTerminal(); got != want {
			t.Errorf("State(%q).IsTerminal() = %v, want %v", st, got, want)
		}
	}
}

// A cancelled child is TERMINAL and routes the parent through failure — the
// spec is explicit that there is no separate cancellation path.
func TestCancelledChildIsTerminal(t *testing.T) {
	if !StateCancelled.IsTerminal() {
		t.Fatal("a cancelled child must be terminal so its parent resumes through failure")
	}
}

func TestChildLinkShape(t *testing.T) {
	l := ChildLink{ChildState: StateCompleted}
	if !l.ChildState.IsTerminal() {
		t.Fatal("ChildLink must carry the child's state so the executor can pick a port")
	}
}
