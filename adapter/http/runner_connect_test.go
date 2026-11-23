// adapter/http/runner_connect_test.go
package http

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nhooyr.io/websocket"

	"github.com/arbitron/arbitron/adapter/runner/hubtest"
	"github.com/arbitron/arbitron/adapter/runnerproto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
	"github.com/arbitron/arbitron/domain/runner"
)

type connectFixture struct {
	pool      *hubtest.Pool
	hub       *runner.Hub
	keys      *runner.KeyService
	keyRepo   *adaptsqlite.RunnerKeyRepo
	srv       *httptest.Server
	logs      *syncBuffer
	plaintext string
	key       runner.RunnerKey
	admin     context.Context
}

func newConnectFixture(t *testing.T) *connectFixture {
	t.Helper()
	p := hubtest.NewPool(t)
	hub := p.NewHub(hubtest.HubConfig())
	repo := adaptsqlite.NewRunnerKeyRepo(p.DB)
	keys := runner.NewKeyService(runner.KeyServiceDeps{Store: repo, Registry: p.Registry, Audit: &keyAuditSpy{},
		Revoker: hub, Clock: p.Clock.Now, DefaultStepLimit: 4})
	admin := identity.WithIdentity(context.Background(), identity.Identity{UserID: uuid.New(), Username: "root", Role: identity.RoleAdmin})
	plain, k, err := keys.Create(admin, "mac-a")
	require.NoError(t, err)
	logs := &syncBuffer{}
	handler := NewRunnerConnectHandler(keys, hub, RunnerConnectConfig{HeartbeatSeconds: 5, HandshakeTimeout: 2 * time.Second,
		IdleTimeout: 10 * time.Second, WriteTimeout: 2 * time.Second,
		Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
	srv := httptest.NewServer(NewRouterFromDeps(RouterDeps{Config: &config.Config{}, Auth: auth, RunnerConnect: handler}))
	t.Cleanup(srv.Close)
	return &connectFixture{pool: p, hub: hub, keys: keys, keyRepo: repo, srv: srv, logs: logs, plaintext: plain, key: k, admin: admin}
}

func (f *connectFixture) wsURL() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/api/v1/runners/connect"
}

func wireHello() runnerproto.Hello {
	return runnerproto.Hello{OS: "darwin", Arch: "arm64", ProtocolVersion: 1, RunnerVersion: "0.2.0",
		HarnessVersions: map[string]string{"claude": "2.1.30"}, WorkspaceRoot: hubtest.ServerRoot,
		ScratchRoot: "/Users/op/.arbitron-runner/scratch",
		Sources: []runnerproto.WireSource{{Kind: "anthropic_subscription", Families: []string{"anthropic"},
			Health: runnerproto.WireHealth{Auth: "ok", Capacity: "available"}}}}
}

func newMachine(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	return priv
}

type wireRunner struct {
	t    *testing.T
	c    *websocket.Conn
	priv ed25519.PrivateKey
}

func (f *connectFixture) dial(t *testing.T, plaintext string, priv ed25519.PrivateKey) *wireRunner {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.wsURL(), &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization":       {"Runner " + plaintext},
		"Cf-Access-Client-Id": {"client.access"},
	}})
	require.NoError(t, err)
	c.SetReadLimit(runnerproto.MaxFrameBytes)
	t.Cleanup(func() { _ = c.CloseNow() })
	return &wireRunner{t: t, c: c, priv: priv}
}

func (f *connectFixture) connect(t *testing.T, priv ed25519.PrivateKey, hello runnerproto.Hello) (*wireRunner, runnerproto.Admitted) {
	t.Helper()
	w := f.dial(t, f.plaintext, priv)
	w.prove()
	w.sendPayload(runnerproto.KindHello, &hello)
	env := w.read()
	require.Equal(t, runnerproto.KindAdmitted, env.Kind)
	var adm runnerproto.Admitted
	require.NoError(t, runnerproto.DecodePayload(env, &adm))
	return w, adm
}

