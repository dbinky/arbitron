package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
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

const lrPiKey = "lr_private_pi_test_key"
const lrAssistant = `{"type":"message_end","message":{"role":"assistant","provider":"lunaroute","model":"glm-5.3","stopReason":"stop","content":[{"type":"text","text":"done"}],"usage":{"input":3,"output":2}}}`

func lrRules() provider.CapabilityRules {
	return provider.NewCapabilityRules(nil).WithLunaRouteModels(map[string]provider.LunaRouteModel{
		"glm-5.3": {ID: "glm-5.3", ContextTokens: provider.LimitFact{Tokens: 202752, Evidence: "fixture#context"}, OutputTokens: provider.LimitFact{Tokens: 32768, Evidence: "fixture#output"}, Reasoning: provider.SupportFact{Support: provider.SupportSupported, Evidence: "fixture#reasoning"}, Efforts: map[string]provider.EffortFact{"pi": {Known: true, Levels: []provider.Effort{provider.EffortHigh}, Evidence: "fixture#effort"}}},
	})
}
func lrPiBinding() provider.ResolvedBinding {
	return provider.ResolvedBinding{BackendExplicit: true, Kind: provider.ProfileKindAgent, Provider: "lunaroute", BaseURL: "https://gw.lunaroute.com/", Backend: "pi", Model: "glm-5.3", ConnectionRef: "lr", RevealKey: func(context.Context) (string, error) { return lrPiKey, nil }}
}
func lrPiProvider(r claudecli.CommandRunner, b provider.ResolvedBinding, rules provider.CapabilityRules) *Provider {
	return New(Config{Binding: b, BinaryPath: "pi", Runner: r, CapabilityRules: rules})
}
func piMission() engine.AgentMission {
	return engine.AgentMission{Prompt: "create a file", WorkDir: "/tmp/ws"}
}
func isolatePiTemp(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	t.Setenv("TMPDIR", d)
	return d
}
func piLeftovers(t *testing.T, d string) []string {
	t.Helper()
	p, e := filepath.Glob(filepath.Join(d, "arbitron-pi-lunaroute-*"))
	require.NoError(t, e)
	return p
}

type capturingRunner struct {
	inner             *multiRunner
	dir               string
	dirInfo, fileInfo os.FileInfo
	modelsJSON        []byte
	panicOnRun        bool
	onRun             func()
}

func (c *capturingRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	if len(spec.Args) > 0 && spec.Args[0] != "--version" {
		c.dir, _ = envValue(spec.Env, PiAgentDirVar)
		c.dirInfo, _ = os.Stat(c.dir)
		c.fileInfo, _ = os.Stat(filepath.Join(c.dir, "models.json"))
		c.modelsJSON, _ = os.ReadFile(filepath.Join(c.dir, "models.json"))
		if c.panicOnRun {
			panic("fixture panic")
		}
		if c.onRun != nil {
			c.onRun()
		}
	}
	return c.inner.Run(ctx, spec)
}
func lrPiRunner(lines ...string) *capturingRunner {
	return &capturingRunner{inner: &multiRunner{scripts: []runScript{lunaRouteCompatibleProbe(), {lines: append([]string{sessionLine}, lines...)}}}}
}

