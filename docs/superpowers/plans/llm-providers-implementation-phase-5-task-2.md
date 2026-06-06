# LLM Providers — Phase 5, Task 2: `arbitron connection validate <name>` CLI command

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `newConnectionCmd()` parent cobra command (registered alongside `newServeCmd()`/`newMCPCmd()`) hosting a single subcommand `arbitron connection validate <name>` that builds the same connection/decryption stack the server uses (load config + master key → SQLite `ConnectionRepo` → `StaticProfileResolver`), resolves `<name>` to a `ResolvedBinding`, runs the Task-1 `ProbeSelector` → `ConnectionProbe`, and prints a clear, secret-free result with an exit code reflecting success/failure.

**Architecture:** A new `cmd/arbitron/connection.go` adds `newConnectionCmd()` + the `validate` subcommand. Validation logic is factored into a small **testable function** `runConnectionValidate(ctx, deps, name, out io.Writer) error` whose dependencies (`ConnectionStore`, `ProfileResolver`-like binding resolution, `ProbeSelector`, master-key availability) are injected via a `validateDeps` struct, so the five-category TDD suite drives it with fakes and no live config/DB/network. A thin builder `buildValidateDeps()` assembles the production stack from config (mirroring `initDomain`'s steps 1, 8a–8d) for the cobra `RunE`. Connection CRUD stays MCP-only; the parent command exists only to host `validate`.

**Tech Stack:** Go, cobra (`cmd/arbitron`), `testing` + `testify`. The `connection validate` path reuses `adapter/config.Load`, `adapter/crypto.{LoadMasterKey,NewAESGCM,RequireMasterKeyIfConnections}`, `adapter/sqlite.{Open,RunMigrations,NewConnectionRepo}`, `adapter/config.ProfilesFromConfig`, `adapter/provider.NewStaticProfileResolver`, and the Task-1 `adapter/probe.NewProbeFactory`. `make test` == `go test ./...` (hermetic).

---

## Dependencies

- **Task 1 (this phase):** `provider.ConnectionProbe`, `provider.ProbeResult`, `provider.ProbeSelector`, `provider.ErrNoProbeForProvider` (`domain/provider/probe.go`) and `adapter/probe.NewProbeFactory(Deps{...})` returning a `provider.ProbeSelector`. **Task 2 cannot start until Task 1's port + factory compile.**
- **Existing composition-root pieces reused (see `cmd/arbitron/main.go`):**
  - Root registration pattern: `rootCmd.AddCommand(newServeCmd())` / `rootCmd.AddCommand(newMCPCmd())` (lines 87–88); `newMCPCmd()` shape (lines 106–113).
  - Config load: `configadapter.Load(filepath.Join(home, ".arbitron", "config.yaml"))` (lines 225–235).
  - DB: `adaptsqlite.Open(cfg.Database.Path)` + `adaptsqlite.RunMigrations(db)` (lines 245–258).
  - Connection secrecy: `adaptcrypto.LoadMasterKey(logger)` (line 384, returns `provider.ErrMasterKeyMissing` when absent), `adaptcrypto.NewAESGCM(masterKey)` (line 395), `adaptsqlite.NewConnectionRepo(db, connCrypto)` (line 406), `connStore.Count(ctx)` + `adaptcrypto.RequireMasterKeyIfConnections(haveKey, connCount)` (lines 411–421).
  - Resolver: `configadapter.ProfilesFromConfig(cfg.Profiles)` (line 430) + `providersadapter.NewStaticProfileResolver(domainProfiles, connStore)` (line 431). `Resolve(ctx, alias)` runs alias→profile→connection→`ResolvedBinding` with a lazy `RevealKey` (`adapter/provider/static.go`).
  - HTTP client + caps: `cfg.LLM.TimeoutSeconds`, `cfg.LLM.MaxResponseBytes` (lines 460–476).
- **Domain seams:** `provider.ResolvedBinding`, `provider.ErrConnectionNotFound` (reused for unknown name), `provider.ErrUnknownProfile`.

> **Resolving `<name>` to a binding — connection name, not profile alias.** `validate` takes a **connection name** (the operator validates a *connection*, per the spec's onboarding path: configure connection → validate → reference from a node). The `StaticProfileResolver.Resolve` takes a *profile alias*, so it is not the right entry point for a bare connection name. Instead, build the binding directly from the connection store: `conn, err := connStore.Get(ctx, name)` (returns `ErrConnectionNotFound` for unknown), then assemble `ResolvedBinding{Provider: conn.Provider, BaseURL: conn.BaseURL, Extras: conn.Extras, Model: "", RevealKey: func(ctx) (string,error){ return connStore.Reveal(ctx, name) }}`. This shares the **one** resolution/decryption path (the same `ConnectionStore.Get`/`Reveal` the resolver uses) without needing a profile alias. The `Model` is empty (probing lists models or checks reachability — it does not call a model). A `bindingFromConnection` helper holds this so it is unit-testable.

## Spec / design references

- Spec `docs/specs/llm-providers-spec.md` — "Connection Validation & Model Listing" (success/reachability-only/specific-failure output); success criterion #5; #6 (no secret leakage).
- Design `docs/superpowers/specs/llm-providers-design-phase-5.md` — "CLI surface" (`newConnectionCmd()` parent + single `validate` subcommand; CRUD stays MCP-only), "Data flow", "Error handling".

## File Structure

| File | Responsibility |
|---|---|
| `cmd/arbitron/connection.go` | **New.** `newConnectionCmd()` parent + `validate` subcommand; `validateDeps` struct; `buildValidateDeps()` production assembler; `runConnectionValidate(ctx, deps, name, out)` (testable core); `bindingFromConnection(...)`; `formatProbeResult(...)`. |
| `cmd/arbitron/main.go` | **Modify** (line 88 area): register `rootCmd.AddCommand(newConnectionCmd())`. |
| `cmd/arbitron/connection_test.go` | **New.** Five-category TDD over `runConnectionValidate` + `formatProbeResult` with fake `ConnectionStore` / `ProbeSelector` (no config/DB/network). |

## Files

- **Modify:** `cmd/arbitron/main.go` — add one registration line after the existing two:

```go
	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())
	rootCmd.AddCommand(newConnectionCmd())
```

- **Create:** `cmd/arbitron/connection.go`

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	configadapter "github.com/arbitron/arbitron/adapter/config"
	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptprobe "github.com/arbitron/arbitron/adapter/probe"
	providersadapter "github.com/arbitron/arbitron/adapter/provider"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/provider"
)

// newConnectionCmd is the parent for connection operator subcommands. CRUD
// (create/update/delete) stays MCP-only; this group exists only to host
// `validate`, mirroring the newServeCmd/newMCPCmd registration pattern.
func newConnectionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "connection",
		Short: "Operator commands for provider connections",
		Long:  "Operator commands for provider connections. Creation/update/deletion are MCP-only; this group hosts on-demand validation.",
	}
	cmd.AddCommand(newConnectionValidateCmd())
	return cmd
}

