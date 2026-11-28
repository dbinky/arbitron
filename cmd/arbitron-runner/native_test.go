package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/runneragent/config"
	"github.com/arbitron/arbitron/adapter/runneragent/wsclient"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeCodex logs every call and answers version, auth and rateLimits RPCs.
func writeFakeCodex(t *testing.T, dir, log string) string {
	t.Helper()
	bin := filepath.Join(dir, "codex")
	script := "#!/bin/sh\n" +
		"echo \"$0 $*\" >> '" + log + "'\n" +
		"case \"$1\" in\n" +
		"  --version) echo 'codex-cli 0.171.0'; exit 0;;\n" +
		"  login) echo 'Logged in using ChatGPT'; exit 0;;\n" +
		"  app-server) while IFS= read -r l; do case \"$l\" in *'\"id\":1'*) " +
		"echo '{\"id\":1,\"result\":{\"ordinaryUsageAllowed\":true,\"rateLimits\":{\"limitId\":\"codex\",\"primary\":{\"usedPercent\":42,\"windowDurationMins\":300,\"resetsAt\":1900000000}}}}';; " +
		"*'\"id\":0'*) echo '{\"id\":0,\"result\":{}}';; esac; done; exit 0;;\n" +
		"esac\nexit 2\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

type nativeRunnerDir struct{ home, cfgPath, codexHome, codexLog, codexBin string }

func newNativeRunnerDir(t *testing.T, serverURL, extraYAML string) nativeRunnerDir {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ws := filepath.Join(home, "workspaces")
	require.NoError(t, os.MkdirAll(ws, 0o700))
	d := nativeRunnerDir{home: home, codexHome: filepath.Join(home, "codex-home"), codexLog: filepath.Join(home, "codex.log")}
	require.NoError(t, os.MkdirAll(d.codexHome, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(d.codexHome, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-6.1-sol"}]}`), 0o600))
	d.codexBin = writeFakeCodex(t, t.TempDir(), d.codexLog)
	d.cfgPath = filepath.Join(home, "config.yaml")
	require.NoError(t, os.WriteFile(d.cfgPath, []byte("workspace_root: \""+ws+"\"\nheartbeat_seconds: 1\nprobe_interval_seconds: 5\n"+
		"claude_binary: \""+filepath.Join(home, "no-claude")+"\"\ncodex_binary: \""+d.codexBin+"\"\n"+extraYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, config.EnvFileName), []byte("ARBITRON_SERVER_URL="+serverURL+"\nARBITRON_RUNNER_KEY="+testRunnerKey+"\n"), 0o600))
	t.Setenv("CODEX_HOME", d.codexHome)
	return d
}

func TestBuildAgent_MissingRulesProberOrInventoryCannotYieldUsableSource(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	t.Run("malformed runner-local capability rules refuse startup", func(t *testing.T) {
		srv, _ := stubHelloHub(t)
		d := newNativeRunnerDir(t, srv.URL, "")
		require.NoError(t, os.WriteFile(filepath.Join(d.home, "capabilities.yaml"), []byte("backends:\n  codex:\n    effort_levels: [ludicrous]\n"), 0o600))
		cfg, err := config.Load(d.cfgPath)
		require.NoError(t, err)
		_, err = buildAgent(context.Background(), cfg, logger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "capability rules")
	})
	t.Run("unparseable runner-local capability rules refuse startup", func(t *testing.T) {
		srv, _ := stubHelloHub(t)
		d := newNativeRunnerDir(t, srv.URL, "")
		path := filepath.Join(d.home, "capabilities.yaml")
		require.NoError(t, os.WriteFile(path, []byte("backends: [unclosed\n"), 0o600))
		cfg, err := config.Load(d.cfgPath)
		require.NoError(t, err)
		_, err = buildAgent(context.Background(), cfg, logger)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "capability rules")
		assert.Contains(t, err.Error(), path, "the operator sees which file is broken")
	})
	t.Run("missing runner-local capability rules use the embedded rules", func(t *testing.T) {
		srv, done := stubHelloHub(t)
		d := newNativeRunnerDir(t, srv.URL, "")
		_, statErr := os.Stat(filepath.Join(d.home, "capabilities.yaml"))
		require.True(t, os.IsNotExist(statErr), "precondition: no override file")
		cfg, err := config.Load(d.cfgPath)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		agent, err := buildAgent(ctx, cfg, logger)
		require.NoError(t, err)
		go func() { _ = agent.Run(ctx) }()
		var got stubResult
		select {
		case got = <-done:
		case <-ctx.Done():
			t.Fatal("no hello reached the server")
		}
		var efforts []string
		for _, s := range got.hello.Sources {
			if s.Kind == "openai_subscription" && len(s.Capabilities) > 0 {
				efforts = s.Capabilities[0].Efforts
				assert.Equal(t, "unrestricted", s.Capabilities[0].ToolPolicy, "SINK: codex tool posture on the wire")
			}
		}
		assert.Equal(t, []string{"default", "low", "medium", "high", "xhigh"}, efforts, "SINK: embedded codex effort levels reached hello")
	})
	t.Run("hello advertises the probed codex source with real evidence", func(t *testing.T) {
		srv, done := stubHelloHub(t)
		d := newNativeRunnerDir(t, srv.URL, "")
		cfg, err := config.Load(d.cfgPath)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		agent, err := buildAgent(ctx, cfg, logger)
		require.NoError(t, err)
		go func() { _ = agent.Run(ctx) }()
		var got stubResult
		select {
		case got = <-done:
		case <-ctx.Done():
			t.Fatal("no hello reached the server")
		}
		found := false
		for _, s := range got.hello.Sources {
			if s.Kind != "openai_subscription" {
				continue
			}
			found = true
			assert.Empty(t, s.UnavailableReason)
			assert.Equal(t, []string{"gpt-6.1-sol"}, s.Models)
			require.NotEmpty(t, s.Capabilities)
			assert.Equal(t, "agent", s.Capabilities[0].Op)
			assert.Equal(t, "codex", s.Capabilities[0].Backend)
		}
		require.True(t, found, "SINK: the codex source reached hello")
		assert.Equal(t, "0.171.0", got.hello.HarnessVersions["codex"])
		assert.Contains(t, got.hello.Capabilities, "agent")
		b, err := os.ReadFile(d.codexLog)
		require.NoError(t, err)
		assert.Contains(t, string(b), d.codexBin+" --version", "discovery used the configured codex")
	})
	t.Run("empty account model list is a visible refusal, not capacity", func(t *testing.T) {
		srv, done := stubHelloHub(t)
		d := newNativeRunnerDir(t, srv.URL, "")
		require.NoError(t, os.WriteFile(filepath.Join(d.codexHome, "models_cache.json"), []byte(`{"models":[]}`), 0o600))
		cfg, err := config.Load(d.cfgPath)
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		agent, err := buildAgent(ctx, cfg, logger)
		require.NoError(t, err)
		go func() { _ = agent.Run(ctx) }()
		var got stubResult
		select {
		case got = <-done:
		case <-ctx.Done():
			t.Fatal("no hello reached the server")
		}
		found := false
		for _, s := range got.hello.Sources {
			if s.Kind == "openai_subscription" {
				found = true
				assert.True(t, strings.Contains(s.UnavailableReason, "models_cache.json"), s.UnavailableReason)
				assert.Empty(t, s.Capabilities)
				assert.Empty(t, s.Models)
			}
		}
		require.True(t, found, "an installed codex stays visible with its reason")
	})
}

// stubHelloHub performs the real machine-signature handshake and captures hello.
func stubHelloHub(t *testing.T) (*httptest.Server, chan stubResult) {
	t.Helper()
	return stubNativeHub(t, false)
}

func stubNativeHub(t *testing.T, confirm bool) (*httptest.Server, chan stubResult) {
	t.Helper()
	done := make(chan stubResult, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != wsclient.ConnectPath {
			http.NotFound(w, r)
			return
		}
		out := stubResult{auth: r.Header.Get("Authorization")}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		ctx := r.Context()
		send := func(e runnerproto.Envelope, err error) {
			require.NoError(t, err)
			b, err := runnerproto.Encode(e)
			require.NoError(t, err)
			_ = c.Write(ctx, websocket.MessageText, b)
		}
		recv := func() (runnerproto.Envelope, bool) {
			_, b, err := c.Read(ctx)
			if err != nil {
				return runnerproto.Envelope{}, false
			}
			e, err := runnerproto.Decode(b)
			return e, err == nil
		}
		keyID := uuid.NewString()
		nonce := make([]byte, 32)
		_, _ = rand.Read(nonce)
		send(runnerproto.NewEnvelope(runnerproto.KindChallenge, runnerproto.Challenge{KeyID: keyID, Nonce: base64.StdEncoding.EncodeToString(nonce)}))
		e, ok := recv()
		if !ok || e.Kind != runnerproto.KindChallengeResponse {
			return
		}
		var cr runnerproto.ChallengeResponse
		require.NoError(t, runnerproto.DecodePayload(e, &cr))
		pub, sig, err := cr.Decoded()
		require.NoError(t, err)
		out.sigOK = ed25519.Verify(pub, runneragent.ChallengeMessage(keyID, nonce), sig)
		if e, ok = recv(); !ok || e.Kind != runnerproto.KindHello {
			return
		}
		require.NoError(t, runnerproto.DecodePayload(e, &out.hello))
		send(runnerproto.NewEnvelope(runnerproto.KindAdmitted, runnerproto.Admitted{RunnerID: "mac-test", HeartbeatSeconds: 1, ProtocolVersion: runnerproto.ProtocolVersion}))
		if confirm {
			fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli", Backend: "codex"})
			require.NoError(t, err)
			jobID := uuid.NewString()
			send(runnerproto.NewJobEnvelope(runnerproto.KindDispatch, jobID, 1, 0, runnerproto.Dispatch{
				Op: "confirm", LeaseUntil: time.Now().Add(time.Minute), Confirmation: &runnerproto.WireConfirmation{
					Model: "gpt-6.1-sol", Backend: "codex", Provider: "claude-cli", EndpointFingerprint: fp,
				},
			}))
			accepted := false
			for {
				e, ok := recv()
				if !ok {
					return
				}
				switch e.Kind {
				case runnerproto.KindAccept:
					accepted = e.JobID == jobID && e.Generation == 1
				case runnerproto.KindReject:
					out.rejected = string(e.Payload)
					done <- out
					return
				case runnerproto.KindResult:
					require.True(t, accepted, "result before accept")
					require.NoError(t, runnerproto.DecodePayload(e, &out.result))
					send(runnerproto.NewJobEnvelope(runnerproto.KindAck, jobID, 1, 0, runnerproto.Ack{Result: true}))
					done <- out
					_, _ = recv()
					return
				}
			}
		}
		done <- out
		for {
			if _, ok := recv(); !ok {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, done
}
