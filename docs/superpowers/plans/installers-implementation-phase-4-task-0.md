# Installers P4 — Task 0: native Windows-service awareness (Go change) — `svc.IsWindowsService` / `svc.Run`

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development.
> STRICT TDD where the platform allows. Steps use checkbox (`- [ ]`) syntax.
>
> **Why this task exists (cross-phase correction):** The master design
> (`docs/superpowers/specs/2026-06-05-installers-design.md` §Required Go changes #3)
> LOCKED the decision **"native"** for the Windows Service. The P0 plan
> (`docs/superpowers/plans/2026-06-05-installers-p0-go-prereqs.md`) explicitly
> **deferred** this change ("The optional native Windows-service awareness … is
> deferred to the Windows installer plan") and P0 shipped **only** `configpath.go`,
> `tls.go`, and `serve.go` in `cmd/arbitron/` — there is **no** `svc`/service file
> in the binary today. Therefore **P4 owns this Go change**, and it must land
> **before** P4 Task 3 (Windows Service registration), which assumes the binary is
> service-aware. P3 and the rest of P4 were written as if P0 had shipped it; that
> assumption is corrected here and in the affected task docs' "Alignment notes".

**Goal:** Make the `arbitron` binary service-aware on Windows so a real, native
Windows Service (registered by P4 Task 3 via `New-Service`/`sc.exe`) runs without a
third-party shim (WinSW/nssm). When launched by the Windows Service Control Manager
(SCM), the binary must detect that context and dispatch through
`golang.org/x/sys/windows/svc`; in every other context (interactive run, Linux,
macOS) it must behave exactly as today.

**Tech Stack:** Go 1.25, cobra, `golang.org/x/sys/windows/svc` (new dependency,
Windows-only via build tags), testify.

---

## Build-tag strategy (load-bearing)

The Windows service code is compiled **only** on Windows via Go build constraints,
so the Linux/macOS CI (`go build ./...` / `go test ./...`) never imports
`windows/svc` and continues to pass:

- `cmd/arbitron/service_windows.go` — `//go:build windows`. Contains the real
  SCM-detection + `svc.Run` dispatch.
- `cmd/arbitron/service_other.go` — `//go:build !windows`. Contains a no-op
  implementation with the **same exported/internal function signature** so
  `runServe` (in `main.go`, platform-neutral) can call it unconditionally.

> **CI caveat (state explicitly in the PR):** `go test ./...` on the Linux/macOS CI
> runners will NOT compile `service_windows.go` (it is `//go:build windows`). Its
> behavior is covered by the Windows CI runner (the same `windows-latest` job P3/P4
> add for Pester) via `GOOS=windows go build ./...` at minimum, and ideally a
> `go test` run on Windows. Do not assume the Linux suite exercises this file.

---

## Contract (consumed by `runServe` and P4 Task 3)

A single platform-neutral entry the serve path calls early:

```go
// runUnderServiceManager reports whether the process was started by the Windows
// SCM and, if so, runs the provided serve function under svc.Run, translating
// SCM Start/Stop/Shutdown control requests into ctx cancellation. On non-Windows
// (or when not started by the SCM) it returns (false, nil) and the caller runs
// the normal interactive serve path unchanged.
func runUnderServiceManager(ctx context.Context, serve func(context.Context) error) (handled bool, err error)
```

`runServe` becomes: build the domain stack + `http.Server` as today, then
`handled, err := runUnderServiceManager(ctx, serveFn)`; if `handled`, return its
result; otherwise fall through to today's interactive `ListenAndServeTLS`/
`ListenAndServe` loop. The TLS wiring from P0 (`buildTLSConfig`) is unchanged and
runs inside `serveFn` either way.

---

## Test plan (write first, platform-aware)

`cmd/arbitron/service_other_test.go` (`//go:build !windows`) — runs on the
Linux/macOS CI:
- `runUnderServiceManager(ctx, serveFn)` returns `handled == false` and `err == nil`
  without invoking `serveFn` (the no-op path must never run serve on non-Windows;
  the caller drives serve). Assert `serveFn` call count == 0.

`cmd/arbitron/service_windows_test.go` (`//go:build windows`) — runs on the Windows
CI runner:
- When `svc.IsWindowsService()` is `false` (interactive Windows run),
  `runUnderServiceManager` returns `handled == false, err == nil` (binary run from a
  console behaves interactively). Inject the `IsWindowsService` probe behind a
  package var/func seam so the test can force `false` deterministically.
- The control-handler maps SCM `Stop`/`Shutdown` to ctx cancellation (unit-test the
  handler goroutine in isolation with a fake `chan svc.ChangeRequest` /
  `chan<- svc.Status`, asserting it cancels the context and reports
  `svc.StopPending` → `svc.Stopped`). No live SCM needed.

Build gates (both CI lanes):
- Linux/macOS: `go build ./...` and `go test ./cmd/arbitron/` stay green (windows
  file excluded).
- Cross-compile gate: `GOOS=windows GOARCH=amd64 go build ./...` succeeds (compiles
  `service_windows.go` + the new dependency) — add this to CI so the Windows path is
  at least type-checked from the Linux runner.

---

## Implementation steps

- [ ] **Step 1:** Add the dependency: `go get golang.org/x/sys/windows/svc@latest`
  (pulls `golang.org/x/sys`). Confirm `go.mod`/`go.sum` updated.
- [ ] **Step 2:** Write `cmd/arbitron/service_other.go` (`//go:build !windows`) with
  the no-op `runUnderServiceManager` returning `(false, nil)`; write its test
  (`service_other_test.go`). Green on Linux/macOS.
- [ ] **Step 3:** Write `cmd/arbitron/service_windows.go` (`//go:build windows`):
  - a seam `var isWindowsService = svc.IsWindowsService` for testability;
  - `runUnderServiceManager`: if `!isWindowsService()` → `(false, nil)`; else
    construct an `svc.Handler` whose `Execute` reports `StartPending` → `Running`,
    launches `serve(ctx)` in a goroutine, and on `Stop`/`Shutdown` cancels `ctx`,
    reports `StopPending` → `Stopped`, and returns the serve error;
  - `svc.Run("arbitron", handler)` with the service name matching P4 Task 3's
    `New-Service -Name arbitron`.
- [ ] **Step 4:** In `cmd/arbitron/main.go` `runServe`, factor the
    listen-and-serve body into a `serveFn func(context.Context) error` closure
    (keeping the P0 TLS branch intact) and call `runUnderServiceManager(ctx, serveFn)`
    first; return early when `handled`. No behavior change on Unix.
- [ ] **Step 5:** Add the Windows build/test job (or extend P4 Task 4's
    `windows-latest` job) to run `go build ./...` and `go test ./cmd/arbitron/` on
    Windows, plus a `GOOS=windows go build ./...` cross-compile gate on the Linux
    job. Confirm both lanes green.
- [ ] **Step 6:** Confirm `go build ./...` and `go test ./...` on Linux/macOS are
    unaffected (windows file excluded by build tag).

---

## Fallback (documented per the master design)

If native `svc` integration proves costly or flaky on the Windows runner, fall back
**in this order**, updating P4 Task 3 accordingly:
1. A **Scheduled Task** running as LocalSystem/the service account at startup
   (`Register-ScheduledTask -Trigger (New-ScheduledTaskTrigger -AtStartup)`), the
   same mechanism P3 uses for the user-level local path — no binary change needed.
2. A downloaded, **checksum-verified** **WinSW** or **nssm** wrapper that supervises
   `arbitron.exe serve --config …` as a service — no binary change needed.

The native path is the default; record which path shipped in the P4 Task 3 summary.

---

## Files

- **Create:** `cmd/arbitron/service_windows.go` (`//go:build windows`).
- **Create:** `cmd/arbitron/service_other.go` (`//go:build !windows`).
- **Create:** `cmd/arbitron/service_windows_test.go` (`//go:build windows`).
- **Create:** `cmd/arbitron/service_other_test.go` (`//go:build !windows`).
- **Modify:** `cmd/arbitron/main.go` — factor `serveFn`, call
  `runUnderServiceManager` first in `runServe`.
- **Modify:** `go.mod` / `go.sum` — add `golang.org/x/sys`.
- **Modify (CI):** the Windows job (shared with P4 Task 4) + a Linux
  `GOOS=windows go build` cross-compile gate.

---

## Acceptance criteria

- [ ] The binary detects SCM launch (`svc.IsWindowsService`) and dispatches via
      `svc.Run`; interactive runs and non-Windows builds are unchanged.
- [ ] Windows service code is isolated behind `//go:build windows`; the `!windows`
      twin keeps `runServe` compiling and behaving identically on Unix.
- [ ] `go build ./...` and `go test ./...` pass on Linux/macOS (windows file
      excluded); `GOOS=windows go build ./...` passes on the Linux runner; the
      Windows CI runner builds + tests the windows-tagged code.
- [ ] No third-party shim is required for the shared Windows Service (P4 Task 3),
      with the Scheduled-Task / WinSW fallback documented if native is deferred.

---

## Dependencies

- **P0 (merged):** `--config` flag and the `runServe` structure this task wraps;
  the P0 TLS path runs unchanged inside `serveFn`.
- **Required by:** **P4 Task 3** (Windows Service registration) — Task 3 depends on
  THIS Go change landing first; the registered service only works because the binary
  is service-aware. P4 Task 3's dependency line is updated to state this.
- **Not used by:** P3 (local Windows uses a Scheduled Task, no SCM dispatch).
