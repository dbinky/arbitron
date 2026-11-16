package provider_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	adapterprovider "github.com/arbitron/arbitron/adapter/provider"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type streamFailureTransport func(*http.Request) (*http.Response, error)

func (f streamFailureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type canceledStreamBody struct {
	ctx    context.Context
	closed chan struct{}
}

func (b *canceledStreamBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *canceledStreamBody) Close() error {
	close(b.closed)
	return nil
}

// A canceled stream used to select ctx.Done instead of delivering its error,
// closing the channel without any outcome. Waiting for body closure before
// reading makes that lost-error path deterministic and also catches leaks.
func TestLunaRouteChat_StreamCancellationOutcome(t *testing.T) {
	for _, mode := range []string{"cancel", "parent deadline", "provider timeout", "blocked consumer"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantCause := context.Canceled
			wantKind := domprovider.ErrLunaRouteCauseUnknown
			var timeout time.Duration
			if mode == "parent deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 20*time.Millisecond)
				defer deadlineCancel()
				wantCause, wantKind = context.DeadlineExceeded, domprovider.ErrLunaRouteServiceFailure
			}
			if mode == "provider timeout" {
				timeout = 20 * time.Millisecond
				wantCause, wantKind = context.DeadlineExceeded, domprovider.ErrLunaRouteServiceFailure
			}
			closed := make(chan struct{})
			started := make(chan struct{})
			g := newChatGW()
			g.expected["stream"] = true
			transport := streamFailureTransport(func(r *http.Request) (*http.Response, error) {
				resp, err := g.RoundTrip(r)
				if err != nil {
					return nil, err
				}
				_ = resp.Body.Close()
				if mode == "blocked consumer" {
					const frame = "data: {\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
					resp.Body = &closingStreamBody{Reader: io.MultiReader(strings.NewReader(frame), &signaledStreamReader{Reader: strings.NewReader(frame + frame), started: started}), closed: closed}
				} else {
					resp.Body = &canceledStreamBody{ctx: r.Context(), closed: closed}
				}
				return resp, nil
			})
			b := lrChatBinding("", lrChatKey, nil)
			b.Pinned = true
			cp, err := adapterprovider.NewFactory(adapterprovider.Deps{HTTPClient: &http.Client{Transport: transport}, HTTPTimeout: timeout, CapabilityRules: lrChatCaps()}).ChatProviderFor(b)
			require.NoError(t, err)
			ch, err := cp.StreamChat(ctx, lrChatRequest())
			require.NoError(t, err)
			if mode == "blocked consumer" {
				select {
				case <-started: // First token was sent; further output has no reader.
				case <-time.After(2 * time.Second):
					t.Fatal("stream did not reach its next frame")
				}
			}
			if mode == "cancel" || mode == "blocked consumer" {
				cancel()
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("stream did not close its body after cancellation")
			}
			var failures []error
			for chunk := range ch {
				assert.False(t, chunk.Done)
				if chunk.Error != nil {
					failures = append(failures, chunk.Error)
				}
			}
			require.Len(t, failures, 1)
			assert.ErrorIs(t, failures[0], wantCause)
			assert.ErrorIs(t, failures[0], wantKind)
			assert.NotContains(t, failures[0].Error(), lrChatKey)
			assert.Len(t, g.reqs, 1)
		})
	}
}

type closingStreamBody struct {
	io.Reader
	closed chan struct{}
}

type signaledStreamReader struct {
	io.Reader
	started chan struct{}
}

func (r *signaledStreamReader) Read(p []byte) (int, error) {
	if r.started != nil {
		close(r.started)
		r.started = nil
	}
	return r.Reader.Read(p)
}

func (b *closingStreamBody) Close() error {
	close(b.closed)
	return nil
}

