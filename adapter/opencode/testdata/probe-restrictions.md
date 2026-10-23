# OpenCode probe restrictions (D12)

Decided by the product owner on 2026-10-21; see D12 in
`docs/designs/2026-09-29-latest-model-profiles-decisions.md`. Verified on 2026-10-21
against the installed `opencode 1.19.3` (npm `opencode-ai`). This needed no paid
model call, no harness installation or update, and no account change.

| Artifact | SHA-256 |
| --- | --- |
| `/opt/homebrew/lib/node_modules/opencode-ai/bin/opencode.exe` | `6d3e6458fce4b649edf974c19959d5dbfe1e7f255a503dcfc01215b24ad3ea5b` |
| `opencode run --help` output | `177277faec689a9ed77962dd130d68c045f279ee104ddebd9ab59d1ec22217ec` |
| `opencode debug --help` output | `31f44ac3c7677fbc3a558e8507b4af554b49d2285bc2626e2d8419145c22d071` |
| `opencode debug config --help` output | `2078cbf5afc8942d0308ff03823a6e6d1975182e60826abefcaa6864efa8e72c` |

The pinned v1.19.3 sources recorded in the P3T4 source triage still apply:
`config/config.ts` `87a9071a…` (deep merge, plugin arrays concatenated),
`config/paths.ts` `cd86a344…` (`$HOME/.opencode` always a config directory),
`mcp/index.ts` `82c45930…` (servers start unless `enabled:false`), and
`plugin/index.ts` `47c62b7c…` (`--pure` skips external plugins).

## Recipe (`adapter/opencode/model_probe.go:ProbeExecutedModel`)

1. Build the probe's own inline config: a deny-all `permission` block plus the
   path's provider routing, with credentials kept in env.
2. Run `opencode debug config --pure` with the probe's cwd, its credential env and
   `OPENCODE_CONFIG_CONTENT=<that config>`. Collect every key under `.mcp`. This
   is OpenCode's own resolution across global, `$HOME/.opencode`, account,
   managed and project layers. Output is capped at 1 MiB with a 30 s deadline.
3. Add `"mcp": {"<each server>": {"enabled": false}}` to the inline config.
   OpenCode deep-merges it over each inherited definition.
4. Re-run `debug config --pure` under the new inline config. Every resolved
   server must report `"enabled": false`. Otherwise
   (`ErrProbeIsolationUnverified`) no run starts.
5. `opencode run --pure <identity prompt> -m <provider>/<model>
   --dangerously-skip-permissions --format json` with that inline config, in the
   private probe cwd. Then `opencode export <session> --pure` must name the
   requested provider/model on every assistant step.

## Reproduction (`testdata/probe-isolation.sh`)

The script runs the installed binary in a throwaway HOME with hostile inherited
config: an MCP server in the global XDG layer, another in `$HOME/.opencode`, and
a plugin in the global plugin directory. Each would touch a marker file if it
started. The provider is an unreachable loopback port, so no model is called.
Results on 2026-10-21:

- `debug config` resolves both inherited servers. The inline overlay
  deep-merges `enabled:false` over both. `debug config` itself starts nothing.
- **Control:** an unrestricted `run` starts both MCP servers (both markers).
  The same run with the overlay and `--pure` starts neither.
- **Control:** without `--pure` the inherited plugin loads (marker). With
  `--pure` it does not.

**Residual (accepted in D12):** with a plugin directory present, OpenCode installs
that config directory's plugin dependencies on first use (network, observed
taking more than 90 s in a fresh HOME). `--pure` prevents the plugins from
loading, not that install.
