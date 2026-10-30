package samples_test

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/variable"
	"github.com/arbitron/arbitron/domain/workflow"
)

const parentName = "feature-pipeline"

// parentSpawn is one stage spawn of the parent: the child it starts and the
// full explicit inputs map spec §5.9 gives it.
type parentSpawn struct {
	child  string
	inputs map[string]any
}

var parentSpawns = map[string]parentSpawn{
	"ideate": {"ideate", map[string]any{
		"repo_url": "{{repo_url}}", "base_branch": "{{base_branch}}", "topic": "{{topic}}",
		"teams_webhook_url": "{{teams_webhook_url}}",
	}},
	"spec": {"spec", map[string]any{
		"repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
		"idea_path": "{{pre-spec.inputs.idea_path}}", "teams_webhook_url": "{{teams_webhook_url}}",
	}},
	"design": {"design", map[string]any{
		"repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
		"spec_path": "{{pre-design.inputs.spec_path}}", "idea_path": "{{pre-design.inputs.idea_path}}",
		"teams_webhook_url": "{{teams_webhook_url}}",
	}},
	"plan": {"plan", map[string]any{
		"repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
		"spec_path": "{{pre-plan.inputs.spec_path}}", "design_paths": "{{pre-plan.inputs.design_paths}}",
		"teams_webhook_url": "{{teams_webhook_url}}",
	}},
	"implement": {"implement", map[string]any{
		"repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
		"plan_paths": "{{pre-implement.inputs.plan_paths}}", "teams_webhook_url": "{{teams_webhook_url}}",
	}},
	"harden": {"harden", map[string]any{
		"repo_url": "{{repo_url}}", "branch": "{{branch}}", "slug": "{{slug}}", "base_branch": "{{base_branch}}",
		"base_ref": "{{pre-harden.inputs.base_branch}}", "baseline_path": "{{pre-harden.inputs.baseline_path}}",
		"teams_webhook_url": "{{teams_webhook_url}}",
	}},
	"refine": {"ralph-loop", map[string]any{
		"repo_url": "{{repo_url}}", "base_branch": "{{branch}}", "slug": "{{slug}}",
		"pr_base": "{{pre-refine.inputs.base_branch}}", "in_place": true,
		"focus_seed": "{{pre-refine.inputs.focus_seed_path}}", "baseline_path": "{{pre-refine.inputs.baseline_path}}",
		"teams_webhook_url": "{{teams_webhook_url}}",
	}},
	"review": {"pr-review", map[string]any{
		"repo": "{{repo_slug}}", "pr_number": "{{pre-review.inputs.pr_number}}",
		"fix_scope": "{{review_fix_scope}}", "teams_webhook_url": "{{teams_webhook_url}}",
	}},
}

// parentStages is the stage order; each stage's spawn success port leads to
// the node named here, and its failure port to fail-<stage>.
var parentStages = []struct{ stage, success string }{
	{"ideate", "adopt-ideate"},
	{"spec", "pre-design"},
	{"design", "pre-plan"},
	{"plan", "plan-ok?"},
	{"implement", "impl-ok?"},
	{"harden", "pre-refine"}, // a harden miss is not a failure: it seeds ralph
	{"refine", "record-refine"},
	{"review", "record-review"},
}

