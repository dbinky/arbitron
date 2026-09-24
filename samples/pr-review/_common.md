# Shared setup (read once per persona)

You are running inside a container with `gh`, `git`, `claude`, and Go available.

## Working directory

The PR has already been cloned, IN ISOLATION, from the remote into a per-instance
workspace and checked out on the PR head branch. This pipeline does NOT use any
local repo under `~/` (e.g. `~/Repos`) — never read from or write to it.

```
cd {{work_dir}}                 # isolated shallow clone of {{repo}}, on the PR head branch
export PR_HEAD="{{pr_head}}"     # PR head SHA (inline-comment commit_id)
export PR_BRANCH="{{pr_branch}}"
```

(Review personas are read-only — no commits, no working-tree changes. The senior
engineer persona edits and COMMITS in this worktree; a downstream pipeline step
verifies and pushes those commits — personas never push themselves.)

## Wired-and-fed verification — TRACE, don't just read the diff
The most common defect that slips review is code that is *present and compiles* but
is never **wired-and-fed**: a component constructed only in tests, or fed a
`nil`/empty/hardcoded input because its producer doesn't exist. A diff-only read
cannot see this — you must trace it with the shell you have:

- **Provenance grep.** For every NEW exported field / collaborator / config the PR
  adds, `grep -rn <Symbol> {{work_dir}}` and ask: is it **constructed or populated
  outside `*_test.go`**? If a value is only ever set in tests (e.g. an embedding
  set, a scorer, a handler/orchestrator), it's dead in production — flag it.
  (`grep -rn LookEmbedding` reveals "written nowhere" that no diff read would.)
- **Composition root.** Is each new component actually assembled into the real
  server/DI wiring so the live path reaches it? If the wiring site still passes
  `nil`/leaves a field unset, the feature is inert — flag it.
- **Anti-green-theater.** Spot-check whether a new test would still pass if its
  production input were `nil`/empty (a cheap mutation probe). Flag fakes that
  ignore a load-bearing argument, and "integration" tests that assert an
  intermediate hop instead of the sink.

This trace is mandatory for the architect and 10x personas and welcome from all.

## How to read PR state

- Diff:                 `gh pr diff {{pr_number}}`
- Files changed:        `gh pr diff {{pr_number}} --name-only`
- Source files:         read directly from the working tree at `{{work_dir}}` (you're on the PR HEAD)
- Whole-repo trace:     `grep -rn <Symbol> {{work_dir}}` (provenance — is it used outside tests?)
- PR-level comments:    `gh pr view {{pr_number}} --comments`
- Inline thread tree:   `gh api "repos/{{repo}}/pulls/{{pr_number}}/comments?per_page=100" --paginate`

## How to post

- Inline comment on a specific line:
  ```
  gh api repos/{{repo}}/pulls/{{pr_number}}/comments \
    -F commit_id="$PR_HEAD" -F path="<file>" -F line=<line> -F side=RIGHT \
    -f body="<your-prefix>: ..."
  ```
- Reply in an existing thread:
  ```
  gh api repos/{{repo}}/pulls/{{pr_number}}/comments \
    -F in_reply_to=<parent-comment-id> \
    -f body="<your-prefix>: ..."
  ```
- General PR comment:
  ```
  gh pr comment {{pr_number}} --body "<your-prefix>: ..."
  ```

(All `gh api repos/{{repo}}/...` calls carry an explicit repo, so they work from
any directory — but `cd {{work_dir}}` first so plain `gh pr ...` commands and
source reads/greps resolve correctly.)

## Attribution

Prefix every comment body with your persona tag — `architect:`, `10x:`,
`security:`, `security2:`, `manager:`, `senior:` — so later rounds (and the
automated persona-completeness check) can identify authors.

The two security reviewers are separate personas with separate tags:
`security:` is the code lens, `security2:` the supply-chain and configuration
lens. The completeness gate counts them independently, so neither may post under
the other's prefix.
