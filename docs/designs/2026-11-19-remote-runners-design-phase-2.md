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
| chi router, `/api/v1` | `adapter/http/server.go:NewRouterFromDeps` (:193), `/api/v1` group (:258) | Only `/api/v1/runners/connect` and Phase 3 `/api/v1/runners/transfers/*` use runner-key middleware outside user auth. Inspection, configuration and `/api/v1/runners/keys` stay under user auth with service admin checks for writes/keys. |
| User auth middleware | `adapter/http/auth_middleware.go:AuthMiddleware` (:80) | Bypassed only for connect/transfers. Operator REST and key management still use user auth; a runner key is refused on those routes and MCP. |
| WebSocket stack | `nhooyr.io/websocket`; precedent `adapter/http/system_stream_handler.go:HandleWebSocket` (:51) | Same library and patterns for the runner channel. |
| Server timeouts | `cmd/arbitron/main.go` `http.Server` (:1690; `WriteTimeout: 0`) | Already supports long-lived WebSockets. Transfer deadlines are Phase 3. |
| Token precedent | `domain/identity/token_format.go:GenerateToken` / `HashToken`, `token_service.go`, `adapter/sqlite/token_repo.go` | Pattern only. Runner keys are a **separate kind** with their own store and prefix. |
| Audit | `domain/audit/service.go:Service.Record` | `runner_key.create`, `runner_key.revoke`. |
| Harness adapters | `adapter/claudecli`, `adapter/codex`, `adapter/pi`, `adapter/opencode`, shared `claudecli.CommandRunner` (`runner.go:58`) | Reuse the existing adapters plus Phase 1 classification/version additions; advertise only verified remote capabilities. |
| Phase 1 seams | `StepRunner` port, ledger fence, placement, dispatcher, classifiers, version table, event log, source health monitor | Remote runner implements `StepRunner`; hub feeds registry liveness and health. |
| launchd install | `install.sh:render_launchd_plist` (:1730), `register_service_macos` (:1784), `select_install_type` (:198), `detect_state` (:1929) | New runner install scenario following the same style. |
| Release build | `Makefile` `dist` (:136), `DIST_PLATFORMS`, `SHA256SUMS` glob `arbitron_*` | Adds darwin `arbitron-runner` builds; the checksum glob is corrected. |
| Claude workspace restriction | `claudecli` `Config.WorkspaceRoot` (`cmd/arbitron/main.go:816`) | Construct a per-job config with the approved scratch root for hermetic chat or canonical workspace root for workspace jobs. The single WorkspaceRoot field does not accept an invented list of roots. |

## 3. Components

### 3.1 Wire protocol (`adapter/runnerproto`)
- **Job:** one shared definition of envelopes and message kinds, with encode and decode, used by both the server endpoint and the runner client. It is transport-independent, so a long-poll adapter could be added later without changing it.
- **Message set:**
  - **Server to runner:** dispatch, cancel, probe, ack, fenced.
  - **Runner to server:**
    - `hello`: bounded admission metadata (OS, protocol version, runner/harness versions, workspace root, supported execution capabilities), sources, and held or terminal-unacknowledged jobs with their durable operation IDs and generations.
    - `heartbeat`: about every 5 seconds, carrying telemetry and source health.
    - `accept` / `reject`, `output`, `result`.
  - Phase 3 adds transfer coordination; Phase 4 adds session export and import.
- **Invariants:**
  - Every message is idempotent, keyed by (runner, job, generation, sequence number).
  - Output is numbered per (job, generation). The runner keeps a bounded buffer of unacked output and resends it after a reconnect from the server's last-acked sequence.
  - A full output buffer applies backpressure or spools locally; it must not silently discard unacked frames. Persist server acknowledgments with accepted output, and retain terminal result/checkpoint references on the runner until durable server acknowledgment. Reconnect may replay terminal results, not just running jobs. The protocol and local spool producers are **UNRESOLVED DEPENDENCY RR-05**.
  - Protocol version negotiation happens in `hello`.

### 3.2 Runner endpoint (`adapter/http`)
- **Job:** upgrade `/api/v1/runners/connect`, authenticate with the runner-key middleware, run the machine-binding challenge, then hand the session to the hub. The handler holds no policy.
- **Key transport:** keys travel only in headers, never query parameters. Extra configurable headers (Cloudflare Access service tokens) pass through on the upgrade request.

