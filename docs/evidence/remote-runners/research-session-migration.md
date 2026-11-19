# Moving a live `interactive_agent` session between runners

Research date: 2026-11-19. Local versions: Claude Code **2.1.328**, codex-cli **0.170.0**, Arbitron `main` @ 5bf48213.

Each claim is labelled **[CODE]** (read in the repo), **[TESTED]** (live experiment below), **[DOC]** (vendor documentation), **[COMMUNITY]** (forums, issues, third-party tools), or **[INFERRED]** (my reasoning, not verified).

---

## TL;DR / recommendation

Use **option (c), a hybrid**, in this order:

1. **Primary path: harness-native handoff (a), with checkpoints at turn boundaries.** After every *successful* turn, the runner snapshots a **session bundle**: the harness transcript, its sidecar files and minimal metadata. The server stores it as the instance's checkpoint. To move the session, the new runner imports the bundle into its own harness store, under the canonical workspace path, and resumes by id.
   - **Claude:** copy the bundle verbatim. **Do not rewrite message content.** Claude Code relocates by itself. Thinking blocks that are bound to the old account are dropped by the API with no error.
   - **Codex:** **strip `reasoning` response items** (their `encrypted_content` is bound to the org) before resuming under a different account. Otherwise the request fails with a hard 400 `invalid_encrypted_content`.
2. **Fallback: rebuild from Arbitron's own transcript (b).** Use this when the bundle is missing or corrupt, the harness versions are incompatible, the resume fails, or the Codex rollout holds org-bound compaction state. Start a fresh session whose first prompt is the original `messages[0]` (orchestration plus template, carrying the same sentinel nonce) plus a rendered transcript of every turn so far. Use summary plus recent turns only if that exceeds a token budget.
3. **Mid-turn exhaustion:** roll the session back to the last turn-boundary checkpoint, restore the workspace to its turn-start snapshot, and **re-send the same user message** automatically on the new runner. The human never retypes, and Arbitron's DB already matches this model because it persists a turn only on success.
4. **Make workspace paths identical across runners** (for example `/Users/arbitron-runner/ws/<instance-id>`, or a fixed symlink). This avoids any path rewriting. It also keeps the absolute paths the model has already seen in tool output valid.

---

## 1. How interactive sessions work in Arbitron today [CODE]

### 1.1 First turn: `InteractiveExecutor`
`domain/engine/executor_interactive.go`

- **L71-75:** short-circuit. If the node already has a `succeeded` record (the session was completed by the ConversationHandler), it returns the stored output. Only one session is ever created per node record.
- **L82-108:** loads the orchestration and template files, resolves variables, and appends a **sentinel instruction with a per-instance nonce** (`uuid.NewString()`, L104). The work dir comes from the reserved `{{workspace}}` variable (L105).
- **L114-131:** resolves `model_profile` to a binding, then `assertBindingKind`, then `selector.StreamProviderFor(binding)`.
- **L133-145:** `ChatRequest{... WorkDir, ToolScope}` with **no SessionID**, so the harness creates a new session.
- **L162-201:** consumes the stream and captures `chunk.SessionID` (the harness session id) from the chunks. **L215-217:** fails closed if no `Done` arrives.
- **L232-239:** builds the conversation record:
  `{sessionId, nonce, messages:[{role:user, content:<full orchestration+template prompt>}, {role:assistant, content:<concatenated streamed text>}]}`
- **L244-259:** persists it as `NodeExecution.LLMConversation` (DB). **L262:** sets the instance to `waiting`. The executor then returns `IsWait`.

### 1.2 Subsequent turns: `conversationHandler.HandleMessage`
`domain/engine/conversation.go`

