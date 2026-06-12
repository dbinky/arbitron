# Codex Interactive — Design Phase 1: Transport Seam + `codex mcp-server` Spike

**Slug:** `codex-interactive`
**Phase:** 1 of 3
**Status:** Draft
**Date:** 2026-06-12
**Spec:** `docs/specs/codex-interactive-spec.md`

## 1. Purpose

Establish the **transport boundary** the interactive codex provider depends on,
and **pin the exact `codex mcp-server` protocol** (codex-cli 0.139) the real
implementation speaks. Everything in Phases 2–3 sits on top of this seam, so it
must be defined first, mockable, and verified against the real binary.

This phase introduces **no** behavior change to any running node. It adds a new,
unwired adapter seam plus a focused spike. The composition root is not touched
until Phase 3.

## 2. Why a new seam (not the existing runner)

The current codex adapter drives `codex exec` through
`claudecli.CommandRunner` — `Run(ctx, RunSpec) (<-chan string, func() error, error)`.
That seam is **stdout-lines-out, one direction**: perfect for `codex exec --json`,
useless for MCP, which is **bidirectional JSON-RPC over stdio** (the server sends
streaming notifications *and* server→client approval requests; the client sends
the prompt, replies, and approval responses). The runner seam cannot express the
server→client direction, so a new boundary is required.

The new boundary is deliberately **narrow and codex-shaped** (not a general MCP
abstraction): it exposes only what an interactive interview turn needs.

## 3. The `mcpSession` port

A single Go interface, owned by `adapter/codex`, depended on by the Phase-2
provider. (Names are indicative; final names land with the code.)

```
// mcpSession is one codex mcp-server conversation turn over stdio. It is the
// transport seam the interactive provider depends on; the real impl wraps the
// mcp-go stdio client + `codex mcp-server`, the fake impl replays canned frames.
type mcpSession interface {
    // Start begins a NEW codex conversation (no prior session) OR resumes an
    // existing one when resumeID != "". It sends the prompt and returns a
    // turnStream the caller folds. cfg carries the read-only sandbox + approval
    // posture and the working directory.
    Start(ctx context.Context, cfg turnConfig) (turnStream, error)
}

type turnConfig struct {
    Model     string
    Prompt    string
    WorkDir   string
    ResumeID  string          // codex conversation id; "" = new conversation
    Approver  approvalFunc    // invoked for each server→client approval request
    Effort    string          // optional reasoning-effort, if the surface accepts it
}

// turnStream yields assistant text increments and a terminal outcome. It mirrors
// the existing parsed/foldEvent contract so Phase 2 reuses the fold discipline.
type turnStream interface {
    // Next returns the next event: an assistant-text increment, or the terminal
    // event (carrying the codex conversation id + token counts), or an error.
    Next(ctx context.Context) (turnEvent, error)
}

// approvalFunc is the provider's auto-policy, called by the transport whenever
// codex requests approval (command exec / patch apply). It returns allow/deny.
// Phase 1 defines the SHAPE; Phase 2 supplies the policy. The transport must
// surface every request to it (never auto-allow internally) so the guardrail is
// the provider's, not the transport's.
type approvalFunc func(req approvalRequest) approvalDecision
```

