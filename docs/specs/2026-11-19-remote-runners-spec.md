# Remote Runners — Product Specification

## Purpose and users

Remote Runners lets an Arbitron operator distribute workflow execution across their own macOS machines and the subscriptions available on each machine. It reduces pressure on a single computer and keeps workflows progressing when a subscription becomes unavailable or a machine is lost.

The release covers automatic placement, recovery, interactive-session continuity, runner administration, and live monitoring as one pool-management feature. It serves a single operator using their own machines and accounts; pooling subscriptions across people is outside scope.

Existing local operation requires no additional setup. Model choices, model-family separation, workflow controls, and access restrictions remain respected.

## Execution and placement

- Operators can add macOS runners that start automatically with the machine and use locally configured harnesses and accounts. Vendor credentials remain on that runner.
- Each runner has a configurable execution limit, initially eight concurrent steps. Waiting workflows do not consume execution slots.
- Placement spreads independent work across eligible runners with available capacity, considering workload, subscription headroom, and machine health.
- Chat steps can run independently of the workflow workspace. Workspace-based steps usually stay on their current machine; they may move when required capacity is unavailable or another machine offers a substantial load advantage. Scripts and workspace-based tools run with that workspace.
- Parallel branches and child workflows receive independent placement.
- Interactive sessions stay on their current runner except for subscription exhaustion, draining, or runner loss.
- Placement and recovery preserve the requested model and model-family restrictions. They never substitute another family to obtain capacity.
- If an eligible alternative exists, work moves rather than waiting for the current subscription to reset. Otherwise, the workflow visibly waits for capacity, identifying what is missing and the next known reset time.
- Exhausted subscriptions return to service automatically once availability is confirmed. Logged-out accounts require a clear operator alert and return automatically after login is restored.
- Temporary provider congestion, including LunaRoute backpressure, is retried without treating it as subscription exhaustion.
- Incompatible runner or harness versions remain visible with an explanation and receive no new work until resolved.

## Recovery and session continuity

- A workspace-changing step counts as complete only when its resulting workspace is recoverable elsewhere. Successful interactive turns preserve both workspace progress and recoverable conversation context.
- Brief connection interruptions do not immediately trigger moves. A runner becomes suspect after roughly 30 seconds without contact and receives no new work. After a configurable grace period, initially about two minutes without contact, it is considered lost.
- Lost-runner recovery preserves completed steps and automatically repeats the interrupted step on an eligible runner. If none is available, the workflow visibly waits for capacity.
- Subscription exhaustion also triggers automatic recovery. An interrupted interactive turn restarts from its last successful turn and resends the same user message without requiring re-entry.
- Session moves preserve full available conversation context where supported. If full restoration fails, the session automatically continues from the original prompt and recorded conversation, with a visible notice that tool details and reasoning may be lost.
- A workspace or interactive session has only one active execution owner. Results from a superseded execution cannot overwrite recovered progress, and concurrent submissions cannot create overlapping turns.
- A server restart allows still-running work to reconnect and continue, avoiding unnecessary retries.
- Automatic retries can repeat external effects, such as a push or comment. This limitation is clearly disclosed; exactly-once external effects are not promised.

## Administration and visibility

Operators can:

- Add a runner using a uniquely named key, shown only once with copy and setup guidance. A key binds to the first machine that claims it and grants runner access only.
- See unclaimed keys, associated runners, creation dates, and last use. Key creation and revocation are restricted to administrators and audited.
- Revoke a key after confirming that its runner will disconnect immediately and affected work will recover automatically, potentially repeating interrupted steps. The confirmation recommends draining first.
- Temporarily exclude a runner from new work, restore its eligibility, change its execution limit and labels, or request a capacity health check.
- Drain a runner for maintenance: stop new assignments, allow active steps to finish, and move its workspaces and interactive sessions at safe boundaries. Show progress and any capacity preventing completion.

The dashboard is read-only except for runner-key management. Existing agent-driven and programmatic operator interfaces provide the same inspection and administration capabilities while respecting access restrictions.

Live views include:

- **Pool:** runner availability, subscription health and reset countdowns, active and queued work, distribution across machines, actionable alerts, and recent events with their causes.
- **Runner detail:** CPU, memory use and pressure, load, free workspace disk, thermal state, uptime, hardware capacity, runner and harness versions, last contact and health checks, assigned workspaces, and drain progress.
- **Queue:** execution location and distinct states for capacity waits, slot waits, workspace moves with progress, and recovery.
- **Run graph and history:** which runner executed each step, placement reasons, workspace moves, interactive-session assignments, and parallel-branch placement.

The pool remains useful with only the local machine, including clear authentication and capacity status.

## Outcomes and non-goals

Success means:

- A workflow fan-out uses multiple eligible Macs while respecting execution limits and model-family rules.
- Subscription exhaustion moves work to available capacity automatically; a dry pool shows a clear wait reason rather than appearing stuck.
- Losing a runner preserves completed work and recovers affected workflows automatically, repeating only interrupted work.
- Interactive sessions continue after a move without users re-entering messages, with a visible notice whenever recovery loses context detail.
- Operators can identify unavailable capacity, explain placement decisions, and drain a machine without manually pausing every workflow.
- Existing local workflows remain usable without runner setup, and existing Linux server support is preserved. Remote runners in this release support macOS only.

Out of scope:

- Linux or Windows remote runners.
- Sharing subscriptions across people, runner ownership rules, or new visibility controls for multi-user pools.
- Automatic runner updates.
- Additional connectivity modes for networks that block the supported runner connection.
- Credentials shared across the pool, historical subscription-usage charts, and general dashboard execution controls.
- Exactly-once external effects or guaranteed preservation of hidden reasoning across accounts.
