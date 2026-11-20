# Remote Runners — Design Phase 2: Remote runners — connectivity and keys

**Spec:** `docs/specs/2026-11-19-remote-runners-spec.md`
**Idea doc (context only):** `docs/ideas/remote-runners.md`
**Evidence:** `docs/evidence/remote-runners/research-transport.md`
**Depends on:** Phase 1 (pool model, registry, ledger and fence, placement, `StepDispatcher`, `StepRunner` port, classifiers, version table, event log).
**Cross-phase decisions:** see Phase 1 §0 (D1–D12). This phase realises D4 (remote LunaRoute key), D8 (machine-bound keys) and D12.

## 1. Purpose and scope

Let an operator add macOS runners that dial out to the server, advertise their capacity sources and telemetry, and execute **hermetic chat steps** placed by Phase 1's dispatcher. A chat fan-out then spreads across Macs and subscriptions.

In scope:
- The shared wire protocol.
- The server's runner WebSocket endpoint and connection hub.
- Liveness: suspect and lost.
- Compatibility checks.
- The remote `StepRunner`.
- Runner keys with machine binding, admin authorization and audit, over MCP and REST.
- The `arbitron-runner` binary with its domain and adapters.
- Darwin telemetry.
- launchd install and release packaging.
- Hermetic chat placement, plus re-placing a lost runner's chat steps.

Out of scope:
- Workspace transfer and placing workspace steps remotely, drain, and restart re-attach (Phase 3).
- Interactive session moves (Phase 4). Interactive steps stay on the built-in runner until then.
- Dashboard views and modals (Phase 5).
- A long-poll fallback, Linux or Windows runners, and self-update (spec non-goals).

## 2. Existing architecture this phase extends

| Seam | Location | How this phase uses it |
|---|---|---|
| chi router, `/api/v1` | `adapter/http/server.go:NewRouterFromDeps` (:193), `/api/v1` group (:258) | New `/api/v1/runners/*` subtree with its **own** runner-key middleware, outside user auth. |
| User auth middleware | `adapter/http/auth_middleware.go:AuthMiddleware` (:80) | Not used for runner routes. A runner key is refused on REST and MCP. |
| WebSocket stack | `nhooyr.io/websocket`; precedent `adapter/http/system_stream_handler.go:HandleWebSocket` (:51) | Same library and patterns for the runner channel. |
| Server timeouts | `cmd/arbitron/main.go` `http.Server` (:1690; `WriteTimeout: 0`) | Already supports long-lived WebSockets. Transfer deadlines are Phase 3. |
| Token precedent | `domain/identity/token_format.go:GenerateToken` / `HashToken`, `token_service.go`, `adapter/sqlite/token_repo.go` | Pattern only. Runner keys are a **separate kind** with their own store and prefix. |
| Audit | `domain/audit/service.go:Service.Record` | `runner_key.create`, `runner_key.revoke`. |
| Harness adapters | `adapter/claudecli`, `adapter/codex`, `adapter/pi`, `adapter/opencode`, shared `claudecli.CommandRunner` (`runner.go:58`) | Linked unchanged into `arbitron-runner`. |
| Phase 1 seams | `StepRunner` port, ledger fence, placement, dispatcher, classifiers, version table, event log, source health monitor | Remote runner implements `StepRunner`; hub feeds registry liveness and health. |
| launchd install | `install.sh:render_launchd_plist` (:1730), `register_service_macos` (:1784), `select_install_type` (:198), `detect_state` (:1929) | New runner install scenario following the same style. |
| Release build | `Makefile` `dist` (:136), `DIST_PLATFORMS`, `SHA256SUMS` glob `arbitron_*` | Adds darwin `arbitron-runner` builds; the checksum glob is corrected. |
| Claude workspace restriction | `claudecli` `Config.WorkspaceRoot` (`cmd/arbitron/main.go:816`) | On the runner, set to the runner's scratch root and workspace root. |

## 3. Components

### 3.1 Wire protocol (`adapter/runnerproto`)
- **Job:** one shared definition of envelopes and message kinds, with encode and decode, used by both the server endpoint and the runner client. It is transport-independent, so a long-poll adapter could be added later without changing it.
- **Message set:**
  - **Server to runner:** dispatch, cancel, probe, ack, fenced.
  - **Runner to server:**
    - `hello`: protocol version, runner and harness versions, workspace root, sources, and held jobs with their generations.
    - `heartbeat`: about every 5 seconds, carrying telemetry and source health.
    - `accept` / `reject`, `output`, `result`.
  - Phase 3 adds transfer coordination; Phase 4 adds session export and import.
