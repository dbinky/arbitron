package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errHooked = errors.New("hooked")

type hookCall struct {
	status     int
	body       string
	transport  error
	retryAfter string
	secrets    []string
}

func hookedProvider(ft *fakeTransport, calls *[]hookCall, preflight func(engine.ChatRequest) error) *Provider {
	return New(Config{
		Binding:      staticBinding("https://gw.example/v1", "m"),
		HTTPClient:   &http.Client{Transport: ft},
		StrictStream: true,
		ClassifyError: func(status int, body string, transport error, retryAfter string, secrets ...string) error {
			*calls = append(*calls, hookCall{status, body, transport, retryAfter, append([]string(nil), secrets...)})
			return errHooked
		},
		Preflight: preflight,
	})
}

func TestClassifyError_ChatStatus(t *testing.T) {
	var calls []hookCall
	ft := &fakeTransport{resp: jsonResponse(503, `{"error":"down"}`)}
	ft.resp.Header.Set("Retry-After", "007")
	_, err := hookedProvider(ft, &calls, nil).Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Same(t, errHooked, err)
	require.Len(t, calls, 1)
	assert.Equal(t, 503, calls[0].status)
	assert.Equal(t, `{"error":"down"}`, calls[0].body)
	assert.Nil(t, calls[0].transport)
	assert.Equal(t, "007", calls[0].retryAfter)
	assert.Equal(t, []string{secretKey}, calls[0].secrets)
	assert.Equal(t, "Bearer "+secretKey, ft.gotReq.Header.Get("Authorization"))
}

func TestClassifyError_BodyBounded(t *testing.T) {
	for _, size := range []int{4096, 4097, 2 << 20} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var calls []hookCall
			body := strings.Repeat("z", size)
			_, err := hookedProvider(&fakeTransport{resp: jsonResponse(500, body)}, &calls, nil).Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
			require.Same(t, errHooked, err)
			require.Len(t, calls, 1)
			if size <= 4096 {
				assert.Equal(t, body, calls[0].body)
			} else {
				assert.Empty(t, calls[0].body)
			}
		})
	}
}

func TestClassifyError_ChatTransport(t *testing.T) {
	var calls []hookCall
	boom := errors.New("connection reset")
	_, err := hookedProvider(&fakeTransport{err: boom}, &calls, nil).Chat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Same(t, errHooked, err)
	require.Len(t, calls, 1)
	assert.Zero(t, calls[0].status)
	assert.ErrorIs(t, calls[0].transport, boom)
	assert.Empty(t, calls[0].body)
	assert.Empty(t, calls[0].retryAfter)
	assert.Equal(t, []string{secretKey}, calls[0].secrets)
}

func drainHookStream(t *testing.T, p *Provider, req engine.ChatRequest) (string, bool, error) {
	t.Helper()
	ch, err := p.StreamChat(context.Background(), req)
	if err != nil {
		return "", false, err
	}
	var output string
	var done bool
	for c := range ch {
		output += c.Token
		done = done || c.Done
		if c.Error != nil {
			err = c.Error
		}
	}
	return output, done, err
}

func TestClassifyError_StreamImmediateAndChannel(t *testing.T) {
	var calls []hookCall
	boom := errors.New("dial tcp: refused")
	_, err := hookedProvider(&fakeTransport{err: boom}, &calls, nil).StreamChat(context.Background(), engine.ChatRequest{Prompt: "hi"})
	require.Same(t, errHooked, err)
	require.Len(t, calls, 1)
	assert.ErrorIs(t, calls[0].transport, boom)
	assert.Equal(t, []string{secretKey}, calls[0].secrets)

	calls = nil
	ft := &fakeTransport{resp: sseResponse(429, `{"error":"busy"}`)}
	ft.resp.Header.Set("Retry-After", "17")
	_, done, err := drainHookStream(t, hookedProvider(ft, &calls, nil), engine.ChatRequest{Prompt: "hi"})
	require.Same(t, errHooked, err)
	assert.False(t, done)
	require.Len(t, calls, 1)
	assert.Equal(t, 429, calls[0].status)
	assert.Equal(t, `{"error":"busy"}`, calls[0].body)
	assert.Equal(t, "17", calls[0].retryAfter)
	assert.Equal(t, []string{secretKey}, calls[0].secrets)
}

