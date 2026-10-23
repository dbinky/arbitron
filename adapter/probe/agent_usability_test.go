package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
	"github.com/arbitron/arbitron/adapter/opencode"
	"github.com/arbitron/arbitron/adapter/pi"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/modelidentity"
	domprovider "github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production claude-code, pi and opencode adapters are the probers this
// lane dispatches to.
var (
	_ ExecutedModelProber = (*claudecli.AgentAdapter)(nil)
	_ ExecutedModelProber = (*pi.Provider)(nil)
	_ ExecutedModelProber = (*opencode.Provider)(nil)
	_ ExecutedModelProber = (*codex.Provider)(nil)
)

func agentLanePath(backend string) modelcatalog.ExecutionPath {
	p := usabilityPath()
	p.Kind, p.Backend = string(domprovider.ProfileKindAgent), backend
	p.Binding.Kind, p.Binding.Backend = domprovider.ProfileKindAgent, backend
	return p
}

func passBinding(_ context.Context, p modelcatalog.ExecutionPath, m string) (domprovider.ResolvedBinding, error) {
	b := p.Binding
	b.Model = m
	return b, nil
}

// claudeAgentSelector builds the real per-binding Claude Code agent adapter, as
// the production factory does for the claude-code backend, with the full agent
// tool scope a real run would carry.
type claudeAgentSelector struct {
	runner claudecli.CommandRunner
	root   string
	got    *domprovider.ResolvedBinding
}

func (s claudeAgentSelector) AgentProviderFor(b domprovider.ResolvedBinding) (engine.AgentProvider, error) {
	if s.got != nil {
		*s.got = b
	}
	cfg := claudecli.Config{BinaryPath: "claude", Runner: s.runner, WorkspaceRoot: s.root, AllowedTools: []string{"Bash", "Write"}, SkipPermissions: true}
	return claudecli.NewAgentAdapterForBinding(cfg, b), nil
}

// Wiring: the lane drives the real Claude Code agent adapter with tools
// suppressed and reads the CLI's own modelUsage, so only the exact executed
// model confirms; a silent fallback or missing report stays unknown.
func TestUsabilityAgentLaneClaudeCodeProvesExecutionFromModelUsage(t *testing.T) {
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
			root := t.TempDir()
			workDir := filepath.Join(root, ".model-probe")
			var got domprovider.ResolvedBinding
			lane, err := NewAgentUsabilityLane(AgentLaneDeps{Agents: claudeAgentSelector{runner: r, root: root, got: &got}, WithBinding: passBinding, WorkDir: workDir})
			require.NoError(t, err)
			path := agentLanePath("claude-code")
			require.True(t, lane.Supports(path))

			ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), path, usabilityEntry())
			require.NoError(t, err)
			assert.Equal(t, tc.confirm, ev.Confirmed())
			if tc.confirm {
				assert.Equal(t, "agent-exec:claude-code:claude-sonnet-5-5-20261001:s", ev.ProofRef, "proof names the harness session, not only the request")
			} else {
				assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
			}
			assert.False(t, got.Pinned, "native discovery is independent of run-pin grammar")
			assert.Equal(t, "claude-sonnet-5-5-20261001", got.Model)
			model, _ := argValueOf(r.gotSpec.Args, "--model")
			assert.Equal(t, "claude-sonnet-5-5-20261001", model)
			tools, ok := argValueOf(r.gotSpec.Args, "--allowedTools")
			assert.True(t, ok)
			assert.Empty(t, tools, "the agent tool scope never reaches a probe")
			assert.NotContains(t, r.gotSpec.Args, "--dangerously-skip-permissions")
			assert.Equal(t, workDir, filepath.Dir(r.gotSpec.Dir))
			assert.NoDirExists(t, r.gotSpec.Dir)
			assert.DirExists(t, workDir)
		})
	}
}

