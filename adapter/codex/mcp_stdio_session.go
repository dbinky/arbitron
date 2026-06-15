package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// ---------------------------------------------------------------------------
// CODEX MCP-SERVER PROTOCOL VOCABULARY — VERIFIED against codex-cli 0.139.0.
//
// Every tool name, argument key, notification method, and event-type
// discriminator below was verified by driving a live `codex mcp-server` 0.139.0
// (initialize + tools/list + two real turns) on 2026-06-15. The earlier // SPIKE
// guesses (sandbox_mode / approval_policy / turn.completed / a server-advertised
// elicitation capability) were ALL wrong and are corrected here.
//
// POSTURE (Option A — workspace-confined, no approval loop): the interview turn
// runs with sandbox=workspace-write (codex confines every write to its cwd — the
// isolated ./work worktree) and approval-policy=never, so the agent writes its
// spec/design deliverables into the disposable worktree WITHOUT a per-mutation
// approval round-trip. codex's OWN sandbox is the containment. There is no MCP
// elicitation loop on this path: codex 0.139 delivers approval payloads as
// non-standard top-level params that mcp-go 0.53's typed ElicitationParams
// discards, so a read-only+approval design could never see what codex wanted to
// do. workspace-write sidesteps that entirely.
// ---------------------------------------------------------------------------

const (
	// codexStartToolName starts a NEW codex conversation; codexReplyToolName
	// continues an existing one by conversation/thread id (tools/list, 0.139).
	codexStartToolName = "codex"
	codexReplyToolName = "codex-reply"

	// codexEventNotificationMethod carries codex's streaming turn events. The
	// payload is params.msg.{type,...} with params._meta.threadId alongside — the
	// type discriminator is NESTED under "msg", not at the params top level.
	codexEventNotificationMethod = "codex/event"

	// Start-tool (`codex`) argument keys.
	codexArgModel          = "model"
	codexArgPrompt         = "prompt"
	codexArgCwd            = "cwd"
	codexArgSandbox        = "sandbox"
	codexArgApprovalPolicy = "approval-policy"

	// Reply-tool (`codex-reply`) argument key carrying the prior conversation id.
	codexArgConversationID = "conversationId"

	// Posture values: workspace-write confines writes to the cwd worktree; never
	// means codex raises no approval request (no elicitation loop on this path).
	codexSandboxWorkspaceWrite = "workspace-write"
	codexApprovalPolicyNever   = "never"

	// codexServerName is the serverInfo.name a genuine codex mcp-server reports at
	// initialize. The posture self-check confirms we negotiated with codex (not an
	// arbitrary MCP peer that would silently ignore our sandbox arg).
	codexServerName = "codex-mcp-server"

	// codex/event msg.type discriminators (verified 0.139):
	//   agent_message → the final assistant text of a message (msg.message)
	//   task_complete → terminal turn success (thread id in _meta.threadId)
	//   token_count   → best-effort usage (msg.info.total_token_usage)
	//   error / stream_error → terminal turn failure
	// agent_message_content_delta streams partial text; we surface ONLY the final
	// agent_message, preserving turnText's whole-message-increment contract (so a
	// reader never double-counts deltas + the final message).
	codexMsgAgentMessage = "agent_message"
	codexMsgTaskComplete = "task_complete"
	codexMsgTokenCount   = "token_count"
	codexMsgError        = "error"
	codexMsgStreamError  = "stream_error"
)

// codexClientName / codexClientVersion identify arbitron to mcp-server during
// the MCP initialize handshake (informational only).
const (
	codexClientName    = "arbitron-codex"
	codexClientVersion = "1"
)

// mcpStdioSession is the production mcpSession: it drives `codex mcp-server` over
// stdio via the mcp-go client. It is spawn-per-turn — each Start launches a fresh
// subprocess, runs one turn, and the returned stream reaps the child when its
// events are exhausted or its context is cancelled.
//
// SECURITY / fail-closed: it launches under codexEnv() (NO OPENAI_API_KEY,
// HOME/CODEX_HOME preserved) and gates on the shared memoized version probe
// (ensureCompatible) before any conversation — a missing/incompatible binary, or
// an empty WorkDir, fails closed with zero events, exactly like the exec path.
type mcpStdioSession struct {
	core *core
}

