# Parallel Workflow Gathering — Phase 1 Task 1: Group definition model and ParseGroupConfig

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Add the `parallel_group` node type, its typed configuration (`GroupConfig`/`GroupTask` on `NodeConfig.Group`), the shared vocabulary constants, and the single checked, deep-copying parser `ParseGroupConfig` with `errors.Is`-able sentinels.

**Architecture:** A new pure file `domain/workflow/group.go` holds the constants, types, sentinels, the per-problem `GroupConfigProblem` and `ParseGroupConfig`, following the collect-every-problem pattern of `ParseSpawnConfig` (`domain/workflow/workflow.go:126-140`). `workflow.go` gains one node-type constant and one `NodeConfig` field; `validation.go`'s `ValidationError` gains `TaskKey` so the task-scoped findings of P1-T2/T3/T4 can name the task. No behaviour changes for any existing node type; a node without `group` serializes byte-for-byte as before.

**Tech Stack:** Go 1.25.5, standard library (`errors`, `fmt`, `reflect`, `regexp`, `strconv`), testify (`assert`/`require`).

**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (Parallel groups; Completion rules; Gathering file changes); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §1 "Definition model" (settings table, ports) — resolves the typed-config half of **PLANNED DEPENDENCY P1-A**.

## Interfaces

**Consumes (existing code):**
- `domain/workflow/workflow.go:13` `type NodeType string` and the const block `workflow.go:15-30` (last entry `NodeTypeSpawnWorkflow NodeType = "spawn_workflow"` at line 29).
- `domain/workflow/workflow.go:206-232` `type NodeConfig struct` (last field `WaitTimeoutSeconds` at line 231).
- `domain/workflow/validation.go:76-82` `type ValidationError struct { Code, Message, NodeID string; EdgeIdx int; Severity string }`.
- Pattern only: `domain/workflow/workflow.go:126` `func ParseSpawnConfig(cfg NodeConfig) (SpawnConfig, error)` (sentinels + `errors.Join`, zero value on error).

**Produces (exact signatures later tasks rely on):**
```go
const NodeTypeParallelGroup NodeType = "parallel_group"

const (
	GroupFormBranches  = "branches"
	GroupFormWorkflows = "workflows"
	GroupRuleAllSucceed   = "all_succeed"
	GroupRuleFirstSuccess = "first_success"
	GroupUnfinishedWaitForRemaining    = "wait_for_remaining"
	GroupUnfinishedCancel              = "cancel_unfinished"
	GroupUnfinishedFinishIndependently = "finish_independently"
	GroupGatherResults         = "results"
	GroupGatherResultsAndFiles = "results_and_files"
	GroupPortTask    = "task"
	GroupPortSuccess = "success"
	GroupPortFailure = "failure"
)

// NodeConfig gains:  Group *GroupConfig `json:"group,omitempty"`
// ValidationError gains:  TaskKey string `json:"taskKey,omitempty"`

type GroupConfig struct {
	Form               string      `json:"form"`
	Tasks              []GroupTask `json:"tasks"`
	CompletionRule     string      `json:"completionRule"`
	UnfinishedBehavior string      `json:"unfinishedBehavior"`
	GatherMode         string      `json:"gatherMode"`
	RepoPath           string      `json:"repoPath,omitempty"`
	ResolverProfile    string      `json:"resolverProfile,omitempty"`
	ResolverPromptFile string      `json:"resolverPromptFile,omitempty"`
}
type GroupTask struct {
	Key      string         `json:"key"`
	Entry    string         `json:"entry,omitempty"`
	Workflow string         `json:"workflow,omitempty"`
	Inputs   map[string]any `json:"inputs,omitempty"`
}

var (
	ErrGroupConfigMissing, ErrGroupFormInvalid, ErrGroupNoTasks, ErrGroupTaskKeyInvalid,
	ErrGroupTaskKeyDuplicate, ErrGroupTaskTargetInvalid, ErrGroupTaskEntryDuplicate,
	ErrGroupReservedInput, ErrGroupRuleInvalid, ErrGroupUnfinishedInvalid,
	ErrGroupGatherInvalid, ErrGroupResolverProfileMissing error
)

// Additive (backward-compatible refinement of the contract): each joined problem is one of these.
type GroupConfigProblem struct {
	Err       error  // one ErrGroup* sentinel
	TaskIndex int    // 0-based offending task, -1 when not task-scoped
	TaskKey   string // offending task key ("" when not task-scoped / key empty)
	Detail    string
}
func (p *GroupConfigProblem) Error() string
func (p *GroupConfigProblem) Unwrap() error
func GroupConfigProblems(err error) []*GroupConfigProblem

func ParseGroupConfig(cfg NodeConfig) (GroupConfig, error) // deep copy on success; zero value + errors.Join(problems...) on error

// Unexported, used by P1-T2 (AnalyzeGroups) and P1-T4:
func cloneGroupConfig(g GroupConfig) GroupConfig
const groupReservedInputName = "workspace"
```

