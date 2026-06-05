// Package bedrock implements engine.ChatProvider against the Amazon Bedrock
// Converse API. Unlike the OpenAI-compatible adapters it derives its own
// regional endpoint and SIGNS each request with AWS SigV4 (service "bedrock")
// rather than setting a Bearer header. The HTTP and clock seams are injectable
// so tests run hermetically against httptest with a deterministic signature.
package bedrock

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrMissingAccessKeyID is returned when the binding has no non-empty
// Extras["access-key-id"], which is the (non-secret) SigV4 key identifier.
var ErrMissingAccessKeyID = errors.New("bedrock: binding Extras[\"access-key-id\"] is empty")

// sigService is the AWS service name signed into the SigV4 credential scope.
const sigService = "bedrock"

// Config configures a Bedrock Converse chat Provider.
//
// SECURITY:
//   - The binding's RevealKey is invoked lazily at call time and parsed via
//     awssig.ParseCredential; the revealed secret/session token contribute only
//     to the SigV4 signature (and X-Amz-Security-Token) — never logged, never
//     placed in any other header, never included in an error.
//   - Region and access-key-id are validated BEFORE RevealKey is invoked, so an
//     obviously-misconfigured binding never decrypts the secret.
//   - MaxResponseBytes caps the per-request response body; 0 means no cap (test
//     convenience only).
type Config struct {
	Binding          provider.ResolvedBinding // Extras{region, access-key-id}, Model, RevealKey
	HTTPClient       *http.Client             // nil → a default *http.Client
	Timeout          time.Duration            // 0 → no per-call timeout
	MaxResponseBytes int64                    // 0 → no cap (test convenience only)
	// Now is the injectable clock used for the SigV4 X-Amz-Date. nil → time.Now.
	Now func() time.Time
}

// Provider implements engine.ChatProvider by POSTing a SigV4-signed Converse
// request to the derived regional bedrock-runtime endpoint.
type Provider struct {
	binding          provider.ResolvedBinding
	httpClient       *http.Client
	timeout          time.Duration
	maxResponseBytes int64
	now              func() time.Time
}

var _ engine.ChatProvider = (*Provider)(nil)

// New constructs a Provider from cfg. A nil HTTPClient uses a default
// *http.Client; a nil Now uses time.Now; a zero Timeout means no per-call
// timeout.
func New(cfg Config) *Provider {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{
		binding:          cfg.Binding,
		httpClient:       hc,
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
		now:              now,
	}
}

// errResponseTooLarge is returned when a response exceeds MaxResponseBytes.
var errResponseTooLarge = errors.New("bedrock: response exceeds MaxResponseBytes")

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

// boundedReader wraps r with the configured MaxResponseBytes cap. A zero cap
// returns r unchanged.
func (p *Provider) boundedReader(r io.Reader) io.Reader {
	if p.maxResponseBytes <= 0 {
		return r
	}
	return &limitedReader{r: r, remaining: p.maxResponseBytes}
}

// signedRequest derives the endpoint, guards region/access-key-id BEFORE
// revealing the secret, reveals + parses the sealed credential, and SigV4-signs
// the POST. The revealed secret is used only by awssig.Sign.
func (p *Provider) signedRequest(ctx context.Context, body []byte, stream bool) (*http.Request, error) {
	accessKeyID := strings.TrimSpace(p.binding.Extras["access-key-id"])
	if accessKeyID == "" {
		return nil, ErrMissingAccessKeyID
	}
	region := strings.TrimSpace(p.binding.Extras["region"])
	if region == "" {
		return nil, ErrMissingRegion
	}
	target, err := endpointURL(p.binding, stream)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("bedrock: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	sealed := ""
	if p.binding.RevealKey != nil {
		sealed, err = p.binding.RevealKey(ctx)
		if err != nil {
			return nil, fmt.Errorf("bedrock: reveal key: %w", err)
		}
	}
	cred, err := awssig.ParseCredential(sealed)
	if err != nil {
		return nil, err
	}
	if err := awssig.Sign(httpReq, body, accessKeyID, cred, region, sigService, p.now()); err != nil {
		return nil, err
	}
	return httpReq, nil
}

// Chat POSTs a non-streaming Converse request and maps the joined assistant text
// and usage to a ChatResponse.
func (p *Provider) Chat(ctx context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	ctx, cancel := procenv.WithTimeout(ctx, p.timeout)
	defer cancel()

	body, err := buildBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := p.signedRequest(ctx, body, false)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("bedrock: chat call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	reader := p.boundedReader(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, p.statusError(resp.StatusCode, reader)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("bedrock: read response: %w", err)
	}
	text, in, out, err := decodeConverse(raw)
	if err != nil {
		return nil, err
	}
	return &engine.ChatResponse{
		Output:    text,
		Model:     p.binding.Model,
		TokensIn:  in,
		TokensOut: out,
	}, nil
}

// StreamChat POSTs a converse-stream request and maps each decoded event to a
// ChatChunk, terminated by a Done chunk carrying usage. Any mid-stream failure
// is delivered as a ChatChunk{Error} before the channel closes; the goroutine
// always drains the body and closes the channel.
func (p *Provider) StreamChat(ctx context.Context, req engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	streamCtx, cancel := procenv.WithTimeout(ctx, p.timeout)

	body, err := buildBody(req)
	if err != nil {
		cancel()
		return nil, err
	}
	httpReq, err := p.signedRequest(streamCtx, body, true)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("bedrock: stream call: %w", err)
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
		var tin, tout int
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			delta, in, o, done, perr := parseConverseEvent(scanner.Bytes())
			if perr != nil {
				send(streamCtx, out, engine.ChatChunk{Error: perr})
				return
			}
			if in != 0 {
				tin = in
			}
			if o != 0 {
				tout = o
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
			send(streamCtx, out, engine.ChatChunk{Error: fmt.Errorf("bedrock: read stream: %w", serr)})
			return
		}
		send(streamCtx, out, engine.ChatChunk{Done: true, TokensIn: tin, TokensOut: tout})
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
// control-scrubbed snippet. It never includes any credential.
func (p *Provider) statusError(status int, body io.Reader) error {
	snippet, _ := io.ReadAll(body)
	const max = 512
	if len(snippet) > max {
		snippet = snippet[:max]
	}
	return fmt.Errorf("%w: status=%d body=%s", ErrUpstreamStatus, status, scrubControl(string(snippet)))
}

// scrubControl replaces ASCII control characters with a space so an
// upstream-shaped error snippet cannot inject newlines, carriage returns, or
// terminal escapes into logs. Printable bytes and valid UTF-8 are preserved.
func scrubControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || (r >= 0 && r < 0x20) || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
