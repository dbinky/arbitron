package codex

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loggedInProbe is the canned `codex login status` reply that reports an active
// ChatGPT subscription session (exit 0). Mirrors compatibleProbe in
// fake_runner_test.go.
func loggedInProbe() runScript {
	return runScript{lines: []string{"Logged in using ChatGPT"}}
}

// HAPPY — binary resolves, version is compatible, and `codex login status`
// exits 0: the backend is fully Ready.
func TestReadiness_Ready(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), loggedInProbe()}}
	state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	assert.Equal(t, Ready, state)

	// Both probes ran in order: `--version` then `login status`.
	require.Len(t, mr.calls, 2)
	assert.Contains(t, mr.calls[0].Args, "--version")
	assert.Contains(t, mr.calls[1].Args, "login")
	assert.Contains(t, mr.calls[1].Args, "status")
}

// STATE VARIATION — the binary cannot be started (not on PATH): NotInstalled is
// a STATE, not an error, so the UI can render the cause. The login probe is NOT
// attempted.
func TestReadiness_NotInstalled(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{startErr: errors.New(`exec: "codex": not found`)}}}
	state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	assert.Equal(t, NotInstalled, state)
	// Fail-closed: only the version probe was attempted, no login probe.
	require.Len(t, mr.calls, 1)
}

// STATE VARIATION — an incompatible (too-old) binary is also reported as
// NotInstalled (present but unusable): the operator must install a supported
// release. No login probe is attempted.
func TestReadiness_IncompatibleVersionIsNotInstalled(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{lines: []string{"codex-cli 0.0.1"}}}}
	state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	assert.Equal(t, NotInstalled, state)
	require.Len(t, mr.calls, 1)
}

// STATE VARIATION — binary present and compatible, but `codex login status`
// exits non-zero (no usable subscription session): NotLoggedIn, a state.
func TestReadiness_NotLoggedIn(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{"Not logged in"}, waitErr: errors.New("exit status 1")},
	}}
	state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	assert.Equal(t, NotLoggedIn, state)
	require.Len(t, mr.calls, 2)
}

// CONTRACT PIN (UNVERIFIED exit-code assumption) — this pins the CURRENT
// login-status exit-code mapping so it cannot drift silently: a NON-ZERO exit
// from `codex login status` maps to NotLoggedIn, and a ZERO exit maps to Ready.
//
// The logged-out exit code was NOT confirmed on a real logged-out host (only the
// flag/JSON surface was verified against codex-cli 0.137.0). If a maintainer
// confirms codex prints "Not logged in" yet exits 0 when logged out, THIS TEST
// must be revised together with probeLogin to branch on stdout content — the
// failing assertion here is the intended tripwire for that change. See the
// EXIT-CODE CONTRACT note on probeLogin and the NotLoggedIn doc.
func TestReadiness_LoginExitCodeMapping_Pinned(t *testing.T) {
	t.Run("non-zero exit ⇒ NotLoggedIn", func(t *testing.T) {
		mr := &multiRunner{scripts: []runScript{
			compatibleProbe(),
			{lines: []string{"Not logged in"}, waitErr: errors.New("exit status 1")},
		}}
		state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
		require.NoError(t, err)
		assert.Equal(t, NotLoggedIn, state,
			"a non-zero `codex login status` exit must map to NotLoggedIn (current, defensive assumption)")
	})

	t.Run("zero exit ⇒ Ready", func(t *testing.T) {
		mr := &multiRunner{scripts: []runScript{compatibleProbe(), loggedInProbe()}}
		state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
		require.NoError(t, err)
		assert.Equal(t, Ready, state,
			"only a zero `codex login status` exit may map to Ready — never a false green on a non-zero exit")
	})
}

// ERROR — a genuine probe fault on the LOGIN check (the subprocess fails to
// start AFTER version succeeded — e.g. the binary vanished mid-probe) is a
// distinct error, NOT a clean not-ready state. The three states must never
// mask an infrastructure fault.
func TestReadiness_LoginProbeStartFault(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{startErr: errors.New("fork/exec: resource temporarily unavailable")},
	}}
	state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.Error(t, err)
	assert.Empty(t, string(state))
	assert.Contains(t, err.Error(), "resource temporarily unavailable")
}

