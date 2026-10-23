package probe

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var usabilityNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func usabilityPath() modelcatalog.ExecutionPath {
	return modelcatalog.ExecutionPath{
		Key: "path-key-1", Kind: string(domprovider.ProfileKindChat), Mode: modelcatalog.ExecutionModeBatch,
		ConnectionRef: "conn-1", RouteProvider: "anthropic-api", ConfigRevision: "rev-7",
		Binding: domprovider.ResolvedBinding{
			Kind: domprovider.ProfileKindChat, Provider: "anthropic-api", BaseURL: "https://api.example.test",
			RevealKey: func(context.Context) (string, error) { return "sk-secret", nil },
		},
	}
}

func usabilityEntry() modelidentity.Entry {
	return modelidentity.Entry{
		CanonicalID: "anthropic/claude-sonnet-5-5", Provider: "anthropic",
		NativeID: "claude-sonnet-5-5", SpecificModel: "claude-sonnet-5-5-20261001",
	}
}

type fakeLane struct {
	supports bool
	check    func(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error)
}

func (f fakeLane) Supports(modelcatalog.ExecutionPath) bool { return f.supports }
func (f fakeLane) Check(ctx context.Context, p modelcatalog.ExecutionPath, e modelidentity.Entry) (LaneResult, error) {
	return f.check(ctx, p, e)
}

func newTestRouter(t *testing.T, timeout time.Duration, lanes ...UsabilityLane) *UsabilityRouter {
	t.Helper()
	r, err := NewUsabilityRouter(UsabilityRouterDeps{Lanes: lanes, Timeout: timeout, Clock: func() time.Time { return usabilityNow }})
	require.NoError(t, err)
	return r
}

func confirmedLane(spelling string) fakeLane {
	return fakeLane{supports: true, check: func(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error) {
		return LaneResult{NativeModel: spelling, ProofRef: "proof:" + spelling}, nil
	}}
}

// Happy: the router, not the lane, stamps the path identity, so a lane cannot
// claim proof for another path or revision.
func TestUsabilityRouterConfirmedStampsPathIdentity(t *testing.T) {
	r := newTestRouter(t, time.Second, confirmedLane("claude-sonnet-5-5-20261001"))
	ev, err := r.Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.True(t, ev.Confirmed())
	assert.Equal(t, "path-key-1", ev.PathKey)
	assert.Equal(t, "rev-7", ev.ConfigRevision)
	assert.Equal(t, "anthropic/claude-sonnet-5-5", ev.CanonicalID)
	assert.Equal(t, "claude-sonnet-5-5-20261001", ev.NativeModel)
	assert.Equal(t, usabilityNow, ev.CheckedAt)
}

func TestUsabilityRouterUnknownWhenNoLaneSupportsPath(t *testing.T) {
	r := newTestRouter(t, time.Second, fakeLane{supports: false})
	ev, err := r.Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
	assert.False(t, ev.Confirmed())
	assert.Equal(t, "path-key-1", ev.PathKey)
	assert.Equal(t, "rev-7", ev.ConfigRevision)
}

// Error: a lane failure is unknown and its (possibly credential-bearing) text
// never reaches the evidence.
func TestUsabilityRouterLaneErrorIsUnknownAndSecretFree(t *testing.T) {
	lane := fakeLane{supports: true, check: func(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error) {
		return LaneResult{}, errors.New("401 for key sk-secret")
	}}
	ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
	assert.NotContains(t, strings.Join([]string{ev.ProofRef, ev.NativeModel, ev.State}, "|"), "sk-secret")
}

func TestUsabilityRouterLanePanicIsUnknown(t *testing.T) {
	lane := fakeLane{supports: true, check: func(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error) {
		panic("boom sk-secret")
	}}
	ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
}

// Failure: a lane that reports success without a proof reference or the
// executed spelling never confirms.
func TestUsabilityRouterIncompleteLaneProofNeverConfirms(t *testing.T) {
	for name, res := range map[string]LaneResult{
		"no proof":  {NativeModel: "claude-sonnet-5-5-20261001"},
		"no model":  {ProofRef: "proof:x"},
		"all empty": {},
	} {
		t.Run(name, func(t *testing.T) {
			lane := fakeLane{supports: true, check: func(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error) {
				return res, nil
			}}
			ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), usabilityPath(), usabilityEntry())
			require.NoError(t, err)
			assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
		})
	}
}

