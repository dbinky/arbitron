package workflow_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grpProfiles resolves exactly the aliases it holds (alias -> kind). It honours
// the alias argument: a checker answering "agent" for everything would make the
// kind/unknown tests unfailable.
type grpProfiles map[string]string

func (p grpProfiles) ProfileKind(alias string) (string, bool) {
	k, ok := p[alias]
	return k, ok
}

// grpFiles answers existence for exact (workflow name, relative path) pairs, so
// a validator that passed the wrong workflow name would not find the file.
type grpFiles map[string]bool

func (f grpFiles) FileExists(workflowName, relativePath string) bool {
	return f[workflowName+"|"+relativePath]
}

// grpValidBranches is grpBasic with every node shaped so the whole workflow is
// valid under Validate (not only under AnalyzeGroups).
func grpValidBranches() *workflow.Workflow { return grpBasic() }

// grpValidWorkflows is a valid workflows-form group workflow.
func grpValidWorkflows() *workflow.Workflow {
	return &workflow.Workflow{
		Name: "fan",
		Nodes: []workflow.Node{
			grpEntryT("start"),
			grpGroup("g", grpWorkflows([2]string{"east", "deploy"}, [2]string{"west", "deploy"})),
			grpT("done"), grpT("fail"),
		},
		Edges: []workflow.Edge{grpE("start", "g"), grpOK("g", "done"), grpFail("g", "fail")},
	}
}

func grpCfg(wf *workflow.Workflow) *workflow.GroupConfig {
	for i := range wf.Nodes {
		if wf.Nodes[i].ID == "g" {
			return wf.Nodes[i].Config.Group
		}
	}
	panic("fixture has no group g")
}

// grpCodesOn lists the codes reported on one node.
func grpCodesOn(errs []workflow.ValidationError, nodeID string) []string {
	var out []string
	for _, e := range errs {
		if e.NodeID == nodeID {
			out = append(out, e.Code)
		}
	}
	return out
}

// grpCodeList lists the bare codes of errs.
func grpCodeList(errs []workflow.ValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Code)
	}
	return out
}

// ---------- happy path: groups are valid end-to-end ----------

func TestValidateGroups_ValidBranchesWorkflowHasNoFindings(t *testing.T) {
	errs := workflow.Validate(grpValidBranches(), nil, nil, nil)
	assert.Empty(t, errs, grpCodes(errs))
}

func TestValidateGroups_ValidWorkflowsFormHasNoFindings(t *testing.T) {
	errs := workflow.Validate(grpValidWorkflows(), nil, nil, nil)
	assert.Empty(t, errs, grpCodes(errs))
}

func TestValidateGroups_EntrypointGroupIsValid(t *testing.T) {
	wf := grpValidBranches()
	wf.Nodes = wf.Nodes[1:]
	wf.Nodes[0].Entrypoint = true
	wf.Edges = wf.Edges[1:]
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.Empty(t, errs, grpCodes(errs))
}

func TestValidateGroups_DeepNestingIsValid(t *testing.T) {
	errs := workflow.Validate(grpDeep(), nil, nil, nil)
	assert.Empty(t, errs, grpCodes(errs))
}

func TestValidateGroups_FilesModeFullyConfiguredIsValid(t *testing.T) {
	wf := grpValidBranches()
	cfg := grpCfg(wf)
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles
	cfg.ResolverProfile = "merger"
	cfg.ResolverPromptFile = "prompts/resolve.md"
	cfg.RepoPath = "{{repo_dir}}/app"
	errs := workflow.Validate(wf, grpFiles{"fan|prompts/resolve.md": true}, grpProfiles{"merger": "agent"}, nil)
	assert.Empty(t, errs, grpCodes(errs))
}

// ---------- config findings: every sentinel maps to its stable code ----------

