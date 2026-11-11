# LunaRoute model identity evidence

This note records source evidence, not live execution verification. LR-03's
reviewed-alias decision is in
[`2026-11-10-lunaroute-provider-decisions.md`](../../designs/2026-11-10-lunaroute-provider-decisions.md#lr-03--fixed-executable-ids-and-alias-handling-under-pinning).

## glm-5.3

- Source title: **Chat completions**.
- URL: https://docs.lunaroute.com/api/chat-completions/
- Markdown source: https://docs.lunaroute.com/api/chat-completions.md
- Accessed: 2026-11-11 (UTC).
- Source passage: the documented `POST /v1/chat/completions` request contains
  `"model": "glm-5.3"`.

This confirms the exact executable spelling. Vendor `zhipuai` follows the
existing GLM family mapping in `domain/modelidentity/identity.go:families`;
LR-03 supplies the reviewed fixed-target decision `glm-5.3 → glm-5.3`.
The request example does not prove live availability or backend compatibility.

## claude-sonnet-5-lr1

- Source title: **Claude Code — The window describes ANTHROPIC_MODEL, not the aliases**.
- URL: https://docs.lunaroute.com/getting-started/claude-code/
- Markdown source: https://docs.lunaroute.com/getting-started/claude-code.md
- Accessed: 2026-11-11 (UTC).
- Source passage:

  > Claude Code applies `CLAUDE_CODE_MAX_CONTEXT_TOKENS` to the model id in `ANTHROPIC_MODEL`, and ignores it
  > for the Claude-shaped alias ids (`claude-sonnet-5-lr1`, …) — those resolve to a real Claude model, which
  > carries its own 1M window.

This confirms the exact alias spelling and its Claude vendor identity. Together
with LR-03, it supports the exact reviewed mapping
`lunaroute / claude-sonnet-5-lr1 → anthropic`. It does not establish an immutable
upstream version, any other `-lrN` alias, or live availability. The classifier
uses the canonical lab name from `domain/modelidentity/identity.go:families`.

P1-T6 collected this alias-specific prerequisite before populating its map.
P1-T7 consumes this evidence for the reviewed alias target
`alias:claude-sonnet-5-lr1`, not an exact upstream-version claim.

## glm-5.3-flash

Identity spelling: **unknown**; not authorized in the production fixed-id table.

## deepseek-4.1-flash-1017

Identity spelling: **unknown**; not authorized in the production fixed-id table.

### Unconfirmed candidates and population blocker

- Source title: **Model catalog**.
- URL: https://docs.lunaroute.com/models/
- Markdown source: https://docs.lunaroute.com/models/index.md
- Additional source: https://docs.lunaroute.com/llms-full.txt (published docs bundle).
- Accessed: 2026-11-11 (UTC).

The catalog documentation says per-model pages are not authored and directs
callers to the runtime catalog. Neither `glm-5.3-flash` nor
`deepseek-4.1-flash-1017` occurs in the published docs bundle. Requests to
`https://docs.lunaroute.com/models/glm-5.3-flash.md` and
`https://docs.lunaroute.com/models/deepseek-4.1-flash-1017.md` returned HTTP 404.
This search is not identity evidence and does not establish alternative
spellings. Listing membership alone would not prove fixed identity either.

The executable reviewed subset is therefore only `glm-5.3` and
`claude-sonnet-5-lr1`. The other two decided release candidates are excluded
from authorization, not silently replaced. Completing the four-model release
set and synchronizing downstream release fixtures requires authoritative
spelling evidence or an explicit release-set revision; P1-T7 Step 2 remains
blocked. An alias remains reviewed, so LR-03's no-alias escalation does not
apply.

P1-T11 still owns the remaining source preparation, capability facts, evidence
register and live gateway verification; none is claimed here.