- **L226-229:** auth/state gate (`loadAndValidate`, L645).
- **L242-245: turn guard.** `turnInProgress sync.Map` (declared at L75-86) uses `LoadOrStore(instanceID)`. An overlapping send gets `instance.ErrInteractiveTurnInProgress` (`domain/instance/instance.go:32-37`). It **rejects, it does not queue**. EndSession (L394-397) and AbandonSession take the same guard. **It is process-local memory.** It does not survive a restart and cannot coordinate several server or runner processes.
- **L250:** re-validates under the guard.
- **L264:** `parseConversation` rebuilds the message array from the DB record.
- **L267-271:** the user message is appended **in memory only**, *"not persisted until LLM succeeds"*.
- **L294-308:** re-resolves the profile on every turn. The *live* binding is used, so profile edits reach a running session.
- **L316-329:** `ChatRequest{Prompt: content (new message only), SessionID: SessionID(nodeExec.LLMConversation), WorkDir: inst.Variables["workspace"]}`. The harness carries the context, and Arbitron sends only the delta.
- **L332-344:** streams the turn and persists the new session id. If the harness returned none, it keeps the old one.
- **L355-361:** sentinel detection, scoped to the stored nonce.
- **L369-372 → `handleNormalResponse` (L860) → `persistTurn` (L887) → `nodeExecs.Update`:** the user and assistant messages are written only after a successful turn. **On failure nothing is persisted (L333-337), so the DB is always at a clean turn boundary.**

### 1.3 Backend selection
`adapter/provider/interactive_stream_selector.go:70-115` routes on `Kind` (chat or agent). Agent bindings dispatch on the **backend** axis (`claude-code`, `codex`). opencode, LunaRoute and unknown backends fail closed.

### 1.4 Claude Code adapter
- `adapter/claudecli/claude.go:56-96 buildArgs`: `--print --verbose --output-format stream-json [--dangerously-skip-permissions] [--allowedTools …] --model … [--append-system-prompt …] [--resume <SessionID>]`. The `--resume` flag is emitted at **L89-91**.
- `adapter/claudecli/stream_agent_adapter.go:50-85`: `RunSpec{Dir: req.WorkDir, Stdin: req.Prompt, Env: env}`. **The cwd is the instance workspace**, so Claude stores the session under `~/.claude/projects/<encoded workspace path>/`.
- The session id comes from stream-json events (`streamjson.go:78,143-163`, `streamstate.go:55-56,124`) and travels on the terminal chunk.
- Credentials come from the on-disk subscription login: the environment is built by `agentEnvWithRules`, and `ANTHROPIC_API_KEY` is stripped for subscription use.

### 1.5 Codex adapter
- `adapter/codex/interactive_provider.go`: **spawn-per-turn**, with no in-memory state (doc comment L50-67). `startTurn` (L134-207) requires a WorkDir (L141), then passes `ResumeID: req.SessionID` to `newSession().Start` (L190-201). `StreamChat` (L219-278) puts `ev.SessionID` (the thread id) on the terminal Done chunk (L261-266).
- `adapter/codex/exec_session.go`:
  - **L196-227:** argv. A fresh turn is `codex exec --json --skip-git-repo-check -s workspace-write -c writable_roots=[wd] -c network_access=false -m … --cd wd`. A resume is `codex exec resume <id> --json … -c sandbox_mode=… -c writable_roots=[wd] … -m … -`.
  - **L213-218:** "RESUME-CWD INVARIANT": `resume` rejects `--cd`, so the process cwd must equal the workspace.
  - **L220-227:** model, effort and sandbox are re-sent on every resume because codex does not inherit them.
  - **L110-139:** rollout verifier for pinned runs.
  - **L150-158:** `RunSpec{Dir: WorkDir, Stdin: prompt, Env: codexEnv()}`.
- `adapter/codex/env.go`: subscription-only. The environment carries no `OPENAI_API_KEY`, and `CODEX_HOME` is forwarded when set. **The account is whatever `~/.codex/auth.json` (or `$CODEX_HOME`) holds on that machine.**
- `adapter/codex/rollout.go:77`: the verifier finds rollouts by glob, `$CODEX_HOME/sessions/*/*/*/rollout-*-<threadID>.jsonl`. An imported rollout must therefore land under that glob.

