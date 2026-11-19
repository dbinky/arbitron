# Arbitron runners: transport research

Date: 2026-11-19. Scope: choosing the server<->runner transport for Arbitron "runners" (outbound-dialing Go binaries; LAN Mac minis first, later Cloudflare Tunnel/Access and corporate proxies). This is research only; the repo was not changed.

Confidence legend: **[V]** verified this session against the cited primary source; **[K]** from prior knowledge of the source/code, not re-fetched today (treat as high-probability, re-check before you rely on an exact number).

---

## 1. Survey

| System | Transport | Who dials | Heartbeat / lease model | How bulk data moves | Source |
|---|---|---|---|---|---|
| **GitHub Actions self-hosted runner** | Plain HTTPS long-poll. Listener creates a session (`POST /sessions`), then `GET /message`, which the server holds open up to **50 s**; it returns `202` when empty and the runner loops. Newer runners use the "Broker" API (`broker.actions.githubusercontent.com`). [V] | Runner, outbound 443 only; no inbound connections needed [V] | Job **lease**: the runner calls `renewjob` every 60 s, and the response carries `lockedUntil`, about 10 min ahead. If the runner doesn't acquire the job within ~2 min of the message, GitHub cancels it. [V, via Depot's reverse-engineering] | Logs, artifacts and caches go to **separate endpoints**: `results-receiver.actions.githubusercontent.com` + `*.blob.core.windows.net` (blob storage), never the job channel [V] | docs.github.com/en/actions/reference/runners/self-hosted-runners ; docs.github.com/en/enterprise-server@3.13/actions/concepts/runners/communicating-with-self-hosted-runners ; depot.dev/blog/github-actions-runner-architecture-part-1-the-listener |
| **GitLab Runner** | HTTPS polling: `POST /api/v4/jobs/request` every `check_interval` (default 3 s). Workhorse long-polls that endpoint (`-apiCiLongPollingDuration`, default **50 s**) and wakes it from a Redis key. [V] Logs go up as incremental `PATCH /jobs/:id/trace` with `Content-Range`; status goes up via `PUT /jobs/:id`. [K] | Runner | No socket heartbeat. Each trace/status update refreshes the job, and a per-job token authorises calls for that job only. Stuck jobs time out server-side. [K] | Artifacts upload through Workhorse to object storage (separate `url`, distinct from `clone_url`). Cache goes to S3/GCS directly from the runner. [V/K] | docs.gitlab.com/runner/configuration/advanced-configuration/ ; gitlab-runner `network/gitlab.go` |
| **Buildkite agent** | HTTPS to Agent API (`agent-edge.buildkite.com/v3`). `ping-mode` is `auto` (default, **prefers streaming, falls back to polling**), `poll-only` or `stream-only`. There is an option to disable HTTP/2. [V] | Agent; no port forwarding or inbound firewall [V] | Periodic ping/heartbeat polling. `disconnect-after-idle-timeout` is optional. [V] | Log chunks stream to the API. Artifacts go to Buildkite storage (multipart) **or the customer's bucket** (S3/GCS/Azure). [V] | buildkite.com/docs/agent/v3 ; buildkite.com/docs/agent/v3/configuration |
| **Temporal workers** | gRPC (HTTP/2) **unary long-poll**: `PollWorkflowTaskQueue` / `PollActivityTaskQueue`, held about 60–70 s. Workers adjust the number of pollers based on poll timeouts. [V for long-poll; K for duration] | Worker | **Activity Heartbeat Timeout** is the max gap between heartbeats; when it's exceeded, the task fails and is retried per policy, with the last heartbeat payload passed on. Heartbeats are throttled client-side (min(0.8×timeout, 30 s default), max 60 s). Start-to-Close and Schedule-to-Start timeouts also apply. [V] | Payloads are capped (~2 MB/4 MB). Big data is passed by reference (claim-check to blob storage). [K] | docs.temporal.io/develop/worker-performance ; docs.temporal.io/encyclopedia/detecting-activity-failures |
| **Nomad clients** | Custom msgpack RPC over a multiplexed (yamux) TCP connection to servers. Clients pull allocations via **blocking queries** (long-poll). [K] | Client | Server-assigned **heartbeat TTL**: `num_clients / max_heartbeats_per_second`, randomised up to 2×, floor `min_heartbeat_ttl` 10 s, plus `heartbeat_grace` 10 s. Missed heartbeat: node "down", allocs `lost`/`disconnected` (`disconnect.lost_after`). After a leader election, **`failover_heartbeat_ttl` = 5 min** gives all clients time to re-heartbeat. [V] | Artifacts are fetched by the client from URLs (go-getter: HTTP/S3/git), not over RPC. [K] | developer.hashicorp.com/nomad/docs/configuration/server |
| **Concourse workers** | The worker dials the **TSA**, a purpose-built SSH server, and **reverse-tunnels** its Garden (7777) and Baggageclaim (7788) ports through it, which registers them with the ATC. [V] | Worker dials TSA; the ATC then talks *to* the worker over the tunnel | The worker keeps re-advertising. If it stops, it's `STALLED`. `fly prune-worker` removes it. [V] | Volumes stream between workers through **Baggageclaim** (HTTP), not the control channel; P2P streaming is optional. [K] | concourse-ci.org/docs/internals/ |
| **Drone runners** | HTTP RPC v2: the runner long-polls the server for a stage, then posts updates and log lines. [K, medium confidence] | Runner | Stage status updates; the server marks stages that stop reporting as errored. [K] | Logs are uploaded to the server or a log store; no artifact channel. [K] | github.com/drone/runner-go (client/http.go) |
| **Woodpecker agents** | **gRPC** (`WOODPECKER_SERVER` default `:9000`, separate port), keepalive pings (`KEEPALIVE_TIME`, `KEEPALIVE_TIMEOUT` 20 s), TLS optional, reconnect retry (`RETRY_TIMEOUT` 2 m). [V] | Agent | RPC API: `RegisterAgent`, `ReportHealth` (periodic), `Next()` (**blocks until assigned**, i.e. a gRPC long-poll), `Init`, `Wait()` (watches for cancel), `Update`, `EnqueueLog`, **`Extend()` ("extends workflow timeout … so queue does not reschedule it as retry")**, `Done`. This is an explicit lease. [V, source] | Logs go over RPC. No artifact transfer (users use plugins/S3). [K] | woodpecker-ci.org/docs/administration/configuration/agent ; github.com/woodpecker-ci/woodpecker/blob/main/rpc/peer.go |
| **Jenkins inbound agents** | Historically, Remoting (JNLP) over a **dedicated TCP port**. **JEP-222** added **WebSocket** transport on the normal HTTP(S) URL because "the port serves non-HTTP traffic" and L7 proxies and ingress handle only HTTP. Nginx needed ping/pong every <60 s, GKE needed service tweaks, and Apache was untested. [V] | Agent | Remoting ping thread; the channel closes and the agent reconnects. [K] | Artifacts are archived to the controller over Remoting or to external stores (artifact-manager-s3). [K] | github.com/jenkinsci/jep/blob/master/jep/222/README.adoc |
| **Kubernetes kubelet** | HTTPS (HTTP/2) to the API server: watches (long-lived streaming GETs) plus writes. [K] | Kubelet (the API server also dials kubelet for exec/logs) | **Lease** object per node in `kube-node-lease`, renewed ~10 s. `node-monitor-grace-period` ~40 s leads to `Ready=Unknown`, then taint-based eviction. [K] | Images come from registries. Volumes use CSI. Never on the API channel. [K] | kubernetes.io/docs/concepts/architecture/nodes/ (Node heartbeats) |
| **Tailscale control plane** | The client upgrades an HTTPS connection to a Noise (ts2021) channel, then a **streaming long-poll `MapRequest`**: one long-lived HTTP response that pushes netmap deltas, with keepalives. DERP relays also use HTTP Upgrade on 443. [K] | Node | Map-stream keepalives. The node is marked offline when the stream drops. [K] | Data plane is separate (WireGuard P2P/DERP). [K] | tailscale.com/blog/how-tailscale-works ; tailscale/tailscale `control/controlclient` |
| **NATS** (as an option) | Pub/sub over TCP, or **WebSocket** for clients and leaf nodes. JetStream gives ack/redelivery semantics. [K] | Client | Ping/pong. JetStream `AckWait` acts as a redelivery lease. [K] | JetStream Object Store (chunked). [K] | docs.nats.io |

