package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeRunner scripts `--version`, the probe `run` and its `export`, recording
// every launch so the tests can assert what reached the subprocess.
type probeRunner struct {
	version   string
	run       []string
	runErr    error
	runWait   error
	export    string
	exportErr error
	calls     []claudecli.RunSpec

	// D12 resolved-config simulation for `debug config`. inherited is every MCP
	// server the global, $HOME/.opencode and managed layers define (name ->
	// enabled flag as authored; nil = unset). The overlay in
	// OPENCODE_CONFIG_CONTENT is deep-merged over it, as the real binary does.
	inherited     map[string]*bool
	ignoreOverlay bool   // a binary that does not honor the overlay
	sprout        string // a server that appears only once an overlay is applied
	debugRaw      string // replaces the debug output verbatim (malformed/oversized)
	debugErr      error
}

func (r *probeRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls = append(r.calls, spec)
	var lines []string
	var waitErr error
	switch {
	case slices.Equal(spec.Args, []string{"--version"}):
		lines = []string{"opencode " + r.version}
	case len(spec.Args) > 0 && spec.Args[0] == "run":
		if r.runErr != nil {
			return nil, nil, r.runErr
		}
		lines, waitErr = r.run, r.runWait
	case len(spec.Args) > 0 && spec.Args[0] == "export":
		lines, waitErr = strings.Split(r.export, "\n"), r.exportErr
	case len(spec.Args) > 1 && spec.Args[0] == "debug" && spec.Args[1] == "config":
		if r.debugRaw != "" {
			lines = strings.Split(r.debugRaw, "\n")
		} else {
			lines = []string{r.resolvedConfig(spec)}
		}
		waitErr = r.debugErr
	}
	ch := make(chan string, len(lines))
	for _, line := range lines {
		ch <- line
	}
	close(ch)
	return ch, func() error { return waitErr }, nil
}

// resolvedConfig is what `opencode debug config` prints: inherited servers with
// the overlay's per-server fields deep-merged on top.
func (r *probeRunner) resolvedConfig(spec claudecli.RunSpec) string {
	overlay := overlayMCP(spec.Env)
	mcp := map[string]map[string]any{}
	for name, enabled := range r.inherited {
		entry := map[string]any{"type": "local", "command": []string{"/bin/sh", "-c", "touch MARKER-" + name}}
		if enabled != nil {
			entry["enabled"] = *enabled
		}
		mcp[name] = entry
	}
	if r.sprout != "" && len(overlay) > 0 {
		mcp[r.sprout] = map[string]any{"type": "remote", "url": "https://sprout.example.test/mcp"}
	}
	if !r.ignoreOverlay {
		for name, fields := range overlay {
			if entry, ok := mcp[name]; ok {
				for k, v := range fields {
					entry[k] = v
				}
			}
		}
	}
	b, _ := json.Marshal(map[string]any{"$schema": opencodeConfigSchema, "mcp": mcp})
	return string(b)
}

// overlayMCP is the mcp block of the OPENCODE_CONFIG_CONTENT a launch carries.
func overlayMCP(env []string) map[string]map[string]any {
	for _, kv := range env {
		if raw, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			var doc struct {
				MCP map[string]map[string]any `json:"mcp"`
			}
			_ = json.Unmarshal([]byte(raw), &doc)
			return doc.MCP
		}
	}
	return nil
}

func overlayPermission(env []string) map[string]string {
	for _, kv := range env {
		if raw, ok := strings.CutPrefix(kv, "OPENCODE_CONFIG_CONTENT="); ok {
			var doc struct {
				Permission map[string]string `json:"permission"`
			}
			_ = json.Unmarshal([]byte(raw), &doc)
			return doc.Permission
		}
	}
	return nil
}

func ptrBool(b bool) *bool { return &b }

// hostileInheritance is a server per inherited layer: global XDG config
// (explicitly enabled), $HOME/.opencode (enabled by default, no field) and the
// managed layer.
func hostileInheritance() map[string]*bool {
	return map[string]*bool{"hostile-global": ptrBool(true), "hostile-dotdir": nil, "hostile-managed": ptrBool(true)}
}

func pinnedProbeBinding() provider.ResolvedBinding {
	b := anthropicBinding("secret-evidence", nil)
	b.Model, b.Pinned = "anthropic/claude-sonnet-4-5", true
	return b
}