func TestPreflight_BlocksBeforeAnyIO(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var calls []hookCall
			refused := errors.New("refused by preflight")
			var seen engine.ChatRequest
			ft := &fakeTransport{resp: jsonResponse(200, `{}`)}
			p := hookedProvider(ft, &calls, func(r engine.ChatRequest) error { seen = r; return refused })
			// Preflight must even precede pin mismatch, body validation and key reveal.
			p.binding.Pinned = true
			p.binding.RevealKey = func(context.Context) (string, error) { t.Fatal("revealed before preflight"); return "", nil }
			req := engine.ChatRequest{Prompt: "hi", Model: "mismatch", MaxTokens: 77}
			var err error
			if streaming {
				_, err = p.StreamChat(context.Background(), req)
			} else {
				_, err = p.Chat(context.Background(), req)
			}
			require.Same(t, refused, err)
			assert.Equal(t, req, seen)
			assert.Nil(t, ft.gotReq)
			assert.Empty(t, calls)
		})
	}
}

const hookCompletion = `{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`
const hookStream = "data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"

func TestPreflight_PassLetsRequestThrough(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var calls []hookCall
			ft := &fakeTransport{resp: jsonResponse(200, hookCompletion)}
			if streaming {
				ft.resp = sseResponse(200, hookStream)
			}
			seen := 0
			p := hookedProvider(ft, &calls, func(req engine.ChatRequest) error { seen++; assert.Equal(t, 77, req.MaxTokens); return nil })
			p.binding.Pinned = true
			req := engine.ChatRequest{Prompt: "hi", MaxTokens: 77}
			if streaming {
				output, done, err := drainHookStream(t, p, req)
				require.NoError(t, err)
				assert.Equal(t, "ok", output)
				assert.True(t, done)
			} else {
				resp, err := p.Chat(context.Background(), req)
				require.NoError(t, err)
				assert.Equal(t, "ok", resp.Output)
				assert.Equal(t, "m", resp.Model)
			}
			assert.Equal(t, 1, seen)
			require.NotNil(t, ft.gotReq)
			assert.Equal(t, "https://gw.example/v1/chat/completions", ft.gotReq.URL.String())
			assert.Equal(t, "Bearer "+secretKey, ft.gotReq.Header.Get("Authorization"))
			var body map[string]any
			require.NoError(t, json.Unmarshal(ft.gotBody, &body))
			assert.Equal(t, "m", body["model"])
			assert.EqualValues(t, 77, body["max_tokens"])
			assert.Contains(t, string(ft.gotBody), `"content":"hi"`)
			assert.Empty(t, calls)
		})
	}
}

func TestNilHooks_BehaviourUnchanged(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			p := New(Config{Binding: staticBinding("https://gw.example/v1", "m"), HTTPClient: &http.Client{Transport: &fakeTransport{resp: jsonResponse(500, `boom`)}}})
			var err error
			if streaming {
				_, _, err = drainHookStream(t, p, engine.ChatRequest{})
			} else {
				_, err = p.Chat(context.Background(), engine.ChatRequest{})
			}
			require.EqualError(t, err, ErrUpstreamStatus.Error()+": status=500 body=boom")
			require.ErrorIs(t, err, ErrUpstreamStatus)
			boom := errors.New("reset")
			p = New(Config{Binding: staticBinding("https://gw.example/v1", "m"), HTTPClient: &http.Client{Transport: &fakeTransport{err: boom}}})
			if streaming {
				_, err = p.StreamChat(context.Background(), engine.ChatRequest{})
			} else {
				_, err = p.Chat(context.Background(), engine.ChatRequest{})
			}
			require.ErrorIs(t, err, boom)
			op := "chat"
			if streaming {
				op = "stream"
			}
			assert.Contains(t, err.Error(), "openrouter: "+op+" call:")
		})
	}
}

type hookErrorReader struct{ err error }

