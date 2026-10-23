# OpenCode exact-model source contract

Retrieved over HTTPS on 2026-10-16; installed binary reports `1.19.2`.
`opencode export --help` confirms `export [sessionID]` exports JSON. No harness
was installed/updated and no authenticated model call was made for this capture.
These sources establish the parser/launcher contract, not confirmed usability.

Versioned primary sources (repository `anomalyco/opencode`):

| File under `v1.19.2` | SHA-256 of complete retrieved source |
| --- | --- |
| [packages/opencode/src/cli/cmd/run.ts](https://raw.githubusercontent.com/anomalyco/opencode/v1.19.2/packages/opencode/src/cli/cmd/run.ts) | `e040e90d4c81cdd59b9568ee90a53c0209cd9ddf1b8b207d1a8685b2e26d57d0` |
| [packages/opencode/src/cli/cmd/export.ts](https://raw.githubusercontent.com/anomalyco/opencode/v1.19.2/packages/opencode/src/cli/cmd/export.ts) | `ce20051b0ee3241459eb5566ac6ea83fbc6d701b13db866b8c9e7f5acc1bd328` |
| [packages/schema/src/v1/session.ts](https://raw.githubusercontent.com/anomalyco/opencode/v1.19.2/packages/schema/src/v1/session.ts) | `8516f7405c91636e7a78ce9da099bf5bf307682a4320c9ad6d89888d80fac4fb` |

The complete `v1.17.7` export.ts was also retrieved and has the identical hash.
The existing adapter version guard remains unchanged; incompatible/missing
run/export fields fail closed rather than inferring success from that guard.

## Captured load-bearing source excerpts

`run.ts:668–677` emits the specific session with each JSON event:

```typescript
function emit(type: string, data: Record<string, unknown>) {
  if (args.format === "json") {
    process.stdout.write(
      JSON.stringify({
        type,
        timestamp: Date.now(),
        sessionID,
        ...data,
      }) + EOL,
```

`run.ts:710–744` filters `part.sessionID` to the current session and emits
`step_start`, `step_finish`, and completed `text` parts. `session.ts:81–85`:

```typescript
const partBase = {
  id: PartID,
  sessionID: SessionID,
  messageID: MessageID,
}
```

`export.ts:284–290` reads exactly the requested session and writes its messages:

```typescript
const sessionInfo = yield* svc.get(sessionID!)
const messages = yield* svc.messages({ sessionID: sessionInfo.id })

const exportData = { info: sessionInfo, messages }

process.stdout.write(JSON.stringify(args.sanitize ? sanitize(exportData) : exportData, null, 2))
process.stdout.write(EOL)
```

`session.ts:453–464` supplies actual assistant identity (not the user's model request):

```typescript
export const Assistant = Schema.Struct({
  ...messageBase,
  role: Schema.Literal("assistant"),
  time: Schema.Struct({
    created: NonNegativeInt,
    completed: Schema.optional(NonNegativeInt),
  }),
  error: Schema.optional(AssistantErrorSchema),
  parentID: MessageID,
  modelID: Model.ID,
  providerID: Provider.ID,
```

`messageBase` includes `id` and `sessionID`; `WithParts` is `{info, parts}`.
Unit/root fixtures reduce these schemas to the consumed fields. They are
explicitly synthetic, not a recording of a successful provider execution.

## Application

Pinned single-shot execution exports only its own session with the same cwd,
binary, credential and inline config environment. A 30-second/1MiB bound applies
to export. Every observed run message must match a completed, non-error
assistant message in that session, and every exported assistant model/provider
must match the requested path. Missing/wrong session, turn, identity, malformed
or truncated output cannot accept the run's output. Only the actual routing
provider prefix is removed; native model slashes survive. Legacy unpinned
execution retains its previous behavior. Discovery smoke isolation, tool/plugin
restrictions, router and durable confirmation are separate pending P3T4–P3T7
work, not certified by this parser or the adapter's normal agent policy.