// parentStore seeds a store the way the engine seeds an instance: every
// declared parent input at its default, then the given overrides.
func parentStore(t *testing.T, wf *workflow.Workflow, overrides map[string]any) *variable.VariableStore {
	t.Helper()
	vars := variable.NewStore()
	for _, in := range wf.Inputs {
		v := in.Default
		if v == nil {
			v = ""
		}
		if err := vars.SetInput(in.Name, v); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range overrides {
		if err := vars.SetInput(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return vars
}

func runTransform(t *testing.T, wf *workflow.Workflow, id string, vars *variable.VariableStore) {
	t.Helper()
	n, ok := findNode(wf, id)
	if !ok || n.Type != workflow.NodeTypeTransform {
		t.Fatalf("%s has no transform %q", wf.Name, id)
	}
	if _, err := engine.NewTransformExecutor().Execute(context.Background(), n, vars, wf.Name); err != nil {
		t.Fatal(err)
	}
}

func spawnNodes(wf *workflow.Workflow) []workflow.Node {
	var out []workflow.Node
	for _, n := range wf.Nodes {
		if n.Type == workflow.NodeTypeSpawnWorkflow {
			out = append(out, n)
		}
	}
	return out
}

func outEdges(wf *workflow.Workflow, id string) []workflow.Edge {
	var out []workflow.Edge
	for _, e := range wf.Edges {
		if e.Source == id {
			out = append(out, e)
		}
	}
	return out
}

func TestParentIsAcyclic(t *testing.T) {
	wf := loadSample(t, parentName)
	if len(wf.Nodes) < 30 {
		t.Fatalf("parent has %d nodes; the graph is not the §5.9 pipeline", len(wf.Nodes))
	}
	for _, n := range wf.Nodes {
		if onCycle(wf, n.ID) {
			t.Errorf("node %s sits on a cycle; the parent has no loops", n.ID)
		}
	}
}

func TestParentRouteCoversEveryStage(t *testing.T) {
	wf := loadSample(t, parentName)
	want := map[string]string{
		"ideate": "ideate", "spec": "pre-spec", "design": "pre-design", "plan": "pre-plan",
		"implement": "pre-implement", "harden": "pre-harden", "refine": "pre-refine", "review": "pre-review",
		"complete": "final-read", "bogus": "notify-step-failed", "": "notify-step-failed",
	}
	for next, target := range want {
		t.Run(next, func(t *testing.T) {
			if got := routeTo(t, wf, "route", map[string]any{"detect": map[string]any{"next_stage": next}}); got != target {
				t.Errorf("route(next_stage=%q) -> %s, want %s", next, got, target)
			}
		})
	}
	if got := nodeByID(wf, "detect").Config.Args; !slices.Equal(got, []string{
		"--fetch", "--order", "ideate,spec,design,plan,implement,harden,refine,review", "--start-at-var", "start_at",
	}) {
		t.Errorf("detect args = %q", got)
	}
}

func TestParentGates(t *testing.T) {
	wf := loadSample(t, parentName)
	cases := []struct {
		name, gate string
		outputs    map[string]any
		want       string
	}{
		{"plan resolved", "plan-ok?", map[string]any{"plan": map[string]any{"childOutput": map[string]any{
			"finish": map[string]any{"outputs": map[string]any{"deps_resolved": true}}}}}, "pre-implement"},
		{"plan blocked", "plan-ok?", map[string]any{"plan": map[string]any{"childOutput": map[string]any{
			"finish": map[string]any{"outputs": map[string]any{"deps_resolved": false}}}}}, "fail-plan-blocked"},
		{"plan deps absent", "plan-ok?", map[string]any{"plan": map[string]any{"childOutput": map[string]any{}}}, "fail-plan-blocked"},
		{"impl done", "impl-ok?", map[string]any{"implement": map[string]any{"childOutput": map[string]any{
			"finish": map[string]any{"status": "done"}}}}, "pre-harden"},
		{"impl blocked", "impl-ok?", map[string]any{"implement": map[string]any{"childOutput": map[string]any{
			"finish": map[string]any{"status": "blocked"}}}}, "fail-impl-blocked"},
		{"record idea", "record-idea?", map[string]any{"intake-check": map[string]any{"record_idea": true}}, "record-provided-idea"},
		{"no idea to record", "record-idea?", map[string]any{"intake-check": map[string]any{"record_idea": false}}, "fresh?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := routeTo(t, wf, c.gate, c.outputs); got != c.want {
				t.Errorf("%s -> %s, want %s", c.gate, got, c.want)
			}
		})
	}
	for _, chain := range [][]string{
		{"intake", "promote-intake", "clone", "intake-check", "promote-check", "record-idea?"},
		{"record-provided-idea", "detect", "route"},
		{"adopt-ideate", "promote-ideate", "pre-spec", "spec"},
		{"pre-design", "design"}, {"pre-plan", "plan"}, {"pre-implement", "implement"},
		{"pre-harden", "harden"}, {"pre-refine", "refine"},
		{"record-refine", "pre-review", "review"},
		{"record-review", "final-read", "notify-done", "done"},
		{"fail-plan-blocked", "notify-failed"}, {"fail-impl-blocked", "notify-failed"},
	} {
		for i := 0; i+1 < len(chain); i++ {
			if !hasEdge(wf, chain[i], chain[i+1]) {
				t.Errorf("missing edge %s -> %s", chain[i], chain[i+1])
			}
		}
	}
	if n := nodeByID(wf, "adopt-ideate"); n.Type != workflow.NodeTypeScript || n.Config.ScriptFile != "scripts/adopt-ideate.sh" {
		t.Errorf("adopt-ideate = %s %q, want the adopt-ideate script", n.Type, n.Config.ScriptFile)
	}
	if n := nodeByID(wf, "promote-ideate"); n.Type != workflow.NodeTypeTransform {
		t.Errorf("promote-ideate = %s, want a transform after adopt-ideate", n.Type)
	}
}

// Fresh mode has no slug or manifest yet, so it goes straight to ideate (P1).
func TestParentFreshModeSkipsDetect(t *testing.T) {
	wf := loadSample(t, parentName)
	for mode, want := range map[string]string{"fresh": "ideate", "resume": "detect", "idea": "detect"} {
		if got := routeTo(t, wf, "fresh?", map[string]any{"intake": map[string]any{"mode": mode}}); got != want {
			t.Errorf("fresh?(mode=%s) -> %s, want %s", mode, got, want)
		}
	}
	if reachableAvoiding(wf, "ideate", "detect", "") {
		t.Error("detect is reachable from ideate; no intake -> detect path may pass through ideate")
	}
	if !reachableAvoiding(wf, "intake", "detect", "ideate") {
		t.Error("detect must be reachable from intake without ideate")
	}
}

func TestParentErrorRouting(t *testing.T) {
	wf := loadSample(t, parentName)
	toStepFailed := workflow.ErrorBehavior{Type: "route", ErrorNodeID: "notify-step-failed"}
	if wf.DefaultErrorBehavior != toStepFailed {
		t.Errorf("defaultErrorBehavior = %+v, want %+v", wf.DefaultErrorBehavior, toStepFailed)
	}
	special := map[string]workflow.ErrorBehavior{
		"notify-done": {Type: "skip"}, "notify-failed": {Type: "skip"}, "notify-step-failed": {Type: "skip"},
		"halt": {Type: "halt"},
	}
	routed := 0
	for _, n := range wf.Nodes {
		if eb, ok := special[n.ID]; ok {
			if n.ErrorBehavior != eb {
				t.Errorf("%s errorBehavior = %+v, want %+v", n.ID, n.ErrorBehavior, eb)
			}
			continue
		}
		if n.Type != workflow.NodeTypeScript && n.Type != workflow.NodeTypeSpawnWorkflow {
			continue
		}
		routed++
		if n.ErrorBehavior != toStepFailed {
			t.Errorf("%s errorBehavior = %+v, want an explicit route to notify-step-failed", n.ID, n.ErrorBehavior)
		}
	}
	if routed < 20 {
		t.Errorf("checked %d script/spawn nodes, want every intake, clone, pre-*, record-* and spawn node", routed)
	}
	// Notify is best effort (P4): skip continues along the one normal edge.
	for id, next := range map[string]string{"notify-done": "done", "notify-failed": "halt", "notify-step-failed": "halt"} {
		if es := outEdges(wf, id); len(es) != 1 || es[0].Target != next {
			t.Errorf("%s successors = %+v, want only %s", id, es, next)
		}
	}
	if es := outEdges(wf, "halt"); len(es) != 0 {
		t.Errorf("halt must be terminal, has %+v", es)
	}
	spawns := spawnNodes(wf)
	if len(spawns) != len(parentStages) {
		t.Fatalf("parent has %d spawns, want %d", len(spawns), len(parentStages))
	}
	for _, s := range parentStages {
		n := nodeByID(wf, s.stage)
		if n.Type != workflow.NodeTypeSpawnWorkflow || n.Config.SpawnMode != workflow.SpawnModeWaitForChild {
			t.Errorf("%s = %s/%s, want an awaited spawn", s.stage, n.Type, n.Config.SpawnMode)
		}
		if got := edgeTarget(wf, s.stage, workflow.SpawnPortSuccess); got != s.success {
			t.Errorf("%s success -> %q, want %s", s.stage, got, s.success)
		}
		if got := edgeTarget(wf, s.stage, workflow.SpawnPortFailure); got != "fail-"+s.stage {
			t.Errorf("%s failure -> %q, want fail-%s", s.stage, got, s.stage)
		}
		if !hasEdge(wf, "fail-"+s.stage, "notify-failed") {
			t.Errorf("fail-%s must lead to notify-failed", s.stage)
		}
	}
	if !hasEdge(wf, "notify-failed", "halt") {
		t.Error("notify-failed must lead to halt")
	}
}

func TestParentSpawnsAreFullyMapped(t *testing.T) {
	wf := loadSample(t, parentName)
	for id, want := range parentSpawns {
		t.Run(id, func(t *testing.T) {
			n, ok := findNode(wf, id)
			if !ok {
				t.Fatalf("no spawn node %s", id)
			}
			if n.Config.ChildWorkflow != want.child {
				t.Errorf("childWorkflow = %q, want %q", n.Config.ChildWorkflow, want.child)
			}
			if !reflect.DeepEqual(n.Config.Inputs, want.inputs) {
				t.Errorf("inputs = %v\nwant     %v", n.Config.Inputs, want.inputs)
			}
			if n.Config.ModelProfile != "" {
				t.Errorf("model_profile = %q; a fully mapped spawn needs no preparer", n.Config.ModelProfile)
			}
			plan := workflow.PlanChildInputs(loadSample(t, want.child).Inputs, n.Config.Inputs)
			if len(plan.Unknown) > 0 {
				t.Errorf("maps keys %v that %s does not declare", plan.Unknown, want.child)
			}
			if len(plan.Prepare) > 0 {
				t.Errorf("leaves required %s inputs unmapped with no default: %v", want.child, plan.Prepare)
			}
		})
	}
	for _, id := range []string{"spec", "design", "plan", "implement", "harden"} {
		if got := nodeByID(wf, id).Config.Inputs["base_branch"]; got != "{{base_branch}}" {
			t.Errorf("%s base_branch = %v, want {{base_branch}}", id, got)
		}
	}
	for _, id := range []string{"pre-harden", "pre-refine"} {
		if args := nodeByID(wf, id).Config.Args; !hasArgPair(args, "--explicit", "base_branch") || !hasArgPair(args, "--require", "base_branch") {
			t.Errorf("%s args = %q, want --explicit base_branch and --require base_branch", id, args)
		}
	}
}

func hasArgPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}

