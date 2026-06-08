# Org-Readiness Phase 1 — Task 1: Edge mode domain + config + verifier port

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task, and superpowers:test-driven-development for every code change. Steps use checkbox (`- [ ]`) syntax for tracking.

**Objective:** Introduce the `edge` authentication mode as a pure-domain + config concern — the typed `Mode`, the load-time `EdgeConfig`, loader defaults / validation / env overlay, and the `EdgeVerifier` domain port the middleware will depend on — with no adapter or wiring yet.

**Spec anchor:** `docs/specs/org-readiness-spec.md` (Phase 1 — "New edge authentication mode", "Operator configuration is minimal and headless").
**Design anchor:** `docs/superpowers/specs/org-readiness-design-phase-1.md` (§Domain, §Configuration).

**Dependencies:** None. This is the first task of Phase 1. Tasks 2–4 build on the types and port defined here.

**Tech stack:** Go, standard library + koanf (already in use). Tests use the standard `testing` package, table-driven, matching `domain/identity` and `adapter/config` conventions.

---

## Files to create / modify

- Modify: `domain/identity/identity.go` — add `ModeEdge`; extend `ModeFromString`.
- Modify: `domain/config/config.go` — add `AuthModeEdge`; add `EdgeConfig`; add `Edge EdgeConfig` field to `AuthConfig`.
- Create: `domain/identity/edge.go` — the `EdgeVerifier` port (one small interface).
- Modify: `adapter/config/loader.go` — defaults for `auth.edge.teamdomain` / `auth.edge.audiences`; `validateAuth` `AuthModeEdge` case; env overlay for the audiences CSV; commented YAML example.
- Tests:
  - `domain/identity/identity_test.go` (add `ModeFromString` edge cases; create file if absent).
  - `adapter/config/loader_test.go` (add edge defaults / validation / env-overlay cases).

---

## TDD plan

### Step group A — `ModeEdge` + `ModeFromString` (domain/identity)

- [ ] **A1. Failing test.** In `domain/identity/identity_test.go`, add a table-driven `TestModeFromString` covering the **mode string round-trip** boundary:
  - `"edge"` → `ModeEdge` (new — currently falls through to `ModeLocal`).
  - `"org"` → `ModeOrg`; `"local"` → `ModeLocal`; `""` → `ModeLocal`; `"bogus"` → `ModeLocal` (unknown defaults to local; config validation rejects genuinely bad values upstream — see `validateAuth`).
  Run `go test ./domain/identity/ -run TestModeFromString -v` → FAIL (`undefined: ModeEdge` / wrong mapping).
- [ ] **A2. Implement.** In `identity.go`:
  - Add `ModeEdge Mode = "edge"` to the `Mode` const block.
  - Add `case "edge": return ModeEdge` to `ModeFromString` (above the `default`).
  - Update the `ModeFromString` doc comment to mention `config.AuthModeEdge`.
  Run the test → PASS.

### Step group B — `EdgeConfig` + `AuthModeEdge` (domain/config)

- [ ] **B1.** In `domain/config/config.go`:
  - Add `AuthModeEdge = "edge"` to the mode const block alongside `AuthModeLocal` / `AuthModeOrg`.
  - Add a new type:
    ```go
    // EdgeConfig configures edge-trusted SSO (auth.mode == "edge"). Both fields are
    // REQUIRED when Mode == "edge". No secret lives here: verification uses the
    // edge proxy's PUBLIC signing keys discovered from TeamDomain. See the
    // org-readiness Phase 1 design.
    type EdgeConfig struct {
        // TeamDomain is the Cloudflare Access team domain, e.g.
        // "https://acme.cloudflareaccess.com". It is BOTH the JWKS source
        // (/cdn-cgi/access/certs) and the expected token issuer (iss).
        TeamDomain string
        // Audiences is the set of application AUD tag(s) to accept. A token whose
        // aud is not in this set is rejected.
        Audiences []string
    }
    ```
  - Add `Edge EdgeConfig` to `AuthConfig` (next to `OIDC` / `Session`), with a doc line: "Edge holds the edge-trusted-SSO settings (auth mode `edge` only)."
  (No standalone test — this is a struct shape, exercised by the loader tests in C.)

### Step group C — loader defaults, validation, env overlay (adapter/config)

