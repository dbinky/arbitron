# Parallel Workflow Gathering — Phase 1 Task 3: Wire group validation into Validate

Dependency order and shared contracts: [alignment index](2026-10-16-parallel-workflow-gathering-implementation-alignment.md).

**Goal:** Make `workflow.Validate` report every parallel_group problem with a stable `GROUP_*` code, the group node id and (when task-scoped) the task key: all `ParseGroupConfig` problems, the shared `AnalyzeGroups` graph findings, the static `repoPath` check, the `resolverProfile` kind/existence check, the `resolverPromptFile` path/existence check and the direct self-run check — while leaving every non-group workflow's findings unchanged.

**Architecture:** A new file `domain/workflow/validation_group.go` holds `IsGroupValidationCode`, `validateGroups` and `validateGroupNode`; `Validate` (`domain/workflow/validation.go:116-156`) gains one call to `validateGroups`, which is a no-op when `HasGroups(wf)` is false. Profile and file checks reuse the positional `ProfileChecker`/`FileChecker` that `Validate` already receives (nil-skippable, like the existing passes); making them mandatory is P1-T4's `WithGroupCheckersRequired`.

**Tech Stack:** Go 1.25.5, standard library (`errors`, `strconv`, `strings`), testify.

**Spec / Design:** `docs/specs/2026-10-16-parallel-workflow-gathering-spec.md` (MCP authoring: "Invalid group definitions and incompatible choices receive actionable authoring feedback"); `docs/designs/2026-10-16-parallel-workflow-gathering-design-phase-1.md` §2 "Validation" (config checks through validation, repoPath, resolverProfile agent kind, override file existence, stable codes collecting all errors).

## Interfaces

**Consumes:**
- P1-T1 (`domain/workflow/group.go`): `NodeTypeParallelGroup`; `GroupFormWorkflows`; `type GroupConfig`/`GroupTask`; `func ParseGroupConfig(cfg NodeConfig) (GroupConfig, error)`; `type GroupConfigProblem struct { Err error; TaskIndex int; TaskKey string; Detail string }` with `Error()`/`Unwrap()`; `func GroupConfigProblems(err error) []*GroupConfigProblem`; the twelve sentinels `ErrGroupConfigMissing … ErrGroupResolverProfileMissing`; `ValidationError.TaskKey`.
- P1-T2 (`domain/workflow/group_ownership.go`): `func HasGroups(wf *Workflow) bool`; `func AnalyzeGroups(wf *Workflow) (*GroupGraph, []ValidationError)`.
- P1-T2 test helpers (`domain/workflow/group_ownership_test.go`, same test package): `grpT`, `grpEntryT`, `grpGroup`, `grpWorkflows`, `grpE`, `grpOK`, `grpFail`, `grpBasic() *workflow.Workflow` (Name `"fan"`, group `g`, tasks `a`→`a1`,`a2` and `b`→`b1`, continuations `done`/`fail`), `grpDeep()`, `grpHas(errs, code, nodeID, taskKey) bool`, `grpCodes(errs) []string`.
- Existing: `domain/workflow/validation.go:14` `type FileChecker interface { FileExists(workflowName, relativePath string) bool }`; `:21` `type ProfileChecker interface { ProfileKind(alias string) (kind string, ok bool) }` (kinds `"chat"`/`"agent"`, as used by `validateModelProfiles` at `:240`); `:59-61` `type validateOptions struct`; `:116` `func Validate(wf *Workflow, checker FileChecker, profiles ProfileChecker, connChecker ConnectionChecker, opts ...ValidateOption) []ValidationError`.

**Produces:**
```go
func IsGroupValidationCode(code string) bool // strings.HasPrefix(code, "GROUP_")

// unexported, extended by P1-T4:
func validateGroups(wf *Workflow, files FileChecker, profiles ProfileChecker, o validateOptions) []ValidationError
func validateGroupNode(wf *Workflow, n Node, files FileChecker, profiles ProfileChecker) []ValidationError
func groupRelativePathProblem(p string, allowTemplates bool) string
```
Codes produced by this task (stable strings): `GROUP_CONFIG_MISSING, GROUP_INVALID_FORM, GROUP_NO_TASKS, GROUP_INVALID_TASK_KEY, GROUP_DUPLICATE_TASK_KEY, GROUP_INVALID_TASK_TARGET, GROUP_DUPLICATE_ENTRY, GROUP_RESERVED_INPUT, GROUP_INVALID_RULE, GROUP_INVALID_UNFINISHED, GROUP_INVALID_GATHER, GROUP_RESOLVER_PROFILE_REQUIRED, GROUP_INVALID_REPO_PATH, GROUP_UNKNOWN_RESOLVER_PROFILE, GROUP_RESOLVER_PROFILE_KIND, GROUP_MISSING_PROMPT_FILE`, plus every P1-T2 graph code surfaced through `Validate`. `Validate` output for a workflow without groups is unchanged.

