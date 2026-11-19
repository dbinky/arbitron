# Remote Runners: spreading node execution across machines and subscriptions

**Status:** idea / not started.
**Written:** 2026-11-19
**Scope:** a pool of **runners**: machines that execute Arbitron nodes on the server's behalf, using the harness subscriptions logged in on each machine. It covers:
- a separate `arbitron-runner` Go binary for macOS
- per-node placement across runners by model group, spread-first
- automatic failover when a subscription runs out or a runner dies, by checkpointing workspaces on the server and moving them
- moving live interactive sessions
- runner keys
- live machine telemetry
- read-only dashboard views
- MCP / REST control of all of the above

Local mode keeps working exactly as today.

**Explicitly out of scope (future):**
- Linux and Windows runners, and their telemetry adapters (§18)
- **Runner ownership and visibility**: "my runner runs my work", multi-user pools (§18)
- a long-poll transport fallback for WebSocket-hostile networks (§10)
- runner self-update (§17)

**Evidence:** research reports for this doc are in [`docs/evidence/remote-runners/`](../evidence/remote-runners/):
- [transport](../evidence/remote-runners/research-transport.md)
- [exhaustion and reset detection](../evidence/remote-runners/research-exhaustion.md)
- [moving interactive sessions](../evidence/remote-runners/research-session-migration.md)

---

## 1. Why this exists

**The immediate problem.** Arbitron and every harness it drives (`claude -p`, `codex exec`, `pi`, `opencode`) run on one machine, today an M2 MacBook Air. Large fan-outs saturate it: CvK-style orchestrations with a dozen agents, parallel reviewer groups, test suites in script nodes. The plan is to add a Mac mini and have it do the heavy lifting.

**The showcase.** The quickest fix would be to move the whole server to the mini, which needs no code at all. This feature goes further on purpose. Arbitron is now open source, and a pool of runners that spreads work across machines and subscriptions, fails over as subscriptions run dry, brings them back when they reset, and survives a dead box, is a strong demonstration of what the engine can do.

**The subscription constraint.** Subscription-billed harnesses (Anthropic and OpenAI plans, used instead of API pricing) only work on a machine where someone has logged in interactively. Today that ties Arbitron to the machine you're logged into. Runners turn "where the subscription lives" into a pool resource. Later (§18) that is also how a shared Arbitron could let each person spend their own subscription.

**Prior thinking.** A 2026-11-18 session opened with the same idea, then called a "remote executor" with an "Arbitron-Client". It was interrupted by an engine bug before any design happened. This doc supersedes it.

## 2. What exists today (grounding)

- **Execution ports.**
  - `domain/engine/ports.go` defines `ChatProvider` (`:45`), `AgentProvider` (`:54`), and their selectors (`:63`, `:71`) and `InteractiveStreamSelector` (`:78`).
  - The composition-root factory picks a concrete adapter per resolved binding: `adapter/provider/factory.go`, `ChatProviderFor` `:192` and `AgentProviderFor` `:251`.
  - Subprocess harnesses run through `claudecli.CommandRunner` (`adapter/claudecli/runner.go:57`). The codex, opencode, backend-health and probe adapters reuse it.
  - Script nodes run via `adapter/process/runner.go`.
- **Workspaces.**
  - `WorkspaceManager` (`domain/engine/ports.go:27`) creates, removes and measures a per-instance directory on the server's disk, exposed to templates as `{{workspace}}`.
  - Chat nodes only use it as the subprocess working directory (`executor_llm.go:83`, via `WorkDirFromVars`, `conventions.go:30`). They never read files, because chat runs with tools suppressed.