// Failure: a lane proving a different spelling than the candidate never confirms.
func TestUsabilityRouterWrongSpellingNeverConfirms(t *testing.T) {
	ev, err := newTestRouter(t, time.Second, confirmedLane("claude-haiku-5-5")).Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
}

// Edge: a check is bounded by the per-check timeout and reports unknown.
func TestUsabilityRouterTimeoutIsUnknown(t *testing.T) {
	lane := fakeLane{supports: true, check: func(ctx context.Context, _ modelcatalog.ExecutionPath, _ modelidentity.Entry) (LaneResult, error) {
		<-ctx.Done()
		return LaneResult{NativeModel: "m", ProofRef: "late"}, nil
	}}
	ev, err := newTestRouter(t, 20*time.Millisecond, lane).Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State, "a result produced after the deadline is not evidence")
}

// Edge: caller cancellation is an error, not an unknown verdict.
func TestUsabilityRouterCallerCancellationIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTestRouter(t, time.Second, confirmedLane("m")).Check(ctx, usabilityPath(), usabilityEntry())
	assert.ErrorIs(t, err, context.Canceled)
}

// Input guard: a path or entry without identity cannot be probed.
func TestUsabilityRouterRejectsMissingIdentity(t *testing.T) {
	r := newTestRouter(t, time.Second, confirmedLane("m"))
	p := usabilityPath()
	p.Key = ""
	_, err := r.Check(context.Background(), p, usabilityEntry())
	assert.Error(t, err)
	p = usabilityPath()
	p.ConfigRevision = ""
	_, err = r.Check(context.Background(), p, usabilityEntry())
	assert.Error(t, err)
	e := usabilityEntry()
	e.CanonicalID = ""
	_, err = r.Check(context.Background(), usabilityPath(), e)
	assert.Error(t, err)
	e = usabilityEntry()
	e.NativeID, e.SpecificModel = "", ""
	_, err = r.Check(context.Background(), usabilityPath(), e)
	assert.Error(t, err)
}

func TestUsabilityRouterRequiresLanesAndBoundsCap(t *testing.T) {
	_, err := NewUsabilityRouter(UsabilityRouterDeps{})
	assert.Error(t, err)
}

// Edge: at most four checks run at once, however many callers there are.
func TestUsabilityRouterCapsParallelChecks(t *testing.T) {
	var inFlight, peak atomic.Int32
	lane := fakeLane{supports: true, check: func(context.Context, modelcatalog.ExecutionPath, modelidentity.Entry) (LaneResult, error) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		inFlight.Add(-1)
		return LaneResult{NativeModel: "m", ProofRef: "p"}, nil
	}}
	r := newTestRouter(t, time.Second, lane)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Check(context.Background(), usabilityPath(), usabilityEntry())
		}()
	}
	wg.Wait()
	assert.LessOrEqual(t, int(peak.Load()), 4)
	assert.GreaterOrEqual(t, int(peak.Load()), 2, "the cap must not serialize everything")
}

// ---- direct-connection (HTTP chat) lane -----------------------------------

type chatCall struct {
	binding domprovider.ResolvedBinding
	req     engine.ChatRequest
}

type fakeChat struct {
	call  *chatCall
	model string // model the "provider" reports as executed
	err   error
}

func (f fakeChat) Chat(_ context.Context, req engine.ChatRequest) (*engine.ChatResponse, error) {
	f.call.req = req
	if f.err != nil {
		return nil, f.err
	}
	return &engine.ChatResponse{Output: "ok", Model: f.model}, nil
}
func (f fakeChat) StreamChat(context.Context, engine.ChatRequest) (<-chan engine.ChatChunk, error) {
	return nil, errors.New("stream must not be used by a usability probe")
}

type fakeChatSelector struct {
	call     *chatCall
	executed func(domprovider.ResolvedBinding) string
	err      error
}

func (s fakeChatSelector) ChatProviderFor(b domprovider.ResolvedBinding) (engine.ChatProvider, error) {
	s.call.binding = b
	if s.err != nil {
		return nil, s.err
	}
	return fakeChat{call: s.call, model: s.executed(b)}, nil
}

func chatLaneFixture(t *testing.T, executed func(domprovider.ResolvedBinding) string) (*ChatUsabilityLane, *chatCall) {
	t.Helper()
	call := &chatCall{}
	lane, err := NewChatUsabilityLane(ChatLaneDeps{
		Chats: fakeChatSelector{call: call, executed: executed},
		WithBinding: func(_ context.Context, p modelcatalog.ExecutionPath, model string) (domprovider.ResolvedBinding, error) {
			if model == "" || p.Binding.RevealKey == nil {
				return domprovider.ResolvedBinding{}, errors.New("not an inventory path")
			}
			b := p.Binding
			b.Model = model
			return b, nil
		},
	})
	require.NoError(t, err)
	return lane, call
}

