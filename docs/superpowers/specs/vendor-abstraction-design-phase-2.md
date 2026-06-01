# Vendor-Abstraction MVP — Design Phase 2: Connection Store, Full Profiles & Resolution

**Date:** 2026-06-01 · **Status:** Design approved; pending implementation plan
**Spec:** `docs/specs/feature-pipeline-replacement-mvp.md`
**Depends on:** Phase 1 (slim profile registry + `ProfileResolver` port).

## Overview

Phase 2 turns the slim Phase-1 profile into the full three-concept model from spec §5–§6: **connections** (endpoint + credential), **models** (`model-id` on top of a connection), and **profiles** (the unit a node references). It adds a Postgres-backed **connection store** with API keys **encrypted at rest** (AES-256-GCM), a `Crypto` port, and the full resolution chain `alias → profile → connection → backend + injected credential`. Nodes are **not** re-touched — they still reference `model_profile: <alias>`; only the resolution behind the alias gets richer. This phase produces no new *node* behavior; it produces the credential and endpoint machinery that Phase 3's native adapters and opencode backend consume.

Realizes the resolution half of acceptance **#2/#3/#7**; the safety half (#7: keys never in `workflow.json`, never logged) is enforced here.

---

## 1. Connection Store — `domain/provider` + `adapter/postgres`

A connection binds an endpoint to its credential (spec §6). `model-id` is deliberately excluded so one connection fronts many models.

```go
// domain/provider
type Connection struct {
    Name     string
    Provider string            // "azure-openai" | "azure-foundry" | "openrouter" | "litellm" | ...
    BaseURL  string
    Extras   map[string]string // e.g. api-version (Azure), region
    // APIKey is never stored in plaintext on this struct outside resolution.
}

type ConnectionStore interface {
    Create(ctx context.Context, c Connection, apiKey string) error
    Update(ctx context.Context, c Connection, apiKey *string) error // nil key = leave unchanged
    Delete(ctx context.Context, name string) error
    Get(ctx context.Context, name string) (Connection, error)       // metadata only; no plaintext key
    List(ctx context.Context) ([]Connection, error)
    // Reveal returns the decrypted key; called only at resolution time.
    Reveal(ctx context.Context, name string) (apiKey string, err error)
}
```

### Postgres schema (sqlc)

```sql
CREATE TABLE connections (
    name           text PRIMARY KEY,
    provider       text        NOT NULL,
    base_url       text        NOT NULL,
    extras         jsonb       NOT NULL DEFAULT '{}',
    api_key_cipher bytea       NOT NULL,
    api_key_nonce  bytea       NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
```

`adapter/postgres` implements `ConnectionStore` with sqlc-generated queries. The ciphertext + nonce columns hold the AES-GCM output; plaintext keys never hit a column, a log line, or a query parameter beyond the encrypt call.

---

## 2. Encryption — `Crypto` port + `adapter/crypto`

```go
// domain/provider
type Crypto interface {
    Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
    Open(ciphertext, nonce []byte) (plaintext []byte, err error)
}
```

- **Adapter:** `adapter/crypto` provides an `AESGCM` implementation. A fresh random 96-bit nonce per seal; AES-256-GCM AEAD.
- **Master key:** sourced once at boot from `ARBITRON_MASTER_KEY` (32 bytes, base64) or the OS keyring; held in memory only. If absent **and** any connection exists, the server fails fast with a clear startup error (cannot decrypt).
- **No plaintext at rest, no plaintext in logs.** `ConnectionStore.Reveal` is the single decryption entry point; callers pass the revealed key straight into a request builder or subprocess env and never persist or log it.

---

## 3. Full Profile Resolution — `domain/provider`

The Phase-1 `Profile` gains `ConnectionRef` and default params. The registry stays flat and global (no named sets); a config swap repoints aliases or their connections.

```go
type Profile struct {
    Alias         string
    Kind          ProfileKind // chat | agent (unchanged from P1)
    ConnectionRef string      // -> Connection.Name
    Model         string      // model-id, or Azure deployment name, or provider/model for opencode
    Params        map[string]any // default temperature, max tokens, ...
}
```

### Resolution chain

`ProfileResolver.Resolve(alias)` now yields a **resolved binding**:

1. alias → `Profile` (from the registry).
2. `Profile.ConnectionRef` → `Connection` (metadata) from `ConnectionStore`.
3. `Connection.Provider` selects the concrete backend: a **native adapter** (Azure/OpenRouter chat — Phase 3) or a **harness + `provider/model` string** (opencode agent — Phase 3).
4. Credential injected on demand via `ConnectionStore.Reveal` at call/launch time.

