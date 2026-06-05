# LLM Providers — Phase 1, Task 2: Provider-aware loopback allowance for local Ollama

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow a connection whose provider is `ollama` AND whose BaseURL host is a loopback address (`localhost`, `127.0.0.1`, `::1`) to use `http` + that loopback host at the connection write path, while leaving the SSRF policy unchanged for every other provider and for `ollama` pointed at any non-loopback internal host (link-local, `169.254.169.254` metadata, RFC1918).

**Architecture:** The authoritative write-path SSRF guard is `provider.ValidateConnectionURL(c Connection, policy URLPolicy)` in `domain/provider/url_policy.go`, called by `adapter/sqlite/connection_repo.go` on Create/Update. The `URLPolicy.ValidateURL(rawURL string)` interface is intentionally provider-blind (it only sees the URL), but `ValidateConnectionURL` already receives the full `Connection` (including `Provider`). So the provider-aware carve-out belongs in `ValidateConnectionURL`: a narrow `ollamaLoopbackAllowed(c)` pre-check that, when satisfied, accepts the URL and skips the strict policy; otherwise the existing `policy.ValidateURL` path runs unchanged. The `strictURLPolicy` and the `URLPolicy` interface are NOT modified, so every existing url_policy test stays green.

**Tech Stack:** Go, `net/url` + `net` (already imported in `url_policy.go`), `testing` + `testify` (`assert`). Tests are pure-function calls — no network, no httptest.

---

## Dependencies

- **None.** Phase 1 depends on nothing; this task is independent of Task 1 (Factory routing). Both can land in either order.

## Spec / design references

- Spec `docs/specs/llm-providers-spec.md` — Provider Catalog ("Ollama — local … operator's base URL e.g. `http://localhost:11434`"); "No secret leakage" / SSRF intent.
- Design `docs/superpowers/specs/llm-providers-design-phase-1.md` §2 "Local-endpoint (localhost) allowance for Ollama": the carve-out is scoped to provider==`ollama` AND loopback hosts ONLY — it does NOT permit `ollama` at link-local/metadata/RFC1918, and does NOT widen the policy for any other provider.

## Design decision: where the carve-out lives

The carve-out must be **provider-aware**, but `URLPolicy.ValidateURL` only takes a `rawURL string` and is shared by `strictURLPolicy`/`allowAnyURLPolicy` plus their existing tests. Adding a parameter would churn that interface and its callers in `adapter/openrouter` semantics. Instead, put the carve-out in `ValidateConnectionURL`, which already has `c.Provider` and `c.BaseURL`. This keeps a single write-path entry point (mirroring how it already wraps errors with the connection name) and leaves `strictURLPolicy` a pure URL guard.

## Files

- **Modify:** `domain/provider/url_policy.go`
  - Add a canonical provider constant (the package currently has none; keep it local and unexported to avoid leaking a policy detail):
    ```go
    // providerOllama is the connection provider string that earns the narrow
    // loopback carve-out below. Kept here (not imported from adapter/provider) so
    // the domain policy has no adapter dependency.
    const providerOllama = "ollama"
    ```
  - Add a private helper that decides the carve-out. It normalizes the provider (trim + lowercase) so it matches the Factory's normalization, parses the URL, and accepts ONLY `http`/`https` on a loopback host for the `ollama` provider:
    ```go
    // ollamaLoopbackAllowed reports whether c earns the narrow local-Ollama
    // carve-out: provider == "ollama" (case/whitespace-insensitive) AND the
    // BaseURL host is a loopback literal (localhost, 127.0.0.0/8, or ::1), over
    // http or https. It is deliberately NARROW: it does NOT accept the
    // 169.254.169.254 metadata endpoint, link-local, unspecified, or RFC1918
    // hosts, and it applies to NO other provider. Anything it does not accept
    // falls through to the unchanged StrictURLPolicy.
    func ollamaLoopbackAllowed(c Connection) bool {
        if strings.ToLower(strings.TrimSpace(c.Provider)) != providerOllama {
            return false
        }
        u, err := url.Parse(c.BaseURL)
        if err != nil {
            return false
        }
        if u.Scheme != "http" && u.Scheme != "https" {
            return false
        }
        host := strings.ToLower(strings.Trim(u.Hostname(), "[]"))
        if host == "localhost" {
            return true
        }
        ip := net.ParseIP(host)
        if ip == nil {
            return false
        }
        // Loopback ONLY. Explicitly exclude the metadata endpoint and any
        // non-loopback internal class even though IsLoopback already excludes
        // them — the explicit guard documents the security boundary.
        if ip.Equal(net.IPv4(169, 254, 169, 254)) {
            return false
        }
        return ip.IsLoopback()
    }
    ```
  - In `ValidateConnectionURL` (currently lines 110-118), add the carve-out BEFORE applying the policy, so an allowed local-Ollama URL short-circuits to `nil`:
    ```go
    func ValidateConnectionURL(c Connection, policy URLPolicy) error {
        if policy == nil {
            policy = StrictURLPolicy
        }
        // Narrow, provider-aware carve-out: local Ollama (loopback host) may use
        // http + loopback, which StrictURLPolicy would otherwise reject. Scoped to
        // provider=="ollama" AND loopback ONLY; everything else is unchanged.
        if ollamaLoopbackAllowed(c) {
            return nil
        }
        if err := policy.ValidateURL(c.BaseURL); err != nil {
            return fmt.Errorf("connection %q: %w", c.Name, err)
        }
        return nil
    }
    ```
    (`strings`, `net`, `net/url`, `fmt` are already imported in `url_policy.go`.)
