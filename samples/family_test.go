package samples_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/workflow"
)

// TestModelFamily pins the spec §3.4 (R14) vendor-family rule: the family is
// the model vendor after provider prefixes are stripped, never the backend.
func TestModelFamily(t *testing.T) {
	cases := []struct {
		model  string
		family string
		ok     bool
	}{
		{"claude-opus-5-5", "anthropic", true},
		{"claude-sonnet-5-5", "anthropic", true},
		{"openai-codex/gpt-6.1-sol", "openai", true},
		{"anthropic/claude-opus-5-5", "anthropic", true},
		{"gpt-6-astra", "openai", true},
		{"gpt-6.1-sol", "openai", true},
		{"o3-mini", "openai", true},
		{"us.anthropic.claude-sonnet-5-5", "anthropic", true},
		{"anthropic.claude-opus-5-5", "anthropic", true},
		{"deepseek-v4-pro", "deepseek", true},
		{"ollama/glm-5.1", "zhipuai", true},
		{"Kimi-K3", "moonshotai", true},
		{"claude-sonnet-5-lr1", "anthropic", true},
		{"", "", false},
		{"  ", "", false},
		{"openai-codex/", "", false},
		{"(inherit)", "", false},
		{"—", "", false},
		{"-", "", false},
		{"--", "", false},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			f := modelidentity.ClassifyFamily("", c.model)
			got, ok := string(f), f != modelidentity.FamilyUnresolved
			if !ok {
				got = ""
			}
			if got != c.family || ok != c.ok {
				t.Errorf("ClassifyFamily(%q) = (%q, %v), want (%q, %v)", c.model, got, ok, c.family, c.ok)
			}
		})
	}
}

// judgePairing declares, for one judge node, the doer nodes whose output it
// certifies. Later tasks add a row with each judge they introduce.
type judgePairing struct {
	workflow  string
	judgeNode string
	doerNodes []string
}

var judgePairings = []judgePairing{
	{"ralph-loop", "tamper-judge-codex", []string{"refine-single-1", "refine-paired"}},
	{"ralph-loop", "tamper-judge-opus", []string{"refine-single-2"}},
	{"implement", "tamper-judge", []string{"impl-task"}},
	{"pr-review", "verify-fix", []string{"senior-engineer"}},
	{"harden", "tamper-judge", []string{"strengthen"}},
	{"harden", "judge-equivalent", []string{"strengthen"}},
}

var judgeNodeRE = regexp.MustCompile(`^(tamper-judge|judge-)`)

// familyOfNode resolves a node's model family through its profile alias and
// the workflow's PROFILES.md table.
func familyOfNode(wf *workflow.Workflow, profiles map[string]profileRow, id string) (string, error) {
	n, ok := findNode(wf, id)
	if !ok {
		return "", fmt.Errorf("node %q does not exist in %s", id, wf.Name)
	}
	alias := n.Config.ModelProfile
	row, ok := profiles[alias]
	if !ok {
		return "", fmt.Errorf("node %q profile %q is not in %s PROFILES.md", id, alias, wf.Name)
	}
	fam := modelidentity.ClassifyFamily("", row.Model)
	if fam == modelidentity.FamilyUnresolved {
		return "", fmt.Errorf("node %q profile %q has an unclassifiable model %q", id, alias, row.Model)
	}
	return string(fam), nil
}

// judgePairingProblems discovers every judge node in wfs and checks it against
// the pairing table. It returns one message per problem; none means clean.
func judgePairingProblems(wfs map[string]*workflow.Workflow, profiles map[string]map[string]profileRow, rows []judgePairing) []string {
	var problems []string
	paired := map[string]bool{}
	for _, r := range rows {
		paired[r.workflow+"/"+r.judgeNode] = true
		wf, ok := wfs[r.workflow]
		if !ok {
			problems = append(problems, fmt.Sprintf("pairing row names unknown workflow %q", r.workflow))
			continue
		}
		if len(r.doerNodes) == 0 {
			problems = append(problems, fmt.Sprintf("%s/%s: pairing row has no doer nodes", r.workflow, r.judgeNode))
		}
		jf, err := familyOfNode(wf, profiles[r.workflow], r.judgeNode)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", r.workflow, err))
			continue
		}
		for _, d := range r.doerNodes {
			df, err := familyOfNode(wf, profiles[r.workflow], d)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", r.workflow, err))
				continue
			}
			if df == jf {
				problems = append(problems, fmt.Sprintf("%s: judge %s and doer %s are both family %q", r.workflow, r.judgeNode, d, jf))
			}
		}
	}
	for name, wf := range wfs {
		for _, n := range wf.Nodes {
			if !judgeNodeRE.MatchString(n.ID) {
				continue
			}
			switch n.Type {
			case workflow.NodeTypeConditional, workflow.NodeTypeTransform:
				continue // routers such as judge-pick carry no model
			case workflow.NodeTypeAgentCall:
			default:
				problems = append(problems, fmt.Sprintf("%s/%s: judge-named node is %s, must be agent_call", name, n.ID, n.Type))
				continue
			}
			if !paired[name+"/"+n.ID] {
				problems = append(problems, fmt.Sprintf("%s/%s: judge node has no declared doer pairing", name, n.ID))
			}
		}
	}
	return problems
}