- **Workspace copying already exists.** `GroupWorkspace` (`domain/engine/group_workspace.go`) snapshots a parent workspace by content (no hard links, no git pointers back) and forks an independent writable copy for each `parallel_group` task. `adapter/git` adds a Git-backed working area with finalize and merge. Moving a workspace between machines is the same operation with a network hop.
- **Concurrency** is global and per user only: `QueueConfig{GlobalCap, PerUserCap}` (`domain/config/config.go:158`), enforced in `domain/queue/manager.go`.
- **Interactive sessions.**
  - The first turn creates a harness session. Its `{sessionId, nonce, messages}` is stored in the DB (`executor_interactive.go`).
  - Later turns resume it (`claude --resume`, `codex exec resume`).
  - A turn is written only if it succeeds, so the DB always sits on a clean turn boundary.
  - The overlap guard from PR #37 is an **in-memory** `sync.Map` (`domain/engine/conversation.go:86`). It cannot work across processes.
  - The harness's own session file is the only complete record of a session. Arbitron keeps text only: no tool calls, tool results or reasoning.
- **There is no exhaustion detection.**
  - Claude's stream-json parser (`adapter/claudecli/streamjson.go`) drops the structured `rate_limit_event` and `quotaLimits` fields.
  - The Codex adapter picks error events by type (`error` / `turn.failed`) and passes the message through unchanged. Nothing classifies it.
  - Pinned and LunaRoute runs deliberately drop the cause so credentials and tool output can't leak, so a quota refusal shows as a bare "exit status 1".
  - Neither adapter reports "subscription exhausted" as a distinct error. The 2026-11-12 incident ("harness process failed" hiding an expired login) is the same gap from the auth side.
  - Test fixtures in `adapter/codex/*_test.go` use an ASCII `You've` and a usage URL that differ from the real codex 0.170 output, which uses U+2019 and `chatgpt.com/settings/usage`. That's harmless today, since nothing matches the text. New fixtures should use real output.
- **LunaRoute.** Busy responses are already classified: `CONCURRENT_REQUEST_LIMIT_EXCEEDED` → `ErrLunaRouteBusy` (`domain/provider/lunaroute_classify.go:38`).
- **HTTP.**
  - chi on one port. Dashboard streams already use WebSockets (`nhooyr.io/websocket`).
  - The server sets `ReadTimeout: 10s` (`cmd/arbitron/main.go:1690`), which would cut off large uploads unless overridden per request.
- **Tokens.** `token_create` / `token_list` / `token_revoke` exist for API tokens (`adapter/mcp/tools_token.go`). Runner keys must be a **separate kind** (§11).

## 3. Vocabulary

- **Node:** unchanged. A step in a workflow graph.
- **Runner:** a machine that executes nodes for the server. It dials out, advertises what it can do, reports telemetry, and does what it's told. It is deliberately dumb: no scheduling and no concurrency decisions.
- **Built-in runner:** the server's own machine, wrapped in the same runner interface, in-process. Local mode is the server plus the built-in runner.
- **Capacity source:** one credential on one runner, such as "the Anthropic subscription on mini-1", "the OpenAI subscription on mini-1" or "the LunaRoute key on mini-1". Each source has:
  - a **model group**: which backends and models it can serve
  - a **health state** (§6)
  - an **in-flight count**

  Each runner has at most one source per vendor: one Anthropic subscription, one OpenAI subscription, one `lr_` key.
- **Workspace home:** the single runner that currently owns a run's workspace. There is exactly one writer at a time.
- **Checkpoint:** the server's copy of a run's workspace, and of any interactive harness session, as of the last completed node or turn.
- **Generation / epoch:** a counter bumped on every ownership handoff of a job, workspace or session. Every write from a runner carries it; stale writes are rejected. This is the fencing mechanism.

## 4. Architecture: control plane and data plane

**The server is the control plane.** It owns:
- the database, the queue and placement
- concurrency counting
- timers, webhooks and waits
- `spawn_workflow`
- checkpoints
- the dashboard and MCP

It evaluates pure nodes (`transform`, `conditional`) itself.

**Runners are the data plane.** They execute everything that touches a harness or a workspace: `llm_call`, `agent_call`, `script`, `tool_call`, `interactive_llm`, `interactive_agent`.