// A resume of a branch cut from dev, run without base_branch, must carry dev
// into every stage, harden's diff base and ralph's PR base (P2b).
func TestParentResumeBaseBranchFromManifest(t *testing.T) {
	wf := loadSample(t, parentName)
	for _, in := range wf.Inputs {
		if in.Name == "base_branch" && in.Default != "" {
			t.Errorf("base_branch default = %#v, want \"\"", in.Default)
		}
	}
	vars := parentStore(t, wf, map[string]any{"repo_url": "https://github.com/o/r", "branch": "dev-x"})
	vars.SetNodeOutput("intake", map[string]any{
		"mode": "resume", "init_mode": "checkout", "slug": "", "repo_slug": "o/r", "base_branch": "",
	})
	runTransform(t, wf, "promote-intake", vars)
	vars.SetNodeOutput("intake-check", map[string]any{
		"slug": "x", "branch": "dev-x", "work_dir": "/ws/work", "idea_path": "", "record_idea": false, "base_branch": "dev",
	})
	runTransform(t, wf, "promote-check", vars)
	for k, want := range map[string]any{"base_branch": "dev", "slug": "x", "branch": "dev-x", "work_dir": "/ws/work", "repo_slug": "o/r", "init_mode": "checkout"} {
		if got, _ := vars.Get(k); got != want {
			t.Errorf("store %s = %#v, want %#v", k, got, want)
		}
	}

	// Fixture outputs of every pre-* read, as manifest-read emits them.
	vars.SetNodeOutput("pre-spec", map[string]any{"inputs": map[string]any{"idea_path": "docs/ideas/x.md"}})
	vars.SetNodeOutput("pre-design", map[string]any{"inputs": map[string]any{"idea_path": "", "spec_path": "docs/specs/x.md"}})
	vars.SetNodeOutput("pre-plan", map[string]any{"inputs": map[string]any{"spec_path": "docs/specs/x.md", "design_paths": []any{"d1.md", "d2.md"}}})
	vars.SetNodeOutput("pre-implement", map[string]any{"inputs": map[string]any{"plan_paths": []any{"p1.md"}}})
	vars.SetNodeOutput("pre-harden", map[string]any{"inputs": map[string]any{"base_branch": "dev", "baseline_path": ".arbitron/pipeline/x.baseline.json"}})
	vars.SetNodeOutput("pre-refine", map[string]any{"inputs": map[string]any{"base_branch": "dev", "focus_seed_path": ".arbitron/pipeline/x.survivors.json", "baseline_path": ".arbitron/pipeline/x.baseline.json"}})
	vars.SetNodeOutput("pre-review", map[string]any{"inputs": map[string]any{"pr_number": float64(42)}})

	resolved := map[string]map[string]any{}
	for id := range parentSpawns {
		got, err := variable.ResolveTypedMap(nodeByID(wf, id).Config.Inputs, vars)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		resolved[id] = got
	}
	for _, id := range []string{"spec", "design", "plan", "implement", "harden"} {
		if got := resolved[id]["base_branch"]; got != "dev" {
			t.Errorf("%s base_branch = %#v, want dev", id, got)
		}
	}
	checks := []struct {
		id, key string
		want    any
	}{
		{"harden", "base_ref", "dev"},
		{"refine", "pr_base", "dev"},
		{"refine", "base_branch", "dev-x"},
		{"refine", "in_place", true},
		{"refine", "focus_seed", ".arbitron/pipeline/x.survivors.json"},
		{"refine", "baseline_path", ".arbitron/pipeline/x.baseline.json"},
		{"plan", "design_paths", []any{"d1.md", "d2.md"}},
		{"implement", "plan_paths", []any{"p1.md"}},
		{"review", "pr_number", float64(42)},
		{"review", "repo", "o/r"},
		{"review", "fix_scope", "all-but-defer"},
	}
	for _, c := range checks {
		if got := resolved[c.id][c.key]; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s.%s = %#v (%T), want %#v (%T)", c.id, c.key, got, got, c.want, c.want)
		}
	}
}