// newConnectionValidateCmd builds `arbitron connection validate <name>`.
func newConnectionValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <name>",
		Short: "Validate a connection's credentials + reachability and list its models",
		Long: "Resolve the named connection, authenticate against its provider, confirm reachability, " +
			"and list available models where the provider supports it. Never prints the secret. " +
			"Exit code 0 on success, non-zero on any validation failure.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			deps, cleanup, err := buildValidateDeps(ctx)
			if err != nil {
				return err
			}
			defer cleanup()
			return runConnectionValidate(ctx, deps, args[0], cmd.OutOrStdout())
		},
	}
}

// validateDeps are the injected dependencies of runConnectionValidate, so the
// command core is unit-testable with fakes (no config/DB/network).
type validateDeps struct {
	store    provider.ConnectionStore
	selector provider.ProbeSelector
}

// buildValidateDeps assembles the production stack: config + master key ->
// SQLite ConnectionRepo (decryption path) -> ProbeFactory. It mirrors
// initDomain steps 1 + 8a–8d so the CLI shares one resolution/decryption path
// with the server. The returned cleanup closes the DB.
func buildValidateDeps(ctx context.Context) (validateDeps, func(), error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return validateDeps{}, nil, fmt.Errorf("determining home directory: %w", err)
	}
	cfg, err := configadapter.Load(filepath.Join(home, ".arbitron", "config.yaml"))
	if err != nil {
		return validateDeps{}, nil, fmt.Errorf("loading config: %w", err)
	}
	db, err := adaptsqlite.Open(cfg.Database.Path)
	if err != nil {
		return validateDeps{}, nil, fmt.Errorf("opening database: %w", err)
	}
	if err := adaptsqlite.RunMigrations(db); err != nil {
		db.Close()
		return validateDeps{}, nil, fmt.Errorf("running migrations: %w", err)
	}
	// Master key: absent is tolerated only when zero connections exist.
	logger := slog.Default()
	masterKey, err := adaptcrypto.LoadMasterKey(logger)
	if err != nil && !errors.Is(err, provider.ErrMasterKeyMissing) {
		db.Close()
		return validateDeps{}, nil, fmt.Errorf("loading master key: %w", err)
	}
	var connCrypto provider.Crypto
	if masterKey != nil {
		connCrypto, err = adaptcrypto.NewAESGCM(masterKey)
		if err != nil {
			db.Close()
			return validateDeps{}, nil, fmt.Errorf("building crypto: %w", err)
		}
	}
	store := adaptsqlite.NewConnectionRepo(db, connCrypto)
	count, err := store.Count(ctx)
	if err != nil {
		db.Close()
		return validateDeps{}, nil, fmt.Errorf("counting connections: %w", err)
	}
	if err := adaptcrypto.RequireMasterKeyIfConnections(masterKey != nil, count); err != nil {
		db.Close()
		return validateDeps{}, nil, err
	}
	selector := adaptprobe.NewProbeFactory(adaptprobe.Deps{
		Timeout:          time.Duration(cfg.LLM.TimeoutSeconds) * time.Second,
		MaxResponseBytes: cfg.LLM.MaxResponseBytes,
		ClaudeBinaryPath: cfg.LLM.BinaryPath,
	})
	return validateDeps{store: store, selector: selector}, func() { db.Close() }, nil
}