func (w *wireRunner) prove() {
	w.t.Helper()
	env := w.read()
	require.Equal(w.t, runnerproto.KindChallenge, env.Kind)
	var ch runnerproto.Challenge
	require.NoError(w.t, runnerproto.DecodePayload(env, &ch))
	nonce, err := base64.StdEncoding.DecodeString(ch.Nonce)
	require.NoError(w.t, err)
	sig := ed25519.Sign(w.priv, runner.ChallengeMessage(uuid.MustParse(ch.KeyID), nonce))
	w.sendPayload(runnerproto.KindChallengeResponse, &runnerproto.ChallengeResponse{
		PublicKey: base64.StdEncoding.EncodeToString(w.priv.Public().(ed25519.PublicKey)),
		Signature: base64.StdEncoding.EncodeToString(sig)})
}

func (w *wireRunner) send(e runnerproto.Envelope) {
	w.t.Helper()
	b, err := runnerproto.Encode(e)
	require.NoError(w.t, err)
	w.writeRaw(b)
}

func (w *wireRunner) writeRaw(b []byte) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(w.t, w.c.Write(ctx, websocket.MessageText, b))
}

func (w *wireRunner) sendPayload(kind string, p any) {
	w.t.Helper()
	e, err := runnerproto.NewEnvelope(kind, p)
	require.NoError(w.t, err)
	w.send(e)
}

func (w *wireRunner) sendJob(kind string, jobID uuid.UUID, gen int64, seq uint64, p any) {
	w.t.Helper()
	e, err := runnerproto.NewJobEnvelope(kind, jobID.String(), gen, seq, p)
	require.NoError(w.t, err)
	w.send(e)
}

func (w *wireRunner) readRaw() []byte {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := w.c.Read(ctx)
	require.NoError(w.t, err)
	return b
}

func (w *wireRunner) read() runnerproto.Envelope {
	w.t.Helper()
	e, err := runnerproto.Decode(w.readRaw())
	require.NoError(w.t, err)
	return e
}

// closeStatus drains frames until the server closes and returns the close code.
func (w *wireRunner) closeStatus() (websocket.StatusCode, []runnerproto.Envelope) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var before []runnerproto.Envelope
	for {
		_, b, err := w.c.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err), before
		}
		if e, derr := runnerproto.Decode(b); derr == nil {
			before = append(before, e)
		}
	}
}

func TestRunnerConnect_FirstClaimBindsMachineAndAdmits(t *testing.T) {
	f := newConnectFixture(t)
	priv := newMachine(t)
	w, adm := f.connect(t, priv, wireHello())
	assert.Equal(t, runnerproto.Admitted{RunnerID: "mac-a", HeartbeatSeconds: 5, ProtocolVersion: 1}, adm)

	k, err := f.keyRepo.GetByHash(context.Background(), runner.HashRunnerKey(f.plaintext))
	require.NoError(t, err)
	assert.Equal(t, []byte(priv.Public().(ed25519.PublicKey)), k.MachinePublicKey, "first connection binds the machine")
	r, srcs, err := f.pool.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.LivenessOnline, r.Liveness)
	assert.Len(t, srcs, 1)

	cpu := 12.5
	w.sendPayload(runnerproto.KindHeartbeat, &runnerproto.Heartbeat{Telemetry: runnerproto.WireTelemetry{SampledAt: f.pool.Clock.Now(), CPUPercent: &cpu}})
	require.Eventually(t, func() bool {
		tel, ok := f.hub.Latest()["mac-a"]
		return ok && tel.CPUPercent != nil && *tel.CPUPercent == 12.5
	}, 2*time.Second, 5*time.Millisecond, "heartbeats reach the hub's telemetry window")
}

