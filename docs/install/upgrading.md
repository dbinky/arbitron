# Upgrading, reconfiguring, and uninstalling

Re-run the **same one-line command** you used to install. The script detects the
existing install (config + service present) and offers **Upgrade**,
**Reconfigure**, or **Uninstall**.

- macOS / Linux: `curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash`
- Windows: `irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 | iex`

Pick the **same install type** (Local vs Shared) you originally chose so the
script looks in the right place. Shared re-runs need the same elevation as the
original install (sudo / root on Linux, Administrator on Windows).

## Upgrade

Upgrade downloads the new binary, **checksum-verifies** it, swaps it in, and
restarts the service. Your **config, data, and secrets are preserved**.

Pin a target version with `ARBITRON_VERSION`; otherwise the latest release is
used:

```bash
ARBITRON_VERSION=1.3.0 curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
```

```powershell
$env:ARBITRON_VERSION='1.3.0'; irm https://raw.githubusercontent.com/dbinky/arbitron/main/install.ps1 | iex
```

Check the running version any time with `arbitron version` (prints version,
commit, and build date).

> ### ⚠️ Forward-only database migrations
>
> Database migrations run **automatically on `serve` start** and are
> **forward-only** — there is **no supported down-migration**.
>
> Once you upgrade to a binary that runs a new migration, **downgrading back to
> an older binary is unsupported**: the older binary cannot read the migrated
> schema and there is no automatic rollback.
>
> **If you need a rollback path, back up the database file before upgrading.**
> To roll back, restore that backup *and* reinstall the matching older binary
> (`ARBITRON_VERSION=<old>`). The database files are:
>
> | Scenario | Database file |
> |---|---|
> | macOS / Linux local | `~/.arbitron/data/arbitron.db` |
> | Windows local | `%LOCALAPPDATA%\Arbitron\data\arbitron.db` |
> | Linux shared | `/var/lib/arbitron/arbitron.db` |
> | Windows shared | `%ProgramData%\Arbitron\data\arbitron.db` |
>
> Stop the service before copying the DB file. Always review the release notes
> for the version you are upgrading to.

## Reconfigure

Reconfigure re-runs the config Q&A **pre-filled from your existing
`config.yaml`**, rewrites the config, and restarts the service. **Data and the
existing master key are preserved** (the key is detected and reused, never
regenerated).

Reconfigure is how you change the port, switch between subscription and API-key
LLM access, toggle skip-permissions, change the auth model, or add/adjust TLS on
a shared server.

## Uninstall

Uninstall stops and removes the **service**, the **binary**, and the
**config**, then **prompts before removing data** (default **keep**).

What is removed per scenario:

| Scenario | Always removed | Prompted (default keep) |
|---|---|---|
| macOS / Linux local | LaunchAgent/systemd unit, `~/.arbitron/bin/arbitron`, `config.yaml` | data, workspaces, logs, **and `arbitron.env`** |
| Windows local | Scheduled Task `Arbitron`, `bin\`, `config.yaml`, env-var/DPAPI secrets | data directory |
| Linux shared | systemd unit, `/usr/local/bin/arbitron`, `/etc/arbitron/config.yaml` + `arbitron.env` | data (`/var/lib/arbitron`); **also** the `arbitron` service account |
| Windows shared | Windows Service `arbitron`, `bin`, `config.yaml`, DPAPI secrets | data directory; **also** the dedicated service account (if one was created) |

On Linux/Windows shared, removing the service account is a **separate prompt**
(default keep). On Linux local/macOS, choosing to remove data also removes the
secrets env file.

> Removing data is **irreversible** — it destroys your database, workspaces, and
> logs. Take a backup first if you might want them back.