`turnEvent` is a small tagged struct: `{Kind: token|done|failed, Text, SessionID,
TokensIn, TokensOut, Err}`. `approvalRequest` carries the structured payload codex
sends (the command argv and/or the patch's target paths + a reason);
`approvalDecision` is `{Allow bool, Reason string}` (reason recorded to the trace).

**Hexagonal note:** this interface lives in `adapter/codex` (an adapter package),
references no `domain/engine` type, and is consumed only by the Phase-2 provider.
The provider, in turn, depends only on `engine.ChatProvider`. No domain edits.

## 4. Real implementation (`mcpStdioSession`)

Backed by `github.com/mark3labs/mcp-go` (v0.53.0, already a dependency — no new
dep) driving `codex mcp-server` as a stdio child:

- **Launch.** Start `codex mcp-server` with the controlled subscription env from
  the existing `codexEnv()` (PATH-augmented, allowlist-filtered, **NO
  `OPENAI_API_KEY`**, `HOME`/`CODEX_HOME` preserved so `~/.codex/auth.json`
  resolves). The binary path resolves through the existing `resolveBinary`.
- **Compatibility.** Reuse the existing memoized version probe
  (`ensureCompatible`/`probeVersion`) before the first turn so an incompatible or
  missing binary **fails closed** before any conversation starts. The probe seam
  is shared with the exec path, so the two cannot drift.
- **Start/resume.** Call the codex mcp-server conversation tool with the prompt +
  posture config; for resume, pass the prior codex conversation id. (Exact tool
  name + parameter schema pinned by the spike — see §6.)
- **Streaming.** Register an mcp-go notification handler that maps codex progress
  notifications to `turnEvent{Kind: token}`; map the terminal result to
  `turnEvent{Kind: done, SessionID, tokens}`.
- **Approvals.** Register an mcp-go handler for codex's server→client approval
  requests; translate each into an `approvalRequest`, call the injected
  `approvalFunc`, and return the decision over the protocol. **The transport
  itself makes no allow/deny judgement** — it only plumbs the request to the
  provider's policy.
- **Teardown.** Close the stdio client and reap the child at end of turn
  (spawn-per-turn — see Phase 3 / spec). A failed start or a closed transport
  yields zero events and a typed error (no half-open turn).

## 5. Fake implementation (`fakeMCPSession`) — test seam

Mirrors `adapter/codex/fake_runner_test.go`: a hand-driven `mcpSession` that
replays a scripted sequence of `turnEvent`s and, at scripted points, invokes the
caller's `approvalFunc` with canned `approvalRequest`s (an info command, a
`docs/` write, a source-file patch, a git commit) so Phase 2 can assert the
policy's decisions. It records what it was asked (prompt, resumeID, cfg) for
argument assertions. **No live codex, no subprocess, no network.**

This fake is the substrate for ALL Phase-2 provider tests.

## 6. The spike (verify before building Phase 2)

A short, throwaway investigation against real `codex mcp-server` (codex-cli
0.139) to **pin the vocabulary** the real impl encodes. Findings are recorded in
this doc (a "Spike Findings" appendix) — mirroring
`vendor-abstraction-opencode-spike-finding.md`. Questions to answer:

1. **Start a conversation.** The exact tool/method name and parameter schema to
   begin a conversation with: model, prompt, cwd, `sandbox_mode = read-only`, and
   the approval policy that forces mutations through an elicitation.
2. **Resume in a fresh process.** Whether mcp-server can resume a prior
   conversation by id from a *new* process (required by the spawn-per-turn
   lifecycle), and the parameter that carries the id.
3. **Streaming shape.** The notification(s) that carry assistant-text increments
   vs. the terminal result, and where the conversation id + token counts appear.
4. **Approval request shape.** The server→client request codex uses to ask for
   command/patch approval, its payload (argv / target paths / reason), and the
   response schema — so the `approvalRequest`/`approvalDecision` mapping is exact.
5. **Sandbox/approval config surface.** How read-only sandbox + the approval
   policy are passed (tool params vs. `-c` config keys vs. server launch flags).

### Decision gate

- **If mcp-server expresses all five** → the real impl uses `codex mcp-server`.
- **If it cannot** (e.g. no fresh-process resume, or no client-answerable
  approval request) → fall back to **`codex app-server`** (the richer JSON-RPC
  TUI backend) **behind the same `mcpSession` interface**. Only the real impl
  changes; the port, the fake, and the entire Phase-2 provider are unaffected.

The gate's outcome is documented inline; downstream phases consume the seam, not
the binary surface.

## 7. Error handling

