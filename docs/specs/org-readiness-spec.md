# Arbitron Org-Readiness — Product Specification

## Summary

Make Arbitron deployable as a **shared, headless, multi-user service for an
organization** — specifically a single containerized instance running behind an
enterprise SSO edge, using a company-hosted model gateway as the model source,
and able to run workflows on a recurring schedule.

This spec covers three independent capability areas, delivered as three phases
under one effort:

1. **Edge-trusted SSO** — authenticate users from an identity-verifying reverse
   proxy (Cloudflare Access) with no second login.
2. **Pluggable agent backend + universal provider routing** — let workflow
   authors choose the agent backend per node, and route every model call (chat
   and agent) through the org's model gateway or any configured provider.
3. **Scheduled / recurring triggers** — start any workflow on a recurring
   schedule, including script-only workflows that contain no model calls.

A cross-cutting requirement is that the whole product runs **headless**,
configured entirely from environment/secrets, suitable for Azure Container Apps.

**Phase order:** Auth → Agent/Provider → Triggers. Each phase is independently
shippable.

## Background & motivation

Arbitron today is oriented toward a single-user, locally-installed deployment:
the service assumes either no authentication (single auto-admin) or that it runs
its own login flow; the agentic-coding path assumes a locally-authenticated
Claude subscription; and workflows can only be started by hand. An organization
that wants to host one shared instance for many users — authenticated through
its existing SSO, drawing on its own governed model gateway, and running routine
workflows automatically — cannot do so today without gaps in authentication,
model routing, and scheduling.

This spec defines the product behavior that closes those gaps. It is
deliberately outcome-focused; implementation design lives in the companion
design documents.

## Goals

- A shared instance can be placed behind an enterprise SSO edge and recognize
  each user with no additional in-app login.
- Administrators are designated centrally; ordinary users get appropriate
  default access without per-user setup.
- Workflow authors can run both conversational and agentic nodes against the
  organization's model gateway, and choose which agent backend executes a given
  node.
- Teams can schedule workflows to run on a recurring basis, unattended.
- The entire service is operable as a single headless container configured from
  environment and secrets.

## Non-goals (this effort)

- Horizontal scale / multi-replica operation and the database changes that would
  require. The deployment starts as a **single instance** on the existing
  embedded database.
- A full in-app role/permission editor or fine-grained per-resource ACLs beyond
  the existing model. Department/group-claim-driven segmentation is explicitly
  deferred (see "Open questions / future").
- Replacing or re-architecting the existing direct-OIDC ("org") or local
  ("single auto-admin") authentication modes. Edge auth is added alongside them.
- A scheduling UI is desirable but the management surface may initially be the
  API; a dashboard view is a follow-on.

---

## Phase 1 — Edge-trusted SSO

### Outcome

A shared Arbitron instance fronted by an identity-verifying reverse proxy
(Cloudflare Access, with the company IdP behind it) authenticates every user
from the edge-issued identity token. Users who have already signed in at the
edge reach Arbitron with **no second login**.

### Requirements

- **New edge authentication mode.** Arbitron gains an authentication mode in
  which user identity is taken from the reverse proxy's signed identity token,
  in addition to the existing local (dev/single-admin) and direct-OIDC modes.
  The deployment selects the mode by configuration.
- **The edge token is independently verified, not merely trusted.** On every
  request, Arbitron validates the proxy-issued identity token's cryptographic
  signature against the proxy's published signing keys, and checks that the
  token was issued for this application and this organization and has not
  expired. A request that reaches the service directly (bypassing the proxy) and
  presents a forged or absent token is rejected.
- **Identity is the verified email address.** The authenticated user is
  identified by the email claim in the verified token. The first time a valid
  token is seen for a new email, the corresponding user is provisioned
  automatically.
- **Administrators are designated by an email allow-list.** The deployment is
  configured with a list of administrator email addresses. A user whose verified
  email is on that list receives administrator privileges; every other
  authenticated user receives the standard default access.
- **Default access shows everything.** Until the organization supplies group or
  department information, every non-admin authenticated user belongs to a single
  default group that can view all workflows and instances. (Group/department
  segmentation is deferred.)
- **Fail closed.** In edge mode, a request without a valid verified token is
  denied (HTTP 401). Arbitron does not present its own login page in edge mode;
  sign-in and sign-out are handled at the edge.
- **Dev keeps working without the edge.** Local/development use continues to run
  in the existing local mode without requiring the proxy or any token.
- **Operator configuration is minimal and headless.** Enabling edge mode
  requires only: the organization/team domain (used to locate signing keys and
  to validate the issuer), the application audience identifier, and the
  administrator email list — all suppliable via environment/secrets.

### Acceptance criteria

- With edge mode enabled, a request bearing a valid token issued by the proxy
  for this application is served as the corresponding user.
- A request with a missing, expired, wrong-audience, wrong-issuer, or
  signature-invalid token is rejected with 401.
- A user whose email is on the admin list is treated as an administrator; any
  other authenticated user is a default-group member who can see all workflows
  and instances.
- A new user's account is created automatically on first valid sign-in.
- The service starts and runs in edge mode with configuration supplied entirely
  through environment/secrets.

---

## Phase 2 — Pluggable agent backend + universal provider routing

### Outcome

Workflow authors can run **conversational nodes and agentic nodes** against the
organization's **model gateway** (or any other configured provider), and can
choose **which agent backend** executes a given agentic node. The deployment is
not tied to any single vendor login.

### Requirements

- **Agent backend is selectable per node.** For an agentic node, the workflow
  author can choose the agent backend that runs it — either of the two supported
  agent backends (Claude Code or opencode). The choice is made on the node.