Static `repoPath` rule (pinned by tests): empty is fine; blank, absolute (`/…`, `\…`) and any `..` segment (also after a template, e.g. `{{x}}/../repo`) are `GROUP_INVALID_REPO_PATH`; template segments such as `{{x}}/repo` are accepted — their runtime value is checked for containment by the launcher (P2-T8 / P3-T10). `resolverPromptFile` uses the same rule without templates, then existence via `FileChecker.FileExists(wf.Name, path)`.

## Wiring & data provenance

- **Live path:** this task IS the wiring of P1-T1/P1-T2 into the existing validator: `Validate` (`domain/workflow/validation.go:116`) → `validateGroups`. `Validate` is already called by `adapter/mcp/tools_workflow.go:workflowValidateHandler` (line 402) and `adapter/http/workflow_handler.go:WorkflowHandler.Validate` (line 213); group-aware write rejection is **P1-T5** (`Service.ValidateGroups`, Create/Update), the MCP group tools **P1-T7**, and checker wiring at the composition root **P1-T8** (`workflowSvc.SetGroupCheckers(...)` in `cmd/arbitron/main.go:initDomain`).
- **Producers of every input:**
  - `wf`: `domain/workflow/service.go:Service.Load` (line 356) or the manifest given to `Service.Create`/`Update` (lines 234/271).
  - `files FileChecker`: MCP/HTTP pass `ownerBoundChecker{fr: FileReader, owner}` (`adapter/mcp/tools_workflow.go:35`), whose underlying producer is `adapter/filesystem/file_reader.go:FileReaderAdapter.FileExists` (line 113); P1-T5's `OwnerFileChecker` binds the same adapter on the service write path.
  - `profiles ProfileChecker`: `adapter/provider/checker.go:ResolverChecker.ProfileKind` (line 36) → `adapter/provider/dynamic.go:DynamicProfileResolver.Kind`, injected as `srv.deps.ProfileChecker` (MCP) / `h.profileChecker` (HTTP), and for writes via P1-T5 `SetGroupCheckers`.
- **Provenance probe:** Step 5 runs the suite with `validateGroups` implemented but NOT called from `Validate`; ten tests fail (listed there), proving the tests fail when the wiring line is absent. `TestValidateGroups_ResolverPromptFile` passes the workflow name through the fake (`grpFiles` keys on `workflowName|path`), so a validator querying the wrong workflow folder fails it.

### Task 3: validation_group.go and the Validate wiring

**Files:**
- Create: `domain/workflow/validation_group.go`
- Modify: `domain/workflow/validation.go:151-156` (end of `Validate`; lines 154-159 once P1-T1 has added `TaskKey`) — add the `validateGroups` call before `return errs`
- Test: `domain/workflow/validation_group_test.go` (package `workflow_test`, testify)

- [x] Step 1: Write the failing test. Create `domain/workflow/validation_group_test.go`:

```go
package workflow_test

import (
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
	files := grpFiles{"fan|prompts/resolve.md": true, "other|prompts/missing.md": true}
	cases := []struct {
		path string
		ok   bool
	}{
		{"prompts/resolve.md", true},
		{"prompts/missing.md", false}, // exists only under ANOTHER workflow's name
		{"../secrets.md", false},
		{"/etc/passwd", false},
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
```

- [x] Step 2: Run it and confirm it fails to compile.

```bash
go test ./domain/workflow/ -run 'TestValidateGroups|TestIsGroupValidationCode' -v
```
Expected:
```
# github.com/arbitron/arbitron/domain/workflow_test [github.com/arbitron/arbitron/domain/workflow.test]
domain/workflow/validation_group_test.go:303:26: undefined: workflow.IsGroupValidationCode
domain/workflow/validation_group_test.go:304:26: undefined: workflow.IsGroupValidationCode
...
FAIL	github.com/arbitron/arbitron/domain/workflow [build failed]
```

- [x] Step 3: Minimal implementation. Create `domain/workflow/validation_group.go`:

