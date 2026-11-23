// adapter/http/runner_connect.go
package http

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runner"
)

// RunnerHub is the hub port the endpoint drives (*runner.Hub).
type RunnerHub interface {
	Admit(ctx context.Context, key runner.RunnerKey, conn runner.RunnerConn, hello runner.HelloInfo) (*runner.Session, error)
	Handle(ctx context.Context, s *runner.Session, in runner.Inbound) error
	Disconnect(s *runner.Session)
}

// RunnerConnectConfig tunes the endpoint; zero values take defaults.
type RunnerConnectConfig struct {
	HeartbeatSeconds int
	HandshakeTimeout time.Duration
	IdleTimeout      time.Duration
	WriteTimeout     time.Duration
	Logger           *slog.Logger
}

// RunnerConnectHandler serves GET /api/v1/runners/connect.
type RunnerConnectHandler struct {
	keys RunnerKeyAuthenticator
	hub  RunnerHub
	cfg  RunnerConnectConfig
	log  *slog.Logger
	auth *RunnerKeyMiddleware
}

// NewRunnerConnectHandler builds the endpoint and its key middleware.
func NewRunnerConnectHandler(keys RunnerKeyAuthenticator, hub RunnerHub, cfg RunnerConnectConfig) *RunnerConnectHandler {
	if keys == nil || hub == nil {
		panic("http: NewRunnerConnectHandler requires keys and hub")
	}
	if cfg.HeartbeatSeconds < 1 {
		cfg.HeartbeatSeconds = 5
	}
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 3 * time.Duration(cfg.HeartbeatSeconds) * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 10 * time.Second
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &RunnerConnectHandler{keys: keys, hub: hub, cfg: cfg, log: log, auth: NewRunnerKeyMiddleware(keys, log)}
}

// KeyAuth is the runner-key middleware to mount in front of ServeHTTP (and
// in front of Phase 3 transfer routes).
func (h *RunnerConnectHandler) KeyAuth() *RunnerKeyMiddleware { return h.auth }

var errRunnerConnClosed = errors.New("runner connect: connection closed")

// wsRunnerConn implements runner.RunnerConn over one WebSocket. Writes are
// serialised and bounded by WriteTimeout (never by the caller's step context,
// which would tear the socket mid-frame).
type wsRunnerConn struct {
	c                *websocket.Conn
	runnerID         runner.RunnerID
	heartbeatSeconds int
	writeTimeout     time.Duration

	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

func newWSRunnerConn(c *websocket.Conn, id runner.RunnerID, hb int, wt time.Duration) *wsRunnerConn {
	return &wsRunnerConn{c: c, runnerID: id, heartbeatSeconds: hb, writeTimeout: wt, done: make(chan struct{})}
}

func (w *wsRunnerConn) write(ctx context.Context, e runnerproto.Envelope) error {
	b, err := runnerproto.Encode(e)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errRunnerConnClosed
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), w.writeTimeout)
	defer cancel()
	return w.c.Write(wctx, websocket.MessageText, b)
}

// Send implements runner.RunnerConn.
func (w *wsRunnerConn) Send(ctx context.Context, m runner.Outbound) error {
	e, err := fromOutbound(m, w.runnerID, w.heartbeatSeconds)
	if err != nil {
		return err
	}
	return w.write(ctx, e)
}

func clampReason(s string) string {
	const max = 120 // WebSocket close reasons are limited to 123 bytes
	if len(s) > max {
		return s[:max]
	}
	return s
}

func (w *wsRunnerConn) markClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	w.closed = true
	return true
}

// Close implements runner.RunnerConn for hub-initiated closes (lost,
// superseded, revoked). It returns immediately; the close handshake runs in
// the background so the hub never blocks on a slow peer.
func (w *wsRunnerConn) Close(code int, reason string) error {
	if !w.markClosed() {
		return nil
	}
	go func() {
		_ = w.c.Close(websocket.StatusCode(code), clampReason(reason))
		close(w.done)
	}()
	return nil
}

// closeSync closes on the handler goroutine (handshake/read-loop failures).
func (w *wsRunnerConn) closeSync(code int, reason string) {
	if !w.markClosed() {
		return
	}
	_ = w.c.Close(websocket.StatusCode(code), clampReason(reason))
	close(w.done)
}