// Edge: only batch agent paths on a backend with a tool-disabled exact-model
// probe are admitted; others stay with the router's unknown default.
func TestUsabilityAgentLaneSupportsOnlyProvableAgentBackends(t *testing.T) {
	lane, err := NewAgentUsabilityLane(AgentLaneDeps{Agents: claudeAgentSelector{}, WithBinding: passBinding, WorkDir: "/tmp/w"})
	require.NoError(t, err)
	assert.True(t, lane.Supports(agentLanePath("claude-code")))
	assert.True(t, lane.Supports(agentLanePath("pi")))
	assert.True(t, lane.Supports(agentLanePath("opencode")))
	assert.True(t, lane.Supports(agentLanePath("codex")))
	for _, backend := range []string{"gemini", ""} {
		assert.False(t, lane.Supports(agentLanePath(backend)), backend)
	}
	interactive := agentLanePath("claude-code")
	interactive.Mode = modelcatalog.ExecutionModeInteractive
	assert.False(t, lane.Supports(interactive))
	assert.False(t, lane.Supports(usabilityPath()), "chat paths belong to the chat lane")
}

type fakeAgentProvider struct{}

func (fakeAgentProvider) RunAgent(context.Context, engine.AgentMission) (*engine.AgentResult, error) {
	return &engine.AgentResult{Model: "claude-sonnet-5-5-20261001"}, nil
}

type fakeProber struct {
	fakeAgentProvider
	model   string
	session string
	err     error
}

func (f fakeProber) ProbeExecutedModel(context.Context, string, string) (string, string, error) {
	return f.model, f.session, f.err
}

type agentSelectorFunc func(domprovider.ResolvedBinding) (engine.AgentProvider, error)

func (f agentSelectorFunc) AgentProviderFor(b domprovider.ResolvedBinding) (engine.AgentProvider, error) {
	return f(b)
}

// Failure/error: every fault before or during the probe is a secret-free
// non-confirmation, and an adapter whose RunAgent merely echoes the model is
// never taken as proof.
func TestUsabilityAgentLaneFaultsNeverConfirm(t *testing.T) {
	ok := func(domprovider.ResolvedBinding) (engine.AgentProvider, error) {
		return fakeProber{model: "claude-sonnet-5-5-20261001", session: "ses_1"}, nil
	}
	for name, tc := range map[string]struct {
		binding func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error)
		agents  agentSelectorFunc
		workDir string
	}{
		"binding fails": {binding: func(context.Context, modelcatalog.ExecutionPath, string) (domprovider.ResolvedBinding, error) {
			return domprovider.ResolvedBinding{}, errors.New("reveal sk-secret failed")
		}, agents: ok},
		"factory fails":     {agents: func(domprovider.ResolvedBinding) (engine.AgentProvider, error) { return nil, errors.New("no adapter") }},
		"echo-only adapter": {agents: func(domprovider.ResolvedBinding) (engine.AgentProvider, error) { return fakeAgentProvider{}, nil }},
		"probe fails": {agents: func(domprovider.ResolvedBinding) (engine.AgentProvider, error) {
			return fakeProber{err: errors.New("cli said sk-secret")}, nil
		}},
		"different model": {agents: func(domprovider.ResolvedBinding) (engine.AgentProvider, error) {
			return fakeProber{model: "claude-haiku-5-5", session: "ses_1"}, nil
		}},
		"no harness session": {agents: func(domprovider.ResolvedBinding) (engine.AgentProvider, error) {
			return fakeProber{model: "claude-sonnet-5-5-20261001"}, nil
		}},
		"work dir cannot be created": {agents: ok, workDir: "/dev/null/probe"},
	} {
		t.Run(name, func(t *testing.T) {
			binding := tc.binding
			if binding == nil {
				binding = passBinding
			}
			workDir := tc.workDir
			if workDir == "" {
				workDir = filepath.Join(t.TempDir(), "probe")
			}
			lane, err := NewAgentUsabilityLane(AgentLaneDeps{Agents: tc.agents, WithBinding: binding, WorkDir: workDir})
			require.NoError(t, err)
			res, err := lane.Check(context.Background(), agentLanePath("claude-code"), usabilityEntry())
			require.ErrorIs(t, err, ErrUsabilityNotConfirmed)
			assert.NotContains(t, err.Error(), "sk-secret")
			assert.Empty(t, res.ProofRef)
		})
	}
}