### Patterns that recur

1. **The worker always dials out.** Every system above does this except the original Jenkins/Concourse designs, which still dial out but then tunnel inbound traffic.
2. **The control channel is small messages; bulk data has its own path.** GitHub uses blob storage, GitLab uses Workhorse to object storage, Buildkite uses S3, Temporal uses claim-checks, Nomad uses go-getter, and Concourse uses Baggageclaim. None pushes hundreds of MB through the dispatch channel.
3. **Liveness is a lease with a TTL, not a TCP connection.** GitHub has `lockedUntil`, Woodpecker has `Extend()`, Temporal has Heartbeat Timeout, Nomad has heartbeat TTL plus grace, and k8s has node Lease. Losing the socket does not mean losing the job. The lease only expires after the TTL.
4. **Post-restart grace.** Nomad's `failover_heartbeat_ttl` (5 min) exists so a control-plane restart does not mass-evict healthy workers.
5. **Long-poll windows sit around 50–60 s** (GitHub 50 s, GitLab 50 s, Temporal ~60–70 s), comfortably under typical proxy idle limits and under Cloudflare's 125 s proxy read timeout.
6. **Projects that started on raw TCP or custom ports migrated to HTTP-native transports** to get through proxies: Jenkins added WebSocket (JEP-222), and Buildkite added streaming with a poll fallback.

