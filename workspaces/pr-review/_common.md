# Shared setup (read once per persona)

You are running inside a container with `gh`, `git`, `claude`, and Go available.
The repo at `~/Repos/{{repo}}` is a host bind-mount — changes persist to disk on
the host, and to GitHub if you `git push`.

## Working directory

```
cd ~/Repos/{{repo}}
git fetch origin --prune
PR_BRANCH=$(gh pr view {{pr_number}} --json headRefName -q .headRefName)
PR_HEAD=$(gh pr view {{pr_number}} --json headRefOid -q .headRefOid)
git checkout "$PR_BRANCH" 2>/dev/null || git checkout -B "$PR_BRANCH" "origin/$PR_BRANCH"
git reset --hard "origin/$PR_BRANCH"
```

(Senior engineer is the only persona that should ever leave the worktree dirty.
All review personas are read-only.)

## How to read PR state

- Diff:                 `gh pr diff {{pr_number}}`
- Files changed:        `gh pr diff {{pr_number}} --name-only`
- Source files:         read directly from the working tree (you're on the PR HEAD)
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

## Attribution

Prefix every comment body with your persona tag — `architect:`, `10x:`,
`security:`, `manager:`, `senior:` — so later rounds can identify authors.