func TestLunaRoutePi_HappyPath(t *testing.T) {
	tmp := isolatePiTemp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OPENAI_API_KEY", "sk-ambient")
	t.Setenv(PiAgentDirVar, "/ambient/pi")
	settings := filepath.Join(home, ".pi/agent/settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(settings), 0700))
	require.NoError(t, os.WriteFile(settings, []byte(`{"defaultProvider":"openai"}`), 0600))
	b := lrPiBinding()
	b.Effort = provider.EffortHigh
	r := lrPiRunner(lrAssistant)
	res, err := lrPiProvider(r, b, lrRules()).RunAgent(context.Background(), piMission())
	require.NoError(t, err)
	assert.Equal(t, "done", res.Summary)
	assert.Equal(t, []engine.ModelEvidence{{Model: "glm-5.3", Provider: "lunaroute", Source: "pi_message_end", Kind: engine.EvidenceReportedExecution}}, res.ModelEvidence)
	versionSpec := r.inner.calls[0]
	assert.Equal(t, []string{"--version"}, versionSpec.Args)
	_, hasKey := envValue(versionSpec.Env, LunaRouteKeyVar)
	assert.False(t, hasKey, "version probe never receives the credential")
	versionDir, ok := envValue(versionSpec.Env, PiAgentDirVar)
	require.True(t, ok)
	assert.Equal(t, r.dir, versionDir)
	spec := r.inner.calls[1]
	assert.Equal(t, []string{"--print", "--mode", "json", "--no-session", "--model", "lunaroute/glm-5.3", "--thinking", "high"}, spec.Args)
	key, ok := envValue(spec.Env, LunaRouteKeyVar)
	require.True(t, ok)
	assert.Equal(t, lrPiKey, key)
	for _, name := range []string{LunaRouteKeyVar, PiAgentDirVar} {
		n := 0
		for _, v := range spec.Env {
			if strings.HasPrefix(v, name+"=") {
				n++
			}
		}
		assert.Equal(t, 1, n)
	}
	_, ok = envValue(spec.Env, "OPENAI_API_KEY")
	assert.False(t, ok)
	require.NotNil(t, r.dirInfo)
	require.NotNil(t, r.fileInfo)
	assert.Equal(t, os.FileMode(0700), r.dirInfo.Mode().Perm())
	assert.Equal(t, os.FileMode(0600), r.fileInfo.Mode().Perm())
	assert.True(t, strings.HasPrefix(r.dir, tmp))
	var got map[string]any
	require.NoError(t, json.Unmarshal(r.modelsJSON, &got))
	assert.JSONEq(t, `{"providers":{"lunaroute":{"baseUrl":"https://gw.lunaroute.com/v1","api":"openai-completions","apiKey":"$LUNAROUTE_API_KEY","authHeader":true,"models":[{"id":"glm-5.3","name":"glm-5.3","reasoning":true,"input":["text"],"contextWindow":202752,"maxTokens":32768}]}}}`, string(r.modelsJSON))
	assert.NotContains(t, string(r.modelsJSON), lrPiKey)
	assert.NotContains(t, strings.Join(spec.Args, " "), lrPiKey)
	assert.Empty(t, piLeftovers(t, tmp))
	raw, err := os.ReadFile(settings)
	require.NoError(t, err)
	assert.Equal(t, `{"defaultProvider":"openai"}`, string(raw))
}

func TestLunaRoutePi_NonLunaRouteUnchanged(t *testing.T) {
	tmp := isolatePiTemp(t)
	mr := &multiRunner{scripts: []runScript{lunaRouteCompatibleProbe(), happyMission()}}
	b := piBinding("anthropic/claude-sonnet-5", provider.EffortHigh)
	_, err := lrPiProvider(mr, b, lrRules()).RunAgent(context.Background(), mission())
	require.NoError(t, err)
	args, err := buildArgs(b)
	require.NoError(t, err)
	env, err := piEnv(context.Background(), b)
	require.NoError(t, err)
	assert.Equal(t, args, mr.calls[1].Args)
	assert.Equal(t, env, mr.calls[1].Env)
	assert.Empty(t, piLeftovers(t, tmp))
}

func TestLunaRoutePi_RefusalsBeforeIO(t *testing.T) {
	for _, name := range []string{"missing limits", "one limit", "reasoning", "effort unknown", "effort unsupported", "context", "unreviewed", "revoked", "prefixed", "background", "task", "backend", "endpoint", "mission model", "foreign key", "reveal"} {
		t.Run(name, func(t *testing.T) {
			tmp := isolatePiTemp(t)
			b := lrPiBinding()
			rules := lrRules()
			m, _ := rules.LunaRouteModel(b.Model)
			want := provider.ErrLunaRouteLimitsUnknown
			mission := piMission()
			reveals := 0
			b.RevealKey = func(context.Context) (string, error) { reveals++; return lrPiKey, nil }
			switch name {
			case "missing limits":
				rules = provider.CapabilityRules{}
			case "one limit":
				m.OutputTokens = provider.LimitFact{}
			case "reasoning":
				m.Reasoning = provider.SupportFact{}
			case "effort unknown":
				m.Efforts = nil
				b.Effort = provider.EffortHigh
				want = provider.ErrLunaRouteAxisUnsupported
			case "effort unsupported":
				b.Effort = provider.EffortMax
				want = provider.ErrLunaRouteAxisUnsupported
			case "context":
				b.ContextWindow = provider.ContextWindow("1m")
				want = provider.ErrLunaRouteAxisUnsupported
			case "unreviewed":
				b.Model = "glm-999.9"
				want = provider.ErrLunaRouteModelSpelling
			case "revoked":
				b.Model = "claude-sonnet-5-lr1"
				want = provider.ErrLunaRouteModelSpelling
			case "prefixed":
				b.Model = "lunaroute/glm-5.3"
				want = provider.ErrLunaRouteModelSpelling
			case "background":
				b.Model = "glm-5.3-background"
				want = provider.ErrLunaRouteModelSpelling
			case "task":
				m.Tasks = map[provider.ProfileKind]provider.SupportFact{provider.ProfileKindAgent: {Support: provider.SupportUnsupported, Evidence: "fixture#task"}}
				want = provider.ErrLunaRouteIncompatible
			case "backend":
				m.Backends = map[string]provider.SupportFact{"pi": {Support: provider.SupportUnsupported, Evidence: "fixture#backend"}}
				want = provider.ErrLunaRouteIncompatible
			case "endpoint":
				b.BaseURL = "https://evil.example"
				want = provider.ErrLunaRouteEndpoint
			case "mission model":
				mission.Model = "glm-5.3-flash"
				want = ErrModelEvidence
			case "foreign key":
				b.RevealKey = func(context.Context) (string, error) { reveals++; return "sk-foreign", nil }
				want = provider.ErrLunaRouteStoredCredential
			case "reveal":
				b.RevealKey = func(context.Context) (string, error) { reveals++; return "", context.DeadlineExceeded }
				want = context.DeadlineExceeded
			}
			if name != "missing limits" {
				rules = rules.WithLunaRouteModels(map[string]provider.LunaRouteModel{"glm-5.3": m})
			}
			mr := &multiRunner{}
			_, err := lrPiProvider(mr, b, rules).RunAgent(context.Background(), mission)
			require.ErrorIs(t, err, want)
			assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
			assert.Empty(t, mr.calls)
			assert.Empty(t, piLeftovers(t, tmp))
			assert.NotContains(t, err.Error(), "sk-foreign")
			if name != "foreign key" && name != "reveal" {
				assert.Zero(t, reveals)
			}
		})
	}
}

func TestLunaRoutePi_ReasoningIndependentOfEffort(t *testing.T) {
	for _, support := range []provider.Support{provider.SupportSupported, provider.SupportUnsupported} {
		rules := lrRules()
		m, _ := rules.LunaRouteModel("glm-5.3")
		m.Reasoning.Support = support
		m.Efforts = nil
		b := lrPiBinding()
		if support == provider.SupportUnsupported {
			b.Effort = provider.EffortNone
			m.Efforts = map[string]provider.EffortFact{"pi": {Known: true, Levels: []provider.Effort{provider.EffortNone}, Evidence: "fixture#off"}}
		}
		rules = rules.WithLunaRouteModels(map[string]provider.LunaRouteModel{b.Model: m})
		r := lrPiRunner(lrAssistant)
		_, err := lrPiProvider(r, b, rules).RunAgent(context.Background(), piMission())
		require.NoError(t, err)
		var def piModelsFile
		require.NoError(t, json.Unmarshal(r.modelsJSON, &def))
		assert.Equal(t, support == provider.SupportSupported, def.Providers["lunaroute"].Models[0].Reasoning)
		if support == provider.SupportUnsupported {
			assert.Equal(t, []string{"--thinking", "off"}, r.inner.calls[1].Args[6:])
		}
	}
}

func TestLunaRoutePi_CleanupAndSafeErrors(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(old)
	for _, pinned := range []bool{false, true} {
		for _, name := range []string{"turn", "wait", "start", "parse", "cancel", "version", "panic"} {
			t.Run(name+map[bool]string{false: " unpinned", true: " pinned"}[pinned], func(t *testing.T) {
				tmp := isolatePiTemp(t)
				b := lrPiBinding()
				b.Pinned = pinned
				r := lrPiRunner(lrAssistant)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch name {
				case "turn":
					r.inner.scripts[1].lines = []string{strings.Replace(lrAssistant, `"stopReason":"stop"`, `"stopReason":"error","errorMessage":"`+lrPiKey+`"`, 1)}
				case "wait":
					r.inner.scripts[1].waitErr = &claudecli.StderrError{Err: errors.New("exit 1"), Tail: []byte(lrPiKey), Summary: lrPiKey}
				case "start":
					r.inner.scripts[1].startErr = errors.New(lrPiKey)
				case "parse":
					r.inner.scripts[1].lines = []string{"not-json " + lrPiKey}
				case "cancel":
					r.inner.scripts[1].blockOnCtx = true
					time.AfterFunc(50*time.Millisecond, cancel)
				case "version":
					r.inner.scripts[0].startErr = errors.New(lrPiKey)
				case "panic":
					r.panicOnRun = true
				}
				p := lrPiProvider(r, b, lrRules())
				if name == "panic" {
					assert.Panics(t, func() { _, _ = p.RunAgent(ctx, piMission()) })
				} else {
					_, err := p.RunAgent(ctx, piMission())
					require.Error(t, err)
					assert.NotContains(t, err.Error(), lrPiKey)
					if name == "turn" {
						assert.ErrorIs(t, err, ErrTurnFailed)
					}
					if name == "version" {
						assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
					}
				}
				if name == "cancel" {
					require.NotEmpty(t, r.dir, "cancellation must clean an already-launched mission")
				}
				assert.Empty(t, piLeftovers(t, tmp))
				assert.NotContains(t, logs.String(), lrPiKey)
			})
		}
	}
}

func TestLunaRoutePi_ConcurrentDistinctDirs(t *testing.T) {
	tmp := isolatePiTemp(t)
	rs := []*capturingRunner{lrPiRunner(lrAssistant), lrPiRunner(lrAssistant)}
	var wg sync.WaitGroup
	for _, r := range rs {
		wg.Add(1)
		go func(r *capturingRunner) {
			defer wg.Done()
			_, err := lrPiProvider(r, lrPiBinding(), lrRules()).RunAgent(context.Background(), piMission())
			assert.NoError(t, err)
		}(r)
	}
	wg.Wait()
	require.NotEmpty(t, rs[0].dir)
	assert.NotEqual(t, rs[0].dir, rs[1].dir)
	assert.Empty(t, piLeftovers(t, tmp))
}

func TestLunaRoutePi_ReportedEvidenceEveryEntry(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		for _, line := range []string{
			lrAssistant, "",
			strings.Replace(lrAssistant, "glm-5.3", "", 1),
			strings.Replace(lrAssistant, "glm-5.3", "glm-5.3-flash", 1),
			strings.Replace(lrAssistant, `"provider":"lunaroute"`, `"provider":"openai"`, 1),
			strings.Replace(lrAssistant, `"provider":"lunaroute"`, `"provider":"`+lrPiKey+`"`, 1),
			strings.Replace(lrAssistant, "glm-5.3", "glm-"+lrPiKey, 1),
			strings.Replace(lrAssistant, "glm-5.3", "claude-"+lrPiKey, 1),
			strings.Replace(lrAssistant, "glm-5.3", strings.Repeat("g", 257), 1),
			strings.Replace(lrAssistant, "glm-5.3", `glm-5.3\n`, 1),
			strings.Replace(lrAssistant, "glm-5.3", "glm-5.3 ", 1),
		} {
			b := lrPiBinding()
			b.Pinned = pinned
			r := lrPiRunner(line)
			res, err := lrPiProvider(r, b, lrRules()).RunAgent(context.Background(), piMission())
			if line == lrAssistant {
				require.NoError(t, err)
				require.NotEmpty(t, res.ModelEvidence)
			} else {
				require.ErrorIs(t, err, ErrModelEvidence)
				assert.NotContains(t, err.Error(), lrPiKey)
			}
		}
	}
}

