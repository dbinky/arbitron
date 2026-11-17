# LR-05 — Private Claude settings/auth input collection (partial)

Reviewed 2026-11-12. Source provenance and offline collector validation only;
**not** installed precedence/helper verification, complete credential-store
isolation, a completed LR-05 contract, or a supported backend fact.

## Sources

- Claude Code settings: <https://code.claude.com/docs/en/settings>.
- CLI reference: <https://code.claude.com/docs/en/cli-reference>.
- Local source access/review date: 2026-11-12. The documentation URLs above
  are references, not pages fetched in this pass. Exact paths and relocation
  behavior below were inspected in the installed native 2.1.322 distribution,
  not inferred from the URLs or from a protocol fixture.
- Distribution identity/build and executable SHA-256 are recorded in
  [lr05-startup.md](lr05-startup.md). The following byte ranges in that same
  distribution identify the inspected bundled JavaScript (offset, length,
  SHA-256):

| Producer | Offset | Length | SHA-256 |
|---|---:|---:|---|
| `Be` config directory | 260734993 | 1100 | `56d02b45e3909a9cd8e9b698aa3071166723503986ea33a8fc995fca44f0a7b9` |
| `k1` secure-storage directory | 262436788 | 1100 | `5acdd0f3eda9dc950f368fffd8b836e44461505510c62e9ecffe1e994c7ef76c` |
| `P` plaintext credentials path | 262457123 | 1100 | `859f35e6c7b92918f8be1941754a0e3203e97e627207c5301feecb4e97007af9` |
| project/local settings paths | 261727025 | 1100 | `49bd29d541fe5892eb61b3c497f364bc28241fc11062687f52048099e1440af5` |

`Be` selects `CLAUDE_CONFIG_DIR` when set, otherwise the process home plus
`.claude`. `k1` additionally honors `CLAUDE_SECURESTORAGE_CONFIG_DIR`. `P`
places the plaintext fallback at `.credentials.json` inside that directory.
The storage code also has a macOS keychain path; a private HOME alone does
**not** establish absence of keychain access. The settings path builder names
`.claude/settings.json` and `.claude/settings.local.json` for project/local
inputs. This experiment uses the actual mission cwd, not an assumed ancestor
or git root. The CLI declares `--setting-sources <sources>` with user, project
and local sources; the startup collector separately requires the empty value.
Those are source/intent facts, not proof that the installed process honored
every control.

## Producer, composition and sink

`TestLiveClaudeCodeIsolation` creates a private home and a previously empty
mission workspace. `seedClaudeIsolation` exclusively creates conflicting user,
project and local settings plus a synthetic plaintext OAuth credential. It
snapshots bounded bytes and modes. The real production Factory emits the
selected credential/model/settings argv under this private HOME. The guard in
`liveClaudeIsolationFactoryWithClient` consumes that **actual** executable,
env and cwd before its same-env version query and mission, without rebuilding
or changing them. Foreign/duplicate HOME, config/secure-storage relocation,
alternate credential env or changed protected inputs refuses before execution.
A changed/missing/new/symlinked project config or changed protected user/auth
input after execution refuses despite otherwise successful inference/files.

The narrow collector additionally requires the accepted startup/final session,
actual captured hosted Bearer request/terminal response-model evidence, tool
trace and command-created file proofs. It writes `lr05-isolation.json` only
after successful experiment/proxy/CA cleanup. Private paths, config/auth bytes,
synthetic credentials, prompts and output text are not serialized. The
observation explicitly states its partial scope. Protocol subprocess fixtures
exercise the real Factory/runner/CONNECT/TLS and final private JSON sink; they
are not installed Claude observations.

The protected user inputs are `settings.json` and `.credentials.json` with
their ancestor modes; the project `.claude` tree is protected in full. Runtime
session/log files in the private home are distinct and removed at cleanup.
The follow-up [main-limit collector](lr05-limits.md) additionally joins reported
context and actual Messages output budgets to the source facts in this same
experiment. The [first-request memory collector](lr05-memory.md) now additionally consumes
protected project instruction bytes and actual Messages user text in this same
experiment. Neither offline slice proves installed `CLAUDE.md` loading, effective
selectors/effort, complete limit enforcement, forced helpers, managed settings
behavior, keychain isolation, absence of every possible external write, or the
absent-subscription case. Those remain part of
the full Steps 1/3 contract experiment. No register label, capability support
fact, ignored-control conclusion or genuine live artifact follows from source
review, fixture success or a missing-input skip.
