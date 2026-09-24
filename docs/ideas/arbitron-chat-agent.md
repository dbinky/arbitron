# `@arbitron` Chat Agent — talk to Arbitron from the channel

**Status:** idea / not started. Lower priority than [teams-collaboration.md](teams-collaboration.md), and depends on it.
**Written:** 2026-09-23
**Scope:** mentioning `@arbitron` in the Teams channel starts a free-form, collaborative LLM session. That session can use local tools and the Arbitron MCP to create and manage workflows and start runs of them.
**Explicitly out of scope:** bot identity, hosting, threads, mirroring and approvals. Those belong to the Teams doc, and this agent reuses them unchanged.

---

## 1. Why this exists — and why it isn't first

dbinky can already do this: open Claude Code, chat, and have it drive Arbitron through the MCP (create a workflow, trigger it, watch it). What that can't do is happen **in the open**. It's a private terminal session, and teammates see only the results.

The chat agent brings that same capability into the channel. A teammate can say *"@arbitron, the ralph loop on the installer branch keeps stalling, look at the last run and tell us why"*, and the investigation happens in a thread everyone can read and join.

It's second because the underlying capability already exists privately. The Teams collaboration work is what's actually missing, and this agent is a thin layer on top once that exists.

---

## 2. What exists today (grounding)

- **Interactive agent nodes.** `interactive_agent` nodes already run a resumable, multi-turn coding-agent session in an instance workspace. They suspend between turns without holding a queue slot (`domain/engine/executor_interactive.go:238-252`). This is the runtime the chat agent needs; nothing new is required.
- **The claude-code agent path runs without human permission prompts.** `adapter/claudecli/agent_adapter.go:33-69`: it passes `--dangerously-skip-permissions`, and the profile's `--allowedTools` list is the *only* bound on what the agent can do.
- **The Arbitron MCP** (`adapter/mcp`) already exposes the full surface:
  - workflows: create, update, validate, publish, delete, and the rest of the lifecycle
  - instances: trigger, cancel, pause, resume, retry, rerun
  - interactive sessions, schedules, traces and notifications
  - admin: connections, profiles, tokens, user roles
- **Access to the Arbitron MCP is implicit, not wired.** No agent adapter passes an MCP configuration to its subprocess (no `--mcp-config` / `--strict-mcp-config` anywhere in `adapter/claudecli`). A claude-code subprocess launched as dbinky presumably inherits his user-level MCP servers, Arbitron included (to verify). That's convenient, but accidental: the tool surface depends on dbinky's personal `~/.claude` setup, not on the workflow.
- **Child runs** (`spawn_workflow`) can already start other workflows and, as of PR #68, read back their results.

---

## 3. The idea

`@arbitron <anything>` in the channel starts a thread and a run of a **built-in `arbitron-chat` workflow**. That is a single `interactive_agent` node on the claude-code backend, whose first turn is the message that mentioned it.

Because it is an ordinary workflow run, it gets everything for free:
- tracing, the audit trail and a trigger source
- profile and model selection through the vendor abstraction
- the queue, cancellation and dashboard mirroring
- the collaborative inbox and all-parties approval, from the Teams doc

Arbitron is used to build Arbitron. **There is no second agent runtime.**

The agent can:
- answer questions about runs, traces and workflows (read tools)
- start runs of existing workflows, including long ones like feature-pipeline, whose own threads then open in the channel
- draft, validate and publish new workflows
- intervene in runs: retry, resume, cancel
- do direct work in its instance workspace, like any `interactive_agent`

---

## 4. Proposal details

### 4a. Make the tool surface explicit

The chat workflow's profile declares its tools explicitly: an explicit MCP configuration pointing at the local Arbitron MCP with a dedicated token, plus a named set of local tools. It must not inherit whatever happens to be in dbinky's `~/.claude`. The agent's powers should be visible in the workflow definition, not dependent on one laptop's dotfiles.

### 4b. Tool tiers

| Tier | Examples | Behaviour |
|---|---|---|
| Read | `workflow_get`, `instance_get`, `trace_get`, `instance_list` | Run freely. |
| Change | `instance_trigger`, `workflow_create/update/publish`, `instance_retry/resume` | Run, and are **announced in the thread** as they happen: *"Triggered feature-pipeline (run 3f2a…)."* |
| Destructive | `instance_cancel`, `workflow_delete`, `workflow_archive` | Need a **confirmation card** clicked by a human in the thread. |
| Admin | `connection_*`, `profile_*`, `token_*`, `user_set_role`, `system_*` | **Not available** to the chat agent at all. |

The announcements are what keeps this "in the open": nothing the agent changes should be discoverable only in a trace.

### 4c. Scope of the chat

- The chat agent may only start or manage workflows that are Teams-eligible (`department` or `public`), per the Teams doc's rule. It can't be used to reach private workflows through the side door.
- One chat is one thread and one run. A thread that goes quiet ends its session after an idle period, and the run's output is the conversation's record. A later reply in the thread starts a **new** chat run seeded with the thread history. It never resumes a stale provider session, because the interactive executor's session reuse isn't built for that.

---

## 5. Prompt injection — does it matter here?

The fair argument that it doesn't: everyone in the channel is a colleague, and nothing in the channel comes from outside the org.

