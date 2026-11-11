# LR-06 OpenCode first automatic compaction correlation (partial)

Access date: 2026-11-11. Sources: anomalyco/opencode tag `v1.19.5`.
Source/schema facts and offline tooling only; LR-06 remains decided.

## Versioned sources

| Source title / URL | SHA-256 of retrieved bytes |
| --- | --- |
| Compaction trigger, parent/summary lifecycle and model selection — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/compaction.ts | `8d478570a7e4ad32b746030d4f86a1c673949b1e2259bd716b3885d99283289a` |
| Prompt loop and automatic overflow dispatch — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/prompt.ts | `f0c5bc64c0f0e966693d4a57f7ede1e9d6e188b396152f04b55303dc75b9b768` |
| Anchored summary prompt builder — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/core/src/session/compaction.ts | `35bc2da1578b6bb80a3c39c1d6c51f234f34ce6586dd4f9d230e2a31b257138d` |
| Built-in compaction agent prompt — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/agent/prompt/compaction.txt | `552db0de0af1873a8acd4a631e2548345d4c43192de1d9be69c8feab4b41f80c` |
| Built-in compaction agent registration — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/agent/agent.ts | `e781c571d584fa996200e320f14c2735e32968256b8db194391e7c06aed7433e` |
| System/message preparation and session headers — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/llm/request.ts | `fb03673ee5ddfb0658ec5f1b9f50ac0f0355446af0de6a6796c20df367598687` |

`lr06-compaction-prompt.txt` preserves the built-in prompt verbatim.
`lr06-compaction-template.txt` preserves the `SUMMARY_TEMPLATE` string value
from the core prompt builder (not the surrounding TypeScript).
Unsanitized export provenance is also recorded in `lr06-subagent-lifecycle.md`.

The prompt loop dispatches pending compaction parts to `compaction.process` and
creates automatic compaction when the last finished non-summary turn exceeds
the registered model's overflow budget. `create` writes a new user message with
the selected provider/model and a `compaction` part with `auto`. `process`
selects the compaction agent override or that user message's model, selects
history, and writes an assistant with `parentID` pointing to that user message,
`mode`/`agent: compaction`, `summary: true`, actual provider/model and timestamps.
The processor executes a real inference request with no tools. Failed/overflow
summaries carry an error/unsuccessful finish, not successful completion.

For a first summary, `buildPrompt` wraps serialized conversation in
`Here is the conversation so far:\n\n<conversation>\n…\n</conversation>`,
then appends the anchored-summary instruction and exact template. Serialization
includes the initiating user text as `[User]: …`. Request preparation prepends
the built-in agent system prompt and emits the actual session header. Prior
summaries and plugin-replaced prompts have different shapes; this slice does
not classify those as covered.

## Implemented producer-to-sink seam

The actual capture proxy recognizes the first-summary wire shape, including
the exact experiment mission in history, anchored instruction/template, system
prefix and absence of tools. After complete selected-model JSON/SSE validation,
it retains bounded response text privately, indexed by the actual request id.
No raw prompt, transcript or summary is an observation field. Close discards
this comparison material after in-flight requests drain.

The captured experiment Factory consumes the real adapter-requested parent
export using its same executable/env/cwd. It requires one current automatic
compaction user/part and ordered, successful selected-model summary with exact
parent/part identities. The actual root-session request, response text,
exported text, millisecond lifecycle interval and same-env executable version
must agree before the exchange becomes `compaction`. Main, child and title
requests cannot substitute for it. Missing/duplicate/malformed/foreign/stale,
secret-bearing, redacted or incomplete evidence refuses mission/publication,
even if command-created workspace files and other helper exchanges succeeded.

`TestAgentEvidenceOpenCodeCompactionFactoryPublication` exercises the real
Factory, runner, CONNECT/TLS proxy, same-env parent/child exports, workspace
files and final exclusive private JSON sink. Protocol executables synthesize
the source-backed lifecycle and requests; these are not installed-helper
observations, supported capability facts or contract verification.

Still required under P3-T5: a source-backed experiment driver that forces a
suitable context through the installed binary's actual automatic-compaction
threshold without inventing model limits, applicable remaining helper
collectors, Claude controls/helpers and complete live entry points. Merely
asking the model to summarize, injecting caller category labels or observing a
changed export does not force/prove this operation. Operator live observations
remain pending under R6; this slice does not close readiness.