### 3.3 Runner keys (`domain/runner` key service, SQLite adapter)
- **Job:** create, list and revoke runner keys, and bind each to a machine. Revocation requires an explicit confirmation acknowledging immediate disconnect and possible repeated effects, recommending drain first; MCP/REST carry the same acknowledgment as the dashboard.
  - **Create:**
    - Admin only. The name is unique and becomes the runner name.
    - The secret is returned **once**; only its hash is stored.
    - Audited as `runner_key.create`.
  - **Bind:** the first successful `hello` proves possession by signing a fresh server challenge, then atomically claims an unclaimed key with the machine public key (D8). Competing first claims admit exactly one machine. Every reconnect proves possession using a fresh challenge; key revocation and session admission are serialized.
  - **List:** admin only. Shows name, runner, claimed or unclaimed, created, and last used.
  - **Revoke:** admin only and audited as `runner_key.revoke`. The live session closes immediately and the runner is treated as lost, so its chat steps are re-placed now and Phase 3 extends this to workspaces.
- **Scope:** a runner key authorises only the runner channel (and Phase 3 transfers). It is never a user identity.
- **Interfaces:** MCP `runner_key_create`, `runner_key_list`, `runner_key_revoke`, plus REST equivalents under `/api/v1/runners/keys`, thin over the service.

### 3.4 Connection hub (`domain/runner` service)
- **Job:** own live runner sessions and turn protocol events into registry and ledger transitions.
  - **Liveness from heartbeats, not the socket:**
    - About 30 seconds without contact: **suspect**, meaning no new placements and nothing moves.
    - After a configurable grace, initially about 2 minutes: **lost**, meaning its jobs are failed over to the dispatcher.
  - **Compatibility on `hello`:** OS (remote execution requires Darwin), the protocol and runner version compatibility ranges, then harness/capability versions against the Phase 1 fixture-backed table, then the workspace root against the server's root.
    - Protocol, runner or harness version mismatch, or a non-macOS remote machine: the authenticated, machine-bound runner is **visible as incompatible, with its reason** and gets no work. Keep a minimal admission envelope readable across versions so protocol refusal can still be explained; do not depend on decoding unsupported execution frames.
    - Root mismatch: the runner is **chat-only, with its reason**.
  - **Reconcile `hello` against the ledger:** each held job is adopted (still leased to this runner), fenced (reassigned or cancelled) or killed (unknown).
  - **Leases:** heartbeats extend the leases of reported jobs. An offer not accepted within its TTL is re-offered with generation + 1.
  - **Runner-originated writes:** all go through the Phase 1 ledger fence.
  - **Health:** source health and headroom from heartbeats update the registry and run through the same health-transition rules as Phase 1, including logged-out alerts.
  - **Telemetry:** the latest telemetry plus a **short in-memory window of recent samples** per runner. This feeds Phase 5 sparklines and the Phase 3 load tiers. It is not persisted history (spec non-goal).
  - **Grace:** job TTLs and reconnect processing must not reassign accepted work before the liveness loss deadline. Temporary socket breaks retain assignment ownership; explicit revocation bypasses grace.
  - **Events:** publishes `runner.connected`, `runner.suspect`, `runner.lost`, `runner.reconnected` and `runner.incompatible`, and appends each to the pool event log.

### 3.5 Remote StepRunner (`adapter/runner/remote`)
- **Job:** implement Phase 1's `StepRunner` over the hub.
  - Send dispatch, await accept, stream output frames back as the step's chat stream, and return the result plus its classified outcome.
  - Translate runner loss into a typed "runner lost" outcome, which the dispatcher re-places.
- **Shared policy:** placement and the decorators retain their Phase 1 contracts. The dispatcher sees more runners in the registry and gains the typed runner-lost recovery branch; it closes/fences the old assignment and re-places without spending workflow retries. All implementations share this policy.

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
    - Adapters: darwin. A Linux-capable built-in telemetry adapter must report supported CPU, memory, load, disk, uptime and hardware fields; individual unsupported fields report "unavailable". A null adapter is only a fallback, never an exclusion of Linux servers or fabricated healthy telemetry. OS producers are **UNRESOLVED DEPENDENCY RR-06**.
  - **Identity and credentials:** read `~/.arbitron-runner/` config and the 0600 `runner.env` (server URL, key, optional `lr_` key, optional extra headers). Generate and hold the machine keypair (D8).
- **Rule:** vendor credentials never leave the runner; only the runner-scoped admission key crosses to the server. The server already holds its own built-in connection secrets; none are serialized into remote dispatch. Keep tool/sandbox restrictions identical to the existing adapters.

