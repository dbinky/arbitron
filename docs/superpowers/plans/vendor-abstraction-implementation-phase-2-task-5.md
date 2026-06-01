# Phase 2, Task 5: Wiring (`main.go`) + Management Surface + Secrecy/Log-Redaction Tests

## Goal

Wire the Phase 2 pieces together in `cmd/arbitron/main.go`: construct `AESGCM` (master key from env/keyring) → `ConnectionStore` (sqlc/sqlite) → the widened `ProfileResolver`, and inject the resolver into the executors and the connection-existence checker into validation. Add the connection **management surface** (CRUD; the plaintext key is supplied only at create/update, then immediately sealed). Add the end-to-end **secrecy/log-redaction tests** that capture log output across a resolved-and-used connection and assert no plaintext key ever appears — logs reference the connection **name** only.

This task completes acceptance **#7** (keys never in `workflow.json`, never logged, AES-GCM at rest, master key in memory only).

**Dependency ordering:** Final Phase 2 task. Builds on **Task 1** (`AESGCM` + `LoadMasterKey` + `RequireMasterKeyIfConnections`), **Task 2** (`ConnectionStore`/`ConnectionRepo` + `Count`), **Task 3** (widened `StaticProfileResolver`), **Task 4** (validation checker + `ValidateConnection`). Also builds on **Phase 1 Task 6** wiring (the claude-cli adapters, `StaticProfileResolver`, and `agent_call` registration already in `main.go`). Sources of truth: design Phase 2 §5, §6, §7; existing `cmd/arbitron/main.go` `initDomain` ordering; `domain/config/config.go` + `adapter/config/loader.go`.

## Files to Create/Modify

### Modify: `domain/config/config.go`

- Add a `Profiles` registry block if Phase 1 has not already (Phase 1 §6 adds `[]{ alias, kind, model }`); **widen** each entry with `connection-ref` and `params` per Task 3:
  ```go
  type ProfileConfig struct {
      Alias         string
      Kind          string         // "chat" | "agent"
      ConnectionRef string         // empty = claude-cli no-key subscription
      Model         string
      Params        map[string]any
  }
  ```
  Add `Profiles []ProfileConfig` to `Config`. **No** connection rows or API keys live in config (keys are supplied only via the management surface and immediately sealed — design §5). The master key comes from `ARBITRON_MASTER_KEY`/keyring, **not** from `config.yaml`.

### Modify: `adapter/config/loader.go`

- Load the `profiles` block (koanf, env-overridable per the existing precedence), widened with `connection-ref` + `params`. Add defaults/`defaultYAMLContent` documentation for the `profiles:` block consistent with the existing commented style. Do **not** add any API-key or master-key field to the YAML/defaults.

### Modify: `cmd/arbitron/main.go` (`initDomain`)

Insert into the existing `initDomain` ordering (after the DB is open + migrated, around steps 5–9), keeping the established numbered-step comment style:

- **Construct crypto + key bootstrap.** `key, err := adaptcrypto.LoadMasterKey()`; treat `ErrMasterKeyMissing` as "no key yet". Build `crypto` only if a key is present.
- **Construct the connection store.** `connStore := adaptsqlite.NewConnectionRepo(db, crypto)`.
- **Fail-fast gate.** `count, _ := connStore.Count(ctx)`; `if err := adaptcrypto.RequireMasterKeyIfConnections(key != nil, count); err != nil { ... return clear startup error }` — the server refuses to start when connections exist but no master key is available (cannot decrypt). A zero-connection (Claude-only) boot proceeds with no master key.
- **Widen the resolver.** Replace the Phase 1 `StaticProfileResolver` construction with the Task 3 widened form: `resolver := adaptprovider.NewStaticProfileResolver(profilesFromConfig(cfg.Profiles), connStore)`. Map `cfg.Profiles` → `[]provider.Profile`.
- **Inject the resolver into executors.** The `llm_call`/`agent_call` executors take the resolver as in Phase 1 (unchanged contract); ensure they now receive the widened resolver. Keep `claude-cli` chat/agent adapters wired as Phase 1 set them.
- **Inject the connection checker into validation.** Build a small adapter implementing Task 4's `workflow.ConnectionChecker` over `connStore` + the loaded profiles (`ProfileConnectionRef` from the profile registry, `ConnectionExists` from `connStore.Get`/a cached `List`), and thread it wherever `workflow.Validate` is called (the validate path used by `workflow_validate` — MCP/HTTP). Mirror how the existing `FileChecker` is injected.
- Add the new imports (`adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"`, `"github.com/arbitron/arbitron/domain/provider"`, the `adapter/provider` alias) and add `connStore` (and `crypto`) to the `domainStack` struct + `Close`/`mcpDeps` as needed for the management surface.

