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
	"context"
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
}

// Provider implements engine.ChatProvider by POSTing to
// {base-url}/chat/completions.
type Provider struct {
	binding          provider.ResolvedBinding
	httpClient       *http.Client
	timeout          time.Duration
	maxResponseBytes int64
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
	}
}

// boundedReader wraps r with the configured MaxResponseBytes cap. A zero cap
// returns r unchanged.
func (p *Provider) boundedReader(r io.Reader) io.Reader {
	if p.maxResponseBytes <= 0 {
		return r
	}
	return &limitedReader{r: r, remaining: p.maxResponseBytes}
}

// errResponseTooLarge is returned when a response exceeds MaxResponseBytes.
var errResponseTooLarge = errors.New("openrouter: response exceeds MaxResponseBytes")

// limitedReader is like io.LimitReader but returns an error (rather than EOF)
// when the limit is exceeded, so an over-large body is a hard failure.
type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, errResponseTooLarge
	}
	if int64(len(p)) > l.remaining+1 {
		p = p[:l.remaining+1]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	if l.remaining < 0 {
		return int(l.remaining + int64(n)), errResponseTooLarge
	}
	return n, err
}

// Chat POSTs a non-streaming completion and maps the first choice's content to
// ChatResponse.Output, copying the model and usage token counts.
func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	ctx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()

	body, err := buildBody(req, p.binding.Model, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := p.newRequest(ctx, body)
	if err != nil {
		return nil, err
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter: chat call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := p.boundedReader(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.statusError(resp.StatusCode, reader)
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("openrouter: read response: %w", err)
	}

	completion, err := decodeCompletion(raw)
	if err != nil {
		return nil, err
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
	streamCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	body, err := buildBody(req, p.binding.Model, true)
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq, err := p.newRequest(streamCtx, body)
	if err != nil {
		cancel()
		return nil, err
	}

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openrouter: stream call: %w", err)
	}

	out := make(chan engine.ChatChunk)
	go func() {
		defer cancel()
		defer close(out)
		defer func() { _ = resp.Body.Close() }()

		reader := p.boundedReader(resp.Body)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			send(streamCtx, out, engine.ChatChunk{Error: p.statusError(resp.StatusCode, reader)})
			return
		}

		var usage *usageBlock
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			delta, u, done, perr := parseSSELine(scanner.Bytes())
			if perr != nil {
				send(streamCtx, out, engine.ChatChunk{Error: perr})
				return
			}
			if u != nil {
				usage = u
			}
			if done {
				break
			}
			if delta != "" {
				if !send(streamCtx, out, engine.ChatChunk{Token: delta}) {
					return
				}
			}
		}
		if serr := scanner.Err(); serr != nil {
			send(streamCtx, out, engine.ChatChunk{Error: fmt.Errorf("openrouter: read stream: %w", serr)})
			return
		}

		final := engine.ChatChunk{Done: true}
		if usage != nil {
			final.TokensIn = usage.PromptTokens
			final.TokensOut = usage.CompletionTokens
		}
		send(streamCtx, out, final)
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

// statusError reads the bounded error body and wraps ErrUpstreamStatus with a
// snippet. It never includes the Authorization header or revealed key, and it
// scrubs control characters so a hostile upstream cannot shape an error string
// that injects newlines/escapes into a structured logger.
func (p *Provider) statusError(status int, body io.Reader) error {
	snippet, _ := io.ReadAll(body)
	const max = 512
	if len(snippet) > max {
		snippet = snippet[:max]
	}
	return fmt.Errorf("%w: status=%d body=%s", ErrUpstreamStatus, status, scrubControl(string(snippet)))
}

// scrubControl replaces ASCII control characters (except none are kept) with a
// space so an upstream-shaped error snippet cannot inject newlines, carriage
// returns, or terminal escapes into logs. Printable bytes and valid UTF-8 are
// preserved.
func scrubControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0 && r < 0x20) || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