### 1.6 What Arbitron itself records
- `NodeExecution.LLMConversation` = `{sessionId, nonce, messages[]}`. Keys are in `domain/engine/conventions.go:21-23`, and `SessionID()` is at L44.
- **The messages are text only:** the full first prompt, every human message, and each assistant turn's *concatenated streamed text tokens*. **Tool calls, tool results, file reads, thinking and reasoning are not recorded.** The harness transcript is the only full-fidelity copy.
- `interactive_get_history` (`adapter/mcp/tools_interactive.go:112-146`) returns `exec.LLMConversation` verbatim (L146), loaded with `GetByInstanceAndNode` at L138.
- **Existing exhaustion handling:** none is typed. Codex has string-level cause recovery for "You've hit your usage limit" (`adapter/codex/parse.go:174-190`, `exec_session.go:340-349`), and that cause is **suppressed for verified or pinned runs** (L346-348). Claude surfaces `result.is_error` (`streamjson.go:62,79,165`; `streamstate.go:88`). Nothing distinguishes "quota exhausted" from other failures today.

---

## 2. Harness session state and portability

### 2.1 Claude Code

**Layout (observed on this machine, [TESTED]/read-only):**
- `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`. The encoding replaces `/` with `-`; for example `/Users/alice/work` becomes `-Users-alice-work`. Community tools say every non-alphanumeric character becomes `-` [COMMUNITY].
- **Sidecar dir** `~/.claude/projects/<encoded-cwd>/<session-id>/`, with `tool-results/*.json` (large tool outputs) and `subagents/agent-*.jsonl` plus `.meta.json`. **The transcript references the tool-results files by absolute path**: 39 references in one local session, such as `/Users/alice/.claude/projects/-Users-alice-Repos-arbitron/<sid>/tool-results/toolu_….json`. These files must travel with the session.
- JSONL record types in 2.1.328: `user`, `assistant`, `system`, `attachment`, `api-request`, `api-request-blob`, `api-request-shape`, `session-owner` (**`accountUuid`, `organizationUuid`**), `permission-mode`, `mode`, `last-prompt`, `ai-title`, `file-history-snapshot/delta`, `cost-state`, `tools-used`, `queue-operation`, and others.
- `api-request` / `api-request-blob` records store the **exact request prefix** (system blocks, `cacheControl`, a hash chain). This backs `--system-prompt-snapshot on`, the default: *"the prompt is rendered on the conversation's first request … recorded; every later request and resume sends the record as-is"* (`claude --help`). The original system prompt, including its environment section (cwd, platform), is replayed verbatim after a move.
- Assistant `thinking` blocks carry a `signature` of about 1-1.2 KB.
- Other per-user state in `~/.claude` (`file-history/`, `session-env/`, `history.jsonl`) is not needed for `-p --resume`. `file-history` only serves rewind [INFERRED].

