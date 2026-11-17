# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's
[private vulnerability reporting](https://github.com/dbinky/arbitron/security/advisories/new)
("Report a vulnerability" on the Security tab). Do not open a public issue.

Include what you found, how to reproduce it, and the version affected. You can
expect an acknowledgement within a week. Arbitron is maintained by one person,
so fixes are best-effort, but credential exposure and authorization bypasses
are treated as the highest priority.

## Supported versions

Only the latest release receives security fixes.

## Scope notes

Arbitron runs coding agents and scripts on the host it is installed on. Read
the [agent security model](docs/agent-security-model.md) and
[docs/install/security.md](docs/install/security.md) before exposing a server
to other users or the network. Connection credentials are encrypted at rest and
must never appear in workflow files, logs or API responses; a path that leaks
one is a vulnerability.
