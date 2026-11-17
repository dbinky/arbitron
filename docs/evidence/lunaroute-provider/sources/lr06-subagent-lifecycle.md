# LR-06 OpenCode synchronous subagent lifecycle validation (partial)

Access date: 2026-11-11. Sources: anomalyco/opencode tag `v1.19.5`.
These are source/schema facts, not an installed-binary completion observation.
LR-06 remains decided; no task/backend support is established here.

## Versioned upstream sources

| Source title / URL | SHA-256 of retrieved source |
| --- | --- |
| Task tool lifecycle — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/tool/task.ts | `db09fa5868ad3ecfdd83aa2bb7243f85e9e2cd13d0b19fd6f307f7a337b2b36e` |
| Session export and optional sanitization — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/cli/cmd/export.ts | `ce20051b0ee3241459eb5566ac6ea83fbc6d701b13db866b8c9e7f5acc1bd328` |
| JSON run-event session correlation — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/cli/cmd/run.ts | `e040e90d4c81cdd59b9568ee90a53c0209cd9ddf1b8b207d1a8685b2e26d57d0` |
| Request preparation and session headers — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/llm/request.ts | `fb03673ee5ddfb0658ec5f1b9f50ac0f0355446af0de6a6796c20df367598687` |
| LLM request consumer — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/session/llm.ts | `6bc3c459b3004fa5bf3cd4d6465d16bacb194285694be0fa5fc3fa12eba28f4e` |
| Built-in general subagent — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/agent/agent.ts | `e781c571d584fa996200e320f14c2735e32968256b8db194391e7c06aed7433e` |

`TaskTool` creates a child with `parentID` equal to the invoking session. Its
metadata records `parentSessionId`, `sessionId`, and `model.{providerID,modelID}`.
Absent an agent-specific override, that model comes from the parent assistant.
The synchronous path waits for the background job; failure/cancellation does
not yield successful completion. Promotion/background mode is distinguishable
through `metadata.background`. The task rejects a returned assistant error or
an error tool part in that returned message. The `general` agent is a built-in
subagent, although inherited configuration can change its effective selection.

`export <sessionID>` serializes `{info, messages}` from the actual session store.
`--sanitize` deliberately replaces tool-state metadata, so it cannot supply this
lifecycle. Unsanitized exports are read only into bounded memory, never logged
or committed. The run command's JSON emitter stamps its root `sessionID`; the
existing production adapter correlates that session and message IDs with its
parent export before accepting execution evidence.

## Implemented seam and scope

`liveFactoryWithClient` now composes `openCodeSubagentsRunner` around the existing
control collector and real runner. The OpenCode experiment mission explicitly
permits `Task` and asks for a synchronous general subagent to verify the files.
At the production adapter's parent-export seam the collector requires a
completed task with selected provider/model, safe distinct parent/child/tool
identities and a bounded current-experiment lifecycle. It then queries each
child using the **same emitted binary, env and cwd**, retaining `--pure` and
removing prompt stdin. Both exports have bounded output and share a bounded
query context. Nothing writes a shared home/config/auth file.

The child must belong to the parent, contain ordered selected-model assistant
turns referring to its user messages, complete within the task interval and end
successfully. Tool-call intermediate turns are allowed; incomplete, erroneous,
truncated, post-terminal, stale, background, substituted and redacted lifecycles
are rejected. A missing task also fails. The only returned projection contains
safe child/tool/message identities. Raw prompts, responses, tool inputs/outputs
and error/config content never enter that projection or errors.

```
go test ./test/lunaroute -run TestAgentEvidenceOpenCodeSubagent -count=1
```

Factory tests use protocol executables and **synthetic** source-grounded exports
alongside real subprocess/TLS inference and command-created workspace files.
They prove source consumption and refusal at the mission sink, not actual
installed-helper behavior. The lifecycle-only modes retain a sole gateway request marked `main`; they do
not relabel it as helper traffic or publish a completed-agent artifact. The
separate two-request correlation mode below proves the additional join.

## Captured session correlation slice

The versioned request preparer emits `x-opencode-session-id` on all providers
and `x-opencode-parent-session-id` when a parent is present, including the
openai-compatible branch. `llm.ts` passes the prepared headers to inference.
Model/plugin header overrides come later in preparation; emitted configuration
alone therefore cannot prove the actual header or destination.

The capture proxy projects only safe, bounded session identities from the
actual requests, rejecting repeated or unsafe identity headers before upstream
I/O. `liveCapturedFactoryWithClient` constructs the existing production Factory
with the control/lifecycle collectors, real runner and this actual capture.
At the parent-export seam it queries the same executable with `--version` under
the same emitted env/cwd and bounds. The join requires the source-reviewed
1.19.5 version, a completed validated child export, observed parent traffic,
exact child/parent header identities, completed selected-model responses and
request intervals inside the synchronous task. Millisecond export timestamps
allow only the remainder of their recorded final millisecond, not a broad grace
period. Only matching request ids become the subagent operation. Overlapping
parent/main traffic is never relabelled. This version check belongs to the
experiment's source provenance, not the production adapter's runtime policy.

The Factory protocol fixtures now include a separate two-request mode that
feeds these real TLS records and actual parent/child exports to the final
private JSON sink. Missing helper traffic, foreign parents, changed versions,
unsafe/repeated headers, stale intervals and model substitution prevent
publication despite intact command-created workspace proofs. Those exports
remain synthetic; no installed binary contract, support fact or verified
register status is established.

Still required: observe this correlation on the installed binary, force/observe
the remaining applicable OpenCode helper categories, collect Claude
controls/helpers, then implement and run the complete installed-binary entry
points. Exported model fields describe
harness selection, **not** a reported gateway model or destination. Only the
actual P2-T9 exchanges can establish those routing facts. No register status,
supported fact or release readiness is inferred from this slice.
