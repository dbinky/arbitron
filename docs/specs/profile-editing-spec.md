# Spec: Database-Driven, Admin-Managed Model Providers & Connections

**Slug:** `profile-editing`
**Status:** Approved spec (product outcomes; implementation details live in the design phase)
**Date:** 2026-06-11

## 1. Problem

Connections (provider endpoints + sealed credentials) are already editable live
via MCP. **Model providers — "profiles" — are not.** They are loaded once from
`config.yaml` at startup, so any change requires editing a file and restarting
the server. A profile today can only express `connection + model + backend +
allowed tools`; it cannot pin how much **context window** or how much **reasoning
effort** a model should use, even though those two axes materially change cost
and quality.

Separately, mutation of both profiles and connections is currently available to
**any authenticated caller** — there is no admin gate.

## 2. Goal

Make model providers **database-backed, admin-editable through MCP, and effective
immediately without a restart**, expand a provider to also pin **context-window
tier** and **reasoning effort**, and restrict all mutation — of providers *and*
connections — to **admins only**. The system must persist **only valid
providers**, where validity is governed by an admin-controlled, slow-moving
capability ruleset.

## 3. Headline Outcomes

- An admin **creates, updates, and deletes providers via MCP**; a change takes
  effect on the **next workflow run with no restart**.
- Providers are **stored in the database** and survive restarts.
- A provider pins: **connection, model, backend, allowed tools, context-window
  tier, and reasoning effort.**
- Context tier and effort **actually take effect at runtime** for the
  **claude-code** and **codex** backends, each through its native mechanism.
- **Only admins** can mutate providers and connections. The system **rejects
  invalid providers** at write time.
- Existing `config.yaml`-based setups **migrate seamlessly** on upgrade.

## 4. The Provider Concept (expanded)

A provider gains two axes beyond today's `connection + model + backend + tools`:

### 4.1 Context-window tier
A discrete tier: **`default` / `200k` / `1m`**.
- `default` — leave the backend/model default untouched.
- `200k` — pin the standard window (avoids the metered extended-context path).
- `1m` — explicitly allow the metered 1M extended window.

Meaningful for Claude backends. For backends where it does not apply (e.g.
codex), setting any tier other than `default` is **invalid** (see §6).

### 4.2 Reasoning effort
A user-facing superset: **`none` / `default` / `low` / `medium` / `high` /
`xhigh` / `max`**.
- `default` — leave the backend/model default (emit nothing).
- `none` — explicitly disable reasoning/thinking.
- `low … max` — increasing reasoning effort.

The **same enum is used for every backend**; the capability ruleset (§5) maps
each value onto whatever that backend/model actually obeys. The enum is
deliberately a superset so a single profile vocabulary spans all backends —
including ones whose native control is coarser (e.g. a future on/off "thinking"
toggle, where `none` maps to off and any other value maps to on).

## 5. Validity & the Capability Ruleset

Provider records are **fast-moving** (edited live via MCP, DB-backed). What makes
a provider *valid* is **slow-moving** and lives in a separate, admin-controlled
**capability ruleset**:

- **File-based, loaded once at startup.** It changes rarely (when a new
  model/backend or a new effort level appears), unlike the live profiles, so it
  intentionally does **not** hot-reload.
- It declares, **per backend (and per model where it matters)**: which
  context-window tiers are allowed, which effort levels are allowed, and **how
  each user-facing value maps to that backend's actual obeyed setting**.
- It is the single source of truth for provider validity — the validation rules
  are configuration, not hardcoded, so they can evolve without code changes.

### 5.1 Write-time validation behavior
On every provider create/update, the write is checked against the ruleset:

- **Inapplicable axis → reject.** If an axis does not apply to the chosen
  backend at all (e.g. a context tier other than `default` on a codex provider),
  the write **fails with a clear, specific error**, and nothing is stored.
- **Supported axis, unsupported level → store and map.** If the axis applies but
  the exact level is not natively distinct for that model (e.g. `xhigh` on a
  model whose ceiling is `high`), the **requested value is stored** and the
  ruleset **maps/floors it at runtime** to what the model obeys.
- **Effective-value feedback.** A successful write **echoes the effective
  resolved settings** (e.g. "stored effort = `xhigh`; effective for this model:
  `high`") so the admin sees exactly what will run.

Only valid (storable) providers ever reach the database.

## 6. Authorization Model

- **Mutation (create / update / delete)** of providers **and** connections →
  **admins only.**
- **Read:**
  - Providers — **any authenticated user** may list/get (workflow authors need
    aliases to reference).
  - Connection details — **admins only.**
  - **Secrets (API keys) are never exposed** to anyone through any surface.
- **Local / single-user mode** (auth disabled): the local caller is the
  **implicit admin** — no friction for the common desktop case. The admin gate
  bites only when auth/RBAC is enabled (shared/server deployments).
- **Mutation surface:** **MCP only.** Existing read-only CLI helpers (e.g.
  connection validation) remain; no new CLI *write* commands are added.

## 7. Live Recache

Providers are cached in memory for fast resolution. The cache is populated at
startup and **re-populated on every change**, so an admin's edit is visible to
the next workflow run **without a restart**.

## 8. Migration / Seeding

- On **first run** (empty providers table), the database is **seeded once** from
  the existing `config.yaml` profiles plus the built-in default profiles.
- **Thereafter the database is the sole source of truth**; `config.yaml`
  profile entries are ignored. (Non-profile settings in `config.yaml` are
  unaffected.)
- The result: an existing deployment upgrades with its current profiles intact,
  then manages them live via MCP.

## 9. Non-Goals (this feature)

- **Per-node** context/effort override (node > profile precedence). This feature
  is **provider-level only**; node-level override is a deferred follow-up.
- **opencode + ollama.com (DeepSeek) effort/thinking** runtime wiring. The
  effort enum (including `none`) is designed to accommodate a future on/off
  "thinking" backend, but only **claude-code** and **codex** get context/effort
  applied at runtime in this feature.
- **New model backends or adapters.** No new backend is built; the ruleset is
  merely structured to be forward-compatible.
- **CLI mutation commands.** MCP is the only mutation surface.
- Any change to how secrets are stored or encrypted (connections already handle
  this).

## 10. Success Criteria

1. An admin edits a provider's model, context tier, or effort via MCP and the
   **next workflow run reflects it with no restart**.
2. A **non-admin** attempt to mutate a provider or connection is **refused**; in
   local mode the single user can do everything.
3. A provider write with an **inapplicable axis is rejected** with a clear
   message; a supported-axis/unsupported-level write **succeeds and reports the
   effective mapped value**.
4. Context tier and effort **demonstrably reach** the claude-code and codex
   backends at runtime (the standard-vs-extended window and the effort level
   actually change what the subprocess requests).
5. An existing `config.yaml`-based deployment **upgrades with its profiles
   intact**, then manages them live; profiles **persist across restarts**.
6. Provider reads are available to any authenticated user; connection details
   are admin-only; **no secret is ever returned**.
