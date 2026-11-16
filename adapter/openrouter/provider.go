// Package openrouter implements engine.ChatProvider against any
// OpenAI-compatible chat-completions endpoint: OpenRouter itself, or a
// self-hosted LiteLLM router. The backend is selected entirely by the
// ResolvedBinding's BaseURL/Model/RevealKey — a LiteLLM router needs no code
// here beyond pointing a connection's base-url at it.
//
// The HTTP seam is injectable (Config.HTTPClient with a swappable
// http.RoundTripper) so tests run with httptest / a fake transport and never
// touch the live network.
package openrouter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// Config configures an OpenRouter (OpenAI-compatible) chat Provider.
//
// SECURITY:
//   - The binding's RevealKey is invoked lazily at call time and used only to
//     set the Authorization header. The revealed key is never logged or
//     persisted.
//   - MaxResponseBytes caps the per-request response body; 0 means no cap
//     (test convenience only).
type Config struct {
	Binding          provider.ResolvedBinding // BaseURL, Extras, Model, RevealKey
	HTTPClient       *http.Client             // nil → a default *http.Client
	Timeout          time.Duration            // 0 → no per-call timeout
	MaxResponseBytes int64                    // 0 → no cap (test convenience only)
	// ClassifyError maps gateway status, transport and protocol failures. Body
	// is complete and at most classifyBodyMax bytes, or empty. The hook owns
	// redaction; secrets contains the credential from this outbound request.
	// Nil preserves the existing OpenRouter errors.
	ClassifyError func(status int, body string, transport error, retryAfter string, secrets ...string) error
	// Preflight validates before body construction, key reveal or I/O. Its
	// error is returned unchanged, even before pinned-model validation.
	Preflight func(req engine.ChatRequest) error
	// AcceptExecutedModel overrides exact matching for pinned execution
	// evidence. Empty expected/reported models are always refused.
	AcceptExecutedModel func(expected, actual string) bool
}

// Provider implements engine.ChatProvider by POSTing to
// {base-url}/chat/completions.
type Provider struct {
	binding          provider.ResolvedBinding
	httpClient       *http.Client
	timeout          time.Duration
	maxResponseBytes int64
	classify         func(status int, body string, transport error, retryAfter string, secrets ...string) error
	preflight        func(engine.ChatRequest) error
	acceptModel      func(expected, actual string) bool
}

var _ engine.ChatProvider = (*Provider)(nil)

// New constructs a Provider from cfg. A nil HTTPClient uses a default
// *http.Client. A zero Timeout means no per-call timeout.
func New(cfg Config) *Provider {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	return &Provider{
		binding:          cfg.Binding,
		httpClient:       hc,
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
		classify:         cfg.ClassifyError,
		preflight:        cfg.Preflight,
		acceptModel:      cfg.AcceptExecutedModel,
	}
}

// boundedReader wraps r with the configured MaxResponseBytes cap (the shared
// procenv bounded read; over-cap yields procenv.ErrResponseTooLarge).
func (p *Provider) boundedReader(ctx context.Context, r io.Reader) io.Reader {
	return procenv.BoundedReader(r, procenv.ResponseLimit(ctx, p.maxResponseBytes))
}

// Chat POSTs a non-streaming completion and maps the first choice's content to
// ChatResponse.Output, copying the model and usage token counts.
func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	if p.preflight != nil {
		if err := p.preflight(req); err != nil {
			return nil, err
		}
	}
	ctx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()

	requireModel := p.binding.Pinned || req.RequireExecutedModel
	if requireModel && (p.binding.Model == "" || (req.Model != "" && req.Model != p.binding.Model)) {
		return nil, errModelExecutionEvidence
	}
	body, err := buildBody(req, p.binding.Model, false, p.binding.Effort)
	if err != nil {
		return nil, err
	}
	httpReq, err := p.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	key := strings.TrimPrefix(httpReq.Header.Get("Authorization"), "Bearer ")
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, p.transportError("chat", err, key)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := p.boundedReader(ctx, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.statusError(resp.StatusCode, reader, resp.Header.Get("Retry-After"), key)
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, p.responseError(fmt.Errorf("openrouter: read response: %w", err), key)
	}

	completion, err := decodeCompletion(raw)
	if err != nil {
		return nil, p.responseError(err, key)
	}
	if requireModel {
		if err := p.verifyExecutedModel(p.binding.Model, completion.Model); err != nil {
			return nil, err
		}
	}

	// Return the RAW assistant content string regardless of OutputSchema. The
	// domain executor's processLLMOutput is the SOLE structured-output parser, so
	// every ChatProvider hands back a string pre-parse (no adapter-side parsing,
	// no double-parse). See domain/engine/executor_llm.go.
	content := completion.Choices[0].Message.Content

	return &engine.ChatResponse{
		Output:    content,
		Model:     completion.Model,
		TokensIn:  completion.Usage.PromptTokens,
		TokensOut: completion.Usage.CompletionTokens,
	}, nil
}

