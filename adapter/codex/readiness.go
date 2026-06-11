package codex

import (
	"context"
	"errors"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/adapter/procenv"
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
	//
	// UNVERIFIED EXIT-CODE ASSUMPTION (see probeLogin): this verdict rests on the
	// premise that `codex login status` exits NON-ZERO when logged out. The flag
	// spellings and JSON event shapes were verified against codex-cli 0.137.0, but
	// the LOGGED-OUT exit code was NOT confirmed on a real logged-out host. If
	// codex instead prints "Not logged in" to stdout and exits 0 (a common CLI
	// pattern), Readiness would report Ready on a logged-out host — a false green.
	// A human MUST confirm the logged-out exit code before relying on this verdict
	// (`codex login logout && codex login status; echo $?`); the mapping below is
	// the deliberate, fail-toward-Ready-only-on-exit-0 assumption.
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
	//
	// The subscription env (no API key) is threaded directly into probeVersion so
	// the bare `--version` call never inherits the engine's parent env — the SAME
	// no-key env the exec/stream paths now pass (defense in depth, one call shape).
	if err := probeVersion(ctx, runner, bin, env); err != nil {
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

// probeLogin runs `codex login status` and reports whether a usable session
// exists. It returns (loggedIn, startErr): a START failure is returned as the
// error (a genuine probe fault); a non-zero EXIT is reported as loggedIn=false
// (a clean NotLoggedIn state), and a zero EXIT as loggedIn=true.
//
// EXIT-CODE CONTRACT (UNVERIFIED for the logged-out case — DEFENSIVE BY DESIGN):
// this maps zero exit ⇒ logged in, any non-zero exit ⇒ logged out. The codex-cli
// 0.137.0 flag/JSON surface was verified, but the LOGGED-OUT exit code was NOT
// confirmed on a real logged-out host. We deliberately treat ONLY a clean exit 0
// as "logged in" so the failure mode is conservative: a codex that exits non-zero
// for any reason reports NotLoggedIn (operator re-runs `codex login`), never a
// false Ready. The dangerous case the maintainer must rule out before merge is a
// codex that prints "Not logged in" yet still exits 0 — that would surface Ready
// on a logged-out host. Confirm with `codex login logout && codex login status;
// echo $?`; if it is 0 when logged out, this mapping (and probeLogin) must branch
// on stdout content instead of the exit code.
//
// SECURITY: the subprocess stdout is drained but NEVER inspected for content —
// only the exit status drives the verdict, so no account identifier or token
// fragment from the output can reach the caller or a log line.
//
// The probe is bounded by probeMaxResponseBytes (a small stdout cap) and a
// derived probeTimeout so a wedged/hostile codex that never EOFs cannot grow
// memory or pin the probe goroutine.
func probeLogin(ctx context.Context, runner claudecli.CommandRunner, bin string, env []string) (bool, error) {
	probeCtx, cancel := procenv.WithTimeout(ctx, probeTimeout)
	defer cancel()

	lines, wait, err := runner.Run(probeCtx, claudecli.RunSpec{
		Bin:              bin,
		Args:             []string{loginSubcommand, loginStatusSubcommand},
		Env:              env,
		MaxResponseBytes: probeMaxResponseBytes,
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
		// See the EXIT-CODE CONTRACT note above: this branch is the load-bearing,
		// unverified-for-logged-out assumption.
		return false, nil
	}
	return true, nil
}
