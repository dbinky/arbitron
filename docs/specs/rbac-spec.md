# Arbitron RBAC & Department Dashboard — Product Specification

## Overview

Arbitron is gaining role-based access control (RBAC) so a single deployment can
serve multiple departments safely. Today, access is binary: a workflow is either
private to its owner or shared with everyone, and a user can only see the
instances they personally triggered. This is fine for a small product/engineering
team running long feature-pipeline workflows, but it does not fit a broader
enterprise deployment where many departments run their own workflows side by
side — including small, high-frequency workflows (for example, autotriaging
Zendesk tickets thousands of times a day).

This specification introduces **departments** as a unit of visibility, a
**three-tier role model**, a **three-level workflow visibility model** that
extends to instances, and a new **Dashboard** tab that gives each department a
holistic, tile-based view of how its workflows are operating.

RBAC is a feature of **auth mode** only. In local (single-user) mode the system
behaves exactly as it does today — see [Local Mode](#local-mode).

## Goals

- Let multiple departments share one Arbitron deployment without seeing each
  other's private work.
- Make workflow sharing department-aware: a workflow can be private, shared with
  a department, or shared with everyone.
- Carry a workflow's visibility through to the instances it produces, so a
  department can collaborate on shared operational workflows.
- Give each department an at-a-glance operational dashboard.
- Keep configuration simple and source-of-truth driven, working with whatever a
  customer's identity provider already emits.
- Preserve today's frictionless local-mode experience unchanged.

## Non-Goals

- **Per-department resource governance** (queue fairness, concurrency quotas,
  rate limits). One department's high-volume workflows could, in principle,
  consume a disproportionate share of workers. This is a real concern for the
  enterprise story but is **explicitly out of scope** here and will be addressed
  in a follow-up specification if needed.
- **A custom permission/role editor.** Roles are derived from configuration and
  identity claims, not authored in-app.
- **Cross-department sharing to a specific *other* department.** A workflow is
  private, shared to the publisher's own department(s), or public to all. There
  is no "share to department X that I do not belong to."
- **Full read-access auditing.** Only consequential changes are audited (see
  [Audit](#audit)).

## Definitions

- **Department (group):** A unit of visibility. A user belongs to zero or more
  departments. The terms "department" and "group" are used interchangeably.
- **Default group:** The single implicit department that holds every user who has
  no department membership (because no group claim is configured, or their token
  does not carry it).
- **Visibility:** A workflow's sharing level — `private`, `department`, or
  `public`.
- **Owner:** The user who created a workflow, or who triggered an instance.

## Roles

There are three tiers. A user's effective tier is the highest that applies.

### Global Administrator
- Sees and can act on **everything**, overriding all department membership and
  visibility rules.
- Membership is defined by the existing administrator configuration list. That
  list is now treated as a **live override, re-evaluated on every login**: a user
  named in the list is an administrator; removing a user from the list demotes
  them on their next login. This is the authoritative super-admin set.

### Department Lead
- Defined by a configuration mapping of **department → lead users**.
- Within their department(s), a lead can:
  - View all department-visible and public content (same as members).
  - **Intervene** on any instance in their department, even ones they did not
    trigger (cancel, pause, resume, retry, rerun, change priority).
  - **Publish** workflows to **public (all)**, in addition to department.
- A lead has no special powers in departments they do not lead.

### Member
- The default tier for any authenticated user.
- Sees content visible to their department(s) plus their own private content.
- Can publish workflows to **their own department(s)**, but not to public.
- Can intervene only on instances they personally own.

## Departments & Membership

### Source of membership
- A **configurable claim name** designates which identity-token claim carries a
  user's group membership. This is set by the deployment operator.
- Membership is **multi-valued**: a user may belong to several departments, and
  sees the **union** of all their departments' content.
- If **no group claim is configured**, or a user's token does not include it,
  that user belongs to the **default group** only.

### Normalization & roster
- Claim values are **normalized** (trimmed and lowercased) before use, so that
  trivial inconsistencies (`Engineering`, `engineering`, ` engineering `) do not
  create separate departments.
- The set of departments is **derived from observed, normalized claim values**.
  There is no separately maintained department registry; a new department simply
  appears the first time a user presents it.

### Persistence & non-interactive triggers
- A user's department membership is **captured at login** and retained with the
  user, so it is available even when no live token is present.
- This matters for **non-interactive triggers** — webhooks and API tokens — which
  power high-frequency workflows. Such a run resolves its department from the
  **triggering user's stored membership**.
- A trigger **may explicitly specify a department override**, but only one of the
  triggering user's own departments. An override naming a department the caller
  does not belong to is rejected.

## Workflow Visibility

Visibility replaces today's private/shared binary with three levels:

| Level | Who can see it |
|-------|----------------|
| **Private** | The owner only (plus administrators). |
| **Department** | Members of the department it was published to (plus administrators). |
| **Public** | Everyone. |

Rules:
- A **member** may set a workflow to **private** or **department** (one of their
  own departments).
- Promotion to **public** requires a **department lead or administrator**.
- **Administrators** always see all workflows regardless of visibility.
- Changing a workflow's visibility is an audited action (see [Audit](#audit)).

## Instance Visibility

A workflow's visibility **transfers to the instances it produces**:

- Each instance is **stamped with the department it ran under** at trigger time
  (a snapshot, so later reorganizations do not retroactively change historical
  runs).
- **Private** workflow → instances visible to the owner (and administrators)
  only, exactly as today.
- **Department** or **public** workflow → instances are visible to the
  appropriate audience: department members for `department`, everyone for
  `public`.

### Who can act on a visible instance
Seeing an instance is not the same as controlling it.

| Actor | View run, trace, variables | Intervene (cancel / pause / resume / retry / rerun / priority) |
|-------|:--:|:--:|
| Instance owner (triggerer) | ✓ | ✓ |
| Department lead (same department) | ✓ | ✓ |
| Administrator | ✓ | ✓ |
| Department member (peer) | ✓ | ✗ |

This lets a department collaborate around shared operational workflows — peers
can observe the triage queue's health — while keeping control of a given run with
its owner, their lead, or an admin.

## Dashboard Tab

A new **Dashboard** tab is added to the navigation, positioned **before the
"Queue" tab**. It presents a tile-based, operational view of how the user's
department's workflows are performing.

### Scope of statistics
- The Dashboard aggregates runs of **department-visible and public workflows** in
  the user's department(s) — regardless of which member triggered them.
- It intentionally **excludes members' private experiments**, which other people
  cannot open anyway.

### Tiles
The Dashboard surfaces, at minimum:
1. **Live queue health** — current counts of running, queued/backlogged, and
   waiting/paused instances, plus failures needing attention. This is the primary
   operational signal for high-frequency workflows.
2. **Throughput over time** — runs per hour/day, so a department can see volume
   and spot spikes (for example, a surge of incoming triage work).
3. **Success / failure & duration** — aggregate success rate, failure rate, and
   typical run duration across the department's workflows.
4. **Per-workflow breakdown** — a ranked list of the department's workflows by
   volume and failure rate, so an operator can tell *which* workflow is
   misbehaving, not just that something is.

### Multi-department users
- A user in **more than one department** sees an **"all my departments" union by
  default**, with a **dropdown to focus a single department**.
- A user in exactly one real department, or in the default group, simply sees
  that group with no need to choose.

## Local Mode

In local (single-user) mode, RBAC is fully inert and the experience is unchanged:
- The single local user can see and do everything.
- There is one implicit group (the default group); no claims are read.
- The Dashboard still appears and shows that single group's activity, with no
  department selector.

## Audit

Only **consequential mutations** are recorded — not ordinary reads:
- Workflow **visibility / publish changes** (who changed what, from which level
  to which).
- **Administrator overrides** (an admin acting outside normal visibility).
- **Department-lead interventions** on instances they do not own.

Per-read access is **not** logged, given the volume implied by high-frequency
workflows.

## Migration of Existing Data

When an existing deployment turns on auth mode and RBAC:
- Existing **private** workflows remain **private**.
- Existing **shared** workflows become **public (all)** — preserving today's
  "everyone can see it" behavior.
- All existing users and instances belong to the **default group** until a
  group claim provides departments. Historical instances are stamped with the
  default group.

## Configuration Summary

The deployment operator controls RBAC through configuration (auth mode only):
- **Group claim name** — which token claim carries department membership. If
  unset, everyone is in the default group.
- **Administrator list** — users who are global admins (live override,
  re-evaluated each login). Reuses the existing bootstrap-admin configuration.
- **Department leads** — a mapping of department → lead users.

No configuration is required to get a working default: with no group claim and no
leads configured, every authenticated user is a member of one default group, and
the deployment behaves like a single shared workspace with administrators.

## Success Criteria

- In auth mode, a member of department A cannot see department B's
  department-scoped workflows or their instances, and vice versa.
- A member can publish a workflow to their own department; a non-lead member
  cannot publish to public.
- A department peer can view, but not cancel/retry, a run they did not trigger;
  the run's owner and the department lead can.
- A webhook- or API-token-triggered run is correctly attributed to a department
  with no live login, and an out-of-department override is rejected.
- An administrator listed in configuration sees and controls everything; removing
  them from configuration demotes them at next login.
- A user in two departments sees a combined dashboard by default and can focus
  either department.
- Local mode behaves exactly as it does today, with the Dashboard showing the
  single group.
- Visibility changes, admin overrides, and lead interventions appear in the audit
  record; ordinary reads do not.