// bindingFromConnection turns a stored connection into a ResolvedBinding for
// probing, attaching the lazy RevealKey closure that shares the store's sole
// decryption path. Model is empty (probing lists/reaches, it never calls a model).
func bindingFromConnection(store provider.ConnectionStore, name string, c provider.Connection) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: c.Provider,
		BaseURL:  c.BaseURL,
		Extras:   c.Extras,
		RevealKey: func(ctx context.Context) (string, error) {
			return store.Reveal(ctx, name)
		},
	}
}

// runConnectionValidate is the testable core: resolve name -> binding -> probe,
// then format the outcome. It returns a non-nil error on any validation failure
// (which cobra turns into a non-zero exit via os.Exit(1) in main()).
func runConnectionValidate(ctx context.Context, deps validateDeps, name string, out io.Writer) error {
	conn, err := deps.store.Get(ctx, name)
	if err != nil {
		if errors.Is(err, provider.ErrConnectionNotFound) {
			return fmt.Errorf("no such connection %q", name)
		}
		return fmt.Errorf("looking up connection %q: %w", name, err)
	}
	binding := bindingFromConnection(deps.store, name, conn)
	probe, err := deps.selector.ProbeFor(binding)
	if err != nil {
		return fmt.Errorf("validate %q: %w", name, err)
	}
	res, err := probe.Probe(ctx, binding)
	if err != nil {
		// The probe's classified error (credentials/unreachable/region) is
		// already secret-free; surface it as the command failure.
		return fmt.Errorf("validate %q: %w", name, err)
	}
	fmt.Fprint(out, formatProbeResult(name, conn.Provider, res))
	return nil
}

