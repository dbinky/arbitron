package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

const testRunnerKey = "ark_Y29tcG9zaXRpb24tcm9vdC10ZXN0"

func TestVersionCommandPrintsStampedVersion(t *testing.T) {
	oldV, oldC, oldD := version, commit, date
	t.Cleanup(func() { version, commit, date = oldV, oldC, oldD })
	version, commit, date = "1.2.3", "abc1234", "2026-11-20T00:00:00Z"
	var out bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	require.NoError(t, root.Execute())
	assert.Equal(t, "arbitron-runner 1.2.3 (commit abc1234, built 2026-11-20T00:00:00Z)\n", out.String())
}

func TestRunFailsOnMissingConfig(t *testing.T) {
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"run", "--config", filepath.Join(t.TempDir(), "absent.yaml")})
	assert.Error(t, root.Execute())
}

// writeFakeClaude answers --version, auth status, and one chat turn.
func writeFakeClaude(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo '2.1.400 (Claude Code)'; exit 0;;\n" +
		"  auth) echo '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;;\n" +
		"esac\n" +
		"cat > /dev/null\n" +
		"m=$(echo \"$*\" | sed -n 's/.*--model \\([^ ]*\\).*/\\1/p')\n" +
		"echo '{\"type\":\"result\",\"is_error\":false,\"result\":\"ok\",\"session_id\":\"s1\",\"model\":\"'\"$m\"'\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1},\"modelUsage\":{\"'\"$m\"'\":{}}}'\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o700))
	return bin
}

type stubResult struct {
	rejected  string
	heartbeat runnerproto.Heartbeat
	auth      string
	hello     runnerproto.Hello
	result    runnerproto.Result
	sigOK     bool
}

// stubHub speaks runnerproto like the real endpoint: challenge, verify the
// machine signature, admit, dispatch one chat job, ack its result.
func stubHub(t *testing.T) (*httptest.Server, chan stubResult) {
	return stubHubWith(t, []string{"claude-sonnet-4-5"}, nil)
}

func stubHubWith(t *testing.T, models []string, mutate func(*runnerproto.WireChat)) (*httptest.Server, chan stubResult) {
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
		send(runnerproto.NewEnvelope(runnerproto.KindAdmitted, runnerproto.Admitted{RunnerID: "mac-test", HeartbeatSeconds: 1, ProtocolVersion: runnerproto.ProtocolVersion, ClaudeModels: models}))
		// Wait for verification to reach the server-side heartbeat sink before dispatch.
		for {
			e, ok = recv()
			if !ok {
				return
			}
			if e.Kind != runnerproto.KindHeartbeat {
				continue
			}
			require.NoError(t, runnerproto.DecodePayload(e, &out.heartbeat))
			ready := len(models) == 0
			for _, s := range out.heartbeat.Sources {
				if s.Kind == "anthropic_subscription" && len(s.Capabilities) > 0 {
					ready = true
				}
				if s.UnavailableReason == "harness_unsupported" || s.UnavailableReason == "harness_not_found" {
					ready = true
				}
			}
			if ready {
				break
			}
		}
		fp, err := runner.EndpointFingerprint(provider.ResolvedBinding{Provider: "claude-cli"})
		require.NoError(t, err)
		chat := &runnerproto.WireChat{Model: "claude-sonnet-4-5", Prompt: "say ok", CredentialKind: "anthropic_subscription", Provider: "claude-cli", EndpointFingerprint: fp}
		if mutate != nil {
			mutate(chat)
		}
		jobID := uuid.NewString()
		send(runnerproto.NewJobEnvelope(runnerproto.KindDispatch, jobID, 1, 0, runnerproto.Dispatch{Op: "chat", LeaseUntil: time.Now().Add(time.Minute), Chat: chat}))
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
				_, _, _ = c.Read(ctx) // hold the session until the runner closes
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, done
}

