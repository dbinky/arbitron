# Pi probe restrictions (2026-10-20)

Installed `pi --version`: `1.3.0`, package
`@earendil-works/pi-coding-agent` at
`/opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent`.
No harness install/update or authenticated model call was performed.

Verified before implementation against installed help and primary distributed
source (upstream https://github.com/earendil-works/pi, `packages/coding-agent`):

| File | SHA-256 |
|---|---|
| `pi --help` | `7e4c9e558ae8e1b64f6ebeb5a28d9f7dee8a99fcf99197cc4610f642861a3dba` |
| `docs/cli.md` | `c45e59f31574de71770a71c9a7c9b6ac03bf9cad4928aa31bb6614d661733679` |
| `docs/packages.md` | `5fe4a3009d7260fe1e2f54b64086fc3c8b4d678232bfa2dbc7e91fe588fc9ea5` |
| `docs/mcp.md` | `23b9cca35d1340e8adce6bed8b495da0ebb58931179a937db87d610a1b40b9bf` |
| `dist/cli/args.js` | `231eca0304b149165a35b12730052586603f23208667e3a57355638017450175` |
| `dist/main.js` | `08b91f9eb3adf9a5027c18732d1e4de70999bd15c445b65cb0ddd0cd68f2746b` |
| `dist/core/resource-loader.js` | `ad50235884ea6d5ee14cdae1efcfe6afef58b22b18ce574e81886d57ca9f4642` |
| `dist/core/package-manager.js` | `14176c5f6a8ba9df57fb2bae52eebb1823b3bd96453200be52978715308ee9eb` |
| `dist/core/sdk.js` | `8155c0b7d819d2248a1bbf0fa4b8e40d15552ff7fa6a772154cbbccb6ae59235` |

## Probe-only recipe

Retain production binary, model/thinking argv, allowlisted credential environment
and real account HOME. Use stdin, print/json/no-session and a private scratch cwd.
The verified 1.3.0 probe floor remains unchanged; ordinary missions retain 0.87.0.

- `--no-tools`: starts with every built-in/extension/custom/MCP tool disabled.
- `--no-mcp`: excludes the built-in MCP extension and its server connections.
  This flag alone cannot suppress replacement MCP extensions.
- **`--no-extensions`**: suppresses discovered/configured/package and built-in
  extensions, including their executable factories and replacement MCP plugins.
  Explicit `-e` paths still work, but the production builder emits none.
- **`--offline`**: main sets `PI_OFFLINE=1` and `PI_SKIP_VERSION_CHECK=1` before
  runtime construction. This suppresses automatic catalog/version/package
  updates and missing-package installation, not the requested provider call.
  Resource resolution can still make read-only npm root/list lookups; this is
  not a process sandbox or a general bypass of user configuration.

Source-contract reproduction (no model call, no network install):

```
node adapter/pi/testdata/probe-restrictions.mjs \
  /opt/homebrew/lib/node_modules/@earendil-works/pi-coding-agent /opt/homebrew/bin/pi
```

The fixture loads the real parser/resource loader/built-ins with hostile global
and trusted-project extension factories and a missing inherited npm package.
It requires no loaded extensions (including MCP), no executed factories and no
package install/update. Then actual CLI `--help` verifies the CLI-to-loader feed;
removing only `--no-extensions` executes the marker factory despite no-tools and
no-mcp. A local npm metadata leaf prevents installs/network during this check.
The initial fixture mistakenly classified npm root/list lookups as installs;
corrected it to distinguish those operations before claiming source acceptance.

`TestModelProbePiRestrictions` verifies exact/wrong-model/rejected-restriction
outcomes and no weaker retry; an ordinary pinned run control keeps normal argv.
`TestUsabilityPiRestrictionsRoot` feeds real SQLite connection credentials,
source data, inventory and startup/ticks through the root-built probe to durable
own-route evidence, announcement receiver and ordinary pinned execution output.
These Go tests replace only process/HTTP I/O; the separate source fixture verifies
the CLI contract rather than accepting a permissive fake as external evidence.