// StreamChat POSTs a streaming completion and maps the SSE token stream to
// ChatChunks, terminated by a Done chunk carrying usage. Any mid-stream
// failure is delivered as a ChatChunk{Error} before the channel closes; the
// goroutine always drains the body and closes the channel.
func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	if p.preflight != nil {
		if err := p.preflight(req); err != nil {
			return nil, err
		}
	}
	streamCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	requireModel := p.binding.Pinned || req.RequireExecutedModel
	if requireModel && (p.binding.Model == "" || (req.Model != "" && req.Model != p.binding.Model)) {
		cancel()
		return nil, errModelExecutionEvidence
	}
	body, err := buildBody(req, p.binding.Model, true, p.binding.Effort)
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq, err := p.newRequest(streamCtx, body)
	if err != nil {
		cancel()
		return nil, err
	}

	key := strings.TrimPrefix(httpReq.Header.Get("Authorization"), "Bearer ")
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		cancel()
		return nil, p.transportError("stream", err, key)
	}

	out := make(chan engine.ChatChunk)
	if p.classify != nil {
		// Keep a slot for the terminal outcome even when cancellation means
		// the consumer has stopped reading. Other providers retain their
		// existing unbuffered stream behavior.
		out = make(chan engine.ChatChunk, 1)
	}
	go func() {
		defer cancel()
		defer close(out)
		defer func() { _ = resp.Body.Close() }()
		fail := func(err error) {
			chunk := engine.ChatChunk{Error: err}
			if send(streamCtx, out, chunk) || p.classify == nil {
				return
			}
			// The context expired while delivering the failure. This goroutine
			// is the only producer: discard at most one pending partial token
			// to guarantee room for the terminal error without blocking cleanup.
			select {
			case <-out:
			default:
			}
			out <- chunk
		}

		reader := p.boundedReader(streamCtx, resp.Body)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			fail(p.statusError(resp.StatusCode, reader, resp.Header.Get("Retry-After"), key))
			return
		}

		var usage *usageBlock
		var sawModel, sawDone, sawChoice bool
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			if p.classify != nil && streamCtx.Err() != nil {
				fail(p.transportError("stream", streamCtx.Err(), key))
				return
			}
			if p.classify != nil {
				if body, failed := gatewayErrorFrame(scanner.Bytes()); failed {
					fail(p.statusError(resp.StatusCode, bytes.NewReader(body), resp.Header.Get("Retry-After"), key))
					return
				}
			}
			frame, done, perr := parseSSELine(scanner.Bytes())
			if perr != nil {
				fail(p.responseError(perr, key))
				return
			}
			if done {
				sawDone = true
				break
			}
			if frame == nil {
				continue
			}
			if requireModel {
				if err := p.verifyExecutedModel(p.binding.Model, frame.Model); err != nil {
					fail(err)
					return
				}
				sawModel = true
			}
			if frame.Usage != nil {
				usage = frame.Usage
			}
			sawChoice = sawChoice || len(frame.Choices) > 0
			if p.classify != nil && len(frame.Choices) == 0 && frame.Usage == nil {
				fail(p.responseError(errors.New("openrouter: stream frame has no choices or usage"), key))
				return
			}
			if len(frame.Choices) > 0 && frame.Choices[0].Delta.Content != "" {
				if !send(streamCtx, out, engine.ChatChunk{Token: frame.Choices[0].Delta.Content}) {
					if p.classify != nil {
						fail(p.transportError("stream", streamCtx.Err(), key))
					}
					return
				}
			}
		}
		if serr := scanner.Err(); serr != nil {
			fail(p.responseError(fmt.Errorf("openrouter: read stream: %w", serr), key))
			return
		}
		if p.classify != nil && streamCtx.Err() != nil {
			fail(p.transportError("stream", streamCtx.Err(), key))
			return
		}

		// Probe budgets cover the complete response, including bytes after DONE.
		if procenv.ResponseLimit(streamCtx, 0) > 0 {
			if _, err := io.Copy(io.Discard, reader); err != nil {
				fail(p.responseError(err, key))
				return
			}
		}
		if requireModel && (!sawModel || !sawDone) {
			fail(fmt.Errorf("%w: incomplete stream", errModelExecutionEvidence))
			return
		}
		if p.classify != nil && (!sawChoice || !sawDone) {
			fail(p.responseError(errors.New("openrouter: incomplete stream"), key))
			return
		}
		final := engine.ChatChunk{Done: true}
		if usage != nil {
			final.TokensIn = usage.PromptTokens
			final.TokensOut = usage.CompletionTokens
		}
		if !send(streamCtx, out, final) && p.classify != nil {
			fail(p.transportError("stream", streamCtx.Err(), key))
		}
	}()

	return out, nil
}