```go
package workflow

import (
	"errors"
	"strconv"
	"strings"
)

// IsGroupValidationCode reports whether code is a parallel_group finding.
// Every group code — errors and the GROUP_REQUIRED_INPUT_RUNTIME advisory —
// carries the "GROUP_" prefix, so a caller can isolate group findings without
// enumerating them.
func IsGroupValidationCode(code string) bool {
	return strings.HasPrefix(code, "GROUP_")
}

// groupConfigCodes maps each ParseGroupConfig sentinel to its stable code.
var groupConfigCodes = []struct {
	sentinel error
	code     string
}{
	{ErrGroupConfigMissing, "GROUP_CONFIG_MISSING"},
	{ErrGroupFormInvalid, "GROUP_INVALID_FORM"},
	{ErrGroupNoTasks, "GROUP_NO_TASKS"},
	{ErrGroupTaskKeyInvalid, "GROUP_INVALID_TASK_KEY"},
	{ErrGroupTaskKeyDuplicate, "GROUP_DUPLICATE_TASK_KEY"},
	{ErrGroupTaskTargetInvalid, "GROUP_INVALID_TASK_TARGET"},
	{ErrGroupTaskEntryDuplicate, "GROUP_DUPLICATE_ENTRY"},
	{ErrGroupReservedInput, "GROUP_RESERVED_INPUT"},
	{ErrGroupRuleInvalid, "GROUP_INVALID_RULE"},
	{ErrGroupUnfinishedInvalid, "GROUP_INVALID_UNFINISHED"},
	{ErrGroupGatherInvalid, "GROUP_INVALID_GATHER"},
	{ErrGroupResolverProfileMissing, "GROUP_RESOLVER_PROFILE_REQUIRED"},
}

// validateGroups runs every parallel_group check. It is a no-op for a
// workflow without groups, so legacy validation output is unchanged.
//
// Per group node: config problems (ParseGroupConfig, the parser the executor
// also uses), the static repoPath check, the resolverProfile kind check (when
// a ProfileChecker is supplied), the resolverPromptFile path/existence check
// (existence only when a FileChecker is supplied) and the direct self-run
// check. Then the shared ownership analysis (AnalyzeGroups) reports ports,
// task edges, body ownership and the cycle rule for the whole graph.
func validateGroups(wf *Workflow, files FileChecker, profiles ProfileChecker, o validateOptions) []ValidationError {
	if !HasGroups(wf) {
		return nil
	}
	var errs []ValidationError
	for _, n := range wf.Nodes {
		if n.Type != NodeTypeParallelGroup {
			continue
		}
		errs = append(errs, validateGroupNode(wf, n, files, profiles)...)
	}
	_, graphErrs := AnalyzeGroups(wf)
	errs = append(errs, graphErrs...)
	return errs
}

// validateGroupNode checks one group node's configuration.
func validateGroupNode(wf *Workflow, n Node, files FileChecker, profiles ProfileChecker) []ValidationError {
	var errs []ValidationError

	_, parseErr := ParseGroupConfig(n.Config)
	for _, p := range GroupConfigProblems(parseErr) {
		code := "GROUP_CONFIG_MISSING"
		for _, m := range groupConfigCodes {
			if errors.Is(p, m.sentinel) {
				code = m.code
				break
			}
		}
		errs = append(errs, ValidationError{
			Code:    code,
			Message: "parallel_group " + n.ID + ": " + p.Error(),
			NodeID:  n.ID,
			TaskKey: p.TaskKey,
		})
	}

	raw := n.Config.Group
	if raw == nil {
		return errs // GROUP_CONFIG_MISSING already says everything actionable
	}

	if problem := groupRelativePathProblem(raw.RepoPath, true); problem != "" {
		errs = append(errs, ValidationError{
			Code: "GROUP_INVALID_REPO_PATH",
			Message: "parallel_group " + n.ID + " repoPath " + strconv.Quote(raw.RepoPath) + " " + problem +
				"; use a path relative to the parent's {{workspace}}, e.g. \"repo\" or \"{{repo_dir}}/app\"",
			NodeID: n.ID,
		})
	}

	if raw.ResolverProfile != "" && profiles != nil {
		kind, ok := profiles.ProfileKind(raw.ResolverProfile)
		switch {
		case !ok:
			errs = append(errs, ValidationError{
				Code:    "GROUP_UNKNOWN_RESOLVER_PROFILE",
				Message: "parallel_group " + n.ID + " references unknown resolverProfile " + strconv.Quote(raw.ResolverProfile) + "; create the profile or use an existing agent profile alias",
				NodeID:  n.ID,
			})
		case kind != "agent":
			errs = append(errs, ValidationError{
				Code:    "GROUP_RESOLVER_PROFILE_KIND",
				Message: "parallel_group " + n.ID + " resolverProfile " + strconv.Quote(raw.ResolverProfile) + " is a " + kind + " profile; conflict resolution edits files and requires an agent profile",
				NodeID:  n.ID,
			})
		}
	}

	if raw.ResolverPromptFile != "" {
		if problem := groupRelativePathProblem(raw.ResolverPromptFile, false); problem != "" {
			errs = append(errs, ValidationError{
				Code:    "GROUP_MISSING_PROMPT_FILE",
				Message: "parallel_group " + n.ID + " resolverPromptFile " + strconv.Quote(raw.ResolverPromptFile) + " " + problem + "; use a path relative to the workflow folder",
				NodeID:  n.ID,
			})
		} else if files != nil && !files.FileExists(wf.Name, raw.ResolverPromptFile) {
			errs = append(errs, ValidationError{
				Code:    "GROUP_MISSING_PROMPT_FILE",
				Message: "parallel_group " + n.ID + " references missing resolverPromptFile " + strconv.Quote(raw.ResolverPromptFile) + "; write it with workflow_write_file or remove the override to use the default prompt",
				NodeID:  n.ID,
			})
		}
	}

	if raw.Form == GroupFormWorkflows && wf.Name != "" {
		for _, t := range raw.Tasks {
			if t.Workflow == wf.Name {
				errs = append(errs, ValidationError{
					Code: "GROUP_INVALID_TASK_TARGET",
					Message: "parallel_group " + n.ID + " task " + strconv.Quote(t.Key) + " runs its own workflow " + strconv.Quote(wf.Name) +
						"; a run that starts itself recurses until it hits the runtime spawn-depth limit",
					NodeID:  n.ID,
					TaskKey: t.Key,
				})
			}
		}
	}

	return errs
}

// groupRelativePathProblem returns "" when p is statically acceptable as a
// path relative to a base directory, else a short description of the problem.
// Rejected: blank, absolute ("/..."), and any ".." segment — including after
// a template, since "{{x}}/../y" is an escape attempt whatever x resolves to.
// When allowTemplates, {{...}} segments are accepted as-is: their runtime
// value is checked for containment by the launcher (Phase 2/3), not here.
// When !allowTemplates, a template marker is itself a problem.
func groupRelativePathProblem(p string, allowTemplates bool) string {
	if p == "" {
		return ""
	}
	if strings.TrimSpace(p) == "" {
		return "is blank"
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return "is absolute"
	}
	if !allowTemplates && strings.Contains(p, "{{") {
		return "must not contain templates"
	}
	for _, seg := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if seg == ".." {
			return "must not contain \"..\" segments"
		}
	}
	return ""
}
```