### 3.8 Install and release
- **Install:** an `install.sh` runner scenario installs `arbitron-runner` using launchd (`RunAtLoad`, `KeepAlive`) in the existing `dev.arbitron` style. **UNRESOLVED DEPENDENCY RR-05:** choose/verify the launch context so startup after machine reboot is automatic and uses the operator's local harness accounts; a login-only LaunchAgent must not be presented as proof of unattended boot startup. It writes the config skeleton and prints `runner.env` guidance, including the workspace-root requirement (D2).
- **Release:** `make dist` builds darwin `arbitron-runner` artifacts, and checksums and signatures cover them.

## 4. Data flow

1. **Connect:** the runner dials `/api/v1/runners/connect` with its key, the endpoint challenges for the machine signature, and the hub creates a session.
2. **Hello:** the hub checks compatibility, reconciles held jobs, then updates the registry (runner online, its sources) and the event log.
3. **Heartbeat:** about every 5 seconds. Telemetry and source health go to the registry and the telemetry window; job leases are extended.
4. **Chat step:** the decorated `ChatProvider` asks the dispatcher to place the step. Placement now chooses among built-in and remote runners for the model group. The ledger row is written with its generation; the remote `StepRunner` sends the dispatch; the runner accepts.
5. **On the runner:** the harness adapter runs in a scratch directory, output frames stream back, and the result arrives with its classified outcome.
6. **On the server:** the fence check runs, the executor validates the result and the fenced completion hook commits it (or re-places after a classified failure), and the engine writes `NodeExecution` with the remote runner and source.
7. **Runner loss:** heartbeats stop, the runner goes suspect and then lost. The dispatcher receives "runner lost" for each job and re-places it with generation + 1. If the old runner writes later, it gets `fenced`.

## 5. Data Provenance

Repository verification distinguishes existing producers from required implementation. **UNRESOLVED DEPENDENCY** means the named value/capability has no implemented producer or verified source yet; a proposed component, earlier phase or research observation does not resolve it. The RR identifiers group cross-phase plan tasks; every occurrence remains open until implementation/source verification. Upstream unresolved inputs propagate to their consumers.