// Happy: a prober that reports the requested spelling confirms, using the
// native id when the catalog has no fixed version.
func TestUsabilityAgentLaneConfirmsNativeSpelling(t *testing.T) {
	var asked string
	agents := agentSelectorFunc(func(b domprovider.ResolvedBinding) (engine.AgentProvider, error) {
		asked = b.Model
		return fakeProber{model: b.Model, session: "ses_1"}, nil
	})
	lane, err := NewAgentUsabilityLane(AgentLaneDeps{Agents: agents, WithBinding: passBinding, WorkDir: filepath.Join(t.TempDir(), "p")})
	require.NoError(t, err)
	entry := modelidentity.Entry{CanonicalID: "anthropic/claude-sonnet-5-5", NativeID: "claude-sonnet-5-5"}
	res, err := lane.Check(context.Background(), agentLanePath("claude-code"), entry)
	require.NoError(t, err)
	assert.Equal(t, "claude-sonnet-5-5", asked)
	assert.Equal(t, LaneResult{NativeModel: "claude-sonnet-5-5", ProofRef: "agent-exec:claude-code:claude-sonnet-5-5:ses_1"}, res)
}

func TestUsabilityAgentLaneRequiresCollaborators(t *testing.T) {
	agents := agentSelectorFunc(func(domprovider.ResolvedBinding) (engine.AgentProvider, error) { return nil, nil })
	for name, d := range map[string]AgentLaneDeps{
		"no selector": {WithBinding: passBinding, WorkDir: "/tmp/w"},
		"no binding":  {Agents: agents, WorkDir: "/tmp/w"},
		"no work dir": {Agents: agents, WithBinding: passBinding},
	} {
		_, err := NewAgentUsabilityLane(d)
		assert.Error(t, err, name)
	}
}

// piRunner answers the version check with the verified tool-free release and
// the probe turn with an assistant message_end naming executed.
type piRunner struct {
	executed string
	calls    []claudecli.RunSpec
}

func (r *piRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls = append(r.calls, spec)
	line := `{"type":"message_end","message":{"role":"assistant","provider":"anthropic","model":"` + r.executed + `","stopReason":"stop","content":[{"type":"text","text":"ok"}]}}`
	if len(spec.Args) == 1 && spec.Args[0] == "--version" {
		line = "1.3.0"
	}
	lines := []string{`{"type":"session","version":3,"id":"pi-ses-1"}`, line}
	if len(spec.Args) == 1 && spec.Args[0] == "--version" {
		lines = []string{line}
	}
	ch := make(chan string, len(lines))
	for _, l := range lines {
		ch <- l
	}
	close(ch)
	return ch, func() error { return nil }, nil
}

// Wiring: on the pi backend the lane drives the real pi adapter with every tool
// and MCP server disabled and confirms only from pi's own assistant message_end;
// a silent fallback stays unknown.
func TestUsabilityAgentLanePiProvesExecutionFromAssistantMessage(t *testing.T) {
	for name, tc := range map[string]struct {
		executed string
		confirm  bool
	}{
		"exact":    {"claude-sonnet-5-5-20261001", true},
		"fallback": {"claude-haiku-5-5", false},
	} {
		t.Run(name, func(t *testing.T) {
			r := &piRunner{executed: tc.executed}
			workDir := filepath.Join(t.TempDir(), ".model-probe")
			agents := agentSelectorFunc(func(b domprovider.ResolvedBinding) (engine.AgentProvider, error) {
				return pi.New(pi.Config{Binding: b, BinaryPath: "pi", Runner: r}), nil
			})
			lane, err := NewAgentUsabilityLane(AgentLaneDeps{Agents: agents, WithBinding: passBinding, WorkDir: workDir})
			require.NoError(t, err)
			path := agentLanePath("pi")
			require.True(t, lane.Supports(path))

			ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), path, usabilityEntry())
			require.NoError(t, err)
			assert.Equal(t, tc.confirm, ev.Confirmed())
			if tc.confirm {
				assert.Equal(t, "agent-exec:pi:claude-sonnet-5-5-20261001:pi-ses-1", ev.ProofRef)
			}
			require.Len(t, r.calls, 2)
			spec := r.calls[1]
			model, _ := argValueOf(spec.Args, "--model")
			assert.Equal(t, "claude-sonnet-5-5-20261001", model)
			assert.Contains(t, spec.Args, "--no-tools")
			assert.Contains(t, spec.Args, "--no-mcp")
			assert.Contains(t, spec.Env, "ANTHROPIC_API_KEY=sk-secret", "the path credential reaches the probe")
			assert.Equal(t, workDir, filepath.Dir(spec.Dir))
			assert.NoDirExists(t, spec.Dir)
		})
	}
}

