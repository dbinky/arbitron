# feature-pipeline model profiles

This parent workflow has **no model nodes**. It runs only scripts, transforms,
conditionals and awaited `spawn_workflow` nodes. Every spawn maps all of its
child's inputs explicitly, so no LLM prepares inputs and no spawn needs a
`model_profile`.

Every model call happens inside a child stage. Each child's profile aliases,
their axes and their doer/judge pairings are listed in that child's own file:

| stage | child workflow | profiles |
|-------|----------------|----------|
| ideate | `ideate` | [`../ideate/PROFILES.md`](../ideate/PROFILES.md) |
| spec | `spec` | [`../spec/PROFILES.md`](../spec/PROFILES.md) |
| design | `design` | [`../design/PROFILES.md`](../design/PROFILES.md) |
| plan | `plan` | [`../plan/PROFILES.md`](../plan/PROFILES.md) |
| implement | `implement` | [`../implement/PROFILES.md`](../implement/PROFILES.md) |
| harden | `harden` | [`../harden/PROFILES.md`](../harden/PROFILES.md) |
| refine | `ralph-loop` | [`../ralph-loop/PROFILES.md`](../ralph-loop/PROFILES.md) |
| review | `pr-review` | [`../pr-review/PROFILES.md`](../pr-review/PROFILES.md) |

The aliases are admin profiles stored in the arbitron DB. Create every alias
those files list before the first run.

- **Following vs pinned** is an admin profile property. A following profile
  tracks its model family's newest release; a pinned one stays on its model id.
- **Each child run pins its models when it is spawned.** A stage started by
  this pipeline keeps the models it resolved at spawn time, so a profile edit
  reaches the next stage the pipeline spawns, not one already running.