func TestValidateGroups_ConfigCodes(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(cfg *workflow.GroupConfig)
		code    string
		taskKey string
	}{
		{"invalid form", func(c *workflow.GroupConfig) { c.Form = "fanout" }, "GROUP_INVALID_FORM", ""},
		{"no tasks", func(c *workflow.GroupConfig) { c.Tasks = nil }, "GROUP_NO_TASKS", ""},
		{"too many tasks", func(c *workflow.GroupConfig) {
			for i := len(c.Tasks); i <= workflow.MaxGroupTasks; i++ {
				c.Tasks = append(c.Tasks, workflow.GroupTask{Key: "t" + strconv.Itoa(i), Entry: "e" + strconv.Itoa(i)})
			}
		}, "GROUP_TOO_MANY_TASKS", ""},
		{"invalid key", func(c *workflow.GroupConfig) { c.Tasks[1].Key = "b.1" }, "GROUP_INVALID_TASK_KEY", "b.1"},
		{"duplicate key", func(c *workflow.GroupConfig) { c.Tasks[1].Key = "a" }, "GROUP_DUPLICATE_TASK_KEY", "a"},
		{"target mismatch", func(c *workflow.GroupConfig) { c.Tasks[1].Workflow = "deploy" }, "GROUP_INVALID_TASK_TARGET", "b"},
		{"duplicate entry", func(c *workflow.GroupConfig) { c.Tasks[1].Entry = "a1" }, "GROUP_DUPLICATE_ENTRY", "b"},
		{"reserved override", func(c *workflow.GroupConfig) {
			c.Tasks[0].Inputs = map[string]any{"workspace": "/elsewhere"}
		}, "GROUP_RESERVED_INPUT", "a"},
		{"invalid rule", func(c *workflow.GroupConfig) { c.CompletionRule = "majority" }, "GROUP_INVALID_RULE", ""},
		{"invalid unfinished", func(c *workflow.GroupConfig) { c.UnfinishedBehavior = "pause" }, "GROUP_INVALID_UNFINISHED", ""},
		{"invalid gather", func(c *workflow.GroupConfig) { c.GatherMode = "files" }, "GROUP_INVALID_GATHER", ""},
		{"files mode without resolver", func(c *workflow.GroupConfig) {
			c.GatherMode = workflow.GroupGatherResultsAndFiles
		}, "GROUP_RESOLVER_PROFILE_REQUIRED", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf := grpValidBranches()
			tc.mut(grpCfg(wf))
			errs := workflow.Validate(wf, nil, nil, nil)
			assert.True(t, grpHas(errs, tc.code, "g", tc.taskKey), grpCodes(errs))
			for _, e := range errs {
				if e.Code == tc.code {
					assert.NotEmpty(t, e.Message)
					assert.Contains(t, e.Message, "g", "message must name the group node")
				}
			}
		})
	}
}

func TestValidateGroups_CollectsConfigStaticAndGraphFindings(t *testing.T) {
	wf := grpValidBranches()
	cfg := grpCfg(wf)
	cfg.Tasks[0].Inputs = map[string]any{"workspace": "/shared"}
	cfg.CompletionRule = "majority"
	cfg.RepoPath = "../repo"
	cfg.ResolverProfile = "chatty"
	cfg.ResolverPromptFile = "prompts/missing.md"
	wf.Edges = append(wf.Edges, grpE("a2", "done"))

	errs := workflow.Validate(wf, grpFiles{}, grpProfiles{"chatty": "chat"}, nil)
	assert.ElementsMatch(t, []string{
		"GROUP_RESERVED_INPUT@g/a", "GROUP_INVALID_RULE@g/",
		"GROUP_INVALID_REPO_PATH@g/", "GROUP_RESOLVER_PROFILE_KIND@g/",
		"GROUP_MISSING_PROMPT_FILE@g/", "GROUP_BODY_ESCAPE@g/a",
	}, grpCodes(errs))
	for _, e := range errs {
		assert.NotEmpty(t, e.Message)
	}
}

func TestValidateGroups_MissingConfigIsTheOnlyFinding(t *testing.T) {
	wf := grpValidWorkflows()
	wf.Nodes[1].Config.Group = nil
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.Equal(t, []string{"GROUP_CONFIG_MISSING"}, grpCodesOn(errs, "g"), grpCodes(errs))
}