Semantics later tasks rely on: every enum is exact-match lowercase with **no default** (empty is invalid); a nil `cfg.Group` yields exactly one problem (`ErrGroupConfigMissing`); an unknown `form` suppresses the per-task target checks; a known `unfinishedBehavior` under an unknown `completionRule` reports only the rule; duplicate child workflow names are legal in workflows form (only keys must be unique); entries must be unique in branches form.

## Wiring & data provenance

- **No composition root of its own** — pure definitions and a pure parser. They become live through:
  - `Validate` → `validateGroups` / `validateGroupNode` (**P1-T3**, `domain/workflow/validation_group.go`) maps each `GroupConfigProblem` to a `GROUP_*` code;
  - `AnalyzeGroups` (**P1-T2**) parses (or leniently clones via `cloneGroupConfig`) each group's config;
  - Service writes `Service.Create/Update` → `Service.ValidateGroups` (**P1-T5**), group operations `Service.UpsertGroup` (**P1-T6**), MCP `workflow_group_*` tools (**P1-T7**), checker wiring in `cmd/arbitron/main.go:initDomain` (**P1-T8**);
  - run time: `ParallelGroupExecutor` (**P2-T11**) and `GroupLauncher.Launch` (**P2-T8**, `GroupLaunchRequest.Config`) re-parse the group node from the parent's `Instance.WorkflowSnapshot`; Phase 4 layout `GET /workflows/{name}/group-layout` (**P4-T3**) via `AnalyzeGroups`.
- **Producers of every input:**
  - `cfg NodeConfig` = `Node.Config`, JSON-decoded from `workflow.json` by `adapter/filesystem/workflow_repo.go:WorkflowRepo.Load` (line 99), reached through `domain/workflow/service.go:Service.Load` (line 356); authored through `adapter/mcp/tools_workflow.go:workflowCreateHandler` (line 285) / `workflowUpdateHandler` (line 312) manifests and, from P1-T6/P1-T7, `GroupUpsert.Config`.
  - At run time `cfg` comes from the instance's frozen `WorkflowSnapshot` (decoded by the engine, P2-T11), never re-loaded by name.
- **Provenance probe:** `TestParseGroupConfig_MissingGroupObject` fails if an absent (`nil`) `group` object is accepted instead of reported; `TestParseGroupConfig_ReturnsDeepCopy` fails if the parser hands out the manifest's own maps.

### Task 1: Group definition model and ParseGroupConfig

**Files:**
- Create: `domain/workflow/group.go`
- Modify: `domain/workflow/workflow.go:15-30` (node-type const block — add `NodeTypeParallelGroup` after line 29)
- Modify: `domain/workflow/workflow.go:206-232` (`NodeConfig` — add `Group` after line 231)
- Modify: `domain/workflow/validation.go:76-82` (`ValidationError` — add `TaskKey` after line 81)
- Test: `domain/workflow/group_test.go` (external test package `workflow_test`, testify, like `domain/workflow/spawn_config_test.go`)

