# Upgrading, reconfiguring, and uninstalling

Re-run the **same one-line command** you used to install. The script detects the
existing install (config + service present) and offers **Upgrade**,
**Reconfigure**, or **Uninstall**.

- macOS / Linux: `curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash`

Pick the **same install type** (Local vs Shared) you originally chose so the
script looks in the right place. Shared re-runs need the same elevation as the
original install (sudo / root on Linux).

## Upgrade

Upgrade downloads the new binary, **checksum-verifies** it, swaps it in, and
restarts the service. Your **config, data, and secrets are preserved**.

Pin a target version with `ARBITRON_VERSION`; otherwise the latest release is
used:

```bash
ARBITRON_VERSION=1.3.0 curl -fsSL https://raw.githubusercontent.com/dbinky/arbitron/main/install.sh | bash
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
> (`ARBITRON_VERSION=<old>`). Back up the database **and** its model-state key,
> which sits beside it with the suffix `.model-state.key`. The key is created on
> first boot and never regenerated: a database restored without its key refuses
> to start ("model-state key ... is missing"), and the error names the file to
> restore. The files are:
>
> | Scenario | Database file (key file: same path + `.model-state.key`) |
> |---|---|
> | macOS / Linux local | `~/.arbitron/data/arbitron.db` |
> | Linux shared | `/var/lib/arbitron/arbitron.db` |
>
> Stop the service before copying the DB and key files. Always review the release notes
> for the version you are upgrading to.

### Profiles must name a fixed model version (latest-model profiles release)

From this release every run pins each profile's model at trigger time. A
**pinned** profile must name a fixed model version on its route, for example
`claude-sonnet-5-5` on the Claude subscription. An empty model (formerly "use
the claude-cli default") or a mutable alias such as `sonnet`, `gpt-4o` or
`gpt-5` refuses every trigger of a workflow that uses it. The refusal names the
spellings the route accepts.

- **Boot preflight.** At every start Arbitron checks each persisted pinned
  profile. Each profile that will refuse triggers is logged as a `WARN`
  (`profile will refuse triggers`) with its alias, model and reason. It is also
  raised once as a `profile_refuses_triggers` notification (and to Teams when a
  webhook is configured). Following profiles are not checked; their selection
  mode governs them.
- **Fix:** update the profile with `profile_update` (or the dashboard) to a
  fixed spelling, e.g. `{"model": "claude-sonnet-5-5"}`.
- **Shipped defaults.** A fresh install's generated `config.yaml` now ships
  `claude-sonnet-5-5` for `default-chat`, `default-agent`, `assessor`,
  `interviewer` and `reviewer`. An existing `config.yaml` is never rewritten.
  Edit its `model: ""` entries yourself.
- **Automatic seed repair.** A config profile seed that was refused at an
  earlier boot (e.g. an empty-model default) stays pending. If the current
  `config.yaml` entry for that alias differs and is valid, Arbitron seeds the
  corrected entry on the next boot. A valid pending original is never replaced.
- **OpenRouter** accepts recognized `vendor/model` exact IDs:
  `anthropic/claude-sonnet-4.5`, `openai/gpt-6.1-sol`,
  `deepseek/deepseek-v4-pro`, `z-ai/glm-5.3`, `moonshotai/kimi-k2.5`.
- **LiteLLM and local Ollama cannot run pinned workflows in this release.**
  Their aliases and tags have no fixed-version identity (decision D13). Move
  those profiles to a supported route before upgrading if workflows depend on
  them.

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
| Linux shared | systemd unit, `/usr/local/bin/arbitron`, `/etc/arbitron/config.yaml` + `arbitron.env` | data (`/var/lib/arbitron`); **also** the `arbitron` service account |

On Linux shared, removing the service account is a **separate prompt**
(default keep). On Linux local/macOS, choosing to remove data also removes the
secrets env file.

> Removing data is **irreversible** — it destroys your database, workspaces, and
> logs. Take a backup first if you might want them back.