// waitClosed waits (bounded) for a background close handshake to finish.
func (w *wsRunnerConn) waitClosed(d time.Duration) {
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if !closed {
		return
	}
	select {
	case <-w.done:
	case <-time.After(d):
	}
}

func (h *RunnerConnectHandler) readEnvelope(ctx context.Context, conn *wsRunnerConn, timeout time.Duration) (runnerproto.Envelope, error) {
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	typ, b, err := conn.c.Read(rctx)
	if err != nil {
		return runnerproto.Envelope{}, err
	}
	if typ != websocket.MessageText {
		return runnerproto.Envelope{}, fmt.Errorf("%w: binary frame", runnerproto.ErrMalformed)
	}
	return runnerproto.Decode(b)
}

func isProtocolError(err error) bool {
	return errors.Is(err, runnerproto.ErrMalformed) || errors.Is(err, runnerproto.ErrUnknownKind) ||
		errors.Is(err, runnerproto.ErrUnsupportedVersion) || errors.Is(err, runnerproto.ErrFrameTooLarge) ||
		errors.Is(err, runner.ErrMalformedMessage)
}

func (h *RunnerConnectHandler) closeMalformed(conn *wsRunnerConn, id runner.RunnerID, stage string) {
	h.log.Warn("runner frame rejected", "runner", string(id), "stage", stage)
	conn.closeSync(runner.CloseMalformed, "malformed "+stage)
}

func (h *RunnerConnectHandler) refuse(ctx context.Context, conn *wsRunnerConn, id runner.RunnerID, ref *runnerproto.Refused, code int) {
	h.log.Warn("runner refused", "runner", string(id), "code", ref.Code, "reason", ref.Reason)
	if e, err := runnerproto.NewEnvelope(runnerproto.KindRefused, ref); err == nil {
		_ = conn.write(ctx, e)
	}
	conn.closeSync(code, ref.Reason)
}

// handshake: challenge -> challenge_response (machine proof) -> hello -> Admit.
func (h *RunnerConnectHandler) handshake(ctx context.Context, conn *wsRunnerConn, key runner.RunnerKey) (*runner.Session, bool) {
	nonce, err := runner.NewChallengeNonce()
	if err != nil {
		h.log.Error("runner challenge unavailable", "runner", string(key.RunnerID), "error", err)
		conn.closeSync(int(websocket.StatusInternalError), "challenge unavailable")
		return nil, false
	}
	ch, err := runnerproto.NewEnvelope(runnerproto.KindChallenge, &runnerproto.Challenge{KeyID: key.ID.String(), Nonce: base64.StdEncoding.EncodeToString(nonce)})
	if err != nil || conn.write(ctx, ch) != nil {
		return nil, false
	}

	env, err := h.readEnvelope(ctx, conn, h.cfg.HandshakeTimeout)
	if err != nil || env.Kind != runnerproto.KindChallengeResponse {
		h.closeMalformed(conn, key.RunnerID, "challenge response")
		return nil, false
	}
	var resp runnerproto.ChallengeResponse
	if err := runnerproto.DecodePayload(env, &resp); err != nil {
		h.closeMalformed(conn, key.RunnerID, "challenge response")
		return nil, false
	}
	pub, sig, err := resp.Decoded()
	if err != nil {
		h.closeMalformed(conn, key.RunnerID, "challenge response")
		return nil, false
	}
	proven, err := h.keys.ProveMachine(ctx, key, nonce, pub, sig)
	switch {
	case err == nil:
	case errors.Is(err, runner.ErrWrongMachine), errors.Is(err, runner.ErrKeyAlreadyClaimed):
		h.refuse(ctx, conn, key.RunnerID, &runnerproto.Refused{Code: "wrong_machine", Reason: "runner key is bound to a different machine"}, runner.CloseWrongMachine)
		return nil, false
	case errors.Is(err, runner.ErrKeyRevoked):
		h.refuse(ctx, conn, key.RunnerID, &runnerproto.Refused{Code: "revoked", Reason: "runner key revoked"}, runner.CloseRevoked)
		return nil, false
	default:
		h.log.Error("runner machine check failed", "runner", string(key.RunnerID), "error", err)
		conn.closeSync(int(websocket.StatusInternalError), "machine check failed")
		return nil, false
	}

	env, err = h.readEnvelope(ctx, conn, h.cfg.HandshakeTimeout)
	if err != nil || env.Kind != runnerproto.KindHello {
		h.closeMalformed(conn, key.RunnerID, "hello")
		return nil, false
	}
	var hello runnerproto.Hello
	if err := runnerproto.DecodePayload(env, &hello); err != nil {
		h.closeMalformed(conn, key.RunnerID, "hello")
		return nil, false
	}
	info, err := helloInfo(hello)
	if err != nil {
		h.closeMalformed(conn, key.RunnerID, "hello")
		return nil, false
	}
	s, err := h.hub.Admit(ctx, proven, conn, info)
	var inc *runner.IncompatibleError
	switch {
	case err == nil:
		return s, true
	case errors.As(err, &inc):
		h.refuse(ctx, conn, key.RunnerID, &runnerproto.Refused{Code: "incompatible", Reason: inc.Compat.Reason,
			ProtocolMin: runnerproto.ProtocolVersion, ProtocolMax: runnerproto.ProtocolVersion}, runner.CloseIncompatible)
	case errors.Is(err, runner.ErrRunnerRevoked):
		h.refuse(ctx, conn, key.RunnerID, &runnerproto.Refused{Code: "revoked", Reason: "runner key revoked"}, runner.CloseRevoked)
	default:
		h.log.Error("runner admission failed", "runner", string(key.RunnerID), "error", err)
		conn.closeSync(int(websocket.StatusInternalError), "admission failed")
	}
	return nil, false
}

