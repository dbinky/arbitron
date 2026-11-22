# Observed: `claude auth status` (Claude Code 2.1.329)

Observed 2026-11-22 on macOS (Apple M2), Claude Code **2.1.329**, during plan dependency resolution (RR-03, P1 T13 `ClaudeAuthProber`). The command was run read-only. Account identifiers and paths are redacted (`<redacted>`). This file is the **producer** for the P1 T13 auth-status fixtures; the fixtures are copied from it verbatim (redactions kept).

## Logged in (subscription, keychain credential)

Command: `claude auth status` (ANTHROPIC_API_KEY unset). Exit code **0**. stdout:

```json
{
  "loggedIn": true,
  "authMethod": "claude.ai",
  "apiProvider": "firstParty",
  "analyticsDisabled": false,
  "projectsDirectory": "<redacted>/.claude/projects",
  "configDirectory": "<redacted>/.claude",
  "loginExpiresAt": "2026-12-13T08:54:44.323Z",
  "consoleProfileSignInOffered": true,
  "email": "<redacted>",
  "orgId": "<redacted>",
  "orgName": "<redacted>",
  "subscriptionType": "max",
  "storedClaudeAiLogin": true,
  "planTier": "max_20x"
}
```

## Logged out (empty config directory)

Command: `CLAUDE_CONFIG_DIR=$(mktemp -d) env -u ANTHROPIC_API_KEY claude auth status`. Exit code **1**. stdout:

```json
{
  "loggedIn": false,
  "authMethod": "none",
  "apiProvider": "firstParty",
  "analyticsDisabled": false,
  "projectsDirectory": "<redacted>/projects",
  "configDirectory": "<redacted>",
  "loginExpiresAt": null,
  "consoleProfileSignInOffered": true
}
```

## Contract derived (decision 2026-11-22, RR-03)

| stdout / exit | `ClaudeAuthProber` result |
|---|---|
| JSON object with boolean `loggedIn: true` **and** exit 0 | auth **ok** |
| JSON object with boolean `loggedIn: false` **and** non-zero exit (exit 1 observed) | **logged out** — raises the source-auth notification and parks the source |
| anything else: no JSON, non-boolean or missing `loggedIn`, exit 0 with `loggedIn: false`, non-zero exit with `loggedIn: true`, timeout, spawn failure | **unknown** — never parks, never recovers |

A bare zero exit without JSON is **not** auth ok. Only `loggedIn` is read; other keys (email, org, plan) are never stored, logged or published.

Where credentials live on macOS (same observation): the Claude subscription login is stored in the **login keychain** item `Claude Code-credentials`; no `~/.claude/.credentials.json` exists. This is why the runner must run in the user's GUI session (P2 T11 LaunchAgent decision).
