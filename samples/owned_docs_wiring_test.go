package samples_test

import (
	"slices"
	"testing"
)

// A stage that regenerates a dated document set clears the set it owns first,
// so a same-day rerun with fewer files cannot publish the first run's extras
// (list-plans and the design globs match by date, not by generation).
func TestStagesClearTheirOwnedDocsBeforeGenerating(t *testing.T) {
	for _, c := range []struct {
		sample, clear, generator string
		args                     []string
	}{
		{"plan", "clear-plans", "produce-plans",
			[]string{"--message", "docs: clear {{slug}} plans for regeneration", "{{plan_dir}}/{{run_date}}-phase-*-task-*.md"}},
		{"design", "clear-designs", "brainstorm-design",
			[]string{"--message", "docs: clear {{slug}} designs for regeneration", "{{design_glob}}"}},
	} {
		t.Run(c.sample, func(t *testing.T) {
			wf := loadSample(t, c.sample)
			n := nodeByID(wf, c.clear)
			if n.Config.ScriptFile != "@lib/clear-owned-docs.sh" {
				t.Fatalf("%s script = %q", c.clear, n.Config.ScriptFile)
			}
			if !slices.Equal(n.Config.Args, c.args) {
				t.Errorf("%s args = %q, want %q", c.clear, n.Config.Args, c.args)
			}
			if !hasEdge(wf, "resolve-inputs", c.clear) || !hasEdge(wf, c.clear, c.generator) {
				t.Errorf("want resolve-inputs -> %s -> %s", c.clear, c.generator)
			}
			if reachableAvoiding(wf, "resolve-inputs", c.generator, c.clear) {
				t.Errorf("%s is reachable without crossing %s", c.generator, c.clear)
			}
		})
	}
}
