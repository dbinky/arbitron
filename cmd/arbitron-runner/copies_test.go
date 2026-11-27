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
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/adapter/runneragent/copies"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"
)

// The real root must use the execution copy store for both hello inventory
// and deletion. A separate empty janitor cannot satisfy either sink.
func TestBuildAgent_CopyInventoryAndDeleteReachPersistedWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root := filepath.Join(home, "workspaces")
	require.NoError(t, os.MkdirAll(root, 0700))
	run := uuid.New()
	workDir := filepath.Join(root, "instances", run.String())
	require.NoError(t, os.MkdirAll(workDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(workDir, "kept.txt"), []byte("real local copy"), 0600))
	store, err := copies.NewFileStore(filepath.Join(home, "copies"))
	require.NoError(t, err)
	require.NoError(t, store.Put(context.Background(), runneragent.CopyState{RunID: run, WorkDir: workDir, HomeGeneration: 3}))
	hellos := make(chan runnerproto.Hello, 1)
	remove := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		send := func(kind string, payload any) error {
			e, err := runnerproto.NewEnvelope(kind, payload)
			if err != nil {
				return err
			}
			b, err := runnerproto.Encode(e)
			if err != nil {
				return err
			}
			return c.Write(r.Context(), websocket.MessageText, b)
		}
		if send(runnerproto.KindChallenge, runnerproto.Challenge{KeyID: uuid.NewString(), Nonce: base64.StdEncoding.EncodeToString(make([]byte, 32))}) != nil {
			return
		}
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			e, err := runnerproto.Decode(b)
			if err != nil {
				return
			}
			if e.Kind != runnerproto.KindHello {
				continue
			}
			var h runnerproto.Hello
			if runnerproto.DecodePayload(e, &h) != nil {
				return
			}
			select {
			case hellos <- h:
			case <-r.Context().Done():
				return
			}
			if send(runnerproto.KindAdmitted, runnerproto.Admitted{RunnerID: "mac-test", HeartbeatSeconds: 1, ProtocolVersion: 1}) != nil {
				return
			}
			select {
			case <-remove:
			case <-ctx.Done():
				return
			}
			if send(runnerproto.KindDeleteCopy, runnerproto.DeleteCopy{RunID: run.String(), HomeGeneration: 3, Reason: "workspace removed"}) != nil {
				return
			}
		}
	}))
	defer srv.Close()
	cfg := config.Config{ServerURL: srv.URL, RunnerKey: testRunnerKey, ConfigDir: home, WorkspaceRoot: root, ScratchRoot: filepath.Join(home, "scratch"), SpoolDir: filepath.Join(home, "spool"), ClaudeBinary: writeFakeClaude(t, t.TempDir()), Heartbeat: time.Second, ProbeInterval: time.Hour, OutputBufferFrames: 64}
	a, err := buildAgent(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case h := <-hellos:
		require.Equal(t, []runnerproto.WireCopy{{RunID: run.String(), HomeGeneration: 3}}, h.Copies)
	case <-ctx.Done():
		t.Fatal("no hello reached the server")
	}
	close(remove)
	require.Eventually(t, func() bool { _, err := os.Stat(workDir); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond, "delete_copy must remove the real workspace")
	// Reopen the production file store: deletion must survive process restart.
	reopened, err := copies.NewFileStore(filepath.Join(home, "copies"))
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, ok, err := reopened.Get(ctx, run); return err == nil && !ok }, 5*time.Second, 10*time.Millisecond, "copy inventory must no longer advertise a deleted directory")
}