var finishOutputRefRE = regexp.MustCompile(`([\w-]+)\.childOutput\.finish\.outputs\.(\w+)`)

// TestFinishFilterKeysNonEmpty keeps TestStageContractsLineUp from passing
// vacuously (P5): every stage's finish filter must yield keys.
func TestFinishFilterKeysNonEmpty(t *testing.T) {
	for _, c := range stageContracts {
		if keys := finishFilterKeys(t, loadSample(t, c.workflow)); len(keys) == 0 {
			t.Errorf("%s finish filter yields no keys", c.workflow)
		}
	}
}

func TestStageContractsLineUp(t *testing.T) {
	wf := loadSample(t, parentName)
	finishKeys := map[string][]string{} // spawn id -> child finish keys
	produced := map[string]bool{"base_branch": true, "branch": true, "slug": true}
	for _, n := range spawnNodes(wf) {
		child := loadSample(t, n.Config.ChildWorkflow)
		if _, ok := findNode(child, "finish"); ok {
			finishKeys[n.ID] = finishFilterKeys(t, child)
			for _, k := range finishKeys[n.ID] {
				produced[k] = true
			}
		}
	}

	raw, err := json.Marshal(wf)
	if err != nil {
		t.Fatal(err)
	}
	refs := finishOutputRefRE.FindAllStringSubmatch(string(raw), -1)
	if len(refs) < 3 {
		t.Fatalf("found %d childOutput.finish.outputs reads, want the plan and implement gates' reads", len(refs))
	}
	for _, m := range refs {
		if !slices.Contains(finishKeys[m[1]], m[2]) {
			t.Errorf("parent reads %s.childOutput.finish.outputs.%s; that child's finish emits %v", m[1], m[2], finishKeys[m[1]])
		}
	}
	// adopt-ideate reads branch and slug from ideate's finish outputs.
	for _, k := range []string{"branch", "slug"} {
		if !slices.Contains(finishKeys["ideate"], k) {
			t.Errorf("ideate finish emits %v, adopt-ideate needs %q", finishKeys["ideate"], k)
		}
	}
	// The record nodes read the refine and review children by node id.
	for spawn, node := range map[string]string{"refine": "push-and-pr", "review": "record-verdict"} {
		child := loadSample(t, nodeByID(wf, spawn).Config.ChildWorkflow)
		if _, ok := findNode(child, node); !ok {
			t.Errorf("%s reads childOutput[%q], which %s lacks", spawn, node, child.Name)
		}
	}

	appends := 0
	for _, n := range wf.Nodes {
		if n.Config.ScriptFile != "@lib/manifest-append.sh" {
			continue
		}
		appends++
		if len(n.Config.Args) < 5 || n.Config.Args[0] != "--workflow" || n.Config.Args[1] != parentName {
			t.Errorf("%s args = %q, want --workflow %s first", n.ID, n.Config.Args, parentName)
			continue
		}
		for _, m := range filterKeyRE.FindAllStringSubmatch(n.Config.Args[len(n.Config.Args)-1], -1) {
			produced[m[1]] = true
		}
	}
	if appends != 3 {
		t.Errorf("parent has %d manifest-append nodes, want record-provided-idea, record-refine and record-review", appends)
	}
	// record-refine and record-review land on top of ralph's one CI commit,
	// so their manifest commits skip the [skip ci] hook: the branch head is the
	// PR head, and a [skip ci] head leaves required checks waiting forever.
	for id, want := range map[string][]string{"record-refine": {"--no-skip-ci", "refine", "done"}, "record-review": {"--no-skip-ci", "review", "done"}, "record-provided-idea": {"ideate", "done"}} {
		if args := nodeByID(wf, id).Config.Args; len(args) < 2+len(want) || !slices.Equal(args[2:2+len(want)], want) {
			t.Errorf("%s records %q, want %q after --workflow", id, args, want)
		}
	}

	requires := 0
	for _, n := range wf.Nodes {
		if !strings.HasPrefix(n.ID, "pre-") {
			continue
		}
		args := n.Config.Args
		for i := 0; i+1 < len(args); i++ {
			if args[i] != "--require" {
				continue
			}
			for _, k := range strings.Split(args[i+1], ",") {
				requires++
				if !produced[k] {
					t.Errorf("%s requires %q, which no stage finish or parent record produces", n.ID, k)
				}
			}
		}
	}
	if requires == 0 {
		t.Fatal("inspected no --require keys; the line-up check is vacuous")
	}
}