func TestValidateGroups_IncompatibleRuleBehaviorPairs(t *testing.T) {
	for _, p := range [][2]string{
		{workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedFinishIndependently},
		{workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedWaitForRemaining},
	} {
		wf := grpValidBranches()
		cfg := grpCfg(wf)
		cfg.CompletionRule, cfg.UnfinishedBehavior = p[0], p[1]
		errs := workflow.Validate(wf, nil, nil, nil)
		assert.True(t, grpHas(errs, "GROUP_INVALID_UNFINISHED", "g", ""), "%s + %s: %v", p[0], p[1], grpCodes(errs))
	}
}

// A workflows-form task naming its OWN workflow recurses until the runtime
// spawn-depth limit; it is caught at author time like a direct self-spawn.
func TestValidateGroups_TaskRunningItsOwnWorkflowIsRejected(t *testing.T) {
	wf := grpValidWorkflows()
	grpCfg(wf).Tasks[1].Workflow = "fan"
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.True(t, grpHas(errs, "GROUP_INVALID_TASK_TARGET", "g", "west"), grpCodes(errs))
}

// ---------- graph findings flow through Validate ----------

func TestValidateGroups_OwnershipFindingsAreReported(t *testing.T) {
	wf := grpValidBranches()
	wf.Edges = append(wf.Edges, grpE("a2", "done"))
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.True(t, grpHas(errs, "GROUP_BODY_ESCAPE", "g", "a"), grpCodes(errs))
}

func TestValidateGroups_GroupCycleRejectedButPlainLoopsUnaffected(t *testing.T) {
	wf := grpValidBranches()
	wf.Edges = append(wf.Edges, grpE("fail", "start"))
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.True(t, grpHas(errs, "GROUP_ON_CYCLE", "g", ""), grpCodes(errs))

	plain := &workflow.Workflow{
		Name:  "loop",
		Nodes: []workflow.Node{grpEntryT("s"), grpT("x")},
		Edges: []workflow.Edge{grpE("s", "x"), grpE("x", "s")},
	}
	assert.Empty(t, workflow.Validate(plain, nil, nil, nil))
}

// ---------- repoPath ----------

func TestValidateGroups_RepoPath(t *testing.T) {
	cases := []struct {
		path string
		ok   bool
	}{
		{"", true},
		{"app", true},
		{"services/api", true},
		{"./app", true},
		{"{{repo_dir}}/repo", true},
		{"{{x}}/repo", true},
		{"../x", false},
		{"a/../../x", false},
		{"/abs/repo", false},
		{`\abs\repo`, false},
		{`app\..\repo`, false},
		{"{{x}}/../repo", false},
		{"   ", false},
	}
	for _, tc := range cases {
		wf := grpValidBranches()
		grpCfg(wf).RepoPath = tc.path
		errs := workflow.Validate(wf, nil, nil, nil)
		got := grpHas(errs, "GROUP_INVALID_REPO_PATH", "g", "")
		assert.Equal(t, !tc.ok, got, "repoPath %q: %v", tc.path, grpCodes(errs))
	}
}

// ---------- resolverProfile ----------

func TestValidateGroups_ResolverProfileKindAndExistence(t *testing.T) {
	profiles := grpProfiles{"merger": "agent", "chatty": "chat"}
	cases := []struct {
		alias string
		code  string
	}{
		{"merger", ""},
		{"chatty", "GROUP_RESOLVER_PROFILE_KIND"},
		{"ghost", "GROUP_UNKNOWN_RESOLVER_PROFILE"},
	}
	for _, tc := range cases {
		wf := grpValidBranches()
		cfg := grpCfg(wf)
		cfg.GatherMode = workflow.GroupGatherResultsAndFiles
		cfg.ResolverProfile = tc.alias
		errs := workflow.Validate(wf, nil, profiles, nil)
		if tc.code == "" {
			assert.Empty(t, grpCodesOn(errs, "g"), "alias %q", tc.alias)
			continue
		}
		assert.True(t, grpHas(errs, tc.code, "g", ""), "alias %q: %v", tc.alias, grpCodes(errs))
	}
}

