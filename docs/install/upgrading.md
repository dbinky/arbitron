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

### Sample workflows and the shared `_lib` are seeded once

Bundled samples and the `_lib` files they share are seeded **once per name**
(the seed ledger in the workspace root). An upgrade adds new samples and new
`_lib` files, but it never replaces one that is already installed, and replacing
the binary or the samples tarball does not change an installed workflow or
library file. A host that was seeded before a fix therefore keeps running the
old copy until an admin updates it. Do not clear the ledger or delete the
installed copies: that discards local customizations. Update them in place.

**Run-time guard.** Triggering a workflow now checks every `spawn_workflow` node
against the child workflow *as the run owner sees it* (their private copy first,
then the published shared one) and refuses to start when the node maps an input
the child does not declare. The error names the workflow, the input and the
child, and no instance is created, so a stale child fails before the first node
instead of after hours of upstream stages. The same check still runs when the
spawn node executes.

**Updating the installed copies** (admin; use the dashboard or the MCP tools):

1. **Compare.** Read the installed file with `workflow_read_file` and compare it
   with the file shipped in the new release (`samples/` in the tarball). If the
   installed copy has local changes, merge the release's changes into it rather
   than overwriting it.
2. **Shared library.** Write the reviewed contents with `workflow_write_file`
   using name `@lib` and `scope: "shared"`. Files changed by the tamper-gate
   hardening: `tamper-gate.sh`, `tamper-verdict.sh`, `manifest-read.sh`. New
   files (for example `commit-leftovers.sh`) are seeded automatically.
3. **Check what the run owner actually resolves.** A private library file with
   the same path shadows the shared one for its owner, so a fixed shared file
   can still be bypassed. `workflow_list_files` with name `@lib` returns both
   `private` and `shared`; for every path above that also appears under
   `private`, update or remove (`workflow_delete_file`) that private copy for
   its owner, then `workflow_read_file` it without a `scope` (private first,
   then shared) as that owner and confirm you get the fixed contents.
4. **Child workflows.** Update the installed `ralph-loop` and `pr-review` to the
   release versions (`ralph-loop` gains the `in_place`, `focus_seed` and
   `baseline_path` inputs that `feature-pipeline` maps in its refine stage;
   `pr-review` changes its model profiles), then **publish** them with
   `workflow_publish` under their existing names. An unpublished edit is not
   what other users run. Keep any profile or prompt customizations when merging.
5. **Verify.** Trigger `feature-pipeline` (or `workflow_validate` it): a
   child that is still stale is refused with the message described above.

Test an upgrade against a host that was already seeded, not only a fresh
install: a fresh install seeds the new copies and hides this gap.

### `feature-pipeline` is now the composable parent

In this release `samples/feature-pipeline` is the composable parent that spawns
the stage workflows (pre-release builds shipped it as `feature-pipeline-next`).
The old monolithic pipeline ships unchanged as `legacy-feature-pipeline`.

Samples are matched by name against the seed ledger, so the rename does not
touch an installed workflow:

- **A host seeded with the monolith** keeps it as `feature-pipeline`. The
  upgrade seeds a second copy of the monolith as `legacy-feature-pipeline`
  plus the stage workflows, but not the new parent: `feature-pipeline` is
  already in the ledger. Nothing is overwritten.
- **A host seeded with a pre-release `feature-pipeline-next`** keeps both of
  its names and gains `legacy-feature-pipeline`.

To switch a host to the new parent (admin, MCP tools or dashboard):

1. Remove the freshly seeded `legacy-feature-pipeline` copy
   (`workflow_unpublish`, then `workflow_delete`). It has no runs.
2. `workflow_rename` the installed `feature-pipeline` (the monolith) to
   `legacy-feature-pipeline`. Its id, run history and schedules move with it.
3. If the host has `feature-pipeline-next`, `workflow_rename` it to
   `feature-pipeline`. Otherwise create `feature-pipeline` from the release's
   `samples/feature-pipeline/` (`workflow_create`, then `workflow_write_file`
   for each file under `scripts/`), and `workflow_publish` it.

The seeder will not re-seed either name afterwards. Branch manifests
(`.arbitron/pipeline/<slug>.json`) written by `feature-pipeline-next` resume
under `feature-pipeline`: the `workflow` field on each manifest entry is a
record of who wrote it, and resume does not read it. Two tests pin this:

- `samples/_lib/manifest-read_test.bats`: "entries stamped
  feature-pipeline-next count the same as feature-pipeline ones"
- `samples/_lib/manifest-append_test.bats`: "resumes a manifest stamped
  feature-pipeline-next under the renamed parent"

### Installed workflows the seed ledger does not know

The ledger only records names the seeder itself installed. A workflow can also
reach a host another way, and then it exists without a ledger entry. Examples:

- the `legacy-feature-pipeline` produced by the rename steps above;
- stage workflows (`ideate`, `spec`, `design`, `plan`, `implement`, `harden`)
  or `ralph-loop` created or renamed with the MCP tools.

When a release ships a sample with one of those names, the seeder refuses to
overwrite it and **stops the whole seed run** at that name. It handles samples
in directory-name order, so every sample after the conflicting one is not
seeded on that boot. The shared `_lib` files are seeded first and are not
affected. Nothing is overwritten and the server still boots, but the later
samples are missing.

**How to tell.** The server log has one `ERROR` line at boot:

```text
sample workflow seeding failed; continuing boot without samples  err="workflow already exists..."
```

The error reads `workflow already exists: sample publication <name>` when a
publication holds the name, and plain `workflow already exists` when only the
workflow itself does. The plain form does not name the sample. In either case,
compare the release's sample directories with the ledger and with
`workflow_list`. A name that is installed but missing from the ledger is the
cause. On a local macOS/Linux install the log is
`~/.arbitron/logs/arbitron.err.log`.

**Remedy.** Before you start the upgraded server, append each such name to the
ledger. The ledger is `<workspace>/.samples_seeded`, where `<workspace>` is
`workspace.path` in `config.yaml`. For example, `~/.arbitron/workspaces` on a
local install:

```sh
LEDGER="$HOME/.arbitron/workspaces/.samples_seeded"   # <workspace>/.samples_seeded
for name in legacy-feature-pipeline ideate spec design plan implement harden ralph-loop; do
  grep -qxF "$name" "$LEDGER" || printf '%s\n' "$name" >> "$LEDGER"
done
```

Only list names that are **already installed** on the host. A ledger entry
tells the seeder never to install that name, so a sample you list but do not
have will never be seeded. Then restart. The `ERROR` line should be gone, and
`sample workflow seeding complete` is logged instead. Update the listed
workflows to the release versions by hand (see *Updating the installed copies*
above).

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