var _ mcpSession = (*mcpStdioSession)(nil)

// newMCPStdioSession builds a session bound to the shared core (resolved binary,
// memoized probe, timeout). It is the single construction seam for the real
// transport; the provider (Phase 2) injects the core.
func newMCPStdioSession(c *core) *mcpStdioSession {
	return &mcpStdioSession{core: c}
}

// Start launches `codex mcp-server`, runs one turn (new conversation when
// cfg.ResumeID == "", a codex-reply resume otherwise), and returns a turnStream
// the caller folds. The prompt + posture ride the protocol body (codexEnv argv
// carries ONLY the bare `mcp-server` subcommand — never the prompt; SECURITY). A
// failed WorkDir guard, probe, launch, or handshake yields a typed error and NO
// stream.
func (s *mcpStdioSession) Start(ctx context.Context, cfg turnConfig) (turnStream, error) {
	// WorkDir guard FIRST: an empty cwd would inherit the engine launch directory
	// (arbitron.db / config.yaml). Same invariant the exec path enforces — fail
	// closed before any subprocess (not even the probe).
	if cfg.WorkDir == "" {
		return nil, ErrWorkDirRequired
	}

	// Version probe (memoized, shared with the exec path): a missing/incompatible
	// binary fails closed here, before any conversation launches.
	if err := s.core.ensureCompatible(ctx); err != nil {
		return nil, err
	}

	// Launch under the subscription env: NO OPENAI_API_KEY, HOME/CODEX_HOME
	// preserved. The ONLY argv is the bare `mcp-server` subcommand — the prompt and
	// every posture knob ride the protocol (SECURITY).
	stdio := transport.NewStdio(s.core.binPath, codexEnv(), "mcp-server")
	cli := mcpclient.NewClient(stdio)

	// events buffers mapped turn events; the notification handler feeds it, the
	// returned stream drains it. Buffered so a normal burst of tokens does not stall
	// the transport's reader goroutine; when the buffer DOES fill, sendTurnEvent
	// applies real backpressure (blocks the producer) rather than discarding
	// transcript frames, and only gives up (with a slog.Warn) if the turn ctx is
	// done — i.e. the consumer has actually torn down.
	events := make(chan turnEvent, 64)
	// A per-turn mapper folds the codex/event stream, carrying the running token
	// usage from token_count events onto the terminal task_complete turnDone.
	// mcp-go invokes OnNotification serially from one reader goroutine, so the
	// mapper's state needs no lock.
	mapper := &eventMapper{}
	cli.OnNotification(func(n mcp.JSONRPCNotification) {
		if ev, ok := mapper.mapNotification(n); ok {
			sendTurnEvent(ctx, events, ev, slog.Default())
		}
	})

	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("codex mcp-server: start: %w", err)
	}
	initRes, err := cli.Initialize(ctx, initializeRequest())
	if err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("codex mcp-server: initialize: %w", err)
	}

	// Fail-closed posture self-check: confirm the peer is a genuine codex
	// mcp-server. codex (and only codex) honours the workspace-write `sandbox`
	// arg that confines writes to the worktree; an arbitrary MCP peer would ignore
	// it and run unconfined. Abort here rather than run an unconfined turn.
	if err := verifyInitializePosture(initRes); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("codex mcp-server: %w", err)
	}

	// term carries the turn's terminal error; the goroutine sets it BEFORE closing
	// events, so the stream reads it under the channel-close happens-before edge
	// (no data race). A clean turn leaves it nil → the stream reports errStreamDone.
	term := &terminalError{}

	// Fire the start/reply tool in the background: it returns only when the turn
	// completes, while the streamed events arrive via OnNotification. Its terminal
	// outcome closes the stream so the caller stops folding.
	go func() {
		_, err := cli.CallTool(ctx, startToolRequest(cfg))
		// A real turn fault (not a ctx cancel — that surfaces via Next's ctx) becomes
		// the stream's terminal error so the turn never ends in a silent clean done.
		if err != nil && ctx.Err() == nil {
			term.set(fmt.Errorf("codex mcp-server: turn: %w", err))
		}
		// Reap the child + close stdio at end of turn (spawn-per-turn), THEN close
		// the event channel so the stream drains to its terminal signal.
		_ = cli.Close()
		close(events)
	}()

	return newEventStream(events, term), nil
}

