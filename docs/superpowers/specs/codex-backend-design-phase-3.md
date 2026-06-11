# Codex Backend — Design Phase 3: readiness probe, sample config, docs

**Spec:** `docs/specs/codex-backend-spec.md`
**Phase goal:** Operationalize the codex backend — a readiness check, a turnkey
mixed-model sample pipeline, and prerequisite docs. Depends on Phases 1–2.

## Component 1 — Codex readiness probe

Unlike the credential-bearing providers, codex carries **no Arbitron
connection** (subscription auth lives in the host's `~/.codex`). So readiness is
a **backend-scoped health check**, not an entry in the connection
`ProbeSelector` (`adapter/probe/factory.go` dispatches by `Provider`; codex has
none).

- **`adapter/codex/readiness.go`** — a `Readiness(ctx)` function returning one of
  three states:
  - `ready` — binary present AND a valid login session,
  - `not-installed` — binary not found on PATH/StandardBinDirs,
  - `not-logged-in` — binary present but no usable `~/.codex/auth.json`
    session.
  Implementation: resolve+`codex --version` (installed?) then a lightweight
  login check (presence/validity of `~/.codex/auth.json`, or `codex login
  status` if that subcommand is stable — flag spelling pinned in the same
  constants block as Phase 1). All subprocess calls go through the shared
  `CommandRunner` seam so the check is testable with the fake.
- **Surface:** report the state through the existing system/health path
  (`mcp__arbitron__system_config` / the server's health reporting), so the UI/CLI
  can show `codex not ready` with the specific cause before a run. The exact
  wiring point is the system-config/health aggregator that already reports
  backend/connection status; codex adds one backend-scoped entry.

## Component 2 — Sample mixed-model feature-pipeline config

Deliver the spec's worked example as runnable config.

- Update the feature-pipeline **profiles** (the deployment `config.yaml` profile
  list and/or `samples/feature-pipeline/`) so:

  | Profile      | Backend       | Model      |
  | ------------ | ------------- | ---------- |
  | `interviewer`| `codex`       | `gpt-5.5`  |
  | `planner`    | `claude-code` | `opus-4.8` |
  | `coder`      | `claude-code` | `opus-4.8` |
  | `assessor`   | `claude-code` | (unchanged)|

- The two `interactive_agent` nodes (`brainstorm-spec`, `brainstorm-design`)
  already reference `model_profile: interviewer`; the `planner`/`coder`/
  `assessor` agent_call nodes are unchanged in the workflow JSON — only the
  profile definitions change. No workflow-graph edit required.
- Because profiles are `kind: agent` for all four, and codex is an agent
  backend, this passes existing profile-kind validation with no special-casing.

## Component 3 — Prerequisite docs

- Document, in the install/usage docs (e.g. `docs/install/README.md` and/or
  `README.md`), that the codex backend requires:
  - the Codex CLI installed on the host,
  - `codex login` (Sign in with ChatGPT) completed,
  - and note the `--yolo` posture and its local-single-user, RCE-equivalent risk
    (cross-referencing `docs/agent-security-model.md`).
- The installer is **not** modified (codex is a user-provided prerequisite per
  the spec).

## Data flow (readiness)

system/health request → codex `Readiness(ctx)` → `CommandRunner` (`codex
--version` + login check) → `{ready | not-installed | not-logged-in}` → surfaced
in health/system-config output.

## Error handling
- Readiness never throws for a "not ready" host — it returns a state, not an
  error, so the UI can render the cause. Genuine probe faults (e.g. runner
  failure) are returned as errors distinct from the three states.

## Testing (strict TDD, 5 scenario categories)
- **happy path:** fake runner reports version + valid login → `ready`.
- **input/state variation:** binary-missing → `not-installed`; version ok +
  no/invalid session → `not-logged-in`.
- **error handling:** runner fault distinguished from a clean not-ready state.
- **security:** readiness check sets no `OPENAI_API_KEY`; reads only login
  presence, never logs token contents.
- **sample config:** a test loads the updated profiles and asserts
  `interviewer` resolves to `backend codex` / `model gpt-5.5` and
  `planner`/`coder` to `claude-code` / `opus-4.8`, and that the sample workflow
  still validates.
- **acceptance:** end-to-end sample feature-pipeline (fake runners) exercises a
  codex interviewer node and a claude-code coder node in one run.

## Out of scope
Azure/multi-user codex auth; installer auto-install; OpenAI API-key mode (all
spec non-goals).
