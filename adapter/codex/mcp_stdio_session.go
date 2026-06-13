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
// SPIKE-GATED PROTOCOL VOCABULARY
//
// The codex mcp-server tool name, argument keys, notification method/shape, and
// the approval-request (elicitation) payload are NOT yet verified against a live
// codex-cli 0.139. Each value below is provisional and tagged
// `// SPIKE: verify against codex-cli 0.139` so the real-binary check is a small,
// isolated follow-up — the CODE STRUCTURE and mcp-go API usage are correct and
// compile regardless. See the "Spike Findings (provisional)" appendix in
// docs/superpowers/specs/codex-interactive-design-phase-1.md.
//
// The codex *event vocabulary* (agent_message / turn.completed / turn.failed /
// usage.input_tokens|output_tokens) is REUSED from parse.go, which is VERIFIED
// against codex-cli 0.137.0 for the `codex exec --json` stream; mcp-server is
// assumed to carry the same item/usage/error shapes inside its notifications.
// ---------------------------------------------------------------------------

const (
	// codexStartToolName is the mcp-server tool that begins / continues a codex
	// conversation. // SPIKE: verify against codex-cli 0.139
	codexStartToolName = "codex"

	// codexEventNotificationMethod is the JSON-RPC notification method that carries
	// codex's streaming turn events (assistant text, completion, failure).
	// // SPIKE: verify against codex-cli 0.139
	codexEventNotificationMethod = "codex/event"

	// Start-tool argument keys. // SPIKE: verify against codex-cli 0.139
	codexArgModel          = "model"
	codexArgPrompt         = "prompt"
	codexArgCwd            = "cwd"
	codexArgSandboxMode    = "sandbox_mode"
	codexArgApprovalPolicy = "approval_policy"
	codexArgResumeID       = "conversation_id"
	codexArgEffort         = "model_reasoning_effort"

	// codexSandboxReadOnly forces the read-only sandbox posture; codexApprovalPolicyOnRequest
	// routes every mutation through a server→client approval (elicitation).
	// // SPIKE: verify against codex-cli 0.139
	codexSandboxReadOnly         = "read-only"
	codexApprovalPolicyOnRequest = "on-request"

	// Codex turn-event "type" discriminators carried in the notification payload.
	// These mirror parse.go's VERIFIED exec vocabulary; mcp-server is assumed to
	// reuse them. // SPIKE: verify against codex-cli 0.139
	codexItemAgentMessage = "agent_message"
	codexTurnCompleted    = "turn.completed"
	codexTurnFailed       = "turn.failed"
	codexErrorEvent       = "error"

	// Approval-request (elicitation) payload keys + verdict response keys.
	// // SPIKE: verify against codex-cli 0.139
	codexApprovalTypeKey     = "type"
	codexApprovalTypeExec    = "exec"
	codexApprovalTypePatch   = "patch"
	codexApprovalCommandKey  = "command"
	codexApprovalPathsKey    = "paths"
	codexApprovalReasonKey   = "reason"
	codexApprovalDecisionKey = "approved"
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
// cfg.ResumeID == "", resume otherwise), and returns a turnStream the caller
// folds. The prompt + posture ride the protocol body (codexEnv argv carries ONLY
// the bare `mcp-server` subcommand — never the prompt; SECURITY). A failed
// WorkDir guard, probe, launch, or handshake yields a typed error and NO stream.
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

	// Register the server→client approval handler BEFORE Start so the elicitation
	// capability is declared during the initialize handshake.
	approver := &elicitationApprover{approver: cfg.Approver}
	cli := mcpclient.NewClient(stdio, mcpclient.WithElicitationHandler(approver))

	// events buffers mapped turn events; the bidirectional notification handler
	// feeds it, the returned stream drains it. Buffered so a normal burst of tokens
	// does not stall the transport's reader goroutine; when the buffer DOES fill,
	// sendTurnEvent applies real backpressure (blocks the producer) rather than
	// discarding transcript frames, and only gives up (with a slog.Warn) if the
	// turn ctx is done — i.e. the consumer has actually torn down.
	events := make(chan turnEvent, 64)
	cli.OnNotification(func(n mcp.JSONRPCNotification) {
		if ev, ok := mapNotification(n); ok {
			sendTurnEvent(ctx, events, ev, slog.Default())
		}
	})

	if err := cli.Start(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("codex mcp-server: start: %w", err)
	}
	if _, err := cli.Initialize(ctx, initializeRequest()); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("codex mcp-server: initialize: %w", err)
	}

	// term carries the turn's terminal error; the goroutine sets it BEFORE closing
	// events, so the stream reads it under the channel-close happens-before edge
	// (no data race). A clean turn leaves it nil → the stream reports errStreamDone.
	term := &terminalError{}

	// Fire the start tool in the background: it returns only when the turn
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
// must not lose content silently). The approval decisions are logged separately
// (tracingApprover), so a drop here never silently loses a deny verdict.
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

