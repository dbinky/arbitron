package backendhealth

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/codex"
)

// scriptRunner replays a fixed sequence of canned subprocess invocations so the
// codex readiness probe (version + login status) runs hermetically. It mirrors
// the multiRunner used inside adapter/codex but is package-local here.
type scriptRunner struct {
	scripts []script
	idx     int
}

type script struct {
	lines    []string
	waitErr  error
	startErr error
}

func (s *scriptRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	var sc script
	if s.idx < len(s.scripts) {
		sc = s.scripts[s.idx]
	}
	s.idx++
	if sc.startErr != nil {
		return nil, nil, sc.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range sc.lines {
			ch <- l
		}
	}()
	return ch, func() error { return sc.waitErr }, nil
}

// captureRunner wraps a scriptRunner and records every RunSpec it receives.
// Used by security tests that need to assert on the child env.
type captureRunner struct {
	inner *scriptRunner
	specs []claudecli.RunSpec
}

func (c *captureRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	c.specs = append(c.specs, spec)
	return c.inner.Run(ctx, spec)
}

// HAPPY — a ready codex backend surfaces a single entry named "codex" carrying
// the "ready" state, so the system/health surface can show it pre-run.
func TestCodexEntry_Ready(t *testing.T) {
	r := &scriptRunner{scripts: []script{
		{lines: []string{"codex-cli 0.137.0"}},      // --version
		{lines: []string{"Logged in using ChatGPT"}}, // login status
	}}
	entry, err := CodexEntry(context.Background(), r)
	require.NoError(t, err)
	assert.Equal(t, "codex", entry.Name)
	assert.Equal(t, string(codex.Ready), entry.State)
}

// STATE — a not-installed host surfaces the "not-installed" state, not an error.
func TestCodexEntry_NotInstalled(t *testing.T) {
	r := &scriptRunner{scripts: []script{
		{startErr: errors.New(`exec: "codex": not found`)},
	}}
	entry, err := CodexEntry(context.Background(), r)
	require.NoError(t, err)
	assert.Equal(t, "codex", entry.Name)
	assert.Equal(t, string(codex.NotInstalled), entry.State)
}

// STATE — a present-but-logged-out host surfaces "not-logged-in".
func TestCodexEntry_NotLoggedIn(t *testing.T) {
	r := &scriptRunner{scripts: []script{
		{lines: []string{"codex-cli 0.137.0"}},
		{lines: []string{"Not logged in"}, waitErr: errors.New("exit status 1")},
	}}
	entry, err := CodexEntry(context.Background(), r)
	require.NoError(t, err)
	assert.Equal(t, string(codex.NotLoggedIn), entry.State)
}

// ERROR — a genuine login-probe START fault (after version succeeded) is
// surfaced as an error so the handler can degrade gracefully without inventing
// a false "ready" verdict.
func TestCodexEntry_ProbeFault(t *testing.T) {
	r := &scriptRunner{scripts: []script{
		{lines: []string{"codex-cli 0.137.0"}},
		{startErr: errors.New("fork/exec: resource temporarily unavailable")},
	}}
	_, err := CodexEntry(context.Background(), r)
	require.Error(t, err)
}

// SURFACE — Backends appends the codex entry; on a probe fault it returns the
// list UNCHANGED (best-effort: a health fault must never break the whole
// system_config read).
func TestBackends_AppendsCodex(t *testing.T) {
	r := &scriptRunner{scripts: []script{
		{lines: []string{"codex-cli 0.137.0"}},
		{lines: []string{"Logged in using ChatGPT"}},
	}}
	out := Backends(context.Background(), r)
	require.Len(t, out, 1)
	assert.Equal(t, "codex", out[0].Name)
	assert.Equal(t, "ready", out[0].State)
}

func TestBackends_FaultOmitsEntry(t *testing.T) {
	r := &scriptRunner{scripts: []script{
		{lines: []string{"codex-cli 0.137.0"}},
		{startErr: errors.New("fork/exec fault")},
	}}
	out := Backends(context.Background(), r)
	assert.Empty(t, out, "a probe fault must not surface a misleading entry, but must not break the read")
}