- [ ] **C1. Failing test — edge validation requires both fields.** In `adapter/config/loader_test.go` add `TestValidateAuth_EdgeMode` (mirror the existing org-mode validation tests). Build a `*domainconfig.Config` with `Auth.Mode = "edge"` and assert:
  - **happy:** `TeamDomain` set + `Audiences` non-empty → `validateAuth` returns nil.
  - **input validation:** missing teamdomain → error mentioning `auth.edge.teamdomain`; empty audiences → error mentioning `auth.edge.audiences`; both missing → error names both.
  Run `go test ./adapter/config/ -run TestValidateAuth_EdgeMode -v` → FAIL (no edge case in `validateAuth`).
- [ ] **C2. Implement `validateAuth` edge branch.** Add a `case domainconfig.AuthModeEdge:` to `validateAuth` between the org and default cases:
  ```go
  case domainconfig.AuthModeEdge:
      var missing []string
      if strings.TrimSpace(cfg.Auth.Edge.TeamDomain) == "" {
          missing = append(missing, "auth.edge.teamdomain")
      }
      if len(cfg.Auth.Edge.Audiences) == 0 {
          missing = append(missing, "auth.edge.audiences")
      }
      if len(missing) > 0 {
          return fmt.Errorf("edge mode requires edge config; missing: %s", strings.Join(missing, ", "))
      }
      return nil
  ```
  Update the `default:` error to list all three valid modes (`AuthModeLocal`, `AuthModeOrg`, `AuthModeEdge`). Run the test → PASS.
- [ ] **C3. Failing test — defaults + env overlay.** Add `TestLoad_EdgeDefaultsAndEnvOverlay` (mirror the existing `auth.bootstrapadmins` / `auth.oidc.scopes` overlay tests):
  - With no config and no env, `Load` succeeds and `cfg.Auth.Edge.TeamDomain == ""`, `len(cfg.Auth.Edge.Audiences) == 0` (defaults present, edge not selected so validation passes under local mode).
  - **Env-overlay parsing of the audiences list:** set `ARBITRON_AUTH_MODE=edge`, `ARBITRON_AUTH_EDGE_TEAMDOMAIN=https://acme.cloudflareaccess.com`, `ARBITRON_AUTH_EDGE_AUDIENCES=aud-one,aud-two` (use `t.Setenv`); assert `cfg.Auth.Mode == "edge"`, `cfg.Auth.Edge.TeamDomain == "https://acme.cloudflareaccess.com"`, `cfg.Auth.Edge.Audiences == []string{"aud-one","aud-two"}`.
  - **boundary:** a single-element `ARBITRON_AUTH_EDGE_AUDIENCES=solo` → `[]string{"solo"}`; trailing/whitespace `" a , b "` → `["a","b"]` (reuses `splitCSV`).
  Run → FAIL (env overlay not wired; audiences arrives as a comma STRING that fails to unmarshal into `[]string`).
- [ ] **C4. Implement loader defaults + env overlay.** In `adapter/config/loader.go`:
  - Add to `defaults()`:
    ```go
    "auth.edge.teamdomain": "",
    "auth.edge.audiences":  []string{},
    ```
  - Add an env overlay block next to the existing `ARBITRON_AUTH_OIDC_SCOPES` block (the env provider delivers a CSV string; overlay it as a real slice so it unmarshals into `[]string`):
    ```go
    if raw := os.Getenv("ARBITRON_AUTH_EDGE_AUDIENCES"); raw != "" {
        if err := k.Load(confmap.Provider(map[string]interface{}{
            "auth.edge.audiences": splitCSV(raw),
        }, "."), nil); err != nil {
            return nil, fmt.Errorf("loading auth.edge.audiences: %w", err)
        }
    }
    ```
  - Extend the commented `# auth:` block in `defaultYAMLContent()` with an `edge:` example documenting `teamdomain`, `audiences`, and the env overrides, mirroring the `oidc:` block style.
  Run the test → PASS.

### Step group D — `EdgeVerifier` port (domain/identity)