The seam belongs at the execution ports. A new runner-facing port, something like `NodeRunner` (dispatch a fully rendered unit of work to a runner and stream results back), sits beside today's providers. The server renders templates, `@lib` files and scripts **before** dispatch, so runners never need the library or the manifests.

On the runner, the existing `claudecli`, `codex`, `pi` and `opencode` adapters are linked in and called locally. Most of the runner is code that already exists. The new parts are:
- the connection
- the job protocol
- workspace pack, unpack and checkpoint
- telemetry

## 5. The runner

- **One static Go binary,** `arbitron-runner`. It lives in `cmd/arbitron-runner`, in the same module, and is released alongside `arbitron`.
  - It has no dashboard embed, no SQLite and no HTTP server, which keeps it small with a small attack surface.
  - It follows the server's strict hexagonal layering: domain ports for connection, job execution, workspace store, telemetry and credentials; adapters implement them.
- **macOS only for now.** The target is a robust Mac mini runner.
  - It starts at boot as a launchd agent with `KeepAlive`, in the same style as today's `dev.arbitron` service.
  - The installer gains a runner scenario.
- **Config and credentials** live in `~/.arbitron-runner/`: a config file plus a 0600 `runner.env` holding:
  - `ARBITRON_SERVER`
  - `ARBITRON_RUNNER_KEY`
  - the machine's `lr_` LunaRoute key, which is the only API key a runner holds

  The Anthropic and OpenAI subscriptions are logged in interactively (`claude /login`, `codex login`). **Credentials never leave the runner. The server never sees a secret.**
- **Same workspace path on every runner**, such as `~/.arbitron-runner/workspaces/<instance-id>`. Harness session files record absolute paths, including references to saved tool results. Identical paths keep those references valid after a move, so nothing needs rewriting.
- **Self-checks.** The runner probes its own sources about every 60 seconds and reports health, so a fresh `codex login` comes back into the pool on its own.

## 6. Capacity sources and health

Each source is in one of these states:

| State | Meaning | Effect |
|---|---|---|
| **available** | Healthy | Eligible for placement |
| **exhausted until T** | Subscription limit hit; reset time known | Ineligible until T, then one job proves it before full trust |
| **exhausted (reset unknown)** | Limit hit, no reset time | Ineligible; re-checked periodically |
| **logged out** | Expired or revoked OAuth, not logged in | Ineligible; **alert a human**, the only state that needs one |

Exhaustion applies only to subscriptions. **LunaRoute never becomes exhausted.**
- Requests over the plan's lanes go to overflow: slower, same price.
- A request waits for a slot for up to 60 seconds, or 240 seconds with the `-background` suffix.
- If that wait expires or LunaRoute's queue is full, you get `429 CONCURRENT_REQUEST_LIMIT_EXCEEDED` with `Retry-After: 60`.
- That 429 is **backpressure**: retry in place, never mark the source exhausted.
- LunaRoute concurrency is not managed by Arbitron. LunaRoute paces it, and its plan lane count is only a spread weight.
- Nodes on LunaRoute need timeouts comfortably above 60 seconds plus generation time.

**Detection must use structured fields only, never message prose.** Both vendors have changed their wording, and the Claude text can be wrong ("monthly spend limit" on a 5-hour hit). From the research:

**Claude Code** (2.1.328):
- Exhausted: an assistant turn with `error:"rate_limit"`, `apiErrorStatus:429`, and `quotaLimits{status:"rejected", resetsAt, rateLimitType}`.
- Logged out: `error:"authentication_failed"`.
- Transient: `overloaded` (529) and "temporarily limiting requests (not your usage limit)".
- Early warning: stream-json emits a `rate_limit_event` with status `allowed`, `allowed_warning` or `rejected`, plus `resetsAt`, on every run. Track it per source.
- The stop reason of an exhausted turn is indistinguishable from a clean finish, so classify from the error fields.