// Happy: the lane runs the exact candidate spelling pinned, tool-less, with the
// executed-model requirement, and confirms only on the reported executed model.
func TestUsabilityChatLaneConfirmsExactExecutedModel(t *testing.T) {
	lane, call := chatLaneFixture(t, func(b domprovider.ResolvedBinding) string {
		if b.Model != "claude-sonnet-5-5-20261001" {
			return "some-fallback-model"
		}
		return b.Model
	})
	require.True(t, lane.Supports(usabilityPath()))
	res, err := lane.Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5-20261001", res.NativeModel)
	assert.NotEmpty(t, res.ProofRef)
	assert.NotContains(t, res.ProofRef, "sk-secret")
	assert.False(t, call.binding.Pinned, "discovery requires execution proof, not fixed-version grammar")
	assert.True(t, call.req.RequireExecutedModel)
	assert.Equal(t, "claude-sonnet-5-5-20261001", call.req.Model)
	assert.Empty(t, call.req.ToolScope)
	assert.Empty(t, call.req.WorkDir)
	assert.Empty(t, call.req.SessionID)
	assert.Greater(t, call.req.MaxTokens, 0)
	assert.NotEmpty(t, call.req.Prompt)
}

// Success: an entry with no fixed spelling probes its catalog native id.
func TestUsabilityChatLaneFallsBackToNativeSpelling(t *testing.T) {
	lane, call := chatLaneFixture(t, func(b domprovider.ResolvedBinding) string { return b.Model })
	e := usabilityEntry()
	e.SpecificModel = ""
	res, err := lane.Check(context.Background(), usabilityPath(), e)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", res.NativeModel)
	assert.Equal(t, "claude-sonnet-5-5", call.binding.Model)
}

// Failure: a provider that executed another model (fallback) never confirms.
func TestUsabilityChatLaneWrongExecutedModelFails(t *testing.T) {
	lane, _ := chatLaneFixture(t, func(domprovider.ResolvedBinding) string { return "claude-haiku-5-5" })
	res, err := lane.Check(context.Background(), usabilityPath(), usabilityEntry())
	assert.Error(t, err)
	assert.Empty(t, res.NativeModel)
	assert.Empty(t, res.ProofRef)
}

func TestUsabilityChatLaneProviderFaultsFail(t *testing.T) {
	call := &chatCall{}
	for name, deps := range map[string]ChatLaneDeps{
		"selector": {Chats: fakeChatSelector{call: call, err: errors.New("no adapter")}, WithBinding: func(_ context.Context, p modelcatalog.ExecutionPath, m string) (domprovider.ResolvedBinding, error) {
			b := p.Binding
			b.Model = m
			return b, nil
		}},
		"binding": {Chats: fakeChatSelector{call: call, executed: func(b domprovider.ResolvedBinding) string { return b.Model }}, WithBinding: func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error) {
			return domprovider.ResolvedBinding{}, errors.New("unsupported route")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			lane, err := NewChatUsabilityLane(deps)
			require.NoError(t, err)
			_, err = lane.Check(context.Background(), usabilityPath(), usabilityEntry())
			assert.Error(t, err)
		})
	}
}

// Edge: only batch, backend-less chat routes with an executed-model producer are
// supported: the evidenced HTTP providers and the Claude Code subscription chat
// adapter (claude-cli, whose collectResult proves modelUsage). Agent backends,
// interactive streams and routes without an exact-model producer (Azure,
// Bedrock) are left for their own lane and report unknown through the router.
func TestUsabilityChatLaneSupportsOnlyEvidencedHTTPRoutes(t *testing.T) {
	lane, _ := chatLaneFixture(t, func(b domprovider.ResolvedBinding) string { return b.Model })
	with := func(mut func(*modelcatalog.ExecutionPath)) modelcatalog.ExecutionPath {
		p := usabilityPath()
		mut(&p)
		return p
	}
	for provider, want := range map[string]bool{
		"anthropic-api": true, "anthropic": true, "openai": true, "openrouter": true, "litellm": true,
		"ollama": true, "ollama-cloud": true, "azure": false, "bedrock": false, "claude-cli": false, "": false,
	} {
		p := with(func(p *modelcatalog.ExecutionPath) { p.Binding.Provider = provider })
		assert.Equal(t, want, lane.Supports(p), "provider %q", provider)
	}
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Backend = "opencode" })))
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Mode = modelcatalog.ExecutionModeInteractive })))
	assert.False(t, lane.Supports(with(func(p *modelcatalog.ExecutionPath) { p.Kind = string(domprovider.ProfileKindAgent) })))
}