func TestJudgeFamilyDiffersFromDoer(t *testing.T) {
	wfs := map[string]*workflow.Workflow{}
	profiles := map[string]map[string]profileRow{}
	for _, name := range allSampleNames(t) {
		wfs[name] = loadSample(t, name)
		profiles[name] = profileTable(t, name)
	}
	for _, p := range judgePairingProblems(wfs, profiles, judgePairings) {
		t.Error(p)
	}
}

// The discovery logic itself must fail loudly; otherwise an empty pairing table
// and a forgotten judge would both pass.
func TestJudgePairingProblems(t *testing.T) {
	mk := func(nodes ...workflow.Node) map[string]*workflow.Workflow {
		return map[string]*workflow.Workflow{"w": {Name: "w", Nodes: nodes}}
	}
	agent := func(id, alias string) workflow.Node {
		return workflow.Node{ID: id, Type: workflow.NodeTypeAgentCall, Config: workflow.NodeConfig{ModelProfile: alias}}
	}
	profs := map[string]map[string]profileRow{"w": {
		"claude-a":     {Alias: "claude-a", Model: "claude-opus-5-5"},
		"gpt-a":        {Alias: "gpt-a", Model: "openai-codex/gpt-6.1-sol"},
		"gpt-b":        {Alias: "gpt-b", Model: "gpt-6-astra"},
		"blank":        {Alias: "blank", Model: ""},
		"claude-alias": {Alias: "claude-alias", Model: "claude-sonnet-5-lr1"},
		"opaque":       {Alias: "opaque", Model: "muse-spark-open"},
	}}

	tests := []struct {
		name string
		wfs  map[string]*workflow.Workflow
		rows []judgePairing
		want int
	}{
		{"cross family passes", mk(agent("doer", "claude-a"), agent("judge-x", "gpt-a")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 0},
		{"tamper-judge prefix is discovered", mk(agent("doer", "claude-a"), agent("tamper-judge-1", "gpt-a")), nil, 1},
		{"undeclared judge fails", mk(agent("doer", "claude-a"), agent("judge-x", "gpt-a")), nil, 1},
		{"llm_call judge fails", mk(workflow.Node{ID: "judge-x", Type: workflow.NodeTypeLLMCall}), nil, 1},
		{"interactive_agent judge fails", mk(workflow.Node{ID: "tamper-judge", Type: workflow.NodeTypeInteractiveAgent}), nil, 1},
		{"conditional router is exempt", mk(workflow.Node{ID: "judge-pick", Type: workflow.NodeTypeConditional}), nil, 0},
		{"transform router is exempt", mk(workflow.Node{ID: "judge-merge", Type: workflow.NodeTypeTransform}), nil, 0},
		{"non-judge agent needs no row", mk(agent("doer", "claude-a"), agent("review", "gpt-a")), nil, 0},
		{"same vendor through a gateway alias fails", mk(agent("doer", "claude-a"), agent("judge-x", "claude-alias")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 1},
		{"opaque alias judge is unresolved and fails", mk(agent("doer", "claude-a"), agent("judge-x", "opaque")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 1},
		{"cross family gateway alias passes", mk(agent("doer", "gpt-a"), agent("judge-x", "claude-alias")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 0},
		{"same family fails", mk(agent("doer", "gpt-a"), agent("judge-x", "gpt-b")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 1},
		{"unknown judge node fails", mk(agent("doer", "claude-a")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 1},
		{"unknown doer node fails", mk(agent("judge-x", "gpt-a")),
			[]judgePairing{{"w", "judge-x", []string{"nope"}}}, 1},
		{"unclassifiable judge fails", mk(agent("doer", "claude-a"), agent("judge-x", "blank")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 1},
		{"unknown profile fails", mk(agent("doer", "claude-a"), agent("judge-x", "ghost")),
			[]judgePairing{{"w", "judge-x", []string{"doer"}}}, 1},
		{"row without doers fails", mk(agent("judge-x", "gpt-a")),
			[]judgePairing{{"w", "judge-x", nil}}, 1},
		{"row for unknown workflow fails", mk(), []judgePairing{{"ghost", "judge-x", []string{"d"}}}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := judgePairingProblems(tt.wfs, profs, tt.rows)
			if len(got) != tt.want {
				t.Errorf("got %d problems %v, want %d", len(got), got, tt.want)
			}
		})
	}
}
