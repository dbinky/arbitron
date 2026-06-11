package codex

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// ReadinessState is a backend-scoped health verdict for the codex backend. It
// is serialized verbatim onto the system/health surface (see
// domain/config.RedactedBackend and the system_config handlers), so its string
// values are a stable contract — a rename is a breaking API change.
//
// Unlike the credential-bearing providers, codex carries NO Arbitron connection
// (its subscription auth lives in the host's ~/.codex), so readiness is a
// backend-scoped check, NOT an entry in the connection ProbeSelector.
type ReadinessState string

const (
	// Ready: the codex binary resolves, reports a compatible version, AND a
	// `codex login status` check confirms a usable ChatGPT subscription session.
	Ready ReadinessState = "ready"
	// NotInstalled: the codex binary could not be started from PATH /
	// StandardBinDirs, OR it is present but reports a version older than
	// minCodexVersion (present-but-unusable collapses to "install a supported
	// release"). The login probe is not attempted.
	NotInstalled ReadinessState = "not-installed"
	// NotLoggedIn: the binary is present and compatible, but `codex login
	// status` exits non-zero — there is no usable subscription session in
	// ~/.codex (the operator must run `codex login`).
	NotLoggedIn ReadinessState = "not-logged-in"
)

// loginSubcommand / loginStatusSubcommand are the `codex login status` spelling.
//
// VERIFIED against codex-cli 0.137.0 (`codex login --help` lists the `status`
// subcommand, which prints "Logged in using ChatGPT" and exits 0 when a session
// is present, and exits non-zero otherwise). LOCALIZED HERE so a binary
// re-verification can adjust the spelling in ONE place, mirroring args.go's flag
// constants.
const (
	loginSubcommand       = "login"
	loginStatusSubcommand = "status"
)

// Readiness reports the codex backend's health as one of three STATES —
// Ready / NotInstalled / NotLoggedIn — for the system/health surface, so a
// run can be pre-flighted with a specific cause ("codex not logged in") instead
// of failing mid-mission.
//
// A clean not-ready host returns a STATE, not an error: NotInstalled when the
// binary cannot be started or is too old, NotLoggedIn when `codex login status`
// exits non-zero. Only a GENUINE probe fault — the login subprocess failing to
// START after the version probe already succeeded (e.g. the binary vanished
// mid-probe, or a fork/exec resource fault) — returns an error, so an
// infrastructure failure is never masked as a clean verdict.
//
// All subprocess calls go through the shared claudecli.CommandRunner seam (the
// SAME seam the provider/version probe use), so this is hermetically testable
// with the fake runner.
//
// SECURITY: the probes run under codexEnv() — the subscription base env that
// sets NO OPENAI_API_KEY and reveals no credential (codex's only auth surface
// is the on-disk ~/.codex session). The function returns ONLY the enum verdict;
// it never surfaces raw `login status` output (which can echo account
// identifiers) onto the health surface, and never logs token contents.
func Readiness(ctx context.Context, cfg Config) (ReadinessState, error) {
	runner := cfg.Runner
	if runner == nil {
		runner = claudecli.NewExecRunner()
	}
	bin := resolveBinary(cfg.BinaryPath)

	// Subscription base env (no API key) for every probe — identical to a real
	// `codex exec` launch's auth surface (env.go).
	env := codexEnv()

	// Step 1 — installed? `codex --version` must start AND report a compatible
	// release. A start failure (binary not found) or an incompatible/unparseable
	// version is a clean NotInstalled STATE, not an error: both mean "the
	// operator must install a supported codex". probeVersion distinguishes a
	// runner START fault from a version mismatch, but for the backend-scoped
	// health verdict both collapse to NotInstalled (present-but-unusable ==
	// must-install). We do NOT attempt the login probe in either case.
	if err := probeVersionEnv(ctx, runner, bin, env); err != nil {
		return NotInstalled, nil //nolint:nilerr // a not-installed host is a STATE, not an error
	}

	// Step 2 — logged in? `codex login status` exits 0 with an active ChatGPT
	// session and non-zero otherwise. We branch on the START outcome vs the EXIT
	// outcome:
	//   - START fault (after version already succeeded) ⇒ genuine probe error.
	//   - non-zero EXIT ⇒ NotLoggedIn (a clean state).
	//   - zero EXIT ⇒ Ready.
	loggedIn, startErr := probeLogin(ctx, runner, bin, env)
	if startErr != nil {
		// A genuine infrastructure fault on the login probe — distinct from any
		// of the three states. (The version probe just succeeded, so a start
		// failure here is not "not installed"; it is an environment fault.)
		return "", startErr
	}
	if !loggedIn {
		return NotLoggedIn, nil
	}
	return Ready, nil
}

// probeVersionEnv runs the version probe with an explicit env. It reuses the
// shared probeVersion logic; the env is set on the RunSpec via the runner so
// the subscription base (no API key) is enforced on the readiness path too.
//
// (probeVersion in version.go does not thread an env because its callers run it
// immediately before a full exec that builds its own env; on the readiness path
// we want the SAME no-key env on the bare `--version` call for defense in depth,
// so this wrapper sets it.)
func probeVersionEnv(ctx context.Context, runner claudecli.CommandRunner, bin string, env []string) error {
	return probeVersion(ctx, envRunner{inner: runner, env: env}, bin)
}

// probeLogin runs `codex login status` and reports whether a usable session
// exists. It returns (loggedIn, startErr): a START failure is returned as the
// error (a genuine probe fault); a non-zero EXIT is reported as loggedIn=false
// (a clean NotLoggedIn state), and a zero EXIT as loggedIn=true.
//
// SECURITY: the subprocess stdout is drained but NEVER inspected for content —
// only the exit status drives the verdict, so no account identifier or token
// fragment from the output can reach the caller or a log line.
func probeLogin(ctx context.Context, runner claudecli.CommandRunner, bin string, env []string) (bool, error) {
	lines, wait, err := runner.Run(ctx, claudecli.RunSpec{
		Bin:  bin,
		Args: []string{loginSubcommand, loginStatusSubcommand},
		Env:  env,
	})
	if err != nil {
		// Start fault after version succeeded — a genuine probe error.
		return false, errors.New("codex: login probe failed to start: " + err.Error())
	}
	// Drain stdout to completion before wait (CommandRunner contract). The lines
	// are intentionally discarded — only the exit status is load-bearing.
	for range lines {
	}
	if waitErr := wait(); waitErr != nil {
		// Non-zero exit ⇒ no usable session ⇒ NotLoggedIn (a clean state). We do
		// NOT surface waitErr's text (it may carry diagnostic account detail).
		return false, nil
	}
	return true, nil
}

// envRunner decorates a CommandRunner to force an Env onto every RunSpec it
// forwards (the shared probeVersion does not set one). It is an adapter over the
// SAME seam — not a redefinition — so the fake runner still records the spec
// (including the injected Env) for the security assertions.
type envRunner struct {
	inner claudecli.CommandRunner
	env   []string
}

func (e envRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	spec.Env = e.env
	return e.inner.Run(ctx, spec)
}