- Missing/incompatible binary, failed launch, broken stdio → typed error from
  `Start`, **zero events**, no half-open stream.
- A malformed/undecodable frame → terminal `turnEvent{Kind: failed, Err}` (fail
  closed; never a silently dropped frame), reusing the existing fail-closed
  posture of `decodeLine`.
- Context cancellation / timeout → `Next` returns the context error; the child is
  reaped; no leaked process.

## 8. Testing (strict TDD — all five categories)

Tests target the **real impl's framing logic** against canned mcp-go messages and
the **fake's** own contract. The provider's behavior is Phase 2.

- **Happy path:** a scripted start→token→token→done sequence folds to ordered
  text increments + a terminal event carrying the conversation id.
- **Success:** resume (`ResumeID` set) sends the id on the start call; the
  recorded request shows the prompt via the protocol (never argv) and the
  no-API-key env.
- **Failure:** a `turn.failed`/error frame yields a terminal `failed` event with
  the cause — never a clean `done`.
- **Error:** launch error / incompatible-binary probe / malformed frame → typed
  error, zero or terminal-error events, child reaped.
- **Edge case:** an approval request arrives *before* any token; a notification
  with empty text; a terminal event with absent token counts; context cancelled
  mid-stream → context error, no leak.

## 9. Deliverables

- `adapter/codex/mcp_session.go` — the `mcpSession` port + `turnConfig` /
  `turnStream` / `turnEvent` / `approvalRequest` / `approvalDecision` types.
- `adapter/codex/mcp_stdio_session.go` — the real mcp-go-backed impl (env +
  probe reuse).
- `adapter/codex/fake_mcp_session_test.go` — the test fake.
- Tests for the real impl's framing + the fake's contract.
- Spike Findings appendix (pinned vocabulary + decision-gate outcome).

## 10. Out of scope for Phase 1

- The `engine.ChatProvider` implementation and the approval **policy** (Phase 2).
- Any composition-root wiring / sandbox config plumbing (Phase 3).
- Any change to `codex exec`, the batch `Provider`, claude-cli, or opencode.

## Spike Findings (provisional)

**Status:** NOT YET VERIFIED against a live codex-cli 0.139. This run had no live
codex binary available, so the real `mcpStdioSession` (Task 1.2) was implemented
against the **documented mcp-go API** (verified — see below) and a **provisional
codex mcp-server protocol vocabulary** (assumed). The code structure and every
mcp-go API call are correct and compile; only the protocol string *values* are
provisional. Each provisional constant in `adapter/codex/mcp_stdio_session.go` is
tagged `// SPIKE: verify against codex-cli 0.139` so the real-binary check is a
small, isolated follow-up.

### mcp-go API — VERIFIED (from the v0.53.0 module source)

These are confirmed against `github.com/mark3labs/mcp-go@v0.53.0` and the impl
uses them as written:

- **Launch (lazy):** `transport.NewStdio(command, env, args...)` builds the stdio
  transport; the subprocess is spawned by `Client.Start(ctx)` (so the elicitation
  capability can be declared *before* spawn). We launch
  `transport.NewStdio(binPath, codexEnv(), "mcp-server")` — argv carries ONLY the
  bare subcommand, never the prompt.
- **Client + handler registration:**
  `client.NewClient(transport, client.WithElicitationHandler(h))`. The
  `ElicitationHandler` interface is `Elicit(ctx, mcp.ElicitationRequest)
  (*mcp.ElicitationResult, error)`. mcp-go declares the elicitation capability
  during `Initialize` iff this handler is set, and routes server→client
  `elicitation/create` requests to it via the bidirectional transport request
  handler (`handleIncomingRequest` → `handleElicitationRequestTransport`).
- **Streaming notifications:** `Client.OnNotification(func(mcp.JSONRPCNotification))`
  registers a server→client notification handler.