// opencodeRunner records every launch and answers the version check, the probe
// run and its export as a real mission would see them: the export names
// executed, so any launch that got this far would look confirmable.
type opencodeRunner struct {
	executed string
	calls    []claudecli.RunSpec
}

// opencodeOverlayDisables reports whether a launch's inline config disables the
// inherited "hostile" MCP server, as the real binary's deep merge would.
func opencodeOverlayDisables(env []string) bool {
	for _, kv := range env {
		if raw, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			var doc struct {
				MCP map[string]struct {
					Enabled *bool `json:"enabled"`
				} `json:"mcp"`
			}
			if json.Unmarshal([]byte(raw), &doc) == nil {
				e := doc.MCP["hostile"].Enabled
				return e != nil && !*e
			}
		}
	}
	return false
}

func (r *opencodeRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls = append(r.calls, spec)
	var line string
	switch spec.Args[0] {
	case "--version":
		line = "opencode 1.19.3"
	case "debug":
		enabled := "true"
		if opencodeOverlayDisables(spec.Env) {
			enabled = "false"
		}
		line = `{"mcp":{"hostile":{"type":"local","command":["touch","MARKER"],"enabled":` + enabled + `}}}`
	case "run":
		if !slices.Contains(spec.Args, "--pure") || !opencodeOverlayDisables(spec.Env) {
			return nil, nil, fmt.Errorf("probe run without D12 isolation")
		}
		line = `{"type":"text","sessionID":"ses_probe","part":{"id":"prt_1","sessionID":"ses_probe","messageID":"msg_1","type":"text","text":"ok"}}`
	case "export":
		line = `{"info":{"id":"ses_probe"},"messages":[{"info":{"id":"msg_1","sessionID":"ses_probe","role":"assistant","providerID":"anthropic","modelID":"` + r.executed + `","time":{"created":1,"completed":2}},"parts":[]}]}`
	}
	ch := make(chan string, 1)
	ch <- line
	close(ch)
	return ch, func() error { return nil }, nil
}

// Wiring (D12): on the opencode backend the lane drives the real opencode
// adapter, which disables every resolved MCP server, verifies the effective
// config and runs under --pure. The fake refuses any probe run lacking that
// isolation. An exact export confirms with the harness session as proof; a
// fallback stays unknown.
func TestUsabilityAgentLaneOpenCodeD12Isolation(t *testing.T) {
	for name, tc := range map[string]struct {
		executed  string
		confirmed bool
	}{
		"exact":    {"claude-sonnet-5-5-20261001", true},
		"fallback": {"claude-haiku-5-5", false},
	} {
		t.Run(name, func(t *testing.T) {
			r := &opencodeRunner{executed: tc.executed}
			workDir := filepath.Join(t.TempDir(), ".model-probe")
			var built int
			agents := agentSelectorFunc(func(b domprovider.ResolvedBinding) (engine.AgentProvider, error) {
				built++
				return opencode.New(opencode.Config{Binding: b, BinaryPath: "opencode", Runner: r, AllowAllTools: true}), nil
			})
			lane, err := NewAgentUsabilityLane(AgentLaneDeps{Agents: agents, WithBinding: passBinding, WorkDir: workDir})
			require.NoError(t, err)
			path := agentLanePath("opencode")
			require.True(t, lane.Supports(path))

			ev, err := newTestRouter(t, time.Second, lane).Check(context.Background(), path, usabilityEntry())
			require.NoError(t, err)
			assert.Equal(t, 1, built, "the lane must reach the production opencode adapter")
			assert.Equal(t, tc.confirmed, ev.Confirmed(), "%+v", ev)
			if tc.confirmed {
				assert.Contains(t, ev.ProofRef, "ses_probe")
			} else {
				assert.Equal(t, modelcatalog.UsabilityUnknown, ev.State)
				assert.Empty(t, ev.ProofRef)
			}
			var runs int
			for _, c := range r.calls {
				if c.Args[0] == "run" {
					runs++
				}
			}
			assert.Equal(t, 1, runs, "exactly one isolated probe run")
		})
	}
}
