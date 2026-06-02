# opencode Headless Spike — Finding

Throwaway de-risking spike for Phase 1, Task 7
(plan: `docs/superpowers/plans/vendor-abstraction-implementation-phase-1-task-7.md`;
source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §5, fallback §9).
**No production Go code, no repo tests, and no committed credentials were produced.**

## Environment

| Item | Value |
| --- | --- |
| opencode binary | `/opt/homebrew/bin/opencode` |
| opencode version | `0.0.55` (the older Go `opencode` variant — Cobra-style single-binary CLI, **not** the newer `opencode run`/`opencode serve` TypeScript variant) |
| OS | macOS (Darwin 25.4.0), arm64 |
| Provider / model | Anthropic (default model auto-selected from `ANTHROPIC_API_KEY`); no explicit model could be chosen — see below |
| Auth mechanism | standard `ANTHROPIC_API_KEY` environment variable (read from the process env at run time) |

## Real headless flags discovered

The plan assumed a `opencode run --model <provider/model> "<prompt>"` syntax (from the newer
opencode). **That syntax does not exist in this installed version.** There is **no `run`
subcommand** (`opencode run --help` just prints the root help). The actual headless contract is:

```
opencode -p "<prompt>" [-c <cwd>] [-q] [-f text|json]
```

| Flag | Meaning |
| --- | --- |
| `-p, --prompt <string>` | **Non-interactive (headless) mode.** Runs one prompt and exits; this is the headless entry point. |
| `-c, --cwd <string>` | Working directory — the stand-in for `{{workspace}}` / engine `WorkDir`. |
| `-q, --quiet` | Hide spinner in non-interactive mode. |
| `-f, --output-format <text\|json>` | Output format for non-interactive mode. |
| `-d, --debug` / `-v, --version` / `-h, --help` | as named. |

**Notably absent:** there is **no `--model` flag**, **no `--resume`/`--continue` flag, no
`--session`/session-id flag, and no `serve` subcommand** in this version. Model selection is
implicit (driven by which provider key is present in env); cross-step session resume is **not
exposed by the CLI surface at all.**

## Single-shot headless edit result (the core pass/fail probe)

Workspace: a scratch dir under `$TMPDIR` containing `hello.txt` (contents: `hello`).

Command run (no TTY; `stdin` redirected from `/dev/null`; 90s timeout to catch hangs):

```
timeout 90 opencode -c "$SPIKE_DIR" -q \
  -p "Edit the file hello.txt ... contents become exactly the word: goodbye ..." \
  < /dev/null
```

Result:
- **Exit code:** `1`
- **Wall-clock:** ~1 second
- **stderr:** `Error: agent processing failed: failed to process events: POST
  "https://api.anthropic.com/v1/messages": 401 Unauthorized {"type":"error","error":
  {"type":"authentication_error","message":"invalid x-api-key"}, ...}`

| Pass-bar condition | Result | Note |
| --- | --- | --- |
| File actually mutated | **FAIL** | `hello.txt` unchanged (`hello`). The edit never ran because the LLM call was rejected. |
| Process exits 0 | **FAIL** | Exit `1` (auth error, not a tool/edit failure). |
| No interactive-prompt hang | **PASS** | Returned in ~1s; a strict 30s-ceiling re-run exited `1` in 1s (not `124`/timeout). Headless mode never blocks on a TTY prompt. |

### What blocked it

**Credentials, not the headless plumbing.** An `ANTHROPIC_API_KEY` *is* present in this
environment, but it is **invalid** — Anthropic returns `401 invalid x-api-key`. The spike
therefore could not complete a real file mutation. Everything *up to* the provider call worked:
opencode launched headlessly, honored `-c` (cwd), read the key from env, selected an Anthropic
model, and issued the API request — all without a TTY and without hanging. The single missing
ingredient is a **valid provider credential**; with one, the edit path is plausibly exercisable,
but that could **not** be confirmed in this environment.

**Required to make the single-shot edit pass in a future run:** a valid Anthropic API key in
`ANTHROPIC_API_KEY` (or the equivalent env var for whichever provider/model is chosen). No config
file is required for this version; env-var auth is sufficient.

## Cross-step resume result

**Could not be tested — and the CLI provides no resume primitive to test.** The resume probe was
gated on the single-shot edit succeeding (it did not, due to bad creds), AND this opencode version
exposes **no headless resume mechanism**: no `--resume`, no `--session`/session-id, and no `serve`
subcommand. There is therefore no supported way to thread a `SessionID` across two headless
`opencode -p` invocations on this binary. This is consistent with the spec §9 risk and the
upstream session-resume concerns (issues #2404, #13851): cross-step headless resume is, at best,
unproven here and, on this version, simply unavailable.

**Resume verdict: UNRELIABLE / UNAVAILABLE / UNTESTED.**

## Contract decision

> **opencode `AgentProvider` = single-shot mission per step.** No `SessionID` threading.

Rationale: this opencode version offers **no headless session-resume primitive** (no
`--resume`/`--session`/`serve`), and resume could not be validated (no usable credential).
Per the plan's resume-decision rule and the spec's documented fallback (§9), when resume is
unreliable **or cannot be tested**, the contract **defaults to single-shot** — the safe default.

## Phase 3 impact

- Each `agent_call` node maps to **exactly one `opencode -p "<mission>"` invocation** run with
  `-c <workspace>` (cwd = the seeded `WorkDir`); the process is headless and does **not** hang on a
  TTY (confirmed).
- **No `SessionID` plumbing** in the opencode adapter — `AgentMission.SessionID` is left unused for
  the opencode backend. Cross-step continuity, if ever needed, must be carried via the
  workspace/filesystem state, not an opencode session.
- The Phase 3 adapter must use **`-p`/`-c`** (this installed version), **not** the assumed
  `run --model …` syntax. If the deployment target ships the newer `opencode run`/`serve` variant,
  the flag mapping (and any resume capability) **must be re-verified** before relying on it.
- The opencode adapter must **inject the provider key as a subprocess env var only**
  (`ANTHROPIC_API_KEY` or provider equivalent), mirroring the engine's `adapter/procenv` secret
  hygiene — never a config file in the repo.
- **claude-cli `AgentProvider` is unaffected**: it continues to thread `SessionID` via `--resume`;
  this finding changes only the opencode backend.
