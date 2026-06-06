# Installers P1 — Task 1: `install.sh` skeleton, detection & shared helpers

> Phase 1 (P1) of the Arbitron installers feature. Source design:
> `docs/superpowers/specs/2026-06-05-installers-design.md` and
> `docs/superpowers/specs/installers-design-phase-1.md`. This is the FIRST task of
> P1: it stands up the script, the platform/state detection, and the reusable
> helper library that every later task (and P2's Shared branch) builds on.

## Context

This task delivers the `install.sh` skeleton at repo root and the foundational,
side-effect-light building blocks: OS/arch/elevation detection, install-type
selection (Local — macOS is local-only; Linux defaults to Local in P1), existing
-install detection that dispatches to one of `Fresh | Upgrade | Reconfigure |
Uninstall`, and a small library of shared helpers (structured logging/coloured
output, interactive prompt primitives with non-interactive `ARBITRON_*`
overrides, a `set -euo pipefail` safety preamble, a checksum-verify helper, and a
path-layout resolver for the local `~/.arbitron` tree).

Everything is factored so P2 (Linux shared / system install) can reuse the same
detection, dispatch, prompt, logging, and checksum primitives by adding a Shared
branch rather than forking the script. Bash cannot be Go-unit-tested, so the
helpers are written as small, independently-callable functions that `bats` can
source and exercise in isolation, and the whole script must pass `shellcheck`.

Depends on: nothing (first task). Feeds: Tasks 2–5 and P2.

## Test plan (write first)

Create `tests/install/helpers.bats` and `tests/install/detect.bats`. Tests source
`install.sh` with a guard env var (e.g. `ARBITRON_INSTALL_LIB_ONLY=1`) so sourcing
defines functions WITHOUT running `main`. Provide a `tests/install/test_helper.bash`
that sets `HOME` to a `BATS_TMPDIR` sandbox and prepends a mock-bin dir to `PATH`.

shellcheck:
- `shellcheck install.sh` exits 0 (no warnings; `# shellcheck disable=` only with a
  justifying comment). A `tests/install/shellcheck.bats` (or CI step) asserts this.

Detection (`detect.bats`):
- `detect_os` returns `macos` when `uname -s` mock prints `Darwin`; `linux` when
  `Linux`; errors with non-zero status + message on anything else.
- `detect_arch` maps `x86_64`/`amd64` → `amd64` and `aarch64`/`arm64` → `arm64`;
  errors on an unsupported arch string.
- `detect_arch` maps Apple-Silicon `uname -m` = `arm64` → `arm64`.
- `is_elevated` returns false (non-zero) when mocked `id -u` ≠ 0 (the local-install
  expectation); returns true when `id -u` = 0. (Local must NOT require elevation.)
- `select_install_type` yields `Local` on macOS unconditionally; yields `Local` in
  P1 on Linux (asserts the Shared branch is a documented stub/`not-yet` path so P2
  can slot in).

Existing-install detection & dispatch (`detect.bats`):
- With no `~/.arbitron/config.yaml` and no service unit present →
  `detect_state` = `Fresh`.
- With config + service present and `ARBITRON_ACTION=upgrade` →
  `detect_state` honours the explicit action (`Upgrade`).
- With config + service present and interactive prompt mocked to choose
  "reconfigure" → dispatch routes to the Reconfigure handler name.
- With config present and `ARBITRON_ACTION=uninstall` → routes to Uninstall.
- An unknown `ARBITRON_ACTION` value errors non-zero with a usage message.

Helpers (`helpers.bats`):
- `log_info`/`log_warn`/`log_error` write to stderr (so stdout stays parseable),
  and `log_error` does NOT itself exit (caller decides).
- `die <msg>` prints to stderr and exits non-zero.
- `prompt_default VAR "question" "default"` returns the default when stdin is
  empty/non-interactive; returns the typed value when stdin supplies one;
  honours a pre-set `ARBITRON_<VAR>` env override without prompting (non-interactive
  install path).
- `prompt_yes_no` parses y/yes/Y → true, n/no → false, empty → the supplied
  default; respects an env override.
- `verify_checksum <file> <expected_sha256>` succeeds on a matching digest and
  fails non-zero on a mismatch (uses `shasum -a 256` on macOS / `sha256sum` on
  linux, selected by a `sha256_tool` helper that is itself unit-tested with both
  mocked).
- `arbitron_home` returns `$HOME/.arbitron`; `layout_*` helpers return the
  absolute bin/config/data/workspaces/logs/secrets-env paths from the local matrix
  (asserts `config` = `$HOME/.arbitron/config.yaml`, `secrets env` =
  `$HOME/.arbitron/arbitron.env`, etc.).
- `require_cmd <name>` returns non-zero (not exit) when a command is absent so
  callers can branch into dependency resolution.

## Implementation steps

1. Create `install.sh` at repo root, `#!/usr/bin/env bash`, with a strict preamble
   (`set -euo pipefail`, `IFS=$'\n\t'`). Add the `ARBITRON_INSTALL_LIB_ONLY` guard:
   when set, source-and-return before `main` runs (enables bats unit testing).
2. Add an output/logging block: `log_info/warn/error` (to stderr, colourised only
   when stdout is a TTY — guard with `[ -t 2 ]`), and `die`.
3. Add prompt primitives: `prompt_default`, `prompt_yes_no`, each consulting a
   matching `ARBITRON_*` env override first (non-interactive support), then reading
   stdin, then falling back to the default.
4. Add detection functions: `detect_os` (`uname -s`), `detect_arch` (`uname -m`
   with the amd64/arm64 normalisation table), `is_elevated` (`id -u`).
5. Add `select_install_type`: macOS → `Local`; Linux → `Local` for P1 with a
   clearly-marked extension seam (a `# P2: Shared branch hooks here` comment and a
   function indirection `INSTALL_TYPE` variable) so P2 adds Shared without a
   rewrite.
6. Add layout helpers: `arbitron_home`, and `layout_bin`, `layout_config`,
   `layout_data`, `layout_db`, `layout_workspaces`, `layout_logs`,
   `layout_secrets_env` — all returning ABSOLUTE paths under `~/.arbitron` per the
   P1 matrix. (P2 will provide a parallel system-path implementation behind the
   same names.)
7. Add `sha256_tool` (pick `sha256sum` or `shasum -a 256`) and `verify_checksum`.
8. Add `require_cmd` and a `command_version` helper (runs `<cmd> --version`,
   greps a semver) used by Task 2.
9. Add existing-install detection: `has_existing_config` (file exists),
   `has_existing_service` (delegates to a Task-4 predicate, stubbed here returning
   false so the skeleton is self-contained), and `detect_state` that combines them
   with `ARBITRON_ACTION`/interactive choice into `Fresh|Upgrade|Reconfigure|
   Uninstall`.
10. Add a `main` dispatcher: detect → select type → detect state → call the
    matching handler function (`run_fresh`, `run_upgrade`, `run_reconfigure`,
    `run_uninstall`), each a stub here that the later tasks flesh out, plus a
    final `print_summary` stub.
11. Run `shellcheck install.sh` until clean.
12. Add `tests/install/test_helper.bash`, `helpers.bats`, `detect.bats`, and a
    shellcheck assertion; make them green.

## Files

- `install.sh` (new, repo root) — skeleton + shared helper library.
- `tests/install/test_helper.bash` (new) — bats common setup (sandbox HOME, mock PATH).
- `tests/install/helpers.bats` (new).
- `tests/install/detect.bats` (new).
- `tests/install/shellcheck.bats` (new) — or a CI step; Task 5 finalises CI wiring.

## Acceptance criteria

- [ ] `install.sh` exists at repo root, is executable, and passes `shellcheck` clean.
- [ ] Sourcing with `ARBITRON_INSTALL_LIB_ONLY=1` defines functions without running `main`.
- [ ] OS/arch/elevation detection covered by passing bats tests, incl. error cases.
- [ ] `detect_state` returns the correct one of `Fresh|Upgrade|Reconfigure|Uninstall`
      across all tested combinations of config/service presence and `ARBITRON_ACTION`.
- [ ] Local install requires NO elevation (`is_elevated` false is the normal path).
- [ ] Layout helpers return the exact absolute paths from the P1 matrix.
- [ ] Prompt + checksum + logging helpers behave per the test plan, including env
      overrides for non-interactive runs.
- [ ] Detection/select/helper functions are factored so a P2 Shared branch can
      reuse them (documented extension seams present).

## Alignment notes (cross-phase contracts — authoritative)

This task defines the canonical bash names and constants that **P2 reuses verbatim**
(P2 must NOT invent variants). The PowerShell side has its own canonical set defined
in **P3 Task 1**, reused verbatim by **P4**.

**Canonical bash helper names (defined here):**
- Logging/UI: `log_info`, `log_warn`, `log_error`, `die`.
- Prompts: `prompt_default`, `prompt_yes_no` (the yes/no prompt is `prompt_yes_no` —
  **not** `confirm`).
- Detection: `detect_os`, `detect_arch` (two separate functions — **not** a combined
  `detect_os_arch`), `is_elevated`, `select_install_type`,
  `detect_state` (returns `Fresh|Upgrade|Reconfigure|Uninstall` — **not**
  `detect_install_state`).
- Layout: `arbitron_home`, `layout_bin`, `layout_config`, `layout_data`,
  `layout_db`, `layout_workspaces`, `layout_logs`, `layout_secrets_env`,
  `layout_runtime` (P2 supplies parallel system-path implementations behind the same
  names).
- Checksum/download: `sha256_tool`, `verify_checksum`; the verified-download primitive
  is `download_verified` (defined in Task 2 — **not** `download_and_verify`). P5
  Task 2 wraps the fail-closed verification *contract* as `verify_download`, but the
  download primitive callers invoke stays `download_verified`.
- Sourcing guard: `ARBITRON_INSTALL_LIB_ONLY=1` (source without running `main`) — the
  single canonical guard; **not** `ARBITRON_INSTALL_NO_MAIN`.

**Canonical repository + download constants (used by P1–P4, finalized by P5):**
- Go **module path** is `github.com/arbitron/arbitron` — this is an import path, NOT a
  URL. Do **not** build any download/raw URL from the module path.
- The **GitHub repository** that hosts releases and the raw scripts is
  **`dbinky/arbitron`**. Define a single shell constant near the top of `install.sh`,
  e.g. `ARBITRON_REPO="${ARBITRON_REPO:-dbinky/arbitron}"`, and build every URL from
  it:
  - release base: `https://github.com/${ARBITRON_REPO}/releases/download/v<version>/…`
  - latest redirect: `https://github.com/${ARBITRON_REPO}/releases/latest`
  - raw script: `https://raw.githubusercontent.com/${ARBITRON_REPO}/main/install.sh`
  P1 Task 3's `download_binary` and P5 Task 2's `arbitron_release_base` /
  `arbitron_resolve_version` all reference this constant. P5 Task 2 finalizes the
  exact URL shapes; P1 must not hard-code `arbitron/arbitron` (the module path) as a
  host/owner.

**Master key:** the secret is the **`ARBITRON_MASTER_KEY` env var** (base64 of 32
random bytes), read by the binary from the environment — it is **never** a YAML key.
The API key uses `ANTHROPIC_API_KEY` (loader special-cases it → `llm.apikey`) or
`ARBITRON_LLM_APIKEY`; never written into `config.yaml`.

## Dependencies

- Depends on: none (first P1 task).
- Required by: Tasks 2, 3, 4, 5; and P2 (Shared) extends this skeleton.
