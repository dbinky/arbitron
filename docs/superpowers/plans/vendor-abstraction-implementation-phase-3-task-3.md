# Phase 3, Task 3: opencode Credential Env-Injection Helper

## Goal

Provide a scoped subprocess-env injection path that layers the connection's credential
and base-url **on top of** the secret-stripping `procenv.BaseEnv()` allowlist, so the
opencode subprocess (task 4) can authenticate to a non-Claude provider. The Phase-1
`procenv.BaseEnv()` deliberately strips secrets; this helper appends the
provider-keyed `PROVIDER_API_KEY` and a provider-appropriate base-url variable,
sourcing the secret value from the resolved connection via
`ResolvedBinding.RevealKey(ctx)` **at launch only**. The revealed key lives solely in
the child process environment — it is never logged and never written to a file.

This is a small, self-contained helper (`opencodeEnv(binding)`) with table tests. It is
the env mechanism that task 4's `adapter/opencode` `RunAgent` consumes.

**This is task 3 of 5.** It depends only on Phase-1 `adapter/procenv` and the Phase-2
`ResolvedBinding`. It MUST land before task 4 (the opencode adapter calls this helper).

### Dependencies consumed (do NOT redefine — import them)

- Phase 1 `adapter/procenv` — `BaseEnv()` (allowlist-filtered, PATH-augmented base) and
  `IsBaseName(name)` (used to reject injected keys that would shadow the base allowlist,
  exactly as `adapter/process/env.go` does for node env).
- Phase 2 `domain/provider.ResolvedBinding` — `Provider`, `BaseURL`, `Extras`, and the
  lazy `RevealKey(ctx) (string, error)`. The key is fetched here at launch only.

### Hex placement

The helper lives in the `adapter/opencode` package (driven adapter). It consumes the
domain `ResolvedBinding` and the `procenv` base env. No domain code imports it.

## Files to Create/Modify

### Create: `adapter/opencode/env.go`

- The injection helper, modeled on `adapter/process/env.go`'s `buildEnv` (base env +
  scoped additions + collision rejection), but sourcing the value from the binding
  rather than the engine env:
  ```go
  // opencodeEnv returns procenv.BaseEnv() with the provider-keyed API key and
  // base-url variables appended for the opencode subprocess. The key is fetched
  // from binding.RevealKey(ctx) at call time and placed ONLY in the returned
  // child env; it is never logged or written to a file. Injected var names that
  // would shadow the base allowlist are rejected.
  func opencodeEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error)
  ```
  - Start from `procenv.BaseEnv()`.
  - Resolve the provider-keyed variable names from a small `providerEnvVars(provider
    string) (keyVar, baseURLVar string, ok bool)` map (opencode's documented per-
    provider env contract, e.g. an OpenAI-compatible/OpenRouter provider →
    `OPENAI_API_KEY` + `OPENAI_BASE_URL`; Azure → its documented pair). An unknown
    provider returns a clear `ErrUnknownProvider`.
  - Reject any injected var name for which `procenv.IsBaseName(name)` is true
    (base/secret-spoof guard), returning an error.
  - Call `binding.RevealKey(ctx)`; on error, return it (no env produced). Append
    `keyVar=<revealed key>` and `baseURLVar=<binding.BaseURL>`.
  - Never log the revealed key or the assembled env.

### Create: `adapter/opencode/env_providers.go`

- `func providerEnvVars(provider string) (keyVar, baseURLVar string, ok bool)` — the
  provider→env-name table (single source of truth for opencode's per-provider env
  contract). Keep it data-driven so adding a provider is a one-line map entry.
- `var ErrUnknownProvider = errors.New("opencode: no env mapping for provider")`.

## Testing

Strict TDD. Pure table tests with a fake binding whose `RevealKey` returns a known
sentinel (and a variant that returns an error). All FIVE scenario categories:

- **Happy path** — a known provider → returned env contains `KEYVAR=<sentinel>` and
  `BASEURLVAR=<base-url>`, appended after the base allowlist.
- **Success (variants)** — multiple providers each map to their correct
  key/base-url var names; the base allowlist (PATH, HOME, etc.) from
  `procenv.BaseEnv()` is fully preserved and PATH is still augmented.
- **Failure** — `RevealKey` returns an error → the helper returns that error and
  produces NO env (no partial injection).
- **Error** — unknown provider → `ErrUnknownProvider`; an injected var name that
  collides with a base allowlist name (`procenv.IsBaseName`) → collision error.
- **Edge case** — empty `BaseURL` (still produces the key var; base-url var empty or
  omitted per the helper's documented rule); empty provider string → `ErrUnknownProvider`.
- **Security / log-capture assertion (REQUIRED)** — install a log capture (e.g. an
  `slog` handler over a buffer) for the whole helper path and assert the revealed key
  (the sentinel) NEVER appears in any captured log output; separately assert the key IS
  present in the returned child-env slice (so the test proves it goes to the env, not
  the log).

## Acceptance Criteria

- `opencodeEnv(ctx, binding)` returns `procenv.BaseEnv()` plus the provider-keyed
  `PROVIDER_API_KEY`-equivalent and base-url variables for the binding's provider.
- The revealed key is sourced via `binding.RevealKey(ctx)` at call time only and exists
  solely in the returned child env — never in a log line and never written to a file.
- Injected var names that would shadow the base allowlist are rejected.
- Unknown providers return `ErrUnknownProvider`; `RevealKey` errors abort with no
  partial env.
- A log-capture test proves the revealed key never appears in logs while confirming it
  is present in the returned env.
- All five scenario categories pass.
- All existing tests continue to pass.
