# Phase 2, Task 3: Widen `Profile` + Resolution Chain (`ResolvedBinding`)

## Goal

Widen the Phase 1 slim `Profile` with `ConnectionRef` and `Params`, and build the full resolution chain `alias → profile → connection → binding`. `ProfileResolver.Resolve` now yields a `ResolvedBinding{Kind, Provider, BaseURL, Extras, Model, Params, RevealKey}` whose `RevealKey` is a lazy closure that decrypts the key only when called at use time. An empty `ConnectionRef` resolves to the claude-cli no-key binding (the Claude subscription path needs **no** connection). This is the seam Phase 3's native adapters and opencode backend consume; the `agent_call`/`llm_call` executors are unchanged — they still hand the resolver an alias.

This task realizes the resolution half of acceptance **#7** (base-url/key resolvable from a profile) and the foundation for **#2/#3**.

**Dependency ordering:** Builds on **Phase 1 Task 3** (the slim `Profile`/`ProfileResolver` + `StaticProfileResolver` in `adapter/provider`) — **WIDEN** those types, do not redefine them from scratch. Builds on **Task 2** (`provider.ConnectionStore`, including `Reveal`). Task 4 (validation) and Task 5 (wiring) consume the widened resolver. Sources of truth: design Phase 2 §3, §6; design Phase 1 §4 (the slim shapes being widened).

## Files to Create/Modify

### Modify: `domain/provider/profile.go` (the Phase 1 slim `Profile`)

- **Widen** the existing slim `Profile` (Phase 1 §4: `Alias`, `Kind`, `Model`) by **adding** `ConnectionRef` and `Params` — keep `Alias`, `Kind`, `Model` and the existing `ProfileKind`/constants exactly as Phase 1 defined them.
  ```go
  type Profile struct {
      Alias         string
      Kind          ProfileKind    // chat | agent (unchanged from P1)
      ConnectionRef string         // -> Connection.Name; empty = claude-cli no-key (subscription)
      Model         string         // model-id, Azure deployment name, or provider/model for opencode
      Params        map[string]any // default temperature, max tokens, ...
  }
  ```
- Add the resolved-binding type that `Resolve` now produces:
  ```go
  // ResolvedBinding is the fully resolved backend selection for an alias. It is
  // the seam Phase 3 adapters consume. RevealKey is lazy: it decrypts the
  // connection's key only when invoked, at call/launch time.
  type ResolvedBinding struct {
      Kind      ProfileKind
      Provider  string            // "claude-cli" for the no-connection subscription path
      BaseURL   string
      Extras    map[string]string
      Model     string
      Params    map[string]any
      RevealKey func(ctx context.Context) (string, error) // nil-safe; no-key path returns "" 
  }
  ```
- **Change** the `ProfileResolver` port so `Resolve` returns a `ResolvedBinding` (widening the Phase 1 `Resolve(alias) (Profile, error)`). Keep the method name `Resolve`:
  ```go
  type ProfileResolver interface {
      Resolve(alias string) (ResolvedBinding, error)
  }
  ```
  Update the Phase 1 sentinel for unknown alias if one exists (reuse it); otherwise add `var ErrUnknownAlias = errors.New("unknown profile alias")`. Domain imports no adapter — `ConnectionStore`/`Crypto` are domain ports, so the resolver depending on them is hex-clean.

### Modify: `adapter/provider/static_resolver.go` (the Phase 1 `StaticProfileResolver`)

- **Widen** the Phase 1 `StaticProfileResolver` (flat, koanf-loaded registry) so its constructor also takes a `provider.ConnectionStore`, and `Resolve` runs the full chain. Keep the existing flat-registry lookup and duplicate/unknown-alias behavior.
  ```go
  func NewStaticProfileResolver(profiles []provider.Profile, store provider.ConnectionStore) *StaticProfileResolver
  ```