func (r hookErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestClassifyError_ChatReadAndDecode(t *testing.T) {
	boom := errors.New("read failed with " + secretKey)
	for _, tc := range []struct {
		name   string
		status int
		body   io.Reader
		cause  error
	}{
		{"status read", 503, io.MultiReader(strings.NewReader("partial "+secretKey[:12]), hookErrorReader{boom}), boom},
		{"read", 200, hookErrorReader{boom}, boom},
		{"decode", 200, strings.NewReader(`{"choices":`), nil},
		{"missing choices", 200, strings.NewReader(`{}`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []hookCall
			resp := jsonResponse(tc.status, "")
			resp.Body = io.NopCloser(tc.body)
			_, err := hookedProvider(&fakeTransport{resp: resp}, &calls, nil).Chat(context.Background(), engine.ChatRequest{})
			require.ErrorIs(t, err, errHooked)
			require.Len(t, calls, 1)
			if tc.cause != nil {
				assert.ErrorIs(t, err, tc.cause)
			}
			if tc.name == "decode" {
				var syntax *json.SyntaxError
				assert.ErrorAs(t, err, &syntax)
			}
			assert.Equal(t, "hooked", err.Error(), "original unsafe cause must not be formatted")
			assert.Empty(t, calls[0].body)
			assert.Equal(t, []string{secretKey}, calls[0].secrets)
			if tc.status == 200 {
				assert.NotNil(t, calls[0].transport)
				assert.Zero(t, calls[0].status)
			}
		})
	}
}

func TestClassifyError_StreamReadAndMalformedFrame(t *testing.T) {
	boom := errors.New("stream failed with " + secretKey)
	for _, tc := range []struct {
		name  string
		body  io.Reader
		cause error
	}{
		{"read", hookErrorReader{boom}, boom},
		{"malformed", strings.NewReader("data: {broken}\n\n"), nil},
		{"scanner limit", strings.NewReader("data: " + strings.Repeat("x", 2<<20)), nil},
		{"missing choice", strings.NewReader("data: {}\n\ndata: [DONE]\n\n"), nil},
		{"missing terminal", strings.NewReader(strings.TrimSuffix(hookStream, "data: [DONE]\n\n")), nil},
		{"empty", strings.NewReader(""), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []hookCall
			resp := sseResponse(200, "")
			resp.Body = io.NopCloser(tc.body)
			_, done, err := drainHookStream(t, hookedProvider(&fakeTransport{resp: resp}, &calls, nil), engine.ChatRequest{})
			require.ErrorIs(t, err, errHooked)
			assert.False(t, done)
			require.Len(t, calls, 1)
			if tc.cause != nil {
				assert.ErrorIs(t, err, tc.cause)
			}
			if tc.name == "malformed" {
				var syntax *json.SyntaxError
				assert.ErrorAs(t, err, &syntax)
			}
			assert.Equal(t, "hooked", err.Error())
			assert.Zero(t, calls[0].status)
			assert.Empty(t, calls[0].body)
			assert.NotNil(t, calls[0].transport)
			assert.Equal(t, []string{secretKey}, calls[0].secrets)
		})
	}
}

func TestClassifyError_StreamGatewayErrorFrame(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","error":{"code":"CONCURRENT_REQUEST_LIMIT_EXCEEDED","message":"echo ` + secretKey + `"}}`,
		`{"code":"MODEL_UNPRICED","message":"echo ` + secretKey + `"}`,
		`{"error":{"message":"no code"}}`,
	} {
		t.Run(body[:12], func(t *testing.T) {
			var calls []hookCall
			ft := &fakeTransport{resp: sseResponse(200, "data: "+body+"\n\n"+hookStream)}
			ft.resp.Header.Set("Retry-After", "9")
			p := hookedProvider(ft, &calls, nil)
			p.binding.Pinned = true // Gateway errors must be classified before evidence checks.
			output, done, err := drainHookStream(t, p, engine.ChatRequest{})
			require.Same(t, errHooked, err)
			assert.Empty(t, output)
			assert.False(t, done)
			require.Len(t, calls, 1)
			assert.Equal(t, 200, calls[0].status)
			assert.Equal(t, body, calls[0].body)
			assert.Nil(t, calls[0].transport)
			assert.Equal(t, "9", calls[0].retryAfter)
			assert.Equal(t, []string{secretKey}, calls[0].secrets)
		})
	}
}

func TestClassifyError_NoPartialSecretAtBodyLimit(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var calls []hookCall
			body := `{"error":{"code":"INVALID_API_KEY","message":"` + strings.Repeat("x", 4040) + secretKey + `"}}`
			if !streaming {
				body = strings.Repeat("x", 4080) + secretKey
			}
			ft := &fakeTransport{resp: jsonResponse(401, body)}
			if streaming {
				ft.resp = sseResponse(200, "data: "+body+"\n\n")
			}
			logs := captureLogs(t, func() {
				p := hookedProvider(ft, &calls, nil)
				var err error
				if streaming {
					_, done, e := drainHookStream(t, p, engine.ChatRequest{})
					err = e
					assert.False(t, done)
				} else {
					_, err = p.Chat(context.Background(), engine.ChatRequest{})
				}
				require.ErrorIs(t, err, errHooked)
				assert.NotContains(t, err.Error(), secretKey[:12])
			})
			assert.NotContains(t, logs, secretKey[:12])
			require.Len(t, calls, 1)
			assert.Empty(t, calls[0].body, "discard entire oversized body, not just its suffix")
		})
	}
}