- **A deployment default applies when a node does not choose.** The operator
  configures a default agent backend for the deployment; an agentic node that
  does not name a backend uses that default. For this organization's deployment,
  the default is Claude Code routed through the model gateway.
- **Any node can use the model gateway.** Both conversational and agentic nodes
  can be configured to send their model calls to the organization's model
  gateway (an OpenAI/Anthropic-compatible proxy in front of the company's model
  service). Neither agent backend requires a personal vendor subscription or an
  interactive vendor login.
- **The agent backend is independent of the provider.** Choosing an agent
  backend (which tool runs the loop) is separate from choosing the provider
  (which endpoint, key, and model it talks to). Either backend can be pointed at
  the gateway or at a directly-configured provider.
- **The gateway is the default path; direct providers remain available.** The
  model gateway is the default route for all model traffic, but the existing
  direct provider integrations remain usable so a node can target a specific
  provider when needed. No new provider integration is required for this effort.
- **Both agent backends are present in the deployment.** The container image and
  the installer make both supported agent backends available so either can be
  selected at runtime.

### Acceptance criteria

- An agentic node configured to use Claude Code completes a task with its model
  calls routed through the gateway.
- An agentic node configured to use opencode completes a task with its model
  calls routed through the gateway.
- A conversational node completes with its model calls routed through the
  gateway.
- An agentic node that names no backend runs on the configured deployment
  default.
- A node can be configured to reach a directly-configured provider instead of
  the gateway and still run.
- A freshly provisioned deployment has both agent backends available without
  any manual post-install step.

---

## Phase 3 — Scheduled / recurring triggers

### Outcome

Teams can schedule any workflow — including script-only workflows with no model
calls — to start automatically on a recurring schedule, unattended.

### Requirements

- **Recurring schedules use cron expressions with a timezone.** A schedule is
  expressed as a cron expression and carries its own timezone (defaulting to
  UTC), so calendar-aligned schedules such as "weekdays at 09:00 local" are
  expressible.
- **Any workflow type can be scheduled,** including workflows composed only of
  script/non-model nodes.
- **Collision policy: skip when busy.** If a scheduled workflow's previous run
  is still active when the next scheduled time arrives, that occurrence is
  skipped rather than started concurrently.
- **Misfire policy: no backfill.** After downtime (e.g., a container restart),
  occurrences that were missed while the service was down are not retroactively
  run; scheduling resumes at the next scheduled time.
- **Skips and misses are surfaced.** Whenever an occurrence is skipped (busy) or
  was missed (downtime), the system raises an alert/notification so operators
  know a run did not happen.
- **Authorized users can self-serve; runs are attributed to them.** A user who
  is permitted to start a given workflow may create a schedule for it. Each
  resulting scheduled run is owned by and attributed to the schedule's creator
  (carrying the creator's identity and department, consistent with how manually
  triggered runs are attributed). Administrators can manage all schedules.
- **Schedules persist across restarts.** Defined schedules survive a restart and
  continue firing without re-creation.
- **Schedules are fully manageable through the API.** Creating, listing,
  updating, enabling/disabling, and deleting schedules is available through the
  same programmatic surfaces (REST and the tool/automation interface) used for
  other trigger operations.

### Acceptance criteria

- A cron schedule starts its workflow at the correct time in the schedule's
  timezone, including a script-only workflow.
- When a prior run is still active at the next scheduled time, the occurrence is
  skipped and an alert is raised.
- After the service is restarted past one or more scheduled times, missed
  occurrences are not backfilled, an alert reflects the miss, and the next
  occurrence fires normally.
- A scheduled run is attributed to the creating user and that user's department.
- A non-administrator can manage their own schedules but not others'; an
  administrator can manage all.
- Schedules defined before a restart are still present and active afterward.

---

## Cross-cutting — Headless shared deployment

### Outcome

Arbitron runs as a single headless container suitable for Azure Container Apps.

### Requirements

- **Configured entirely from environment/secrets.** The service starts and runs
  with no configuration file and no interactive setup; all settings, including
  the edge-auth, gateway, and agent-backend settings above, are suppliable
  through environment variables and secrets.
- **No interactive login at startup.** The deployment requires no interactive
  vendor or identity login to become ready.
- **Single instance on the embedded database to start.** The deployment runs as
  one instance using the existing embedded datastore; multi-instance scale-out
  is out of scope for this effort.
- **Both agent backends installed.** The container image (and the host installer)
  provision both supported agent backends.

### Acceptance criteria

- The container becomes ready and serves the dashboard/API with configuration
  provided only through environment/secrets and no interactive steps.
- The same image runs the auth, model-routing, and scheduling capabilities above
  when given the corresponding configuration.

---

## Success criteria (overall)

- A single shared instance can be deployed for the organization behind its SSO
  edge, drawing on its model gateway, with administrators and default users
  recognized automatically.
- Workflow authors can build and run both conversational and agentic workflows
  against the gateway, choosing the agent backend per node.
- Teams can schedule workflows (including script-only ones) to run unattended,
  with attribution and failure alerting.
- The whole deployment is reproducible from configuration alone, with no
  interactive or per-user setup.

## Open questions / future work

- **Group/department segmentation from SSO claims.** When the SSO edge can carry
  group/department information, edge mode should map those claims to Arbitron's
  existing department/visibility model so users see only their scope. Deferred
  until the claim is available.
- **Scheduling dashboard.** A UI for viewing and managing schedules and their
  run history is a follow-on to the API-level management in Phase 3.
- **Multi-instance scale-out.** Horizontal scaling and the datastore changes it
  implies are explicitly deferred; the single-instance scheduler and embedded
  database are sufficient to start.
- **Run-now / manual fire of a schedule.** An on-demand "run this scheduled
  workflow now" convenience may be added with the scheduling surface.