Helper names in the test file are prefixed `grp` so they cannot collide with existing helpers in package `workflow_test` (`codesOf`, `extractCodes`, `stubProfileChecker`, `mockFileChecker`, ...).

- [x] Step 1: Write the failing test. Create `domain/workflow/group_test.go`:

```go
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
```

- [x] Step 2: Run it and confirm it fails to compile.

```bash
go test ./domain/workflow/ -run 'TestGroupVocabulary|TestGroupConfig|TestValidationError_TaskKey|TestParseGroupConfig|TestGroupConfigProblem' -v
```
Expected:
```
# github.com/arbitron/arbitron/domain/workflow_test [github.com/arbitron/arbitron/domain/workflow.test]
domain/workflow/group_test.go:14:36: undefined: workflow.GroupConfig
domain/workflow/group_test.go:15:19: undefined: workflow.GroupConfig
domain/workflow/group_test.go:16:18: undefined: workflow.GroupFormBranches
domain/workflow/group_test.go:17:21: undefined: workflow.GroupTask
...
FAIL	github.com/arbitron/arbitron/domain/workflow [build failed]
```

- [x] Step 3: Add the node type. In `domain/workflow/workflow.go`, replace lines 26-30:

```go
	// NodeTypeSpawnWorkflow starts a CHILD workflow instance from within a
	// parent workflow. It is ONE node type with two modes (see SpawnMode*);
	// there is deliberately no separate node type per mode.
	NodeTypeSpawnWorkflow NodeType = "spawn_workflow"
)
```
with:
```go
	// NodeTypeSpawnWorkflow starts a CHILD workflow instance from within a
	// parent workflow. It is ONE node type with two modes (see SpawnMode*);
	// there is deliberately no separate node type per mode.
	NodeTypeSpawnWorkflow NodeType = "spawn_workflow"
	// NodeTypeParallelGroup fans out a fixed, ordered set of tasks (parallel
	// branches or awaited child workflows) and gathers them before continuing
	// through its success or failure port. Its configuration lives in
	// NodeConfig.Group; see group.go.
	NodeTypeParallelGroup NodeType = "parallel_group"
)
```

- [x] Step 4: Add the config field. In `domain/workflow/workflow.go`, replace lines 231-232:

```go
	WaitTimeoutSeconds string            `json:"waitTimeoutSeconds,omitempty"`
}
```
with:
```go
	WaitTimeoutSeconds string            `json:"waitTimeoutSeconds,omitempty"`
	// Group is the parallel_group configuration (parallel_group only). Read it
	// through ParseGroupConfig, which checks it and returns a deep copy.
	Group *GroupConfig `json:"group,omitempty"`
}
```

`omitempty` on a pointer keeps every existing manifest and snapshot byte-identical (`TestGroupConfig_OmittedFromNonGroupNodeJSON`).

- [x] Step 5: Add `TaskKey` to findings. In `domain/workflow/validation.go`, replace lines 80-82:

```go
	EdgeIdx  int    `json:"edgeIdx,omitempty"`
	Severity string `json:"severity,omitempty"`
}
```
with:
```go
	EdgeIdx  int    `json:"edgeIdx,omitempty"`
	Severity string `json:"severity,omitempty"`
	// TaskKey identifies the parallel_group task a finding is about (set
	// together with NodeID = the group node id); empty for every other finding.
	TaskKey string `json:"taskKey,omitempty"`
}
```

(The contract lists this field under P1-T3; it is added here, one task earlier, because P1-T2's `AnalyzeGroups` already reports task-scoped findings. The field and JSON tag are exactly the contract's.)

- [x] Step 6: Minimal implementation. Create `domain/workflow/group.go`:

