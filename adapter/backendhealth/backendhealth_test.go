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
