# LR-06 OpenCode first-conversation title correlation (partial)

Access date: 2026-11-11. Sources: anomalyco/opencode tag `v1.19.5`.
Source/schema facts and offline tooling only; LR-06 remains decided.

## Versioned sources

| Source title / URL | SHA-256 of retrieved bytes |
| --- | --- |
| First-user title lifecycle — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/prompt.ts | `f0c5bc64c0f0e966693d4a57f7ede1e9d6e188b396152f04b55303dc75b9b768` |
| System/message preparation and session headers — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/llm/request.ts | `fb03673ee5ddfb0658ec5f1b9f50ac0f0355446af0de6a6796c20df367598687` |
| Prepared request consumer — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/llm.ts | `6bc3c459b3004fa5bf3cd4d6465d16bacb194285694be0fa5fc3fa12eba28f4e` |
| Default title agent prompt — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/agent/prompt/title.txt | `e7a6848eba328f28c7e870874cf0591e4edbaf90d7602ad8fdfe90601c6e656f` |

The last source is preserved verbatim as `lr06-title-prompt.txt` for the wire
fixture. Built-in title-agent registration and unsanitized export schema are
also sourced in `lr06-subagent-lifecycle.md`.

`ensureTitle` runs on the first prompt-loop iteration in a scoped background
fiber. It requires a root session with a default title and exactly one real
user message. It selects the title agent override or `getSmallModel`, falling
back to the selected main model. It calls `llm.stream` with `small: true`, no
tools, and a leading user message containing exactly
`Generate a title for this conversation:\n`, followed by the first conversation.
Request preparation prepends the agent's system prompt and emits the session
header. This is a real inference request, not a saved assistant transcript.

The result strips `<think>…</think>` blocks, selects the first nonempty trimmed
line, and truncates over 100 JavaScript UTF-16 units to 97 units plus `...`.
Only a nonempty result reaches `sessions.setTitle`; export exposes it through
`info.title`. A changed title alone does not prove its request, auth or model.

## Implemented producer-to-sink seam

The actual capture proxy recognizes the fresh experiment's source-defined title
wire signature: default-title system prefix, exact title request prefix and the
verbatim mission user text (string or one text block). No caller label supplies
the category. After the shared complete-response parser succeeds, bounded
JSON/SSE content is normalized for **private in-memory comparison only**.
Conversation, system prompt, generated title and reasoning are never serialized;
comparison material is discarded when capture closes.

At the real adapter-requested export seam, the captured Factory's runner joins
exactly one such exchange to the exported root-session title, the actual
session header, selected request/reported models, Bearer proof, current-run
interval and same-emitted-env `1.19.5` executable version. The existing subagent
collector must also succeed. Only that observed request becomes a `title`
operation with the versioned source reference. Other simultaneous parent
traffic stays `main`. Missing/duplicate/foreign/drifting/empty/malformed or
secret-bearing title evidence refuses the mission despite command-created
workspace proofs. A title still in flight or not yet reflected in the export
cannot pass; no default title or inferred model repairs it.

`TestAgentEvidenceOpenCodeTitleFactoryPublication` drives the real Factory,
runner, CONNECT/TLS proxy, parent/child exports and final private observation
sink with source-grounded protocol executables. Both JSON and streaming title
responses are covered. These are **synthetic CLI fixtures**, not genuine
installed-helper observations, capability support or register verification.

Still required: installed-binary collection, applicable compaction/other helper
collectors, Claude controls/helpers, and complete operator-run entry points.
This slice does not close P3-T5 or release readiness.
