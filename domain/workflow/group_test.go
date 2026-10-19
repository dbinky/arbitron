package workflow_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// grpBranchesConfig is a fully valid branches-form config with two tasks.
func grpBranchesConfig() *workflow.GroupConfig {
	return &workflow.GroupConfig{
		Form: workflow.GroupFormBranches,
		Tasks: []workflow.GroupTask{
			{Key: "a", Entry: "a1", Inputs: map[string]any{"mode": "fast"}},
			{Key: "b", Entry: "b1"},
		},
		CompletionRule:     workflow.GroupRuleAllSucceed,
		UnfinishedBehavior: workflow.GroupUnfinishedWaitForRemaining,
		GatherMode:         workflow.GroupGatherResults,
	}
}

// grpWorkflowsConfig is a fully valid workflows-form config that reuses one
// child workflow with distinct inputs.
func grpWorkflowsConfig() *workflow.GroupConfig {
	return &workflow.GroupConfig{
		Form: workflow.GroupFormWorkflows,
		Tasks: []workflow.GroupTask{
			{Key: "east", Workflow: "deploy", Inputs: map[string]any{"region": "us-east-1"}},
			{Key: "west", Workflow: "deploy", Inputs: map[string]any{"region": "us-west-2"}},
		},
		CompletionRule:     workflow.GroupRuleFirstSuccess,
		UnfinishedBehavior: workflow.GroupUnfinishedCancel,
		GatherMode:         workflow.GroupGatherResults,
	}
}

// grpProblemFor returns the first parse problem wrapping sentinel, or nil.
func grpProblemFor(err error, sentinel error) *workflow.GroupConfigProblem {
	for _, p := range workflow.GroupConfigProblems(err) {
		if errors.Is(p, sentinel) {
			return p
		}
	}
	return nil
}

// ---------- vocabulary ----------

func TestGroupVocabulary_ExactStrings(t *testing.T) {
	assert.Equal(t, workflow.NodeType("parallel_group"), workflow.NodeTypeParallelGroup)
	assert.Equal(t, "branches", workflow.GroupFormBranches)
	assert.Equal(t, "workflows", workflow.GroupFormWorkflows)
	assert.Equal(t, "all_succeed", workflow.GroupRuleAllSucceed)
	assert.Equal(t, "first_success", workflow.GroupRuleFirstSuccess)
	assert.Equal(t, "wait_for_remaining", workflow.GroupUnfinishedWaitForRemaining)
	assert.Equal(t, "cancel_unfinished", workflow.GroupUnfinishedCancel)
	assert.Equal(t, "finish_independently", workflow.GroupUnfinishedFinishIndependently)
	assert.Equal(t, "results", workflow.GroupGatherResults)
	assert.Equal(t, "results_and_files", workflow.GroupGatherResultsAndFiles)
	assert.Equal(t, "task", workflow.GroupPortTask)
	assert.Equal(t, "success", workflow.GroupPortSuccess)
	assert.Equal(t, "failure", workflow.GroupPortFailure)
}

// ---------- JSON shape ----------

func TestGroupConfig_DecodesFromManifestJSON(t *testing.T) {
	raw := []byte(`{
		"id": "fan", "type": "parallel_group",
		"config": {"group": {
			"form": "workflows",
			"tasks": [
				{"key": "east", "workflow": "deploy", "inputs": {"region": "us-east-1", "replicas": 3}},
				{"key": "west", "workflow": "deploy"}
			],
			"completionRule": "first_success",
			"unfinishedBehavior": "finish_independently",
			"gatherMode": "results_and_files",
			"repoPath": "{{repo_dir}}/app",
			"resolverProfile": "merger",
			"resolverPromptFile": "prompts/resolve.md"
		}}
	}`)
	var n workflow.Node
	require.NoError(t, json.Unmarshal(raw, &n))
	require.NotNil(t, n.Config.Group)

	cfg, err := workflow.ParseGroupConfig(n.Config)
	require.NoError(t, err)
	assert.Equal(t, workflow.NodeTypeParallelGroup, n.Type)
	assert.Equal(t, "workflows", cfg.Form)
	require.Len(t, cfg.Tasks, 2)
	assert.Equal(t, "east", cfg.Tasks[0].Key)
	assert.Equal(t, "deploy", cfg.Tasks[0].Workflow)
	assert.Equal(t, float64(3), cfg.Tasks[0].Inputs["replicas"], "JSON numbers decode as float64")
	assert.Equal(t, "{{repo_dir}}/app", cfg.RepoPath)
	assert.Equal(t, "merger", cfg.ResolverProfile)
	assert.Equal(t, "prompts/resolve.md", cfg.ResolverPromptFile)
}

