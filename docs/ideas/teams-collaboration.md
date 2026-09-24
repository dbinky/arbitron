# Teams Collaboration — planning and building in the open

**Status:** idea / not started. No code, no tenant setup, no decision to build.
**Written:** 2026-09-23
**Scope:** a two-way Microsoft Teams surface for Arbitron runs. One channel, one thread per run, the web dashboard and the thread mirroring each other, a multi-party approval mode for interactive nodes, an Arbitron bot identity, local-only hosting, and the Graph-backed extras that identity makes possible.
**Explicitly out of scope:** the free-form `@arbitron` chat agent. That is its own idea with its own risks; see [arbitron-chat-agent.md](arbitron-chat-agent.md). This doc builds the plumbing it would later sit on.

---

## 1. Why this exists

We plan product and build it in the open. Arbitron is where a lot of that work now happens: brainstorms, specs, designs, plans, implementation loops, reviews. But the only place to watch or steer it is a web dashboard running on one Mac. A run is effectively a private conversation between Arbitron and whoever has the dashboard open.

The goal is to make a run something the team watches and steers together, asynchronously. Someone starts a brainstorm, the others see it unfold in a thread, anyone can add to it, and the decisions it produces carry the sign-off of the people who took part. The thread becomes the durable, readable record of *why* something was built the way it was.

Teams is the first surface because that's where the team already is. It is not the last one; see §4i.

---

## 2. What exists today (grounding)

**One-way Teams alerts already ship.** `adapter/notification/teams_notifier.go` posts notifications to a Teams Workflows (Power Automate) webhook as Adaptive Cards (`teams_payload.go:51`), configured live via the `system_update_teams_config` MCP tool. The pr-review and feature-pipeline samples also post rich cards through their own `notify-teams.sh` scripts. All of it is fire-and-forget: Teams can't reply, nothing is threaded, and nothing identifies a run's thread.

**Interactive nodes are single-speaker conversations.**
- `interactive_llm` / `interactive_agent` nodes suspend the run to `waiting` between turns and release their queue slot (`domain/engine/executor_interactive.go:238-252`).
- Turns arrive through `conversationHandler.HandleMessage` (`domain/engine/conversation.go:149`).
- A per-instance turn guard **rejects** an overlapping message outright (`conversation.go:166`, `ErrInteractiveTurnInProgress`). This was added because concurrent `claude --resume` calls corrupted the session.
- Each message is published with a role and content but **no author** (`publishInteractiveMessage`, `conversation.go:110`).
- Ending the session is a single action by a single person. There is no concept of approval, and none of multiple parties.

**Everything a thread would show is already on the event bus.** The dashboard's live view is driven by event types defined in `domain/engine/ports.go:242-255`: node started/completed/failed, instance completed/failed/cancelled, and `interactive.message`, `.token`, `.ended`, `.history`. A Teams adapter can subscribe to exactly the same stream.

**Attribution is keyed to Arbitron users.**
- A run's trigger source is one of `manual`, `schedule` or `run_now` (`domain/instance/origin.go:17-21`).
- The intervention audit records `ActorUserID`, an Arbitron user UUID, and only when the actor is not the run's owner (`domain/instance/intervention.go:26`).
- In local mode there is exactly one Arbitron user (`domain/identity/identity.go:25`), so today a teammate's action in Teams would have nowhere to be recorded.

**Visibility has three levels:** `private`, `department` and `public` (`domain/workflow/visibility.go:11-13`), stamped per instance as well (`domain/instance/instance.go:68-70`).

**Identity.** Generic OIDC login with group-claim → department mapping (`adapter/oidc/provider.go:117`, `domain/identity/groups.go`) works against Entra today, but nothing calls Microsoft Graph. Users in more than about 200 groups hit Entra's group "overage": the token carries a pointer to Graph instead of the groups, and those users silently fall into the default department.

**Concurrency.** The defaults are 20 running instances globally and 5 per user (`adapter/config/loader.go:67-68`).

**Inbound HTTP waits exist.** A `webhook_wait` node can suspend a run until an HTTP callback arrives.

---

## 3. The vision, concretely

A single Teams channel, `arbitron`. Every conversation with Arbitron is a thread in it.

