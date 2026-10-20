# Security notes

Read this before running Arbitron on a shared machine or exposing it publicly.

## `--dangerously-skip-permissions`

Every install asks, **explicitly and with a warning**:

> Enable unattended agent tools (`--dangerously-skip-permissions`)?

It is never silently defaulted — the default answer is **No**.

Enabling it passes `--dangerously-skip-permissions` to the `claude` CLI, which
removes **every** tool-permission prompt: the agent runs `Bash` / `Write` /
`Edit` actions — shell commands, file writes, network calls — **with no human in
the loop**.

This is **RCE-equivalent (remote code execution)** whenever any prompt input can
be influenced by an untrusted source: anyone who can submit work to the agent
can effectively run arbitrary code as the account the service runs under.

**Only enable it on a machine and account where you accept that risk** and where
every input to the agent is fully trusted. On a shared server this is especially
dangerous because the service may run as a privileged account.

## Local mode + TLS exposure, and `allowunauthenticated`

**Local mode (`auth.mode: local`) has no authentication.** It is designed to
either bind loopback (single auto-admin, scenarios a/b/c) or sit behind a
separately authenticated edge (the `edge+local` shared option, which forces a
loopback bind).

The binary **refuses a non-loopback bind in local mode** by default. If you
enable **direct app-TLS** (`server.tls`) in local mode on a **non-loopback
host**, you would expose an **unauthenticated admin API behind TLS**. The
installer detects this and:

1. prints a warning, then
2. requires an **explicit extra confirmation**; only then does it set the
   **escape hatch** `server.tls.allowunauthenticated: true`. If you decline, the
   installer **aborts** rather than write an unsafe config.

**When is `allowunauthenticated` acceptable?** Only when the host is reachable
**solely** behind a separately-authenticated edge (tunnel/proxy) that you
control — i.e. nothing on the public internet can reach the bind directly.

**Safer default for direct public exposure:** use **OIDC org mode**
(`auth.mode: org`) so every request carries a per-user identity, instead of
exposing an unauthenticated admin. See
[tunnels-and-tls.md](tunnels-and-tls.md) for the exposure options.

## Secrets and file modes

Secrets — the **master key**, an optional **Anthropic API key**, and an optional
**OIDC client secret** — are **never written into `config.yaml`**. They live in a
protected sink that depends on the platform:

| Scenario | Secrets sink | Mode / protection |
|---|---|---|
| macOS / Linux local | `~/.arbitron/arbitron.env` | file mode `0600` (created under `umask 077`) |
| Linux shared | `/etc/arbitron/arbitron.env` | `0600`, owned `root:arbitron`, referenced via systemd `EnvironmentFile=` |

On Linux shared, `config.yaml` itself is world-readable (`0644 root:root`) **only
because it is secret-free** — every secret is in the `0600 root:arbitron` env
file.

### The master key (`ARBITRON_MASTER_KEY`)

The master key is **32 random bytes, base64-encoded**, generated up front and
written to the secrets sink. It is an **environment variable
(`ARBITRON_MASTER_KEY`) read by the binary at runtime — it is never a YAML field
and is never logged**. It encrypts stored connection secrets (e.g. Azure /
OpenRouter keys).

A Claude-subscription-only or API-key-only deployment simply never uses the
master key; it is generated anyway so that adding an Azure/OpenRouter connection
later "just works." On upgrade/reconfigure the existing key is **detected and
reused**, never regenerated.

## Download trust root

**Mandatory:** every artifact the installer downloads — the binary, and any
checksum-bearing dependency — is **SHA256-verified against the release
`SHA256SUMS` before it is executed or moved into place**. A mismatch aborts the
install leaving **no artifact** behind. This gate is **not bypassable**.

Release artifacts are named `arbitron_<version>_<os>_<arch>` and published
on GitHub Releases for `dbinky/arbitron` alongside `SHA256SUMS`,
`SHA256SUMS.sig`, and `SHA256SUMS.pem`.

### Defense-in-depth: verify the cosign signature

`SHA256SUMS` is signed with **cosign keyless** signing in the release workflow.
For extra assurance you can verify the signature before trusting the checksums.
Download `SHA256SUMS`, `SHA256SUMS.sig`, and `SHA256SUMS.pem` from the release,
then:

```bash
cosign verify-blob SHA256SUMS \
  --signature SHA256SUMS.sig \
  --certificate SHA256SUMS.pem \
  --certificate-identity-regexp 'https://github.com/dbinky/arbitron/\.github/workflows/release\.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The **trust root is the release workflow's GitHub OIDC identity**: the signature
is only valid if it was produced by
`dbinky/arbitron/.github/workflows/release.yml` running on a `v*` tag, with the
OIDC token issued by `https://token.actions.githubusercontent.com`. There is no
long-lived signing key to steal.