```go
type ResolvedBinding struct {
    Kind       ProfileKind
    Provider   string
    BaseURL    string
    Extras     map[string]string
    Model      string
    Params     map[string]any
    RevealKey  func(ctx context.Context) (string, error) // lazy; decrypt at use
}
```

- **Subscription `claude -p`** needs **no connection** (spec §6): a Claude profile may have an empty `ConnectionRef`, resolving to the ambient-login claude-cli backend with no key. A Claude-only run stores zero connections.
- **Many-to-one topology:** a shared-key endpoint (OpenRouter, an Azure OpenAI resource) is one connection serving many profiles; a per-model endpoint (Azure AI Foundry serverless) is one connection per profile. Both are just rows.
- **One-line backend swap:** repointing a profile's `ConnectionRef`/`Model` (or the connection's `base-url`/key) changes the backend for every node using that alias — no graph edit.

The `ResolvedBinding` is the seam Phase 3's adapters consume; Phase 1's `StaticProfileResolver` is widened to this chain. `agent_call`/`llm_call` executors are unchanged — they still hand the resolver an alias and receive a backend.

---

## 4. Validation Extensions — `domain/workflow/validation.go`

- A profile's `ConnectionRef` (when non-empty) must name an existing connection — checked at `workflow_validate` via the injected resolver/store checker.
- Kind-matching (P1) is unchanged.
- Azure's required `api-version` must be present in the connection `Extras` for Azure providers (checked when the connection is created/updated, not at workflow validate time).

---

## 5. Config / Management Surface

- **Connections** are managed through the server (CRUD via `ConnectionStore`), surfaced later by the Phase 5 dashboard/MCP write path. For MVP they can also be seeded from config/CLI; the **plaintext key is supplied at create/update time only** and immediately sealed.
- **Profiles** remain a koanf-loaded server-level registry (now including `connection-ref` + params), env-overridable per existing precedence. Still **not** in `workflow.json`.
- `cmd/arbitron/main.go`: construct `AESGCM` (master key from env/keyring) → `ConnectionStore` (Postgres) → widened `ProfileResolver`; inject the resolver into the executors and validation.

---

## 6. Security Requirements (acceptance #7)

- Keys never appear in `workflow.json` (profiles/connections are server-side; nodes hold only an alias).
- Keys never logged: structured logs reference a connection **name**, never the key; `Reveal` output is never passed to a logger.
- Each key is scoped to the connection that owns it; AES-GCM ciphertext + per-row nonce at rest.
- Master key only in memory, sourced at boot; fail-fast if missing while connections exist.

---

## 7. Testing Strategy (strict TDD, 5 scenario categories)

- **`AESGCM` crypto** — seal/open round-trip; tampered ciphertext fails open; wrong-key fails; empty plaintext; nonce uniqueness across seals.
- **`ConnectionStore` (Postgres)** — create/get/list/update/delete; `Reveal` decrypts; update-with-nil-key preserves the key; get returns no plaintext; missing-connection error; duplicate-name edge.
- **Resolution chain** — alias→profile→connection→binding happy path; empty `ConnectionRef` → claude-cli no-key binding; unknown connection error; unknown alias error; lazy `RevealKey` only decrypts when called.
- **Validation** — profile with valid connection OK; profile referencing missing connection fails; Azure connection missing `api-version` rejected at connection create; kind-matching still enforced.
- **Secrecy assertions** — a test that captures log output and asserts no plaintext key appears for a resolved/used connection.

## 8. Sequencing (each built test-first)

1. `Crypto` port + `AESGCM` adapter + master-key bootstrap.
2. `ConnectionStore` port + Postgres adapter + sqlc queries + migration.
3. Widen `Profile` + resolution chain (`ResolvedBinding`); keep claude-cli no-key path working.
4. Validation extensions (connection existence, Azure extras).
5. Wiring + management surface; secrecy/log-redaction tests.

## 9. Acceptance Mapping

- **#7 Connection safety & delivery** (resolution + secrecy half) — base-url/key resolvable from a profile; key in neither workflow files nor logs; multi-region (two Azure connections) resolves each to the correct endpoint.
- Resolution foundation for **#2/#3** (native adapters + opencode env-injection, delivered in Phase 3).