// initializeRequest is the MCP handshake request: latest protocol version, the
// arbitron client identity. The elicitation capability is added by mcp-go when
// the elicitation handler is present (see WithElicitationHandler).
func initializeRequest() mcp.InitializeRequest {
	return mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: codexClientName, Version: codexClientVersion},
		},
	}
}

// startToolRequest builds the codex start/resume tool call. The prompt rides the
// protocol body (Arguments), NEVER argv. A non-empty ResumeID adds the resume
// key; an empty one starts a fresh conversation. A non-empty Effort adds the
// reasoning-effort key; an empty one emits nothing (preserving the exec path's
// "default effort passes nothing" behaviour).
func startToolRequest(cfg turnConfig) mcp.CallToolRequest {
	args := map[string]any{
		codexArgModel:          cfg.Model,
		codexArgPrompt:         cfg.Prompt,
		codexArgCwd:            cfg.WorkDir,
		codexArgSandboxMode:    codexSandboxReadOnly,
		codexArgApprovalPolicy: codexApprovalPolicyOnRequest,
	}
	if cfg.ResumeID != "" {
		args[codexArgResumeID] = cfg.ResumeID
	}
	if cfg.Effort != "" {
		args[codexArgEffort] = cfg.Effort
	}
	return mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: codexStartToolName, Arguments: args},
	}
}

// ---------------------------------------------------------------------------
// Pure mappers (unit-tested without a subprocess)
// ---------------------------------------------------------------------------

// codexMCPEvent is the tolerant envelope for one codex turn-event notification
// payload. It reuses parse.go's exec vocabulary (item/usage/error), assuming
// mcp-server carries the same shapes. Only the fields we fold are decoded; every
// other field and any unknown "type" is ignored (forward-compat).
type codexMCPEvent struct {
	Type           string         `json:"type"`
	ConversationID string         `json:"conversation_id"`
	ThreadID       string         `json:"thread_id"` // alt id key on some surfaces
	Item           *codexItem     `json:"item"`
	Usage          *codexUsage    `json:"usage"`
	Message        string         `json:"message"`
	Error          *codexErrorObj `json:"error"`
}

// mapNotification maps one mcp-go notification to a turnEvent. It returns
// (ev, true) for a codex turn event we surface (an assistant-text token, the
// terminal done, or a failure) and (zero, false) for any notification we do not
// consume — an unrelated method, an unknown/empty-text codex event — so unrelated
// MCP chatter never produces a spurious event.
func mapNotification(n mcp.JSONRPCNotification) (turnEvent, bool) {
	if n.Method != codexEventNotificationMethod {
		return turnEvent{}, false
	}
	ev, err := decodeNotificationPayload(n)
	if err != nil {
		// A codex-event notification we cannot decode fails closed as a failure so
		// the turn never silently drops a frame (mirrors decodeLine's posture).
		return turnEvent{Kind: turnFailed, Err: fmt.Errorf("codex mcp-server: %w", err)}, true
	}
	return foldNotificationEvent(ev)
}

// decodeNotificationPayload re-encodes the notification params and decodes the
// codex event envelope, so the mapper consumes the same JSON the wire delivered
// (independent of how mcp-go staged the fields in AdditionalFields).
func decodeNotificationPayload(n mcp.JSONRPCNotification) (codexMCPEvent, error) {
	raw, err := json.Marshal(n.Params.AdditionalFields)
	if err != nil {
		return codexMCPEvent{}, err
	}
	var ev codexMCPEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return codexMCPEvent{}, fmt.Errorf("%w: %v", ErrParse, err)
	}
	return ev, nil
}

// foldNotificationEvent maps one decoded codex event to a turnEvent. An
// agent_message with non-empty text → turnToken; turn.completed → turnDone with
// the conversation id + best-effort tokens; turn.failed / error → turnFailed; any
// other (or an empty-text message) → (zero, false) ignored.
func foldNotificationEvent(ev codexMCPEvent) (turnEvent, bool) {
	switch ev.Type {
	case codexItemAgentMessage:
		text := agentText(ev)
		if text == "" {
			return turnEvent{}, false
		}
		return turnEvent{Kind: turnToken, Text: text}, true
	case codexTurnCompleted:
		out := turnEvent{Kind: turnDone, SessionID: conversationID(ev)}
		if ev.Usage != nil {
			out.TokensIn = ev.Usage.InputTokens
			out.TokensOut = ev.Usage.OutputTokens
		}
		return out, true
	case codexTurnFailed, codexErrorEvent:
		return turnEvent{Kind: turnFailed, Err: turnError(ev)}, true
	default:
		return turnEvent{}, false
	}
}

