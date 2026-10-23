# Claude fingerprint state (Area 4 / G4)

Inspected 2026-10-19: the installed native Claude Code 2.1.303 binary at
`~/.local/share/claude/versions/2.1.303`, SHA-256
`d2da3b003dc08b88b7230469014fe3e1dc6fe94bc8f648e89b34175d30269370`.
Read only embedded implementation strings; no account files, Keychain entries,
or subscribed model calls were read or invoked.

The [official directory reference](https://code.claude.com/docs/en/claude-directory)
documents the configuration override and identifies global state as containing
OAuth metadata and personal MCP configuration. The inspected binary's global
file resolver (`B`, reached from `CWo`) places `.claude.json` in the configured
directory when `CLAUDE_CONFIG_DIR` is nonempty, otherwise directly in HOME.
`procenv.BaseEnv` forwards that override to all Claude launchers. Settings,
credentials and the plugin installation record remain under the config directory
(default `~/.claude`); the global file is a sibling only in the default layout.

The embedded usage-state sets are Claude's own classification of what sessions
rewrite without changing configuration: global `TO` (counters, impressions,
`cachedGrowthBookFeaturesAt`, `startupPrefetchedAt`, first-start dates) and
per-project `RO` (`lastCost`, `lastSessionId`, `lastModelUsage` and the other
last-session metrics written by `zHe` when a session ends, plus `history`,
example files and onboarding flags). The CLI's own `zm` treats a project diff
confined to `RO` as no change. The OAuth profile writer `Aln` stores
`profileFetchedAt` separately from account and organization identity.
`claudeGlobalProjection` omits exactly the `TO` keys except cache contents, the
`RO` keys inside every `projects` entry, and `profileFetchedAt`. Account
metadata, MCP configuration (global and per-project), env, model settings,
feature cache contents (`cachedGrowthBookFeatures`, `cachedDynamicConfigs`, ...),
project tools/permissions, new project entries and unknown keys remain
content-sensitive. Settings/plugin files still use their complete bytes. Strict
JSON object/EOF decoding rejects partial state and hides decoder content from
surfaced errors.

Why the project metrics matter (paired:3): any interactive Claude session on the
same host rewrites `projects[<its cwd>].last*` in the effective global file on
exit. Hashing them changed every Claude path's `ConfigRevision` whenever the
operator closed a session, so a probe or a cycle overlapping it could never
confirm or commit. `TestModelProbeClaudeSelfRefreshAtRoot/*/global_rewrite`
now rewrites these fields and goes red without the omission.

Project scope (paired:3): the inspected binary names project settings as
`.claude/settings.json` and `.claude/settings.local.json`; with the project
`.mcp.json` they are read from the launch working directory. The probes launch
from `<workspaces>/.model-probe`, so `claudeProjectInputs` hashes those three
files there. Real runs launch from their own workspace and are not covered by a
per-path revision; the run's executed-model check is their guard (G5).

## Unresolved OAuth renewal identity

The inspected credential writer stores access/refresh tokens, expiry, scopes,
subscription type, rate-limit tier and client ID in `claudeAiOauth`. Its renewal
CAS compares the old refresh token before replacing that bundle. Account UUID
and organization UUID live separately in global `oauthAccount` metadata. The
file-backed token bundle has no inspectable authenticated account claim binding
its opaque token material to that metadata. Merely keeping the cached metadata
while dropping tokens would also conceal a token-only account replacement.

A temporary implementation dropping tokens with unchanged cached metadata made
`TestModelProbeClaudeSelfRefreshAtRoot/relocated=false/batch/opaque_token_replacement`
fail: it incorrectly confirmed. That implementation was discarded. The shipped
credential projection canonicalizes JSON but preserves **every** credential and
expiry field; token replacement (including renewal) invalidates old proof.
G4/G36 stay open until a source-verified producer distinguishes actual renewal
from account replacement through the real launch path. Keychain-backed identity
is not established by these file-backed fixtures either.

`TestModelProbeClaudeSelfRefreshAtRoot` runs both batch and interactive agent
probes via the real root, inventory, router, factory and subprocess launcher,
under default and relocated layouts. Global counter/profile-timestamp rewrites
confirm and retain the gate revision. Account/settings edits before launch
refuse without launching; edits during I/O, MCP changes, partial state, opaque
token replacement and wrong/missing modelUsage refuse confirmation. Raw hashing
fails the counter case; ignoring the effective global file fails the relocated
account-before-launch case. This closes only the global-state/location slice.
