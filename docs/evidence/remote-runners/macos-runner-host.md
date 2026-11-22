# Observed: macOS runner host facts (thermal, credentials, FileVault, root paths)

Observed 2026-11-22 on macOS 26.5.2, Apple **M2** (arm64), during plan dependency resolution (RR-05, RR-06, RR-07). Read-only commands only.

## Thermal (RR-06, P2 T7)

| Source | Output on this M2 (idle) | Verdict |
|---|---|---|
| `pmset -g therm` (exit 0) | `Note: No thermal warning level has been recorded` / `Note: No performance warning level has been recorded` / `Note: No CPU power status has been recorded` | Note-only on Apple silicon; carries no state. **Dropped.** |
| `sysctl kern.thermalstate`, `kern.thermal_pressure_level`, `machdep.xcpm` | `unknown oid` | Not available. |
| `/usr/bin/notifyutil -g com.apple.system.thermalpressurelevel` | `com.apple.system.thermalpressurelevel 0` | **Chosen source.** cgo-free, stock binary, reads the Darwin notify state that backs `OSThermalPressureLevel` / `NSProcessInfo.thermalState`. |

`OSThermalPressureLevel` enum (`<libkern/OSThermalNotification.h>`): `0` Nominal, `1` Moderate, `2` Heavy, `3` Trapping, `4` Sleeping.

Decision 2026-11-22 mapping: `0 → nominal`, `1 → fair`, `2 → serious`, `3` or `4 → critical`. Output that is not exactly `com.apple.system.thermalpressurelevel <integer 0–4>` (missing key, other integer, non-integer, non-zero exit, timeout) → `Thermal = nil` (unavailable), never `nominal`. Level 0 is the observed real fixture; levels 1–4 are synthetic fixtures from the documented enum. No hardware thermal-load test is required.

## Harness credentials and boot context (RR-05, P2 T11)

- Claude Code subscription login: **login keychain** item `Claude Code-credentials` (no `~/.claude/.credentials.json`). The login keychain is only usable inside the user's logged-in session.
- Codex login: `~/.codex/auth.json`, mode `0600` (file credential).
- `fdesetup status`: **FileVault is On.** With FileVault on, automatic login is unavailable; after a cold boot the Mac waits at the pre-boot unlock screen, and unlocking with a user's password normally logs that user straight into their session. Unattended restart is only possible with `sudo fdesetup authrestart`.

Decision 2026-11-22: per-user LaunchAgent (`gui/<uid>`) — see P2 T11.

## Paths (RR-07, P2 T11)

- `/var` is a symlink to `/private/var` on macOS; a root created under `/var/lib/...` has real path `/private/var/lib/...`, which differs from the Linux path seen by cwd-keyed state (Claude project/session keying, `pwd -P`, git).
- `/opt` exists natively (`drwxr-xr-x root wheel`), is not a symlink and is not SIP-protected; `/srv` does not exist.

Decision 2026-11-22: canonical cross-OS root `/opt/arbitron/workspaces` — see P2 T11.
