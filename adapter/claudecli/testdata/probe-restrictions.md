# Claude probe restrictions (2026-10-20)

Installed CLI: `claude --version` → `2.1.304 (Claude Code)`. No install,
upgrade, authenticated model call or real webhook was used for verification.
`claude --help` SHA-256:
`b10f5c36adfe3285f6c5a0d18284f466e6c4486499de56ef62745e13312a7fbc`.

Primary documentation retrieved before implementation (SHA-256 of raw Markdown):

- https://code.claude.com/docs/en/cli-reference.md
  `029287d82a4da572e8befd5cc5a699ab516c77b18d375bd715eda5972dede53f`
- https://code.claude.com/docs/en/env-vars.md
  `2f8189d3d6345bd18a3741c3e1efab7f29283189fb0c015085f48e009faa67d3`
- https://code.claude.com/docs/en/managed-mcp.md
  `45a53673fa9e07f69610a79437bb4c0cc477090311097772bf740c0e7dbbb6ad`
- https://code.claude.com/docs/en/hooks.md
  `bd43bf9a559f39af292fc159573371c01e19dadd84f8c1b10fac954d975dba11`

## Probe-only recipe

Reuse the actual binary, account/config directory, connection credential and
capability environment, model, stdin, print/stream-json protocol and private
scratch cwd. Keep `--allowedTools ""` and no permission bypass or fallback.
Add these independent controls:

- `--tools ""`: removes built-in tools. `--allowedTools` alone grants permission
  without prompting; it does **not** remove the registered tools.
- `--strict-mcp-config --mcp-config '{"mcpServers":{}}'`: use only the explicit
  empty server set, not inherited servers. The managed-MCP documentation says
  an installed exclusive `managed-mcp.json` makes strict mode exit at startup;
  this is a failed/unknown probe, not permission to retry without restrictions.
- `--safe-mode`: disables customization loading, including installed/managed
  plugins, skills, MCP, CLAUDE.md and user hooks. Authentication and model
  selection remain normal. Do **not** use `--bare`: its documented API-key-only
  Anthropic auth would break the subscription route.
- `--setting-sources ""`: excludes user/project/local settings, including their
  `env` and plugin/update settings. Managed policy still applies.
- `--no-chrome`: explicitly disables the built-in Chrome integration.
- Child env `DISABLE_UPDATES=1`,
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, and
  `CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1`: disable harness
  updates, nonessential traffic/background plugin command sources, and the
  official marketplace auto-install exception documented for that traffic flag.

Safe mode does not bypass enterprise policy: policy-configured hooks can still
run. No claim of an OS sandbox or bypass of administrator policy is made. The
probe contract here excludes inherited tools/MCP/plugins and update activity;
ordinary execution keeps its existing customization/permission behavior.
Unsupported flags/configuration fail closed at process execution, yielding no
confirmed model; there is no less-restricted retry.

Both batch agent and interactive agent use the existing shared argument and
environment builders; the chat lanes explicitly request the same probe posture.
Exact execution still requires the requested `modelUsage` key. A customization
restriction or successful process alone is never identity proof.

`TestModelProbeClaudeRestrictions` checks all four adapter forms and exact,
wrong-model and rejected-configuration outcomes. `TestUsabilityClaudeRestrictionsRoot`
loads hostile inherited settings/MCP files, drives real root startup/ticks through
SQLite and the sender/receiver, requires the workflow mode's own persisted proof,
and then executes an ordinary pinned run. Its runner substitutes process I/O;
it verifies the documented emitted recipe, not independent CLI internals.
