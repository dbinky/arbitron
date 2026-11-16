package codex

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lrKey = "lr_live_codexTestKey123"

func lrCodexBinding() provider.ResolvedBinding {
	return provider.ResolvedBinding{Kind: provider.ProfileKindAgent, Provider: "lunaroute", BaseURL: provider.LunaRouteHostedRoot, Backend: "codex", BackendExplicit: true, ConnectionRef: "lr", Model: "glm-5.3", RevealKey: func(context.Context) (string, error) { return lrKey, nil }}
}
func lrCodexRules() provider.CapabilityRules {
	return provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{
		"glm-5.3": {ID: "glm-5.3", ContextTokens: provider.LimitFact{Tokens: 202752, Evidence: "fixture#context"}, OutputTokens: provider.LimitFact{Tokens: 32768, Evidence: "fixture#output"}, Efforts: map[string]provider.EffortFact{"codex": {Known: true, Levels: []provider.Effort{provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh}, Evidence: "fixture#effort"}}},
	})
}
func lrMission() engine.AgentMission {
	return engine.AgentMission{Prompt: "nonce: create proof in this workspace", WorkDir: "/tmp/ws", Model: "glm-5.3"}
}
func isolateTemp(t *testing.T) string { t.Helper(); d := t.TempDir(); t.Setenv("TMPDIR", d); return d }
func lrNoLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// Validates the actual version and mission sinks, and writes the correlated
// rollout exactly where the emitted CODEX_HOME directs the harness to write it.
type lrSinkRunner struct {
	t     *testing.T
	inner multiRunner
	homes []string
}