- **Invariants:**
  - Every message is idempotent, keyed by (runner, job, generation, sequence number).
  - Output is numbered per (job, generation). The runner keeps a bounded buffer of unacked output and resends it after a reconnect from the server's last-acked sequence.
  - Protocol version negotiation happens in `hello`.

### 3.2 Runner endpoint (`adapter/http`)
- **Job:** upgrade `/api/v1/runners/connect`, authenticate with the runner-key middleware, run the machine-binding challenge, then hand the session to the hub. The handler holds no policy.
- **Key transport:** keys travel only in headers, never query parameters. Extra configurable headers (Cloudflare Access service tokens) pass through on the upgrade request.

### 3.3 Runner keys (`domain/runner` key service, SQLite adapter)
- **Job:** create, list and revoke runner keys, and bind each to a machine.
  - **Create:**
    - Admin only. The name is unique and becomes the runner name.
    - The secret is returned **once**; only its hash is stored.
    - Audited as `runner_key.create`.
  - **Bind:** the first successful `hello` with an unclaimed key records the machine's public key (D8). Later connections must sign a server challenge with the matching private key.
  - **List:** admin only. Shows name, runner, claimed or unclaimed, created, and last used.
  - **Revoke:** admin only and audited as `runner_key.revoke`. The live session closes immediately and the runner is treated as lost, so its chat steps are re-placed now and Phase 3 extends this to workspaces.
- **Scope:** a runner key authorises only the runner channel (and Phase 3 transfers). It is never a user identity.
- **Interfaces:** MCP `runner_key_create`, `runner_key_list`, `runner_key_revoke`, plus REST equivalents under `/api/v1/runners/keys`, thin over the service.

### 3.4 Connection hub (`domain/runner` service)
- **Job:** own live runner sessions and turn protocol events into registry and ledger transitions.
  - **Liveness from heartbeats, not the socket:**
    - About 30 seconds without contact: **suspect**, meaning no new placements and nothing moves.
    - After a configurable grace, initially about 2 minutes: **lost**, meaning its jobs are failed over to the dispatcher.
  - **Compatibility on `hello`:** the protocol version, then harness versions against the Phase 1 version table, then the workspace root against the server's root.
    - Protocol or harness version mismatch: the runner is **incompatible, with its reason** and gets no work.
    - Root mismatch: the runner is **chat-only, with its reason**.
  - **Reconcile `hello` against the ledger:** each held job is adopted (still leased to this runner), fenced (reassigned or cancelled) or killed (unknown).
  - **Leases:** heartbeats extend the leases of reported jobs. An offer not accepted within its TTL is re-offered with generation + 1.
  - **Runner-originated writes:** all go through the Phase 1 ledger fence.
  - **Health:** source health and headroom from heartbeats update the registry and run through the same health-transition rules as Phase 1, including logged-out alerts.
  - **Telemetry:** the latest telemetry plus a **short in-memory window of recent samples** per runner. This feeds Phase 5 sparklines and the Phase 3 load tiers. It is not persisted history (spec non-goal).
  - **Events:** publishes `runner.connected`, `runner.suspect`, `runner.lost`, `runner.reconnected` and `runner.incompatible`, and appends each to the pool event log.

### 3.5 Remote StepRunner (`adapter/runner/remote`)
- **Job:** implement Phase 1's `StepRunner` over the hub.
  - Send dispatch, await accept, stream output frames back as the step's chat stream, and return the result plus its classified outcome.
  - Translate runner loss into a typed "runner lost" outcome, which the dispatcher re-places.
- **Unchanged:** the dispatcher, placement and the decorators. The dispatcher just sees more runners in the registry.

### 3.6 Hermetic chat placement (extends the Phase 1 decorators and placement)
- **Job:** chat bindings carry **no workspace affinity**: `llm_call` and the spawn input preparer. Group resolution is an `interactive_agent` stream on server-local staging, so it keeps built-in affinity (Phase 1 §3.6). On a remote runner the chat request's working directory is replaced by a per-job scratch directory under the runner's scratch root. On the built-in runner it is unchanged.
- **Phase 2 boundary:** steps needing a workspace (`agent_call`, `script`, `tool_call`) and all interactive steps keep built-in-runner affinity until Phases 3 and 4.
- **Lost runner:** its interrupted chat steps are re-placed straight away (no workspace involved), with generation + 1. This does not consume `retries`.

