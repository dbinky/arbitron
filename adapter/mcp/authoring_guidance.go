package mcp

// AuthoringGuidance is the MCP server's instructions block: the standard
// variable conventions for authoring workflows. It is the single source for
// both the server instructions and the workflow_create/workflow_update hint.
const AuthoringGuidance = `Authoring Arbitron workflows — standard variable conventions.

Reuse these names instead of inventing new ones; the dashboard and other
workflows understand them.

Inputs:
- repo / repo_url: the repository, as owner/name or a clone URL.
- base_branch: the ref the work is cut from, or that a PR targets.
- pr_number: the pull request under work.
- slug: short kebab-case label used for branch and doc naming.
- teams_webhook_url: completion-alert webhook (keep secrets out of committed manifests).

Runtime variables (set by a transform or script node, not declared as inputs):
- branch: the work branch. Prefer this name over bespoke ones such as pr_branch.
- work_dir: the checkout or worktree path inside {{workspace}}.

Git column: the dashboard queue shows each run's branch. It reads, in order,
the variable named by the manifest's optional top-level "gitVariable" field
(dotted paths allowed, e.g. "init-node.pr_branch"), then branch, then
pr_branch, then the last path segment of work_dir. Set gitVariable only when
the branch lives under another name.

Profiles: model/provider selection is pinned per node via config.model_profile.
There is no convention for passing a profile as a workflow input.`

// WorkflowManifestHint is appended to the workflow_create and workflow_update
// tool descriptions: the variable conventions plus the parallel_group guide.
const WorkflowManifestHint = " Use the standard variable conventions from the server instructions (repo/repo_url, base_branch, pr_number, slug, branch, work_dir); set the optional top-level gitVariable when the run's branch lives under a non-standard variable." +
	ParallelGroupGuidance

// ParallelGroupGuidance documents parallel_group authoring for every tool
// that writes workflow definitions (workflow_create, workflow_update,
// workflow_group_upsert).
const ParallelGroupGuidance = ` Parallel groups: a "parallel_group" node fans out a FIXED list of tasks, in the author order you give, and gathers them; its settings live under config.group. ` +
	`Forms: "branches" - each task names an "entry" node of a branch body inside this workflow, and the group has exactly one edge per task {"source":<group>,"target":<entry>,"output":"task","label":<task key>}; entries are distinct, each body is owned by exactly one task (only the group's task edge may enter it, no edge may leave it to the main path or a sibling body, and bodies end at sink nodes). ` +
	`"workflows" - each task names a child "workflow" you can see (the same workflow may repeat with different "inputs") and has no task edges. ` +
	`Every group has exactly one "success" and one "failure" outgoing edge and no other outputs. ` +
	`Tasks: "key" is unique and matches [A-Za-z0-9_-]+; "inputs" are literals or {{templates}} applied to that task's own copy of the group-start variables ("workspace" is reserved and set by the engine); a required child input you do not map is checked at run time (validation reports an advisory). ` +
	`Every task works in its own copy of the parent's {{workspace}}, so a group-start variable holding an absolute path inside the parent's {{workspace}} (the usual work_dir convention) is refused before any task starts unless each task overrides it with a task-local input such as "work_dir":"{{workspace}}/repo". ` +
	`Results are never merged into parent variables: read them downstream as {{<group>.tasks.N.result.<var>}}, {{<group>.byKey.<key>.status}} or {{<group>.winner}}. ` +
	`completionRule "all_succeed" (unfinishedBehavior "wait_for_remaining" or "cancel_unfinished" once a task has failed) or "first_success" (unfinishedBehavior "cancel_unfinished" or "finish_independently" after the winner). ` +
	`gatherMode "results" or "results_and_files"; file gathering requires resolverProfile naming an AGENT-kind profile, an optional resolverPromptFile that must already exist in the workflow (write it with workflow_write_file first), and accepts an optional repoPath relative to the parent's {{workspace}}. ` +
	`Nesting: a group may sit inside another group's branch body (its success/failure continuations stay in that body) and owns its own task bodies. A group may be the entrypoint but never on a cycle. ` +
	`Author bodies and groups together in one workflow_create/workflow_update manifest, revise one group with workflow_group_upsert, inspect with workflow_group_get, remove with workflow_group_delete (it lists retained nodes), and check with workflow_validate. A server that cannot execute parallel groups yet still accepts and validates them but refuses to trigger them. ` +
	`Example (branches group whose "audit" task is a nested first_success workflows group): ` + ParallelGroupExample

// ParallelGroupExample is a complete, valid manifest demonstrating both forms,
// nesting, ordered tasks, per-task inputs on a repeated child workflow, both
// completion rules and all continuation ports. A unit test validates it.
const ParallelGroupExample = `{"name":"parallel-review","description":"Lint and audit a PR in parallel, then report","author":"you",
"inputs":[{"name":"pr_number","type":"number","description":"pull request under work","required":true}],
"nodes":[
 {"id":"fanout","type":"parallel_group","entrypoint":true,"label":"Lint and audit","config":{"group":{"form":"branches",
   "tasks":[{"key":"lint","entry":"lint-run"},{"key":"audit","entry":"audit-fanout"}],
   "completionRule":"all_succeed","unfinishedBehavior":"cancel_unfinished","gatherMode":"results"}}},
 {"id":"lint-run","type":"tool_call","label":"Lint","config":{"command":"make","args":["lint"]}},
 {"id":"audit-fanout","type":"parallel_group","label":"Audit (first answer wins)","config":{"group":{"form":"workflows",
   "tasks":[{"key":"security","workflow":"pr-audit","inputs":{"focus":"security","label":"PR {{pr_number}} security"}},
            {"key":"perf","workflow":"pr-audit","inputs":{"focus":"performance","label":"PR {{pr_number}} performance"}}],
   "completionRule":"first_success","unfinishedBehavior":"finish_independently","gatherMode":"results"}}},
 {"id":"audit-ok","type":"transform","config":{"operations":[{"variable":"audit_winner","operation":"set","value":"{{audit-fanout.winner}}"}]}},
 {"id":"audit-failed","type":"transform","config":{"operations":[{"variable":"audit_failed","operation":"set","value":true}]}},
 {"id":"report","type":"transform","config":{"operations":[{"variable":"lint_status","operation":"set","value":"{{fanout.byKey.lint.status}}"}]}},
 {"id":"report-failure","type":"transform","config":{"operations":[{"variable":"review_failed","operation":"set","value":true}]}}
],
"edges":[
 {"source":"fanout","target":"lint-run","output":"task","label":"lint"},
 {"source":"fanout","target":"audit-fanout","output":"task","label":"audit"},
 {"source":"fanout","target":"report","output":"success"},
 {"source":"fanout","target":"report-failure","output":"failure"},
 {"source":"audit-fanout","target":"audit-ok","output":"success"},
 {"source":"audit-fanout","target":"audit-failed","output":"failure"}
]}`