```go
package workflow

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
)

// Parallel group vocabulary. A parallel_group node fans out a FIXED, ordered
// set of tasks and gathers them before continuing through exactly one of its
// success/failure ports. These constants are the single source of truth for
// the allowed values, shared by validation (this package), the MCP authoring
// tools, the group executor/launcher (Phase 2) and the run views (Phase 4).
// Every value is exact-match lowercase; there are no inherited defaults.
const (
	// GroupFormBranches: each task is a closed, exclusively owned body of
	// nodes inside THIS workflow, entered through a "task" edge.
	GroupFormBranches = "branches"
	// GroupFormWorkflows: each task is an awaited child workflow run.
	GroupFormWorkflows = "workflows"

	// GroupRuleAllSucceed: the group succeeds when every task succeeds.
	GroupRuleAllSucceed = "all_succeed"
	// GroupRuleFirstSuccess: the first successful task wins.
	GroupRuleFirstSuccess = "first_success"

	// GroupUnfinishedWaitForRemaining (all_succeed only): after a task fails,
	// wait for the remaining tasks before taking the failure path.
	GroupUnfinishedWaitForRemaining = "wait_for_remaining"
	// GroupUnfinishedCancel (both rules): cancel unfinished tasks as soon as
	// the group is decided.
	GroupUnfinishedCancel = "cancel_unfinished"
	// GroupUnfinishedFinishIndependently (first_success only): let losing
	// tasks run to completion; they can no longer affect the parent.
	GroupUnfinishedFinishIndependently = "finish_independently"

	// GroupGatherResults gathers task outcomes and results only.
	GroupGatherResults = "results"
	// GroupGatherResultsAndFiles additionally merges eligible file changes into
	// the parent's working area (Phase 3); it requires a resolverProfile.
	GroupGatherResultsAndFiles = "results_and_files"

	// GroupPortTask names the edges from a branches-form group to each task's
	// entry node. Every task edge carries this SAME output name; the unique
	// entry target, not the port name, identifies the task.
	GroupPortTask = "task"
	// GroupPortSuccess / GroupPortFailure are the group's two continuations.
	GroupPortSuccess = "success"
	GroupPortFailure = "failure"
)

// GroupConfig is the configuration of one parallel_group node
// (NodeConfig.Group). Obtain a checked, deep-copied value with
// ParseGroupConfig; never execute from the raw manifest pointer.
type GroupConfig struct {
	Form               string      `json:"form"`
	Tasks              []GroupTask `json:"tasks"`
	CompletionRule     string      `json:"completionRule"`
	UnfinishedBehavior string      `json:"unfinishedBehavior"`
	GatherMode         string      `json:"gatherMode"`
	// RepoPath is an optional templated path, relative to the immediate
	// parent's {{workspace}}, naming an existing repository inside it.
	RepoPath string `json:"repoPath,omitempty"`
	// ResolverProfile is the agent-kind profile alias used for conflict
	// resolution; required when GatherMode is results_and_files.
	ResolverProfile string `json:"resolverProfile,omitempty"`
	// ResolverPromptFile is an optional workflow-relative prompt override.
	ResolverPromptFile string `json:"resolverPromptFile,omitempty"`
}

// GroupTask is one fixed task of a group, in author order.
type GroupTask struct {
	// Key is the task's unique, stable identity: nonempty, ^[A-Za-z0-9_-]+$.
	Key string `json:"key"`
	// Entry (branches form only) is the id of the body's entry node.
	Entry string `json:"entry,omitempty"`
	// Workflow (workflows form only) is the child workflow's name.
	Workflow string `json:"workflow,omitempty"`
	// Inputs are typed literals and/or {{templates}} applied to the task's
	// own copy of the group-start variables. "workspace" is reserved.
	Inputs map[string]any `json:"inputs,omitempty"`
}

// Sentinels ParseGroupConfig reports. Each problem is a *GroupConfigProblem
// wrapping exactly one of these, so validation can map each to its own
// ValidationError code while the executor wraps them verbatim — one set of
// checks, two presentations (the ParseSpawnConfig pattern).
var (
	ErrGroupConfigMissing          = errors.New("parallel_group has no group configuration")
	ErrGroupFormInvalid            = errors.New("unrecognized group form")
	ErrGroupNoTasks                = errors.New("parallel_group has no tasks")
	ErrGroupTaskKeyInvalid         = errors.New("invalid task key")
	ErrGroupTaskKeyDuplicate       = errors.New("duplicate task key")
	ErrGroupTaskTargetInvalid      = errors.New("task target does not match the group form")
	ErrGroupTaskEntryDuplicate     = errors.New("duplicate task entry")
	ErrGroupReservedInput          = errors.New("task input name is reserved")
	ErrGroupRuleInvalid            = errors.New("unrecognized completionRule")
	ErrGroupUnfinishedInvalid      = errors.New("invalid unfinishedBehavior")
	ErrGroupGatherInvalid          = errors.New("unrecognized gatherMode")
	ErrGroupResolverProfileMissing = errors.New("resolverProfile is required for results_and_files")
)

// GroupConfigProblem is one problem found by ParseGroupConfig. It unwraps to
// its sentinel (errors.Is works on the joined error) and carries the offending
// task, when the problem is task-scoped, so validation can fill
// ValidationError.TaskKey without re-deriving the checks.
type GroupConfigProblem struct {
	Err       error  // one of the ErrGroup* sentinels
	TaskIndex int    // 0-based position of the offending task; -1 when not task-scoped
	TaskKey   string // the offending task's key ("" when not task-scoped or the key itself is empty)
	Detail    string // corrective specifics
}

// Error renders the problem as one actionable sentence.
func (p *GroupConfigProblem) Error() string {
	msg := p.Err.Error()
	if p.TaskIndex >= 0 {
		if p.TaskKey != "" {
			msg = "task " + strconv.Quote(p.TaskKey) + ": " + msg
		} else {
			msg = "task #" + strconv.Itoa(p.TaskIndex+1) + ": " + msg
		}
	}
	if p.Detail != "" {
		msg += ": " + p.Detail
	}
	return msg
}

// Unwrap exposes the sentinel to errors.Is / errors.As.
func (p *GroupConfigProblem) Unwrap() error { return p.Err }

// GroupConfigProblems flattens an error returned by ParseGroupConfig into its
// individual problems, in report order. It returns nil for a nil error or an
// error that carries no *GroupConfigProblem.
func GroupConfigProblems(err error) []*GroupConfigProblem {
	var out []*GroupConfigProblem
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if p, ok := e.(*GroupConfigProblem); ok {
			out = append(out, p)
			return
		}
		if j, ok := e.(interface{ Unwrap() []error }); ok {
			for _, inner := range j.Unwrap() {
				walk(inner)
			}
			return
		}
		walk(errors.Unwrap(e))
	}
	walk(err)
	return out
}

// groupTaskKeyRE is the allowed task-key charset. Keys appear in variable
// paths ({{group.byKey.<key>.status}}) and git refs, so dots, slashes and
// whitespace are excluded.
var groupTaskKeyRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// groupReservedInputName is reseeded by the engine for every task (each task
// gets its own working area); an author override would point a task at a
// shared directory.
const groupReservedInputName = "workspace"

// ParseGroupConfig extracts and checks a parallel_group node's contract.
//
// It is the ONE parser shared by author-time validation and the Phase 2
// executor: an instance executes from a WorkflowSnapshot that may predate the
// validation that would have caught a problem, so the executor must re-check,
// and must re-check identically.
//
// EVERY problem is reported (errors.Join of *GroupConfigProblem values) rather
// than the first. On error the zero GroupConfig is returned — a caller must
// never launch from half a contract. On success the result is a DEEP copy:
// Tasks and every nested Inputs map/slice are freshly allocated, so neither
// the caller nor the manifest can mutate the other.
func ParseGroupConfig(cfg NodeConfig) (GroupConfig, error) {
	if cfg.Group == nil {
		return GroupConfig{}, &GroupConfigProblem{
			Err:       ErrGroupConfigMissing,
			TaskIndex: -1,
			Detail:    "set config.group with form, tasks, completionRule, unfinishedBehavior and gatherMode",
		}
	}
	g := cloneGroupConfig(*cfg.Group)

	var problems []error
	add := func(sentinel error, idx int, key, detail string) {
		problems = append(problems, &GroupConfigProblem{Err: sentinel, TaskIndex: idx, TaskKey: key, Detail: detail})
	}

	formKnown := g.Form == GroupFormBranches || g.Form == GroupFormWorkflows
	if !formKnown {
		add(ErrGroupFormInvalid, -1, "", fmt.Sprintf("%s (must be exactly %s or %s)",
			strconv.Quote(g.Form), strconv.Quote(GroupFormBranches), strconv.Quote(GroupFormWorkflows)))
	}

	if len(g.Tasks) == 0 {
		add(ErrGroupNoTasks, -1, "", "declare at least one task")
	}

	seenKeys := make(map[string]bool, len(g.Tasks))
	seenEntries := make(map[string]string, len(g.Tasks))
	for i, t := range g.Tasks {
		switch {
		case t.Key == "":
			add(ErrGroupTaskKeyInvalid, i, "", "every task needs a nonempty key matching [A-Za-z0-9_-]+")
		case !groupTaskKeyRE.MatchString(t.Key):
			add(ErrGroupTaskKeyInvalid, i, t.Key, strconv.Quote(t.Key)+" must match [A-Za-z0-9_-]+")
		case seenKeys[t.Key]:
			add(ErrGroupTaskKeyDuplicate, i, t.Key, "task keys must be unique within a group")
		}
		if t.Key != "" {
			seenKeys[t.Key] = true
		}

		if formKnown {
			switch g.Form {
			case GroupFormBranches:
				if t.Entry == "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a branches task must name its body's entry node")
				}
				if t.Workflow != "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a branches task must not name a workflow; use form "+strconv.Quote(GroupFormWorkflows)+" for child workflows")
				}
				if t.Entry != "" {
					if first, dup := seenEntries[t.Entry]; dup {
						add(ErrGroupTaskEntryDuplicate, i, t.Key, "entry "+strconv.Quote(t.Entry)+" is already the entry of task "+strconv.Quote(first))
					} else {
						seenEntries[t.Entry] = t.Key
					}
				}
			case GroupFormWorkflows:
				if t.Workflow == "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a workflows task must name its child workflow")
				}
				if t.Entry != "" {
					add(ErrGroupTaskTargetInvalid, i, t.Key, "a workflows task must not name an entry node; use form "+strconv.Quote(GroupFormBranches)+" for in-workflow bodies")
				}
			}
		}

		if _, reserved := t.Inputs[groupReservedInputName]; reserved {
			add(ErrGroupReservedInput, i, t.Key, "\"workspace\" is reseeded by the engine for every task; remove it from inputs")
		}
	}

	ruleKnown := g.CompletionRule == GroupRuleAllSucceed || g.CompletionRule == GroupRuleFirstSuccess
	if !ruleKnown {
		add(ErrGroupRuleInvalid, -1, "", fmt.Sprintf("%s (must be exactly %s or %s)",
			strconv.Quote(g.CompletionRule), strconv.Quote(GroupRuleAllSucceed), strconv.Quote(GroupRuleFirstSuccess)))
	}
	if detail := unfinishedProblem(g.CompletionRule, g.UnfinishedBehavior, ruleKnown); detail != "" {
		add(ErrGroupUnfinishedInvalid, -1, "", detail)
	}

	switch g.GatherMode {
	case GroupGatherResults:
	case GroupGatherResultsAndFiles:
		if g.ResolverProfile == "" {
			add(ErrGroupResolverProfileMissing, -1, "", "file gathering needs an agent-kind profile alias to resolve merge conflicts")
		}
	default:
		add(ErrGroupGatherInvalid, -1, "", fmt.Sprintf("%s (must be exactly %s or %s)",
			strconv.Quote(g.GatherMode), strconv.Quote(GroupGatherResults), strconv.Quote(GroupGatherResultsAndFiles)))
	}

	if len(problems) > 0 {
		return GroupConfig{}, errors.Join(problems...)
	}
	return g, nil
}

// unfinishedProblem returns "" when behavior is acceptable for rule. An
// unknown behavior is always reported; a KNOWN behavior under an unknown rule
// is not (compatibility is undefined until the rule is fixed, and the rule is
// already reported).
func unfinishedProblem(rule, behavior string, ruleKnown bool) string {
	switch behavior {
	case GroupUnfinishedWaitForRemaining, GroupUnfinishedCancel, GroupUnfinishedFinishIndependently:
	default:
		return fmt.Sprintf("%s (must be exactly %s, %s or %s)", strconv.Quote(behavior),
			strconv.Quote(GroupUnfinishedWaitForRemaining), strconv.Quote(GroupUnfinishedCancel),
			strconv.Quote(GroupUnfinishedFinishIndependently))
	}
	if !ruleKnown {
		return ""
	}
	switch {
	case rule == GroupRuleAllSucceed && behavior == GroupUnfinishedFinishIndependently:
		return "all_succeed accepts only wait_for_remaining or cancel_unfinished"
	case rule == GroupRuleFirstSuccess && behavior == GroupUnfinishedWaitForRemaining:
		return "first_success accepts only cancel_unfinished or finish_independently"
	}
	return ""
}

// cloneGroupConfig returns a structurally independent copy of g.
func cloneGroupConfig(g GroupConfig) GroupConfig {
	out := g
	if g.Tasks != nil {
		out.Tasks = make([]GroupTask, len(g.Tasks))
		for i, t := range g.Tasks {
			out.Tasks[i] = t
			if t.Inputs != nil {
				out.Tasks[i].Inputs = cloneGroupValue(t.Inputs).(map[string]any)
			}
		}
	}
	return out
}

// cloneGroupValue deep-copies a task input value. map[string]any and []any
// (what JSON decoding produces) take a fast path; any other map or slice type
// is copied via reflection with its static type preserved. Scalars, pointers
// and structs are returned as-is — task inputs are JSON-shaped data.
func cloneGroupValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		if t == nil {
			return t
		}
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = cloneGroupValue(e)
		}
		return out
	case []any:
		if t == nil {
			return t
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = cloneGroupValue(e)
		}
		return out
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(rv.Type(), rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneReflectElem(iter.Value(), rv.Type().Elem()))
		}
		return out.Interface()
	case reflect.Slice:
		if rv.IsNil() {
			return v
		}
		out := reflect.MakeSlice(rv.Type(), rv.Len(), rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out.Index(i).Set(cloneReflectElem(rv.Index(i), rv.Type().Elem()))
		}
		return out.Interface()
	}
	return v
}

// cloneReflectElem deep-copies one container element, converting back to the
// container's element type (an interface-typed nil element stays a zero value).
func cloneReflectElem(e reflect.Value, elemType reflect.Type) reflect.Value {
	if (e.Kind() == reflect.Interface || e.Kind() == reflect.Map || e.Kind() == reflect.Slice) && e.IsNil() {
		return reflect.Zero(elemType)
	}
	c := reflect.ValueOf(cloneGroupValue(e.Interface()))
	if !c.IsValid() {
		return reflect.Zero(elemType)
	}
	return c.Convert(elemType)
}
```

