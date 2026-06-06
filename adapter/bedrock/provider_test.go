package bedrock

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

const testSecret = "TOP-SECRET-ACCESS-KEY"
const testSession = "TOP-SECRET-SESSION-TOKEN"

func fixedNow() time.Time { return time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC) }

// bindingTo points a bedrock binding at srv, supplying a sealed bare secret.
func bindingTo(srvURL, region, model string, withSession bool) provider.ResolvedBinding {
	sealed := testSecret
	if withSession {
		sealed = `{"secret":"` + testSecret + `","session_token":"` + testSession + `"}`
	}
	return provider.ResolvedBinding{
		Provider: "bedrock",
		Extras:   map[string]string{"region": region, "access-key-id": "AKID"},
		Model:    model,
		// BaseURL is unused by bedrock (endpoint is derived); kept empty.
		RevealKey: func(context.Context) (string, error) { return sealed, nil },
	}
}

// newProvider builds a Provider whose HTTP client is rerouted to srv via a
// rewriting transport, so the derived bedrock-runtime URL hits the test server.
func newProvider(t *testing.T, srv *httptest.Server, b provider.ResolvedBinding, maxBytes int64) *Provider {
	t.Helper()
	return New(Config{
		Binding:          b,
		HTTPClient:       &http.Client{Transport: rewriteTo(srv.URL)},
		MaxResponseBytes: maxBytes,
		Now:              fixedNow,
	})
}

// recordingTransport redirects every request to base (the httptest server) while
// preserving the original path — so SigV4 signs the real bedrock host but the
// bytes hit the local server. It also records the outgoing request for header
// assertions.
type recordingTransport struct {
	base string
	last *http.Request
}

func rewriteTo(base string) *recordingTransport { return &recordingTransport{base: base} }

func (rt *recordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	rt.last = r.Clone(r.Context())
	target, _ := http.NewRequestWithContext(r.Context(), r.Method, rt.base+r.URL.Path, r.Body)
	target.Header = r.Header
	return http.DefaultTransport.RoundTrip(target)
}

// happy-path: Chat returns assistant text + usage; request is SigV4-signed.
func TestChat_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Errorf("missing SigV4 Authorization: %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Amz-Date") == "" {
			t.Error("missing X-Amz-Date")
		}
		_, _ = io.WriteString(w, `{"output":{"message":{"content":[{"text":"hi there"}]}},"usage":{"inputTokens":2,"outputTokens":3}}`)
	}))
	defer srv.Close()

	p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "anthropic.claude-v2", false), 0)
	resp, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Output != "hi there" || resp.TokensIn != 2 || resp.TokensOut != 3 {
		t.Errorf("resp = %+v", resp)
	}
}

// error-handling: 403 -> ErrUpstreamStatus (config-class auth), secret NOT in error.
func TestChat_403AuthError_NoSecretLeak(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"The security token included in the request is invalid"}`)
	}))
	defer srv.Close()

	p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", true), 0)
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	if !errors.Is(err, ErrUpstreamStatus) {
		t.Fatalf("err = %v, want ErrUpstreamStatus", err)
	}
	if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testSession) {
		t.Errorf("error leaked a secret: %v", err)
	}
}

// error-handling: 400 (bad model) and 429/500 map to ErrUpstreamStatus.
func TestChat_StatusErrorsMapped(t *testing.T) {
	for _, code := range []int{400, 429, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"message":"nope"}`)
		}))
		p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 0)
		_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
		if !errors.Is(err, ErrUpstreamStatus) {
			t.Errorf("status %d: err = %v, want ErrUpstreamStatus", code, err)
		}
		srv.Close()
	}
}

// error-handling: RevealKey returning an error surfaces through Chat (no secret printed).
func TestChat_RevealKeyError(t *testing.T) {
	revealErr := errors.New("vault unreachable")
	b := provider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1", "access-key-id": "AKID"},
		Model:     "m",
		RevealKey: func(context.Context) (string, error) { return "", revealErr },
	}
	p := New(Config{Binding: b, Now: fixedNow})
	_, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"})
	if !errors.Is(err, revealErr) {
		t.Fatalf("err = %v, want errors.Is revealErr", err)
	}
}

// input-validation: missing region guarded BEFORE revealing the secret.
func TestChat_MissingRegionGuarded(t *testing.T) {
	revealed := false
	b := provider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"access-key-id": "AKID"}, // no region
		Model:     "m",
		RevealKey: func(context.Context) (string, error) { revealed = true; return testSecret, nil },
	}
	p := New(Config{Binding: b, Now: fixedNow})
	if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); !errors.Is(err, ErrMissingRegion) {
		t.Fatalf("err = %v, want ErrMissingRegion", err)
	}
	if revealed {
		t.Error("secret was revealed despite a missing-region guard failure")
	}
}

// input-validation: missing access-key-id guarded.
func TestChat_MissingAccessKeyIDGuarded(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "bedrock",
		Extras:    map[string]string{"region": "us-east-1"}, // no access-key-id
		Model:     "m",
		RevealKey: func(context.Context) (string, error) { return testSecret, nil },
	}
	p := New(Config{Binding: b, Now: fixedNow})
	if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); !errors.Is(err, ErrMissingAccessKeyID) {
		t.Fatalf("err = %v, want ErrMissingAccessKeyID", err)
	}
}

