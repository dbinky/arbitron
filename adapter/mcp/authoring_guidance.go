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
// tool descriptions.
const WorkflowManifestHint = " Use the standard variable conventions from the server instructions (repo/repo_url, base_branch, pr_number, slug, branch, work_dir); set the optional top-level gitVariable when the run's branch lives under a non-standard variable."
