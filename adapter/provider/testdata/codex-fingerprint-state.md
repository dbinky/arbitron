# Codex fingerprint state (Area 4 / G4)

Inspected 2026-10-19 against the pinned `rust-v0.163.0` source; no subscribed
model invocation or account modification was performed.

- [Auth storage](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/login/src/auth/storage.rs): `AuthDotJson` persists the authentication mode, token bundle and refresh timestamp in `CODEX_HOME/auth.json`.
- [Token data](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/login/src/token_data.rs): `TokenData` persists access/refresh tokens, an ID-token JWT and a selected account identifier. `parse_chatgpt_jwt_claims` derives user, workspace, plan and FedRAMP routing identity from the JWT payload. Expiry and signatures can change without changing that identity.
- [Model cache](https://github.com/openai/codex/blob/rust-v0.163.0/codex-rs/models-manager/src/cache.rs): `FileModelsCache.refresh_ttl` rewrites the freshness timestamp after validating the existing scope/version/ETag. The model descriptions remain unchanged during this renewal.

`harness_codex_state.go` canonicalizes the JSON and retains stable user/workspace,
plan, issuer and routing claims; only identified OAuth credentials lose their
rotating token material and refresh timestamp. A missing stable user or workspace
keeps the full credential content in the fingerprint. Other authentication modes
and unknown outer/token fields stay content-sensitive. This parsing is solely
for proof invalidation; the CLI still authenticates and verifies execution.

The cache projection drops only its freshness timestamp. Model membership,
capabilities, scope, client version, ETag and unknown fields still invalidate
proof, as does every byte of `config.toml`. Malformed/partial JSON or an invalid
JWT envelope returns no fingerprint and exposes no decoder content.

`TestModelProbeCodexSelfRefreshAtRoot` uses the root's actual inventory, router,
factory, subprocess launcher and rollout reader for batch and interactive
probes. Its shell fixture rotates synthetic OAuth tokens and cache timestamps
during the turn. Renewal confirms; account/config/model-metadata edits, partial
auth and wrong rollouts remain unknown. Both renewal cases fail with raw hashing.
`TestHarnessFingerprintCodexIgnoresOnlyRoutineRefreshState` also checks another
user in the same workspace, plan changes and API-key authentication.

This closes only G4's file-backed Codex renewal slice. Claude's effective state
location and OAuth/counter projection, and G1's tool-disable contract, remain open.
