# Phase 1, Task 7: opencode Headless Spike (throwaway — finding, not production code)

## Goal

Run a contained, throwaway spike to de-risk the spec §9 critical-path item (opencode headless session-resume; upstream issues #2404, #13851) **before** the `AgentProvider` contract is frozen for opencode. The deliverable is a **written finding** that sets the opencode `AgentProvider` shape (single-shot mission per step vs. cross-step resume) for Phase 3. This task produces **no production Go code and no unit tests** — it is a manual/exploratory probe whose output is recorded.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §5.

## Dependencies

- **None blocking.** This task is parallelizable with Tasks 1–6 and does not block Phase 1 completion (design §8 item 7). It only informs Phase 3. Conceptually it references the `AgentProvider`/`AgentMission` shape from Task 1 (specifically the `SessionID` field), but it does not depend on that code existing.

## Important constraints

- **Throwaway:** do not add the spike to the production tree, the executor map, or CI. Run it in a scratch directory (e.g. `/tmp/opencode-spike/` or a `git worktree`/branch that is discarded). Do NOT wire opencode into `cmd/arbitron` or `adapter/`.
- **No secrets in the repo:** the provider key is injected as an environment variable into the subprocess at run time only; never commit it. Mirror the engine's secret hygiene (`adapter/procenv` allowlist philosophy) — the key lives in the operator's shell, not in any file.
- **Hex note:** because this is a spike, it does not need to honor the port/adapter split. The point is to learn the backend's behavior so the real Phase-3 adapter (which WILL honor the split) is shaped correctly.

## Procedure

1. **Set up a temp workspace** that stands in for a `{{workspace}}`:
   - Create a scratch dir with a single seed file (e.g. `note.txt` containing a known line).
   - Confirm `opencode` is installed and on PATH (`opencode --version`).

2. **Single-shot headless edit** (the core pass/fail probe):
   - Run, with no TTY (headless) and a provider key injected only into the subprocess env:
     ```
     OPENCODE_<PROVIDER>_API_KEY=… opencode run --model <provider/model> "<edit instruction that mutates note.txt>"
     ```
     (Use the actual opencode env-var name + `run` subcommand syntax for the chosen provider/model. Run from inside the scratch dir so the workspace = cwd, matching how the engine seeds `WorkDir`.)
   - **Record:** the exact command, the provider/model used, the env-var name, stdout/stderr, exit code, wall-clock duration.

3. **Cross-step resume probe:**
   - Attempt a second `opencode` step that depends on the first session's context (via `opencode serve` + a session id, or whatever resume mechanism opencode exposes headless). The instruction should require continuity (e.g. "now revert the change you just made") so a fresh session would behave differently from a resumed one.
   - **Record:** whether resume worked headless, reliably, and without an interactive prompt; capture upstream-issue-relevant symptoms (hang, prompt, lost context, error).

## Pass bar

- **Step 2 passes** iff: the seed file is **actually mutated**, the process **exits 0**, and **nothing blocks** on an interactive prompt (no hang). Record explicit pass/fail for each of the three conditions.

## Resume decision (the finding that sets the contract)

- **If step 3 (resume) is unreliable** (hangs, prompts, drops context, or errors): the opencode `AgentProvider` contract is **single-shot mission per step** — no `SessionID` threading. Each `agent_call` node becomes one self-contained `opencode run`, and `AgentMission.SessionID` is left unused for the opencode backend.
- **If step 3 is reliable:** the opencode adapter MAY thread `AgentMission.SessionID` for cross-step continuity (still optional).
- Either way, the **claude-cli `AgentProvider` is unaffected** — it already threads `SessionID` via `--resume` and that path is not changed by this finding.

## Deliverable (record the finding)

Capture the result as a written finding for the Phase 3 design notes. Because this conversation must not create stray docs, record the finding in the **PR description / Phase 3 design notes section** (not a new standalone file), structured as:

- **Environment:** opencode version, provider/model, env-var name, OS.
- **Single-shot result:** pass/fail for each of {file mutated, exit 0, no hang}, with the captured command + exit code + duration.
- **Resume result:** worked / unreliable, with the symptom and any upstream-issue correlation (#2404, #13851).
- **Contract decision:** "opencode `AgentProvider` = single-shot per step" OR "resume supported", with one sentence of rationale.
- **Phase 3 impact:** one line on how this shapes the opencode adapter (e.g. "no `SessionID` plumbing in the opencode adapter; each step is one `opencode run`").

## Testing (strict TDD note)

- **None.** Per design §7, the opencode spike is explicitly **not unit-tested** — it is throwaway exploratory work and its output is a design finding, not shippable code. The five-category TDD discipline does not apply to a probe that ships no production code. (This is the one task in the phase with no test deliverable, by design.)

## Acceptance Criteria

- The single-shot headless `opencode run` probe was executed and the three pass-bar conditions (file mutated, exit 0, no hang) are each recorded pass/fail.
- The cross-step resume probe was attempted and its reliability recorded.
- A written finding exists (in the PR / Phase 3 design notes) stating the opencode `AgentProvider` contract decision (single-shot vs. resume) and its Phase 3 impact.
- No opencode code, config, or wiring was added to the production tree; no provider key was committed.
