# Phase 2, Task 1: `Crypto` Port + `AESGCM` Adapter + Master-Key Bootstrap

## Goal

Introduce the `Crypto` port in `domain/provider` (`Seal`/`Open`) and an `adapter/crypto` AES-256-GCM implementation that seals connection API keys at rest. Add the master-key bootstrap that sources a 32-byte key once at boot from `ARBITRON_MASTER_KEY` (base64) or the OS keyring, holds it in memory only, and fails fast when the key is absent while connections exist. This is the encryption foundation that Task 2's `ConnectionStore` consumes; no key is ever persisted in plaintext.

This task realizes the at-rest half of security acceptance **#7** (AES-GCM at rest, master key in memory only).

**Dependency ordering:** This is the first Phase 2 task and has no Phase 2 predecessors. It builds on the **Phase 1 `domain/provider`** package (introduced in Phase 1 Task 3 — the slim `Profile`/`ProfileResolver`). If that package does not yet exist when this task runs, create it; do **not** redefine `Profile`/`ProfileResolver` here — only add the `Crypto` port alongside them. Sources of truth: design Phase 2 §2, §6, §7; design Phase 1 §4.

## Files to Create/Modify

### Modify (or Create if Phase 1 not yet landed): `domain/provider/crypto.go`

- Define the `Crypto` driven port. Domain-only; no adapter imports.
  ```go
  // Crypto seals and opens secrets for storage at rest. The plaintext never
  // leaves the caller except through Open at the single decryption entry point.
  type Crypto interface {
      // Seal encrypts plaintext, returning ciphertext and the per-seal nonce.
      Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
      // Open decrypts ciphertext using the supplied nonce. A tampered
      // ciphertext or wrong key returns an error and no plaintext.
      Open(ciphertext, nonce []byte) (plaintext []byte, err error)
  }
  ```
- Define sentinel errors the adapter and callers match on:
  ```go
  // ErrMasterKeyMissing is returned by the bootstrap when no master key is
  // available (env unset and keyring empty) while it is required.
  var ErrMasterKeyMissing = errors.New("master key not available")
  // ErrMasterKeyInvalid is returned when the supplied key is not 32 bytes.
  var ErrMasterKeyInvalid = errors.New("master key must be 32 bytes (base64-encoded)")
  // ErrDecrypt is returned when Open fails authentication (tamper or wrong key).
  var ErrDecrypt = errors.New("decryption failed")
  ```

### Create: `adapter/crypto/aesgcm.go`

- Implement `AESGCM` satisfying `provider.Crypto` using `crypto/aes` + `crypto/cipher` (AES-256-GCM AEAD) and `crypto/rand`.
- Compile-time assertion: `var _ provider.Crypto = (*AESGCM)(nil)`.
- Constructor validates the key length and builds the GCM cipher once:
  ```go
  // NewAESGCM builds an AES-256-GCM Crypto from a 32-byte master key. The key
  // is held only in this in-memory struct; it is never logged or persisted.
  func NewAESGCM(masterKey []byte) (*AESGCM, error)
  ```
  Return `provider.ErrMasterKeyInvalid` (wrapped) when `len(masterKey) != 32`.
- `Seal`:
  ```go
  func (a *AESGCM) Seal(plaintext []byte) (ciphertext, nonce []byte, err error)
  ```
  Generate a fresh random 96-bit (12-byte) nonce per call via `crypto/rand`; `gcm.Seal(nil, nonce, plaintext, nil)`. Return ciphertext and nonce separately (they map to the `api_key_cipher` / `api_key_nonce` columns in Task 2). Never reuse a nonce.
- `Open`:
  ```go
  func (a *AESGCM) Open(ciphertext, nonce []byte) (plaintext []byte, err error)
  ```
  `gcm.Open(nil, nonce, ciphertext, nil)`; on any failure return `nil, fmt.Errorf("...: %w", provider.ErrDecrypt)` — never surface the raw GCM error to a logger and never return partial plaintext.
- Do **not** implement `String()`/`MarshalJSON` that could leak the key; the struct's key field stays unexported.

### Create: `adapter/crypto/masterkey.go`