func TestRunnerConnect_CopiedKeyFromAnotherMachineRefused(t *testing.T) {
	f := newConnectFixture(t)
	first := newMachine(t)
	w1, _ := f.connect(t, first, wireHello())
	require.NoError(t, w1.c.Close(websocket.StatusNormalClosure, "bye"))

	w2 := f.dial(t, f.plaintext, newMachine(t))
	w2.prove()
	code, frames := w2.closeStatus()
	assert.Equal(t, websocket.StatusCode(runner.CloseWrongMachine), code)
	require.Len(t, frames, 1)
	assert.Equal(t, runnerproto.KindRefused, frames[0].Kind)
	var ref runnerproto.Refused
	require.NoError(t, runnerproto.DecodePayload(frames[0], &ref))
	assert.Equal(t, "wrong_machine", ref.Code)

	k, err := f.keyRepo.GetByHash(context.Background(), runner.HashRunnerKey(f.plaintext))
	require.NoError(t, err)
	assert.Equal(t, []byte(first.Public().(ed25519.PublicKey)), k.MachinePublicKey, "binding unchanged")
}

func TestRunnerConnect_HTTPRefusals(t *testing.T) {
	f := newConnectFixture(t)
	get := func(target, auth string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, f.srv.URL+target, nil)
		require.NoError(t, err)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	const path = "/api/v1/runners/connect"
	assert.Equal(t, http.StatusUnauthorized, get(path, "").StatusCode)
	assert.Equal(t, http.StatusUnauthorized, get(path, "Bearer "+f.plaintext).StatusCode)
	assert.Equal(t, http.StatusUnauthorized, get(path, "Runner ark_bogus").StatusCode)
	assert.Equal(t, http.StatusUnauthorized, get(path+"?key="+f.plaintext, "Runner "+f.plaintext).StatusCode)

	require.NoError(t, f.keys.Revoke(f.admin, f.key.ID, runner.RevokeAck{Acknowledged: true}))
	resp := get(path, "Runner "+f.plaintext)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	var body ErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	assert.Equal(t, "RUNNER_KEY_REVOKED", body.Code)
}

func TestRunnerConnect_MalformedFramesClose(t *testing.T) {
	f := newConnectFixture(t)
	t.Run("garbage instead of challenge response", func(t *testing.T) {
		w := f.dial(t, f.plaintext, newMachine(t))
		w.read() // challenge
		w.writeRaw([]byte("not json"))
		code, _ := w.closeStatus()
		assert.Equal(t, websocket.StatusCode(runner.CloseMalformed), code)
	})
	t.Run("heartbeat instead of hello", func(t *testing.T) {
		w := f.dial(t, f.plaintext, newMachine(t))
		w.prove()
		w.sendPayload(runnerproto.KindHeartbeat, &runnerproto.Heartbeat{})
		code, _ := w.closeStatus()
		assert.Equal(t, websocket.StatusCode(runner.CloseMalformed), code)
	})
}

func TestRunnerConnect_MalformedAfterAdmissionCloses(t *testing.T) {
	f := newConnectFixture(t)
	w, _ := f.connect(t, newMachine(t), wireHello())
	e, err := runnerproto.NewJobEnvelope(runnerproto.KindOutput, uuid.NewString(), 1, 2, &runnerproto.Output{Seq: 3, Token: "x"})
	require.NoError(t, err)
	w.send(e)
	code, _ := w.closeStatus()
	assert.Equal(t, websocket.StatusCode(runner.CloseMalformed), code)
}

func TestRunnerConnect_FutureProtocolRefusedAndVisible(t *testing.T) {
	f := newConnectFixture(t)
	w := f.dial(t, f.plaintext, newMachine(t))
	w.prove()
	hello := wireHello()
	hello.ProtocolVersion = 2
	payload, err := json.Marshal(hello)
	require.NoError(t, err)
	w.send(runnerproto.Envelope{V: 2, Kind: runnerproto.KindHello, Payload: payload})
	code, frames := w.closeStatus()
	assert.Equal(t, websocket.StatusCode(runner.CloseIncompatible), code)
	require.Len(t, frames, 1)
	var ref runnerproto.Refused
	require.NoError(t, runnerproto.DecodePayload(frames[0], &ref))
	assert.Equal(t, "incompatible", ref.Code)
	assert.Contains(t, ref.Reason, "protocol version 2")
	assert.Equal(t, 1, ref.ProtocolMin)
	assert.Equal(t, 1, ref.ProtocolMax)

	r, _, err := f.pool.Registry.GetRunner(context.Background(), "mac-a")
	require.NoError(t, err)
	assert.Equal(t, runner.CompatIncompatible, r.Compat.State, "visible in the pool as incompatible")
	assert.Contains(t, f.pool.EventKinds(t), "runner.incompatible")
}

