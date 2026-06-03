# Arbitron Phase 6 — Multi-User & Auth

## Overview

Phase 6 turns the Arbitron from a single-user local tool into a multi-user platform with real identities, roles, private and shared workspaces, and a publishing model — without disturbing the zero-friction local experience that phases 1–5 shipped.

The system runs in one of two modes, selected by configuration:

- **Local mode** (default, unchanged) — a single auto-authenticated default user with admin rights, no login flow. This is the current "just run it" experience, preserved exactly.
- **Org mode** — every request carries a real, authenticated identity. There is no anonymous access. Users authenticate via OIDC (browser) or tokens/device-code (non-browser clients), are provisioned automatically on first login, and are scoped to what they own unless they are admins.

Phase 6 also closes the authentication gaps left open in earlier phases: in org mode, **every** request-handling surface is authenticated — the REST API, the WebSocket streams, the interactive-session endpoints, and the MCP endpoint. None of these may be reachable anonymously when org mode is on.

Out of scope: scheduled and webhook triggers (Phase 7) and parallel fan-out/fan-in execution (Phase 8).

---

## 1. Operating Modes

### Local Mode (default)

- A default user is auto-created and every request is auto-authenticated as that user.
- The default user is implicitly an **admin** and sees everything.
- No login flow, no tokens required, no IdP configuration. Identical to the current experience.
- The default user's existing workflow store is treated as their private workspace; the shared directory is available but, with one user, has no practical visibility boundary.

### Org Mode (configured)

- Selected by an explicit configuration flag.
- Every request must resolve to an authenticated user. Unauthenticated requests to protected surfaces are rejected.
- All ownership, workspace, visibility, and role rules below apply.

The two modes share one data model and one set of rules. Local mode is org mode with a single, pre-authenticated, admin user — not a separate code path with different semantics.

---

## 2. Identity & Authentication (Org Mode)

### OIDC for Browser Sessions

- Arbitron performs the OIDC authorization-code flow itself: it redirects the user to the configured identity provider, exchanges the returned code for tokens, and establishes a session.
- The dashboard authenticates via the resulting **session** (cookie-based). An unauthenticated browser request is redirected into the login flow; on success the user lands back where they intended to go.
- The identity provider is **swappable via configuration** — Entra, Google, Okta, or any standards-compliant OIDC provider. Nothing in the product is tied to a specific vendor. The choice of provider for a given deployment is a configuration concern.
- Sessions expire after a configurable lifetime and can be ended by logging out.

### Tokens & Device-Code for Non-Browser Clients

Non-browser clients — the MCP server / Claude Code, the CLI, and scripts hitting the REST API — cannot perform an interactive browser redirect. Org mode provides two ways for them to authenticate, both resolving to a real user with that user's role and ownership:

- **Personal API tokens** — a logged-in user mints one or more personal access tokens from the dashboard. A token is presented by the client as a bearer credential on each request. Tokens are **listable and revocable** by their owner; revoking a token immediately invalidates it. Each token records a label and last-used information so users can tell them apart and spot stale ones.
- **OIDC device-code login** — a client with no browser initiates a device-authorization flow: it displays a short code and a URL, the user approves in any browser, and the client receives a credential it caches locally. This gives Claude Code and CLI users a first-run login that needs no manual token copying.

A personal API token carries the **full permissions of its owning user**. There are no granular per-token permission scopes in this phase — tokens are a credential for "act as me," made safe by being individually revocable. (Scoped tokens are a possible future refinement, not a Phase 6 deliverable.)

### Coverage of All Surfaces

In org mode, authentication is enforced on every request-handling surface, including the ones currently left open:

