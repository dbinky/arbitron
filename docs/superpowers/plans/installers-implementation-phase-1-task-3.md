# Installers P1 — Task 3: config Q&A, config.yaml + secrets env, binary download

> Phase 1 (P1) of the Arbitron installers feature. Source design:
> `docs/superpowers/specs/2026-06-05-installers-design.md` (§Config Q&A,
> §Secrets handling, §Distribution) and
> `docs/superpowers/specs/installers-design-phase-1.md` (§Config Q&A). Builds on
> Tasks 1–2.

## Context

This task delivers the interactive configuration step and the artifacts it
produces: it asks the short "easy questions", downloads + checksum-verifies the
`arbitron` binary for the detected os/arch, writes a `config.yaml` with **absolute**
paths and a **loopback** bind, and writes a protected secrets env file (`0600`)
containing a freshly generated master key (and, if chosen, the Anthropic API key).
It does NOT register the service (Task 4) — it just produces correct on-disk state.

Config keys are the real ones the loader consumes (verified against
`adapter/config/loader.go`): `server.host` (loopback), `server.port`,
`database.path`, `workspace.path`, `logging.file`, and the `llm.*` block. LLM
access maps to two shapes:
- **Subscription** (default on local) → `llm.provider: claude-cli`, `llm.binarypath`
  = the `claude` path resolved in Task 2, and `claude /login` run interactively to
  populate `CLAUDE_CONFIG_DIR`.
- **API key** → `llm.provider: anthropic` and the key placed in the secrets env as
  `ANTHROPIC_API_KEY` (the loader special-cases `ANTHROPIC_API_KEY → llm.apikey`),
  NOT written into `config.yaml`.

The skip-permissions question maps to `llm.skippermissions` and is asked
explicitly with the RCE-equivalent warning, never silently defaulted. The master
key is 32 random bytes → base64 → `ARBITRON_MASTER_KEY` in the secrets env (the
name the binary reads, per `adapter/crypto/masterkey.go`), generated even for
Claude-only installs so adding an encrypted connection later "just works"; it is
never logged.

Depends on: Task 1 (prompts, layout, logging), Task 2 (`download_verified`,
resolved `claude` path). Feeds: Task 4 (service references config + secrets env).

## Test plan (write first)

Create `tests/install/config.bats` and `tests/install/binary.bats`. Drive the Q&A
non-interactively via the `ARBITRON_*` env overrides from Task 1 (e.g.
`ARBITRON_PORT`, `ARBITRON_LLM_ACCESS`, `ARBITRON_API_KEY`, `ARBITRON_SKIP_PERMS`,
`ARBITRON_DATA_DIR`) and mock `curl`/`openssl`/`base64` where needed. All writes go
under the sandbox `HOME`.

config.yaml (`config.bats`):
- After a subscription-mode run, `~/.arbitron/config.yaml` exists and contains an
  ABSOLUTE `database.path` under the data dir (asserts it starts with `/` and is the
  sandbox path, not `./arbitron.db`).
- `workspace.path` and `logging.file` are absolute and under the data/logs dirs.
- `server.host` is a loopback address (`127.0.0.1`); `server.port` equals the chosen
  port (default 8080 when unanswered).
- Subscription mode writes `llm.provider: claude-cli` and `llm.binarypath` = the
  Task-2 `claude` path.
- API-key mode writes `llm.provider: anthropic` and does NOT put the key in
  `config.yaml` (asserts the key string is absent from the file).
- `llm.skippermissions: true` is written only when the skip-permissions prompt is
  answered yes; defaults to `false` otherwise.
- The generated YAML is valid (parse it with the installed binary's load path in an
  integration check, or with `python -c yaml.safe_load` / a yaml linter in CI).
- Reconfigure mode pre-fills answers from an existing `config.yaml` (asserts a
  pre-existing port is offered as the default).

secrets env (`config.bats`):
- `~/.arbitron/arbitron.env` exists with mode `0600` (asserts `stat` perms).
- It contains an `ARBITRON_MASTER_KEY=` line whose value is valid base64 decoding to
  exactly 32 bytes.
- API-key mode adds an `ANTHROPIC_API_KEY=` line; subscription mode does NOT.
- The master key value never appears in stdout/stderr logs (asserts the run output
  does not contain the key).