// formatProbeResult renders a successful probe outcome. It never includes the
// secret (it has no access to it). Three shapes: models listed, listing
// unsupported (reachability only), or reachable with an empty list.
func formatProbeResult(name, providerName string, res provider.ProbeResult) string {
	switch {
	case res.ModelsUnsupported:
		return fmt.Sprintf("connection %q (%s): reachable. model listing not supported for this provider.\n", name, providerName)
	case len(res.Models) == 0:
		return fmt.Sprintf("connection %q (%s): reachable, no models.\n", name, providerName)
	default:
		s := fmt.Sprintf("connection %q (%s): reachable. %d model(s):\n", name, providerName, len(res.Models))
		for _, m := range res.Models {
			s += "  - " + m + "\n"
		}
		return s
	}
}
```

> **Exit code:** `main()` already does `if err := rootCmd.Execute(); err != nil { os.Exit(1) }` (`cmd/arbitron/main.go` lines 90–92). Returning a non-nil error from `runConnectionValidate`/`RunE` yields a non-zero exit; returning `nil` (success) yields exit 0. No new exit-code plumbing is needed. Set `cmd.SilenceUsage = true` on the validate command (add `SilenceUsage: true` to its `cobra.Command` literal) so a validation failure prints the error, not the usage text.

- **Test:** `cmd/arbitron/connection_test.go`

## TDD test list (five categories)

**Seams:** a `fakeConnectionStore` implementing `provider.ConnectionStore` (returns a canned `Connection` from `Get`, a canned secret from `Reveal`, or `ErrConnectionNotFound`); a `fakeProbeSelector`/`fakeProbe` implementing `provider.ProbeSelector`/`provider.ConnectionProbe` (returns a canned `ProbeResult` or a classified error); a `bytes.Buffer` as `out`. Tests drive `runConnectionValidate(ctx, deps, name, &buf)` and `formatProbeResult(...)` directly — **no config/DB/network**. Each test: write failing test → run (`go test ./cmd/arbitron/... -run <Name> -v`, expect FAIL) → minimal impl → run (expect PASS) → commit.

### Happy-path

- [ ] `TestRunConnectionValidate_ListsModels` — fake store returns `Connection{Name:"oai", Provider:"openai", BaseURL:"https://api.openai.com/v1"}`; fake probe returns `ProbeResult{Reachable:true, Models:[]string{"gpt-4o","gpt-4o-mini"}}`. `require.NoError`; assert `buf.String()` contains `reachable`, `2 model(s)`, `gpt-4o`, `gpt-4o-mini`.
- [ ] `TestRunConnectionValidate_ReachabilityOnly` — fake probe returns `ProbeResult{Reachable:true, ModelsUnsupported:true}` (Claude CLI shape). `require.NoError`; assert `buf.String()` contains `reachable` and `model listing not supported`, and does NOT contain `model(s):`.
- [ ] `TestFormatProbeResult_ThreeShapes` — table over `{models listed → "N model(s)" + each id, ModelsUnsupported → "not supported", empty list → "reachable, no models"}`. Assert each rendered string.
- [ ] `TestNewConnectionCmd_HostsValidateOnly` — `cmd := newConnectionCmd()`; assert `cmd.Use == "connection"`, that it has exactly one subcommand whose `Use` starts with `"validate"`, and that no `create`/`update`/`delete` subcommand exists (CRUD stays MCP-only). Also assert the root registration: build the root the same way `main()` does (or assert `newConnectionCmd()` is wired) — a focused check that `validate` requires exactly one arg (`cmd.Args` is `cobra.ExactArgs(1)`).

### Input-validation

- [ ] `TestRunConnectionValidate_UnknownConnection` — fake store's `Get` returns `provider.ErrConnectionNotFound`. `require.Error`; assert `assert.Contains(err.Error(), "no such connection")` and `assert.Contains(err.Error(), `"missing"`)` (the name echoed); assert `buf.Len() == 0` (no success output). The probe selector is never consulted (fake selector records zero calls).
- [ ] `TestConnectionValidate_RequiresExactlyOneArg` — execute `newConnectionValidateCmd()` with zero args and with two args via `cmd.Execute()` over a `cmd.SetArgs([]string{...})`; assert both error (cobra `ExactArgs(1)` enforcement). (Use a command whose `RunE` is stubbed to `nil` so only arg parsing is exercised, or assert `cmd.Args` directly.)

### Error-handling

- [ ] `TestRunConnectionValidate_CredentialsRejected` — fake probe returns `("", probeErr)` where `probeErr` wraps the credentials sentinel (use `adaptprobe.ErrCredentialsRejected`). `require.Error`; assert `assert.ErrorIs(err, adaptprobe.ErrCredentialsRejected)` and `assert.Contains(err.Error(), "missing"?? name)`; assert `buf.Len() == 0`.
- [ ] `TestRunConnectionValidate_Unreachable` — fake probe returns the `adaptprobe.ErrUnreachable` sentinel. `require.Error`, `assert.ErrorIs(err, adaptprobe.ErrUnreachable)`, no success output.
- [ ] `TestRunConnectionValidate_RegionConfigError` — fake probe returns `adaptprobe.ErrRegionConfig`. `require.Error`, `assert.ErrorIs(err, adaptprobe.ErrRegionConfig)`.
- [ ] `TestRunConnectionValidate_NoProbeForProvider` — fake selector's `ProbeFor` returns `provider.ErrNoProbeForProvider`. `require.Error`, `assert.ErrorIs(err, provider.ErrNoProbeForProvider)`; the probe is never called (fake probe records zero `Probe` calls).
- [ ] `TestRunConnectionValidate_StoreGetTransientError` — fake store's `Get` returns a non-`ErrConnectionNotFound` error (e.g. `errors.New("db locked")`). `require.Error`; assert the message contains `looking up connection` and is NOT phrased as "no such connection" (distinguishes a missing connection from a store fault).

### Edge-cases

- [ ] `TestRunConnectionValidate_EmptyModelList` — fake probe returns `ProbeResult{Reachable:true, Models:nil}`. `require.NoError`; assert `buf.String()` contains `reachable, no models` and not `model(s):`.
- [ ] `TestRunConnectionValidate_KeylessConnection_NoReveal` — fake store with a `Reveal` that fails the test if called for a keyless connection is unnecessary; instead assert the binding's `RevealKey` is invoked **only by the probe**: use a fake probe that calls `binding.RevealKey(ctx)` and a store whose `Reveal` returns `""` (keyless local-Ollama). `require.NoError`; assert the store recorded exactly one `Reveal("ollama-local")` call (the single decryption path), proving `bindingFromConnection` wired `Reveal` lazily.
- [ ] `TestBindingFromConnection_MapsFields` — `c := provider.Connection{Provider:"bedrock", BaseURL:"", Extras:map[string]string{"region":"us-east-1","access-key-id":"AKIA"}}`; `b := bindingFromConnection(store, "bed", c)`; assert `b.Provider=="bedrock"`, `b.Extras["region"]=="us-east-1"`, `b.Model==""`, and that invoking `b.RevealKey(ctx)` calls `store.Reveal(ctx,"bed")` (record the arg).

### Security

- [ ] `TestRunConnectionValidate_NeverPrintsSecret` — fake store's `Reveal` returns the sentinel `"sk-SUPER-SECRET"`; fake probe ignores it and returns a normal model list. Install a buffer-backed `slog` default. `require.NoError`; assert `assert.NotContains(buf.String(), "sk-SUPER-SECRET")` AND `assert.NotContains(logBuf.String(), "sk-SUPER-SECRET")` (the command output never includes the secret; the secret reaches only the probe's auth header — `formatProbeResult` has no access to it by construction).
- [ ] `TestRunConnectionValidate_ProbeErrorSecretFree` — fake probe returns an error string that does NOT contain the secret (probes are secret-free by Task-1 contract); assert the command-wrapped error also excludes the sentinel: `assert.NotContains(err.Error(), "sk-SUPER-SECRET")`. This guards the wrapping in `runConnectionValidate` from re-introducing the secret.

> **Note on the `assert.Contains(err.Error(), "missing"?? name)` placeholders above:** in each test, pass the connection name literal (e.g. `"missing"` for the unknown-connection test, `"oai"` for others) and assert it is echoed in the error so the operator sees which connection failed. Replace the `??` shorthand with the test's own `name` constant when writing the test.

## Acceptance criteria (what `go test ./...` proves)

- `go test ./cmd/arbitron/...` passes, proving:
  - `arbitron connection validate <name>` resolves a connection via the shared `ConnectionStore.Get`/`Reveal` path, probes it, and prints: a model list (when supported), "model listing not supported" (reachability only), or "reachable, no models" (empty list).
  - An unknown connection name yields a clear "no such connection" error (built on `ErrConnectionNotFound`) and no success output; a store fault is distinguished from a missing connection.
  - Probe failures map to specific, secret-free command errors that preserve the credentials / unreachable / region-config classification via `errors.Is`, and produce a non-zero exit (non-nil error → `os.Exit(1)` in `main`).
  - The `connection` parent hosts only `validate` (CRUD stays MCP-only) and `validate` requires exactly one argument.
  - No secret appears in command output or logs.
- `go test ./...` (`make test`) stays green — the change adds one cobra group + one registration line and a testable core; no existing behavior is altered.

## "No secret in logs/errors" security checks

- `formatProbeResult` and `runConnectionValidate` never receive the plaintext key: the secret is fetched lazily by the probe via `binding.RevealKey` and used only for the provider's auth header (Task 1). The CLI core holds the `RevealKey` closure but never calls it itself.
- Probe errors are secret-free by the Task-1 contract (`scrubControl` + classified sentinels, no key interpolation); `runConnectionValidate` wraps them with only the connection *name*, never the secret. `TestRunConnectionValidate_NeverPrintsSecret` and `TestRunConnectionValidate_ProbeErrorSecretFree` assert this against a sentinel secret in both stdout and `slog` output.
- `SilenceUsage = true` ensures a failure prints the (secret-free) error, not a usage dump that could echo args.
