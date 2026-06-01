# Phase 3, Task 4: `adapter/opencode` AgentProvider (`RunAgent`)

## Goal

Implement a native `engine.AgentProvider` that routes an agent mission to any
provider/model via the `opencode` CLI, reusing the shared `CommandRunner` subprocess
seam and the task-3 credential env-injection helper. The adapter invokes
`opencode run --model <provider/model> "<mission prompt>"` with `cmd.Dir =
mission.WorkDir`, parses the output into
`AgentResult{Summary, Steps, Tokens, SessionID}`, and accounts tokens coarsely.

**Session shape per the Phase 1 §5 spike finding:** DEFAULT to **single-shot mission
per step** — one `opencode run` per agent node, with NO `SessionID` threading — unless
the spike explicitly cleared headless resume. Do not implement `opencode serve`/session
resume unless the recorded spike finding says resume is reliable.

**This is task 4 of 5. It depends on task 3** (the `opencodeEnv` helper) and reuses the
shared `CommandRunner` seam (the same interface the `adapter/claudecli` adapters use —
`Run(ctx, RunSpec) (lines <-chan string, wait func() error, err error)`). It does not
depend on task 5.

### Dependencies consumed (do NOT redefine — import them)

- Phase 1 `domain/engine.AgentProvider` + `AgentMission` (`Model` = `provider/model`,
  `Prompt`, `WorkDir`, `ToolScope`, `SessionID`, `Timeout`) and `AgentResult`
  (`Summary`, `Steps []AgentStep`, `Model`, `TokensIn`, `TokensOut`, `SessionID`) /
  `AgentStep` (`Kind`, `Summary`).
- The shared `CommandRunner` seam and `RunSpec` (`Bin`, `Args`, `Stdin`, `Dir`, `Env`,
  `MaxResponseBytes`) from the claude-cli adapter package. Inject it via `Config` so the
  adapter is unit-testable with the canned-output fake.
- Task 3's `opencodeEnv(ctx, binding)` for the subprocess env (base allowlist + injected
  provider key + base-url).
- Phase 2 `domain/provider.ResolvedBinding` (`Provider`, `BaseURL`, `Model`, lazy
  `RevealKey`) — passed into `opencodeEnv` and used to form `--model`.

### Hex placement

Driven adapter under `adapter/`; implements `engine.AgentProvider`, consumes the
`CommandRunner` seam, the `ResolvedBinding`, and the task-3 env helper. Domain never
imports it.

## Files to Create/Modify

### Create: `adapter/opencode/provider.go`

- `Config`:
  ```go
  type Config struct {
      Binding          provider.ResolvedBinding // provider, base-url, Model, RevealKey
      BinaryPath       string                   // "" → resolved from PATH
      Runner           claudecli.CommandRunner  // nil → production os/exec runner
      Timeout          time.Duration            // default per-mission timeout
      MaxResponseBytes int64
  }
  ```
  (Reuse the existing `CommandRunner` interface/`RunSpec` from the claudecli package
  rather than redefining a parallel seam.)
- `Provider` + `New(cfg Config) *Provider`, with
  `var _ engine.AgentProvider = (*Provider)(nil)`.
- `RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error)`:
  1. Validate `mission.WorkDir` is non-empty (an empty `cmd.Dir` inherits the engine
     launch directory — same SECURITY invariant as `claudecli.ErrWorkDirRequired`;
     reuse that guard pattern).
  2. Build args via `buildArgs` (below): `run --model <mission.Model>` and the mission
     prompt; thread `mission.ToolScope` to opencode's permission flags if applicable.
     **Do NOT thread `SessionID` / resume args** unless the spike-cleared path is on.
  3. Build the env via task-3 `opencodeEnv(ctx, p.binding)`.
  4. Build a `RunSpec{Bin, Args, Stdin: mission.Prompt-or-arg, Dir: mission.WorkDir,
     Env, MaxResponseBytes}` and call `runner.Run`. Honor `mission.Timeout` /
     `cfg.Timeout` via a derived context (mirror `claudecli`'s `withTimeout`).
  5. Drain the lines channel, parse output → `AgentResult`, then call `wait()` (respect
     the CommandRunner draining contract: drain fully before `wait`).

### Create: `adapter/opencode/args.go`

- `func buildArgs(mission engine.AgentMission) []string` — the arg-threading policy,
  table-tested: always `run`, `--model <mission.Model>`, the prompt; tool-scope mapping;
  and explicitly NO resume arg in the default single-shot contract. (If the spike
  cleared resume, gate a `--session <id>` arg behind an explicit `cfg`/policy flag and
  test it separately; default off.)

### Create: `adapter/opencode/parse.go`

- `func parseOutput(lines []string) (engine.AgentResult, error)` — map opencode's
  emitted output into the result: a `Summary` (final assistant/result text), `Steps`
  (`tool_call` / `edit` / `assistant` / `result` events folded into `[]AgentStep`),
  coarse `TokensIn` / `TokensOut` (subprocess accounting — best-effort from any usage
  line opencode emits, else zero), and `SessionID` left empty in the single-shot
  contract. Keep parsing tolerant: unknown line shapes are skipped, not fatal.

## Testing

Strict TDD. Drive with the `CommandRunner` fake (canned stdout lines + a controllable
`wait()` error) — NO real opencode binary. All FIVE scenario categories:

- **Happy path** — single-shot `run` with canned success output → an `AgentResult` with
  the expected `Summary`, `Steps`, and coarse tokens; asserts the fake received
  `run --model <provider/model>`, `Dir == mission.WorkDir`, and the env produced by
  `opencodeEnv` (base allowlist + injected key/base-url).
- **Success (variants)** — tool-scope arg threading; output with multiple step events
  folds into ordered `Steps`; a usage line maps to coarse `TokensIn`/`TokensOut`; the
  default contract emits NO resume/session arg and leaves `SessionID` empty.
- **Failure** — non-zero process exit (`wait()` returns an error) → `RunAgent` returns a
  wrapped error.
- **Error** — binary-not-found (runner `Run` returns an error before streaming);
  context cancel / timeout → returns a context error; `RevealKey` error (via the env
  helper) → propagates.
- **Edge case** — empty `WorkDir` → `ErrWorkDirRequired`-style error before launch;
  malformed / empty opencode output → tolerant parse (empty `Summary`/`Steps`, no
  panic); `MaxResponseBytes` exceeded surfaces the runner's bounded-read error.
- **Security / log-capture assertion (REQUIRED)** — capture logs across all branches
  and assert the revealed provider key never appears in any log line or returned error;
  confirm the key reaches the subprocess only via the env (assert it is in the
  `RunSpec.Env` the fake received, not in any logged field).

## Acceptance Criteria

- `adapter/opencode` implements `engine.AgentProvider` (compile-time assertion).
- `RunAgent` invokes `opencode run --model <provider/model>` with
  `cmd.Dir = mission.WorkDir`, reusing the shared `CommandRunner` seam.
- Subprocess env is built via the task-3 `opencodeEnv` helper (base allowlist + injected
  provider key + base-url); the key is present in the child env and absent from logs.
- Output parses into `AgentResult{Summary, Steps, Tokens, SessionID}` with coarse token
  accounting.
- Default session shape is single-shot mission-per-step with NO `SessionID` threading;
  resume is implemented only if the recorded Phase 1 §5 spike finding cleared headless
  resume (and is then off by default).
- Empty `WorkDir` is rejected before launch.
- A log-capture test proves the revealed key never appears in logs.
- All five scenario categories pass with the `CommandRunner` fake (no real opencode
  binary).
- All existing tests continue to pass.