### 3.7 Runner binary (`cmd/arbitron-runner`, `domain/runneragent`, `adapter/runneragent/*`)
- **Layering:** the same hexagonal rules as the server. The runner domain defines ports; adapters implement them; `cmd/arbitron-runner` is its composition root. There is no dashboard embed, no SQLite and no HTTP server.
- **Ports and their one job each:**
  - **Connection:** dial out, reconnect with jitter (cap about 30 seconds), and keep the output resend buffer. Adapter: WebSocket client over `runnerproto`.
  - **Job executor:** run dispatched chat jobs with the linked harness adapters, then classify. Process and agent jobs arrive in Phase 3, and session jobs in Phase 4.
  - **Source prober:** report local sources and health with the Phase 1 probes and classifiers:
    - Anthropic subscription: claude login.
    - OpenAI subscription: codex login.
    - LunaRoute: the runner's own `lr_` key from `runner.env`, which never leaves the machine.
  - **MachineTelemetry:** CPU, memory used and pressure, 1-minute load as a percentage of cores, free disk on the workspace volume, thermal state, uptime, cores and RAM.
    - Adapters: darwin. A **null adapter** reports "unavailable"; it is used by the built-in runner on a Linux server.
  - **Identity and credentials:** read `~/.arbitron-runner/` config and the 0600 `runner.env` (server URL, key, optional `lr_` key, optional extra headers). Generate and hold the machine keypair (D8).
- **Rule:** vendor credentials never leave the runner; the server never sees a secret.

### 3.8 Install and release
- **Install:** an `install.sh` runner scenario installs `arbitron-runner` as a launchd agent (`RunAtLoad`, `KeepAlive`) in the existing `dev.arbitron` style. It writes the config skeleton and prints `runner.env` guidance, including the workspace-root requirement (D2).
- **Release:** `make dist` builds darwin `arbitron-runner` artifacts, and checksums and signatures cover them.

## 4. Data flow

1. **Connect:** the runner dials `/api/v1/runners/connect` with its key, the endpoint challenges for the machine signature, and the hub creates a session.
2. **Hello:** the hub checks compatibility, reconciles held jobs, then updates the registry (runner online, its sources) and the event log.
3. **Heartbeat:** about every 5 seconds. Telemetry and source health go to the registry and the telemetry window; job leases are extended.
4. **Chat step:** the decorated `ChatProvider` asks the dispatcher to place the step. Placement now chooses among built-in and remote runners for the model group. The ledger row is written with its generation; the remote `StepRunner` sends the dispatch; the runner accepts.
5. **On the runner:** the harness adapter runs in a scratch directory, output frames stream back, and the result arrives with its classified outcome.
6. **On the server:** the fence check runs, the dispatcher completes or re-places, and the engine writes `NodeExecution` with the remote runner and source.
7. **Runner loss:** heartbeats stop, the runner goes suspect and then lost. The dispatcher receives "runner lost" for each job and re-places it with generation + 1. If the old runner writes later, it gets `fenced`.

## 5. Data Provenance

| Input | Consumer | Producer |
|---|---|---|
| Runner key secret and hash | Key service, middleware | **New** key generator in the `domain/runner` key service (pattern: `domain/identity/token_format.go`) |
| Machine public key and signature | Binding check | **New** keypair generated by the runner identity adapter on first start, stored in `~/.arbitron-runner/` |
| Runner name | Registry | The key name supplied by the admin at create |
| Server URL, key, `lr_` key, extra headers | Runner connection and source prober | `~/.arbitron-runner/runner.env`, written by the operator using the install guidance |
| Runner version | Compatibility | Linker-stamped version (`DIST_LDFLAGS -X main.version`, `Makefile`:19) for `arbitron-runner` |
| Harness versions on runner | Compatibility | Runner's local harness version probes (`adapter/codex/version.go:compatibleVersion`; **new** claude version probe) |
| Supported harness versions | Compatibility | Phase 1 version table (Codex floor from `minCodexVersion`; Claude floor is a Phase 1 **UNRESOLVED DEPENDENCY**) |
| Runner workspace root | Compatibility | Runner config file in `~/.arbitron-runner/` |
| Server workspace root | Compatibility | `domain/config.WorkspaceConfig` (`config.go:223`), consumed by `adapter/filesystem/workspace.go` |
| Source list and health on runner | Registry, placement | Runner source prober using the Phase 1 classifiers and probes |
| CPU, memory, load, disk, uptime, cores, RAM | Telemetry window, Phase 3 tiers, Phase 5 | **New** darwin `MachineTelemetry` adapter |
| Thermal state, memory pressure | Same | **UNRESOLVED DEPENDENCY:** no producer exists in the repo. The darwin adapter must source them from macOS interfaces, and the plan must choose that source and fixture-test it. |
| Telemetry on a Linux server (built-in) | Pool views | Null `MachineTelemetry` adapter reporting "unavailable" |
| Workspace root on a Linux-hosted server | Mac runners | **UNRESOLVED DEPENDENCY:** a Mac runner must be able to create the server's absolute root (for example `/var/lib/...`). Installer guidance must cover creating it; until then such a runner is chat-only by design (§3.4). |
| Heartbeat interval, suspect and lost thresholds, offer TTL | Hub | **New** server config values (defaults: 5 s, about 30 s, about 2 min) |
| Admin identity for key operations | Key service | `domain/identity` context identity; `IsAdmin` |

