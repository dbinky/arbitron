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
| Overflow budget and reported-usage threshold — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/overflow.ts | `47dfe71e6f96737518c494744561611fffe955594ceb10220dd7c751c3ec139c` |
| Compaction configuration schema — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/core/src/v1/config/config.ts | `9f7f0a80128153537e2eb4bf5163e45cbf68e3ed03c1efd1c39f39ea4cc18067` |
| Registered model input-limit population — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/provider/provider.ts | `8d31296a58bc7955a1adb8b6d60da53a34ab7540d85644bab212a8ee27b8b69d` |
| Step-finish overflow and summary exclusion — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/processor.ts | `0b31e207beda56bd9a4b9b88c9e42d89a0651dd011773748c8e2938dfc3dcb74` |
| Explicit existing-session continuation — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/cli/cmd/run.ts | `e040e90d4c81cdd59b9568ee90a53c0209cd9ddf1b8b207d1a8685b2e26d57d0` |
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

## Source-backed forcing driver (offline implementation, operator run pending)

`liveCompactingFactoryWithClient` constructs a runner around the real production
Factory launch. It finishes the tool mission first, reads that launch's exact
session id and current successful export, and obtains nonzero reported usage.
The v1.19.5 overflow function uses `tokens.total` when nonzero, otherwise
input + output + cache read + cache write. Missing, negative, fractional,
overflowing or stale usage and pre-existing summaries refuse the experiment.

On a bounded continuation of that exact session, the experiment registers
`limit.input` from R2's committed `max_input_tokens`, requiring the emitted
context/output limits to match the snapshot unchanged. It sets only the
experiment's `compaction.auto=true` and `reserved=input-reportedUsage`.
The resulting usable budget equals the warm-up's actual usage, so the native
prompt loop's `>=` check triggers automatic compaction. The reserve must be
positive. No production metadata is reduced, usage/database record injected,
or prompt-only summary accepted. `reserved` alone would not work: without
`limit.input`, overflow.ts instead uses context minus maximum output.

The same executable/env/cwd must resolve the exact input/reserve/auto controls
in `debug config --pure` before the continuation launches. The version query
is bounded and restricted to the reviewed experiment version. The driver runs
within the mission budget, not inside the adapter's 30-second export budget.
It requires a fresh automatic summary and successful subsequent final turn;
the existing export/request joins still require actual selected-model traffic.
The processor excludes summary turns from its overflow test, but checks normal
turns even when they finish: permanently setting the budget to one token would
cause repeated compaction. Using the observed warm-up count allows a shorter
post-summary context to finish. If it does not, bounded capture/mission limits
and the one-compaction validator refuse publication; no fallback or readiness
claim is made.

`TestLiveOpencodeCompaction` is a real operator entry point for this supporting
experiment. It requires explicit key/model inputs and fresh listing membership,
uses the production capability loader and R2 snapshot, isolates HOME and seeded
user/project config, wraps the installed binary, verifies real workspace/tool
proofs and all four current-run categories, and checks cleanup before writing
`observations/lr06-opencode-compaction.json`. It does not mark LR-06 verified or
populate support. Offline protocol executables validate driver-to-JSON plumbing;
the installed debug-schema test proves accepted control syntax only, not
installed-helper routing. Complete contract/control collection and the full
agent live entry points remain P3-T5 work. Operator observations remain pending
under R6; this slice does not close readiness.