func TestRunnerConnect_DispatchRoundTripCarriesNoSecrets(t *testing.T) {
	f := newConnectFixture(t)
	w, _ := f.connect(t, newMachine(t), wireHello())
	a := f.pool.ReserveJob(t, f.pool.SeedRun(t), "mac-a", runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""), runner.OpChat)
	job := hubtest.ChatJob("claude-opus-5-5", runner.OpChat)
	job.Binding.BaseURL = "https://secret-gateway.example"
	job.Binding.RevealKey = func(context.Context) (string, error) { return "lr_supersecret", nil }
	job.Chat.WorkDir = "/srv/arbitron/workspaces/run-9"
	type res struct {
		out runner.StepOutput
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := f.hub.Run(context.Background(), a, job)
		done <- res{out, err}
	}()

	raw := w.readRaw()
	for _, secret := range []string{"lr_supersecret", "secret-gateway", "run-9", f.plaintext} {
		assert.NotContains(t, string(raw), secret)
	}
	env, err := runnerproto.Decode(raw)
	require.NoError(t, err)
	require.Equal(t, runnerproto.KindDispatch, env.Kind)
	assert.Equal(t, a.Fence.JobID.String(), env.JobID)
	assert.Equal(t, a.Fence.Generation, env.Generation)
	var d runnerproto.Dispatch
	require.NoError(t, runnerproto.DecodePayload(env, &d))
	assert.Equal(t, "chat", d.Op)
	assert.Equal(t, "claude-opus-5-5", d.Chat.Model)
	assert.Equal(t, "anthropic_subscription", d.Chat.CredentialKind)

	w.sendJob(runnerproto.KindAccept, a.Fence.JobID, a.Fence.Generation, 0, nil)
	w.sendJob(runnerproto.KindResult, a.Fence.JobID, a.Fence.Generation, 0, &runnerproto.Result{OK: true,
		Chat: &runnerproto.WireChatResult{Output: "hi from the mac", Model: "claude-opus-5-5", TokensIn: 3, TokensOut: 4}})
	ack := w.read()
	require.Equal(t, runnerproto.KindAck, ack.Kind)
	var ap runnerproto.Ack
	require.NoError(t, runnerproto.DecodePayload(ack, &ap))
	assert.True(t, ap.Result)

	select {
	case r := <-done:
		require.NoError(t, r.err)
		assert.Equal(t, "hi from the mac", r.out.Chat.Output)
		assert.Equal(t, 4, r.out.Chat.TokensOut)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
}