Notes:
- The config pass reuses `ParseGroupConfig` — the same parser the Phase 2 executor runs — and maps each `GroupConfigProblem` through `groupConfigCodes`; `TaskKey` comes from the problem, so a duplicate-key finding names the duplicated key and a target finding names the offending task.
- `GROUP_CONFIG_MISSING` short-circuits the per-node static checks (there is nothing else to check), but `AnalyzeGroups` still runs, so port findings on that node are still reported.
- `validateGroups` takes `o validateOptions` now so P1-T4 can extend it without changing the call site.

- [x] Step 4: Prove the wiring matters — run before wiring.

```bash
go test ./domain/workflow/ -run 'TestValidateGroups|TestIsGroupValidationCode'
```
Expected (it compiles; every test that needs a group finding fails because `Validate` does not call `validateGroups` yet):
```
--- FAIL: TestValidateGroups_ConfigCodes (0.00s)
    --- FAIL: TestValidateGroups_ConfigCodes/invalid_form (0.00s)
        validation_group_test.go:143:
            	Error:      	Should be true
...
--- FAIL: TestValidateGroups_MissingConfigIsTheOnlyFinding (0.00s)
--- FAIL: TestValidateGroups_IncompatibleRuleBehaviorPairs (0.00s)
--- FAIL: TestValidateGroups_TaskRunningItsOwnWorkflowIsRejected (0.00s)
--- FAIL: TestValidateGroups_OwnershipFindingsAreReported (0.00s)
--- FAIL: TestValidateGroups_GroupCycleRejectedButPlainLoopsUnaffected (0.00s)
--- FAIL: TestValidateGroups_RepoPath (0.00s)
--- FAIL: TestValidateGroups_ResolverProfileKindAndExistence (0.00s)
--- FAIL: TestValidateGroups_ResolverPromptFile (0.00s)
--- FAIL: TestValidateGroups_EscapingPromptPathRejectedWithoutChecker (0.00s)
FAIL
FAIL	github.com/arbitron/arbitron/domain/workflow	0.3s
```