**Does the cwd matter?**
- [TESTED, exp 1a] On 2.1.328, `claude -p --resume <id>` run from a **different** cwd, with **no copy**, found the session in the *other* project folder by id. It answered correctly and **appended to the original file**.
- [TESTED, exp 1b] With the JSONL copied into the new cwd's encoded folder, resume used the copy and wrote to the copy.
- [TESTED] On resume from a new cwd, Claude Code **injects a system message by itself**: `# Environment update - Primary working directory: …/dirB (was …/dirA)`. The harness handles relocation natively. Older versions reportedly only resumed from the starting directory ([issue #5768](https://claudeissues.com/issue/5768-bug-resuming-sessions-only-works-from-the-directory-in-which-they-were-started)), so place the file under the new cwd's encoded folder rather than relying on the cross-project lookup.
- Copying the transcript to the new slug, optionally rewriting `cwd`, is the established community recipe ([claude-code-migrate](https://github.com/emreonal11/claude-code-migrate), [claude-repath](https://pypi.org/project/claude-repath/0.5.1/), [DEV article](https://dev.to/gowtham-sai-yadav/why-you-cannot-copy-a-claude-code-session-to-another-machine-2008), [vincentschmalbach.com](https://www.vincentschmalbach.com/migrate-claude-code-sessions-to-a-new-computer/)) [COMMUNITY].

**Is anything bound to the account?**
- `session-owner` record: [TESTED, exp 2] replacing `accountUuid` and `organizationUuid` with fake UUIDs did **not** block `--resume`, and the CLI carried the fake values forward into new records. So it is not enforced locally, at least in `-p` mode.
- **Thinking signatures:** [DOC] [Preserved thinking](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking): *"Thinking blocks from Claude Fable 5.2, Claude Mythos 5.2, Claude Sonnet 5.5, and Claude Haiku 5.5 work only in the account that produced them or a linked one. In any other account, the API drops them, with no error."* For Claude API logins, "account" means the **organization**. Also: *"A move to another account or cloud platform works the same way: keep sending the full history."* Opus 5.5 is not on the account-bound list, but its blocks *can* be omitted if the producing account "is no longer in good standing", and the request still succeeds.
- [TESTED, exp 3] Corrupting the thinking-block signature in a copied session produced **no error**. The resume answered correctly from the visible text. The cache was partly invalidated (20.6k cache-creation tokens against 43.6k cache-read before), which is consistent with the API dropping the unusable block.
- **Implication [INFERRED]:** a Claude session moved to another subscription account will resume. Hidden reasoning from Sonnet/Haiku 5.5 (and likely Fable/Mythos 5.2) turns is silently lost. Visible text, tool calls and tool results are intact. This could not be tested across real accounts because only one account exists here.
- **Prefix check hazard [DOC + INFERRED]:** blocks that remain valid, such as Opus 5.5 under another account, are tied to "what came before" them. If **message content** is edited, for example a path rewrite inside tool results, the request "can get a 400 error". **Rewrite only metadata such as `cwd` fields, never message text.** Better: make the paths identical so nothing needs rewriting.
- **Prompt cache:** caches are per org. The first turn after a move is a full cold prefill on the new account's quota.

### 2.2 Codex

**Layout (observed, [TESTED]/read-only):**
- `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<thread-id>.jsonl`. Records: `session_meta`, `turn_context`, `response_item` (`message`, `reasoning`, `custom_tool_call[_output]`, `additional_tools`), `event_msg`, `token_usage_record`, `world_state`.
- `session_meta.payload` holds `id`, `session_id`, **`cwd`**, `runtime_workspace_roots`, **`account_scoped: true`, `creator_account_id`, `creator_user_id`**, `model_provider`, `originator: codex_exec`, `history_mode`, `cli_version`, `base_instructions`.
- `turn_context.payload` holds `cwd`, `sandbox_policy`, `model`, `effort`, and more. A new one is written per turn.
- `reasoning` items carry **`encrypted_content`** of about 3-5 KB each.
- A SQLite index `~/.codex/state_5.sqlite`, table `threads`, has columns `rollout_path`, `cwd`, `creator_account_id`, `model` and others. There are also `thread_history_1.sqlite` and others.

**Does the cwd or the index matter?**
- [TESTED, exp 4] A rollout copied under a **fabricated thread id** (so it had never been indexed), placed in `sessions/2026/11/19/`, was resumed with `codex exec resume <id>` from a **different cwd**. It answered with the secret word. Codex then **indexed the file itself**: the `threads` row was created with `cwd = cxB`, and the rollout gained a new `turn_context` with the new cwd. Neither a DB import nor a cwd rewrite is needed.
- Note that `codex exec resume` without an id filters by cwd (`--all` disables that, per `codex exec resume --help`). Resuming by id does not filter.
- This matches the community recipe of copying one `.jsonl` and resuming ([zonca.dev](https://www.zonca.dev/posts/2025-12-16-copy-codex-session)) [COMMUNITY].

**Is anything bound to the account?**
- **Encrypted reasoning is bound to the org** [COMMUNITY, strong]: after switching ChatGPT login to an API key, resume fails with HTTP 400 `invalid_encrypted_content`: *"Encrypted content organization_id did not match the target organization."* ([OpenAI forum](https://community.openai.com/t/codex-app-cannot-resume-session-after-api-key-switch-encrypted-content-organization-id-mismatch/1374803), [related thread](https://community.openai.com/t/unable-to-use-apikey-to-resume-a-session-that-was-chatgpt/1361697)). The suggested fix is to delete the reasoning output items from the local conversation storage. A third-party restore tool deletes `encrypted_content` from `reasoning` items ([entireio/cli PR #878](https://gh.r-mj.com/entireio/cli/pull/878/files)). A Codex commit notes that resuming under a different provider gives "failed to decrypt" errors ([mirror](https://forge.lthn.ai/core/core-agent-ide/src/commit/5907422d6546929c8ce0dffe59f50d3bee502152/codex-rs/tui/src/lib.rs)).
- **Unlike Anthropic, this is a hard failure, not a silent drop.**
- [INFERRED] Two different ChatGPT subscription accounts are different orgs, so the move will 400 unless reasoning items are stripped. If both runners' accounts are seats in **the same ChatGPT Business/Enterprise workspace**, the org may match. That is unverified.
- [INFERRED] Dropping the **whole** `reasoning` response_item line is safer than blanking `encrypted_content`. With ChatGPT auth, codex runs `store=false`, so a bare reasoning item id cannot be dereferenced server-side.
- [INFERRED] Remote **compaction** output (`compaction` / `replacement_history` items) is likely encrypted and org-bound as well. If a rollout contains compaction state, use the transcript fallback (b).
- `account_scoped` / `creator_account_id`: strings in the 0.170 binary include TUI checks ("The Cloud account changed. Reopen the conversation before sending.", "This session belongs to a different backend"). **Whether `codex exec resume` enforces `creator_account_id` is unknown and untested** (only one account is available). The import step should be ready to rewrite `creator_account_id` / `creator_user_id` to the target runner's account, or to clear `account_scoped`. Verify on a real two-account rig first.
- Prompt cache: per org, so the first turn after a move is cold.

### 2.3 Portability summary

| | Claude Code 2.1.328 | codex-cli 0.170 |
|---|---|---|
| Find by id from another cwd | Yes [TESTED] | Yes [TESTED] |
| Needs index or DB import | No | No, it self-indexes [TESTED] |
| Sidecar files | `<sid>/tool-results`, `<sid>/subagents`, referenced by absolute path | None found |
| Relocation handling | Injects an "Environment update (was …)" system message [TESTED] | Writes a new `turn_context` cwd [TESTED]; Arbitron re-sends `writable_roots` |
| Cross-account hidden reasoning | Silently dropped for Sonnet/Haiku 5.5, Fable/Mythos 5.2 [DOC]; tampered signature gives no error [TESTED] | **Hard 400** `invalid_encrypted_content` unless stripped [COMMUNITY] |
| Local owner metadata | `session-owner`, not enforced [TESTED] | `creator_account_id` / `account_scoped`, enforcement unknown |
| Prompt cache after move | Cold (per org) | Cold (per org) |

---

## 3. Experiments (exact commands and outcomes)

All experiments ran in `<scratch>/session-exp/` (`$E`), and every prompt was trivial.

`$P=~/.claude/projects`. `$A`, `$B`, `$C` and `$D` are the encoded folders for `$E/dirA`…`dirD`. `S=c623adbe-4edc-468c-9256-0157d5025a02`.

**Exp 0 (aborted, $0):** `claude -p --model haiku …` failed with `"Invalid API key · Fix external API key"`, because `ANTHROPIC_API_KEY` in this shell overrides the subscription login. All later Claude runs use `env -u ANTHROPIC_API_KEY`. Arbitron's subscription adapters likewise must not leak that variable.

**Exp 1: copy across cwd** (Claude, model resolved to `claude-haiku-5-5`)
```
cd $E/dirA && env -u ANTHROPIC_API_KEY claude -p --model haiku --output-format json \
  "Remember this secret word: PERIWINKLE. Reply only with OK."
# -> result "OK", session c623adbe…, $0.0085; transcript has 1 thinking block (sig 1224 chars)
# 1a: no copy, resume from dirB
cd $E/dirB && env -u ANTHROPIC_API_KEY claude -p --model haiku --output-format json --resume $S \
  "What is the secret word? Reply with just the word."
# -> success; appended to dirA's file (cross-project lookup by id), entries carry cwd=dirB
# 1b: copy into dirB's encoded folder, resume again
mkdir -p $B && cp $A/$S.jsonl $B/ && (same resume command)
# -> "PERIWINKLE"; written to the copy in $B; cache_read 43,633 / cache_creation 52 (same account)
```
The resumed request blob contained `# Environment update - Primary working directory: …/dirB (was …/dirA)`.

**Exp 2: forged session owner**
```
jq -c 'if .type=="session-owner" then .accountUuid="00000000-1111-2222-3333-444444444444" |
       .organizationUuid="55555555-6666-7777-8888-999999999999" else . end' $B/$S.jsonl > $C/$S.jsonl
cd $E/dirC && env -u ANTHROPIC_API_KEY claude -p --model haiku --output-format json --resume $S "What is the secret word? …"
# -> "PERIWINKLE", is_error false. The CLI propagated the forged owner into its new session-owner record.
```

**Exp 3: corrupted thinking signature**
```
jq -c 'if .type=="assistant" then .message.content |= map(if .type=="thinking"
       then .signature = ("AAAA" + .signature[4:] | ascii_downcase) else . end) else . end' $B/$S.jsonl > $D/$S.jsonl
cd $E/dirD && env -u ANTHROPIC_API_KEY claude -p --model haiku --output-format json --resume $S "What is the secret word? …"
# -> "PERIWINKLE", is_error false; usage cache_read 23,215 / cache_creation 20,588 (prefix partly re-cached)
```
This is consistent with the documented silent drop of unusable thinking blocks. A real cross-org signature was not tested.

**Exp 4: Codex, unindexed rollout and a different cwd** (`gpt-5.6-luna`, effort low)
```
cd $E/cxA && echo "Remember this secret word: TANGERINE. Reply only with OK." | \
  codex exec --json --skip-git-repo-check -m gpt-5.6-luna -c model_reasoning_effort=low
# -> thread 01a1f5d2-0c2a-7a72-a9b9-1b1f7521c726, "OK"
T=01a1f5d2-0c2a-7a72-a9b9-1b1f7521c726; N=01a1f5d2-0c2a-7a72-a9b9-00000000beef
sed "s/$T/$N/g" ~/.codex/sessions/2026/11/19/rollout-…-$T.jsonl > ~/.codex/sessions/2026/11/19/rollout-…-$N.jsonl
cd $E/cxB && echo "What is the secret word? Reply with just the word." | \
  codex exec resume $N --json --skip-git-repo-check -m gpt-5.6-luna -c model_reasoning_effort=low -
# -> thread.started $N, "TANGERINE"; usage input 31,665 (cached 22,528), reasoning 158 tokens
# state_5.sqlite threads row for $N now exists with cwd=…/cxB; rollout gained turn_context cwd=cxB and a reasoning item (encrypted_content 3,916 chars)
```

**Artifacts left behind (all created by these tests; safe to delete):**
- `~/.claude/projects/<encoded-scratch>-session-exp-dir{A,B,C,D}/`
- `~/.codex/sessions/2026/11/19/rollout-2026-11-19T14-59-06-01a1f5d2-0c2a-7a72-a9b9-{1b1f7521c726,00000000beef}.jsonl`, plus their rows in `~/.codex/state_5.sqlite`

---

## 4. Design options

### (a) Copy the harness session file and resume on the new runner

- **Fidelity:** highest. Visible text, tool calls, tool results, subagent transcripts and the snapshotted system prompt all survive. Lost: Claude hidden thinking for account-bound models (silently), and Codex encrypted reasoning (which must be stripped).
- **Account binding risk:**
  - **Claude:** low. It resumes, at worst with less thinking. One residual risk: Opus 5.5 blocks remain valid and are prefix-checked, so any content edit could 400. Do not edit content.
  - **Codex:** **certain failure unless reasoning is stripped**. There is also the unknown `creator_account_id` enforcement.
- **Cache and cost:** the first post-move turn is a full cold prefill of the whole context on the new account. For a 150k-token brainstorm, that is about 150k input tokens charged at cache-write rates against the new subscription's limits. It is the same order as (b) and is paid once.
- **What must travel:**
  - **Claude:** the `<sid>.jsonl`, the `<sid>/` sidecar dir (`tool-results/`, `subagents/`), placed under `~/.claude/projects/<encode(new workspace)>/`.
  - **Codex:** the single `rollout-*-<tid>.jsonl`, placed under `$CODEX_HOME/sessions/YYYY/MM/DD/`, with reasoning items removed when the account changes.
  - **Both:** the workspace itself, which is already planned to move.
  - Absolute paths in tool results only stay valid if the workspace path and `$HOME` (for Claude tool-results) match. Hence the canonical path, or a symlink from the old path, on the target.
- **Fragility:** harness-version coupling, because the JSONL formats are undocumented and change often (2.1.x added `api-request*`, `session-owner`; codex added `world_state`, `account_scoped`). Pin harness versions across the runner fleet, or record `cli_version` in the bundle and refuse cross-version imports.

### (b) Rebuild a fresh session from Arbitron's transcript

- **Fidelity:** medium. It keeps every human message and every assistant reply text, and the original prompt with its nonce. It **loses tool calls and results**, which files were read, subagent work, and all hidden reasoning. For a spec brainstorm that is mostly acceptable, because the conversation is mostly text and the artifact (the spec doc) is in the workspace. It is weaker for agentic sessions.
- **Form:** replay as one primer turn: `messages[0]` + "Transcript so far: …" + "Continue; the user's next message follows". Turn-by-turn re-execution would be wrong, because it would re-run tools. Use a summary plus the last N turns only above a budget.
- **Account risk:** none. It is a brand-new session on the new account.
- **Cost:** one cold prefill of the transcript size, often smaller than (a) because tool output is not carried.
- **What must travel:** nothing beyond the workspace. Arbitron's DB already has it.
- **Fragility:** version-independent and vendor-independent. It is also the only option that would allow cross-vendor moves later, which are out of scope now.
- **Caveat:** the sentinel nonce must be re-embedded verbatim (`sentinelInstruction(nonce, …)`), or completion detection breaks (`conversation.go:355-361`).

### (c) Hybrid (recommended)

Use (a) by default, with automatic fallback to (b) on any import or resume failure. The checkpoint is captured **after each successful turn**, so the move never depends on the exhausted runner being reachable or idle. Exhausted does not mean offline, but the runner could also be dead.

### Changes in Arbitron (hexagonal)

**Domain (`domain/engine`, `domain/instance`):**
- New sentinel `engine.ErrSubscriptionExhausted`, carrying `ResetAt`, `Vendor` and `AccountRef`. Adapters must classify exhaustion: Claude's usage-limit result text, and codex's `turn.failed` "You've hit your usage limit". The classification has to happen **before** `SanitizeProcessError` hides the cause on verified runs (`exec_session.go:343-348`).
- New port, implemented per backend by adapters and executed on the runner:
  ```go
  type SessionBundle struct {
      Backend, SessionID, HarnessVersion, OriginWorkDir string
      AccountFingerprint string // opaque, never credentials
      Checksum string
      Ref BlobRef // stored server-side, not inline in LLMConversation
  }
  type SessionPortability interface {
      Export(ctx, workDir, sessionID string) (SessionBundle, error)                  // turn-boundary checkpoint
      Import(ctx, b SessionBundle, workDir string, sameAccount bool) (sessionID string, err error) // strips account-bound items when !sameAccount
  }
  ```
- A domain `SessionMover` service in `ConversationHandler`, roughly: acquire the guard → pick a runner with the same vendor/model group that is not exhausted → move the workspace → `Import` (or the fallback `RebuildPrompt(messages, nonce)`) → update the record → release.
- `LLMConversation` gains `runnerId`, `checkpointRef`, `generation` (fencing token), and a `moves[]` audit list. The record's `messages` remain the source of truth for (b) and for history.
- **The turn guard becomes a durable lease** (DB row or CAS on `generation`) instead of `sync.Map` (`conversation.go:86`). A late result from the old runner with a stale generation is discarded.

**Adapters:** `adapter/claudecli` and `adapter/codex` implement `SessionPortability`. Codex `Import` drops `response_item/reasoning` lines and refuses compaction items, falling back to (b). It may also rewrite `creator_account_id` once that is verified as needed. The runner protocol adapter carries `Export`/`Import` RPCs. The selector (`interactive_stream_selector.go`) is unchanged: the backend axis stays the same, and only the runner placement changes.

**`cmd/arbitron`:** wires the bundle blob store (filesystem/SQLite) and the mover.

### Failure modes

- **Move requested during an in-flight turn:** the mover must take the same lease. If a turn is running, wait for it to finish or fail; never move concurrently. Two writers on one harness session is exactly the corruption the existing guard prevents (see memory: concurrent `claude --resume` poisoned sessions).
- **Old runner still running after a move** (for example, a retry relaunch; pause does not stop retries): fence it by generation, and kill the old runner's child process before importing.
- **Import succeeds but the first resume 400s:** fall back to (b) automatically. Record that the move was lossy.
- **Version skew between runners:** refuse (a) and use (b).
- **Workspace and harness transcript out of sync:** keep the checkpoint and workspace snapshot paired by turn index.

---

## 5. Exhaustion mid-turn vs between turns

**Between turns** (the next send fails before any output, or quota is pre-checked): this is clean. The DB and the checkpoint are at the same turn boundary. Move, then re-send the user's message on the new runner. The user sees "moved to runner X" and continues.

**Mid-turn** (the turn fails after partial output or tool actions):
- **Arbitron state is already clean.** The user message and partial reply were never persisted (`conversation.go:267-271, 333-337`), and `InteractiveExecutor` fails closed without Done (`executor_interactive.go:215-217`).
- **The harness transcript is dirty.** Claude writes JSONL incrementally, so it may end with a dangling `tool_use`. Codex will have a partial rollout turn.
- **The workspace may be dirty**, because `interactive_agent` tools edit files.
- **Recommended:** restore the **pre-turn checkpoint bundle**, not the live file. Restore the workspace to its **turn-start snapshot** (a cheap git stash/commit or a copy taken when the turn begins). Then automatically re-send the same message on the new runner. The streamed partial tokens the client already saw should be cleared with a "turn restarted on another runner" event.
- **Alternative (not default):** resume from the partial transcript with "your previous turn was interrupted; continue". This keeps the partial work but carries dangling tool state. Claude Code usually patches interrupted tool calls; codex partial turns are less predictable [INFERRED]. It is also riskier for the first-turn executor case.
- **First-turn special case:** if exhaustion hits during `InteractiveExecutor.Execute`, no record exists yet. Simply re-execute the node on another runner. A fresh session is cheap and needs no bundle.

---

## Sources
- Anthropic, Preserved thinking (account-bound thinking; silent drop): https://platform.claude.com/docs/en/build-with-claude/preserved-thinking
- Anthropic, Fix a thinking block bound to a different conversation: https://platform.claude.com/docs/en/build-with-claude/preserved-thinking/fix-bound-to-a-different-conversation
- Claude Code resume-only-from-start-dir (older behaviour): https://claudeissues.com/issue/5768-bug-resuming-sessions-only-works-from-the-directory-in-which-they-were-started
- Resume-picker excludes moved sessions: https://claudeissues.com/issue/58475-resume-picker-excludes-sessions-after-moving-them-to-a-new-project-directory
- Thinking-signature resume bugs: https://claudeissues.com/issue/21726-bug-session-resume-fails-with-invalid-signature-in-thinking-block-thinking-block , https://claudeissues.com/issue/63147-resuming-an-extended-thinking-session-fails-permanently-with-400-thinking-blocks
- Why you can't copy a Claude Code session: https://dev.to/gowtham-sai-yadav/why-you-cannot-copy-a-claude-code-session-to-another-machine-2008
- claude-code-migrate: https://github.com/emreonal11/claude-code-migrate ; claude-repath: https://pypi.org/project/claude-repath/0.5.1/ ; https://www.vincentschmalbach.com/migrate-claude-code-sessions-to-a-new-computer/
- Codex org mismatch on resume: https://community.openai.com/t/codex-app-cannot-resume-session-after-api-key-switch-encrypted-content-organization-id-mismatch/1374803 ; https://community.openai.com/t/unable-to-use-apikey-to-resume-a-session-that-was-chatgpt/1361697
- Copy a Codex session: https://www.zonca.dev/posts/2025-12-16-copy-codex-session
- Reasoning `encrypted_content` stripping in a restore tool: https://gh.r-mj.com/entireio/cli/pull/878/files
- Codex "failed to decrypt" on provider change: https://forge.lthn.ai/core/core-agent-ide/src/commit/5907422d6546929c8ce0dffe59f50d3bee502152/codex-rs/tui/src/lib.rs
- Codex session management overview: https://codex.danielvaughan.com/2026/04/13/codex-cli-session-management-resume-fork-transcripts