Design notes the implementer must keep (the tests pin them):
- `ParseGroupConfig` returns `GroupConfig{}` whenever it returns an error — the executor must never launch from half a contract (mirrors `ParseSpawnConfig`).
- Each problem is a `*GroupConfigProblem`; `errors.Is(err, ErrGroupX)` works on the joined error because `Unwrap` exposes the sentinel, and `GroupConfigProblems` recovers per-task `TaskKey`/`TaskIndex` for validation.
- The duplicate-key and duplicate-entry problems name the SECOND occurrence (the first keeps its identity).
- `cloneGroupValue` deep-copies `Inputs` at every depth (JSON shapes fast-path, reflection for other map/slice types).

- [x] Step 7: Run the new tests and confirm they pass.

```bash
gofmt -l domain/workflow/
go test ./domain/workflow/ -run 'TestGroupVocabulary|TestGroupConfig|TestValidationError_TaskKey|TestParseGroupConfig|TestGroupConfigProblem' -v
```
Expected (`gofmt -l` prints nothing):
```
--- PASS: TestGroupVocabulary_ExactStrings (0.00s)
--- PASS: TestGroupConfig_DecodesFromManifestJSON (0.00s)
--- PASS: TestGroupConfig_OmittedFromNonGroupNodeJSON (0.00s)
--- PASS: TestValidationError_TaskKeyOmittedWhenEmpty (0.00s)
--- PASS: TestParseGroupConfig_BranchesHappyPath (0.00s)
--- PASS: TestParseGroupConfig_WorkflowsFormRepeatedChildIsValid (0.00s)
--- PASS: TestParseGroupConfig_OneTaskIsValid (0.00s)
--- PASS: TestParseGroupConfig_EveryCompatibleRuleBehaviorPairIsValid (0.00s)
--- PASS: TestParseGroupConfig_FilesModeWithResolverProfileIsValid (0.00s)
--- PASS: TestParseGroupConfig_ReturnsDeepCopy (0.00s)
--- PASS: TestParseGroupConfig_MissingGroupObject (0.00s)
--- PASS: TestParseGroupConfig_InvalidForm (0.00s)
--- PASS: TestParseGroupConfig_InvalidFormSkipsTargetChecks (0.00s)
--- PASS: TestParseGroupConfig_NoTasks (0.00s)
--- PASS: TestParseGroupConfig_InvalidTaskKey (0.00s)
--- PASS: TestParseGroupConfig_TaskKeyCharsetAccepted (0.00s)
--- PASS: TestParseGroupConfig_DuplicateTaskKey (0.00s)
--- PASS: TestParseGroupConfig_TaskTargetMustMatchForm (0.00s)
--- PASS: TestParseGroupConfig_DuplicateEntry (0.00s)
--- PASS: TestParseGroupConfig_ReservedWorkspaceOverride (0.00s)
--- PASS: TestParseGroupConfig_InvalidRule (0.00s)
--- PASS: TestParseGroupConfig_IncompatibleOrUnknownUnfinishedBehavior (0.00s)
--- PASS: TestParseGroupConfig_KnownBehaviorUnderUnknownRuleReportsRuleOnly (0.00s)
--- PASS: TestParseGroupConfig_InvalidGatherMode (0.00s)
--- PASS: TestParseGroupConfig_FilesModeRequiresResolverProfile (0.00s)
--- PASS: TestParseGroupConfig_ReportsEveryProblem (0.00s)
--- PASS: TestParseGroupConfig_NoPartialResultOnError (0.00s)
--- PASS: TestGroupConfigProblem_MessageNamesTask (0.00s)
--- PASS: TestGroupConfigProblems_NilAndForeignErrors (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/workflow	0.2s
```

- [x] Step 8: Confirm nothing else regressed (the new field and type are additive).

```bash
go vet ./domain/workflow/
go test ./domain/workflow/
go build ./...
```
Expected: `go vet` prints nothing; `ok  	github.com/arbitron/arbitron/domain/workflow`; the build succeeds.

- [x] Step 9: Commit.

```bash
git add domain/workflow/group.go domain/workflow/group_test.go domain/workflow/workflow.go domain/workflow/validation.go && git commit -m "feat(workflow): parallel_group definition model and ParseGroupConfig" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

## Completion evidence

- Red: focused tests failed to compile on the missing group definitions before implementation.
- Green: all focused tests and the full workflow package pass; `gofmt -l domain/workflow/`, `go vet ./domain/workflow/`, `go build ./...`, and `git diff --check` are clean.
- Self-review, in order: (1) implementation matches this task's definitions/parser slice; its live consumers and composition wiring remain assigned to the named later tasks above; (2) an isolated Go overlay returning empty success instead of parsing fails both the manifest-output and missing-group tests; (3) reviewed against neighboring spawn parser/tests and existing JSON conventions, with no quality fixes needed.
- Re-ran focused tests, package tests, vet and build after review; all passed.
