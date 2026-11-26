package main

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// Exercise the installed root, real WebSocket translator and real transfer
// client. A rejected scoped read must reach HTTP and return as a failed receipt.
func TestBuildAgent_MaintenancePullUsesConfiguredTransfer(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ws := filepath.Join(home, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0700))
	run := uuid.NewString()
	request := runnerproto.PullWorkspace{OperationID: "pull-root", RunID: run, WorkDir: filepath.Join(ws, "instances", run),
		HomeGeneration: 2, CheckpointVersion: 1, ManifestHash: strings.Repeat("c", 64), BaseRef: run + "@1:" + strings.Repeat("c", 64),
		FenceKey: "run:" + run, FenceOwner: "mac-test", FenceGeneration: 1, FenceToken: uuid.NewString(), PullToken: "scoped-root-token", PartBytes: 1024}
	received := make(chan runnerproto.WirePullReceipt, 1)
	headers := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/transfers/manifest" {
			headers <- r.Header.Clone()
			http.Error(w, "scoped read refused", http.StatusForbidden)
			return
		}
		if r.URL.Path != wsclient.ConnectPath {
			http.NotFound(w, r)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		send := func(kind string, payload any) {
			e, err := runnerproto.NewEnvelope(kind, payload)
			require.NoError(t, err)
			b, err := runnerproto.Encode(e)
			require.NoError(t, err)
			require.NoError(t, c.Write(r.Context(), websocket.MessageText, b))
		}
		send(runnerproto.KindChallenge, runnerproto.Challenge{KeyID: uuid.NewString(), Nonce: base64.StdEncoding.EncodeToString(make([]byte, 32))})
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			e, err := runnerproto.Decode(b)
			require.NoError(t, err)
			switch e.Kind {
			case runnerproto.KindHello:
				send(runnerproto.KindAdmitted, runnerproto.Admitted{RunnerID: "mac-test", HeartbeatSeconds: 1, ProtocolVersion: 1})
				send(runnerproto.KindPullWorkspace, request)
			case runnerproto.KindPullReceipt:
				var rc runnerproto.WirePullReceipt
				require.NoError(t, runnerproto.DecodePayload(e, &rc))
				received <- rc
				_, _, _ = c.Read(r.Context())
				return
			}
		}
	}))
	defer srv.Close()
	cfg := config.Config{ServerURL: srv.URL, RunnerKey: testRunnerKey, ConfigDir: home, WorkspaceRoot: ws,
		ScratchRoot: filepath.Join(home, "scratch"), SpoolDir: filepath.Join(home, "spool"), ClaudeBinary: writeFakeClaude(t, t.TempDir()),
		Heartbeat: time.Second, ProbeInterval: time.Hour, OutputBufferFrames: 64}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a, err := buildAgent(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case rc := <-received:
		require.False(t, rc.OK)
		require.Contains(t, rc.Error, "status 403")
		require.Equal(t, request.BaseRef, rc.BaseRef)
		require.Equal(t, request.FenceToken, rc.FenceToken)
	case <-ctx.Done():
		t.Fatal("no maintenance receipt reached the server")
	}
	select {
	case h := <-headers:
		require.Equal(t, "Runner "+testRunnerKey, h.Get("Authorization"))
		require.Equal(t, request.PullToken, h.Get("X-Arbitron-Transfer"))
	case <-ctx.Done():
		t.Fatal("the configured transfer client was never called")
	}
}