- **Handshake / turn:** `Client.Start(ctx)`, `Client.Initialize(ctx,
  mcp.InitializeRequest)`, `Client.CallTool(ctx, mcp.CallToolRequest)`,
  `Client.Close()`.

> **Approval channel = elicitation.** Codex's client-answered approval request is
> modelled as MCP **elicitation** (`elicitation/create`), the standard
> server→client "ask the user/client a question" request. mcp-go also exposes
> **sampling** (`sampling/createMessage`) as a server→client request; elicitation
> is the correct fit for an allow/deny approval and is what the impl registers. If
> the live spike shows codex uses sampling (or a bespoke method) instead, only the
> handler registration + the `mapApprovalRequest` payload decode change — the port,
> the policy, and Phase 2 are unaffected.

### codex mcp-server protocol — ASSUMED (must be verified against 0.139)

| Concern | Constant(s) in `mcp_stdio_session.go` | Assumed value | Status |
| --- | --- | --- | --- |
| Start/resume tool name | `codexStartToolName` | `"codex"` | ASSUMED |
| Start-tool arg keys | `codexArgModel/Prompt/Cwd/SandboxMode/ApprovalPolicy/ResumeID/Effort` | `model`/`prompt`/`cwd`/`sandbox_mode`/`approval_policy`/`conversation_id`/`model_reasoning_effort` | ASSUMED |
| Read-only sandbox value | `codexSandboxReadOnly` | `"read-only"` | ASSUMED |
| Approval policy value | `codexApprovalPolicyOnRequest` | `"on-request"` | ASSUMED |
| Streaming notification method | `codexEventNotificationMethod` | `"codex/event"` | ASSUMED |
| Turn-event `type` discriminators | `codexItemAgentMessage/TurnCompleted/TurnFailed/ErrorEvent` | `agent_message`/`turn.completed`/`turn.failed`/`error` | **REUSED from the VERIFIED `codex exec --json` vocabulary** (`parse.go`, codex-cli 0.137.0); mcp-server assumed to carry the same item/usage/error shapes inside its notifications |
| Usage token keys | (via `codexUsage`) | `input_tokens`/`output_tokens` | REUSED (verified for exec) |
| Conversation id key | `conversation_id` (fallback `thread_id`) | — | ASSUMED (fallback reuses the verified exec key) |
| Approval payload keys | `codexApprovalTypeKey/CommandKey/PathsKey/ReasonKey` | `type`(`exec`/`patch`)/`command`/`paths`/`reason` | ASSUMED |
| Approval verdict response | `codexApprovalDecisionKey` | `approved` (in the elicitation result content; allow→`accept`, deny→`decline`) | ASSUMED |

### Decision gate — outcome

**Default surface: `codex mcp-server`** (selected). The mcp-go client + the
elicitation channel can, in principle, express all five spike questions:
(1) a start tool with model/prompt/cwd/sandbox/approval args; (2) fresh-process
resume via a `conversation_id` arg on the start call; (3) streaming via
notifications + a terminal completion frame; (4) a client-answered approval via
`elicitation/create`; (5) sandbox/approval passed as tool args.

**Fallback (behind the SAME `mcpSession` port): `codex app-server`.** Adopt it
only if live verification shows mcp-server *cannot* express either (a)
fresh-process resume-by-id or (b) a client-answerable approval request. Switching
surfaces changes ONLY `mcp_stdio_session.go` — the `mcpSession` interface, the
fake, the approval policy, and all of Phase 2 are unaffected.

### Live-codex follow-up (the small isolated check)

Run `codex mcp-server` once against codex-cli 0.139 and confirm/adjust the
ASSUMED rows above: the exact tool name, arg keys + sandbox/approval values, the
streaming notification method + payload shape, and the elicitation approval
payload + verdict response schema. Only the tagged constants change; the mappers
(`mapNotification`, `mapApprovalRequest`, `decisionToElicitationResult`,
`startToolRequest`) and their tests stay structurally identical.