---

## 2. Cloudflare Tunnel / Access and proxy constraints (verified)

| Fact | Source |
|---|---|
| **gRPC over Cloudflare Tunnel: "Only private subnet routing is supported. Public hostname deployments are not currently supported."** | developers.cloudflare.com/network/grpc-connections/ [V] |
| **Cloudflare Access does not cover gRPC through the reverse proxy.** Access *ignores* gRPC traffic when gRPC is enabled, and Cloudflare recommends disabling gRPC on Access-protected origins. | same [V] |
| gRPC (where allowed) requires port 443, TLS, HTTP/2 via ALPN and the zone gRPC toggle. WAF inspects headers only. | same [V] |
| **WebSockets are supported on all plans.** Cloudflare closes idle WS after an unspecified idle period, and **"may restart servers, which terminates WebSockets connections"** during deploys. Its guidance: app-level keepalive and reconnect logic. (Argo is not WS-compatible.) | developers.cloudflare.com/network/websockets/ [V] |
| Long-lived Cloudflare One connections "can last up to eight hours". The troubleshooting docs suggest app keepalives and `protocol: http2` for cloudflared if idle connections drop. | developers.cloudflare.com/cloudflare-one/.../troubleshoot-tunnels/common-errors/ [V] |
| **Tunnel buffers proxied responses by default.** `Content-Type: text/event-stream` makes cloudflared stream them. This matters for SSE and for any streamed HTTP response. | same [V] |
| **Proxy Read Timeout = 125 s** (error 524). Enterprise can raise it to 6000 s. Long-poll holds must stay well under this. | developers.cloudflare.com/support/troubleshooting/http-status-codes/cloudflare-5xx-errors/error-524/ [V] |
| **Max request body: 100 MB Free/Pro, 200 MB Business, 500 MB Enterprise** (Enterprise can self-raise). Larger requests get 413. A single-PUT upload of a several-hundred-MB workspace will fail through Cloudflare. | developers.cloudflare.com/workers/platform/limits (request body by plan) [V] |
| Access **service tokens**: `CF-Access-Client-Id` + `CF-Access-Client-Secret` headers, or a single configurable header. They need a **Service Auth** policy, otherwise Access sends a login redirect. Duration is set at creation (example: 8760 h). | developers.cloudflare.com/cloudflare-one/access-controls/service-credentials/service-tokens/ [V] |

Corporate proxies [K, general practice]:
- Explicit proxies (`HTTPS_PROXY` + `CONNECT`) pass anything over TLS, including WS and h2.
- **TLS-intercepting proxies** (Zscaler, Netskope, Bluecoat) often downgrade to HTTP/1.1, break HTTP/2 trailers (which kills gRPC: status travels in trailers), and sometimes block or idle-kill WebSocket upgrades.
- Plain HTTPS request/response (long-poll) is the most universally compatible.
- WebSocket is widely but not universally supported.
- gRPC is the least compatible.

Go's `net/http` `ProxyFromEnvironment` covers `HTTPS_PROXY` for plain HTTPS and for `coder/websocket` (it dials through an `http.Client`). grpc-go also honours `HTTPS_PROXY` via CONNECT, but that doesn't help with intercepting proxies.

---

## 3. Transport comparison for Arbitron

Requirements recap:
- Server-to-runner: dispatch, cancel, drain and probe, with low latency.
- Runner-to-server: 5 s heartbeats with telemetry, status and **token-streamed output** for interactive sessions.
- Bulk transfer of hundreds of MB.
- Resumable across restarts, sleep/wake and Wi-Fi blips.
- Fencing.
- The server stays the source of truth for slot counts.