func exactProbeRunner() *probeRunner {
	return &probeRunner{version: minOpencodeProbeVersion, run: []string{exactRunEvent},
		export:    modelExport("ses_run", "msg_answer", "assistant", "anthropic", "claude-sonnet-4-5"),
		inherited: hostileInheritance()}
}

func (r *probeRunner) launched(sub string) []claudecli.RunSpec {
	var out []claudecli.RunSpec
	for _, c := range r.calls {
		if len(c.Args) > 0 && c.Args[0] == sub {
			out = append(out, c)
		}
	}
	return out
}

// D12 (decided 2026-10-21): an OpenCode usability probe reads OpenCode's fully
// resolved configuration (every layer), overrides EVERY MCP server it names with
// enabled:false through the inline overlay, re-reads the effective config to
// prove none is enabled, and only then runs under --pure (no external plugins)
// with a deny-all permission block. Identity proof is unchanged: the correlated
// export must name the requested provider/model.

// HAPPY — servers inherited from global, $HOME/.opencode and managed layers are
// all disabled in the probe's run, and the exact export confirms the model.
func TestModelProbeOpenCodeD12DisablesEveryResolvedMCP(t *testing.T) {
	r := exactProbeRunner()
	p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r, AllowAllTools: true})
	model, session, err := p.ProbeExecutedModel(context.Background(), "Reply with the single word: ok", "/tmp/probe")
	require.NoError(t, err)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", model)
	assert.Equal(t, "ses_run", session)

	debug := r.launched("debug")
	require.Len(t, debug, 2, "resolve the inherited servers, then verify the effective config")
	for _, c := range debug {
		assert.Equal(t, []string{"debug", "config", "--pure"}, c.Args)
		assert.Equal(t, "/tmp/probe", c.Dir, "the resolved config must be the probe cwd's")
	}
	runs := r.launched("run")
	require.Len(t, runs, 1)
	run := runs[0]
	assert.Contains(t, run.Args, "--pure", "external plugins must not load")
	assert.Equal(t, "/tmp/probe", run.Dir)
	assert.Equal(t, map[string]string{"*": "deny"}, overlayPermission(run.Env), "allow-all knob must not reach a probe")
	mcp := overlayMCP(run.Env)
	require.Len(t, mcp, 3)
	for _, name := range []string{"hostile-global", "hostile-dotdir", "hostile-managed"} {
		assert.Equal(t, map[string]any{"enabled": false}, mcp[name], name)
	}
	exports := r.launched("export")
	require.Len(t, exports, 1)
	assert.Equal(t, []string{"export", "ses_run", "--pure"}, exports[0].Args, "the export must not load plugins either")
	assert.Equal(t, overlayMCP(run.Env), overlayMCP(exports[0].Env))
}

// SUCCESS — a host with no MCP servers still probes, under --pure.
func TestModelProbeOpenCodeD12NoInheritedServers(t *testing.T) {
	r := exactProbeRunner()
	r.inherited = nil
	p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r})
	model, _, err := p.ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
	require.NoError(t, err)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", model)
	require.Len(t, r.launched("run"), 1)
	assert.Contains(t, r.launched("run")[0].Args, "--pure")
}

// FAILURE — isolation that cannot be verified refuses the probe before any run:
// a binary that ignores the overlay, a server that appears only after it, or an
// overlay that leaves a server enabled.
func TestModelProbeOpenCodeD12UnverifiedIsolationRefuses(t *testing.T) {
	for name, mutate := range map[string]func(*probeRunner){
		"overlay ignored":       func(r *probeRunner) { r.ignoreOverlay = true },
		"server sprouts":        func(r *probeRunner) { r.sprout = "late-remote" },
		"resolution malformed":  func(r *probeRunner) { r.debugRaw = "not json" },
		"resolution not object": func(r *probeRunner) { r.debugRaw = `{"mcp":["a"]}` },
		"resolution fails":      func(r *probeRunner) { r.debugErr = errors.New("exit status 1 secret-evidence") },
		"resolution oversized": func(r *probeRunner) {
			r.debugRaw = `{"mcp":{},"pad":"` + strings.Repeat("x", int(maxModelExportBytes)) + `"}`
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := exactProbeRunner()
			mutate(r)
			p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r})
			model, _, err := p.ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
			require.ErrorIs(t, err, ErrProbeIsolationUnverified)
			assert.Empty(t, model)
			assert.NotContains(t, err.Error(), "secret-evidence")
			assert.Empty(t, r.launched("run"), "no probe run without verified isolation")
		})
	}
}

