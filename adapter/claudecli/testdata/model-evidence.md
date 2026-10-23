# Claude non-streaming result evidence

Source inspected 2026-10-16: Anthropic's published
[`@anthropic-ai/claude-agent-sdk@0.3.301`](https://registry.npmjs.org/@anthropic-ai/claude-agent-sdk/-/claude-agent-sdk-0.3.301.tgz),
`package/sdk.d.ts`, `SDKResultSuccess` (lines 5857–5940).
SHA-256 of that declaration file:
`26f9a0c91ad64e2020f0d3734932302e3cc09685bddf2b2f9ef60839ce278773`.
The [official SDK reference](https://platform.claude.com/docs/en/agent-sdk/typescript)
also describes the result's per-model usage map. Installed CLI version checked
with `claude --version`: `2.1.301 (Claude Code)`. No paid model call was made.

Relevant declaration excerpt:

```typescript
export declare type SDKResultSuccess = {
    type: 'result';
    subtype: 'success';
    is_error: boolean;
    result: string;
    modelUsage: Record<string, ModelUsage>;
    // ...other fields...
};
```

The declaration documents `modelUsage` as per-model totals for query-pipeline
calls, including main loop, subagents, sidechains and compaction. Totals may be
cumulative across turns/resumed sessions. This slice follows owner decision D3:
a successful result must contain the requested model key; additional helper
models are tolerated. It does not infer per-turn billing guarantees from those
cumulative totals or introduce a new alias/deployment mapping.

The existing launcher uses one prompt on stdin with `--print --verbose
--output-format stream-json --model X`, without `--fallback-model`. The same
existing parser/matcher used by streaming calls now checks non-streaming
ChatAdapter, AgentAdapter, StreamAgentAdapter.Chat and the deprecated Provider.
The streaming ChatAdapter and StreamAgentAdapter lanes take the same
requirement from the pinned binding as well as the request flag (D14). A required empty model or a request inconsistent with its pinned binding fails
before launch. Missing/wrong usage or multiple terminal results cannot publish
accepted output. Legacy unpinned calls retain their existing behavior, including
calls whose output lacks modelUsage. Context-window matching ignores only a
single terminal `[1m]`, the suffix already recognized by the fixed-version
resolver. Arbitrary bracket text, incomplete/repeated suffixes and trailing
characters are not evidenced model equivalences and cannot satisfy a pin.

Representative schema-based fixture (not a live execution recording):

```json
{"type":"result","subtype":"success","is_error":false,"result":"verified output","session_id":"proof-session","modelUsage":{"claude-sonnet-4-6":{},"claude-haiku-4-5":{}}}
```

Tests drive real initDomain, migrated SQLite profiles/connections and immutable
run pins through the existing factory to durable run/node output. Only process
I/O is replaced. Chat uses the subscription launch; agent uses a persisted
connection's endpoint/key and profile tool scope. The process fixture checks
model, format, no fallback flag, prompt, instance cwd, tools and agent credentials.
This is execution enforcement, not tool-disabled discovery, router construction,
a successful live smoke check or durable confirmation reuse.