| Criterion | HTTPS long-poll + POST (GitHub/GitLab) | WebSocket (one duplex conn) | gRPC bidi streaming | SSE (down) + POST (up) |
|---|---|---|---|---|
| Through CF Tunnel public hostname | Yes. Hold must be <125 s (use ~50 s). | **Yes** (all plans) | **No** (private-network routing only) | Yes, *only* if the response is `text/event-stream` (else buffered). 125 s read timeout means a comment ping every ~15–30 s. |
| Cloudflare Access auth | Yes (service-token headers on every request) | Yes (headers on the upgrade request) | **No: Access ignores gRPC** | Yes |
| TLS-intercepting corporate proxies | **Best** | Usually works, sometimes blocked or idle-killed | Worst (h2 trailers) | Good, but some proxies buffer streaming responses |
| Server-to-runner latency (cancel/probe) | Immediate if the poll is parked. Gaps happen during reconnects and while the runner is busy POSTing, unless a poll is always parked. | Immediate | Immediate | Immediate |
| Token streaming up (runner to dashboard) | Batched POSTs (e.g. every 100–250 ms). Fine, but chatty: one request per batch. | Natural, cheap framing | Natural | Batched POSTs (same as long-poll) |
| Reconnect semantics | Implicit: every request is independent, no "connection state" to lose. Easiest to make correct. | Must reconnect and re-sync explicitly. CF *will* drop WS on deploys. | Must reconnect and re-sync. Built-in keepalive and backoff. | Down-stream needs `Last-Event-ID` resume. Up-stream is stateless. |
| Ops/debuggability | curl-able, normal access logs, normal middleware | One log line per connection. Needs ping/pong (Jenkins: <60 s for nginx). | Separate listener or h2c mux. grpcurl. Protobuf toolchain. | curl-able |
| Go maturity | stdlib | `github.com/coder/websocket` (successor of `nhooyr.io/websocket`, **already used by Arbitron** for dashboard streams) or gorilla | grpc-go (excellent) | stdlib (needs `http.Flusher`) |
| Fit with existing chi server | Same port, same middleware and auth | Same port. Arbitron already runs WS on it with `WriteTimeout: 0`. | Effectively a separate listener/port, or h2c tricks. Bypasses chi auth middleware. | Same port |
| Connection count per runner | 1 parked poll + short POSTs | 1 | 1 | 1 SSE + short POSTs |

**Conclusion of the comparison:**
- **gRPC is ruled out** by the Cloudflare requirement. Tunnel public hostnames don't support it, and Access doesn't protect it. That holds regardless of grpc-go's quality.
- Between **WebSocket** and **long-poll**: WebSocket fits the interactive-streaming and push-cancel needs better, and Arbitron already runs it on the same chi listener.
- Long-poll is the most proxy-proof, and its statelessness makes correctness easier.
- Buildkite's `ping-mode: auto` shows mature systems ship *both* behind one protocol.

---

## 4. Lease / heartbeat / fencing patterns

- **Job lease with TTL** (GitHub `lockedUntil`, Woodpecker `Extend`, Temporal heartbeat timeout, k8s Lease). Ownership is a server-side DB row with an expiry. Heartbeats extend it. The connection is merely how heartbeats travel.
- **Fencing token** (Kleppmann, "How to do distributed locking", martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html [K]). Every grant of a lease gets a monotonically increasing number, and every write by the holder carries it. The resource (here, the Arbitron server, which also *is* the lock service) rejects writes with a stale token. Because Arbitron's server is both lock manager and storage, fencing is simple: `UPDATE ... WHERE job_id=? AND lease_epoch=?`.
- **Graceful TTL vs grace.** Nomad pairs a short TTL with a grace period, and uses a much longer post-failover TTL so a restarted control plane doesn't reassign everything.
- **Dispatch delivery.** Every system here is at-least-once on the wire, with an idempotent claim or ack step. GitHub: message, then "acquire job" within ~2 min, else cancelled. GitLab: `jobs/request` returns the job atomically assigned to that runner. Temporal: the poll response *is* the assignment, and the task is re-dispatched when its timeout expires. Exactly-once *execution of side effects* (e.g. `git push`, an LLM spend) isn't achievable in general. What you can get is exactly-once *acceptance of results* per (job, epoch).

---

## 5. Recommendation for Arbitron v1

### 5.1 Control channel: one outbound WebSocket per runner, on the existing chi listener

