# pr-review model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these per-stage profiles by alias. They are **admin
profiles stored in the arbitron DB**, NOT seedable from `config.yaml`: the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort levels below. If the profiles table is ever
emptied/reseeded, recreate them with these exact axes (MCP `profile_create` /
`arbitron` admin API).

## The rule the lineup encodes

**The family that writes never certifies.** Every stage that judges is on a
different model family from the stage it judges:

| stage | writes / decides | checked by |
|---|---|---|
| review panel | `architect-*` gpt-6-sol, `10x-*` Opus 5.5 | each other, across rounds |
| security | `security` Opus 5.5 (code lens) | `security-2` gpt-6-astra (supply-chain lens) |
| **the fix** | **`senior-engineer` Opus 5.5 — the ONLY node that mutates the repo** | **`verify-fix` gpt-6-sol** |

The fix row is the one that matters most and was the last to be fixed. Until
2026-09-23 `senior-engineer` and `verify-fix` shared `coder-opus-medium` — the
same model, effort and harness — so the only node that writes code was certified
by its own family, which is exactly the shared-blind-spot case the rest of this
lineup exists to avoid.

It matters more here than anywhere else in the panel for two reasons: the
reviewers only post comments while this node changes the repository, and
`fix-gate` does not gate the push — BOTH paths push, and the verdict only decides
whether the PR comment reads "verified clean" or "pushed but flagged". So
`verify-fix` is an advisory signal on work that ships either way, and a verifier
inclined to agree with itself costs the one signal a human uses to decide whether
to look closely.

| alias | kind | backend | model | effort | tools | used by |
|-------|------|---------|-------|--------|-------|---------|
| approver-codex-high | agent | codex | gpt-6-sol | high | Read,Write,Edit,Grep,Glob,Bash | verify-fix |
| coder-opus-medium | agent | claude-code | claude-opus-5-5 | medium | Read,Write,Edit,Grep,Glob,Bash | senior-engineer |
| reviewer-codex-medium | agent | codex | gpt-6-sol | medium | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | architect-r1, architect-r2 |
| reviewer-opus-high | agent | claude-code | claude-opus-5-5 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | 10x-r1 |
| reviewer-opus-medium | agent | claude-code | claude-opus-5-5 | medium | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | 10x-r2 |
| reviewer-sonnet5-high | agent | claude-code | claude-sonnet-5-5 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | manager |
| security-astra-high | agent | codex | gpt-6-astra | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | security-2 |
| security-opus-high | agent | claude-code | claude-opus-5-5 | high | Read,Grep,Glob,Bash(gh:*),Bash(git:*) | security |

NOTE: the `tools` column is a statement of INTENT, not an enforced read-only
sandbox. On the claude-code profiles the scoped-Bash syntax `Bash(gh:*)`/`Bash(git:*)`
is passed through as an allowlist. On the **codex** profiles
(`reviewer-codex-medium`, `security-astra-high`, `approver-codex-high`) it is NOT enforced at all: the
codex backend drops the tool scope and always runs
`--dangerously-bypass-approvals-and-sandbox`, so those reviewers have full,
unconfirmed shell access and "read-only" is honored only because the prompt says
so. The real boundary is the container — see
[`docs/agent-security-model.md`](../../docs/agent-security-model.md).
(`coder-opus-medium` and `approver-codex-high` use plain `Bash` because
senior-engineer and verify-fix must run arbitrary build/test commands, not just
gh/git — and note that on verify-fix's codex backend even that list is advisory.) If you ever swap a stage back to an opencode-backed
profile, remember the opencode tool map (`adapter/opencode/toolmap.go`) does NOT
parse scoped `Bash(...)` tokens and silently drops them — such profiles must use
plain `Bash` or the agent gets no shell at all.

NOTE: `reviewer-sonnet5-high` sets `context_window: 1m` and runs Sonnet 5.5, which
accepts the full claude-code effort range (no `xhigh → high` flooring that Sonnet
4.6 had). The `*-sonnet5-*` aliases keep their names for compatibility (workflows reference
them by alias); since 2026-09-28 they run Sonnet 5.5 (`claude-sonnet-5-5`).
