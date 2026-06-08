# Installers P2 — Task 2: Shared config Q&A + `/etc/arbitron/config.yaml` + secrets env

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:test-driven-development.
> Write the bats tests FIRST, watch them fail, then implement. `shellcheck` clean.
> Extends `install.sh` from P1 and Task 1. Steps use `- [ ]` checkboxes.

Design refs: `docs/superpowers/specs/2026-06-05-installers-design.md` (§"Config Q&A
(shared)" / §"Secrets handling") and `docs/superpowers/specs/installers-design-phase-2.md`.

## Context

Delivers the **shared install Q&A** and the artifacts it produces:

- The shared question set: bind host/port; **auth model** (edge-auth + local mode
  **or** OIDC org mode); OIDC sub-questions + optional RBAC; **TLS model** (tunnel/proxy
  → `PublicBaseURL` + Secure cookies; direct app-TLS cert+key; autocert domain); the
  unattended-agent-tools (skip-permissions / RCE) prompt; service account name; data
  location.
- Writing `/etc/arbitron/config.yaml` with **absolute** paths and the chosen auth/TLS
  shape.
- Writing `/etc/arbitron/arbitron.env` at mode **0600, owned `root:arbitron`**, holding
  the API key (if any) and the auto-generated master key. The master key is generated
  silently (32 random bytes → base64) and never logged.

This task plugs into the `run_shared_install` orchestrator's config TODO from Task 1.

> **Parity requirement (P4):** This question set and its ordering MUST stay parallel to
> the Windows shared installer (`install.ps1`, P4). Keep prompts in shared, named
> functions (e.g. `prompt_shared_auth`, `prompt_shared_tls`, `prompt_oidc`) whose
> labels/order P4 can mirror verbatim. Note any wording in a comment block at the top of
> the Q&A section so P4 can copy it.
>
> ### CANONICAL shared Q&A order (source of truth — P2 ↔ P4 parity)
> This is THE ordered list; P2 implements it here and **P4 Task 2 / Task 4 assert
> against this exact list** (P4 Task 4's parity test references this block). Identical
> order AND semantics on both installers:
> 1. **Bind host / port.** (Default host `0.0.0.0`, port `8080`; edge+local later
>    forces host to `127.0.0.1`.)
> 2. **Auth model** — `edge-auth + local mode` **or** `OIDC org mode`.
> 3. **If OIDC:** issuer URL → client ID → client secret → redirect URL (derived
>    `publicbaseurl + /auth/callback`) → bootstrap admins → **optional RBAC** (group
>    claim → default group → department leads).
> 4. **TLS model** — `tunnel/proxy` (→ `PublicBaseURL` + Secure cookies + guidance)
>    **or** `direct app-TLS` (cert + key paths) **or** `autocert` (domain; forces
>    port 443).
> 5. **"Enable unattended agent tools?"** — the `--dangerously-skip-permissions`
>    prompt with the RCE-equivalent warning (asked explicitly, default No).
> 6. **Service account name** (default `arbitron`).
> 7. **Data location** (default shown — `/var/lib/arbitron` on Linux,
>    `%ProgramData%\Arbitron\data` on Windows).
> 8. **Master key** — auto-generated **silently** into the secrets store (no prompt).
>
> **P0 TLS invariants — encoded identically on both installers:** cert XOR autocert
> (never both, never neither when TLS enabled); autocert ⇒ `server.port == 443` (+
> `autocertcachedir`); local mode + TLS on a **non-loopback** host ⇒
> `server.tls.allowunauthenticated=true` only after an explicit extra confirmation.

## Config field reference (exact koanf keys — from `adapter/config/loader.go`)

Write these flattened keys (the loader is case-insensitive but use lower-case to match
the commented template):

- `server.host`, `server.port`, `server.publicbaseurl`
- `server.tls.enabled`, `server.tls.certfile`, `server.tls.keyfile`,
  `server.tls.autocertdomain`, `server.tls.autocertcachedir`,
  `server.tls.allowunauthenticated`
- `auth.mode` (`local` | `org`), `auth.bootstrapadmins` (CSV/sequence),
  `auth.oidc.issuerurl`, `auth.oidc.clientid`, `auth.oidc.clientsecret`,
  `auth.oidc.redirecturl`, `auth.session.cookiesecure`
- `auth.rbac.groupclaim`, `auth.rbac.defaultgroup`, `auth.rbac.leads.<dept>`
- `database.path` (`/var/lib/arbitron/arbitron.db`),
  `workspace.path` (`/var/lib/arbitron/workspaces`), logging path under `/var/log/arbitron`.

Secrets in `config.yaml` are avoided: API key and master key go to the env file. The
OIDC client secret ALSO goes to the env file as **`ARBITRON_AUTH_OIDC_CLIENTSECRET`** —
this IS bound by the loader: `adapter/config/loader.go` loads `env.Provider("ARBITRON_",
".", …)` which maps `ARBITRON_AUTH_OIDC_CLIENTSECRET` → the `auth.oidc.clientsecret` key
(uppercase→lowercase, `_`→`.`). So the client secret never needs to live in
`config.yaml`. (The earlier "verify the binding / fallback to config.yaml" caveat is
resolved: the env binding exists; use it.)

## P0 validation rules to respect (mirror them in prompts so the operator never hits a
## load-time failure):

- TLS enabled requires **either** cert+key **or** autocertdomain (not both, not neither).
- autocertdomain requires `autocertcachedir` **and** `server.port == 443`.
- `server.tls.enabled` with `auth.mode=local` on a **non-loopback** host is rejected
  unless `server.tls.allowunauthenticated=true`.
- org mode requires `auth.oidc.issuerurl`, `clientid`, `clientsecret`, `redirecturl`.

## Test plan (write first)

New bats file `tests/install/shared_config.bats`. Drive prompts by piping scripted
answers (or setting the answer-injection env vars the Q&A reads — match P1's prompt
mechanism). Mock `chown`/`chmod`/`install` and a master-key generator
(`openssl rand` / `head -c 32 /dev/urandom | base64`) so output is deterministic.

**Auth — edge+local:**
1. Choosing edge-auth + local mode writes `auth.mode: local` and **enforces loopback
   bind**: `server.host` is forced to `127.0.0.1` (assert it overrides any non-loopback
   the operator typed, with a `log_warn` explaining why), and `auth.oidc.*` keys stay empty.
2. Edge+local sets `server.publicbaseurl` to the operator's public URL and
   `auth.session.cookiesecure: true` (cookies Secure behind the tunnel).

**Auth — OIDC org:**
3. OIDC answers produce `auth.mode: org` with `auth.oidc.issuerurl`,
   `auth.oidc.clientid`, `auth.oidc.redirecturl` set to the typed values.
4. `auth.oidc.clientsecret` is **not** in `config.yaml` in plaintext when the env-var
   binding exists (assert it appears in `arbitron.env` instead); bootstrap admins land
   in `auth.bootstrapadmins`.
5. Optional RBAC: when enabled, `auth.rbac.groupclaim` + `auth.rbac.defaultgroup` are
   written and a department-lead answer produces an `auth.rbac.leads.<dept>` entry; when
   RBAC declined, those keys are absent/empty.
6. OIDC `redirecturl` is validated/derived as `publicbaseurl + /auth/callback` (assert
   the derived default matches, and a mismatch warns).

**TLS — three models:**
7. Tunnel/proxy model: `server.tls.enabled: false`, `server.publicbaseurl` set,
   `auth.session.cookiesecure: true`, and a guidance block is printed (assert stdout
   contains "tunnel" / "proxy" guidance; installer never configures the tunnel).
8. Direct app-TLS model: `server.tls.enabled: true` with `certfile`/`keyfile` set to the
   typed absolute paths; `autocertdomain` empty.
9. Autocert model: `server.tls.enabled: true`, `autocertdomain` set,
   `autocertcachedir` set (default under `/var/lib/arbitron`), and the prompt **forces
   `server.port: 443`** (or refuses to proceed) to satisfy P0's port-443 rule.
10. Local-mode + direct-TLS on a non-loopback host: the installer either keeps the bind
    loopback (edge+local always loopback) OR, if the operator forces a public bind with
    TLS in local mode, sets `server.tls.allowunauthenticated: true` only after an
    explicit extra confirmation (assert the confirmation is required and the key is set).

**Skip-permissions / RCE prompt:**
11. The unattended-agent-tools prompt is shown with an RCE-equivalent warning (assert
    stdout contains "remote code execution" / "RCE" wording) and is asked explicitly
    (never silently defaulted); the answer is recorded (config/env per P1's convention).

**Secrets env:**
12. `/etc/arbitron/arbitron.env` is created mode **0600** owned **root:arbitron**
    (assert `chmod 600` and `chown root:arbitron` argv against that path).
13. A master key is generated and written as a base64 value to `arbitron.env`
    (`ARBITRON_MASTER_KEY=`), and the generated value is **never** echoed to stdout
    (assert the base64 string does not appear in captured stdout).
14. When the operator supplies an Anthropic API key, it lands in `arbitron.env`
    (`ARBITRON_*` key per P1 convention), not in `config.yaml`.

**Paths:**
15. `config.yaml` uses absolute system paths: `database.path: /var/lib/arbitron/...`,
    `workspace.path: /var/lib/arbitron/workspaces`, log path under `/var/log/arbitron`.

16. **Reconfigure pre-fill:** when an existing `/etc/arbitron/config.yaml` is present,
    prompts default to the existing values (assert a prompt shows the existing host/port
    as default). Existing `arbitron.env` master key is preserved, not regenerated.

17. **shellcheck clean.**

## Implementation steps

- [ ] **Step 1:** Add a parity-comment header above the shared Q&A listing the question
  order for P4 to mirror.
- [ ] **Step 2:** `prompt_shared_bind()` — host (default `0.0.0.0` or existing) + port
  (default 8080 or existing). Note edge+local will override host to loopback later.
- [ ] **Step 3:** `prompt_shared_auth()` — choose `edge+local` or `oidc`.
  - edge+local: set `AUTH_MODE=local`; force `HOST=127.0.0.1` with a `log_warn`;
    require a public base URL for `server.publicbaseurl`; `COOKIE_SECURE=true`.
  - oidc: set `AUTH_MODE=org`; call `prompt_oidc`.
- [ ] **Step 4:** `prompt_oidc()` — issuer URL, client ID, client secret (→ env), bootstrap
  admins (CSV), derive `redirecturl = publicbaseurl + /auth/callback` (prompt to accept
  /override). Then `prompt_rbac` (optional): group claim, default group (`default`),
  optional department leads → `auth.rbac.leads.<dept>`.
- [ ] **Step 5:** `prompt_shared_tls()` — choose tunnel/proxy | direct cert/key |
  autocert.
  - tunnel/proxy: `TLS_ENABLED=false`; ensure `publicbaseurl` + `cookiesecure=true`;
    print operator guidance (never provision a tunnel).
  - direct: `TLS_ENABLED=true`; prompt cert path + key path (absolute).
  - autocert: `TLS_ENABLED=true`; prompt domain; set `autocertcachedir` default under
    `/var/lib/arbitron/autocert`; force `PORT=443`.
  - Guard: local mode + TLS on non-loopback → require explicit confirm then set
    `allowunauthenticated=true` (mirrors P0 escape hatch).
- [ ] **Step 6:** `prompt_skip_permissions()` — reuse P1's RCE-warning prompt verbatim.
- [ ] **Step 7:** `prompt_service_account()` — default `arbitron`; export `ARB_SVC_USER`
  (consumed by Task 1's account/ownership steps — ensure ordering: this prompt runs
  before account creation, or account creation re-reads the value).
- [ ] **Step 8:** `prompt_data_location()` — default `/var/lib/arbitron`; derive
  `database.path`, `workspace.path`.
- [ ] **Step 9:** `generate_master_key()` — 32 random bytes → base64
  (`head -c 32 /dev/urandom | base64` or `openssl rand -base64 32`); never log it.
- [ ] **Step 10:** `write_shared_config()` — emit `/etc/arbitron/config.yaml` from the
  collected variables using the exact koanf keys above; `chmod 0644 root:root` (no
  secrets inside — the OIDC client secret goes to `arbitron.env` as
  `ARBITRON_AUTH_OIDC_CLIENTSECRET`, which the loader binds, so config.yaml stays
  secret-free).
- [ ] **Step 11:** `write_shared_env()` — emit `/etc/arbitron/arbitron.env` with
  `ARBITRON_MASTER_KEY`, optional API key, optional OIDC client secret; then
  `chmod 0600` + `chown root:arbitron`.
- [ ] **Step 12:** Reconfigure: pre-fill prompt defaults from an existing config.yaml;
  preserve an existing master key from `arbitron.env`.
- [ ] **Step 13:** Wire all of the above into `run_shared_install` at the Task 1 config
  TODO, ordered: account-name prompt → (Task 1 account/layout) → Q&A → write config →
  write env. Iterate until bats pass + `shellcheck` clean.

## Files

- Modified: `install.sh` — shared Q&A functions, `write_shared_config`,
  `write_shared_env`, `generate_master_key`, reconfigure pre-fill; wired into
  `run_shared_install`.
- Created: `tests/install/shared_config.bats`.
- Reuse: P1 prompt/UI helpers and the existing-config reader.

## Acceptance criteria

- [ ] Shared Q&A asks: bind host/port, auth model (edge+local | OIDC), OIDC details +
  optional RBAC, TLS model (tunnel | direct | autocert), skip-permissions (RCE warning),
  service account name, data location — in an order documented for P4 parity.
- [ ] edge+local **forces loopback bind** and writes `auth.mode: local`.
- [ ] OIDC writes `auth.mode: org` + issuer/client-id/redirect; client secret + master
  key + API key go to `arbitron.env`, not `config.yaml` (where the env binding exists).
- [ ] TLS models map correctly to `server.tls.*` and respect P0 validations (cert XOR
  autocert; autocert ⇒ port 443 + cachedir; local+TLS+non-loopback ⇒
  `allowunauthenticated` only after explicit confirm).
- [ ] `/etc/arbitron/arbitron.env` is **0600 root:arbitron**; master key generated
  silently (32B→base64) and never logged; preserved on reconfigure.
- [ ] `config.yaml` uses absolute `/var/lib/arbitron` + `/var/log/arbitron` paths.
- [ ] bats green; `shellcheck install.sh` clean.

## Dependencies

- **Task 1 (this phase):** `run_shared_install` orchestrator, system layout, service
  account, `ARB_SVC_USER`, path globals.
- **P0 (merged):** `server.tls` block + its fail-fast validations; the `--config` target
  path. These dictate the TLS prompt guards.
- **P1 tasks:** prompt/UI helpers, the RCE skip-permissions prompt wording, and the
  existing-config reader (reused for reconfigure pre-fill).
- **P4 (parallel):** keep this question set/order identical to the Windows shared
  installer.