func TestClassifyError_ResponseBudget(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, afterDone := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/afterDone=%v", streaming, afterDone), func(t *testing.T) {
				var calls []hookCall
				body := hookCompletion + strings.Repeat(" ", int(procenv.ProbeResponseBytes)+1)
				if streaming {
					body = strings.Repeat("\n", int(procenv.ProbeResponseBytes)+1)
					if afterDone {
						body = hookStream + body
					}
				}
				p := hookedProvider(&fakeTransport{resp: jsonResponse(200, body)}, &calls, nil)
				ctx := procenv.WithProbeResponseLimit(context.Background())
				var err error
				if streaming {
					ch, e := p.StreamChat(ctx, engine.ChatRequest{})
					require.NoError(t, e)
					for c := range ch {
						assert.False(t, c.Done)
						if c.Error != nil {
							err = c.Error
						}
					}
				} else {
					_, err = p.Chat(ctx, engine.ChatRequest{})
				}
				require.ErrorIs(t, err, errHooked)
				assert.ErrorIs(t, err, procenv.ErrResponseTooLarge)
				require.Len(t, calls, 1)
				assert.Equal(t, []string{secretKey}, calls[0].secrets)
			})
		}
	}
}

func TestAcceptExecutedModel_ChatAndStream(t *testing.T) {
	const selected = "claude-sonnet-5-lr1"
	const drift = "claude-sonnet-5-5"
	for _, streaming := range []bool{false, true} {
		for _, mode := range []string{"nil exact", "nil drift", "accept drift", "reject exact", "accept empty"} {
			t.Run(fmt.Sprintf("stream=%v/%s", streaming, mode), func(t *testing.T) {
				actual := selected
				if strings.Contains(mode, "drift") {
					actual = drift
				}
				if mode == "accept empty" {
					actual = ""
				}
				b := staticBinding("https://gw.example/v1", selected)
				b.Pinned = true
				body := fmt.Sprintf(`{"model":%q,"choices":[{"message":{"content":"ok"}}]}`, actual)
				if streaming {
					body = fmt.Sprintf("data: {\"model\":%q,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", actual)
				}
				var accept func(string, string) bool
				seen := 0
				if !strings.HasPrefix(mode, "nil") {
					accept = func(expected, reported string) bool {
						seen++
						assert.Equal(t, selected, expected)
						assert.Equal(t, actual, reported)
						return strings.HasPrefix(mode, "accept")
					}
				}
				p := New(Config{Binding: b, HTTPClient: &http.Client{Transport: &fakeTransport{resp: jsonResponse(200, body)}}, AcceptExecutedModel: accept})
				var err error
				var output string
				var done bool
				if streaming {
					output, done, err = drainHookStream(t, p, engine.ChatRequest{})
				} else {
					var resp *engine.ChatResponse
					resp, err = p.Chat(context.Background(), engine.ChatRequest{})
					if resp != nil {
						output = resp.Output.(string)
						assert.Equal(t, actual, resp.Model)
					}
				}
				if mode == "nil exact" || mode == "accept drift" {
					require.NoError(t, err)
					assert.Equal(t, "ok", output)
					if streaming {
						assert.True(t, done)
					}
				} else {
					require.ErrorIs(t, err, errModelExecutionEvidence)
					assert.False(t, done)
					assert.Empty(t, output)
					if actual != "" {
						assert.NotContains(t, err.Error(), actual, "upstream model values are not diagnostics")
					}
				}
				if mode == "accept drift" || mode == "reject exact" {
					assert.Equal(t, 1, seen)
				}
			})
		}
	}
}

type hookRoundTripper func(*http.Request) (*http.Response, error)

func (f hookRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func awaitHookValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for request or classification")
		var zero T
		return zero
	}
}