**Codex** (0.170):
- `codex exec --json` gives only a `turn.failed` message and exit code 1.
- The structured cause is in the session log on the runner: `codex_error_info` is `usage_limit_exceeded` (exhausted), `unauthorized` (logged out), or `server_overloaded` / `rate_limit_exceeded` / `response_stream_disconnected` (transient), with `resets_at`.
- Early warning: `codex app-server` → `account/rateLimits/read` returns `used_percent`, `window_minutes` and `resets_at` for each window **without spending model tokens**. Poll it.

**Windows.** Track limit windows by length (5-hour, 7-day), not as "primary/secondary", because plans differ.

**Version pinning.** Pin harness versions per runner and keep fixture tests of these JSON shapes. They have changed before.

**Terms of service.** Running the unmodified CLI on your own subscription on your own machines is fine. Calling the vendor usage endpoint with the CLI's OAuth token from another program is not, and is out. So is routing other people's work through your plan credentials (§18).

## 7. Placement and spread

**Two gates per node,** both counted on the server:
1. **A runner slot.** Each runner advertises a configured slot limit, default 8, set in server config or via MCP, and the server enforces it. It counts **running node executions**, not runs placed on the runner: a run parked on a timer, a child or a human holds disk, not CPU.
2. **A healthy source** of the node's model group on that runner.

**Policy is spread-first:** prefer the eligible runner and source with the most headroom.
- **Ultra-tier subscriptions** have no short windows, and GPT is used sparingly. Spreading keeps throughput high and burns all accounts down evenly.
- **LunaRoute** has one key and plan per machine, so spreading its calls multiplies throughput.