// sendTurnEvent delivers one mapped turn event to the buffered events channel
// with real backpressure: it tries a non-blocking send first (the common,
// free-buffer path), and on a full buffer BLOCKS the producer (the transport
// reader goroutine) until either a slot frees — slowing codex instead of
// discarding transcript content — or the turn ctx is done. It returns true when
// the frame was delivered, false on a true drop.
//
// A drop happens ONLY when ctx is cancelled/timed-out, i.e. the consumer has torn
// down and nothing will ever drain again; blocking past that point would leak the
// reader goroutine. That genuine drop is logged at WARN so the lost frame is
// observable (the transcript that becomes the deliverable and the audit record
// must not lose content silently).
func sendTurnEvent(ctx context.Context, events chan<- turnEvent, ev turnEvent, logger *slog.Logger) bool {
	// Fast path: free buffer space, deliver without blocking the reader goroutine.
	select {
	case events <- ev:
		return true
	default:
	}
	// Buffer full: apply backpressure. Block until a slot frees (consumer drains)
	// or the turn ctx is done (consumer gone — give up rather than leak).
	select {
	case events <- ev:
		return true
	case <-ctx.Done():
		logger.Warn("codex interactive: dropping turn event under backpressure after context done",
			"kind", ev.Kind,
			"err", ctx.Err(),
		)
		return false
	}
}

// initializeRequest is the MCP handshake request: latest protocol version + the
// arbitron client identity. No client elicitation capability is declared — the
// workspace-write posture raises no approval requests, so there is no elicitation
// loop to service.
func initializeRequest() mcp.InitializeRequest {
	return mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: codexClientName, Version: codexClientVersion},
		},
	}
}

// ErrUnverifiedPosture is returned when the Initialize handshake does not identify
// a genuine codex mcp-server peer (serverInfo.name) — so the session is aborted
// rather than run a turn whose workspace-write `sandbox` arg an unknown peer might
// ignore. It is the fail-closed replacement for the original (impossible)
// server-side elicitation-capability check: in MCP, elicitation is a CLIENT
// capability, so a server's InitializeResult never advertises it.
var ErrUnverifiedPosture = errors.New("codex mcp-server: handshake did not identify a codex mcp-server peer")

// verifyInitializePosture is the post-Initialize fail-closed self-check. The
// workspace-write sandbox containment rests on codex honouring the `sandbox` arg;
// only a real codex mcp-server does. So we confirm the negotiated peer reports
// serverInfo.name == codexServerName and fail CLOSED (or on a nil result)
// otherwise — turning "talking to the wrong/none server" into a loud,
// session-aborting error rather than an unconfined turn.
func verifyInitializePosture(res *mcp.InitializeResult) error {
	if res == nil || res.ServerInfo.Name != codexServerName {
		return ErrUnverifiedPosture
	}
	return nil
}