That's true of the *people*. It isn't true of what the agent *reads*. The gaps:
- **Jira tickets** filed through service-desk portals or email-to-ticket, or by partner and guest accounts. The planned Jira integration makes this the biggest channel.
- **GitHub content from outside contributors:** issues, PR descriptions, review comments. pr-review-style workflows read exactly this.
- **Fetched web pages and package READMEs** read by agents during research or implementation.
- **Guests or shared channels** with external tenants, if the team ever enables them.
- **Workflow outputs**, which are LLM text that may have been produced from any of the above, and which now flow from child runs to parents.

What makes it matter is the combination: text from those sources can reach an agent that runs **on dbinky's Mac, with dbinky's logins, with permission prompts skipped**, and with MCP tools that can start and change workflows. That isn't a reason not to build it, but it is the reason for §4b.

**On a classifier.** A small model that screens text for injection before the agent sees it is a reasonable extra layer. There are two kinds of candidate:

- **Dedicated injection classifiers:** Meta's Prompt Guard and ProtectAI's DeBERTa-v3 prompt-injection model. They are trained for exactly this task and are small enough for CPU.
- **General "decision" models:** [Von](https://github.com/wfzyx/von) and [Laya](https://huggingface.co/convaiinnovations/laya). Both are Apache-2.0, ModernBERT-large-sized (~395–421M parameters), and non-generative. They answer a typed question about a text (choose an option, give a score, or answer yes/no) in one forward pass, with calibrated confidence.
  - **They are not injection detectors.** Neither publishes an injection or jailbreak benchmark. Using one here means asking it a question like *"does this text try to instruct an AI assistant?"*, zero-shot or after fine-tuning, and measuring the result ourselves. Von's training mix includes security and adversarial-reasoning data, which is promising but unmeasured for this task.
  - **Local runtime fits.** Both run self-hosted. Von supports Apple Metal and ships a Python/TypeScript library, a CLI and an HTTP server (`von serve`). Laya reports roughly 200–460 ms per question on CPU and ~33 ms on a T4 GPU.
  - **Short inputs.** Laya's English checkpoint reads about 320 tokens of text per question (1,024 total for the multilingual checkpoint). Jira tickets, PR threads and READMEs would need chunking, with one call per chunk.
  - **Upside:** one local decision model could also do other cheap Arbitron jobs, like routing an `@arbitron` message to the right workflow or triaging failed runs. The limit: approval votes stay explicit button presses (Teams doc §4e), never a model's reading of someone's reply.

Whichever is chosen, treat it as **leaky by design**: good at phrasings it has seen, weak against new ones. The controls that actually hold are structural:
1. no admin tools
2. human confirmation for destructive actions
3. every change announced in the thread
4. an explicit, minimal tool list (§4a)

A classifier sits on top of those, never in place of them.

---

## 6. Decisions locked

- **`@arbitron` starts a run of a built-in chat workflow.** It is a single `interactive_agent` node, not a separate runtime.
- The chat agent can use **local tools and the Arbitron MCP**, to create and manage workflows and start runs of them.
- It acts as dbinky, on his Mac and his subscription, like everything else in the Teams doc.
- **Collaborative by default.** Anyone in the channel can talk to it, using the Teams doc's inbox and approval mechanics.
- **Lower priority** than the Teams collaboration work, and built after it.

---

## 7. Non-goals

- A second agent runtime or a bespoke chat loop outside the workflow engine.
- Admin operations from chat: connections, profiles, tokens, roles, system config.
- Reaching private workflows from chat.
- Per-person credentials. Everything acts as dbinky for now.
- A prompt-injection classifier in the first version (see open questions).

---

## 8. Open questions

1. **Classifier or not, and which kind.** Is a classifier worth adding once Jira/GitHub content is in play, or do the structural controls suffice for a three-person team? If yes: a dedicated injection model (Prompt Guard, ProtectAI) or a general decision model (Von, Laya) that also serves routing and triage? Either way, decide by testing against a small set of real and adversarial Jira and PR text, not by the published numbers.
2. **Workspace.** Does a chat get an empty workspace, or a checkout of a named repo when the conversation is about code? Probably: empty by default, with a tool to clone into it.
3. **Idle timeout** before a chat's session ends: an hour? A day?
4. **Model and profile** for the chat node: fixed, or selectable per message (e.g. `@arbitron opus: …`)?
5. **Announcement granularity.** Should read tools also be summarised in the thread (*"looked at 3 traces"*), or just changes?

---

## 9. Suggested sequencing

After Teams collaboration steps 1–3 (bot, threads, inbound replies) exist:

1. The built-in `arbitron-chat` workflow with an explicit MCP config and tool list (§4a), read tools only.
2. Change tools with in-thread announcements.
3. Destructive tools behind confirmation cards.
4. Once the Jira integration exists, evaluate a classifier against real content: dedicated injection models against Von and Laya. Run them as a local sidecar, for example `von serve`.

---

## Appendix — related

- [teams-collaboration.md](teams-collaboration.md): the plumbing this agent rides on.
- `adapter/claudecli/agent_adapter.go`: why the tool list is the only boundary.
- [decision-node.md](decision-node.md): where the injection classifier would live, as a `decision` profile.