- **Test:** `domain/provider/url_policy_test.go` (append; package `provider_test`, already imports `provider`, `assert`, `testing`).

## TDD test list (five categories)

**Seam:** none — `ValidateConnectionURL` is a pure function over a `provider.Connection` value. No httptest/network. Construct `provider.Connection{Name, Provider, BaseURL}` literals and assert the returned error (or nil) with `assert`. Use `provider.StrictURLPolicy` (the production policy) as the passed policy so the tests prove the carve-out under the real guard, not a permissive stub.

### Happy-path

- [ ] `TestValidateConnectionURL_OllamaLocalhostHTTP_Allowed`
  - `c := provider.Connection{Name: "ollama-local", Provider: "ollama", BaseURL: "http://localhost:11434/v1"}`
  - `assert.NoError(provider.ValidateConnectionURL(c, provider.StrictURLPolicy))`.
- [ ] `TestValidateConnectionURL_Ollama127001HTTP_Allowed`
  - Same with `BaseURL: "http://127.0.0.1:11434/v1"` → `assert.NoError(...)`.
- [ ] `TestValidateConnectionURL_OllamaIPv6LoopbackHTTP_Allowed`
  - `BaseURL: "http://[::1]:11434/v1"` → `assert.NoError(...)`.
- [ ] `TestValidateConnectionURL_OllamaCloudHTTPS_Allowed`
  - Keyed cloud Ollama is a normal public https host and must pass the unchanged strict path: `Provider: "ollama", BaseURL: "https://ollama.com/v1"` → `assert.NoError(...)`.

### Input-validation

- [ ] `TestValidateConnectionURL_OllamaProviderCaseAndWhitespace_Allowed`
  - `Provider: "  Ollama  "`, `BaseURL: "http://localhost:11434"` → `assert.NoError(...)` (carve-out normalizes the provider string the same way the Factory does).
- [ ] `TestValidateConnectionURL_OllamaEmptyBaseURL_Rejected`
  - `Provider: "ollama", BaseURL: ""` → `assert.ErrorIs(err, provider.ErrInvalidBaseURL)` (empty URL is not a loopback literal, so it falls through to strict, which rejects empty).
- [ ] `TestValidateConnectionURL_OllamaUnparseableBaseURL_Rejected`
  - `Provider: "ollama", BaseURL: "http://%zz"` → `assert.ErrorIs(err, provider.ErrInvalidBaseURL)` (parse failure in the carve-out returns false; strict then rejects it).

### Error-handling

- [ ] `TestValidateConnectionURL_OllamaPublicHTTP_Rejected`
  - `Provider: "ollama", BaseURL: "http://api.example.com/v1"` (public host, plaintext http) → `assert.ErrorIs(err, provider.ErrInvalidBaseURL)`. The carve-out is loopback-only, so a non-loopback ollama host still hits strict's https-only rule. Also `assert.Contains(err.Error(), "ollama-pub")` for the connection name when `Name: "ollama-pub"`.
- [ ] `TestValidateConnectionURL_NilPolicyWithOllamaLoopback_Allowed`
  - `provider.ValidateConnectionURL(provider.Connection{Provider:"ollama", BaseURL:"http://localhost:11434"}, nil)` → `assert.NoError(...)` (nil policy defaults to strict, but the carve-out still applies and short-circuits).

### Edge-cases