// Legacy compatibility: a node without a group must serialize exactly as
// before — no "group" key appears.
func TestGroupConfig_OmittedFromNonGroupNodeJSON(t *testing.T) {
	out, err := json.Marshal(workflow.NodeConfig{TemplateFile: "p.md"})
	require.NoError(t, err)
	assert.NotContains(t, string(out), `"group"`)
}

func TestValidationError_TaskKeyOmittedWhenEmpty(t *testing.T) {
	out, err := json.Marshal(workflow.ValidationError{Code: "X", Message: "m"})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "taskKey")

	out, err = json.Marshal(workflow.ValidationError{Code: "X", Message: "m", TaskKey: "a"})
	require.NoError(t, err)
	assert.Contains(t, string(out), `"taskKey":"a"`)
}

// ---------- happy path ----------

func TestParseGroupConfig_BranchesHappyPath(t *testing.T) {
	cfg, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: grpBranchesConfig()})
	require.NoError(t, err)
	assert.Equal(t, workflow.GroupFormBranches, cfg.Form)
	require.Len(t, cfg.Tasks, 2)
	assert.Equal(t, []string{"a", "b"}, []string{cfg.Tasks[0].Key, cfg.Tasks[1].Key}, "author order is preserved")
	assert.Equal(t, "a1", cfg.Tasks[0].Entry)
}

// Success: one child workflow reused with distinct inputs is legal; only the
// task KEY must be unique, never the workflow name.
func TestParseGroupConfig_WorkflowsFormRepeatedChildIsValid(t *testing.T) {
	cfg, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: grpWorkflowsConfig()})
	require.NoError(t, err)
	assert.Equal(t, "deploy", cfg.Tasks[0].Workflow)
	assert.Equal(t, "deploy", cfg.Tasks[1].Workflow)
	assert.Equal(t, "us-west-2", cfg.Tasks[1].Inputs["region"])
}

func TestParseGroupConfig_OneTaskIsValid(t *testing.T) {
	g := grpBranchesConfig()
	g.Tasks = g.Tasks[:1]
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	assert.NoError(t, err)
}

func TestParseGroupConfig_EveryCompatibleRuleBehaviorPairIsValid(t *testing.T) {
	pairs := [][2]string{
		{workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedWaitForRemaining},
		{workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedCancel},
		{workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedCancel},
		{workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedFinishIndependently},
	}
	for _, p := range pairs {
		g := grpBranchesConfig()
		g.CompletionRule, g.UnfinishedBehavior = p[0], p[1]
		_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
		assert.NoError(t, err, "%s + %s must be accepted", p[0], p[1])
	}
}

func TestParseGroupConfig_FilesModeWithResolverProfileIsValid(t *testing.T) {
	g := grpBranchesConfig()
	g.GatherMode = workflow.GroupGatherResultsAndFiles
	g.ResolverProfile = "merger"
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	assert.NoError(t, err)
}

// ---------- deep copy ----------

func TestParseGroupConfig_ReturnsDeepCopy(t *testing.T) {
	src := grpBranchesConfig()
	src.Tasks[0].Inputs = map[string]any{
		"opts": map[string]any{"depth": []any{"x", map[string]any{"k": "v"}}},
	}
	cfg, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: src})
	require.NoError(t, err)

	// Mutating the parsed copy at depth must not reach the manifest...
	cfg.Tasks[0].Inputs["opts"].(map[string]any)["depth"].([]any)[1].(map[string]any)["k"] = "mutated"
	cfg.Tasks[0].Key = "renamed"
	cfg.Tasks = append(cfg.Tasks[:1], workflow.GroupTask{Key: "z", Entry: "z1"})
	assert.Equal(t, "v", src.Tasks[0].Inputs["opts"].(map[string]any)["depth"].([]any)[1].(map[string]any)["k"])
	assert.Equal(t, "a", src.Tasks[0].Key)
	assert.Equal(t, "b", src.Tasks[1].Key)

	// ...and mutating the manifest afterwards must not reach the parsed copy.
	again, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: src})
	require.NoError(t, err)
	src.Tasks[0].Inputs["opts"].(map[string]any)["depth"].([]any)[0] = "changed"
	assert.Equal(t, "x", again.Tasks[0].Inputs["opts"].(map[string]any)["depth"].([]any)[0])
}