func TestLunaRoutePi_VersionOutputNeverEscapes(t *testing.T) {
	r := lrPiRunner(lrAssistant)
	r.inner.scripts[0].lines = []string{"opaque-version-private-content"}
	_, err := lrPiProvider(r, lrPiBinding(), lrRules()).RunAgent(context.Background(), piMission())
	require.ErrorIs(t, err, ErrIncompatiblePi)
	require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
	assert.NotContains(t, err.Error(), "opaque-version-private-content")
}

func TestLunaRoutePi_RequiresFinalSuccessfulTurn(t *testing.T) {
	for _, line := range []string{strings.Replace(lrAssistant, `"stopReason":"stop"`, `"stopReason":"toolUse"`, 1), strings.Replace(lrAssistant, `"stopReason":"stop",`, "", 1)} {
		r := lrPiRunner(line)
		_, err := lrPiProvider(r, lrPiBinding(), lrRules()).RunAgent(context.Background(), piMission())
		require.ErrorIs(t, err, ErrModelEvidence)
	}
}

func TestLunaRoutePi_BuilderOnlyBackgroundAndOptionalProvider(t *testing.T) {
	b := lrPiBinding()
	b.Model = "glm-5.3-background"
	m, _ := lrRules().LunaRouteModel("glm-5.3")
	m.ID = b.Model
	raw, err := lunaRouteModelsJSON(b, lrRules().WithLunaRouteModels(map[string]provider.LunaRouteModel{b.Model: m}))
	require.NoError(t, err)
	assert.Contains(t, string(raw), b.Model)
	line := strings.Replace(lrAssistant, `"provider":"lunaroute",`, "", 1)
	res, err := lrPiProvider(lrPiRunner(line), lrPiBinding(), lrRules()).RunAgent(context.Background(), piMission())
	require.NoError(t, err)
	assert.Empty(t, res.ModelEvidence[0].Provider, "never invent the reported provider")
}

