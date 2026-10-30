package samples_test

import (
	"strings"
	"testing"
)

func TestPRReviewProfileRemap(t *testing.T) {
	wf := loadSample(t, "pr-review")
	for node, want := range map[string]string{
		"manager":         "reviewer-codex-high",
		"senior-engineer": "coder-sonnet5-high",
		"verify-fix":      "approver-codex-high",
	} {
		if got := nodeByID(wf, node).Config.ModelProfile; got != want {
			t.Errorf("%s model_profile = %q, want %q", node, got, want)
		}
	}
	if d := nodeByID(wf, "verify-fix").Description; !strings.Contains(d, "Sonnet 5.5") {
		t.Errorf("verify-fix description does not mention Sonnet 5.5: %q", d)
	}
	if wf.GitVariable != "pr_branch" {
		t.Errorf("gitVariable = %q, want pr_branch", wf.GitVariable)
	}
}

func TestPRReviewProfilesTableMatches(t *testing.T) {
	wf := loadSample(t, "pr-review")
	table := profileTable(t, "pr-review")
	for _, n := range wf.Nodes {
		if a := n.Config.ModelProfile; a != "" {
			if _, ok := table[a]; !ok {
				t.Errorf("node %s alias %q missing from PROFILES.md", n.ID, a)
			}
		}
	}
	for _, gone := range []string{"reviewer-sonnet5-high", "coder-opus-medium"} {
		if _, ok := table[gone]; ok {
			t.Errorf("PROFILES.md still lists %q", gone)
		}
	}
}