// ---------- failure: one sentinel per problem ----------

func TestParseGroupConfig_MissingGroupObject(t *testing.T) {
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{})
	require.Error(t, err)
	assert.ErrorIs(t, err, workflow.ErrGroupConfigMissing)
	require.Len(t, workflow.GroupConfigProblems(err), 1)
}

func TestParseGroupConfig_InvalidForm(t *testing.T) {
	for _, form := range []string{"", "Branches", "parallel"} {
		g := grpBranchesConfig()
		g.Form = form
		_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
		assert.ErrorIs(t, err, workflow.ErrGroupFormInvalid, "form %q", form)
	}
}

// An unknown form makes every per-task target rule undefined, so only the form
// is reported — not a cascade of target findings the author cannot act on.
func TestParseGroupConfig_InvalidFormSkipsTargetChecks(t *testing.T) {
	g := grpBranchesConfig()
	g.Form = "fanout"
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	require.Error(t, err)
	assert.False(t, errors.Is(err, workflow.ErrGroupTaskTargetInvalid))
}

func TestParseGroupConfig_NoTasks(t *testing.T) {
	g := grpBranchesConfig()
	g.Tasks = nil
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	assert.ErrorIs(t, err, workflow.ErrGroupNoTasks)
}

func TestParseGroupConfig_InvalidTaskKey(t *testing.T) {
	for _, key := range []string{"", "has space", "dotted.key", "slash/key", "ünicode"} {
		g := grpBranchesConfig()
		g.Tasks[1].Key = key
		_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
		p := grpProblemFor(err, workflow.ErrGroupTaskKeyInvalid)
		require.NotNil(t, p, "key %q must be rejected", key)
		assert.Equal(t, 1, p.TaskIndex)
	}
}

func TestParseGroupConfig_TaskKeyCharsetAccepted(t *testing.T) {
	g := grpBranchesConfig()
	g.Tasks[0].Key = "Task_1-a"
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	assert.NoError(t, err)
}

func TestParseGroupConfig_DuplicateTaskKey(t *testing.T) {
	g := grpBranchesConfig()
	g.Tasks[1].Key = "a"
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	p := grpProblemFor(err, workflow.ErrGroupTaskKeyDuplicate)
	require.NotNil(t, p)
	assert.Equal(t, "a", p.TaskKey)
	assert.Equal(t, 1, p.TaskIndex, "the SECOND occurrence is the offender")
}

func TestParseGroupConfig_TaskTargetMustMatchForm(t *testing.T) {
	cases := []struct {
		name string
		mut  func(g *workflow.GroupConfig)
	}{
		{"branches task without entry", func(g *workflow.GroupConfig) { g.Tasks[0].Entry = "" }},
		{"branches task naming a workflow", func(g *workflow.GroupConfig) { g.Tasks[0].Workflow = "deploy" }},
		{"workflows task without workflow", func(g *workflow.GroupConfig) {
			*g = *grpWorkflowsConfig()
			g.Tasks[0].Workflow = ""
		}},
		{"workflows task naming an entry", func(g *workflow.GroupConfig) {
			*g = *grpWorkflowsConfig()
			g.Tasks[0].Entry = "a1"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := grpBranchesConfig()
			tc.mut(g)
			_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
			p := grpProblemFor(err, workflow.ErrGroupTaskTargetInvalid)
			require.NotNil(t, p)
			assert.Equal(t, 0, p.TaskIndex)
			assert.NotEmpty(t, p.TaskKey)
		})
	}
}

func TestParseGroupConfig_DuplicateEntry(t *testing.T) {
	g := grpBranchesConfig()
	g.Tasks[1].Entry = "a1"
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	p := grpProblemFor(err, workflow.ErrGroupTaskEntryDuplicate)
	require.NotNil(t, p)
	assert.Equal(t, "b", p.TaskKey)
}

func TestParseGroupConfig_ReservedWorkspaceOverride(t *testing.T) {
	g := grpWorkflowsConfig()
	g.Tasks[1].Inputs = map[string]any{"workspace": "/tmp/elsewhere"}
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	p := grpProblemFor(err, workflow.ErrGroupReservedInput)
	require.NotNil(t, p)
	assert.Equal(t, "west", p.TaskKey)
}