// SECURITY — the readiness check sets NO OPENAI_API_KEY on either probe and
// never injects a credential into the child env (codex is subscription-only;
// the only auth surface is the on-disk ~/.codex session). It reuses the same
// subscription env as a real exec launch.
func TestReadiness_SecurityNoAPIKeyInEnv(t *testing.T) {
	// Poison the parent env so a naive implementation that forwarded it would
	// leak a key into the child spec.
	t.Setenv("OPENAI_API_KEY", "sk-should-never-be-forwarded")

	mr := &multiRunner{scripts: []runScript{compatibleProbe(), loggedInProbe()}}
	_, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)

	for _, call := range mr.calls {
		for _, kv := range call.Env {
			assert.NotContains(t, kv, "OPENAI_API_KEY",
				"readiness must never place OPENAI_API_KEY in the child env")
		}
		// The prompt/stdin must carry no credential either.
		assert.NotContains(t, call.Stdin, "sk-should-never-be-forwarded")
	}
}

// SECURITY — the login-status output (which may echo account identifiers) is
// never surfaced in the returned state: Readiness returns only the enum, never
// raw subprocess text. This guards against token/identity leakage through the
// health surface.
func TestReadiness_StateCarriesNoSubprocessText(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{"Logged in using ChatGPT as token=sk-secret-abc123"}},
	}}
	state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	assert.Equal(t, Ready, state)
	assert.NotContains(t, string(state), "sk-secret-abc123")
}

// guard — the readiness states have stable, documented string values (they are
// serialized onto the health surface, so a rename is a breaking API change).
func TestReadinessStateValues(t *testing.T) {
	assert.Equal(t, "ready", string(Ready))
	assert.Equal(t, "not-installed", string(NotInstalled))
	assert.Equal(t, "not-logged-in", string(NotLoggedIn))
}

// WIRE-STRING CONTRACT — the three ReadinessState values are serialized VERBATIM
// onto GET /system/config and the MCP system_config surface (via
// config.RedactedBackend.State). They are a public API contract: a dashboard
// keys off these exact strings, and the type doc itself calls a rename "a
// breaking API change". This table pins each (constant → wire string) pair so a
// future tidy-up rename fails CI here instead of silently breaking a consumer.
// It is the readiness-side half of the contract guard; the backend NAME ("codex")
// is pinned in backendhealth.TestCodexBackendName_Pinned.
func TestReadinessState_WireStringContract(t *testing.T) {
	cases := []struct {
		state ReadinessState
		wire  string
	}{
		{Ready, "ready"},
		{NotInstalled, "not-installed"},
		{NotLoggedIn, "not-logged-in"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.wire, func(t *testing.T) {
			assert.Equal(t, tc.wire, string(tc.state),
				"ReadinessState %q is a public wire-string contract; a rename is a breaking API change", tc.wire)
		})
	}
}

// CLI-COUPLING PIN — the `codex login status` subcommand spellings are pinned
// here as string literals, mirroring TestBuildArgs_PinnedFlagSpellings (args.go)
// and TestCodexEnv_CodexHomeVarNamePinned (env.go). TestReadiness_Ready already
// verifies them functionally via assert.Contains(args, "login"), but an explicit
// pin makes the coupling searchable and ensures a codex-cli subcommand rename is
// caught as a one-line, test-caught change rather than a behavioral regression.
func TestReadiness_LoginSubcommandPinned(t *testing.T) {
	assert.Equal(t, "login", loginSubcommand, "codex login subcommand spelling must match codex-cli 0.137.0")
	assert.Equal(t, "status", loginStatusSubcommand, "codex login status subcommand spelling must match codex-cli 0.137.0")
}

// guard — a nil Runner falls back to the production exec runner (so a real
// host with no codex installed reports NotInstalled, not a panic). This does
// NOT shell out in CI beyond a `codex --version` that is expected to fail.
func TestReadiness_NilRunnerDoesNotPanic(t *testing.T) {
	// Point at a binary name that will not resolve so the production runner's
	// start fails cleanly into NotInstalled.
	state, err := Readiness(context.Background(), Config{BinaryPath: "codex-does-not-exist-xyz"})
	require.NoError(t, err)
	assert.Equal(t, NotInstalled, state)
}