// send performs a context-aware send so the goroutine returns if the consumer
// cancels mid-stream. It returns false if the context was cancelled.
func send(ctx context.Context, out chan<- engine.ChatChunk, c engine.ChatChunk) bool {
	select {
	case out <- c:
		return true
	case <-ctx.Done():
		return false
	}
}

// classifyBodyMax bounds complete diagnostics handed to ClassifyError.
const classifyBodyMax = 4096

// classifiedReadError preserves the original read/protocol error's identity
// without ever formatting its potentially credential-bearing text.
type classifiedReadError struct {
	classified, cause error
}

func (e *classifiedReadError) Error() string   { return e.classified.Error() }
func (e *classifiedReadError) Unwrap() []error { return []error{e.classified, e.cause} }

func (p *Provider) transportError(op string, err error, secrets ...string) error {
	if p.classify != nil {
		return p.classify(0, "", err, "", secrets...)
	}
	return fmt.Errorf("openrouter: %s call: %w", op, err)
}

// responseError keeps nil-hook diagnostics unchanged and classified failures
// safe while retaining errors.Is/As for the original read/decode failure.
func (p *Provider) responseError(err error, secrets ...string) error {
	if p.classify == nil {
		return err
	}
	return &classifiedReadError{classified: p.transportError("response", err, secrets...), cause: err}
}

// statusError omits oversized or incomplete bodies rather than forwarding a
// truncated secret. Nil hooks retain the original status/snippet behavior.
func (p *Provider) statusError(status int, body io.Reader, retryAfter string, secrets ...string) error {
	if p.classify != nil {
		raw, err := io.ReadAll(io.LimitReader(body, classifyBodyMax+1))
		if err != nil || len(raw) > classifyBodyMax {
			raw = nil
		}
		classified := p.classify(status, string(raw), nil, retryAfter, secrets...)
		if err != nil {
			return &classifiedReadError{classified: classified, cause: err}
		}
		return classified
	}
	return fmt.Errorf("%w: status=%d body=%s", ErrUpstreamStatus, status, procenv.StatusSnippet(body))
}

func (p *Provider) verifyExecutedModel(expected, actual string) error {
	if p.acceptModel == nil {
		return verifyExecutedModel(expected, actual)
	}
	if expected == "" || actual == "" || !p.acceptModel(expected, actual) {
		return errModelExecutionEvidence
	}
	return nil
}

// gatewayErrorFrame recognizes structured error envelopes, not provider codes.
// Interpretation and redaction stay with the injected classifier. Check before
// parsing content so an error object cannot become an empty successful stream.
func gatewayErrorFrame(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, []byte("data:")) {
		return nil, false
	}
	body := bytes.TrimSpace(line[len("data:"):])
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil {
		return nil, false
	}
	if raw, ok := envelope["error"]; ok && string(raw) != "null" {
		return body, true
	}
	var code, kind string
	_ = json.Unmarshal(envelope["code"], &code)
	_ = json.Unmarshal(envelope["type"], &kind)
	return body, code != "" || kind == "error"
}