func TestParseGroupConfig_InvalidRule(t *testing.T) {
	for _, rule := range []string{"", "any_success", "ALL_SUCCEED"} {
		g := grpBranchesConfig()
		g.CompletionRule = rule
		_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
		assert.ErrorIs(t, err, workflow.ErrGroupRuleInvalid, "rule %q", rule)
	}
}

func TestParseGroupConfig_IncompatibleOrUnknownUnfinishedBehavior(t *testing.T) {
	cases := [][2]string{
		{workflow.GroupRuleAllSucceed, workflow.GroupUnfinishedFinishIndependently},
		{workflow.GroupRuleFirstSuccess, workflow.GroupUnfinishedWaitForRemaining},
		{workflow.GroupRuleAllSucceed, ""},
		{workflow.GroupRuleFirstSuccess, "ignore"},
		{"bogus_rule", "ignore"},
	}
	for _, c := range cases {
		g := grpBranchesConfig()
		g.CompletionRule, g.UnfinishedBehavior = c[0], c[1]
		_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
		assert.ErrorIs(t, err, workflow.ErrGroupUnfinishedInvalid, "%s + %q must be rejected", c[0], c[1])
	}
}

// A known behavior under an unknown rule reports only the rule: compatibility
// is undefined until the rule is fixed.
func TestParseGroupConfig_KnownBehaviorUnderUnknownRuleReportsRuleOnly(t *testing.T) {
	g := grpBranchesConfig()
	g.CompletionRule = "quorum"
	g.UnfinishedBehavior = workflow.GroupUnfinishedCancel
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	assert.ErrorIs(t, err, workflow.ErrGroupRuleInvalid)
	assert.False(t, errors.Is(err, workflow.ErrGroupUnfinishedInvalid))
}

func TestParseGroupConfig_InvalidGatherMode(t *testing.T) {
	for _, mode := range []string{"", "files", "RESULTS"} {
		g := grpBranchesConfig()
		g.GatherMode = mode
		_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
		assert.ErrorIs(t, err, workflow.ErrGroupGatherInvalid, "gatherMode %q", mode)
	}
}

func TestParseGroupConfig_FilesModeRequiresResolverProfile(t *testing.T) {
	g := grpBranchesConfig()
	g.GatherMode = workflow.GroupGatherResultsAndFiles
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	assert.ErrorIs(t, err, workflow.ErrGroupResolverProfileMissing)
}

// ---------- error collection ----------

func TestParseGroupConfig_ReportsEveryProblem(t *testing.T) {
	g := &workflow.GroupConfig{
		Form: workflow.GroupFormBranches,
		Tasks: []workflow.GroupTask{
			{Key: "", Entry: "a1"},
			{Key: "b", Entry: "", Inputs: map[string]any{"workspace": "x"}},
		},
		CompletionRule:     "nope",
		UnfinishedBehavior: "nope",
		GatherMode:         workflow.GroupGatherResultsAndFiles,
	}
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	require.Error(t, err)
	for _, s := range []error{
		workflow.ErrGroupTaskKeyInvalid, workflow.ErrGroupTaskTargetInvalid, workflow.ErrGroupReservedInput,
		workflow.ErrGroupRuleInvalid, workflow.ErrGroupUnfinishedInvalid, workflow.ErrGroupResolverProfileMissing,
	} {
		assert.ErrorIs(t, err, s)
	}
	assert.Len(t, workflow.GroupConfigProblems(err), 6)
}

// A caller must never be able to launch from half a contract.
func TestParseGroupConfig_NoPartialResultOnError(t *testing.T) {
	g := grpBranchesConfig()
	g.GatherMode = ""
	got, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	require.Error(t, err)
	assert.Equal(t, workflow.GroupConfig{}, got)
}

func TestGroupConfigProblem_MessageNamesTask(t *testing.T) {
	g := grpBranchesConfig()
	g.Tasks[1].Entry = ""
	_, err := workflow.ParseGroupConfig(workflow.NodeConfig{Group: g})
	p := grpProblemFor(err, workflow.ErrGroupTaskTargetInvalid)
	require.NotNil(t, p)
	assert.Contains(t, p.Error(), `task "b"`)
	assert.Contains(t, p.Error(), "entry")
}

func TestGroupConfigProblems_NilAndForeignErrors(t *testing.T) {
	assert.Nil(t, workflow.GroupConfigProblems(nil))
	assert.Nil(t, workflow.GroupConfigProblems(errors.New("unrelated")))
}