// security: response body capped by MaxResponseBytes (OOM DoS guard).
func TestChat_MaxResponseBytesCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("A", 1024)) // 1KiB > 16B cap
	}))
	defer srv.Close()
	p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 16)
	if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); err == nil {
		t.Fatal("expected an over-large-body error with a 16-byte cap")
	}
}

// security: the secret/session token are signed in but never appear in any
// outgoing header other than the (hex) signature / X-Amz-Security-Token.
func TestChat_NoSecretInOutgoingHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"output":{"message":{"content":[{"text":"ok"}]}},"usage":{}}`)
	}))
	defer srv.Close()
	rt := rewriteTo(srv.URL)
	b := bindingTo(srv.URL, "us-east-1", "m", true)
	p := New(Config{Binding: b, HTTPClient: &http.Client{Transport: rt}, Now: fixedNow})
	if _, err := p.Chat(context.Background(), engine.ChatRequest{Prompt: "x"}); err != nil {
		t.Fatal(err)
	}
	for name, vals := range rt.last.Header {
		if http.CanonicalHeaderKey(name) == "X-Amz-Security-Token" {
			continue
		}
		for _, v := range vals {
			if strings.Contains(v, testSecret) {
				t.Errorf("header %q leaked secret-access-key: %q", name, v)
			}
			if strings.Contains(v, testSession) {
				t.Errorf("header %q leaked session token: %q", name, v)
			}
		}
	}
	if rt.last.Header.Get("X-Amz-Security-Token") != testSession {
		t.Error("X-Amz-Security-Token should carry the session token")
	}
}

// happy-path: StreamChat yields ordered tokens then a Done chunk with usage.
func TestStreamChat_OrderedTokensThenDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Join([]string{
			`{"contentBlockDelta":{"delta":{"text":"Hel"}}}`,
			`{"contentBlockDelta":{"delta":{"text":"lo"}}}`,
			`{"metadata":{"usage":{"inputTokens":5,"outputTokens":6}}}`,
			`{"messageStop":{"stopReason":"end_turn"}}`,
		}, "\n"))
	}))
	defer srv.Close()
	p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 0)
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var tokens []string
	var done engine.ChatChunk
	for c := range ch {
		if c.Error != nil {
			t.Fatalf("stream error: %v", c.Error)
		}
		if c.Done {
			done = c
			continue
		}
		tokens = append(tokens, c.Token)
	}
	if strings.Join(tokens, "") != "Hello" {
		t.Errorf("tokens = %v, want Hello", tokens)
	}
	if !done.Done || done.TokensIn != 5 || done.TokensOut != 6 {
		t.Errorf("done = %+v, want Done with usage 5/6", done)
	}
}

// error-handling: StreamChat non-2xx status is delivered via channel, not as a
// direct return — the goroutine is already running when the status is checked.
func TestStreamChat_NonOKStatusDeliveredViaChannel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"unauthorized"}`)
	}))
	defer srv.Close()
	p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 0)
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	if err != nil {
		t.Fatalf("StreamChat must not return a direct error on non-2xx — got: %v", err)
	}
	var gotErr error
	for c := range ch {
		if c.Error != nil {
			gotErr = c.Error
		}
	}
	if !errors.Is(gotErr, ErrUpstreamStatus) {
		t.Fatalf("channel error = %v, want errors.Is ErrUpstreamStatus", gotErr)
	}
}

// error-handling: a malformed event mid-stream surfaces as a ChatChunk{Error}.
func TestStreamChat_MalformedEventError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Join([]string{
			`{"contentBlockDelta":{"delta":{"text":"ok"}}}`,
			`{"contentBlockDelta": BROKEN`,
		}, "\n"))
	}))
	defer srv.Close()
	p := newProvider(t, srv, bindingTo(srv.URL, "us-east-1", "m", false), 0)
	ch, err := p.StreamChat(context.Background(), engine.ChatRequest{Prompt: "x"})
	if err != nil {
		t.Fatal(err)
	}
	var gotErr error
	for c := range ch {
		if c.Error != nil {
			gotErr = c.Error
		}
	}
	if gotErr == nil {
		t.Fatal("expected a mid-stream ChatChunk{Error} for malformed JSON")
	}
}

// security: scrubControl prevents a hostile upstream error body from injecting
// control characters (newlines, CR, terminal escapes) into structured log output.
func TestScrubControl(t *testing.T) {
	cases := []struct {
		input string
		want  string
		desc  string
	}{
		{"", "", "empty string unchanged"},
		{"hello world", "hello world", "printable ASCII preserved"},
		{"a\x00b", "a b", "NUL (0x00) → space"},
		{"a\tb", "a b", "TAB (0x09) → space"},
		{"a\nb", "a b", "LF (0x0A) → space"},
		{"a\rb", "a b", "CR (0x0D) → space"},
		{"a\x1bb", "a b", "ESC (0x1B) → space"},
		{"a\x7fb", "a b", "DEL (0x7F) → space"},
		{"\x00\t\n\r\x1b\x7f", "      ", "sequence of control chars all → space"},
		{"café", "café", "valid UTF-8 multibyte preserved"},
		{"你好世界", "你好世界", "CJK runes preserved"},
		{"a\x1fb", "a b", "0x1F (unit sep) → space"},
		{"a b", "a b", "0x20 (space) preserved as-is"},
	}
	for _, c := range cases {
		got := scrubControl(c.input)
		if got != c.want {
			t.Errorf("scrubControl(%q) = %q, want %q (%s)", c.input, got, c.want, c.desc)
		}
	}
}