func (r *lrSinkRunner) Run(ctx context.Context, s claudecli.RunSpec) (<-chan string, func() error, error) {
	r.t.Helper()
	home, ok := envValue(s.Env, codexHomeVar)
	require.True(r.t, ok)
	st, err := os.Stat(home)
	require.NoError(r.t, err)
	assert.Equal(r.t, os.FileMode(0700), st.Mode().Perm())
	for _, name := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL"} {
		_, found := envValue(s.Env, name)
		assert.False(r.t, found)
	}
	for _, name := range []string{"auth.json", "config.toml"} {
		_, err := os.Stat(filepath.Join(home, name))
		assert.True(r.t, os.IsNotExist(err))
	}
	r.homes = append(r.homes, home)
	if len(s.Args) == 1 && s.Args[0] == "--version" {
		_, found := envValue(s.Env, LunaRouteKeyVar)
		assert.False(r.t, found, "version probe must not receive key")
	} else {
		key, found := envValue(s.Env, LunaRouteKeyVar)
		require.True(r.t, found)
		assert.Equal(r.t, lrKey, key)
		assert.Equal(r.t, lrMission().Prompt, s.Stdin)
		assert.Equal(r.t, lrMission().WorkDir, s.Dir)
		assert.Equal(r.t, 1, countLunaRouteVar(s.Env, LunaRouteKeyVar))
		assert.Equal(r.t, 1, countLunaRouteVar(s.Env, codexHomeVar))
	}
	return r.inner.Run(ctx, s)
}
func countLunaRouteVar(env []string, name string) int {
	n := 0
	for _, s := range env {
		if strings.HasPrefix(s, name+"=") {
			n++
		}
	}
	return n
}
func lrSuccessScript(t *testing.T, r *lrSinkRunner, model, providerID string) runScript {
	s := happyMission()
	s.lines[0] = `{"type":"thread.started","thread_id":"` + rolloutThread + `"}`
	s.onWait = func() {
		home, _ := envValue(r.inner.calls[len(r.inner.calls)-1].Env, codexHomeVar)
		line := `{"type":"turn_context","payload":{"model":"` + model + `","model_provider":"` + providerID + `"}}`
		writeRollout(t, home, rolloutThread, metaLine(rolloutThread), line)
	}
	return s
}
func TestLunaRouteCodex_HappyPath(t *testing.T) {
	tmp := isolateTemp(t)
	t.Setenv("CODEX_HOME", "/operator/private")
	t.Setenv("OPENAI_API_KEY", "sk-ambient")
	t.Setenv("OPENAI_BASE_URL", "https://foreign.example")
	r := &lrSinkRunner{t: t}
	r.inner.scripts = []runScript{compatibleProbe(), lrSuccessScript(t, r, "glm-5.3", "lunaroute")}
	b := lrCodexBinding()
	b.Pinned = true
	res, err := New(Config{Binding: b, Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "glm-5.3", res.Model)
	assert.Equal(t, "Done: edited the workspace.", res.Summary)
	assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Provider: "lunaroute", Source: "codex_turn_context", Kind: engine.EvidenceRoutingIntent}}, res.ModelEvidence)
	require.Len(t, r.inner.calls, 2)
	assert.Equal(t, r.homes[0], r.homes[1])
	assert.NotEqual(t, "/operator/private", r.homes[0])
	assert.Equal(t, []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "-m", "glm-5.3", "-c", `model_provider="lunaroute"`, "-c", `model_providers.lunaroute.name="LunaRoute"`, "-c", `model_providers.lunaroute.base_url="https://gw.lunaroute.com/v1"`, "-c", `model_providers.lunaroute.env_key="LUNAROUTE_API_KEY"`, "-c", `model_providers.lunaroute.wire_api="responses"`, "-c", `review_model="glm-5.3"`, "--cd", "/tmp/ws", "--json"}, r.inner.calls[1].Args)
	assert.NotContains(t, strings.Join(r.inner.calls[1].Args, " "), lrKey)
	lrNoLeftovers(t, tmp)
}
func TestLunaRouteCodex_SubscriptionUnchanged(t *testing.T) {
	tmp := isolateTemp(t)
	t.Setenv("CODEX_HOME", "/operator/codex-home")
	revealed := false
	r := &multiRunner{scripts: []runScript{compatibleProbe(), happyMission()}}
	res, err := New(Config{Binding: codexBinding("gpt-5-codex", &revealed), Runner: r}).RunAgent(context.Background(), lrMission())
	require.NoError(t, err)
	assert.Empty(t, res.ModelEvidence)
	assert.False(t, revealed)
	assert.Equal(t, []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5-codex", "--cd", "/tmp/ws", "--json"}, r.calls[1].Args)
	assert.Equal(t, codexEnv(), r.calls[0].Env)
	assert.Equal(t, codexEnv(), r.calls[1].Env)
	lrNoLeftovers(t, tmp)
}
func TestLunaRouteCodex_ExactEffort(t *testing.T) {
	for _, e := range []provider.Effort{provider.EffortLow, provider.EffortMedium, provider.EffortHigh, provider.EffortXHigh} {
		t.Run(string(e), func(t *testing.T) {
			tmp := isolateTemp(t)
			r := &lrSinkRunner{t: t}
			r.inner.scripts = []runScript{compatibleProbe(), lrSuccessScript(t, r, "glm-5.3", "")}
			b := lrCodexBinding()
			b.Effort = e
			res, err := New(Config{Binding: b, Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
			require.NoError(t, err)
			args := r.inner.calls[1].Args
			assert.Equal(t, []string{"-c", "model_reasoning_effort=" + string(e)}, args[len(args)-5:len(args)-3])
			assert.Empty(t, res.ModelEvidence[0].Provider)
			lrNoLeftovers(t, tmp)
		})
	}
}
func TestLunaRouteCodex_PreflightBeforeIO(t *testing.T) {
	for name, change := range map[string]func(*provider.ResolvedBinding, *provider.CapabilityRules, *engine.AgentMission){
		"unreviewed": func(b *provider.ResolvedBinding, _ *provider.CapabilityRules, m *engine.AgentMission) {
			b.Model = "glm-999.9"
			m.Model = b.Model
		},
		"revoked": func(b *provider.ResolvedBinding, _ *provider.CapabilityRules, m *engine.AgentMission) {
			b.Model = "claude-sonnet-5-lr1"
			m.Model = b.Model
		},
		"mission mismatch": func(_ *provider.ResolvedBinding, _ *provider.CapabilityRules, m *engine.AgentMission) {
			m.Model = "glm-5.3-flash"
		},
		"context": func(b *provider.ResolvedBinding, _ *provider.CapabilityRules, _ *engine.AgentMission) {
			b.ContextWindow = provider.ContextWindow("1m")
		},
		"none": func(b *provider.ResolvedBinding, _ *provider.CapabilityRules, _ *engine.AgentMission) {
			b.Effort = provider.EffortNone
		},
		"max": func(b *provider.ResolvedBinding, _ *provider.CapabilityRules, _ *engine.AgentMission) {
			b.Effort = provider.EffortMax
		},
		"invalid effort": func(b *provider.ResolvedBinding, _ *provider.CapabilityRules, _ *engine.AgentMission) {
			b.Effort = "turbo"
		},
		"missing effort facts": func(b *provider.ResolvedBinding, r *provider.CapabilityRules, _ *engine.AgentMission) {
			b.Effort = provider.EffortHigh
			*r = provider.CapabilityRules{}
		},
		"task incompatible": func(_ *provider.ResolvedBinding, r *provider.CapabilityRules, _ *engine.AgentMission) {
			*r = provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": {Tasks: map[provider.ProfileKind]provider.SupportFact{provider.ProfileKindAgent: {Support: provider.SupportUnsupported, Evidence: "fixture#task"}}}})
		},
		"incompatible": func(_ *provider.ResolvedBinding, r *provider.CapabilityRules, _ *engine.AgentMission) {
			*r = provider.CapabilityRules{}.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": {Backends: map[string]provider.SupportFact{"codex": {Support: provider.SupportUnsupported, Evidence: "fixture#unsupported"}}}})
		},
		"foreign endpoint": func(b *provider.ResolvedBinding, _ *provider.CapabilityRules, _ *engine.AgentMission) {
			b.BaseURL = "https://foreign.example"
		},
	} {
		t.Run(name, func(t *testing.T) {
			tmp := isolateTemp(t)
			b, rules, m := lrCodexBinding(), lrCodexRules(), lrMission()
			revealed := false
			b.RevealKey = func(context.Context) (string, error) { revealed = true; return lrKey, nil }
			change(&b, &rules, &m)
			r := &multiRunner{}
			res, err := New(Config{Binding: b, Runner: r, CapabilityRules: rules}).RunAgent(context.Background(), m)
			require.Error(t, err)
			assert.Nil(t, res)
			assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
			switch name {
			case "unreviewed", "revoked":
				assert.ErrorIs(t, err, provider.ErrLunaRouteModelSpelling)
			case "mission mismatch":
				assert.ErrorIs(t, err, ErrModelEvidence)
			case "context", "none", "max", "missing effort facts":
				assert.ErrorIs(t, err, provider.ErrLunaRouteAxisUnsupported)
			case "invalid effort":
				assert.ErrorIs(t, err, provider.ErrInvalidEffort)
			case "incompatible", "task incompatible":
				assert.ErrorIs(t, err, provider.ErrLunaRouteIncompatible)
			case "foreign endpoint":
				assert.ErrorIs(t, err, provider.ErrLunaRouteEndpoint)
			}
			assert.False(t, revealed)
			assert.Empty(t, r.calls)
			lrNoLeftovers(t, tmp)
		})
	}
}
func TestLunaRouteCodex_CredentialFailures(t *testing.T) {
	for _, key := range []string{"", "sk-foreign"} {
		tmp := isolateTemp(t)
		b := lrCodexBinding()
		b.RevealKey = func(context.Context) (string, error) { return key, nil }
		r := &multiRunner{}
		_, err := New(Config{Binding: b, Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
		require.ErrorIs(t, err, provider.ErrLunaRouteStoredCredential)
		assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
		assert.NotContains(t, err.Error(), "sk-foreign")
		assert.Empty(t, r.calls)
		lrNoLeftovers(t, tmp)
	}
	b := lrCodexBinding()
	b.RevealKey = func(context.Context) (string, error) {
		return lrKey, errors.Join(context.DeadlineExceeded, errors.New(lrKey))
	}
	tmp := isolateTemp(t)
	r := &multiRunner{}
	_, err := New(Config{Binding: b, Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
	assert.NotContains(t, err.Error(), lrKey)
	assert.Empty(t, r.calls)
	lrNoLeftovers(t, tmp)
}
func TestLunaRouteCodex_EvidenceRequiredOnEverySuccess(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, proof := range []string{"missing", "empty", "drift", "foreign", "unsafe model", "unsafe provider", "wrong session", "no terminal"} {
			t.Run(proof+"/pinned="+strconv.FormatBool(pinned), func(t *testing.T) {
				tmp := isolateTemp(t)
				r := &lrSinkRunner{t: t}
				b := lrCodexBinding()
				b.Pinned = pinned
				s := lrSuccessScript(t, r, b.Model, "lunaroute")
				switch proof {
				case "missing":
					s.onWait = nil
				case "empty":
					s = lrSuccessScript(t, r, "", "lunaroute")
				case "drift":
					s = lrSuccessScript(t, r, "glm-5.3-flash", "lunaroute")
				case "foreign":
					s = lrSuccessScript(t, r, "glm-5.3", "openai")
				case "unsafe model":
					s = lrSuccessScript(t, r, "claude-"+lrKey, "lunaroute")
				case "unsafe provider":
					s = lrSuccessScript(t, r, "glm-5.3", lrKey)
				case "wrong session":
					s.onWait = func() {
						home, _ := envValue(r.inner.calls[1].Env, codexHomeVar)
						writeRollout(t, home, rolloutThread, metaLine(rolloutThread2), ctxLine(b.Model))
					}
				case "no terminal":
					s.lines = s.lines[:len(s.lines)-1]
				}
				r.inner.scripts = []runScript{compatibleProbe(), s}
				res, err := New(Config{Binding: b, Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
				require.ErrorIs(t, err, ErrModelEvidence)
				assert.Nil(t, res)
				assert.NotContains(t, err.Error(), lrKey)
				// paired:4: a completed turn whose private rollout cannot prove the
				// route is a local evidence fault, never a gateway diagnosis trigger;
				// an incomplete turn stays opaque so the wrapper can diagnose it.
				if proof == "no terminal" {
					assert.NotErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
				} else {
					assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
				}
				lrNoLeftovers(t, tmp)
			})
		}
	}
}
func TestLunaRouteCodex_FinalTurnMustComplete(t *testing.T) {
	tmp := isolateTemp(t)
	r := &lrSinkRunner{t: t}
	s := lrSuccessScript(t, r, "glm-5.3", "")
	s.lines = append(s.lines, `{"type":"turn.started"}`)
	r.inner.scripts = []runScript{compatibleProbe(), s}
	res, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
	require.ErrorIs(t, err, ErrModelEvidence)
	assert.NotErrorIs(t, err, provider.ErrLunaRouteLocalFailure, "an unfinished turn is opaque and must stay diagnosable")
	assert.Nil(t, res)
	lrNoLeftovers(t, tmp)
}

func TestLunaRouteCodex_ProcessErrorsSafeAndClean(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, s := range []runScript{{waitErr: &claudecli.StderrError{Err: errors.New("exit status 1"), Summary: lrKey, Tail: []byte(lrKey)}}, {startErr: errors.New(lrKey)}, {waitErr: errors.New(lrKey), lines: []string{`{"type":"error","message":"` + lrKey + `"}`}}, {lines: []string{`{"type":"item.completed","item":` + lrKey}}, {lines: []string{`{"type":"turn.failed","error":{"message":"` + lrKey + `"}}`}}} {
		tmp := isolateTemp(t)
		r := &multiRunner{scripts: []runScript{compatibleProbe(), s}}
		res, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
		require.Error(t, err)
		assert.Nil(t, res)
		assert.NotContains(t, err.Error(), lrKey)
		assert.NotContains(t, logs.String(), lrKey)
		assert.NotErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
		lrNoLeftovers(t, tmp)
	}
}
func TestLunaRouteCodex_CancellationAndPanicCleanup(t *testing.T) {
	tmp := isolateTemp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r := &multiRunner{scripts: []runScript{compatibleProbe(), {blockOnCtx: true}}}
	_, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(ctx, lrMission())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	lrNoLeftovers(t, tmp)
	r = &multiRunner{scripts: []runScript{compatibleProbe(), {onWait: func() { panic("fixture panic") }}}}
	assert.Panics(t, func() {
		_, _ = New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
	})
	lrNoLeftovers(t, tmp)
}
func TestLunaRouteCodex_VersionFailureIsLocalAndClean(t *testing.T) {
	tmp := isolateTemp(t)
	r := &lrSinkRunner{t: t}
	r.inner.scripts = []runScript{{startErr: errors.New(lrKey)}}
	_, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
	require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
	assert.NotContains(t, err.Error(), lrKey)
	assert.Len(t, r.inner.calls, 1)
	lrNoLeftovers(t, tmp)
}
func TestLunaRouteCodex_HomeFailures(t *testing.T) {
	tmp := isolateTemp(t)
	b := lrCodexBinding()
	_, err := prepareLunaRouteLaunchWithFS(context.Background(), b, func(string, string) (string, error) { return "", errors.New(lrKey) }, os.Chmod)
	require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
	assert.NotContains(t, err.Error(), lrKey)
	lrNoLeftovers(t, tmp)
	_, err = prepareLunaRouteLaunchWithFS(context.Background(), b, os.MkdirTemp, func(string, os.FileMode) error { return errors.New(lrKey) })
	require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
	assert.NotContains(t, err.Error(), lrKey)
	lrNoLeftovers(t, tmp)
}
func TestLunaRouteCodex_MkdirFailureBeforeLaunch(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing", "parent"))
	r := &multiRunner{}
	_, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
	require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
	assert.Empty(t, r.calls)
}

func TestLunaRouteCodex_DefaultAllowsUnknownOptionalFacts(t *testing.T) {
	tmp := isolateTemp(t)
	r := &lrSinkRunner{t: t}
	r.inner.scripts = []runScript{compatibleProbe(), lrSuccessScript(t, r, "glm-5.3", "")}
	mission := lrMission()
	mission.Model = ""
	res, err := New(Config{Binding: lrCodexBinding(), Runner: r}).RunAgent(context.Background(), mission)
	require.NoError(t, err, "Codex has no required limit metadata; default settings do not invent optional facts")
	assert.Equal(t, "glm-5.3", res.Model)
	assert.NotEmpty(t, res.ModelEvidence)
	lrNoLeftovers(t, tmp)
}

func TestLunaRouteCodex_ConcurrentHomes(t *testing.T) {
	isolateTemp(t)
	homes := make([]string, 2)
	var wg sync.WaitGroup
	for i := range homes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := &lrSinkRunner{t: t}
			s := lrSuccessScript(t, r, "glm-5.3", "")
			onWait := s.onWait
			s.onWait = func() { homes[i], _ = envValue(r.inner.calls[1].Env, codexHomeVar); onWait() }
			r.inner.scripts = []runScript{compatibleProbe(), s}
			_, err := New(Config{Binding: lrCodexBinding(), Runner: r, CapabilityRules: lrCodexRules()}).RunAgent(context.Background(), lrMission())
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()
	require.NotEmpty(t, homes[0])
	assert.NotEqual(t, homes[0], homes[1])
	for _, home := range homes {
		_, err := os.Stat(home)
		assert.True(t, os.IsNotExist(err))
	}
}
func TestLunaRouteCodex_RealProcessSink(t *testing.T) {
	tmp := isolateTemp(t)
	ws := t.TempDir()
	bin := filepath.Join(t.TempDir(), "codex")
	script := `#!/bin/sh
set -eu
[ -n "$CODEX_HOME" ]
[ ! -e "$CODEX_HOME/auth.json" ]
[ ! -e "$CODEX_HOME/config.toml" ]
[ -z "${OPENAI_API_KEY:-}" ]
[ -z "${OPENAI_BASE_URL:-}" ]
if [ "$1" = --version ]; then
 [ -z "${LUNAROUTE_API_KEY:-}" ]
 printf 'codex-cli 0.167.0\n'
 exit 0
fi
[ "$LUNAROUTE_API_KEY" = lr_live_codexTestKey123 ]
[ "$1" = exec ] && [ "$2" = --dangerously-bypass-approvals-and-sandbox ]
[ "$3" = -m ] && [ "$4" = glm-5.3 ]
model="$4"
shift 4
for expected in 'model_provider="lunaroute"' 'model_providers.lunaroute.name="LunaRoute"' 'model_providers.lunaroute.base_url="https://gw.lunaroute.com/v1"' 'model_providers.lunaroute.env_key="LUNAROUTE_API_KEY"' 'model_providers.lunaroute.wire_api="responses"' 'review_model="glm-5.3"' 'model_reasoning_effort=high'; do
 [ "$1" = -c ] && [ "$2" = "$expected" ]
 shift 2
done
[ "$1" = --cd ] && [ "$(cd "$2" && pwd -P)" = "$(pwd -P)" ] && [ "$3" = --json ]
[ "$(/bin/cat)" = 'nonce: create proof in this workspace' ]
printf 'nonce proof' > codex-proof.txt
mkdir -p "$CODEX_HOME/sessions/2026/11/11"
printf '{"type":"session_meta","payload":{"id":"lr-thread-0001"}}\n{"type":"turn_context","payload":{"model":"%s"}}\n' "$model" > "$CODEX_HOME/sessions/2026/11/11/rollout-test-lr-thread-0001.jsonl"
printf '{"type":"thread.started","thread_id":"lr-thread-0001"}\n{"type":"item.completed","item":{"type":"agent_message","text":"command completed"}}\n{"type":"turn.completed"}\n'
`
	require.NoError(t, os.WriteFile(bin, []byte(script), 0700))
	b := lrCodexBinding()
	b.Effort = provider.EffortHigh
	mission := lrMission()
	mission.WorkDir = ws
	res, err := New(Config{Binding: b, BinaryPath: bin, CapabilityRules: lrCodexRules(), MaxResponseBytes: 1 << 20}).RunAgent(context.Background(), mission)
	require.NoError(t, err)
	proof, err := os.ReadFile(filepath.Join(ws, "codex-proof.txt"))
	require.NoError(t, err)
	assert.Equal(t, "nonce proof", string(proof))
	assert.Equal(t, "command completed", res.Summary)
	assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Source: "codex_turn_context", Kind: engine.EvidenceRoutingIntent}}, res.ModelEvidence)
	lrNoLeftovers(t, tmp)
}

func TestLunaRouteCodex_NoUsabilityProbe(t *testing.T) {
	r := &multiRunner{}
	_, _, err := New(Config{Binding: lrCodexBinding(), Runner: r}).ProbeExecutedModel(context.Background(), "", "/tmp/ws")
	require.ErrorIs(t, err, ErrModelEvidence)
	assert.Contains(t, err.Error(), "pinned-only")
	assert.Empty(t, r.calls)
}