// agentText returns the assistant text of an agent_message event, tolerating both
// a nested item.text and a top-level message (whichever the surface uses).
func agentText(ev codexMCPEvent) string {
	if ev.Item != nil && ev.Item.Text != "" {
		return ev.Item.Text
	}
	return ev.Message
}

// conversationID returns the codex conversation id, preferring conversation_id
// and falling back to thread_id.
func conversationID(ev codexMCPEvent) string {
	if ev.ConversationID != "" {
		return ev.ConversationID
	}
	return ev.ThreadID
}

// turnError builds the failure cause for a turn.failed / error event, preferring
// the nested error message and falling back to the top-level message.
func turnError(ev codexMCPEvent) error {
	msg := ev.Message
	if ev.Error != nil && ev.Error.Message != "" {
		msg = ev.Error.Message
	}
	if msg == "" {
		msg = "turn failed"
	}
	return fmt.Errorf("codex turn failed: %s", msg)
}

// errMalformedApproval is returned when a codex approval request carries neither a
// command nor write paths — a request the transport cannot map to an action, so
// it fails closed (the policy is never asked to allow an unspecified mutation).
var errMalformedApproval = errors.New("codex mcp-server: malformed approval request (no command or write paths)")

// codexApprovalPayload is the tolerant envelope for a codex approval (elicitation)
// request's structured payload.
type codexApprovalPayload struct {
	Type    string   `json:"type"`
	Command []string `json:"command"`
	Paths   []string `json:"paths"`
	Reason  string   `json:"reason"`
}

// mapApprovalRequest maps a codex approval elicitation payload to an
// approvalRequest. A request carrying a command → Command (WritePaths nil); one
// carrying paths → WritePaths (Command nil). The reason falls back to the human
// message when codex omits an explicit reason, so the trace is never blank. A
// request with neither action fails closed with errMalformedApproval.
func mapApprovalRequest(p mcp.ElicitationParams) (approvalRequest, error) {
	payload, err := decodeApprovalPayload(p)
	if err != nil {
		return approvalRequest{}, err
	}
	reason := payload.Reason
	if reason == "" {
		reason = p.Message
	}
	switch {
	case len(payload.Command) > 0:
		return approvalRequest{Command: payload.Command, Reason: reason}, nil
	case len(payload.Paths) > 0:
		return approvalRequest{WritePaths: payload.Paths, Reason: reason}, nil
	default:
		return approvalRequest{}, errMalformedApproval
	}
}

// decodeApprovalPayload extracts the codex approval payload from the elicitation
// params' _meta.AdditionalFields, re-encoding through JSON so it decodes exactly
// as the wire delivered it.
func decodeApprovalPayload(p mcp.ElicitationParams) (codexApprovalPayload, error) {
	var fields map[string]any
	if p.Meta != nil {
		fields = p.Meta.AdditionalFields
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return codexApprovalPayload{}, err
	}
	var payload codexApprovalPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return codexApprovalPayload{}, fmt.Errorf("%w: %v", ErrParse, err)
	}
	return payload, nil
}

// decisionToElicitationResult translates the policy's verdict into the protocol
// response: allow → accept, deny → decline. The verdict + reason ride the result
// content so the trace records why codex was (dis)allowed. It NEVER returns nil,
// so codex always receives a well-formed answer (a dropped response would wedge
// the turn).
func decisionToElicitationResult(dec approvalDecision) *mcp.ElicitationResult {
	action := mcp.ElicitationResponseActionDecline
	if dec.Allow {
		action = mcp.ElicitationResponseActionAccept
	}
	return &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action: action,
			Content: map[string]any{
				codexApprovalDecisionKey: dec.Allow,
				codexApprovalReasonKey:   dec.Reason,
			},
		},
	}
}

// elicitationApprover is the mcp-go ElicitationHandler that plumbs codex's
// server→client approval requests to the provider's policy. The transport makes
// NO allow/deny judgement of its own: it maps the request, asks the injected
// approver, and returns the decision. A malformed request or a nil approver fails
// CLOSED (declines) so codex can never proceed with an unvetted mutation.
type elicitationApprover struct {
	approver approvalFunc
}

var _ mcpclient.ElicitationHandler = (*elicitationApprover)(nil)

// Elicit handles one codex approval request. It returns nil error (the decision
// is carried in the result's Action) so a deny is a normal answer, not a transport
// fault.
func (e *elicitationApprover) Elicit(_ context.Context, req mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	approvalReq, err := mapApprovalRequest(req.Params)
	if err != nil || e.approver == nil {
		// Fail closed: a request we cannot map, or a mis-wired (nil) policy, is
		// denied — the policy is never consulted on a malformed request.
		return decisionToElicitationResult(approvalDecision{Allow: false, Reason: "codex: approval request denied (unmappable or no policy)"}), nil
	}
	return decisionToElicitationResult(e.approver(approvalReq)), nil
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