- Re-running reconfigure PRESERVES the existing master key (asserts the value is
  unchanged) and existing secrets.

binary download (`binary.bats`):
- The download URL is built for the detected os/arch as
  `arbitron_<version>_<os>_<arch>` (asserts the mocked `curl` was called with a URL
  containing the resolved triple; `ARBITRON_VERSION` override honoured, else latest).
- The binary is checksum-verified against `SHA256SUMS` before being placed at
  `~/.arbitron/bin/arbitron` and made executable (asserts a mismatch aborts and no
  binary is installed; a match installs mode `0755`).

shellcheck: `install.sh` clean.

## Implementation steps

1. Add `run_config_qa`: ask port (default 8080), LLM access (subscription | api-key,
   default subscription), skip-permissions (explicit, with the RCE warning text),
   and data location (default `~/.arbitron/data`). Each question uses Task 1's
   `prompt_default`/`prompt_yes_no` with env overrides; reconfigure pre-fills from
   the existing config (add a small `read_existing_config_value` grep helper).
2. Add `generate_master_key`: 32 random bytes → base64 (`openssl rand -base64 32`
   or `head -c 32 /dev/urandom | base64`), captured into a variable, never echoed.
3. Add `write_secrets_env`: write `~/.arbitron/arbitron.env` with `umask 077` (or
   create then `chmod 600`), containing `ARBITRON_MASTER_KEY=…` and, for API-key
   mode, `ANTHROPIC_API_KEY=…`. On reconfigure, reuse the existing master key.
4. Add `write_config_yaml`: emit a here-doc with absolute `database.path`,
   `workspace.path`, `logging.file`, loopback `server.host`, chosen `server.port`,
   and the `llm.*` block per the chosen access mode + skip-permissions answer; write
   with `0600`. Create the data/workspaces/logs/bin dirs (`mkdir -p`, `0700`).
5. Add `claude_login` (subscription mode only): export `CLAUDE_CONFIG_DIR` under the
   data dir and run `claude /login` interactively (skipped/mocked in tests).
6. Add `download_binary`: resolve version (`ARBITRON_VERSION` or latest), build the
   `arbitron_<version>_<os>_<arch>` URL + its `SHA256SUMS` entry from the canonical
   repo constant `ARBITRON_REPO` (default **`dbinky/arbitron`** — see Task 1's
   "Alignment notes"; the Go module path `github.com/arbitron/arbitron` is NOT a URL),
   call Task 2's `download_verified`, place at `$(layout_bin)/arbitron`, `chmod 0755`.
   (The exact version-resolution + URL builders are finalised in P5 Task 2 as
   `arbitron_resolve_version` / `arbitron_release_base`; P1 may target a provisional
   layout via `ARBITRON_REPO` and be re-pointed.)
7. Wire these into `run_fresh`/`run_reconfigure` from Task 1 in the correct order
   (deps already resolved by Task 2 → Q&A → download → write config + secrets).
8. Run `shellcheck`; write `config.bats` + `binary.bats`; make green.

## Files

- `install.sh` (modified) — Q&A, config/secrets writers, binary download.
- `tests/install/config.bats` (new).
- `tests/install/binary.bats` (new).

## Acceptance criteria

- [ ] `config.yaml` is written with absolute db/workspace/log paths and a loopback
      `server.host`; chosen port respected (default 8080).
- [ ] Subscription mode → `llm.provider: claude-cli` + `llm.binarypath`; API-key
      mode → `llm.provider: anthropic`, key only in the secrets env (never in YAML).
- [ ] `llm.skippermissions` reflects an explicit answer (RCE warning shown), default
      false.
- [ ] `arbitron.env` is `0600`, contains a base64 32-byte `ARBITRON_MASTER_KEY`, and
      the key is never logged.
- [ ] Reconfigure pre-fills from existing config and preserves the master key + data.
- [ ] Binary is downloaded for the correct os/arch and checksum-verified before
      install; a mismatch aborts.
- [ ] The written `config.yaml` loads cleanly with the real binary (loader accepts it).
- [ ] `shellcheck install.sh` clean.

## Dependencies

- Depends on: Task 1 (prompts/layout/logging), Task 2 (`download_verified`, resolved
  `claude` path).
- Required by: Task 4 (service uses config path + secrets env), Task 5 (tests).