`GET /api/v1/runners/connect` (upgrade), authenticated by the per-runner key in a header (`Authorization: Bearer`). When behind Cloudflare Access, the runner also sends the CF service-token headers; make the header set configurable.

- **Frames.** JSON envelopes `{type, id, job_id?, epoch?, seq?, payload}` to start. This is consistent with the existing dashboard WS, and you can switch to protobuf later if telemetry volume matters.
- **Server-to-runner:** `dispatch`, `cancel`, `drain`, `probe`, `ack`.
- **Runner-to-server:** `hello` (runner id, version, capabilities, **in-flight jobs with epochs**), `heartbeat` (every 5 s with telemetry and per-source health), `accept`/`reject`, `status`, `output` (seq-numbered chunks), `result`.
- **Keepalive:**
  - WS ping every ~15–20 s, under nginx's and Jenkins-observed 60 s limits and Cloudflare's unspecified idle cut.
  - The 5 s heartbeat already keeps the connection non-idle.
  - The server treats a missing heartbeat for ~3× the interval as "connection suspect", *not* "job lost".
- **Reconnect:** exponential backoff with jitter, cap ~30 s. Expect drops on CF deploys, sleep/wake and Wi-Fi changes.
- **Output streaming:**
  - The runner seq-numbers output per (job, epoch) and keeps a bounded ring buffer of unacked chunks.
  - On reconnect it resends from the server's last-acked seq, which the server returns in its `hello` reply.
  - The server dedupes by seq.
  - This gives lossless, ordered token streams across blips. The server fans out to the dashboard over its existing WS/event bus.
- **Design the message protocol to be transport-independent.** Every operation should be an idempotent request keyed by (runner, job, epoch, seq). Then a **long-poll fallback** becomes a thin adapter later: `GET /runners/poll` for server-to-runner messages, `POST /runners/events` for the rest. Ship that when the first corporate-proxy user hits a WS block. This is the Buildkite `auto` model. Don't build it in v1 for the LAN.

### 5.2 Job ownership: DB-backed lease + fencing epoch; server counts slots

- Assignment is a transaction:
  - Reserve a slot on the runner.
  - Insert `job_assignment(job_id, runner_id, epoch=prev+1, state=offered, lease_until=now+offer_ttl)`.
  - Then send `dispatch`.
- The runner replies `accept(job_id, epoch)`, which moves the row to `running` and sets `lease_until=now+lease_ttl` (e.g. 30–45 s). An unaccepted offer expires (e.g. 30 s) and the job is re-offered with **epoch+1**. Duplicate `dispatch` delivery is harmless because the runner dedupes on (job_id, epoch).
- Heartbeats extend the leases of all jobs the runner reports. The server's lease row is authoritative.
- **Every runner write carries (job_id, epoch).** The server applies it only `WHERE runner_id=? AND epoch=?` and state is non-terminal. Otherwise it replies `fenced` and the runner must kill the local process.
- **On reconnect** (sleep/wake, blip), `hello` lists the runner's local jobs and epochs, and the server reconciles each one:
  - Still leased to this runner: adopt and extend.
  - Reassigned or cancelled: `fenced` (kill).
  - Unknown: kill.