func TestBuildAgentDeliversChatResultToServer(t *testing.T) {
	srv, done := stubHub(t)
	// RR-07: resolve macOS /var -> /private/var so the root verifies.
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	ws := filepath.Join(home, "workspaces")
	scratch := filepath.Join(home, "scratch")
	spoolDir := filepath.Join(home, "spool")
	require.NoError(t, os.MkdirAll(ws, 0o700))
	claude := writeFakeClaude(t, t.TempDir())
	cfgPath := filepath.Join(home, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(
		"workspace_root: \""+ws+"\"\nscratch_root: \""+scratch+"\"\nspool_dir: \""+spoolDir+"\"\nheartbeat_seconds: 1\nclaude_binary: \""+claude+"\"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, config.EnvFileName), []byte(
		"ARBITRON_SERVER_URL="+srv.URL+"\nARBITRON_RUNNER_KEY="+testRunnerKey+"\n"), 0o600))

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	agent, err := buildAgent(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- agent.Run(ctx) }()

	var got stubResult
	select {
	case got = <-done:
	case <-ctx.Done():
		t.Fatal("no chat result reached the server")
	}

	assert.Equal(t, "Runner "+testRunnerKey, got.auth)
	assert.True(t, got.sigOK, "the machine key signed the server challenge")
	assert.Equal(t, runtime.GOOS, got.hello.OS)
	assert.Equal(t, ws, got.hello.WorkspaceRoot)
	assert.Equal(t, scratch, got.hello.ScratchRoot)
	assert.Equal(t, "2.1.400", got.hello.HarnessVersions["claude"])
	require.Len(t, got.hello.Sources, 1)
	assert.Equal(t, "anthropic_subscription", got.hello.Sources[0].Kind)
	assert.Equal(t, "ok", got.hello.Sources[0].Health.Auth, "auth status was probed before admission")
	assert.Empty(t, got.rejected)
	require.Len(t, got.heartbeat.Sources, 1)
	require.Len(t, got.heartbeat.Sources[0].Capabilities, 3, "chat, stream and P2 T13's native confirm capability")
	var ops []string
	for _, c := range got.heartbeat.Sources[0].Capabilities {
		ops = append(ops, c.Op)
		assert.Equal(t, []string{"claude-sonnet-4-5"}, c.Models)
	}
	assert.ElementsMatch(t, []string{"chat", "stream", "confirm"}, ops)
	assert.True(t, got.result.OK)
	require.NotNil(t, got.result.Chat)
	assert.Equal(t, "ok", got.result.Chat.Output)

	info, err := os.Stat(filepath.Join(home, config.MachineKeyFileName))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	assert.Empty(t, entries, "the job's scratch dir is gone")
	assert.Eventually(t, func() bool {
		logs, _ := filepath.Glob(filepath.Join(spoolDir, "*.log"))
		return len(logs) == 0
	}, 5*time.Second, 50*time.Millisecond, "the acked result leaves the spool")

	cancel()
	select {
	case err := <-runDone:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not stop")
	}
}

// RR-07: a root reached through a symlinked component is never advertised;
// the hub sees "" and marks the runner chat_only.
func TestBuildAgentAdvertisesNoRootWhenItHasASymlinkedComponent(t *testing.T) {
	srv, done := stubHub(t)
	home, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target := filepath.Join(home, "real")
	require.NoError(t, os.MkdirAll(filepath.Join(target, "workspaces"), 0o700))
	require.NoError(t, os.Symlink(target, filepath.Join(home, "link")))
	ws := filepath.Join(home, "link", "workspaces")
	scratch := filepath.Join(home, "scratch")
	spoolDir := filepath.Join(home, "spool")
	claude := writeFakeClaude(t, t.TempDir())
	cfgPath := filepath.Join(home, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(
		"workspace_root: \""+ws+"\"\nscratch_root: \""+scratch+"\"\nspool_dir: \""+spoolDir+"\"\nheartbeat_seconds: 1\nclaude_binary: \""+claude+"\"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(home, config.EnvFileName), []byte(
		"ARBITRON_SERVER_URL="+srv.URL+"\nARBITRON_RUNNER_KEY="+testRunnerKey+"\n"), 0o600))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	agent, err := buildAgent(ctx, cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	require.NoError(t, err)
	go func() { _ = agent.Run(ctx) }()
	select {
	case got := <-done:
		assert.Empty(t, got.hello.WorkspaceRoot, "an unverified root must not reach hello")
		assert.Equal(t, scratch, got.hello.ScratchRoot)
	case <-ctx.Done():
		t.Fatal("no hello/result reached the server")
	}
}