// startToolRequest builds the codex tool call for this turn. A fresh conversation
// (ResumeID == "") calls the `codex` start tool with the model, prompt, cwd, and
// the workspace-write + no-approval posture. A resume (ResumeID != "") calls the
// `codex-reply` tool with the prior conversation id + prompt — model/sandbox carry
// from the original session, so they are not re-sent. The prompt ALWAYS rides the
// protocol body (Arguments), NEVER argv.
//
// Effort is intentionally not emitted on this path: codex 0.139's mcp-server start
// tool has no verified top-level reasoning-effort argument (the exec path still
// applies effort via its `-c` flag). cfg.Effort is therefore ignored here.
func startToolRequest(cfg turnConfig) mcp.CallToolRequest {
	if cfg.ResumeID != "" {
		return mcp.CallToolRequest{
			Params: mcp.CallToolParams{
				Name: codexReplyToolName,
				Arguments: map[string]any{
					codexArgConversationID: cfg.ResumeID,
					codexArgPrompt:         cfg.Prompt,
				},
			},
		}
	}
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{
			Name: codexStartToolName,
			Arguments: map[string]any{
				codexArgModel:          cfg.Model,
				codexArgPrompt:         cfg.Prompt,
				codexArgCwd:            cfg.WorkDir,
				codexArgSandbox:        codexSandboxWorkspaceWrite,
				codexArgApprovalPolicy: codexApprovalPolicyNever,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Event mapping (unit-tested without a subprocess)
// ---------------------------------------------------------------------------

// codexMCPEvent is the tolerant envelope for one codex/event notification. The
// real wire shape is params.{_meta,msg}: the event-type discriminator and all
// payload live under "msg"; the thread id rides "_meta.threadId" (and is echoed
// as msg.thread_id on some events). Only the fields we fold are decoded; every
// other field and any unknown msg.type is ignored (forward-compat).
type codexMCPEvent struct {
	Meta codexEventMeta `json:"_meta"`
	Msg  codexEventMsg  `json:"msg"`
}

type codexEventMeta struct {
	ThreadID string `json:"threadId"`
}

type codexEventMsg struct {
	Type             string          `json:"type"`
	Message          string          `json:"message"`            // agent_message text / error message
	ThreadID         string          `json:"thread_id"`          // alt thread id on some events
	LastAgentMessage string          `json:"last_agent_message"` // task_complete
	Info             *codexTokenInfo `json:"info"`               // token_count
	Error            *codexErrorObj  `json:"error"`              // error / stream_error
}

// codexTokenInfo is the token_count event's usage envelope; total_token_usage
// reuses codexUsage (parse.go) for input_tokens/output_tokens.
type codexTokenInfo struct {
	TotalTokenUsage codexUsage `json:"total_token_usage"`
}

// eventMapper folds the codex/event notification stream for one turn. It carries
// the running token usage (from token_count events) so the terminal task_complete
// turnDone reports the final counts — codex sends usage and completion as separate
// events. mcp-go calls the notification handler serially, so no lock is needed.
type eventMapper struct {
	tokensIn  int
	tokensOut int
}

// mapNotification maps one mcp-go notification to a turnEvent. It returns
// (ev, true) for a codex turn event we surface (an assistant-text message, the
// terminal done, or a failure) and (zero, false) for any notification we do not
// consume — an unrelated method, a usage-only token_count, or an unknown/empty
// codex event — so unrelated MCP chatter never produces a spurious event.
func (m *eventMapper) mapNotification(n mcp.JSONRPCNotification) (turnEvent, bool) {
	if n.Method != codexEventNotificationMethod {
		return turnEvent{}, false
	}
	ev, err := decodeNotificationPayload(n)
	if err != nil {
		// A codex-event notification we cannot decode fails closed as a failure so
		// the turn never silently drops a frame (mirrors decodeLine's posture).
		return turnEvent{Kind: turnFailed, Err: fmt.Errorf("codex mcp-server: %w", err)}, true
	}
	return m.fold(ev)
}

// fold maps one decoded codex event to a turnEvent, threading token usage through
// the mapper. token_count updates the running usage and yields no event;
// agent_message (non-empty) → turnText; task_complete → turnDone (thread id +
// accumulated tokens); error/stream_error → turnFailed; anything else → ignored.
func (m *eventMapper) fold(ev codexMCPEvent) (turnEvent, bool) {
	switch ev.Msg.Type {
	case codexMsgTokenCount:
		if ev.Msg.Info != nil {
			m.tokensIn = ev.Msg.Info.TotalTokenUsage.InputTokens
			m.tokensOut = ev.Msg.Info.TotalTokenUsage.OutputTokens
		}
		return turnEvent{}, false
	case codexMsgAgentMessage:
		if ev.Msg.Message == "" {
			return turnEvent{}, false
		}
		return turnEvent{Kind: turnText, Text: ev.Msg.Message}, true
	case codexMsgTaskComplete:
		return turnEvent{
			Kind:      turnDone,
			SessionID: threadID(ev),
			TokensIn:  m.tokensIn,
			TokensOut: m.tokensOut,
		}, true
	case codexMsgError, codexMsgStreamError:
		return turnEvent{Kind: turnFailed, Err: turnError(ev)}, true
	default:
		return turnEvent{}, false
	}
}

// decodeNotificationPayload re-encodes the notification params and decodes the
// codex event envelope, so the mapper consumes the same JSON the wire delivered
// (independent of how mcp-go staged the fields in AdditionalFields).
//
// `_meta` is an MCP-reserved key that mcp-go parses into Params.Meta (NOT
// AdditionalFields, which holds only the non-reserved fields — here `msg`), so the
// thread id is lifted from Params.Meta separately rather than from the decoded msg
// envelope.
func decodeNotificationPayload(n mcp.JSONRPCNotification) (codexMCPEvent, error) {
	raw, err := json.Marshal(n.Params.AdditionalFields)
	if err != nil {
		return codexMCPEvent{}, err
	}
	var ev codexMCPEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return codexMCPEvent{}, fmt.Errorf("%w: %v", ErrParse, err)
	}
	if tid, ok := n.Params.Meta["threadId"].(string); ok {
		ev.Meta.ThreadID = tid
	}
	return ev, nil
}

// threadID returns the codex thread (conversation) id for a turnDone, preferring
// _meta.threadId and falling back to msg.thread_id.
func threadID(ev codexMCPEvent) string {
	if ev.Meta.ThreadID != "" {
		return ev.Meta.ThreadID
	}
	return ev.Msg.ThreadID
}

// turnError builds the failure cause for an error / stream_error event, preferring
// the nested error message and falling back to the top-level message.
func turnError(ev codexMCPEvent) error {
	msg := ev.Msg.Message
	if ev.Msg.Error != nil && ev.Msg.Error.Message != "" {
		msg = ev.Msg.Error.Message
	}
	if msg == "" {
		msg = "turn failed"
	}
	return fmt.Errorf("codex turn failed: %s", msg)
}

// ---------------------------------------------------------------------------
// eventStream — the turnStream the notification handler feeds
// ---------------------------------------------------------------------------

// terminalError holds the turn's terminal error, written by the Start goroutine
// BEFORE it closes the event channel and read by the stream only AFTER it observes
// the close — so the channel close establishes the happens-before edge and no
// mutex is needed. A nil value means the turn ended cleanly (errStreamDone).
type terminalError struct {
	err error
}

func (t *terminalError) set(err error) { t.err = err }

// get returns the recorded terminal error, or the clean errStreamDone sentinel.
func (t *terminalError) get() error {
	if t != nil && t.err != nil {
		return t.err
	}
	return errStreamDone
}

// eventStream is the turnStream backing the real session: it drains the buffered
// turnEvent channel the OnNotification handler feeds, honours ctx cancellation,
// and reports the terminal errStreamDone (or the turn's recorded error) once the
// channel closes. The thin subprocess glue (Start) wires this to the live client;
// the stream itself is pure and unit-tested with an in-memory channel.
type eventStream struct {
	events <-chan turnEvent
	term   *terminalError
	// done caches the terminal error once the channel has drained, so repeated Next
	// calls keep returning it rather than reading a closed channel as a zero event.
	done error
}

// newEventStream builds a stream over the given event channel. term carries the
// turn's terminal error (set before the channel closes); a nil term, or a nil
// recorded error, reports the clean errStreamDone sentinel at drain time.
func newEventStream(events <-chan turnEvent, term *terminalError) *eventStream {
	return &eventStream{events: events, term: term}
}

// Next returns the next buffered event, the terminal errStreamDone once the
// channel closes (or the turn's recorded error if one occurred), or the context
// error on cancellation/timeout — never a silently dropped frame.
func (s *eventStream) Next(ctx context.Context) (turnEvent, error) {
	if s.done != nil {
		return turnEvent{}, s.done
	}
	select {
	case <-ctx.Done():
		return turnEvent{}, ctx.Err()
	case ev, ok := <-s.events:
		if !ok {
			s.done = s.term.get()
			return turnEvent{}, s.done
		}
		return ev, nil
	}
}