> dbinky starts the feature-pipeline for "Jira saga state" from the channel. A thread opens: *"feature-pipeline started by dbinky: Jira saga state."* The brainstorm node's first question lands in the thread. dbinky answers. Sam, reading along, replies with a constraint dbinky hadn't mentioned; Arbitron folds it into its next question. Alex never posts. Arbitron drafts the spec and posts it as a card with **Approve** / **Request changes**. Approval is needed from dbinky and Sam, the two who spoke, but not Alex. Sam requests a change, Arbitron revises, both approve, and the run moves on. Hours later a design review needs sign-off, and the thread @mentions them. Meanwhile, anyone with the dashboard open sees the same conversation, the same card and the same votes, live.

The thread is the record, and the dashboard is a second view of it.

---

## 4. Proposal

### 4a. Bot identity, hosted locally

Arbitron keeps running on the Mac. Nothing runs in Azure.

- **Identity.** An Entra app registration plus a Teams bot registration gives Arbitron its own identity, so people can @mention it. The bot can be registered through the **Teams Developer Portal** (Tools → Bot management) without an Azure subscription (to verify during setup). These are directory objects in the tenant, not hosted or billed resources. A tenant admin approves the Teams app and its permissions.
- **Getting messages to the Mac.** Teams delivers messages by POSTing to a public HTTPS endpoint. There is no polling or long-poll channel for Teams bots. A **Cloudflare Tunnel** from the Mac provides that endpoint using an *outbound* connection: no open ports, no Azure. Only the bot's message path is routed through the tunnel. The dashboard and MCP stay on localhost.
- **Verification.** Every inbound request's Bot Framework JWT is checked against Microsoft's published signing keys before anything else happens. The tunnel makes the endpoint public, so this check is the actual front door.
- **Replying asynchronously.** The bot must acknowledge each message within roughly 15 seconds. All real work replies later as a *proactive* message, using a stored conversation reference (Teams' handle for posting into a thread). These references are persisted so replies survive a restart.
- **Seeing the whole thread.** By default a bot only receives messages that @mention it. Collaborative threads need it to see *every* reply in its threads, to know who has spoken (§4e) and to take replies without an @mention each time. That needs the resource-specific consent (RSC) permission `ChannelMessage.Read.Group`, which a team owner can grant for just this team.

### 4b. One channel, one thread per run

- A run **started from Teams** lives in the thread it was started from.
- A run of a Teams-eligible workflow (§4g) **started elsewhere** (dashboard, MCP, schedule, a parent's spawn) opens a new thread in the `arbitron` channel.
- **Child runs** post into their parent's thread as a nested status line, with their own thread only if they have interactive work. The spawn node's run hierarchy already exists to anchor this.
- Status updates are compact and edited in place where possible (one "progress" card that updates, not a message per node). Only moments that need attention post new messages: questions, approvals, failures, completion.

### 4c. Dashboard ↔ Teams mirroring

There is one conversation record, held in Arbitron. Teams and the dashboard are both views of it.

- A reply typed in Teams appears in the dashboard's interactive pane, attributed to its author. A reply typed in the dashboard appears in the thread, posted by the bot as *"dbinky (via dashboard): …"*.
- Approval cards (§4e) render in both places, and a vote cast in either shows up in both.
- **Messages must carry their author.** Today `interactive.message` events carry only role and content. With several humans in one conversation, "user" is no longer enough, for the LLM or for the record.

### 4d. Several people talking to one node

The turn guard's "reject overlapping messages" behavior protects the session, but in a busy thread it would bounce people's messages. The collaborative version adds an **inbox**:
- Messages that arrive while a turn is running are held, not rejected.
- When the turn finishes, everything held is delivered as the next turn, in order, each line prefixed with its author: *"Sam: …"*, *"dbinky: …"*.

The guard's real guarantee (never two concurrent resumes of one provider session) is kept. It just stops being visible as an error to humans.

### 4e. All-parties approval — the headline feature

An opt-in approval mode for interactive nodes, for any artifact the node produces: an idea, a spec, a design, a plan.

- **Who votes:** everyone who has **posted in the thread**. Lurkers don't get a vote. The voter list is frozen at the moment the artifact is put up for approval.
- **How:** the artifact is posted as a card with **Approve** / **Request changes**. Voting is an explicit action (a button, or its dashboard equivalent), **never inferred by the LLM** from someone's tone. Sign-off has to be a deliberate act.
- **Changes requested:** the requester's comments go back to the LLM, it revises, and a new round starts with a fresh voter list. Anyone who spoke in the meantime now votes.
- **Unanimous approval:** the session ends with the approved artifact as its output, and the node exits.
- **Record:** every round, voter list and vote is recorded against the run, so the approval history is part of the decision record.
- **Degenerate case:** with one participant, this is exactly today's single-person flow.

The one failure mode it must handle is **a voter who never votes**. See open questions (§8) for deadline and override options.

Revision rounds happen *inside* the node's session, so this doesn't collide with the rule against putting interactive nodes in loop bodies.

### 4f. Attribution: runs owned locally, actions attributed to people

Decision: runs stay owned by the local Arbitron user, and every tool acts with dbinky's credentials. What changes is the *record*:
- A new trigger source, `teams`.
- An **external actor** (Entra object id plus display name) is recorded on the run's origin and on every intervention: replies, votes, cancels.

The intervention table keys on an Arbitron user UUID today, so it needs a field that can name a person who has no Arbitron account.

### 4g. Which workflows can be used from Teams

Only workflows with **`department` or `public`** visibility can be started from, mirrored into or acted on through Teams. **Private workflows stay fully usable from the dashboard and MCP**; they just never touch a Teams thread.

This is enforced wherever Teams is involved: at trigger, when mirroring, and on inbound actions. The instance's own stamped visibility is what's checked, so a workflow changed to private mid-run doesn't start leaking, and a run started before a workflow was made public doesn't suddenly appear.

### 4h. Graph extras

The same app registration can carry application permissions for Microsoft Graph, which buys:
- **An overage fix.** Resolve group membership through Graph when the token says "too many groups", so those users land in the right department. This fixes a latent bug in today's OIDC path, not just the Teams path.
- **Real @mentions in hand-offs.** *"Waiting on @dbinky and @Sam to approve the design."* In async work that's arguably worth more than being mentionable.
- **User lookup:** display names and avatars for attribution in the dashboard.
- **Catch-up after downtime.** Read the thread history for messages the bot missed while the Mac was offline (§5). This needs `ChannelMessage.Read.All`, a Microsoft "protected API" that may need a separate approval request beyond admin consent, so treat it as optional.

Likely permissions: `GroupMember.Read.All` and `User.Read.All` (application), RSC `ChannelMessage.Read.Group`, and optionally `ChannelMessage.Read.All`. All need tenant admin consent.

### 4i. Teams sits behind a port

Teams is an adapter, not a dependency. The domain defines a collaboration-channel port in its own vocabulary:
- open a thread for a run, post a message, update a status card, request approval
- receive an inbound message, vote or command, with an external actor

Adaptive Cards, Bot Framework activities and Graph calls stay entirely in `adapter/`. If Teams gets painful, Slack or an open-source chat can replace it without touching the engine. This matches how model providers are already abstracted.

---

## 5. Design tensions and gotchas

**The Mac is the server.** When it sleeps, reboots or loses network, the bot is dead. Teams retries briefly, then drops the message. For three people this is acceptable if it's handled honestly:
- keep the Mac awake
- when it comes back, post *"I was offline from X to Y"*
- if the catch-up permission is granted, replay anything missed

This is the price of local-only hosting, and it should be stated plainly.

**Dashboard links from Teams don't work for teammates.** The dashboard is on the Mac's localhost. A "view in dashboard" link in a thread only works for dbinky unless the dashboard is shared, and the known-good way to share is local auth mode behind Cloudflare Access (edge auth mode breaks local MCP and API tokens). Either the thread must be self-sufficient, or the Cloudflare Access sharing gets set up alongside the bot tunnel. The two could plausibly share one tunnel with different access policies.

**Everything acts as dbinky.** Any ticket, page, commit or PR that a Teams-driven run produces appears to come from dbinky, because the CLI tools and subscriptions on the Mac are his. That is a deliberate decision for now (§6). The Arbitron-side record (§4f) is what shows who actually asked.

**Concurrency: less of a problem than it looks, with one catch.**
- Runs waiting on humans release their queue slot (§2), so a dozen idle threads cost nothing.
- **In local mode every run is owned by the one local user, so the per-user cap of 5 is effectively the ceiling for everyone.** Raising it is a one-line config change.
- The real ceilings are elsewhere:
  - subscription rate limits: Codex quota exhaustion has already happened once
  - memory and CPU on the Mac: each coding agent is a heavyweight subprocess
  - disk for per-run git workspaces

  Plan for those, not for the cap.

**Teams-to-dashboard fidelity.** Differences to decide rather than discover:
- Teams messages can be edited or deleted after Arbitron has consumed them. The first version is what counted.
- Teams formatting (HTML) and dashboard Markdown need converting.
- Long artifacts exceed Teams' message size limit (roughly 28 KB; verify). Post a summary and attach the full document as a file.

**Conversation length.** `MaxConversationTurns = 50` (`conversation.go:24`) is documented as a reference upper bound. Collaborative threads with several people will run longer than single-person ones. Check whether anything treats 50 as a limit before this ships.

**Backend choice for collaborative nodes.** Codex's interactive mode can't do MCP elicitation, so Codex interactive nodes fail their handshake. Collaborative interactive nodes should default to the claude-code backend.

**Content from outside the org.** The people in the thread are colleagues, but what the agents *read* isn't always theirs: Jira tickets filed from outside, external PRs, fetched web pages. That matters far more for the chat agent than here. See [arbitron-chat-agent.md](arbitron-chat-agent.md) §5.

---

## 6. Decisions locked

- **One channel** (`arbitron`), with a **thread per run**.
- **Anyone in the channel** can reply to interactive nodes and vote on approvals.
- **Voters are the people who posted in the thread.** Lurkers don't vote.
- **The dashboard mirrors Teams, and Teams mirrors the dashboard.** Arbitron holds the one conversation record.
- **Runs stay owned by the local user.** The Teams person who acted is recorded separately (Entra identity), in the origin and audit records.
- **All tools act as dbinky**, with his CLI logins and subscriptions, for now.
- **Only `department` and `public` workflows are usable from Teams.** Private workflows remain usable from the dashboard and MCP.
- **Everything runs locally on the Mac.** No Azure compute. A Cloudflare Tunnel carries the bot endpoint.
- **Teams sits behind a platform port.** It is an adapter and can be replaced.
- **The Graph extras are in scope** (overage, @mentions, lookup, catch-up) and ride on the same app registration.
- **Cost controls are deferred.** It's three people.

---

## 7. Non-goals

- The free-form `@arbitron` chat agent ([arbitron-chat-agent.md](arbitron-chat-agent.md)).
- Removing private visibility from Arbitron, or changing it outside Teams.
- Per-person Arbitron accounts, or per-person credentials for the tools. Everything acts as dbinky.
- Hosting in Azure or any cloud, or high availability.
- Multiple channels, per-workflow channels, or DMs from the bot, for now.
- Cost budgets or rate limiting per person.

---

## 8. Open questions

1. **A voter who never votes.** Options: the run's starter can close the vote; a deadline after which silence counts as approval; or silence counts as abstention after N hours. Default: the starter can override, and nothing times out automatically.
2. **Which runs get threads.** Every run of a department/public workflow (including scheduled ones), or only those started from Teams plus an opt-in flag per workflow? Scheduled runs could make the channel noisy.
3. **Late joiners mid-round.** Someone who posts *during* an approval round: do they join the current round's voters, or only the next one? Default: the next one, since the current list is frozen.
4. **Can one person block approval?** "Request changes" forces a revision round. Should anyone be able to force approval through? Default: no one. The starter's override in (1) is the only escape hatch.
5. **Shared dashboard.** Set up Cloudflare Access sharing for the dashboard as part of this work, or keep threads self-sufficient and leave the dashboard dbinky-only?
6. **Cancelling from Teams.** Allow anyone in the channel to cancel a run, or only the person who started it?

---

## 9. Suggested sequencing

1. **Tenant setup and bot skeleton.** App registration, Developer Portal bot, RSC consent, Cloudflare Tunnel, JWT verification, an echo reply. This proves local-only hosting end to end before anything else is built on it.
2. **Threads per run, outbound only.** Open a thread, keep a live status card, post attention events. This replaces the Power Automate alerts with threaded ones.
3. **Inbound replies.** Teams replies drive interactive nodes, with an inbox instead of rejection, authored messages, and dashboard mirroring in both directions.
4. **Attribution.** A `teams` trigger source and external actors in origin and audit records.
5. **All-parties approval mode.** Voter snapshot, cards in both surfaces, revision rounds, the recorded vote history, and the override from open question 1.
6. **Graph extras.** Overage fix first, since it also fixes the existing OIDC bug; then @mentions, lookup, and catch-up.

Steps 1–3 are the minimum that delivers "async and in the open". Step 5 is what makes it collaborative.

---

## Appendix — related

- [arbitron-chat-agent.md](arbitron-chat-agent.md): the `@arbitron` chat agent, which builds on this plumbing.
- [decision-node.md](decision-node.md): a `decision` node's `uncertain` port can hand an ambiguous call to humans in a thread.
- `docs/specs/2026-07-27-workflow-spawn-node-spec.md`: spawn hierarchy, which anchors child-run status in a parent's thread.
- PR #68: awaited children return `childOutput`, which is relevant to rendering child results in a thread.