| Leaf input | Consumer | Verified producer or explicit dependency |
|---|---|---|
| Key name, requested limit/labels and revocation acknowledgment | Key/admin service | Admin request payload; identity from `domain/identity/identity.go:FromContext` / `Identity.IsAdmin`; audit sink `domain/audit/service.go:Service.Record`. **UNRESOLVED DEPENDENCY RR-05:** implement validated key/operator request schemas and separate user-auth routes from runner admission. |
| Key secret/hash, machine public key, challenge/signature, key claim/revoke/last-use timestamps | Admission, key list | **UNRESOLVED DEPENDENCY RR-05:** no runner key generator, machine identity store or challenge/atomic-claim producer exists. `domain/identity/token_format.go:GenerateToken` / `HashToken` are user-token precedents only, not runner credentials. |
| Server URL, runner key, local LunaRoute key, extra access headers, workspace/scratch roots | Runner startup | **UNRESOLVED DEPENDENCY RR-05:** operator supplies `~/.arbitron-runner/runner.env` and runner config using installer guidance; no runner config reader/installer exists. Resolve/validate these explicit inputs without transmitting local vendor credentials. |
| Runner binary/version, OS, protocol and supported-version range | Admission/compatibility | Existing build convention `Makefile:DIST_LDFLAGS` stamps the server; **UNRESOLVED DEPENDENCY RR-05:** add runner build/version stamping, OS advertisement, protocol envelope and an explicit server/runner compatibility table. No such binary or negotiated range exists today. |
| Harness versions, local accounts, supported operation/model/backend/axes | Eligibility | Existing version logic `adapter/codex/version.go:compatibleVersion` is private. **UNRESOLVED DEPENDENCY RR-02/RR-03:** implement runner-local version/auth/capability probes and fixture-backed floors (including Claude and distinct Codex feature floors); linking adapters is not a capability advertisement. |
| Server absolute execution workspace root | Root check | `domain/config/config.go:WorkspaceConfig`, made absolute by `adapter/filesystem/workspace.go:NewWorkspaceManager` / `WorkspaceManagerAdapter.BasePath`; compare the resolved path, not an unresolved config literal. |
| Same execution-root availability on macOS with Linux-hosted server | Workspace eligibility | **UNRESOLVED DEPENDENCY RR-07:** root creation/permissions and macOS filesystem restrictions require a verified installer/layout solution; a Mac may not be able to create `/var/lib/...`. Such runners stay explicitly chat-only until resolved; local Linux execution remains usable. |
| CPU, memory use/pressure, load, free workspace disk, thermal state, uptime, cores/RAM, sample timestamps | Hub window, placement, Phase 5 | **UNRESOLVED DEPENDENCY RR-06:** no MachineTelemetry producer exists for any of these fields. Plans must choose and fixture-test actual macOS interfaces (including thermal/pressure) and Linux built-in equivalents, units, sample age and unavailable values. A null adapter does not satisfy the required live metrics by itself. |
| Sources, health/headroom/reset observations and last probe time | Hub/registry | **UNRESOLVED DEPENDENCY RR-02/RR-03:** Phase 1 inventory/prober/classifier producers plus runner-local integration; never infer available account capacity merely from CLI installation or elapsed reset. |
| Held jobs, terminal results, generations, output sequence and acknowledgment cursor | Reconnect/adoption | **UNRESOLVED DEPENDENCY RR-05:** implement runner job executor, local retained-result/output spool and replay protocol; server authoritative assignments come from Phase 1 ledger (**UNRESOLVED DEPENDENCY RR-01**). |
| Chat prompt/system prompt, exact model and model-evidence requirements | Remote execution | `domain/engine/executor_llm.go:LLMCallExecutor.Execute` and `domain/engine/child_input_preparer.go:LLMChildInputPreparer.Prepare`; binding metadata from `domain/provider/provider.go:ResolvedBinding`. **UNRESOLVED DEPENDENCY RR-02/RR-05:** a serialized allowlisted descriptor and local credential binding are required; do not serialize `RevealKey`, server credentials or the server's final environment. |
| Heartbeat/contact timestamps, admission/accept/result events, telemetry window, suspect/lost transitions | Hub, registry, recovery | **UNRESOLVED DEPENDENCY RR-05:** runner protocol/hub/client and measured server receipt times do not exist. Event publication and visibility filtering depend on Phase 1 RR-01/RR-16. |
| Heartbeat (~5 s), suspect (~30 s), loss (~2 min), offer TTL, output bounds and restart grace | Hub/client | **UNRESOLVED DEPENDENCY RR-04:** add validated config and fake-clock seams; leases must not trigger failover before the configured liveness grace. |
| launchd plist, startup guidance, release artifacts/checksums/signatures | Runner installation | Existing patterns `install.sh:render_launchd_plist`, `register_service_macos`, `Makefile:dist`; **UNRESOLVED DEPENDENCY RR-05:** add the runner scenario/artifacts rather than treating server packaging as already supporting them. |

### Deferred to plans
- **Composition-root wiring** in `cmd/arbitron/main.go`:
  - Construct the key service, hub, remote `StepRunner` and runner endpoint.
  - Register remote runners into the Phase 1 registry and dispatcher.
  - Mount only connect/transfers with runner-key middleware; keep operator and key-management routes under user auth in `adapter/http/server.go`.
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
- **Handshake failures close with a typed reason and do not register the runner:** a bad or revoked key, a wrong machine (failed signature), or a malformed admission envelope. A valid bound machine with unsupported protocol/version stays visible as incompatible (§3.4), even if its execution channel closes. No secret appears in logs.
- **An incompatible harness or runner version** leaves the runner visible with its reason and no new work.
- **An unaccepted dispatch** is re-offered with generation + 1. Duplicate dispatch delivery is harmless because the runner dedupes on (job, generation).
- **A stale-generation write** gets `fenced`, and the runner kills the local process.
- **A connection drop** is not runner loss. Only heartbeat timeouts move a runner to suspect or lost.
- **A runner key used on REST or MCP** is refused as unauthenticated.

## 8. Testing — STRICT TDD (all five categories required)
For every component and category below: write a failing test, run it to prove the intended failure, implement the minimum change, rerun to green, then refactor while green. Use fake clocks and fault injection at ownership/commit boundaries; do not substitute post-hoc tests for STRICT TDD.
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

- **Admission/reconnect regressions:** runner-key requests cannot reach operator/key REST routes; simultaneous first claims bind one machine; incompatible protocol/runner versions remain visible; non-Darwin remotes receive no work; bounded-output overflow never loses frames; terminal-result replay survives server restart; a healthy Linux built-in needs no runner setup.