// Removing the empty-frame guard must not turn a malformed frame followed by
// valid content into success. Error envelopes must beat model verification,
// including after some content has already reached the consumer.
func TestLunaRouteChat_StreamFailureBoundaries(t *testing.T) {
	const token = "data: {\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	const tail = "data: {\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"content\":\"must-not-escape\"}}]}\n\ndata: [DONE]\n\n"
	for _, tc := range []struct {
		name, frame, prefix string
		want                error
		retry               string
	}{
		{"empty frame", `{"model":"glm-5.3"}`, "", domprovider.ErrLunaRouteServiceFailure, ""},
		{"empty choices", `{"model":"glm-5.3","choices":[]}`, "", domprovider.ErrLunaRouteServiceFailure, ""},
		{"null usage", `{"model":"glm-5.3","choices":[],"usage":null}`, "", domprovider.ErrLunaRouteServiceFailure, ""},
		{"busy after content", `{"error":{"code":"CONCURRENT_REQUEST_LIMIT_EXCEEDED","message":"gateway-private Bearer ` + lrChatKey + `"}}`, token, domprovider.ErrLunaRouteBusy, "7"},
		{"refused after content", `{"code":"MODEL_UNPRICED","message":"gateway-private ` + lrChatKey + `"}`, token, domprovider.ErrLunaRouteRequestRefused, ""},
		{"unknown error after content", `{"error":{"message":"gateway-private ` + lrChatKey + `"}}`, token, domprovider.ErrLunaRouteCauseUnknown, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newChatGW()
			g.expected["stream"] = true
			g.status, g.body = 200, tc.prefix+"data: "+tc.frame+"\n\n"+tail
			g.header = http.Header{"Content-Type": {"text/event-stream"}, "Retry-After": {"007"}}
			b := lrChatBinding("", lrChatKey, nil)
			b.Pinned = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ch, err := chatProvider(t, g, b).StreamChat(ctx, lrChatRequest())
			require.NoError(t, err)
			var content string
			var failures []error
			for chunk := range ch {
				assert.False(t, chunk.Done, "a failure cannot complete successfully")
				if chunk.Error != nil {
					failures = append(failures, chunk.Error)
				} else {
					assert.Empty(t, failures, "no output after the terminal failure")
				}
				content += chunk.Token
			}
			require.NoError(t, ctx.Err(), "stream must close without waiting for caller cancellation")
			require.Len(t, failures, 1)
			require.ErrorIs(t, failures[0], tc.want)
			for _, other := range domprovider.LunaRouteGatewaySentinels() {
				if other != tc.want {
					assert.NotErrorIs(t, failures[0], other)
				}
			}
			var classified *domprovider.LunaRouteFailure
			require.ErrorAs(t, failures[0], &classified)
			assert.Equal(t, tc.retry, classified.RetryAfter)
			assert.NotContains(t, failures[0].Error(), lrChatKey)
			assert.NotContains(t, failures[0].Error(), "gateway-private")
			wantContent := ""
			if tc.prefix != "" {
				wantContent = "partial"
			}
			assert.Equal(t, wantContent, content)
			assert.Len(t, g.reqs, 1, "no retry after partial output")
		})
	}
}

// Reverting to Scanner's 64 KiB default must fail the large successful frame;
// crossing the explicit 1 MiB ceiling must produce one classified failure.
func TestLunaRouteChat_StreamFrameSizeBoundary(t *testing.T) {
	for _, size := range []int{70 * 1024, 1024 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			g := newChatGW()
			g.expected["stream"] = true
			g.status = 200
			content := strings.Repeat("z", size)
			g.body = "data: {\"model\":\"glm-5.3\",\"choices\":[{\"delta\":{\"content\":\"" + content + "\"}}]}\n\ndata: [DONE]\n\n"
			b := lrChatBinding("", lrChatKey, nil)
			b.Pinned = true
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ch, err := chatProvider(t, g, b).StreamChat(ctx, lrChatRequest())
			require.NoError(t, err)
			var chunks []engine.ChatChunk
			for chunk := range ch {
				chunks = append(chunks, chunk)
			}
			require.NoError(t, ctx.Err())
			if size < 1024*1024 {
				require.Len(t, chunks, 2)
				assert.Equal(t, content, chunks[0].Token)
				assert.NoError(t, chunks[0].Error)
				assert.True(t, chunks[1].Done)
				assert.NoError(t, chunks[1].Error)
			} else {
				require.Len(t, chunks, 1)
				assert.Empty(t, chunks[0].Token)
				assert.False(t, chunks[0].Done)
				require.ErrorIs(t, chunks[0].Error, domprovider.ErrLunaRouteServiceFailure)
				assert.NotContains(t, chunks[0].Error.Error(), lrChatKey)
			}
			assert.Len(t, g.reqs, 1)
		})
	}
}