### Create: management surface — `adapter/mcp` (and/or `adapter/http`) connection CRUD

- Add a connection-management write path consistent with the existing MCP/HTTP tool/handler style (`adapter/mcp` `RegisterAllTools`, or an `adapter/http` handler). Operations map 1:1 to `ConnectionStore`:
  - `connection_create` — params: name, provider, base_url, extras, **api_key**; calls `connStore.Create(ctx, conn, apiKey)`. The plaintext key is accepted **only** here, passed straight to `Create`, sealed immediately; it is never echoed back, never stored in a response, never logged.
  - `connection_update` — params include an **optional** api_key (omitted → nil → key preserved per Task 2).
  - `connection_delete`, `connection_get` (metadata only — never returns the key), `connection_list` (metadata only).
- SECURITY: request-logging middleware must not log the `api_key` field. Ensure the create/update request bodies are excluded from any body-logging, or the key field is redacted, before logging. Responses for get/list contain metadata only.

## Testing (strict TDD — all five scenario categories)

Cover design §7's `Secrecy assertions` bullet plus the wiring, with the explicit log-capture redaction test:

- **Happy path** — `initDomain`-style wiring (or a focused construction test) builds `AESGCM → ConnectionStore → widened resolver` and the resolver resolves a profile bound to a created connection to a `ResolvedBinding` whose `RevealKey` returns the right key.
- **Success** — the management surface round-trips: `connection_create` then `connection_get` returns metadata with **no** key; `connection_list` returns metadata only; `connection_update` with no api_key preserves the key; `connection_delete` removes it.
- **Failure** — boot with `connStore.Count > 0` and no master key available returns the fail-fast startup error (cannot decrypt); creating a connection whose name already exists surfaces `ErrConnectionExists` through the management surface; resolving a profile referencing a missing connection surfaces a clear error.
- **Error** — `connection_get`/`connection_update`/`connection_delete` on a missing connection returns `ErrConnectionNotFound` through the surface; an Azure `connection_create` missing `api-version` is rejected (Task 4 `ValidateConnection`).
- **Edge case** — a **Claude-only** deployment (zero connections, no master key) boots successfully and resolves an empty-`ConnectionRef` profile to the no-key binding; `workflow_validate` passes for a workflow using only the no-connection Claude profile.

### Secrecy / log-redaction test (explicit, required)

- Drive a full path: create a connection with a known sentinel key (e.g. `"sk-SENTINEL-DO-NOT-LOG"`), resolve a profile bound to it, invoke `RevealKey(ctx)`, and (if a Phase-3-style consumer is stubbed) hand the key to a no-op request builder. Capture **all** log output during this sequence via a buffer-backed `*slog.Logger` installed on the engine/resolver/store/management surface, then assert:
  - the captured output contains **no** substring of the sentinel key;
  - log lines that reference the connection reference its **name** only;
  - the management `create`/`update` request logging contains no `api_key` value.
- Add a `workflow.json`/snapshot assertion: round-trip a workflow that uses these profiles and assert the serialized workflow contains **no** API key and no connection secret (nodes hold only the `model_profile` alias) — confirming keys never appear in `workflow.json`.

## Acceptance Criteria

- `main.go` constructs `AESGCM` (key from `ARBITRON_MASTER_KEY`/keyring, in memory only) → `ConnectionStore` (sqlc/sqlite) → widened `ProfileResolver`, injects the resolver into the executors and a `ConnectionChecker` into validation, and fails fast when connections exist but no master key is available.
- A management surface provides connection CRUD; the plaintext key is accepted only at create/update, sealed immediately, and never returned, persisted in plaintext, or logged.
- The log-redaction test proves no plaintext key appears in any log across resolve-and-use; logs reference the connection name only; `workflow.json` contains no keys.
- A Claude-only (zero-connection, no-master-key) deployment boots and resolves the no-key binding.
- All five TDD scenario categories and the explicit secrecy/redaction assertions pass; existing tests still pass.
