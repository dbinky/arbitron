# ralph-loop model profiles (DB-only — recreate after a profiles-table reseed)

`workflow.json` references these per-role profiles by alias. They are **admin
profiles stored in the arbitron DB**, NOT seedable from `config.yaml` (the config
`ProfileConfig` shape has no `effort`/`context_window` fields, so a config seed
would silently drop the effort levels and the DB is authoritative once non-empty).
If the profiles table is ever emptied/reseeded, recreate them with these exact
axes (MCP `profile_create` / `arbitron` admin API).

## The rule the lineup encodes

**The family that writes never certifies.** `select-work` routes each review to
its own cross-family pair, so a single-system area's two independent approvals
can never be one model grading itself twice — two passes by the same family share
blind spots, which is the same opinion twice rather than redundancy.

| lane | refines | approves |
|------|---------|----------|
| single review 1 (wiring lens) | `coder-sonnet5-high` — Sonnet 5 | `approver-codex-high` — gpt-6-sol |
| single review 2 (failure lens) | `coder-codex-high` — gpt-6-sol | `approver-opus-medium` — Opus 5.5 |
| paired integration seam | `coder-opus-medium` — Opus 5.5 | `approver-codex-high` — gpt-6-sol |

Routing by which box is outstanding also means a withheld approval naturally
sends the same row back to the same refiner next pass, with no extra bookkeeping.

| alias | kind | backend | model | effort | tools | used by |
|-------|------|---------|-------|--------|-------|---------|
| approver-codex-high | agent | codex | gpt-6-sol | high | Read,Write,Edit,Grep,Glob,Bash | assess-single-1, assess-paired |
| approver-opus-medium | agent | claude-code | claude-opus-5-5 | medium | Read,Write,Edit,Grep,Glob,Bash | assess-single-2 |
| assessor-sonnet5-high | chat | (inherit) | claude-sonnet-5 | high | (chat: tool-suppressed) | refine-assess |
| coder-codex-high | agent | codex | gpt-6-sol | high | Read,Write,Edit,Grep,Glob,Bash | refine-single-2 |
| coder-opus-medium | agent | claude-code | claude-opus-5-5 | medium | Read,Write,Edit,Grep,Glob,Bash | refine-paired |
| coder-sonnet5-high | agent | claude-code | claude-sonnet-5 | high | Read,Write,Edit,Grep,Glob,Bash | refine-single-1 |
| coder-sonnet5-medium | agent | claude-code | claude-sonnet-5 | medium | Read,Write,Edit,Grep,Glob,Bash | baseline-tests |
| critic-codex-high | agent | codex | gpt-6-astra | high | Read,Write,Edit,Grep,Glob,Bash | critique-seed, critique-replan |
| planner-opus-high | agent | claude-code | claude-opus-5-5 | high | Read,Write,Edit,Grep,Glob,Bash | seed-ralph |

## Tracking-file contract

The loop is a runtime contract across four files, and the scripts and templates
all assume it. `seed-ralph` must write every one of them:

| artifact | holds |
|---|---|
| `RALPH.md` | the charter plus three role blocks: `## Role: single-review-1`, `## Role: single-review-2`, `## Role: paired-review` |
| `docs/reference/focus-areas.md` | single-system reviews ONLY |
| `docs/reference/paired-reviews.md` | paired seams — a separate file |
| `docs/reference/gaps-identified.md` | gaps, with a `Won't Fix` section |

`select-work.sh`, `count-review-plan.sh`, `record-pass-commit.sh` and
`verify-ralph-seed.sh` all read that layout, and `verify-ralph-seed.sh` greps for
the three role headings by name. Collapsing the paired table back into
`focus-areas.md` validates clean and then silently skips every paired review:
`select-work` fails soft with `next_kind=none`, the router takes its default exit
and the run opens a PR having done only the singles.

## Axis constraints worth knowing

- **codex accepts effort up to `xhigh` but only `default` context.**
- The Sonnet 5 and Opus 5.5 profiles leave `context_window: default` so each
  inherits its model's native 1M window rather than being pinned to a tier.
- Opus 5.5's own API default effort is `medium`, so a `high` profile here is a
  deliberate step up, not a restatement of the default.