func TestLunaRoutePi_NoUsabilityProbeAndDirectEnv(t *testing.T) {
	mr := &multiRunner{}
	_, _, err := lrPiProvider(mr, lrPiBinding(), lrRules()).ProbeExecutedModel(context.Background(), "p", "/tmp/ws")
	require.ErrorIs(t, err, ErrModelEvidence)
	assert.Empty(t, mr.calls)
	_, err = piEnv(context.Background(), lrPiBinding())
	require.ErrorIs(t, err, ErrUnknownProvider)
	assert.Contains(t, err.Error(), "per-launch provider definition")
}

func TestLunaRoutePi_FilesystemFailures(t *testing.T) {
	for _, name := range []string{"mkdir", "chmod", "write"} {
		t.Run(name, func(t *testing.T) {
			tmp := isolatePiTemp(t)
			fs := piFileSystem{mkdirTemp: os.MkdirTemp, chmod: os.Chmod, writeFile: os.WriteFile, removeAll: os.RemoveAll}
			removed := 0
			fs.removeAll = func(d string) error { removed++; return os.RemoveAll(d) }
			switch name {
			case "mkdir":
				fs.mkdirTemp = func(string, string) (string, error) { return "", errors.New(lrPiKey) }
			case "chmod":
				fs.chmod = func(string, os.FileMode) error { return errors.New(lrPiKey) }
			case "write":
				fs.writeFile = func(p string, b []byte, m os.FileMode) error {
					require.NoError(t, os.WriteFile(p, b, m))
					return errors.New(lrPiKey)
				}
			}
			_, err := prepareLunaRoutePiLaunchWithFS(context.Background(), lrPiBinding(), lrRules(), fs)
			require.Error(t, err)
			assert.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
			assert.NotContains(t, err.Error(), lrPiKey)
			assert.Empty(t, piLeftovers(t, tmp))
			if name != "mkdir" {
				assert.Equal(t, 1, removed)
			}
		})
	}
	notDir := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(notDir, []byte("x"), 0600))
	t.Setenv("TMPDIR", notDir)
	mr := &multiRunner{}
	_, err := lrPiProvider(mr, lrPiBinding(), lrRules()).RunAgent(context.Background(), piMission())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private agent dir")
	assert.Empty(t, mr.calls)
}