- `Resolve(alias)` logic:
  1. **alias → Profile** from the in-memory registry; unknown alias → `ErrUnknownAlias`.
  2. If `profile.ConnectionRef == ""` → **no-key binding**: return `ResolvedBinding{ Kind: profile.Kind, Provider: "claude-cli", BaseURL: "", Extras: nil, Model: profile.Model, Params: profile.Params, RevealKey: func(context.Context) (string, error) { return "", nil } }`. No `ConnectionStore` call at all — a Claude-only run stores zero connections (design §3).
  3. Else **ConnectionRef → Connection** via `store.Get(ctx, profile.ConnectionRef)` (metadata only); a missing connection → wrap `provider.ErrConnectionNotFound` (so callers can report "profile X references unknown connection Y"). Note: `Resolve` here takes no `ctx` per the Phase 1 signature — use `context.Background()` for the metadata `Get`, but ensure `RevealKey` accepts the caller's `ctx` (below).
  4. Build the binding from the connection: `Provider/BaseURL/Extras/Model/Params` from the `Connection` + `Profile`. `Model` comes from the `Profile`; `Provider/BaseURL/Extras` from the `Connection`.
  5. **`RevealKey`** is a closure capturing `store` and `profile.ConnectionRef`: `func(ctx context.Context) (string, error) { return store.Reveal(ctx, connRef) }`. It is invoked only by Phase 3 backends at call/launch time — **lazy**: building the binding performs no decryption.
- Compile-time assertion: `var _ provider.ProfileResolver = (*StaticProfileResolver)(nil)`.
- SECURITY: the resolver logs (if at all) the alias and connection **name** only — never the revealed key. `RevealKey`'s output is returned to the caller, never logged inside the resolver.

> Many-to-one topology and one-line backend swap are intrinsic to this chain (design §3): a shared-key endpoint is one connection serving many profiles; repointing a profile's `ConnectionRef`/`Model` or the connection's `base-url`/key swaps the backend for every node using that alias with no graph edit. No extra code — assert these in tests.

## Testing (strict TDD — all five scenario categories)

Test the widened `StaticProfileResolver` against a **fake `ConnectionStore`** (in-memory, with a recording `Reveal` so laziness is observable). Cover design §7's `Resolution chain` bullet:

- **Happy path** — alias → profile (with a `ConnectionRef`) → connection → `ResolvedBinding` carries the connection's `Provider/BaseURL/Extras` and the profile's `Model/Params/Kind`.
- **Success** — empty `ConnectionRef` resolves to the claude-cli no-key binding: `Provider == "claude-cli"`, `BaseURL == ""`, and `RevealKey(ctx)` returns `("", nil)` **without** touching the store (assert the fake store's `Reveal`/`Get` were never called).
- **Failure** — a profile whose `ConnectionRef` names a connection absent from the store fails `Resolve` with an error wrapping `ErrConnectionNotFound`.
- **Error** — unknown alias → `ErrUnknownAlias`; `RevealKey` propagates a `ConnectionStore.Reveal` decrypt error (fake store returns `ErrDecrypt`) when invoked.
- **Edge case** — **lazy `RevealKey`**: building the binding (and never calling `RevealKey`) results in zero `Reveal` calls on the fake store; calling `RevealKey` exactly N times results in exactly N `Reveal` calls (no caching assumption unless specified). Two profiles sharing one `ConnectionRef` (many-to-one) both resolve to the same `BaseURL`/key. Repointing a profile's `ConnectionRef` to a different connection changes the resolved `BaseURL` with no other change (one-line swap).

### Secrecy assertion (explicit)

- Resolve a profile bound to a connection, then call `RevealKey(ctx)` under a buffer-backed `*slog.Logger`, and assert the captured log output contains **no** substring of the revealed key — the resolver references the connection **name** only. (The end-to-end used-by-executor redaction test is in Task 5.)

## Acceptance Criteria

- `Profile` is widened (not redefined) with `ConnectionRef` + `Params`, preserving the Phase 1 `Alias/Kind/Model` and `ProfileKind`.
- `ProfileResolver.Resolve` returns a `ResolvedBinding`; the `StaticProfileResolver` runs `alias → profile → connection → binding`.
- Empty `ConnectionRef` yields the claude-cli no-key binding with **no** `ConnectionStore` call; the Claude-only path needs zero connections.
- `RevealKey` is lazy — decryption happens only when it is invoked, at use time — and the resolver never logs the revealed key.
- All five TDD scenario categories and the secrecy assertion pass; the unchanged `llm_call`/`agent_call` executors still resolve via an alias.
