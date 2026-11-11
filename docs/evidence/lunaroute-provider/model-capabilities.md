# LunaRoute static model capability evidence

## Sources and review method

Accessed **2026-11-11 UTC**:

- **Model catalog**: https://docs.lunaroute.com/models/;
  Markdown https://docs.lunaroute.com/models/index.md (HTTP 200).
- **LunaRoute documentation (full text)**:
  https://docs.lunaroute.com/llms-full.txt (HTTP 200).
- Candidate per-model Markdown pages at
  `https://docs.lunaroute.com/models/<id>.md` for each of the four headings
  below all returned HTTP 404.

The Model catalog says “The catalog is live data, not a static list in these
docs.” Its published source explicitly says per-model pages are not authored.
There are **no per-model static facts for the release set on that page**.
The Models API's `202752` example is for **glm-5.3-nvfp4**, not `glm-5.3`.
The Claude setup page's “1M” window describes client behavior, not a per-model
limit from the model docs. Neither is copied to another model. Runtime catalog
membership, default client values and short completions are not static evidence.

Each table below explicitly records the result of this source review. Unknown
means unpublished in the required model-doc source, not unsupported. There is
no inferred reasoning boolean, effort level, or token limit.

## glm-5.3

Source: Model catalog above; candidate https://docs.lunaroute.com/models/glm-5.3.md (404).

| Fact | Published value |
| --- | --- |
| context_tokens | unknown |
| output_tokens | unknown |
| reasoning | unknown |
| efforts (chat, claude-code, opencode, codex, pi) | unknown |
| request_settings.temperature | unknown |
| request_settings.output_schema | unknown |
| tasks / backends | unknown; no completed live observation |

## glm-5.3-flash

Source: Model catalog above; candidate https://docs.lunaroute.com/models/glm-5.3-flash.md (404).
Identity is also unconfirmed; this candidate is **not executable**.

| Fact | Published value |
| --- | --- |
| context_tokens | unknown |
| output_tokens | unknown |
| reasoning | unknown |
| efforts (chat, claude-code, opencode, codex, pi) | unknown |
| request_settings.temperature | unknown |
| request_settings.output_schema | unknown |
| tasks / backends | unknown; no completed live observation |

## deepseek-4.1-flash-1017

Source: Model catalog above; candidate https://docs.lunaroute.com/models/deepseek-4.1-flash-1017.md (404).
Identity is also unconfirmed; this candidate is **not executable**.

| Fact | Published value |
| --- | --- |
| context_tokens | unknown |
| output_tokens | unknown |
| reasoning | unknown |
| efforts (chat, claude-code, opencode, codex, pi) | unknown |
| request_settings.temperature | unknown |
| request_settings.output_schema | unknown |
| tasks / backends | unknown; no completed live observation |

## claude-sonnet-5-lr1

Source: Model catalog above; candidate https://docs.lunaroute.com/models/claude-sonnet-5-lr1.md (404).
Alias identity evidence is separate in [model-identities.md](model-identities.md).

| Fact | Published value |
| --- | --- |
| context_tokens | unknown |
| output_tokens | unknown |
| reasoning | unknown |
| efforts (chat, claude-code, opencode, codex, pi) | unknown |
| request_settings.temperature | unknown |
| request_settings.output_schema | unknown |
| tasks / backends | unknown; no completed live observation |

## LR-02 on-failure application

No reviewed release model has the published limits required for Claude Code,
OpenCode or Pi. None may be accepted on those backends; Pi reasoning must not
be defaulted to false. **LR-02 is escalated**: obtain authoritative per-model
documentation (or an explicit decision changing the source contract) before
backend readiness can pass. Unknown facts do not prevent a reviewed identity
from being selected for chat with default settings, but are not proof of chat
success. The two unconfirmed identities remain excluded by LR-03.