- Master-key bootstrap, sourced once at boot, in memory only.
  ```go
  // LoadMasterKey resolves the 32-byte master key from, in order:
  //   1. ARBITRON_MASTER_KEY (base64-encoded 32 bytes), then
  //   2. the OS keyring (service "arbitron", key "master-key").
  // It returns ErrMasterKeyMissing when neither source yields a key, and
  // ErrMasterKeyInvalid when the decoded key is not 32 bytes.
  func LoadMasterKey() ([]byte, error)
  ```
  - Read `ARBITRON_MASTER_KEY`; if set, `base64.StdEncoding.DecodeString` it and length-check to 32 bytes.
  - If env is unset, attempt the OS keyring lookup. Keep the keyring access behind a small internal seam (an unexported `keyringGetter` func var) so tests can stub it without a real keyring; the production default reads the OS keyring.
  - SECURITY: never log the key bytes or the base64 string. Log only that a key was/was not found and from which source (env vs keyring), never the value.
- Bootstrap gate that ties the key to whether any connection exists:
  ```go
  // RequireMasterKeyIfConnections fails fast at startup when connections exist
  // but no master key is available, because their ciphertext could never be
  // decrypted. It returns ErrMasterKeyMissing with a clear, actionable message.
  func RequireMasterKeyIfConnections(haveKey bool, connectionCount int) error
  ```
  Returns an error when `connectionCount > 0 && !haveKey`; returns nil otherwise (a Claude-only deployment with zero connections boots with no master key — see Task 3's no-key path). The actual `connectionCount` is supplied by the Task 2 store at wiring time (Task 5); this helper keeps the policy in the adapter and is unit-testable in isolation.

## Testing (strict TDD — all five scenario categories)

Write tests first. Cover, per design §7's `AESGCM crypto` bullet plus the master-key bootstrap:

- **Happy path** — `Seal` then `Open` round-trips: `Open(Seal(plaintext)) == plaintext` for a representative API-key string (e.g. `"sk-abc123..."`).
- **Success** — `NewAESGCM` with a valid 32-byte key succeeds; `LoadMasterKey` with a valid base64 32-byte `ARBITRON_MASTER_KEY` returns the decoded 32 bytes; `RequireMasterKeyIfConnections(false, 0)` returns nil (Claude-only boot).
- **Failure** — tampered ciphertext fails `Open`: flip a byte of the ciphertext (and separately a byte of the nonce) and assert `Open` returns `ErrDecrypt` and `nil` plaintext (no partial output). Wrong-key fails: seal with key A, build an `AESGCM` with key B, assert `Open` returns `ErrDecrypt`.
- **Error** — `NewAESGCM` with a 16-byte (or 0-byte) key returns `ErrMasterKeyInvalid`; `LoadMasterKey` with env unset and a stubbed empty keyring returns `ErrMasterKeyMissing`; `LoadMasterKey` with a non-base64 or wrong-length `ARBITRON_MASTER_KEY` returns `ErrMasterKeyInvalid`; `RequireMasterKeyIfConnections(false, 3)` returns `ErrMasterKeyMissing`.
- **Edge case** — empty plaintext (`[]byte{}`) seals and opens back to empty; nonce uniqueness across many seals: seal the same plaintext N times (e.g. 1000) and assert all nonces are distinct and all ciphertexts differ (probabilistic GCM nonce uniqueness). Maximum-length-ish key string seals/opens cleanly.

### Secrecy assertion (explicit)

- A log-capture test around `LoadMasterKey` and `NewAESGCM` (and the keyring/env paths): inject a `*slog.Logger` writing to a buffer, run the bootstrap with a known key value, and assert the captured output contains **no** substring of the key bytes or its base64 form. (The full resolved-connection redaction test lives in Task 5; this task asserts the bootstrap itself never logs the key.)

## Acceptance Criteria

- `provider.Crypto` port exists in `domain/provider` with `Seal`/`Open`; domain imports no adapter.
- `adapter/crypto.AESGCM` implements `provider.Crypto` with AES-256-GCM, a fresh random 96-bit nonce per `Seal`, and authenticated `Open` returning `ErrDecrypt` on tamper/wrong-key.
- Master key is sourced once from `ARBITRON_MASTER_KEY` (base64 32 bytes) or the OS keyring, held in memory only, never logged or persisted.
- Fail-fast policy helper returns `ErrMasterKeyMissing` when connections exist but no key is available, and nil for a zero-connection (Claude-only) boot.
- All five TDD scenario categories and the secrecy log-capture assertion pass; existing tests still pass.
