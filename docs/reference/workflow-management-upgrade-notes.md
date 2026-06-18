# Workflow Management — Upgrade Notes

Operational upgrade notes for the workflow-management feature (stable UUID
identity + admin lifecycle). These are deployment-facing notes, not product spec.

## BREAKING: workflow lifecycle operations now require admin

**What changed.** The workflow lifecycle operations are now gated to **admin**
callers in the domain service (`WorkflowService.requireAdmin`), which returns the
canonical `ErrForbidden` for any authenticated non-admin caller. This is enforced
for **every transport** (HTTP and MCP) because the gate lives in the domain
service, not in an adapter — there is no adapter-routable bypass.

Affected operations:

- `workflow_delete` (MCP) / `DELETE /workflows/{name}` (HTTP) — **Delete**
- `workflow_rename` — **Rename**
- `workflow_publish` / `workflow_unpublish` — **Publish / Unpublish**
- `workflow_archive` / `workflow_unarchive` — **Archive / Unarchive**
- `workflow_copy` (admin duplicate) — **Duplicate**
- workflow ownership transfer — **TransferOwnership**

This is a deliberate authz **tightening**, not a vulnerability fix: previously
some of these surfaces were reachable by non-admin callers. Security review
classified the change as operational/availability (a correct tightening), and the
gate cannot be bypassed by routing to a different adapter.

## Adapter error mapping (so denials are not mistaken for outages)

Both adapters map the lifecycle sentinels consistently:

| Service sentinel              | MCP result                         | HTTP status |
| ----------------------------- | ---------------------------------- | ----------- |
| `ErrForbidden`/`ErrUnauthorized` | `not authorized: ...`           | `403`       |
| `ErrHasRunningInstances`      | `workflow has running instances`   | `409`       |
| `ErrNotFound`                 | `workflow not found`               | `404`       |
| `ErrSharedReadOnly`           | `workflow is shared and read-only` | `409`       |

A non-admin lifecycle attempt is therefore a **403**, never a 500 — so authz
probes do not look like server faults in alerting, and clients do not retry on a
500.

## Migration / rollout sign-off

Before upgrading a non-local deployment, confirm:

1. **No non-admin caller relies on self-service delete (or rename/publish/etc.).**
   Audit any automation or integration that invokes `workflow_delete` and friends
   with a non-admin token. Such callers will now receive `403 Forbidden`.
2. **Grant admin** to any operator/service account that legitimately performs
   workflow lifecycle management, or route those operations through an
   admin-scoped credential.
3. **Local mode is unaffected** in the sense that it seeds an admin identity, so
   single-user/local deployments continue to work without change.

### Rollback story

The gate is a single domain-service check (`requireAdmin`). If a deployment must
temporarily restore the prior (looser) behavior, the rollback is to deploy the
prior binary — there is no data migration to reverse. The stable-id schema
(migrations 016/017) is additive and forward-compatible, so rolling the binary
back does not require a schema downgrade; the `workflow_id` reference columns
simply go unused by the older binary.
