# LunaRoute static model capability evidence

## Sources and review method

Source title: **Authenticated LunaRoute Models API metadata**,
`GET https://gw.lunaroute.com/v1/models`, retrieved by the operator on
**2026-11-11**. The credential-free snapshot is
[sources/lr02-live-models-2026-11-11.json](sources/lr02-live-models-2026-11-11.json).
[Operator ruling R2](../../designs/2026-11-10-lunaroute-provider-decisions.md#r2--lr-02-source-revised-the-authenticated-v1models-entry-is-authoritative-per-model-metadata)
authorizes these recorded fields as static evidence. Nothing is fetched at
runtime to populate the table.

The earlier model-doc review found unauthored per-model pages (404) at
`https://docs.lunaroute.com/models/<id>.md`; R2 replaces that source requirement.
R3 fixes the release set to the three headings below. The upstream weights name
`deepseek-4.1-flash-1017` and revoked `claude-*-lr*` aliases are not executable
release ids; identity evidence is in [model-identities.md](model-identities.md).

Field mapping:

- `context_window` → `context_tokens`; `max_output_tokens` → `output_tokens`.
  The snapshot's `max_input_tokens` and `max_completion_tokens` agree.
- `capabilities.reasoning` → independent reasoning support;
  `capabilities.json_schema` → `request_settings.output_schema`.
- Temperature support is not published and stays unknown.
- `client_compat.pi.supportsReasoningEffort` and `thinkingLevelMap` establish
  **pi only** effort facts. Non-null identity mappings are listed; Arbitron's
  `none` is pi's `off`, accepted for glm-5.3 because its mapping is `off: none`.
  Remapping a graded requested level would violate the exact-emission rule:
  GLM's medium/minimal/xhigh mappings do not authorize those explicit levels,
  nor does glm-5.3-flash's `off: low` authorize `none`. DeepSeek's null
  minimal/off mappings do not authorize them. This does not
  infer effort support for chat, Claude Code, OpenCode or Codex.
- All entries publish `openai_chat`, `anthropic_messages`, `openai_responses`,
  `tools` and `reasoning` as true. Protocol/tool metadata is not evidence of a
  completed Arbitron backend task. P2-T8/P3-T5/P4-T1 own scoped completion facts;
  static metadata alone leaves task/backend support unknown. The independent
  P2-T8 completed-chat observation below authorizes only glm-5.3 chat.

## glm-5.3

Source: snapshot `models[id=glm-5.3]`, Models API above, 2026-11-11.

| Fact | Published value |
| --- | --- |
| context_tokens | 524288 |
| output_tokens | 131072 |
| reasoning | supported |
| efforts.chat | unknown |
| efforts.claude-code | unknown |
| efforts.opencode | unknown |
| efforts.codex | unknown |
| efforts.pi | [none, low, high, max] |
| request_settings.temperature | unknown |
| request_settings.output_schema | supported |
| tasks.chat | supported; observations/lr04-discovery-chat.json |
| tasks.agent / backends | unknown; no completed agent observation |

## glm-5.3-flash

Source: snapshot `models[id=glm-5.3-flash]`, Models API above, 2026-11-11.

| Fact | Published value |
| --- | --- |
| context_tokens | 524288 |
| output_tokens | 163840 |
| reasoning | supported |
| efforts.chat | unknown |
| efforts.claude-code | unknown |
| efforts.opencode | unknown |
| efforts.codex | unknown |
| efforts.pi | [low, high, max] |
| request_settings.temperature | unknown |
| request_settings.output_schema | supported |
| tasks / backends | unknown; completion population owned separately |

## deepseek-4.1-flash

Source: snapshot `models[id=deepseek-4.1-flash]`, Models API above, 2026-11-11.

| Fact | Published value |
| --- | --- |
| context_tokens | 1048576 |
| output_tokens | 262144 |
| reasoning | supported |
| efforts.chat | unknown |
| efforts.claude-code | unknown |
| efforts.opencode | unknown |
| efforts.codex | unknown |
| efforts.pi | [low, medium, high, xhigh, max] |
| request_settings.temperature | unknown |
| request_settings.output_schema | supported |
| tasks / backends | unknown; completion population owned separately |

## Scoped chat completion (P2-T8)

The operator's [2026-11-11 discovery/chat observation](observations/lr04-discovery-chat.json)
records a completed `glm-5.3` chat with matching reported model, terminal content,
selected Bearer auth and correlated `/v1/chat/completions` inference. Only that
model's `tasks.chat` is populated as supported, citing the observation directly.
This proves no new static limit, effort mapping, agent task or backend support.
MCP discovery consumes the fact from the deployed default loader. Other models'
chat support stays unknown until their own completed observations are applied.

## LR-02 application and readiness

R2 clears the missing-static-facts escalation. The embedded capability table
now contains these reviewed facts and is consumed by the production loader,
MCP discovery and request preflight. LR-02 remains **decided** pending the
separately owned completion/application checks; no backend is marked ready or
verified from this static population. Unknown future facts must still not be
replaced with defaults, nor reasoning inferred from effort levels.