- [x] Step 5: Wire it. In `domain/workflow/validation.go`, at the end of `Validate`, replace:

```go
	if o.workflows != nil {
		errs = append(errs, validateChildWorkflowReferences(wf, o.workflows)...)
	}

	return errs
}
```
with:
```go
	if o.workflows != nil {
		errs = append(errs, validateChildWorkflowReferences(wf, o.workflows)...)
	}

	// parallel_group checks (validation_group.go). A no-op without groups, so
	// legacy workflows see exactly the findings they always did.
	errs = append(errs, validateGroups(wf, checker, profiles, o)...)

	return errs
}
```

- [x] Step 6: Run the tests and confirm they pass.

```bash
gofmt -l domain/workflow/
go test ./domain/workflow/ -run 'TestValidateGroups|TestIsGroupValidationCode' -v
```
Expected (`gofmt -l` prints nothing):
```
--- PASS: TestValidateGroups_ValidBranchesWorkflowHasNoFindings (0.00s)
--- PASS: TestValidateGroups_ValidWorkflowsFormHasNoFindings (0.00s)
--- PASS: TestValidateGroups_EntrypointGroupIsValid (0.00s)
--- PASS: TestValidateGroups_DeepNestingIsValid (0.00s)
--- PASS: TestValidateGroups_FilesModeFullyConfiguredIsValid (0.00s)
--- PASS: TestValidateGroups_ConfigCodes (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/invalid_form (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/no_tasks (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/invalid_key (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/duplicate_key (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/target_mismatch (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/duplicate_entry (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/reserved_override (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/invalid_rule (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/invalid_unfinished (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/invalid_gather (0.00s)
    --- PASS: TestValidateGroups_ConfigCodes/files_mode_without_resolver (0.00s)
--- PASS: TestValidateGroups_MissingConfigIsTheOnlyFinding (0.00s)
--- PASS: TestValidateGroups_IncompatibleRuleBehaviorPairs (0.00s)
--- PASS: TestValidateGroups_TaskRunningItsOwnWorkflowIsRejected (0.00s)
--- PASS: TestValidateGroups_OwnershipFindingsAreReported (0.00s)
--- PASS: TestValidateGroups_GroupCycleRejectedButPlainLoopsUnaffected (0.00s)
--- PASS: TestValidateGroups_RepoPath (0.00s)
--- PASS: TestValidateGroups_ResolverProfileKindAndExistence (0.00s)
--- PASS: TestValidateGroups_NilProfileCheckerSkipsResolverKind (0.00s)
--- PASS: TestValidateGroups_ResolverPromptFile (0.00s)
--- PASS: TestValidateGroups_EscapingPromptPathRejectedWithoutChecker (0.00s)
--- PASS: TestIsGroupValidationCode (0.00s)
--- PASS: TestValidateGroups_LegacyWorkflowsUnaffected (0.00s)
PASS
ok  	github.com/arbitron/arbitron/domain/workflow	0.3s
```

- [x] Step 7: Regression — the whole package (every existing legacy validation test must still pass unchanged), vet, and the adapters that render validation output.

```bash
go vet ./domain/workflow/
go test ./domain/workflow/ ./adapter/mcp/ ./adapter/http/
```
Expected:
```
ok  	github.com/arbitron/arbitron/domain/workflow	0.3s
ok  	github.com/arbitron/arbitron/adapter/mcp	0.8s
ok  	github.com/arbitron/arbitron/adapter/http	6.5s
```

### Self-review and verification record

1. **Spec compliance:** implemented this task's parser-code mapping, graph findings,
   static paths, optional checker passes and self-run check only; no new options.
2. **Anti-green-theater:** observed the expected undefined-symbol failure, then
   all ten expected failing tests before wiring. Tests assert final `Validate`
   findings with node/task identity; checker fakes honor alias and workflow/path.
   Added combined-error collection, backslash path and prompt-template coverage.
   Confirmed existing MCP/HTTP handlers load real workflows and pass their
   configured file/profile checkers into the now-wired `Validate` call.
3. **Code quality:** reviewed neighboring validation/ownership code; reused the
   shared parser, graph analysis and test helpers. No further production changes.

Final rerun: focused tests and workflow/MCP/HTTP regressions passed uncached;
`go vet ./domain/workflow/`, `gofmt -l domain/workflow/` and `git diff --check`
were clean. No blockers.

- [x] Step 8: Commit.

```bash
git add domain/workflow/validation_group.go domain/workflow/validation_group_test.go domain/workflow/validation.go && git commit -m "feat(workflow): validate parallel_group definitions in Validate" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