// Without a profile checker the resolver pass is skipped like every other
// nil-skippable checker; making it mandatory is WithGroupCheckersRequired's job.
func TestValidateGroups_NilProfileCheckerSkipsResolverKind(t *testing.T) {
	wf := grpValidBranches()
	cfg := grpCfg(wf)
	cfg.GatherMode = workflow.GroupGatherResultsAndFiles
	cfg.ResolverProfile = "ghost"
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.Empty(t, grpCodesOn(errs, "g"), grpCodes(errs))
}

// ---------- resolverPromptFile ----------

func TestValidateGroups_ResolverPromptFile(t *testing.T) {
	files := grpFiles{
		"fan|prompts/resolve.md":    true,
		"other|prompts/missing.md":  true,
		"fan|{{prompt}}/resolve.md": true, // existence cannot legitimize a template
	}
	cases := []struct {
		path string
		ok   bool
	}{
		{"prompts/resolve.md", true},
		{"prompts/missing.md", false}, // exists only under ANOTHER workflow's name
		{"../secrets.md", false},
		{"/etc/passwd", false},
		{`\prompts\resolve.md`, false},
		{`prompts\..\secret.md`, false},
		{"{{prompt}}/resolve.md", false},
		{"   ", false},
	}
	for _, tc := range cases {
		wf := grpValidBranches()
		grpCfg(wf).ResolverPromptFile = tc.path
		errs := workflow.Validate(wf, files, nil, nil)
		got := grpHas(errs, "GROUP_MISSING_PROMPT_FILE", "g", "")
		assert.Equal(t, !tc.ok, got, "resolverPromptFile %q: %v", tc.path, grpCodes(errs))
	}
}

func TestValidateGroups_EscapingPromptPathRejectedWithoutChecker(t *testing.T) {
	wf := grpValidBranches()
	grpCfg(wf).ResolverPromptFile = "../x.md"
	errs := workflow.Validate(wf, nil, nil, nil)
	assert.True(t, grpHas(errs, "GROUP_MISSING_PROMPT_FILE", "g", ""), grpCodes(errs))
}

// ---------- IsGroupValidationCode / legacy compatibility ----------

func TestIsGroupValidationCode(t *testing.T) {
	assert.True(t, workflow.IsGroupValidationCode("GROUP_ON_CYCLE"))
	assert.True(t, workflow.IsGroupValidationCode("GROUP_REQUIRED_INPUT_RUNTIME"))
	assert.False(t, workflow.IsGroupValidationCode("UNREACHABLE_NODE"))
	assert.False(t, workflow.IsGroupValidationCode("group_on_cycle"))
	assert.False(t, workflow.IsGroupValidationCode(""))
}

// Legacy regression: a non-group workflow — valid or broken — never receives a
// GROUP_ finding, and its findings are unchanged.
func TestValidateGroups_LegacyWorkflowsUnaffected(t *testing.T) {
	broken := &workflow.Workflow{
		Name: "legacy",
		Nodes: []workflow.Node{
			grpEntryT("s"),
			{ID: "sp", Type: workflow.NodeTypeSpawnWorkflow, Config: workflow.NodeConfig{ChildWorkflow: "c", SpawnMode: "bogus"}},
			{ID: "orphan", Type: workflow.NodeTypeTransform},
		},
		Edges: []workflow.Edge{grpE("s", "sp"), {Source: "sp", Target: "s", Output: "task"}},
	}
	errs := workflow.Validate(broken, nil, nil, nil)
	require.NotEmpty(t, errs)
	for _, e := range errs {
		assert.False(t, strings.HasPrefix(e.Code, "GROUP_"), "legacy workflow got group finding %+v", e)
		assert.Empty(t, e.TaskKey)
	}
	assert.ElementsMatch(t, []string{"INVALID_SPAWN_MODE", "UNREACHABLE_NODE"}, grpCodeList(errs))
}