// LunaRoute launches hand the binary a credential and rely on isolation/auth
// controls verified only from minLunaRoutePiVersion. An older binary that still
// satisfies the subscription floor is refused before the key-bearing launch; the
// floor is not applied to subscription use.
func TestLunaRoutePi_VersionFloorIsLunaRouteOnly(t *testing.T) {
	for _, tc := range []struct {
		version string
		ok      bool
	}{
		{minPiVersion, false}, {"1.3.0", false}, {"1.5.1", false},
		{minLunaRoutePiVersion, true}, {"1.5.3", true}, {"2.0.0", true},
		{"unparseable", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			tmp := isolatePiTemp(t)
			r := lrPiRunner(lrAssistant)
			r.inner.scripts[0] = runScript{lines: []string{tc.version}}
			_, err := lrPiProvider(r, lrPiBinding(), lrRules()).RunAgent(context.Background(), piMission())
			if tc.ok {
				require.NoError(t, err)
				require.Len(t, r.inner.calls, 2)
			} else {
				require.ErrorIs(t, err, ErrIncompatiblePi)
				require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
				require.Len(t, r.inner.calls, 1, "only the credential-free version probe ran")
				_, hasKey := envValue(r.inner.calls[0].Env, LunaRouteKeyVar)
				assert.False(t, hasKey)
				assert.NotContains(t, err.Error(), lrPiKey)
			}
			assert.Empty(t, piLeftovers(t, tmp))
		})
	}
}

func capturePiLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

// G15: a private-dir removal failure is logged (secret-free) and the run moves
// on. Preparation rollback keeps returning the original local error.
func TestLunaRoutePi_CleanupFailuresAreLogged(t *testing.T) {
	removeFails := func(string) error { return errors.New("remove failed " + lrPiKey) }
	t.Run("rollback", func(t *testing.T) {
		logs := capturePiLogs(t)
		isolatePiTemp(t)
		fs := piFileSystem{mkdirTemp: os.MkdirTemp, chmod: func(string, os.FileMode) error { return errors.New("chmod failed") }, writeFile: os.WriteFile, removeAll: removeFails}
		_, err := prepareLunaRoutePiLaunchWithFS(context.Background(), lrPiBinding(), lrRules(), fs)
		require.ErrorIs(t, err, provider.ErrLunaRouteLocalFailure)
		assert.Contains(t, err.Error(), "secure private agent dir")
		assert.Contains(t, logs.String(), "private agent dir cleanup")
		assert.NotContains(t, logs.String(), lrPiKey)
	})
	t.Run("after launch", func(t *testing.T) {
		logs := capturePiLogs(t)
		isolatePiTemp(t)
		fs := piFileSystem{mkdirTemp: os.MkdirTemp, chmod: os.Chmod, writeFile: os.WriteFile, removeAll: removeFails}
		launch, err := prepareLunaRoutePiLaunchWithFS(context.Background(), lrPiBinding(), lrRules(), fs)
		require.NoError(t, err)
		launch.cleanup()
		assert.Contains(t, logs.String(), "private agent dir cleanup")
		assert.NotContains(t, logs.String(), lrPiKey)
		require.NoError(t, os.RemoveAll(launch.dir))
	})
	t.Run("successful run keeps its result", func(t *testing.T) {
		logs := capturePiLogs(t)
		tmp := isolatePiTemp(t)
		r := lrPiRunner(lrAssistant)
		r.onRun = func() {
			// A read-only parent makes the real removal fail after the run.
			require.NoError(t, os.Chmod(tmp, 0500))
			t.Cleanup(func() { _ = os.Chmod(tmp, 0700) })
		}
		res, err := lrPiProvider(r, lrPiBinding(), lrRules()).RunAgent(context.Background(), piMission())
		require.NoError(t, err)
		assert.Equal(t, "done", res.Summary)
		assert.Contains(t, logs.String(), "private agent dir cleanup")
		assert.NotContains(t, logs.String(), lrPiKey)
	})
}