var preInputRefRE = regexp.MustCompile(`\{\{(pre-[A-Za-z0-9_-]+)\.inputs\.([A-Za-z0-9_]+)`)

// preInputKeys returns the keys a manifest-read node resolves into .inputs:
// every --explicit and --require key and every --optional k=default key.
func preInputKeys(args []string) map[string]bool {
	keys := map[string]bool{}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--explicit", "--require":
			for _, k := range strings.Split(args[i+1], ",") {
				keys[k] = true
			}
		case "--optional":
			k, _, _ := strings.Cut(args[i+1], "=")
			keys[k] = true
		}
	}
	return keys
}

// unresolvedPreRefs returns each {{pre-X.inputs.k}} in a spawn map whose k is
// not among pre-X's manifest-read keys (or whose pre-X does not exist). Such a
// ref renders empty, and the child starts without the input.
func unresolvedPreRefs(wf *workflow.Workflow) (bad []string, refs int) {
	var walk func(spawn string, v any)
	walk = func(spawn string, v any) {
		switch x := v.(type) {
		case string:
			for _, m := range preInputRefRE.FindAllStringSubmatch(x, -1) {
				refs++
				pre, ok := findNode(wf, m[1])
				if !ok || !preInputKeys(pre.Config.Args)[m[2]] {
					bad = append(bad, spawn+": "+m[0]+"}}")
				}
			}
		case map[string]any:
			for _, e := range x {
				walk(spawn, e)
			}
		case []any:
			for _, e := range x {
				walk(spawn, e)
			}
		}
	}
	for _, n := range wf.Nodes {
		if n.Type == workflow.NodeTypeSpawnWorkflow {
			walk(n.ID, n.Config.Inputs)
		}
	}
	return bad, refs
}

// T25: every {{pre-X.inputs.k}} a spawn map reads is a key pre-X resolves.
func TestParentSpawnPreInputRefsResolve(t *testing.T) {
	bad, refs := unresolvedPreRefs(loadSample(t, parentName))
	for _, b := range bad {
		t.Errorf("%s names a key its pre- node does not resolve", b)
	}
	if refs < 8 {
		t.Errorf("found %d pre-X.inputs refs in spawn maps; the check is not seeing them", refs)
	}

	// The check itself catches a key the pre- node does not resolve.
	synthetic := &workflow.Workflow{Nodes: []workflow.Node{
		{ID: "pre-a", Type: "script", Config: workflow.NodeConfig{Args: []string{"--explicit", "x", "--optional", "y="}}},
		{ID: "a", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{Inputs: map[string]any{
			"x": "{{pre-a.inputs.x}}", "y": "{{pre-a.inputs.y}}", "z": "{{pre-a.inputs.z}}", "w": "{{pre-b.inputs.x}}",
		}}},
	}}
	if bad, refs := unresolvedPreRefs(synthetic); len(bad) != 2 || refs != 4 {
		t.Errorf("synthetic check = %v over %d refs, want the z and pre-b refs flagged over 4", bad, refs)
	}
}