- [ ] `TestValidateConnectionURL_OllamaLoopback8HTTP_Allowed`
  - `BaseURL: "http://127.0.0.8:11434/v1"` (any 127.0.0.0/8 address is loopback per `net.IP.IsLoopback`) → `assert.NoError(...)`. (Contrast: the existing `TestStrictURLPolicy_InternalHosts_Rejected` rejects `https://127.0.0.8/v1` for a non-ollama URL — that test is unchanged and stays green.)
- [ ] `TestValidateConnectionURL_OllamaUnspecified_Rejected`
  - `BaseURL: "http://0.0.0.0:11434"` → `assert.ErrorIs(err, provider.ErrInvalidBaseURL)`. `0.0.0.0` is unspecified, not loopback, so the carve-out declines and strict rejects it.

### Security (the load-bearing scoping checks)

- [ ] `TestValidateConnectionURL_NonOllamaLocalhost_StillRejected`
  - For each `prov := range {"openrouter", "openai", "litellm", "azure-openai", ""}`:
    `c := provider.Connection{Name: "x", Provider: prov, BaseURL: "http://localhost:11434/v1"}` → `assert.ErrorIs(provider.ValidateConnectionURL(c, provider.StrictURLPolicy), provider.ErrInvalidBaseURL)`. Proves the loopback allowance is `ollama`-only and does NOT widen the policy for any other provider (including the new `openai` from Task 1 and the empty/claude path).
- [ ] `TestValidateConnectionURL_OllamaMetadataEndpoint_StillRejected`
  - `Provider: "ollama", BaseURL: "http://169.254.169.254/latest/meta-data/"` → `assert.ErrorIs(err, provider.ErrInvalidBaseURL)`. The canonical SSRF credential-exfiltration target is explicitly NOT allowed even for ollama.
- [ ] `TestValidateConnectionURL_OllamaLinkLocal_StillRejected`
  - `Provider: "ollama", BaseURL: "http://169.254.0.10/v1"` → `assert.ErrorIs(err, provider.ErrInvalidBaseURL)` (link-local is not loopback; carve-out declines).
- [ ] `TestValidateConnectionURL_OllamaRFC1918Hosts_StillRejected`
  - For each `host := range {"10.0.0.5", "192.168.1.10", "172.16.0.1"}`:
    `Provider: "ollama", BaseURL: "http://"+host+"/v1"` → `assert.ErrorIs(err, provider.ErrInvalidBaseURL)`. Private-range hosts are not loopback; ollama earns no allowance for them.

## Acceptance criteria (what `go test ./...` proves)

- `go test ./domain/provider/...` passes with the new tests, proving:
  - Local Ollama over `http://localhost`, `http://127.0.0.0/8`, and `http://[::1]` is accepted at the write path.
  - Cloud Ollama over `https://ollama.com` is accepted via the unchanged strict path.
  - The carve-out is `ollama`-only: `openrouter`/`openai`/`litellm`/`azure-openai`/empty pointed at `localhost` are STILL rejected.
  - The carve-out is loopback-only: ollama pointed at the metadata endpoint, link-local, unspecified, or RFC1918 hosts is STILL rejected.
  - All pre-existing `TestStrictURLPolicy_*` and `TestValidateConnectionURL_*` tests in `url_policy_test.go` remain green (the `URLPolicy` interface and `strictURLPolicy` are untouched).
- `go test ./...` (`make test`) stays green: `adapter/sqlite/connection_repo.go` calls `ValidateConnectionURL` unchanged, so a `connection_create` for a local-Ollama connection now succeeds end-to-end without weakening any other provider's guard.

## "No secret in logs/errors" security checks

- This task adds no key handling: `ValidateConnectionURL` runs BEFORE the key is sealed in `ConnectionRepo.Create`/`Update` (see `adapter/sqlite/connection_repo.go:63-101`), and it only inspects `c.Provider`/`c.BaseURL`/`c.Name` (no key material is present on `Connection`). The carve-out therefore introduces no new leak surface.
- Rejection errors continue to wrap `ErrInvalidBaseURL` and name only the connection (`c.Name`) and host — never a secret — preserving the existing "errors never leak secrets" guarantee.
- Security-class boundary is asserted by tests, not just by code: the `NonOllamaLocalhost_StillRejected`, `MetadataEndpoint_StillRejected`, `LinkLocal_StillRejected`, and `RFC1918Hosts_StillRejected` cases lock the carve-out to its intended narrow scope so a future edit that widens it fails the suite.