// FAILURE — identity proof is unchanged: a fallback export never confirms.
func TestModelProbeOpenCodeD12WrongModelIsNotConfirmed(t *testing.T) {
	r := exactProbeRunner()
	r.export = modelExport("ses_run", "msg_answer", "assistant", "anthropic", "claude-haiku-4-5")
	p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r})
	model, _, err := p.ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
	require.ErrorIs(t, err, ErrModelExecutionEvidence)
	assert.Empty(t, model)
}

// ERROR — missing load-bearing inputs fail before any process, and an
// unverified (older) binary never probes. Errors carry no credential.
func TestModelProbeOpenCodeD12InputGuards(t *testing.T) {
	const secret = "secret-evidence"
	noModel := pinnedProbeBinding()
	noModel.Model = ""
	unknown := pinnedProbeBinding()
	unknown.Provider = "mystery"
	noKey := anthropicBinding("", errors.New("vault sealed "+secret))
	noKey.Model = "anthropic/claude-sonnet-4-5"
	for name, tc := range map[string]struct {
		binding provider.ResolvedBinding
		workDir string
		version string
		anyRun  bool
	}{
		"empty work dir":     {pinnedProbeBinding(), "", minOpencodeProbeVersion, false},
		"empty model":        {noModel, "/tmp/probe", minOpencodeProbeVersion, false},
		"unknown provider":   {unknown, "/tmp/probe", minOpencodeProbeVersion, false},
		"credential failure": {noKey, "/tmp/probe", minOpencodeProbeVersion, false},
		"unverified binary":  {pinnedProbeBinding(), "/tmp/probe", "1.19.2", true},
	} {
		t.Run(name, func(t *testing.T) {
			r := exactProbeRunner()
			r.version = tc.version
			p := New(Config{Binding: tc.binding, BinaryPath: "opencode", Runner: r})
			model, _, err := p.ProbeExecutedModel(context.Background(), "say ok", tc.workDir)
			require.Error(t, err)
			assert.Empty(t, model)
			assert.NotContains(t, err.Error(), secret)
			assert.Empty(t, r.launched("run"))
			assert.Empty(t, r.launched("debug"))
			if !tc.anyRun {
				assert.Empty(t, r.calls, "no process before inputs validate")
			}
		})
	}
}

// EDGE — the restrictions are the probe's alone: an ordinary pinned mission
// keeps its own posture (no --pure, no MCP overrides, no config resolution)
// and its export check.
func TestModelProbeOpenCodeD12LeavesMissionsUnrestricted(t *testing.T) {
	r := exactProbeRunner()
	p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r, AllowAllTools: true})
	got, err := callMission(context.Background(), p)
	require.NoError(t, err)
	assert.Equal(t, "anthropic/claude-sonnet-4-5", got)
	assert.Empty(t, r.launched("debug"))
	run := r.launched("run")
	require.Len(t, run, 1)
	assert.NotContains(t, run[0].Args, "--pure")
	assert.Empty(t, overlayMCP(run[0].Env))
	assert.Equal(t, []string{"export", "ses_run"}, r.launched("export")[0].Args)
}

// SECURITY — the discovery probe's `--version` check runs with an explicit
// allowlisted env and the private probe cwd. A nil Env would inherit the whole
// server environment, so a synthetic master key in the parent must not reach it.
func TestModelProbeOpenCodeVersionCheckDoesNotInheritServerEnv(t *testing.T) {
	t.Setenv("ARBITRON_MASTER_KEY", "synthetic-master-key-canary")
	r := exactProbeRunner()
	p := New(Config{Binding: pinnedProbeBinding(), BinaryPath: "opencode", Runner: r})
	_, _, err := p.ProbeExecutedModel(context.Background(), "say ok", "/tmp/probe")
	require.NoError(t, err)

	var version []claudecli.RunSpec
	for _, c := range r.calls {
		if slices.Equal(c.Args, []string{"--version"}) {
			version = append(version, c)
		}
	}
	require.Len(t, version, 1)
	assert.NotNil(t, version[0].Env, "a nil Env inherits the server environment")
	assert.Equal(t, "/tmp/probe", version[0].Dir, "the version check runs in the private probe cwd")
	for _, kv := range version[0].Env {
		assert.NotContains(t, kv, "synthetic-master-key-canary")
		assert.False(t, strings.HasPrefix(kv, "ARBITRON_MASTER_KEY="))
	}
}