func TestClassifyError_ConcurrentCredentialRotation(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			const first = "opaque-first-credential-12345"
			const second = "opaque-second-credential-67890"
			var reveals atomic.Int32
			b := staticBinding("https://gw.example/v1", "m")
			b.RevealKey = func(context.Context) (string, error) {
				if reveals.Add(1) == 1 {
					return first, nil
				}
				return second, nil
			}
			arrived := make(chan string, 2)
			releaseFirst := make(chan struct{}, 1)
			defer close(releaseFirst)
			calls := make(chan hookCall, 2)
			transport := hookRoundTripper(func(r *http.Request) (*http.Response, error) {
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				arrived <- key
				if key == first {
					select {
					case <-releaseFirst:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				if streaming {
					return sseResponse(401, `{"error":{"code":"INVALID_API_KEY","message":"`+key+`"}}`), nil
				}
				return nil, errors.New("echo " + key)
			})
			p := New(Config{Binding: b, HTTPClient: &http.Client{Transport: transport}, StrictStream: true, ClassifyError: func(status int, body string, cause error, retry string, secrets ...string) error {
				calls <- hookCall{status, body, cause, retry, append([]string(nil), secrets...)}
				return provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: status, Body: body, Code: provider.LunaRouteErrorCode(provider.LunaRouteChat, body), Transport: cause, Secrets: secrets})
			}})
			logs := captureLogs(t, func() {
				run := func(result chan<- error) {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if streaming {
						ch, err := p.StreamChat(ctx, engine.ChatRequest{Prompt: "hi"})
						if err == nil {
							for c := range ch {
								if c.Error != nil {
									err = c.Error
								}
								assert.False(t, c.Done)
							}
						}
						result <- err
					} else {
						_, err := p.Chat(ctx, engine.ChatRequest{Prompt: "hi"})
						result <- err
					}
				}
				one := make(chan error, 1)
				two := make(chan error, 1)
				go run(one)
				require.Equal(t, first, awaitHookValue(t, arrived))
				go run(two)
				require.Equal(t, second, awaitHookValue(t, arrived))
				err2 := awaitHookValue(t, two)
				call2 := awaitHookValue(t, calls)
				assert.Equal(t, []string{second}, call2.secrets)
				require.Error(t, err2)
				assert.NotContains(t, err2.Error(), second)
				assert.NotContains(t, err2.Error(), second[:15])
				releaseFirst <- struct{}{}
				err1 := awaitHookValue(t, one)
				call1 := awaitHookValue(t, calls)
				assert.Equal(t, []string{first}, call1.secrets)
				require.Error(t, err1)
				assert.NotContains(t, err1.Error(), first)
				assert.NotContains(t, err1.Error(), first[:15])
			})
			assert.NotContains(t, logs, first[:15])
			assert.NotContains(t, logs, second[:15])
		})
	}
}

func TestClassifyError_RetryAfterAndPricing(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, code := range []string{"CONCURRENT_REQUEST_LIMIT_EXCEEDED", "MODEL_UNPRICED"} {
			t.Run(fmt.Sprintf("stream=%v/%s", streaming, code), func(t *testing.T) {
				body := fmt.Sprintf(`{"error":{"code":%q}}`, code)
				resp := jsonResponse(429, body)
				if streaming {
					resp = sseResponse(200, "data: "+body+"\n\n")
				}
				resp.Header.Set("Retry-After", "007")
				p := New(Config{Binding: staticBinding("https://gw.example/v1", "m"), HTTPClient: &http.Client{Transport: &fakeTransport{resp: resp}}, StrictStream: true, ClassifyError: func(status int, body string, cause error, retry string, secrets ...string) error {
					return provider.ClassifyLunaRoute(provider.LunaRouteOutcome{Operation: provider.LunaRouteChat, Status: status, Code: provider.LunaRouteErrorCode(provider.LunaRouteChat, body), Transport: cause, RetryAfter: retry, Secrets: secrets})
				}})
				var err error
				if streaming {
					_, done, e := drainHookStream(t, p, engine.ChatRequest{})
					err = e
					assert.False(t, done)
				} else {
					_, err = p.Chat(context.Background(), engine.ChatRequest{})
				}
				var failure *provider.LunaRouteFailure
				require.ErrorAs(t, err, &failure)
				if code == "MODEL_UNPRICED" {
					assert.ErrorIs(t, err, provider.ErrLunaRouteRequestRefused)
					assert.Empty(t, failure.RetryAfter)
					assert.NotContains(t, err.Error(), "retry after")
				} else {
					assert.ErrorIs(t, err, provider.ErrLunaRouteBusy)
					assert.Equal(t, "7", failure.RetryAfter)
					assert.Contains(t, err.Error(), "retry after 7")
				}
			})
		}
	}
}
