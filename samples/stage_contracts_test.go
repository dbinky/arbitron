package samples_test

import (
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
)

// stageContract is what a stage workflow promises its parent: a single `finish`
// sink that records the stage through @lib/manifest-append.sh with these
// output keys. Each stage task adds its row. An exact contract allows no key
// beyond keys.
type stageContract struct {
	workflow, stage, status string
	keys                    []string
	exact                   bool
}

var stageContracts = []stageContract{
	{workflow: "ideate", stage: "ideate", status: "done", keys: []string{"slug", "branch", "idea_path"}},
	{workflow: "spec", stage: "spec", status: "done", keys: []string{"spec_path"}},
	{workflow: "design", stage: "design", status: "done", keys: []string{"design_paths"}},
	{workflow: "plan", stage: "plan", status: "{{plan_status}}", keys: []string{"plan_paths", "deps_resolved", "deps_gaps"}},
	{workflow: "implement", stage: "implement", status: "{{impl_status}}", keys: []string{"status", "blocked_reason", "baseline_path"}},
	// A miss or a skip is still done: the score and gate travel as outputs.
	{workflow: "harden", stage: "harden", status: "done", exact: true,
		keys: []string{"result", "skip_reason", "score", "gate_passed", "survivors_path", "focus_seed_path", "harden_report_path"}},
}

var filterKeyRE = regexp.MustCompile(`(\w+):`)

// finishFilterKeys returns the object keys of the finish node's jq filter (its
// last arg). Every stage writes the filter in explicit `key: .path` form (P5),
// so the keys are the identifiers followed by a colon.
func finishFilterKeys(t *testing.T, wf *workflow.Workflow) []string {
	t.Helper()
	n, ok := findNode(wf, "finish")
	if !ok || len(n.Config.Args) == 0 {
		t.Fatalf("%s: no finish node with args", wf.Name)
	}
	var keys []string
	for _, m := range filterKeyRE.FindAllStringSubmatch(n.Config.Args[len(n.Config.Args)-1], -1) {
		keys = append(keys, m[1])
	}
	return keys
}

// sinks returns the ids of nodes with no outgoing edge.
func sinks(wf *workflow.Workflow) []string {
	out := map[string]bool{}
	for _, e := range wf.Edges {
		out[e.Source] = true
	}
	var ids []string
	for _, n := range wf.Nodes {
		if !out[n.ID] {
			ids = append(ids, n.ID)
		}
	}
	return ids
}

func TestFinishFilterKeys(t *testing.T) {
	wf := &workflow.Workflow{Name: "x", Nodes: []workflow.Node{{ID: "finish", Config: workflow.NodeConfig{
		Args: []string{"--workflow", "x", "spec", "done", `{spec_path: .["finalize-spec"].paths[0], b: {c: .d} }`},
	}}}}
	if got, want := finishFilterKeys(t, wf), []string{"spec_path", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("finishFilterKeys = %v, want %v", got, want)
	}
}

func TestSinks(t *testing.T) {
	if got := sinks(syntheticGraph()); !slices.Equal(got, []string{"end"}) {
		t.Errorf("sinks = %v, want [end]", got)
	}
}

func TestStageContract(t *testing.T) {
	for _, c := range stageContracts {
		t.Run(c.workflow, func(t *testing.T) {
			wf := loadSample(t, c.workflow)
			if got := sinks(wf); !slices.Equal(got, []string{"finish"}) {
				t.Fatalf("sinks = %v, want only finish", got)
			}
			n := nodeByID(wf, "finish")
			if n.Config.ScriptFile != "@lib/manifest-append.sh" {
				t.Errorf("finish scriptFile = %q, want @lib/manifest-append.sh", n.Config.ScriptFile)
			}
			want := []string{"--workflow", c.workflow, c.stage, c.status}
			if len(n.Config.Args) < 5 || !slices.Equal(n.Config.Args[:4], want) {
				t.Fatalf("finish args = %q, want prefix %q plus a filter", n.Config.Args, want)
			}
			keys := finishFilterKeys(t, wf)
			if c.exact && !slices.Equal(slices.Sorted(slices.Values(keys)), slices.Sorted(slices.Values(c.keys))) {
				t.Errorf("finish filter keys %v, want exactly %v", keys, c.keys)
			}
			for _, k := range c.keys {
				if !slices.Contains(keys, k) {
					t.Errorf("finish filter keys %v lack %q", keys, k)
				}
			}
		})
	}
}