### Deferred to plans
- **Composition-root wiring** in `cmd/arbitron/main.go`:
  - Construct the key service, hub, remote `StepRunner` and runner endpoint.
  - Register remote runners into the Phase 1 registry and dispatcher.
  - Mount `/api/v1/runners/*` with runner-key middleware in `adapter/http/server.go`.
  - Register the MCP key tools in `adapter/mcp/register.go`.
- **Composition root for `cmd/arbitron-runner`:** wire the runner ports to their adapters and link the harness adapters.
- **SQLite migrations:** runner keys (hash, name, machine public key, timestamps, revoked) and runner liveness columns.
- **Config:** liveness thresholds, the offer TTL and the extra-header passthrough.
- **`install.sh` runner scenario, plist and tests** (bats and shellcheck). `Makefile` `dist`, `release` and checksum-glob changes.
- **Optional dependency migration** from `nhooyr.io/websocket` to `github.com/coder/websocket` (same API), only if it is chosen.

## 6. Integration seams
- **`StepRunner`:** the remote runner is a second implementation; dispatcher and placement code paths are shared.
- **Ledger fence:** the hub's single write path, so no per-handler generation checks.
- **`runnerproto`:** the only coupling between server and runner binary. Versioned in `hello`.
- **Pool event log and telemetry window:** consumed by Phase 5.
- **Phase 3 hook:** workspace and process jobs extend the same dispatch and accept message kinds and the runner's job executor.

## 7. Error handling
- **Handshake failures close with a typed reason and do not register the runner:** a bad or revoked key, a wrong machine (failed signature), an unsupported protocol, or a malformed `hello`. No secret appears in logs.
- **An incompatible harness or runner version** leaves the runner visible with its reason and no new work.
- **An unaccepted dispatch** is re-offered with generation + 1. Duplicate dispatch delivery is harmless because the runner dedupes on (job, generation).
- **A stale-generation write** gets `fenced`, and the runner kills the local process.
- **A connection drop** is not runner loss. Only heartbeat timeouts move a runner to suspect or lost.
- **A runner key used on REST or MCP** is refused as unauthenticated.

## 8. Testing — STRICT TDD (all five categories required)
Tests are written first for every component.
- **Happy path:** a chat fan-out of N steps spreads across the built-in runner and two in-memory remote runners, respecting each step limit and model-family rules.
- **Success:**
  - The first claim binds the key to the machine.
  - A reconnect after a dropped socket replays output with no gaps or duplicates.
  - A remote LunaRoute source places LunaRoute steps using the runner's own key.
- **Failure:**
  - A copied key used from a second machine (different keypair) is rejected.
  - An incompatible harness version is visible and gets no work.
  - A revoked key disconnects the runner, and its chat steps are re-placed.
  - A runner whose workspace root doesn't match is chat-only.
- **Error:**
  - Malformed frames are rejected.
  - A stale-generation result is fenced and the step keeps its newer outcome.
  - An unaccepted offer is re-offered with a bumped generation.
- **Edge-case:**
  - A heartbeat gap under 30 seconds changes nothing.
  - Suspect and lost transitions run against a fake clock.
  - A runner key is refused on REST and MCP.
  - Telemetry is "unavailable" on the null adapter.
  - Duplicate dispatch delivery is a no-op.
- **Contract tests:** the real server hub runs against an in-memory runner client over `runnerproto`, plus connection-kill chaos tests.