// FA17 PAIRED (§10+§11) — all three ReadinessState enum values produced by
// codex.Readiness must flow correctly through CodexEntry into a
// config.RedactedBackend with BOTH Name AND State populated. The entry then
// appears on the RedactedConfig.Backends list returned to both read surfaces
// (MCP system_config + HTTP GET /system/config). This table test pins the
// full §10→§11 flow for every state so a rename or mapping gap in either
// layer is caught immediately.
func TestCodexEntry_AllStatesFlowToRedactedBackend(t *testing.T) {
	cases := []struct {
		name    string
		scripts []script
		want    string
	}{
		{
			name: "ready",
			scripts: []script{
				{lines: []string{"codex-cli 0.137.0"}},
				{lines: []string{"Logged in using ChatGPT"}},
			},
			want: string(codex.Ready),
		},
		{
			name:    "not-installed (start error)",
			scripts: []script{{startErr: errors.New(`exec: "codex": not found`)}},
			want:    string(codex.NotInstalled),
		},
		{
			name: "not-logged-in",
			scripts: []script{
				{lines: []string{"codex-cli 0.137.0"}},
				{lines: []string{"Not logged in"}, waitErr: errors.New("exit status 1")},
			},
			want: string(codex.NotLoggedIn),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptRunner{scripts: tc.scripts}
			entry, err := CodexEntry(context.Background(), r)
			require.NoError(t, err, "clean readiness state must never be an error")

			// Both fields must be correct: Name is the correlation key the UI uses to
			// map a health entry to a profile's backend; State is the opaque enum
			// value that appears on the RedactedConfig.Backends wire surface.
			assert.Equal(t, "codex", entry.Name, "entry Name must equal codexBackendName for state %q", tc.want)
			assert.Equal(t, tc.want, entry.State, "entry State must carry the enum string for state %q", tc.want)

			// Full surface: the entry must also appear in Backends() for non-fault states.
			r2 := &scriptRunner{scripts: tc.scripts}
			out := Backends(context.Background(), r2)
			require.Len(t, out, 1, "Backends must include the codex entry for state %q", tc.want)
			assert.Equal(t, entry, out[0], "Backends entry must equal CodexEntry result")
		})
	}
}

// SECURITY — CodexEntry must never place OPENAI_API_KEY in a child env on
// either the version probe or the login-status probe. The codex backend is
// subscription-only; the health surface is a read path, not an auth path, but
// it still shells out — so the subscription-env invariant applies. A naive
// implementation that forwarded the parent env would leak a key into the child.
//
// This mirrors TestReadiness_SecurityNoAPIKeyInEnv at the readiness layer but
// verifies the invariant holds end-to-end through CodexEntry (closing gap #4
// in docs/reference/gaps-identified.md — no credential reaches the surface).
func TestCodexEntry_SecurityNoAPIKeyInEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-should-never-be-forwarded")

	inner := &scriptRunner{scripts: []script{
		{lines: []string{"codex-cli 0.137.0"}},
		{lines: []string{"Logged in using ChatGPT"}},
	}}
	cr := &captureRunner{inner: inner}

	_, err := CodexEntry(context.Background(), cr)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(cr.specs), 1, "at least one probe must have run")

	for i, spec := range cr.specs {
		for _, kv := range spec.Env {
			assert.NotContains(t, kv, "OPENAI_API_KEY",
				"CodexEntry probe %d must not contain OPENAI_API_KEY in child env (subscription-only)", i)
		}
	}
}

// CLI-COUPLING PIN — the backend name surfaced on the health surface is pinned
// as a string literal here, mirroring TestReadiness_LoginSubcommandPinned
// (readiness.go), TestCodexEnv_CodexHomeVarNamePinned (env.go), and
// TestMinCodexVersion_Pinned (version.go). codexBackendName flows as the JSON
// "name" field on RedactedBackend; the UI/CLI correlates it with
// workflow.BackendCodex ("codex") to map "codex not logged in" warnings to the
// right profiles before a run. A rename without this pin would break the
// correlation silently.
func TestCodexBackendName_Pinned(t *testing.T) {
	assert.Equal(t, "codex", codexBackendName,
		"backend name must match workflow.BackendCodex and the health surface vocab consumed by the UI/CLI")
}