// The jam is a free conversation ended by the human. A schema would make the
// sentinel demand structured output and invite the agent to end the jam early;
// the slug travels through ideate-result.json instead.
func TestIdeateJamHasNoOutputSchema(t *testing.T) {
	wf := loadSample(t, "ideate")
	n, ok := findNode(wf, "jam")
	if !ok {
		t.Fatal("ideate has no jam node")
	}
	if n.Type != workflow.NodeTypeInteractiveAgent {
		t.Errorf("jam type = %s, want %s", n.Type, workflow.NodeTypeInteractiveAgent)
	}
	if n.Config.OutputSchema != nil {
		t.Errorf("jam outputSchema = %v, want none", n.Config.OutputSchema)
	}
}

// The spec target is computed once in `promote` and the template must write
// exactly there: the slug is given (never re-derived), and pushing is the
// finish script's job.
func TestSpecTemplateTargetsSpecTarget(t *testing.T) {
	data, err := os.ReadFile("spec/templates/brainstorm-spec.md")
	if err != nil {
		t.Fatal(err)
	}
	tpl := string(data)
	for _, want := range []string{"./work/{{spec_target}}", "{{slug}}"} {
		if !strings.Contains(tpl, want) {
			t.Errorf("template lacks %q", want)
		}
	}
	for _, bad := range []string{"<slug>-spec.md", "git push"} {
		if strings.Contains(tpl, bad) {
			t.Errorf("template must not contain %q", bad)
		}
	}
}

// The design glob is shared by both finalize nodes and must be run-dated, so a
// rerun on a branch holding older designs never collects them.
func TestDesignGlobIsRunDated(t *testing.T) {
	wf := loadSample(t, "design")
	n, ok := findNode(wf, "promote")
	if !ok {
		t.Fatal("design has no promote node")
	}
	for _, op := range n.Config.Operations {
		if op.Variable != "design_glob" {
			continue
		}
		if v, _ := op.Value.(string); !strings.Contains(v, "{{init.run_date}}-{{slug}}-design-phase-*") {
			t.Errorf("design_glob = %q, want it run-dated and slug-scoped", v)
		}
		return
	}
	t.Error("promote sets no design_glob")
}

// Neither design prompt may push: the codex/opus sandboxes have no egress and
// `finish` pushes. The array of paths must also stay typed, so finish reads it
// straight from finalize-aligned rather than through a transform.
func TestDesignOrchHasNoPush(t *testing.T) {
	for _, f := range []string{
		"design/orchestration/brainstorm-design.orch.md",
		"design/templates/brainstorm-design.md",
		"design/templates/align-designs.md",
	} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "git push") || strings.Contains(string(data), "push --quiet") {
			t.Errorf("%s must not push", f)
		}
	}
	wf := loadSample(t, "design")
	n := nodeByID(wf, "finish")
	if got := n.Config.Args[len(n.Config.Args)-1]; !strings.Contains(got, `design_paths: .["finalize-aligned"].paths`) {
		t.Errorf("finish filter = %q, want design_paths read from finalize-aligned", got)
	}
	if _, ok := findNode(wf, "brainstorm-design"); !ok {
		t.Fatal("no brainstorm-design node")
	}
}

// Orchestration files are not variable-resolved, so the spec path and idea doc
// must be wired through the template.
func TestDesignTemplatesWireInputs(t *testing.T) {
	bd, _ := os.ReadFile("design/templates/brainstorm-design.md")
	for _, want := range []string{"{{resolve-inputs.inputs.spec_path}}", "{{resolve-inputs.inputs.idea_path}}", "./work/docs/designs/{{run_date}}-{{slug}}-design-phase-N.md"} {
		if !strings.Contains(string(bd), want) {
			t.Errorf("brainstorm-design.md lacks %q", want)
		}
	}
	ad, _ := os.ReadFile("design/templates/align-designs.md")
	for _, want := range []string{"{{resolve-inputs.inputs.spec_path}}", "{{finalize-designs.paths}}"} {
		if !strings.Contains(string(ad), want) {
			t.Errorf("align-designs.md lacks %q", want)
		}
	}
	orch, _ := os.ReadFile("design/orchestration/brainstorm-design.orch.md")
	if strings.Contains(string(orch), "{{") {
		t.Error("orchestration file contains {{ }}; it is not variable-resolved")
	}
}