- [ ] **D1.** Create `domain/identity/edge.go` with the port (no test — it is an interface contract; its behavior is tested via the adapter in Task 2):
  ```go
  package identity

  import "context"

  // EdgeVerifier verifies a single edge-issued identity token (e.g. a Cloudflare
  // Access JWT) and returns the authenticated email. It is the domain port the
  // auth middleware depends on in edge mode so the verification adapter is
  // swappable and testable. Verification is STATELESS (per request). On ANY
  // failure — malformed, bad signature, wrong issuer/audience, expired/not-yet-
  // valid, or a missing/empty email claim — it returns ("", err); the caller maps
  // a non-nil error to a fail-closed 401. The returned email is the verified
  // identity; the raw token is never logged or returned.
  type EdgeVerifier interface {
      Verify(ctx context.Context, rawToken string) (email string, err error)
  }
  ```
- [ ] **D2.** Run `go build ./...` and `go vet ./domain/... ./adapter/config/...` → clean.

### Step group E — verify + commit

- [ ] **E1.** `go test ./domain/identity/ ./domain/config/ ./adapter/config/ -v` → all PASS.
- [ ] **E2.** `go vet ./...` clean. Do NOT commit (the orchestrator commits the phase).

---

## Acceptance criteria (this task)

- `ModeFromString("edge")` returns `ModeEdge`; all other strings unchanged.
- `EdgeConfig{TeamDomain, Audiences}` exists on `AuthConfig`; `AuthModeEdge == "edge"`.
- `validateAuth` accepts edge mode with both fields, rejects it (fail-fast at load) when either is missing, and the unknown-mode error lists all three modes — satisfying the spec's "service starts and runs in edge mode with configuration supplied entirely through environment/secrets".
- `ARBITRON_AUTH_EDGE_AUDIENCES` parses as a real list; `ARBITRON_AUTH_EDGE_TEAMDOMAIN` and `ARBITRON_AUTH_MODE=edge` round-trip into the config.
- The `EdgeVerifier` port compiles and is documented as fail-closed and stateless.

## Notes — security & reuse

- **No secret in config.** Only the public team domain + AUD tag are configured; mirrors the design's "No secret in config".
- **Fail-fast at load** is the established pattern (`validateAuth` for org, `validateTLS`): an edge deployment missing its team domain cannot verify a single token, so reject at boot rather than 401 on every request.
- **Mirror existing patterns:** the env-overlay-as-slice trick is copied verbatim from `auth.oidc.scopes` / `auth.bootstrapadmins`; the validation case mirrors the org case; the port mirrors the small-interface style of `identity.OIDCProvider`.
- **Do not** add a `domain/config` import to `domain/identity` (keep `ModeFromString` taking a plain string).

## Test scenario coverage

| Category | Scenario | Step |
|---|---|---|
| Happy | `"edge"` → `ModeEdge` | A1 |
| Happy | edge mode with both fields validates | C1 |
| Happy | env overlay selects edge + parses fields | C3 |
| Input validation | missing teamdomain / audiences → load error | C1 |
| Input validation | unknown mode error lists all three modes | C2 |
| Boundary | unknown/empty mode string → `ModeLocal` | A1 |
| Boundary | single-element / whitespace audiences CSV | C3 |
| Integration/state | full `Load` round-trip from env to struct | C3 |

---

## Cross-phase coordination (plan alignment)

These files are edited by multiple phases; **serialize edits — do not run the
listed tasks concurrently on the same file** (merge-conflict / build-break risk):

- **`cmd/arbitron/main.go`** (composition root): P1-task4 (auth/edge wiring),
  P2-task1/2/3 (default backend + adapter wiring), P3-task3/4 (evaluator
  start/stop + management wiring). Apply in phase order; keep each phase's wiring
  in its own block.
- **`domain/instance/ports.go` + the SQLite instance/user repo + in-test repo
  fakes**: P1-task3 (`UserRepository.GetByEmail`) and P3-task3
  (`HasActiveRun`). Widen the interface once per task and update ALL fakes in the
  same task to keep the build green.
- **`domain/config/config.go` + `adapter/config/loader.go`**: P1-task1/2/4
  (`auth.edge.*`) and P2-task1 (`llm.defaultagentbackend`). Additive fields only;
  no shared keys, but coordinate the struct edits.

Migration numbering: P3 owns **`013_schedules`** (012 is the latest existing).
P1 adds only a sqlc query (`GetByEmail`) against the existing `users.email`
column — **no new migration**. If P1 turns out to need a column, it takes 014.

Recommended implementation order: **Phase 1 → Phase 2 → Phase 3** (the chosen
phase order). Within a phase, tasks are sequential unless noted.
