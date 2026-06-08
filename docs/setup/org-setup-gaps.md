# Org Deployment — Live Verification Gaps

The org-readiness feature (edge SSO, pluggable agent backends, scheduled
triggers) is fully covered by automated tests **except** for three paths that
can only be exercised against the real work environment. CI mocks the network,
subprocess, and clock boundaries, so these must be verified by hand once during
deployment (and after any change to the relevant adapters).

Each item lists what is *not* automatically verified and the manual procedure to
confirm it.

## 1. Model routing through the real LiteLLM gateway → Azure AI Foundry

**Not covered by CI:** claude-code and opencode talking to an actual LiteLLM
endpoint. Tests use mock subprocess runners and assert the injected environment
(`ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL` + per-call key, `-m openai/<model>`),
not a real request.

**Why it can't be automated:** requires the provisioned LiteLLM proxy exposing
both surfaces (Anthropic `/v1/messages` + OpenAI `/v1/chat/completions`) in front
of Azure AI Foundry, plus a valid gateway key.

**Manual procedure:**
1. Configure a connection pointing at the LiteLLM gateway (base URL + key) and a
   model profile for it.
2. Run a workflow with an **agent node set to `backend: claude-code`** and
   confirm it completes against the gateway (claude-code uses the Anthropic
   surface).
3. Run the same with an **agent node set to `backend: opencode`** (uses the
   OpenAI surface).
4. Run a **chat / LLM node** routed through the gateway.
5. Confirm in the LiteLLM logs that all three requests arrived and reached AIF.

## 2. Edge auth against a real Cloudflare Access tenant

**Not covered by CI:** verification against Cloudflare's live JWKS and a real
application AUD. Tests use a locally generated RSA key + `httptest` JWKS.

**Why it can't be automated:** requires the Cloudflare Access application in
front of the deployment, issuing real `Cf-Access-Jwt-Assertion` tokens for the
company IdP (Entra).

**Manual procedure:**
1. Deploy with `auth.mode=edge`, `auth.edge.teamdomain=<team>.cloudflareaccess.com`,
   `auth.edge.audiences=<application AUD tag>`, and the admin email list.
2. Sign in through Cloudflare Access and confirm you reach Arbitron with **no
   second login**, recognized as the right user.
3. Confirm an email on the bootstrap list is an **admin**, and another valid user
   lands in the **default group** (sees all).
4. Confirm a request that reaches the container **without** a valid token (e.g.
   bypassing the proxy) is rejected with **401**.

## 3. Scheduler no-backfill across a real container restart

**Not covered by CI:** real wall-clock behavior across a process/container
restart. Tests use an injected clock to simulate downtime deterministically.

**Why it can't be automated:** requires a real Azure Container App instance and
elapsed wall-clock time spanning one or more scheduled occurrences.

**Manual procedure:**
1. Create a schedule (e.g. every few minutes) for a script-only workflow and
   confirm it fires on time in its timezone, attributed to you (the creator).
2. Stop the container long enough to **skip at least one occurrence**, then start
   it again.
3. Confirm the missed occurrence is **not** backfilled, a `schedule_missed`
   alert was raised, and the next occurrence fires normally.
4. While one run is still active at the next tick, confirm the overlapping
   occurrence is **skipped** with a `schedule_skipped` alert.

---

These were tracked during the review loop as the only items that could not be
closed in CI. They are not code defects — they are environment-dependent
acceptance checks for the deployment.
