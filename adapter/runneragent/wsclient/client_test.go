package wsclient

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const runnerKey = "ark_dGVzdC1ydW5uZXIta2V5"

type seen struct {
	auth, extra, path, query string
	hello                    runnerproto.Hello
}

func stubServer(t *testing.T, serve func(ctx context.Context, c *websocket.Conn)) (*httptest.Server, chan seen) {
	t.Helper()
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := seen{auth: r.Header.Get("Authorization"), extra: r.Header.Get("Cf-Access-Client-Id"), path: r.URL.Path, query: r.URL.RawQuery}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		got <- s
		serve(r.Context(), c)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestDialSendsKeyOnlyInAuthorizationHeader(t *testing.T) {
	hellos := make(chan runnerproto.Hello, 1)
	srv, got := stubServer(t, func(ctx context.Context, c *websocket.Conn) {
		nonce := base64.StdEncoding.EncodeToString(make([]byte, 32))
		env, _ := runnerproto.NewEnvelope(runnerproto.KindChallenge, runnerproto.Challenge{KeyID: tKey, Nonce: nonce})
		b, _ := runnerproto.Encode(env)
		_ = c.Write(ctx, websocket.MessageText, b)
		_, msg, err := c.Read(ctx)
		if err != nil {
			return
		}
		e, err := runnerproto.Decode(msg)
		if err != nil {
			return
		}
		var h runnerproto.Hello
		if runnerproto.DecodePayload(e, &h) == nil {
			hellos <- h
		}
	})
	cl, err := New(Options{ServerURL: srv.URL, RunnerKey: runnerKey, Headers: http.Header{"Cf-Access-Client-Id": {"id1"}}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := cl.Dial(ctx)
	require.NoError(t, err)
	defer sess.Close()

	s := <-got
	assert.Equal(t, "Runner "+runnerKey, s.auth)
	assert.Equal(t, "id1", s.extra)
	assert.Equal(t, ConnectPath, s.path)
	assert.Empty(t, s.query)
	assert.NotContains(t, s.path, runnerKey)

	in, err := sess.Recv(ctx)
	require.NoError(t, err)
	assert.Equal(t, runneragent.KindChallenge, in.Kind)
	assert.Equal(t, make([]byte, 32), in.Challenge.Nonce)

	require.NoError(t, sess.Send(ctx, runneragent.Outbound{Kind: runneragent.KindHello, Hello: &runneragent.Hello{OS: "darwin", ProtocolVersion: 1, RunnerVersion: "1.0.0"}}))
	select {
	case h := <-hellos:
		assert.Equal(t, "darwin", h.OS)
	case <-ctx.Done():
		t.Fatal("server never received hello")
	}
}

func TestDialUnauthorizedIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	cl, err := New(Options{ServerURL: srv.URL, RunnerKey: runnerKey})
	require.NoError(t, err)
	_, err = cl.Dial(context.Background())
	require.Error(t, err)
	assert.True(t, errors.Is(err, runneragent.ErrRefused))
	assert.NotContains(t, err.Error(), runnerKey)
}

func TestSessionRecvRejectsMalformedFrame(t *testing.T) {
	srv, _ := stubServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"v":1,"kind":"dispatch","jobId":"nope"}`))
		_, _, _ = c.Read(ctx)
	})
	cl, err := New(Options{ServerURL: srv.URL, RunnerKey: runnerKey})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := cl.Dial(ctx)
	require.NoError(t, err)
	defer sess.Close()
	_, err = sess.Recv(ctx)
	assert.ErrorIs(t, err, runnerproto.ErrMalformed)
}

func TestSessionRecvRejectsBinaryAndOversizeFrames(t *testing.T) {
	srv, _ := stubServer(t, func(ctx context.Context, c *websocket.Conn) {
		_ = c.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", runnerproto.MaxFrameBytes+1)))
		_, _, _ = c.Read(ctx)
	})
	cl, err := New(Options{ServerURL: srv.URL, RunnerKey: runnerKey})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := cl.Dial(ctx)
	require.NoError(t, err)
	defer sess.Close()
	_, err = sess.Recv(ctx)
	assert.Error(t, err, "frames over 1 MiB are refused by the read limit")
}

func TestNewValidatesOptions(t *testing.T) {
	_, err := New(Options{ServerURL: "", RunnerKey: runnerKey})
	assert.Error(t, err)
	_, err = New(Options{ServerURL: "https://a.example.com", RunnerKey: "sk-nope"})
	assert.Error(t, err)
	_, err = New(Options{ServerURL: "https://a.example.com", RunnerKey: runnerKey, Headers: http.Header{"Authorization": {"Bearer x"}}})
	assert.Error(t, err, "extra headers can never replace the runner key")
}