func TestRunnerConnect_ReconnectReplaysOutputWithoutGapsOrDuplicates(t *testing.T) {
	f := newConnectFixture(t)
	priv := newMachine(t)
	w1, _ := f.connect(t, priv, wireHello())
	a := f.pool.ReserveJob(t, f.pool.SeedRun(t), "mac-a", runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""), runner.OpStream)
	type res struct {
		out runner.StepOutput
		err error
	}
	done := make(chan res, 1)
	go func() {
		out, err := f.hub.Run(context.Background(), a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
		done <- res{out, err}
	}()
	require.Equal(t, runnerproto.KindDispatch, w1.read().Kind)
	w1.sendJob(runnerproto.KindAccept, a.Fence.JobID, a.Fence.Generation, 0, nil)
	var r res
	select {
	case r = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream Run did not return on accept")
	}
	require.NoError(t, r.err)
	tokens := make(chan []string, 1)
	go func() {
		var all []string
		for c := range r.out.Stream {
			if !c.Done {
				all = append(all, c.Token)
			}
		}
		tokens <- all
	}()
	w1.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 1, &runnerproto.Output{Seq: 1, Token: "a"})
	w1.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 2, &runnerproto.Output{Seq: 2, Token: "b"})
	require.Equal(t, runnerproto.KindAck, w1.read().Kind)
	require.Equal(t, runnerproto.KindAck, w1.read().Kind)
	require.NoError(t, w1.c.CloseNow(), "socket drops abruptly")

	hello := wireHello()
	hello.Held = []runnerproto.HeldJob{{JobID: a.Fence.JobID.String(), Generation: a.Fence.Generation, State: "running", LastOutputSeq: 4}}
	w2, _ := f.connect(t, priv, hello)
	resume := w2.read()
	require.Equal(t, runnerproto.KindAck, resume.Kind)
	var ap runnerproto.Ack
	require.NoError(t, runnerproto.DecodePayload(resume, &ap))
	assert.Equal(t, uint64(2), ap.Seq, "server tells the runner to resend from 3")

	w2.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 2, &runnerproto.Output{Seq: 2, Token: "b"})
	w2.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 3, &runnerproto.Output{Seq: 3, Token: "c"})
	w2.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 4, &runnerproto.Output{Seq: 4, Token: "d"})
	w2.sendJob(runnerproto.KindResult, a.Fence.JobID, a.Fence.Generation, 0, &runnerproto.Result{OK: true,
		Chat: &runnerproto.WireChatResult{Output: "abcd", TokensOut: 4}})
	select {
	case got := <-tokens:
		assert.Equal(t, []string{"a", "b", "c", "d"}, got)
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not complete")
	}
}

func TestRunnerConnect_NoSecretInLogs(t *testing.T) {
	f := newConnectFixture(t)
	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/runners/connect?key="+f.plaintext, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	priv := newMachine(t)
	w, _ := f.connect(t, priv, wireHello())
	require.NoError(t, w.c.Close(websocket.StatusNormalClosure, "bye"))
	w2 := f.dial(t, f.plaintext, newMachine(t))
	w2.prove()
	w2.closeStatus()

	logs := f.logs.String()
	assert.Contains(t, logs, "mac-a", "connections are logged by runner id")
	assert.NotContains(t, logs, f.plaintext)
	assert.NotContains(t, logs, runner.HashRunnerKey(f.plaintext))
	assert.NotContains(t, logs, f.plaintext[:12], "not even the key prefix")
}

func TestRunnerConnect_MountedOutsideUserAuth(t *testing.T) {
	f := newConnectFixture(t)
	f.connect(t, newMachine(t), wireHello()) // org mode, no user session: admitted via the runner key alone

	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/api/v1/health", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Runner "+f.plaintext)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a runner key never authorizes user routes")

	auth := NewAuthMiddleware(identity.ModeOrg, "arbitron_session", newFakeSessionRepo(), nil, newFakeUserRepo(), identity.GroupConfig{DefaultGroup: "default"})
	unwired := NewRouterFromDeps(RouterDeps{Config: &config.Config{}, Auth: auth})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/runners/connect", nil)
	r.Header.Set("Authorization", "Runner "+f.plaintext)
	unwired.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "without the handler the path is just a user route, which refuses runner keys")
}

func TestRunnerConnect_UnprovenCapabilityNeverReceivesDispatch(t *testing.T) {
	f := newConnectFixture(t)
	h := wireHello()
	h.Sources[0].UnavailableReason = "harness_unsupported"
	f.connect(t, newMachine(t), h)
	snap, err := f.pool.Registry.Snapshot(context.Background())
	require.NoError(t, err)
	decision := runner.Place(runner.ExecRequirements{Group: runner.ModelGroup{Kind: runner.CredAnthropicSubscription, Family: "anthropic"}, Model: "claude-opus-5-5", Op: runner.OpChat, Affinity: runner.AffinityNone}, snap, runner.PlacementPolicy{})
	require.False(t, decision.Placed, "unproven harness must never produce a dispatch offer")
}

