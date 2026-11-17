# LR-07 — independent Codex app-server configuration collection

Accessed 2026-11-12. Source: OpenAI Codex `rust-v0.167.1`, commit
`0a94398fa60c4fac0e7659e4213a0a36e613dd46`. Installed inference-free schema
check: `codex-cli 0.167.1`. This is not an exec mission, authentication,
helper-routing, isolation-enforcement or completed-agent observation.

## Versioned producers

URLs below are relative to
<https://github.com/openai/codex/blob/0a94398fa60c4fac0e7659e4213a0a36e613dd46/>.
SHA-256 values cover the complete downloaded source files.

| Source | Producer / relevant lines | SHA-256 |
|---|---|---|
| `codex-rs/app-server-protocol/src/protocol/v1.rs` | initialize client identity and response `userAgent` / `codexHome`, 29–84 | `6e767472256756ec8262097f32a2b88330a469de4a4879255835c9f99f178139` |
| `codex-rs/app-server-protocol/src/protocol/v2/config.rs` | snake-case effective Config (281 onward), camel-case config/read params and response (390–409) | `a50d4abcd7ea13486e622845f7987720658cb80ac3c15b3c374207659989eb65` |
| `codex-rs/app-server/src/config_manager_service.rs` | read with explicit cwd, effective layer conversion and origins, 132–207 | `993ae697e3d6153250e962551d74ab8d40538e162fe4c4d5b30652c4c9227b17` |
| `codex-rs/config/src/loader/mod.rs` | CODEX_HOME user config and disabled untrusted project layers, 126–139; session override layer, 256–270 | `a5a6a601eb2f7c1352106caa1904a7ff336937b73643e5831d52bfd21044d4b2` |
| `codex-rs/model-provider-info/src/lib.rs` | env_key versus literal/auth alternatives, 168–177; requires_openai_auth, 220–224; env lookup, 519 onward | `7a9eb4b69e85fd79c590245fca24b013c24652db3a81c41a2266293495f5ff85` |

`config/read` returns resolved config, not request-authentication proof. Its
redaction is limited (for example OTEL exporter headers); raw response bytes
must remain bounded private memory and never enter observations or diagnostics.
No `thread/start`, `turn/start` or inference is requested. App-server stdio EOF
can terminate before queued requests are handled, so collection keeps stdin
open until initialize has returned, sends initialized then config/read, closes
stdin after its response, drains bounded stdout and requires successful exit.

## Experiment and exact scope

`TestLiveCodexConfigControls` constructs the real collector from the explicit
`ARBITRON_LUNAROUTE_E2E_CODEX_MODEL` and installed executable. It runs both
conflicting and absent private HOME/project input cases in distinct temporary
directories. No provider credential, proxy or runtime-injection environment is
inherited. CODEX_HOME is separately private and contains no auth.json or
config.toml before or after either query. The selected model, review model and
five provider fields are passed as session overrides. The response must report
those exact values, `requires_openai_auth:false`, no alternate credential/header
configuration, and sessionFlags origins for every override. Initialize must
agree with the same executable's version query and canonical private home.

Byte/mode snapshots protect the seeded user/project trees, including absent
files. Both cases must agree and all private query directories must be removed
before exclusive 0600 JSON publication. Raw config, origins, home paths,
notification identities and stdout/stderr are not serialized. Failed/missing,
malformed, duplicate, oversized, drifting or cancelled queries publish nothing.

This establishes **app-server resolved configuration only**. It does not prove
exec-mode precedence, that auth files were never read, real Bearer auth, review
or compaction requests, exact effort support, enforced model limits, complete
isolation, tool completion or LR-07 verification. Those still require the
same-mission/helper producers and genuine operator observations in P4-T1. No
runtime version gate or production adapter control is added.

Run:

```sh
ARBITRON_LUNAROUTE_E2E_CODEX_MODEL=glm-5.3 go test ./test/lunaroute -run '^TestLiveCodexConfigControls$' -count=1 -v
```

Only a successful explicit run writes
`observations/lr07-codex-config-controls.json`. Missing model/binary skips
locally with readiness pending and fails release-mode prerequisites. The
inference-free installed schema test does not publish an artifact or change
register/capability facts. Re-run collection after binary changes; do not treat
this narrow artifact as the complete contract/run observation.