**By node kind:**
- **Chat nodes are hermetic.** They run in an empty scratch directory with no workspace, so they can go to any runner with a healthy source and nothing moves. Context comes from the template only.
- **Agent, script and interactive nodes** need the workspace. Placement weighs **stay** (the current home has a healthy source for this group: the common case, nothing moves) against **move** (the home's source is unhealthy, or another runner is much less loaded). Without that stickiness, a doer ≠ approver loop alternating Claude and GPT would copy the repo on every step.
- **Scripts** have no model group; they run on the workspace's home.

**Other rules:**
- **Parallel group branches** each get their own workspace copy already, so they place independently. A fan-out naturally spreads across runners.
- **`spawn_workflow` children** get their own placement.
- **When no healthy source exists,** the node waits in a distinct **waiting · capacity** state that names the missing group and the next known reset time. A dry pool must never look like a hung engine.
- **Always move rather than wait** for a reset, even a near one.
- **Failover never crosses model groups.** Runners advertise their sources, and placement assigns accordingly, so doer ≠ approver and other family rules hold.

## 8. Workspaces: checkpoints, moves, failover, fencing

**Checkpoint after every node that changes the workspace.**
- When an agent or script node finishes, the runner sends the server only the **changed files**: walk the tree, hash, send what differs.
- **A node is not recorded as done until its checkpoint lands,** so every completed node is recoverable.
- The server keeps the latest checkpoint per active run. That costs server disk (a few GB on the MBA), and it's worth it.

**Moves are straight copies** to the target runner. The minis are on one LAN. Bulk bytes never go over the control channel (§10).

**One code path for both kinds of failover:**
- **Planned** (subscription exhausted, drain, rebalancing): the old runner finishes its node and the checkpoint lands. The server restores it on the new home, bumps the generation, and tells the old runner to delete its copy.
- **Unplanned** (runner lost):
  - **Suspect:** about 30 seconds without a heartbeat. No new placements, nothing moves; this absorbs Wi-Fi blips and sleep.
  - **Lost:** after a configurable grace period, around 2 minutes. Each affected run restores its last checkpoint on a healthy runner and **re-runs the node that was in flight**. That matches today's retry semantics: at most one node's work is lost.

**Fencing.** Every handoff bumps the generation, and every runner write carries it. A runner that reconnects after being declared lost reports which generations it holds. The server tells it to delete stale copies, and rejects results from jobs that were reassigned. Without fencing, two copies of a run carry on in parallel, the same class of bug as the pause/resume duplicate chain.

**Server restarts.** After a server restart, **extend** the leases of jobs still running instead of reassigning them, as Nomad does after a control-plane failover. Otherwise every server restart becomes a failover storm.

**Side effects outside the workspace** (a `git push`, a PR comment) happen at least once, even if the runner is later fenced. That is the same contract retries already have.

## 9. Moving live interactive sessions

Requirement: if an `interactive_agent` session's subscription runs out mid-session, the session **moves** to another runner with the same model group and a different account, and continues with its context.

**Default: move the harness's own session.**
- After every successful turn, the runner checkpoints the harness session to the server, so a move never depends on the old machine being reachable:
  - Claude: the session `.jsonl` plus its `<session-id>/` folder of saved tool results and subagent logs.
  - Codex: the session file.
- To move, import the checkpoint on the new runner and resume by session ID.
- **Tested locally:** Claude resumes a copied session from a different working directory. It doesn't check the account or org IDs stored in the file, and it tolerates a corrupted thinking-block signature. Codex resumes a copied session under a new ID from a different directory and indexes it itself.
- **Cross-account, inferred:**
  - Claude: per Anthropic's docs, hidden reasoning from current models is only valid in the account that produced it. It is dropped without an error in another account, so the session resumes but loses that reasoning. **Never edit message text** when moving: a changed history can cause a 400.
  - Codex: encrypted reasoning items are tied to the original account's org. Users report `400 invalid_encrypted_content`. **Strip reasoning items** from Codex sessions before resuming.
  - Both: the first turn after a move rebuilds the prompt cache on the new account.

**Fallback: rebuild from Arbitron's transcript.** If import or resume fails, start a fresh session from the original first prompt, with the same completion nonce, plus the transcript so far. Context survives, but tool and reasoning detail is lost.

**Exhaustion mid-turn.** Arbitron's DB is already at a clean boundary. Roll the workspace and the session checkpoint back to the start of that turn, move, and **resend the same message automatically**. Exhaustion between turns is simple: move, then send.

**The turn guard must change.** The in-memory overlap guard (`conversation.go:86`) becomes a DB lease with a generation number, the same fencing as everywhere else.

Interactive sessions never move for load balancing, only for exhaustion, drain, or a lost runner.

## 10. Transport

**Control channel: one outbound WebSocket per runner,** on the existing chi port.
- **Why not gRPC:** Cloudflare Tunnel supports gRPC only for private-network routing, not public hostnames, and Access doesn't support gRPC at all. TLS-inspecting corporate proxies tend to break it too. That would rule out the future cloud-hosted case.
- **Why WebSocket:**
  - It works through Cloudflare on all plans, and Access service-token headers can go on the upgrade request.
  - Arbitron already runs WebSockets for the dashboard.
  - Cancel, drain and probe arrive instantly, and streaming interactive tokens is cheap.
- **Every message is an idempotent request** keyed by (runner, job, generation, sequence number). Output is numbered and replayed after a reconnect.
- **Connection loss is not runner loss.** Cloudflare drops WebSockets when it restarts its servers. Liveness is the lease and heartbeat in §8, not the socket.
- **The idempotent design keeps a long-poll fallback cheap** for proxy-hostile networks: GitHub- and GitLab-style 50-second holds, under Cloudflare's 125-second timeout. It's not built in v1.

**Bulk transfer: separate chunked HTTPS requests,** coordinated over the WebSocket. Authentication is the runner key plus a short-lived transfer token scoped to the job and generation. This mirrors GitHub, GitLab, Buildkite and Concourse, none of which put artifacts on the job channel.
- Parts of about 64 MB, resumable. Cloudflare Free and Pro cap request bodies at 100 MB.
- Transfer handlers need per-request read deadlines (`http.ResponseController`) because of the server-wide `ReadTimeout: 10s`.

## 11. Runner keys and security

**Keys are the one interactive exception to "dashboards are state-only".** A secret shown once should never sit in config or in an agent's transcript.
- **Add runner** is a dashboard modal. You enter a unique key name, which becomes the runner's name, and click Create. The key is shown **once**, obscured, with a Copy button and a "won't be shown again" warning, plus the `runner.env` snippet. Cancelling before Create leaves nothing behind.
- **Each key binds to one runner.** The first runner to connect with a key claims it, and from then on the key only works from that machine. Unclaimed keys show as **unclaimed**.
- **Keys list:** key name · runner · created · last used · [Revoke].
- **Revoke** has a confirmation that states the consequence: the live runner disconnects immediately, and its runs fail over from their checkpoints with the in-flight node re-run. It recommends draining first.
- **MCP parity:** `runner_key_create` (returns the secret once), `runner_key_list`, `runner_key_revoke`. Creating keys through MCP puts the secret in that agent's transcript, which is why the modal exists for humans.
- **Runner keys open the runner channel only,** never REST or MCP as a user. A compromised mini must not get the control plane.
- Creating and revoking keys is **admin-only and audited** (`runner_key.create`, `runner_key.revoke`).
- **No server-side enrollment otherwise.** A runner with a valid key that dials in appears on its own.

**Runner security posture.** Agents run with permission checks skipped, on a dedicated Mac, with the runner user's full access. That's acceptable for your own minis. It needs revisiting before contributors' machines (§18).

## 12. Machine telemetry

- **A `MachineTelemetry` port in the runner domain,** with **only a darwin adapter** for now. Linux and Windows adapters are future work.
- Reported every 5 seconds with the heartbeat:
  - CPU
  - memory used and **memory pressure**
  - 1-minute load as a percentage of cores
  - free disk on the workspaces volume
  - thermal state
  - uptime
  - cores and RAM
  - runner and harness versions
- Telemetry is for display and as a placement input. A runner under memory pressure or thermally throttled is a poor home.

## 13. Dashboard (state views only)

All views are live, driven by runner telemetry. Apart from runner keys (§11), there are **no controls**: everything else is MCP or REST. Throwaway mockups were made during the session in a gitignored `docs/temp-mockups/` folder; they aren't committed.

- **Pool, a new top-level nav item:**
  - **Alerts:** a source logged out (the only state that needs a human), runs waiting for capacity, runners offline.
  - **Model-group cards** for Anthropic, OpenAI and LunaRoute: healthy count, in flight, queued, next reset countdown, and a spread bar per runner. LunaRoute shows overflow and 429s as pressure, never as exhaustion.
  - **A card per runner:** online, suspect or offline; draining or cordoned; version; heartbeat age; slots; live CPU and memory; a chip per source with state and countdown; workspaces homed there.
  - **Recent pool events:** connects, disconnects, moves, drains, and source state changes, each with who or what caused it.
  - **The Runner keys section,** plus an **+ Add runner** button.
- **Pool in local mode:** the same page with one built-in runner card. Even with a single machine it turns "harness process failed" into a red *logged out* chip.
- **Queue:**
  - a **Runner** column showing the workspace's home
  - new states: **waiting · capacity** (with reason and countdown), **queued · slot**, **moving workspace** (with progress), and **recovering from checkpoint**
  - interactive sessions show their pin
  - parallel groups show which runner each branch is on
- **Run graph:**
  - nodes colored by the runner they ran on; grey for server-side pure nodes
  - workspace-move badges on the edges where moves happened
  - a node panel with **placement and the reason for it** ("workspace on mini-1, but mini-1 OpenAI exhausted, resets in 2h 14m; moved to mini-2"), plus the workspace's history of homes
- **Runner detail:**
  - live telemetry sparklines and stats
  - sources with harness versions and last check
  - homed workspaces, and drain progress when draining

## 14. MCP and REST surface

Everything the dashboard shows is readable, and everything an operator does is an MCP tool with a REST equivalent under `/api/v1`.

| Area | Operations |
|---|---|
| Runners | `runner_list`, `runner_get` |
| Runner control | `runner_cordon`, `runner_uncordon`, `runner_drain`, `runner_update` (slot limit, labels; also server config) |
| Sources | `runner_source_probe` (force a health check) |
| Keys | `runner_key_create`, `runner_key_list`, `runner_key_revoke` |
| Placement | each execution record in `trace_get` / `trace_get_node` carries its runner, source and reason; workspace moves appear as trace events |

**Drain** stops new placements, lets in-flight nodes finish, moves homed workspaces at their next node boundary, and waits for interactive sessions (or moves them). It replaces the manual "pause everything, watch child processes, deploy, resume" routine. Authorization stays in the service layer, per AGENTS.md.

## 15. Local mode

Local mode is the server plus the built-in runner, with no configuration. **With no runners configured, behavior must be identical to today.** That is a test gate in its own right. All placement, checkpoint and health code runs in local mode as a one-runner pool, so it gets exercised every day before a second machine exists.

## 16. Suggested build order

The pipeline may resequence this; it records the dependency shape.

1. **Runner seam with the built-in runner.**
   - Introduce the runner-facing port, and recast today's execution as the in-process runner.
   - Add structured exhaustion, auth and transient classification to the Claude and Codex adapters (§6), and capacity source health.
   - Local mode must be byte-for-byte unchanged in behavior.
2. **Remote runner binary.** `cmd/arbitron-runner` (macOS), the WebSocket protocol with idempotent, generation-carrying messages, runner keys (modal, MCP, claim-binding, audit), heartbeats, the darwin `MachineTelemetry` adapter, and launchd install.
3. **Placement.** Slots per runner, two-gate placement, spread-first, the waiting · capacity and queued · slot states, hermetic chat.
4. **Checkpoints and moves.** Changed-file checkpoints, chunked transfer, planned moves, suspect/lost detection, failover from checkpoint, fencing, lease extension after server restart, drain and cordon.
5. **Interactive session moves.** Session checkpoints per turn, import and resume, Codex reasoning stripping, the transcript-rebuild fallback, resending a turn interrupted mid-way, and the turn guard as a DB lease.
6. **Dashboard.** The pool, queue, run-graph and runner-detail views from §13, then `make ui`.

## 17. Open questions and risks

- **Runner upgrades and version skew.** The protocol needs version negotiation from day one. Undecided: whether the server refuses old runners, whether runners update themselves, and how harness-version pinning is reported.
- **Cross-account checks we couldn't run** with one account per vendor:
  - whether Codex checks its stored account ID on resume
  - how much is lost when a Claude session moves accounts

  Verify both once a second account exists.
- **Checkpoint cost on big repos.** Changed-file deltas should keep this small, but a ralph loop on a large monorepo is the stress case. Measure it.
- **A missed generation check** anywhere a runner can write would let a fenced runner act on a reassigned job. Make the check structural, not per-handler.
- **Harness drift.** Vendors change JSON shapes and mark some of them internal. Fixture tests per pinned version are the defense.
- **Corporate proxies that block WebSockets** need the long-poll fallback (§10). That isn't a v1 concern on a home LAN.

## 18. Future scope

- **Runner ownership and visibility (v2).** Runners carry an owner, and placement prefers, or requires, the runner owned by whoever started the run. That lets a shared Arbitron spend each person's own subscription. **Terms-of-service boundary:** a person's runner only ever runs that person's work. Never pool subscriptions across people. Also open: who sees which runners.
- **Linux and Windows runners,** and their `MachineTelemetry` adapters.
- **Long-poll transport fallback.**
- **Pool-level capacity sources,** such as a key shared by several machines. The source model allows it; nothing needs it yet.
- **Usage-over-time charts** per source, to verify spread-first is actually spreading.
