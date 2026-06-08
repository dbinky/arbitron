# Final Org-Readiness Gaps — Deferred Scope

These capabilities were explicitly **deferred** (non-goals) from the org-readiness
work (`docs/specs/org-readiness-spec.md`) so the first shared deployment could
ship. They are not defects — they are the next slice of org work, captured here
so we can pick them up shortly. Each is described by outcome, current state, and
what handling it involves.

> Distinct from [`docs/setup/org-setup-gaps.md`](../setup/org-setup-gaps.md),
> which lists environment-dependent *verification* steps for the shipped feature.
> This doc lists *unbuilt* capability.

---

## 1. SSO group / department mapping to RBAC

**Outcome:** Authenticated users are placed into the right department/group and
see only their scope, driven by claims the SSO edge forwards (e.g. Entra groups
via Cloudflare Access).

**Current state:** Edge mode authenticates by verified email and puts every
non-admin user into the single **default group that can see everything**. The
RBAC department/visibility model already exists (Phase 6) and is used by manual
and scheduled triggers; it is simply not fed from edge-token claims yet.

**Handling involves:** configuring Cloudflare Access to forward the group/dept
claim; reading that claim in the edge verifier/provisioner; mapping it onto the
existing department model and default-group fallback. Depends on the claim being
available in the token.

## 2. Scheduling dashboard UI

**Outcome:** Users view, create, edit, enable/disable, and inspect run history of
schedules from the dashboard.

**Current state:** Phase 3 ships full schedule management over **REST + MCP**
only; there is no dashboard view.

**Handling involves:** a dashboard surface over the existing schedule API
(list/create/edit/enable-disable/delete) plus a per-schedule run-history view.
No backend change required beyond possibly exposing recent runs per schedule.

## 3. Multi-instance / horizontal scale-out

**Outcome:** Arbitron runs as more than one replica without double-firing
schedules, racing the queue, or corrupting state.

**Current state:** Deployment is a **single instance** on embedded SQLite. The
schedule evaluator and queue are in-process and assume one instance.

**Handling involves:** a shared datastore (or leader election / distributed
lock), making the scheduler and queue coordination-safe across replicas, and the
migration off single-writer SQLite. Largest of the deferred items; revisit when
single-instance capacity becomes the constraint.

## 4. Schedule "run now" / manual fire

**Outcome:** A user can trigger a defined schedule immediately, on demand,
without waiting for its next cron tick (reusing its variables + creator
attribution).

**Current state:** Schedules only fire on their cron cadence; ad-hoc runs go
through the separate manual `instance_trigger`.

**Handling involves:** a small "run now" action on the schedule API (REST + MCP)
that fires the schedule's workflow with its configured variables, stamped to the
creator, respecting the same collision policy. Low effort.

## 5. Additional provider adapters

**Outcome:** Connect provider families not covered today, if a need arises.

**Current state:** Existing families cover the deployment — `claude-cli`,
`litellm`/`openai`/`ollama`, `azure`, `anthropic-api`, `bedrock`, `openrouter`.
LiteLLM is the gateway and reaches Azure AI Foundry, so no new adapter is needed
now.

**Handling involves:** only adding a new adapter behind the existing
`ChatProvider`/`AgentProvider` ports + provider-id mapping **if** a concrete
provider outside those families is required. Demand-driven; no action unless a
specific provider is requested.

---

## Suggested order when we pick this up

1. **Run now** (#4) — small, self-contained, immediately useful.
2. **SSO group/dept mapping** (#1) — unlocks real per-department scoping; gated on
   the claim being forwarded.
3. **Scheduling dashboard** (#2) — UI over capabilities that already exist.
4. **Multi-instance** (#3) — larger architectural effort; do when scale demands.
5. **New provider adapters** (#5) — only on demand.