func closeFor(err error) (int, string) {
	switch {
	case errors.Is(err, runner.ErrRunnerRevoked):
		return runner.CloseRevoked, "runner key revoked"
	case errors.Is(err, runner.ErrSessionClosed):
		return runner.CloseSuperseded, "session superseded"
	case errors.Is(err, runner.ErrMalformedMessage):
		return runner.CloseMalformed, "malformed message"
	default:
		return int(websocket.StatusInternalError), "server error"
	}
}

// readLoop feeds frames to the hub until the socket ends. A closed or idle
// socket is NOT runner loss: the hub's heartbeat-gap liveness decides that.
func (h *RunnerConnectHandler) readLoop(ctx context.Context, conn *wsRunnerConn, s *runner.Session) {
	for {
		env, err := h.readEnvelope(ctx, conn, h.cfg.IdleTimeout)
		if err != nil {
			if isProtocolError(err) {
				h.closeMalformed(conn, s.RunnerID, "frame")
			} else {
				h.log.Debug("runner socket ended", "runner", string(s.RunnerID), "status", int(websocket.CloseStatus(err)))
			}
			return
		}
		in, err := toInbound(env)
		if err != nil {
			h.closeMalformed(conn, s.RunnerID, env.Kind)
			return
		}
		if err := h.hub.Handle(ctx, s, in); err != nil {
			code, reason := closeFor(err)
			if code == int(websocket.StatusInternalError) {
				h.log.Error("runner message failed", "runner", string(s.RunnerID), "kind", env.Kind, "error", err)
			} else {
				h.log.Warn("runner connection closed", "runner", string(s.RunnerID), "reason", reason)
			}
			conn.closeSync(code, reason)
			return
		}
	}
}

// ServeHTTP upgrades an authenticated runner request and runs the session.
func (h *RunnerConnectHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, ok := RunnerKeyFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "RUNNER_KEY_REQUIRED", "Authorization: Runner <key> is required")
		return
	}
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept wrote the HTTP error
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(runnerproto.MaxFrameBytes)
	conn := newWSRunnerConn(c, key.RunnerID, h.cfg.HeartbeatSeconds, h.cfg.WriteTimeout)
	defer conn.waitClosed(h.cfg.WriteTimeout)
	ctx := r.Context()
	s, ok := h.handshake(ctx, conn, key)
	if !ok {
		return
	}
	defer h.hub.Disconnect(s)
	h.log.Info("runner connected", "runner", string(s.RunnerID), "key_id", s.KeyID.String())
	h.readLoop(ctx, conn, s)
	h.log.Info("runner disconnected", "runner", string(s.RunnerID))
}
