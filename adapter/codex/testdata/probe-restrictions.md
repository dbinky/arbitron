# Codex restricted probes (D10)

Verified against installed `codex-cli 0.163.0`, without installing/upgrading a
harness or invoking a model. D10 replaces the historical blanket tool-free
requirement: built-in tools may remain registered, but the probe is read-only,
network-disabled, without inherited MCP servers/plugins, and identity-only.
Exact-model evidence still comes exclusively from the account cache and the
correlated rollout's new `turn_context.model` records.

## Primary-source / installed-help hashes

Sources under `https://github.com/openai/codex/tree/rust-v0.163.0/`:

| File | SHA-256 |
| --- | --- |
| `codex-rs/core/config.schema.json` | `e9dd16b9d1710e57b020e42da4af6d6606e8f44cb028a2e94196dee3890d9170` |
| `codex-rs/core/src/tools/spec_plan.rs` | `198432ab11c33b6509d72388ab04b7cb21de8ea500c7c0078b2921de14f7019e` |
| `codex-rs/tools/src/tool_config.rs` | `1925968949c32190288dbc3a1473520ced9c66da425996a0652dd572b1341aae` |
| `codex-rs/config/src/merge.rs` | `d119f72f5bb56b295cab98a94e62fdfdb937463f32b7765049a6e62a7cf61b29` |
| `codex-rs/config/src/loader/mod.rs` | `f1d9c11bc0408da165edd405a148e7307fbf2e15569798597d218fda04f68ff0` |
| `codex-rs/config/src/overrides.rs` | `dbac7a6f68de31e74c11e9671fe0b1f68f8783c68cff5a9ba26348e0b5f86c0a` |
| `codex-rs/core/src/config/mod.rs` | `cbd8dd6d50cb7d61183d309cbefa95cd368de87bf44d2ac60e83c3a2aff59658` |
| `codex-rs/exec/src/lib.rs` | `8228767ba1fa4872ca4c18bcb4eeefadf9cde91d70b79d0b3179cb3cadeace44` |
| `codex-rs/cli/src/mcp_cmd.rs` | `f751ab7501f9af69d0cfd81e313d5f1d8eb344c004ed26d21de346dbf06b092f` |

Installed `codex exec --help`:
`df0d864ca2076f7654288c6a1e3394e3eaa2ea8f15021df2524efdd90c70986c`.
Installed `codex exec resume --help`:
`186d8922e15809b2072de0fcd943d47ce1745e9ab112b651f3e4e722a17fa969`.

## Verified recipe (recorded before implementation)

Both initial and resumed probes retain the real binary, account environment,
model, effort and rollout path. No unrelated connection API key is injected.

1. Require Codex >=0.163.0 for this recipe; ordinary execution's minimum stays
   unchanged. Use `--strict-config` so unrecognized restriction keys fail closed.
2. All restriction/config reads and turns share the probe's <=30s deadline and
   <=1MiB output ceiling, even when ordinary execution is unlimited.
3. Enumerate configured MCP servers using the same binary/environment/cwd:
   `codex -c features.plugins=false -c features.apps=false
   -c 'web_search="disabled"' -c sandbox_workspace_write.network_access=false
   mcp list --json`. No `--strict-config` here: installed 0.163.0 exits 1 with
   "`--strict-config` is not supported for `codex mcp`" (re-checked 2026-10-20
   with an isolated empty `CODEX_HOME`); the model turn keeps it. This is config enumeration, NOT model membership or model
   proof. `run_list` enumerates `McpManager.configured_servers`; it does not
   initialize the configured stdio commands. Its auth-status discovery may use
   local HTTP. Failed/malformed/truncated enumeration prevents the model turn.
4. Build a TOML inline-table override naming EVERY enumerated server, e.g.
   `-c 'mcp_servers={"hostile"={enabled=false}}'`. Names are quoted TOML strings,
   including dotted names. Retain all fixed restriction overrides on the turn.
   `features.plugins=false` disables plugin capability loading (Config's
   `plugins_config_input`); `features.apps=false` disables app-provided MCP.
5. Fresh: `--sandbox read-only` (batch) / `-s read-only` (interactive), never
   bypass. Resume rejects `-s` and `--cd`: reassert
   `-c 'sandbox_mode="read-only"'`, process cwd, every fixed restriction and the
   freshly enumerated MCP overrides. Read-only has no network permission;
   additionally pin `sandbox_workspace_write.network_access=false` and disable
   the independent web-search surface. No writable roots are granted.
6. Prompt stdin is exactly: `Reply with only your model identity on one line.
   Do not use tools or take any other action.` Resume retains its `-` placeholder.
   Verify the saved session before enumerating/launching a resumed turn, then
   require new exact-model rollout context and successful completion.

**Deep-merge control:** `mcp_servers={}` does NOT clear inherited tables. An
isolated account with `[mcp_servers.hostile] command="never-execute"` remained
`enabled:true` under `CODEX_HOME=<isolated> codex -c 'mcp_servers={}' mcp list
--json`. The same non-model command with
`-c 'mcp_servers={"hostile"={enabled=false}}'` returned `enabled:false`.
No server command or paid model was invoked. A fake accepting arbitrary `-c`
strings cannot establish these semantics. `--ignore-user-config` alone is not
used: it does not remove every installation/project layer; enumeration plus
per-server overrides preserve real account/provider configuration.

This recipe is a restriction contract, not a claim that sandboxing removes
built-in tool registrations. Refresh/restart and delivery acceptance consume the
same cache/rollout evidence downstream in P3T6/P3T7/P4T3.