func TestRunnerConnect_ServerRestartReplaysDurableInbox(t *testing.T) {
	f := newConnectFixture(t)
	priv := newMachine(t)
	w1, _ := f.connect(t, priv, wireHello())
	a := f.pool.ReserveJob(t, f.pool.SeedRun(t), "mac-a", runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""), runner.OpStream)
	type result struct {
		out runner.StepOutput
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		out, err := f.hub.Run(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
		done <- result{out, err}
	}()
	require.Equal(t, runnerproto.KindDispatch, w1.read().Kind)
	w1.sendJob(runnerproto.KindAccept, a.Fence.JobID, a.Fence.Generation, 0, nil)
	first := <-done
	require.NoError(t, first.err)
	w1.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 1, &runnerproto.Output{Seq: 1, Token: "a"})
	require.Equal(t, runnerproto.KindAck, w1.read().Kind)
	require.NoError(t, w1.c.CloseNow())
	f.srv.Close()
	// A new hub and HTTP server share only the existing file-backed database.
	f.hub = f.pool.NewHub(hubtest.HubConfig())
	handler := NewRunnerConnectHandler(f.keys, f.hub, RunnerConnectConfig{Logger: slog.New(slog.NewTextHandler(f.logs, nil))})
	f.srv = httptest.NewServer(NewRouterFromDeps(RouterDeps{Config: &config.Config{}, RunnerConnect: handler}))
	t.Cleanup(f.srv.Close)
	hello := wireHello()
	hello.Held = []runnerproto.HeldJob{{JobID: a.Fence.JobID.String(), Generation: a.Fence.Generation, State: "running", LastOutputSeq: 2}}
	w2, _ := f.connect(t, priv, hello)
	resume := w2.read()
	require.Equal(t, runnerproto.KindAck, resume.Kind)
	var ack runnerproto.Ack
	require.NoError(t, runnerproto.DecodePayload(resume, &ack))
	require.Equal(t, uint64(1), ack.Seq)
	out, err := f.hub.Run(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream))
	require.NoError(t, err)
	w2.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 1, &runnerproto.Output{Seq: 1, Token: "a"})
	w2.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 2, &runnerproto.Output{Seq: 2, Token: "b"})
	w2.sendJob(runnerproto.KindResult, a.Fence.JobID, a.Fence.Generation, 0, &runnerproto.Result{OK: true, Chat: &runnerproto.WireChatResult{Output: "ab"}})
	var tokens []string
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case c, ok := <-out.Stream:
			if !ok {
				require.Equal(t, []string{"a", "b"}, tokens)
				return
			}
			if !c.Done {
				tokens = append(tokens, c.Token)
			}
		case <-timer.C:
			t.Fatal("recovered stream did not finish")
		}
	}
}

func TestRunnerConnect_JournalFailureSendsNoAck(t *testing.T) {
	f := newConnectFixture(t)
	w, _ := f.connect(t, newMachine(t), wireHello())
	a := f.pool.ReserveJob(t, f.pool.SeedRun(t), "mac-a", runner.MakeSourceID("mac-a", runner.CredAnthropicSubscription, ""), runner.OpStream)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.hub.Run(ctx, a, hubtest.ChatJob("claude-opus-5-5", runner.OpStream)); done <- err }()
	require.Equal(t, runnerproto.KindDispatch, w.read().Kind)
	w.sendJob(runnerproto.KindAccept, a.Fence.JobID, a.Fence.Generation, 0, nil)
	require.NoError(t, <-done)
	require.NoError(t, f.pool.DB.Close())
	w.sendJob(runnerproto.KindOutput, a.Fence.JobID, a.Fence.Generation, 1, &runnerproto.Output{Seq: 1, Token: "must-not-ack"})
	code, frames := w.closeStatus()
	require.Equal(t, websocket.StatusInternalError, code)
	require.Empty(t, frames, "journal failure must not acknowledge unpersisted output")
}