// Plan inputs are explicit, plans land in the per-slug run-dated layout, and no
// plan prompt pushes (checkpoint-push and finish do). The orchestration file is
// not variable-resolved, so it must carry no {{ }}.
func TestPlanTemplatesWireInputs(t *testing.T) {
	want := map[string][]string{
		"plan/templates/produce-plans.md": {
			"./work/{{resolve-inputs.inputs.spec_path}}", "{{resolve-inputs.inputs.design_paths}}",
			"./work/{{plan_dir}}/{{run_date}}-phase-{N}-task-{M}.md",
		},
		"plan/templates/align-plans.md": {
			"./work/{{resolve-inputs.inputs.spec_path}}", "{{resolve-inputs.inputs.design_paths}}",
			"./work/{{plan_dir}}/{{run_date}}-phase-*-task-*.md",
		},
		"plan/templates/deps-assess.md":    {"{{align-plans.response}}"},
		"plan/templates/deps-assess-2.md":  {"{{align-plans-2.response}}"},
		"plan/templates/deps-assess-3.md":  {"{{align-plans-3.response}}"},
		"plan/templates/resolve-deps.md":   {"{{deps-count.gaps}}", "{{deps-assess.notes}}"},
		"plan/templates/resolve-deps-2.md": {"{{deps-count-2.gaps}}", "{{deps-assess-2.notes}}"},
		"plan/templates/restore-steps.md":  {"./work/{{plan_dir}}/{{run_date}}-phase-*-task-*.md"},
	}
	for f, needles := range want {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		tpl := string(data)
		for _, n := range needles {
			if !strings.Contains(tpl, n) {
				t.Errorf("%s lacks %q", f, n)
			}
		}
		for _, bad := range []string{"git push", "push --quiet", "{{spec_path}}", "-implementation-phase-", "design-phase-*"} {
			if strings.Contains(tpl, bad) {
				t.Errorf("%s must not contain %q", f, bad)
			}
		}
	}
	orch, err := os.ReadFile("plan/orchestration/resolve-deps.orch.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(orch), "{{") || strings.Contains(string(orch), "git push") {
		t.Error("resolve-deps.orch.md must carry no {{ }} and no git push")
	}
}

// deps-assess-2 and -3 are deps-assess pointed at the later alignment reports,
// and resolve-deps-2 is resolve-deps pointed at the second pass. Each pair may
// differ only in those references, so the gates and the human rounds cannot
// drift apart.
func TestPlanLaterPassesMirrorTheFirst(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	assess := read("plan/templates/deps-assess.md")
	for _, sfx := range []string{"-2", "-3"} {
		want := strings.ReplaceAll(assess, "{{align-plans.response}}", "{{align-plans"+sfx+".response}}")
		if read("plan/templates/deps-assess"+sfx+".md") != want {
			t.Errorf("deps-assess%s.md differs from deps-assess.md beyond the align-plans%s reference", sfx, sfx)
		}
	}
	resolve := strings.NewReplacer("{{deps-assess.", "{{deps-assess-2.", "{{deps-count.", "{{deps-count-2.").
		Replace(read("plan/templates/resolve-deps.md"))
	if read("plan/templates/resolve-deps-2.md") != resolve {
		t.Error("resolve-deps-2.md differs from resolve-deps.md beyond the -2 references")
	}
	wf := loadSample(t, "plan")
	for _, sfx := range []string{"-2", "-3"} {
		n1, n2 := nodeByID(wf, "deps-assess"), nodeByID(wf, "deps-assess"+sfx)
		if n2.Config.TemplateFile != "templates/deps-assess"+sfx+".md" || n1.Config.ModelProfile != n2.Config.ModelProfile ||
			!reflect.DeepEqual(n1.Config.OutputSchema, n2.Config.OutputSchema) {
			t.Errorf("deps-assess%s must share deps-assess's profile and schema and read its own template", sfx)
		}
		p1, p2 := nodeByID(wf, "align-plans"), nodeByID(wf, "align-plans"+sfx)
		if p1.Config.TemplateFile != p2.Config.TemplateFile || p1.Config.ModelProfile != p2.Config.ModelProfile || p1.Config.TimeoutSeconds != p2.Config.TimeoutSeconds {
			t.Errorf("align-plans%s must share align-plans's config", sfx)
		}
	}
}

// plan_paths stays a typed array: finish reads it straight from list-plans,
// never through a transform. list-plans is the only plan-file reader and is
// confined to this run's date (Review Focus 2).
func TestPlanFinishReadsTypedPlanPaths(t *testing.T) {
	wf := loadSample(t, "plan")
	n := nodeByID(wf, "finish")
	if got := n.Config.Args[len(n.Config.Args)-1]; !strings.Contains(got, `plan_paths: .["list-plans"].plan_paths`) {
		t.Errorf("finish filter = %q, want plan_paths read from list-plans", got)
	}
	var dir string
	for _, op := range nodeByID(wf, "promote").Config.Operations {
		if op.Variable == "plan_dir" {
			dir, _ = op.Value.(string)
		}
	}
	if dir != "docs/plans/{{slug}}" {
		t.Errorf("promote plan_dir = %q, want docs/plans/{{slug}}", dir)
	}
}

// impl-task only commits: the pi sandbox may have no egress, and
// checkpoint-push-impl pushes after the tamper gate has ruled. Both loop
// prompts read the explicit plan list, never the legacy run-dated glob, and the
// doer clears the previous void's must-fix list before anything else.
func TestImplTemplateHasNoPush(t *testing.T) {
	want := map[string][]string{
		"implement/templates/impl-task.md":   {"{{resolve-inputs.inputs.plan_paths}}", "{{workspace}}/tamper/must-fix.md"},
		"implement/templates/impl-assess.md": {"{{resolve-inputs.inputs.plan_paths}}", "{{impl-task.response}}", "all_remaining_blocked"},
	}
	for f, needles := range want {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		tpl := string(data)
		for _, n := range needles {
			if !strings.Contains(tpl, n) {
				t.Errorf("%s lacks %q", f, n)
			}
		}
		for _, bad := range []string{"git push", "push --quiet", "-implementation-phase-", "{{run_date}}-{{slug}}"} {
			if strings.Contains(tpl, bad) {
				t.Errorf("%s must not contain %q", f, bad)
			}
		}
	}
	impl, _ := os.ReadFile("implement/templates/impl-task.md")
	tpl := string(impl)
	if i, j := strings.Index(tpl, "must-fix.md"), strings.Index(tpl, "## Select the task"); i < 0 || j < 0 || i > j {
		t.Error("impl-task.md must handle must-fix.md before selecting a task")
	}
}

// An existing test whose setup contradicts the spec, design or contracts would
// otherwise block implement until a human authorizes the change. implement may
// repair it itself, within guardrails, and must record why; the tamper judge
// rules on any repair the gate flags by checking that recorded justification.
func TestImplFixtureRepairIsBoundedAndJudged(t *testing.T) {
	impl, err := os.ReadFile("implement/templates/impl-task.md")
	if err != nil {
		t.Fatal(err)
	}
	tpl := string(impl)
	sec := strings.Index(tpl, "## Repairing a conflicting existing test")
	if sec < 0 {
		t.Fatal("impl-task.md must have a fixture-repair section")
	}
	if blocked := strings.Index(tpl, "## If blocked"); blocked < 0 || sec > blocked {
		t.Error("the fixture-repair section must come before \"If blocked\" so repair is tried first")
	}
	for _, want := range []string{
		"> FIXTURE REPAIR (self-authorized):", // the recorded justification
		"spec, design or contracts",           // the only grounds
		"Never delete a test",                 // guardrails
		"add a skip",
		"without a replacement",
		"preexisting failure",
	} {
		if !strings.Contains(tpl, want) {
			t.Errorf("impl-task.md fixture-repair rules must contain %q", want)
		}
	}
	wf := loadSample(t, "implement")
	ctx := ""
	for _, op := range nodeByID(wf, "tamper-context").Config.Operations {
		if op.Variable == "tamper_context" {
			ctx, _ = op.Value.(string)
		}
	}
	if !strings.Contains(ctx, "FIXTURE REPAIR") {
		t.Errorf("implement's tamper_context must point the judge at FIXTURE REPAIR lines, got %q", ctx)
	}
	judge, err := os.ReadFile("_lib/tamper-judge.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FIXTURE REPAIR", "cites"} {
		if !strings.Contains(string(judge), want) {
			t.Errorf("tamper-judge.md must rule on recorded fixture repairs (missing %q)", want)
		}
	}
}

// strengthen may change tests only and must clear a voided round's must-fix
// list first; it records equivalence claims in the claims file and never
// pushes. The judge reads the claims and writes rulings to the verdicts file:
// agent_call does not enforce outputSchema, so the file is what is read.
func TestHardenTemplates(t *testing.T) {
	want := map[string][]string{
		"harden/templates/strengthen.md": {
			"{{workspace}}/harden/survivors-round.json", "{{workspace}}/harden/equivalence-claims.json",
			"{{workspace}}/tamper/must-fix.md", `"mutant_id"`, `"justification"`, "test files only",
			"git -C ./work status --porcelain",
		},
		"harden/templates/judge-equivalent.md": {
			"{{workspace}}/harden/equivalence-claims.json", "{{workspace}}/harden/equivalence-verdicts.json",
			`"rulings"`, `"equivalent"`, `"reason"`,
		},
	}
	for f, needles := range want {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		tpl := string(data)
		for _, n := range needles {
			if !strings.Contains(tpl, n) {
				t.Errorf("%s lacks %q", f, n)
			}
		}
		for _, bad := range []string{"git push", "push --quiet"} {
			if strings.Contains(tpl, bad) {
				t.Errorf("%s must not contain %q", f, bad)
			}
		}
	}
	data, _ := os.ReadFile("harden/templates/strengthen.md")
	tpl := string(data)
	if i, j := strings.Index(tpl, "must-fix.md"), strings.Index(tpl, "survivors-round.json"); i < 0 || j < 0 || i > j {
		t.Error("strengthen.md must handle must-fix.md before the survivors")
	}
	wf := loadSample(t, "harden")
	if n := nodeByID(wf, "judge-equivalent"); n.Config.OutputSchema != nil || n.Type != workflow.NodeTypeAgentCall {
		t.Errorf("judge-equivalent = %s with outputSchema %v, want an agent_call writing a verdict file", n.Type, n.Config.OutputSchema)
	}
}

// An approved interactive session lives only in the child workspace until it
// is pushed, and a rerun starts a fresh workspace. So each session is pushed
// straight after it ends (before finalize, so even a misnamed file survives),
// and design pushes again after finalize, before the align agent can fail.
func TestInteractiveSessionsArePushedEarly(t *testing.T) {
	cases := []struct {
		workflow string
		chain    []string
	}{
		{"spec", []string{"brainstorm-spec", "checkpoint-push-spec", "finalize-spec"}},
		{"design", []string{"brainstorm-design", "checkpoint-push-brainstorm", "finalize-designs", "checkpoint-push-designs", "align-designs"}},
	}
	for _, c := range cases {
		t.Run(c.workflow, func(t *testing.T) {
			wf := loadSample(t, c.workflow)
			for i := 0; i+1 < len(c.chain); i++ {
				if !hasEdge(wf, c.chain[i], c.chain[i+1]) {
					t.Errorf("missing edge %s -> %s", c.chain[i], c.chain[i+1])
				}
			}
			for _, id := range c.chain {
				if !strings.HasPrefix(id, "checkpoint-push-") {
					continue
				}
				n, ok := findNode(wf, id)
				if !ok {
					t.Errorf("no %s node", id)
					continue
				}
				if n.Type != "script" || n.Config.ScriptFile != "@lib/checkpoint-push.sh" {
					t.Errorf("%s = %s %q, want script @lib/checkpoint-push.sh", id, n.Type, n.Config.ScriptFile)
				}
			}
			session := c.chain[0]
			if reachableAvoiding(wf, session, c.chain[2], c.chain[1]) {
				t.Errorf("%s reaches %s without crossing %s", session, c.chain[2], c.chain[1])
			}
			if c.workflow == "design" && reachableAvoiding(wf, "finalize-designs", "align-designs", "checkpoint-push-designs") {
				t.Error("finalize-designs reaches align-designs without checkpoint-push-designs")
			}
		})
	}
}