- REST API endpoints
- WebSocket streams (system-wide stream and per-instance streams)
- Interactive-session endpoints (sending messages to and ending an instance's interactive session)
- The MCP endpoint and all MCP tools

A request that cannot be resolved to an authenticated user is rejected before it can read or change any state.

---

## 3. Users, Roles & Provisioning

### Roles

- **User** — can create, edit, and delete their own workflows; trigger workflows and manage their own instances; view their own execution traces, variable history, and notifications; publish to and copy from the shared directory.
- **Admin** — everything a User can do, plus: view all users' instances and traces, intervene in any user's instances, configure system-level settings (parallelism caps, LLM backend, notification settings), and manage users' roles.

### Just-In-Time Provisioning

- In org mode, a user account is created automatically on the user's **first successful login**, from their verified identity claims (stable identifier, username/display name). No pre-registration or invitation step is required.
- Newly provisioned accounts default to the **User** role.

### Bootstrapping & Role Management

- The **first admin(s)** are established by configuration — a bootstrap list of admin identities (e.g. email or subject). On first login, an identity on that list is provisioned as an admin.
- After bootstrap, **admins promote and demote** other users between User and Admin from within the application. The bootstrap list remains a floor (a configured bootstrap admin cannot be locked out by demotion).
- Role changes take effect on the user's subsequent requests.

---

## 4. Workspaces & Publishing

### Private Workspaces

- Each user has a **private workspace** containing their own workflow definitions. Only that user (and admins) can see or modify workflows in it.

### The Shared Directory

- There is one **shared directory** of published workflows, visible to all authenticated users and **read-only** to everyone through normal use.
- Nobody edits a shared workflow in place. The original publisher edits their private copy and re-publishes; any other user copies it into their own workspace first.

### Publishing

- **Publishing** copies a workflow folder from a user's private workspace into the shared directory.
- Workflow **names are unique** within the shared directory.
- Only the **original publisher** can overwrite their shared workflow by re-publishing it. Another user who wants to share a modified version must publish it under a new, unique name.
- **Re-publishing replaces the shared copy but does not affect instances already queued or running** — instances snapshot their workflow definition at creation time, so in-flight executions are never disturbed by a re-publish.
- Publishing is **self-service**: any User may publish. There is no admin approval gate in this phase.

### Running & Copying Shared Workflows

- A shared workflow can be **run directly**: any user may trigger it in place. The resulting instance snapshots the shared definition at trigger time, so a later re-publish does not change what an already-running instance executes.
- **Copying** a shared workflow into your private workspace is required only when you want to **modify** it. A copy is independent and owned by the copier; it has no further link to the shared original.

---

## 5. Visibility & Enforcement

### What Each Role Sees

- **Users** see only their **own** workflow instances, execution traces, variable history, and notifications, plus their own private workflows and the shared directory.
- **Admins** see **everything** across all users — all instances, traces, and workflows.
- The shared directory is visible to all authenticated users regardless of role.

### Consistent Enforcement

Visibility and permission rules are enforced **server-side at every interface**, not merely hidden in the dashboard:

- REST list and detail endpoints return only what the requesting user is permitted to see, and reject actions they are not permitted to take.
- WebSocket streams only deliver events for instances the subscriber is allowed to observe.
- MCP tools apply the same rules as the REST API: admin-only operations exist as tools but are rejected for non-admin callers, and listing/reading tools are scoped to the caller's visibility.

A non-admin user cannot read, stream, act on, or even confirm the existence of another user's instance through any interface.

---

## 6. Admin Intervention

Admins can observe and act on any user's work:

- View any instance's full execution trace, variable history, and live output streams.
- **Intervene** in any user's instance: pause, resume, cancel, set priority, retry, and re-run.
- **Participate in any interactive step** — send messages to and end an interactive session on any user's instance.
- Configure system-level settings (parallelism caps, LLM backend configuration, notification settings) and manage user roles.

Admin actions taken on another user's instance are **attributed** in that instance's execution record, so the owner and other admins can see that an admin intervened and what they did.

---

## 7. Migration & Compatibility

- An existing **local-mode** install continues to work with no changes and no migration required.
- When an existing install is switched to **org mode**, the data created under the former default user is **not orphaned**: it is assigned to a designated bootstrap admin so that existing workflows and instances remain owned, visible, and operable. No execution history or workflow definitions are lost in the transition.
- The data model is unchanged in shape — the existing users table (identity, username, role) and instance ownership are the foundations this phase builds on, now populated with real users instead of a single default.

---

## 8. Success Criteria

Phase 6 is complete when:

1. **Local mode is unchanged** — running the server with no auth configuration yields the current single-user, no-login, admin-everywhere experience, and all phase 1–5 behavior is intact.
2. **Org mode authenticates every surface** — with org mode enabled, REST, WebSocket, interactive, and MCP requests all require a valid identity, and anonymous access to any of them is rejected.
3. **Browser login works end-to-end** — an unauthenticated dashboard visit drives the OIDC flow against the configured provider and returns the user to their destination with a working session.
4. **Non-browser clients can authenticate** — a user can mint and revoke a personal API token, and a browserless client can complete a device-code login; both let the client act as that user with the right role and ownership.
5. **JIT provisioning and bootstrap admin work** — a first-time user is auto-created as a User, a configured bootstrap identity is auto-created as an admin, and an admin can promote/demote other users.
6. **Private/shared/publishing model works** — a user can publish a private workflow to the shared directory under a unique name, only the publisher can re-publish over it, any user can run it directly, and copying produces an independent private workflow.
7. **Visibility is enforced everywhere** — a non-admin cannot see or act on another user's instances through REST, WebSocket, or MCP; an admin can; and re-publishing does not disturb running instances.
8. **Admin intervention is attributed** — an admin can pause/resume/cancel/retry/re-run and participate in interactive steps on any user's instance, and those actions are recorded against the instance.