- A sleeping Mac that wakes after its lease expired therefore can't act on a reassigned job. Its writes are rejected and it's told to stop.
- **On server restart**, load leases from SQLite and **extend all running leases by a restart grace** (Nomad's `failover_heartbeat_ttl` idea, e.g. 2–5 min) before declaring any job lost. This gives runners time to reconnect and re-adopt.
- Slot accounting lives only in the server DB and is derived from non-terminal assignment rows. Runners never self-schedule; they only accept or reject offers. `drain` = the server stops offering and lets running jobs finish.
- **Accept that agent side effects are at-least-once.** A job whose lease expired mid-run might have pushed commits before being fenced. Mitigations: retries resume in a fresh workspace, and workflow-level idempotency (branch names, `[skip ci]` patterns).

### 5.3 Bulk workspace transfer: separate plain-HTTPS endpoints, not the WS

- Coordination messages travel on the WS ("upload workspace W for job J, epoch E"). Bytes go over **separate HTTP requests** (`PUT/GET /api/v1/runners/workspaces/{id}/...`) authenticated by the runner key plus a short-lived, job/epoch-scoped transfer token. This mirrors GitHub, GitLab and Buildkite.
- **Format:** streamed tar + zstd. Hash each part (sha256) so retries are verifiable.
- **Upload in chunks of ~64 MB** (`PUT …/parts/{n}` then `POST …/complete`) from day one, even though the LAN doesn't need it, because Cloudflare caps request bodies at 100 MB on Free/Pro. Resumable-by-part also covers Wi-Fi blips. Downloads use `Range` GETs for resume.
- The server stores parts on disk (later S3-compatible storage via a `WorkspaceStore` port, so the server can hand out presigned URLs like GitHub does). In v1, LAN runner-to-server-to-runner via the server's disk is fine. Direct runner-to-runner can be a later optimisation, since the server can coordinate it but the runners have no inbound ports.
- **Repo finding:** `cmd/arbitron/main.go:1687-1696` sets `ReadTimeout: 10 * time.Second` on the single `http.Server`. That bounds reading the *entire request body*, so a several-hundred-MB (or even 64 MB over slow Wi-Fi) upload would be cut off. The transfer handlers must lift it per request with `http.NewResponseController(w).SetReadDeadline(...)` (Go ≥1.20), or the transfer endpoints should get their own listener/server. Prefer the per-request deadline so everything stays on one port behind one tunnel hostname. `WriteTimeout: 0` is already set for WS and is what long downloads need.
- Minor: `nhooyr.io/websocket` (go.mod) has moved to `github.com/coder/websocket`. The API is the same. Consider migrating when adding the runner channel.

### 5.4 Same port or separate listener?

**Same port, same chi router, separate route subtree** (`/api/v1/runners/*`) with its own auth middleware (runner keys, not user sessions). Reasons:
- A single Cloudflare Tunnel hostname and Access app (with a Service Auth policy on that path) covers it.
- The existing WS stack and `WriteTimeout: 0` already support long-lived connections.
- A separate port would only be justified for gRPC, which is ruled out.

Keep runner authz in a domain service (`domain/runner`) per the hexagonal rules, and keep the WS handler thin.

### 5.5 Why not the alternatives (short)

- **gRPC bidi:** blocked by Cloudflare. No public-hostname Tunnel support, and Access bypass is a security hole. It's also fragile through TLS-intercepting proxies. Woodpecker uses it, but on a dedicated port in trusted networks.
- **Pure long-poll (GitHub model):** a strong, proxy-proof choice, and the planned fallback. As the *primary* it's chattier for 5 s heartbeats plus token streaming, and it adds latency gaps for cancel/probe. Arbitron already has WS plumbing and nothing to reuse for long-poll.
- **SSE + POST:** works, but needs the `text/event-stream` content type to avoid Tunnel buffering. You still need POSTs for everything upstream, so it's two mechanisms for what WS does in one.
- **SSH reverse tunnel (Concourse) / NATS:** extra infrastructure and an extra port or protocol. Overkill for a single-server SQLite system.

### 5.6 Main risks

1. **WebSocket blocked or idle-killed by some corporate proxies.** Mitigation: the transport-agnostic protocol and a planned long-poll adapter. Ping ≤20 s.
2. **Cloudflare drops WS on edge deploys** (documented) and has an unspecified idle timeout. Mitigation: leases decoupled from connections, seq-resumed output, jittered reconnect. Test by killing connections in CI (chaos test).
3. **Fencing bugs.** Any runner write path that forgets the epoch check reopens split-brain. Mitigation: enforce it in one domain-service method that every runner-originated mutation goes through, and add table tests for stale-epoch rejection.
4. **Server-restart mass reassignment** if the restart grace is omitted. Note that the current launchd restart marks running instances failed, per your notes. Runner-hosted jobs must *not* inherit that behaviour.
5. **Body size and read timeouts for transfers** (CF 100 MB cap; the server's 10 s `ReadTimeout`). Mitigation: chunked parts plus per-handler deadlines.
6. **Credential handling.** Runner keys and CF service tokens must follow the existing no-secrets-in-logs/dashboard rules, with redaction tests. If the per-runner key is ever sent as a query param (some WS clients do this), it will show up in access logs. Use headers only.
7. **Side effects under at-least-once.** A fenced job may already have pushed. The workflow level must tolerate reruns.

**Overall confidence: ~75–80%** in WebSocket-primary + HTTP-bulk + DB lease/epoch fencing for v1. The gRPC exclusion is high confidence (≥95%, Cloudflare documents it directly). The choice between WS and long-poll as primary is the judgement call; if broad corporate-proxy reach became an early requirement rather than a later one, long-poll-primary (GitHub/GitLab model) would be equally defensible.
