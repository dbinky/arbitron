# LR-06 OpenCode private config/auth isolation collection (partial)

Access date: 2026-11-11. Source and installed schema version: OpenCode 1.19.5.
This is experiment tooling and an inference-free schema check, not installed
agent/helper completion or LR-06 verification.

## Versioned source inputs

Repository: anomalyco/opencode, tag `v1.19.5`.

| Source title / URL | SHA-256 of retrieved source |
| --- | --- |
| Global XDG/home paths — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/core/src/global.ts | `37c9aae045e01cd246cd4d8c489e84ea4849b6297d3a6f9ff9b1bca57f85c013` |
| Auth schema/read/write — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/auth/index.ts | `dc897ff4b04dba1ed81f3fe24cdce7289ae32acc6bac60bc430a884bb60ad5b0` |
| Configuration loading/merge — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/opencode/src/config/config.ts | `839c934f014c29a40f06cafd8bf2da1f0b2976ecd9e3e0f71b859792837ab14d` |
| Core config initialization — https://raw.githubusercontent.com/anomalyco/opencode/v1.19.5/packages/core/src/v1/config/config.ts | `9f7f0a80128153537e2eb4bf5163e45cbf68e3ed03c1efd1c39f39ea4cc18067` |

Global paths use `xdg-basedir`; with only private HOME and no XDG override,
config is `.config/opencode`, and auth is `.local/share/opencode/auth.json`.
Auth reads provider entries with `{type:"api", key:...}` and can alternatively
read `OPENCODE_AUTH_CONTENT`. Config's global/project loading and overlay merge
are detailed in [the effective-control note](lr06-effective-controls.md).
`Config.ensureGitignore` creates `.gitignore` on first config load even under
`--pure`, unless it already exists. The seed includes exactly its versioned
contents (`node_modules`, `package.json`, `package-lock.json`, `bun.lock`,
`.gitignore`, newline-separated with no final newline). It is protected, not
excluded from comparison. The seeds include `$schema` to avoid loader insertion.

## Composition and boundaries

`seedOpenCodeIsolation` exclusively creates a private 0700 HOME and seeds:

- user config with foreign main/helper/provider/destination/credential/limits;
- project config with distinct foreign main/helper selections;
- auth entries for LunaRoute and OpenAI with a synthetic conflicting credential.

The snapshot stores bounded bytes/modes of the complete user config tree,
project config and auth file privately. Auth/database/log directories are not
confused: runtime databases/logs may be written in the experiment, but the
protected config/auth inputs must remain unchanged. Missing/unreadable files,
new/deleted config entries, byte/mode changes, symlinks (including ancestors)
and overflow refuse observation publication. No path, credential, config or
snapshot bytes are serialized.

`TestLiveOpencodeCompaction` constructs this producer before launch and passes
it to the actual Factory's control journal. Before even `debug config` executes,
the runner binds the protected HOME/project roots to the real emitted env/cwd,
rejecting duplicate/missing/foreign HOME and XDG/auth/config relocation overrides.
It checks again after each same-env debug query. The final supporting collector
requires both actual launch bindings and unchanged current snapshots in addition
to all four correlated inference categories and command-created workspace proofs.
Only safe source/unchanged/private-home booleans enter the JSON artifact, after
capture and workspace cleanup. There is no production env/config change.

## Offline verification and scope

```
go test ./test/lunaroute -run 'TestAgentEvidenceOpenCode(Isolation|InstalledIsolationSeeds)' -count=1
```

The installed schema check runs only `debug config --pure` in throwaway HOME and
project directories with synthetic credentials. Without the overlay it requires
both the distinct user provider and project selectors to load, preventing a
misspelled seed path from masquerading as precedence evidence. With the overlay
it requires the selected effective controls, then unchanged protected trees.
No paid inference or live credential is used. Missing installed binary skips
with verification pending.

Protocol executables additionally exercise real Factory/runner/CONNECT/TLS,
actual warm-up/continuation checks, completed helper joins and command-created
files through the final private JSON sink. Mutated/missing snapshots or missing
actual-launch feeds cannot publish despite otherwise successful traffic/files.
They prove tooling, not installed-binary isolation, helper or contract completion.
The operator supporting run and the complete contract/agent entry points remain
separate milestones; no support fact, register status or readiness change follows.