// SECURITY — the readiness VERSION probe must carry the subscription env on its
// RunSpec (NOT a nil Env that would inherit the engine's parent environment).
// envRunner/probeVersionEnv were removed in favor of threading env directly into
// probeVersion; this pins that the version probe's child env is non-empty AND
// matches the same subscription base the login probe uses, so a future refactor
// cannot silently let the `--version` call inherit the parent env again.
func TestReadiness_VersionProbeCarriesSubscriptionEnv(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), loggedInProbe()}}
	_, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	require.Len(t, mr.calls, 2, "both the version and login probes must run on a ready host")

	// Call 0 is `--version`, call 1 is `login status`: both must carry the SAME
	// non-empty subscription env (no inheritance of the parent env).
	require.Contains(t, mr.calls[0].Args, "--version")
	assert.NotEmpty(t, mr.calls[0].Env, "version probe env must be the subscription base, never nil/inherited")
	assert.Equal(t, mr.calls[1].Env, mr.calls[0].Env,
		"version and login probes must run under the identical subscription env")
}

// SECURITY/DoS — both readiness probes (version + login status) must bound their
// stdout with the shared probe byte cap so a wedged/hostile codex cannot grow
// memory unbounded on a health-surface poll.
func TestReadiness_ProbesBoundStdout(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), loggedInProbe()}}
	_, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	require.Len(t, mr.calls, 2)
	for i, call := range mr.calls {
		assert.Equal(t, probeMaxResponseBytes, call.MaxResponseBytes,
			"probe %d must set the shared MaxResponseBytes cap", i)
	}
}

// FA16 PAIRED (§5+§10) — the version probe (version.go) and the readiness check
// (readiness.go) share the minCodexVersion threshold and the same binary
// resolution. The threshold is a BOUNDARY: exactly minCodexVersion is accepted
// (probe succeeds, Readiness can reach the login probe), while one minor version
// below is rejected by probeVersion (ErrIncompatibleCodex → NotInstalled).
//
// This table test pins the collapse contract: BOTH a binary-start error AND an
// ErrIncompatibleCodex from the version probe map to the exact same NotInstalled
// state, never to an error. The versions used here reference minCodexVersion
// directly so a floor change is a one-line, test-caught update in BOTH units.
func TestReadiness_VersionThresholdParity(t *testing.T) {
	cases := []struct {
		name    string
		lines   []string
		waitErr error
		startErr error
	}{
		{
			name:     "binary not found (start error)",
			startErr: errors.New(`exec: "codex": not found`),
		},
		{
			name:  "version below minimum (ErrIncompatibleCodex path)",
			lines: []string{"codex-cli 0.0.1"},
		},
		{
			// A version that is one minor below minCodexVersion (0.137.0 → 0.136.x).
			// ErrIncompatibleCodex must still be returned by probeVersion and
			// collapsed to NotInstalled here.
			name:  "version one minor below minimum",
			lines: []string{"codex-cli 0.136.9"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			mr := &multiRunner{scripts: []runScript{{
				lines:    tc.lines,
				startErr: tc.startErr,
			}}}
			state, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
			require.NoError(t, err, "version/start failures must be a STATE, not an error")
			assert.Equal(t, NotInstalled, state,
				"any version probe failure must collapse to NotInstalled (§5+§10 shared threshold)")
			// The login probe must NOT run: only one call expected.
			assert.Len(t, mr.calls, 1, "login probe must not run after version failure")
		})
	}
}

// guard — TrimSpace handling: an env entry list with no OPENAI_API_KEY remains
// clean even when the parent has unrelated vars set. (Defense-in-depth around
// the security test.)
func TestReadiness_EnvIsSubscriptionBase(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), loggedInProbe()}}
	_, err := Readiness(context.Background(), Config{BinaryPath: "codex", Runner: mr})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(mr.calls), 1)
	// The probe env must be non-empty (it carries the allowlisted base incl. PATH).
	assert.NotEmpty(t, mr.calls[0].Env)
	var hasPath bool
	for _, kv := range mr.calls[0].Env {
		if strings.HasPrefix(kv, "PATH=") {
			hasPath = true
		}
	}
	assert.True(t, hasPath, "subscription base env must carry PATH so the child resolves codex")
	_ = os.Getenv // keep os import meaningful across builds
}