func TestUsabilityChatLaneRequiresCollaborators(t *testing.T) {
	_, err := NewChatUsabilityLane(ChatLaneDeps{})
	assert.Error(t, err)
}

// Wiring: the router is a modelcatalog.UsabilityProbe and a chat lane inside it
// yields evidence the durable store accepts as confirmed.
func TestUsabilityRouterWithChatLaneProducesConfirmedEvidence(t *testing.T) {
	lane, _ := chatLaneFixture(t, func(b domprovider.ResolvedBinding) string { return b.Model })
	var probe modelcatalog.UsabilityProbe = newTestRouter(t, time.Second, lane)
	ev, err := probe.Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.True(t, ev.Confirmed(), "%+v", ev)

	// A fallback model through the same router is unknown, not usable.
	bad, _ := chatLaneFixture(t, func(domprovider.ResolvedBinding) string { return "other" })
	probe = newTestRouter(t, time.Second, bad)
	ev, err = probe.Check(context.Background(), usabilityPath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
}

// ---- Claude Code subscription (claude-cli chat) lane ----------------------

type claudeChatSelector struct{ runner claudecli.CommandRunner }

func (s claudeChatSelector) ChatProviderFor(b domprovider.ResolvedBinding) (engine.ChatProvider, error) {
	return claudecli.NewChatAdapterForBinding(claudecli.Config{BinaryPath: "claude", Runner: s.runner}, b), nil
}

func claudeLanePath() modelcatalog.ExecutionPath {
	p := usabilityPath()
	p.ConnectionRef, p.RouteProvider = "", "claude-cli"
	p.Binding.Provider = "claude-cli"
	return p
}

// Wiring: the lane drives the real Claude Code chat adapter and its modelUsage
// reader, so a CLI that executed a fallback model never produces evidence, and
// the exact model does.
func TestUsabilityChatLaneClaudeCLIProvesExecutionFromModelUsage(t *testing.T) {
	for name, tc := range map[string]struct {
		usage   string
		confirm bool
	}{
		"exact":    {`{"claude-sonnet-5-5-20261001":{}}`, true},
		"fallback": {`{"claude-haiku-5-5":{}}`, false},
		"missing":  {`null`, false},
	} {
		t.Run(name, func(t *testing.T) {
			r := &fakeRunner{lines: []string{`{"type":"result","is_error":false,"result":"ok","session_id":"s","modelUsage":` + tc.usage + `}`}}
			workDir := filepath.Join(t.TempDir(), "model-probe")
			lane, err := NewChatUsabilityLane(ChatLaneDeps{
				WorkDir: workDir,
				Chats:   claudeChatSelector{runner: r},
				WithBinding: func(_ context.Context, p modelcatalog.ExecutionPath, m string) (domprovider.ResolvedBinding, error) {
					b := p.Binding
					b.Model = m
					return b, nil
				},
			})
			require.NoError(t, err)
			require.True(t, lane.Supports(claudeLanePath()))
			router := newTestRouter(t, time.Second, lane)
			ev, err := router.Check(context.Background(), claudeLanePath(), usabilityEntry())
			require.NoError(t, err)
			assert.Equal(t, tc.confirm, ev.Confirmed())
			if !tc.confirm {
				assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
			}
			model, _ := argValueOf(r.gotSpec.Args, "--model")
			assert.Equal(t, "claude-sonnet-5-5-20261001", model)
			assert.NotContains(t, r.gotSpec.Args, "--dangerously-skip-permissions")
			assert.Equal(t, workDir, filepath.Dir(r.gotSpec.Dir))
			assert.NoDirExists(t, r.gotSpec.Dir)
		})
	}
}

func argValueOf(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// Edge: without a scratch directory the claude-cli route is unsupported (the
// adapter would refuse every check), so the router reports unknown.
func TestUsabilityChatLaneClaudeCLIUnsupportedWithoutWorkDir(t *testing.T) {
	lane, _ := chatLaneFixture(t, func(b domprovider.ResolvedBinding) string { return b.Model })
	assert.False(t, lane.Supports(claudeLanePath()))
	ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), claudeLanePath(), usabilityEntry())
	require.NoError(t, err)
	assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
}
