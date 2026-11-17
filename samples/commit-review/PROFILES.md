# commit-review model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` routes the `reviewer` input to one review node per model; every
review node shares `commit-review.md` and differs only in its profile alias.
These are **admin profiles stored in the arbitron DB** (most are shared with
`pr-review`), not seedable from `config.yaml`, which has no `effort` or
`context_window` fields. If the profiles table is emptied or reseeded, recreate
them with these exact axes (MCP `profile_create`).

A node's `model_profile` is not templated, so a new reviewer means a new
`review-<name>` node, a new branch on `route`, and a new value in
`scripts/unknown-reviewer.sh`.

| alias | kind | backend | connection | model | effort | context | tools | reviewer value |
|-------|------|---------|------------|-------|--------|---------|-------|----------------|
| reviewer-codex-high | agent | codex | | gpt-6.1-sol | high | default | Read,Write,Edit,Grep,Glob,Bash | gpt-sol (default) |
| reviewer-opus-high | agent | claude-code | | claude-opus-5-5 | high | default | Read,Grep,Glob,Bash(gh:\*),Bash(git:\*) | opus |
| reviewer-sonnet5-high | agent | claude-code | | claude-sonnet-5-5 | high | 1m | Read,Grep,Glob,Bash(gh:\*),Bash(git:\*) | sonnet |
| reviewer-GLM | agent | opencode | ollama-cloud | glm-5.2 | default | default | Read,Grep,Glob,Bash | glm |
| reviewer-ds4-pro | agent | opencode | ollama-cloud | deepseek-v4-pro | default | default | Read,Grep,Glob,Bash | deepseek |

All selections are pinned. The reviewer needs `gh` on PATH and an authenticated
`gh` login on the server; it never needs a checkout.
