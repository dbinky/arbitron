# Subscription exhaustion detection for Arbitron runners

Research date: 2026-11-19. Local versions inspected: Claude Code **2.1.328** (`~/.local/share/claude/versions/2.1.328`), Codex CLI **0.170.0** (`~/.codex/packages/standalone/releases/0.170.0-aarch64-apple-darwin`).

Method: public sources (GitHub issues, official docs, the openai/codex source on `main`), plus read-only inspection of local session logs and of the string tables in the installed binaries (`strings`, no execution). No harness was run and no files were modified. Only field names, shapes, and non-sensitive literals are quoted here.

Confidence key: **H** means verified in primary source or local data. **M** means one good source, or inferred from code strings. **L** means secondary or community sources only.

---

## What Arbitron already detects (repo, `main` @ 5bf48213)

| Area | Current behaviour |
|---|---|
| `adapter/codex/parse.go:166-200` (`eventFailureCause` / `failureCause`) | On a non-zero exit, it recovers the message from `codex exec --json` stdout `error` / `turn.failed` events. It is **message text only**: no classification and no reset time. The comments say "You've hit your usage limit" with an ASCII apostrophe, but Codex actually emits a typographic `’` (U+2019, see below). Nothing string-matches on it today, but any future `strings.Contains` on the ASCII form would silently miss. |
| `adapter/codex/provider.go:258`, `exec_session.go:343` | Report the stdout cause together with the wait error. |
| `adapter/claudecli/streamstate.go:88` | A `result` with `is_error` becomes the generic `errModelExecutionEvidence`, or "claude reported an error: <text>". The `assistant.error` kind (`rate_limit` / `authentication_failed` / ...) and `api_error_status` are **not decoded** (`streamjson.go` only reads `is_error`). `rate_limit_event` is ignored. |
| `adapter/claudecli/process_error.go` | `SanitizeProcessError` replaces every subprocess error with "harness process failed", so exhaustion, expired OAuth and crashes all look the same. Memory notes confirm operators had to dig the cause out of session JSONL. |
| `adapter/pi/provider.go:30` | Documents that pi exits 0 on quota or auth refusal, so `stopReason` is the only signal. |
| `adapter/backendhealth` | Only Codex install/login readiness (`ready` / `not-installed` / `not-logged-in`). No quota state and no Claude entry. |

Net: **Arbitron detects none of the three states structurally today.** It surfaces text at best.

---

## Claude Code CLI (`claude -p`, subscription OAuth)

### A. What is emitted when the limit is hit

**Current behaviour (2.1.28x–2.1.32x, Oct–Nov 2026), verified locally (H).** There are 27 real limit hits in `~/.claude/projects/**/*.jsonl`, including 3 with `entrypoint: "sdk-cli"`, which is a `claude -p` / Arbitron run. The turn is a **synthetic assistant message**:

```jsonc
{ "type": "assistant", "isApiErrorMessage": true,
  "error": "rate_limit",                 // SDK error-kind enum
  "apiError": "usage_limit_reached",     // present from ~2.1.296/2.1.306; null on 2.1.284/2.1.286
  "apiErrorStatus": 429,
  "apiErrorParams": { "rate_limit_info": { "status": "rejected", "resetsAt": <epoch s>,
        "rateLimitType": "seven_day" | "five_hour", "overageStatus": "rejected",
        "overageDisabledReason": "...", "isUsingOverage": false } },
  "quotaLimits": { "status": "rejected", "resetsAt": <epoch s>, "rateLimitType": "...",
        "unifiedRateLimitFallbackAvailable": false, "overageStatus": "...", "upgradePaths": ["overage"], ... },
  "message": { "model": "<synthetic>", "stop_reason": "stop_sequence",
               "content": [{ "type": "text", "text": "You've hit your … limit · … resets 10pm (America/Denver)" }] } }
```

- **The reset is machine-readable (H).** `quotaLimits.resetsAt` and `apiErrorParams.rate_limit_info.resetsAt` are Unix epoch seconds, and `rateLimitType` says which window. Locally, every one of the 27 hits carried `quotaLimits`. Observed distances to reset ran from 0 to 111 h, which is consistent with 5 h and weekly windows.
- **Do not parse the human text (H).** It is localized to the machine's time zone, has no date when the reset is today ("resets 10pm"), and is misleading. Local hits on a weekly window read "You've hit your **monthly spend limit** · raise it at claude.ai/settings/usage… · your weekly limit resets…", because the copy changes with overage state. Variants seen in public reports: "You've hit your session limit · resets X", "You've hit your weekly limit · resets Jun 3 at 4pm (Europe/Berlin)" (#68816, 2026-06), and "You've hit your limit · resets 2:10pm (America/Sao_Paulo)".
- **Historic format (H, public).** Through 2025 the text was `Claude AI usage limit reached|<epoch>`, with the reset epoch after a pipe. Dozens of GitHub issues used it as their title, for example #1328 (`…|1748268000`, May 2025) and #15064 (`…|1766502000`, Dec 2025). It was later replaced by the "You've hit your … limit · resets …" copy. So **the format has changed at least twice**: pipe-epoch, then prose, then prose plus a structured `apiError`. Only the structured fields are worth depending on.
- **In stream-json (M, from the 2.1.328 SDK schema embedded in the binary).** The `assistant` event carries a top-level `error` from the enum `authentication_failed | oauth_org_not_allowed | account_on_hold | verification_required | billing_error | rate_limit | overloaded | invalid_request | model_not_found | server_error | unknown | max_output_tokens | cloud_credential_error`. The `result` event carries `is_error`, `api_error_status` (int), `api_error_code` and `api_error_type`. Before the error, a `rate_limit_event` with `status: "rejected"` is emitted (see B).
- **Gotcha (H, #68816, 2026-06, closed not-planned).** `stop_reason` is `stop_sequence`, the same as a clean finish. A consumer that looks only at stop reason treats an exhausted session as done.
- **Exit code (L/M).** Reports say `-p` still emits valid stream-json with the synthetic message. Exit-code behaviour is not documented. Arbitron's own failures (memory: "claude exited with error") imply a non-zero exit, but don't rely on it. Classify from the events.
- Related public reports (L): limits hit while `/usage` showed low use (#12829, #54177, #61828, #78617), and stuck after reset (#15064). Expect occasional false exhaustion. Re-verify after `resetsAt` rather than trusting the window percentage alone.

### B. Proactive usage signal

1. **`rate_limit_event` in `--output-format stream-json` (H for existence, M for richness).** `{"type":"rate_limit_event","rate_limit_info":{...},"uuid","session_id"}`, "emitted when rate limit info changes". Documented fields (public, 2.1.209, #78476): `status` (`allowed | allowed_warning | rejected`), `resetsAt`, `rateLimitType` (`five_hour | seven_day | seven_day_opus | seven_day_sonnet | seven_day_overage_included | overage`), `overageStatus`, `overageResetsAt`, `isUsingOverage`. Issues #77018 and #78476 (2026-07) asked for utilization in headless mode and were auto-closed not-planned. **However**, the 2.1.328 schema now has an optional `utilization` (a 0–1 fraction for the limiting window), `surpassedThreshold`, and an `@internal` `unifiedWindows` object: `five_hour` / `seven_day` / `seven_day_overage_included`, each with `{utilization, resetsAt, status, surpassedThreshold}`, read from the `anthropic-ratelimit-unified-*` response headers. Its comment says events fire "when a window's rounded percentage, reset time, status or threshold moves". It is marked `@internal` and is absent for API-key, Bedrock and Vertex sessions. **Treat it as a bonus that may disappear. `status: allowed_warning` plus `resetsAt` is the stable part.** Not verified live, because we did not run `claude`.
2. **Statusline JSON (H, official docs).** `rate_limits.five_hour.{used_percentage 0-100, resets_at epoch s}` and `rate_limits.seven_day.{…}` are documented for Pro, Max and Ultra subscribers, after the first API response. A window is dropped once its `resets_at` passes. These fields exist since about v2.1.207 per #77018. The statusline only runs in the **interactive** TUI, so `-p` runs never invoke it.
3. **`/api/oauth/usage` (L/M).** This is the endpoint behind `/usage`. The binary contains `/api/oauth/usage?skip_spend=1` and related variants. Third-party crates (cship, claude-usage, clauth) call it with the stored OAuth bearer and `anthropic-beta: oauth-2025-04-20` and get `five_hour` / `seven_day` / `seven_day_sonnet` / `seven_day_opus` objects with `utilization` (0–100) and `resets_at` (ISO 8601). It is undocumented, and calling it with Claude Code's token from another program is what the ToS forbids (see D).
4. **`/usage` and `/status` in `-p` mode (M).** Per #77018 (2.1.207), `/usage` in `-p` printed attribution without percentages. Not a reliable source.
5. **Local-log estimators** (ccusage, Claude-Code-Usage-Monitor). These sum tokens from `~/.claude/projects` JSONL and guess against plan caps. They are **estimates, not the server's meter (H)**. Fine for trend lines, not for gating.

**Windows (H for 5 h and weekly; M for per-model).** A rolling **5-hour** "session" window and a **7-day** weekly window. Per-model weekly buckets (`seven_day_opus`, `seven_day_sonnet`, `seven_day_overage_included`) apply only to some plans and accounts. When overage or "extra usage" is enabled, `overageStatus` / `isUsingOverage` mean requests continue on paid credit instead of failing. A runner should treat `isUsingOverage: true` as "spending money", not as "healthy". The caps differ by plan (Pro, Max 5x, Max 20x, Ultra). Anthropic does not publish exact numbers, so learn them empirically from `utilization`.

**Billing pool (H, support article 15036540, latest dated update 2026-10-07).** `claude -p` and Agent SDK usage on a plan login **still draw from the subscription limits**. The separate monthly Agent SDK credit announced for 2026-06-15 was paused.

### C. Distinguishing exhausted, logged-out and transient

| State | Signal (structured) | Local evidence |
|---|---|---|
| **Exhausted** | `error: "rate_limit"` **and** `quotaLimits.status == "rejected"` (or `rate_limit_event.status == "rejected"`) **and** `apiErrorStatus == 429`. Newer builds add `apiError: "usage_limit_reached"`. Reset = `resetsAt`, window = `rateLimitType`. | 27 hits |
| **Server throttle (not your quota)** | `error: "rate_limit"`, 429, **no** `quotaLimits` / `rate_limit_info.status != rejected`. Text "Server is temporarily limiting requests (not your usage limit)" (binary string; #68816). | — |
| **Overloaded** | `error: "overloaded"` (529). Texts "Repeated 529 Overloaded errors", "Opus is experiencing high load…". `api_error_type: overloaded_error`. Transient: retry with backoff, maybe on another model. | — |
| **Logged out / expired OAuth** | `error: "authentication_failed"`. Texts "Failed to authenticate: OAuth session expired and could not be refreshed" (`apiErrorStatus` null: refresh failed client-side), "Login expired · Please run /login" or "Invalid API key" (401). Also `oauth_org_not_allowed`, `account_on_hold`, `verification_required`, `billing_error`: all need a human. | **462** sdk-cli hits (Arbitron runs, 2026-11-12), 1×401 |
| **Network** | `error: "server_error"`, e.g. "API Error: Connection lost mid-response." | 1 hit |

Out-of-band auth check: `env -u ANTHROPIC_API_KEY claude auth status`. Per existing memory, a stray `ANTHROPIC_API_KEY` in the environment masks the subscription login.

### D. ToS caveats (Claude)
- OAuth is "intended exclusively for… ordinary use of Claude Code". Running the **unmodified** `claude` binary signed in with your own subscription is explicitly allowed, including when a platform hosts it (code.claude.com legal-and-compliance).
- Third parties may not "route requests through Free, Pro, Max, or Ultra plan credentials on behalf of their users" or "collect, store, or intermediate Claude.ai credentials or session tokens". Pooling several people's subscriptions behind one router, or reading the CLI's token to call `/api/oauth/usage` yourself, is in that zone.
- "Advertised usage limits… assume ordinary, individual usage". Fleet-style round-robin across accounts for one team's workload risks enforcement "without prior notice".
- A business product built on this should use Commercial terms or API keys. Server-side blocks on non-Claude-Code OAuth use have been enforced since 2026-01 (press, L).

---

## Codex CLI (`codex exec --json`, ChatGPT login)

### A. What is emitted when the limit is hit

**Source of truth (H): `codex-rs/protocol/src/error.rs` on `main`, plus the strings in the 0.170.0 binary.** The `UsageLimitReachedError { plan_type, resets_at: Option<DateTime<Utc>>, limit_window_minutes, rate_limits, promo_message, rate_limit_reached_type }` renders as one of:

- `You’ve hit your usage limit. Upgrade to Pro (https://chatgpt.com/explore/pro), visit https://chatgpt.com/settings/usage to purchase more credits or try again at <T>.`
- `You’ve hit your usage limit. Upgrade to Plus to continue using Codex (…), or try again at <T>.`
- `You’ve hit your usage limit. Visit https://chatgpt.com/settings/usage to purchase more credits or try again at <T>.`
- `You’ve hit your usage limit for <limit_name>. Switch to another model now, or try again at <T>.`
- `You’ve hit your usage limit. To get more access now, send a request to your admin…` (workspace owner/member variants)
- `You’ve hit your usage limit. Try again at <T>.` / `… Try again later.` (when `resets_at` is unknown)

`<T>` is **local time**: `%-I:%M %p` if the reset is today, otherwise `%b %-d<st|nd|rd|th>, %Y %-I:%M %p`, for example "Nov 21st, 2026 3:05 PM". The apostrophe is **U+2019**. Related messages: `UsageNotIncluded`: "To use Codex with your ChatGPT plan, upgrade to Plus…". Both map to `codex_error_info: usage_limit_exceeded`. `RetryPolicy::Never` applies: Codex does not retry internally.

- **`codex exec --json` stdout (H).** `{"type":"error","message":…}` and/or `{"type":"turn.failed","error":{"message":…}}`. `ThreadErrorEvent` has **only `message`**: no `codex_error_info`, no reset epoch. The process exits **1** (`exec/src/lib.rs` `process::exit(1)` paths; confirmed by Arbitron memory: dies in about 3 s with "exit status 1"). stderr carries only noise. On this host it is the benign "Shell snapshot validation failed" line.
- **The rollout JSONL on the runner has the structured form (H, memory plus code).** `~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<thread_id>.jsonl` gets an error event with `codex_error_info: "usage_limit_exceeded"` and a `token_count` event whose `rate_limits` shows the exhausted window (`used_percent` 100, `resets_at`, `rate_limit_reached_type`). In an earlier incident (memory, 2026-09) `limit_id` flipped from `codex` to `premium` at 100 %. The thread id is the first `thread.started` event in exec JSON, so the runner can locate the file deterministically. No current local rollout contains an exhaustion: all 5,365 `rate_limits` snapshots across 325 rollouts are non-reached.
- **Reset is machine-readable only via `rate_limits.*.resets_at` (epoch s)** or the app-server. Parsing `<T>` from the message is possible but lossy: local time zone, minute resolution, and the year is omitted when the reset is today.
- **Format history (M).** Older builds sent `resets_in_seconds` in the rollout and `x-codex-primary-used-percent` / `x-codex-primary-reset-after-seconds` headers. Current builds use `resets_at` (absolute epoch). The 0.170 binary has `x-codex-credits-*`, `x-codex-rate-limit-reached-type` and `x-codex-active-limit`. Accept both `resets_at` and `resets_in_seconds`.

### B. Proactive usage signal

1. **Rollout `token_count` events (H, verified locally).** After every model response:
   ```jsonc
   {"type":"event_msg","payload":{"type":"token_count",
     "info":{ "total_token_usage", "last_token_usage", "model_context_window", ... },
     "rate_limits":{ "limit_id":"codex", "limit_name":null,
       "primary":{"used_percent":19.0,"window_minutes":10080,"resets_at":<epoch s>},
       "secondary":null,
       "credits":{"has_credits":false,"unlimited":false,"balance":"0"},
       "individual_limit":null, "spend_control_reached":null,
       "plan_type":"prolite", "rate_limit_reached_type":null }}}
   ```
   On this machine every snapshot has `plan_type: "prolite"`, only a **weekly** primary window (10080 min) and `secondary: null`. That is consistent with Pro plans having no 5 h limit since 2026-07-12. Primary and secondary are **not** fixed to "5h" and "weekly". Always read `window_minutes`.
2. **`codex app-server` JSON-RPC `account/rateLimits/read` (H, schema in the openai/codex repo).** `GetAccountRateLimitsResponse { ordinaryUsageAllowed: boolean|null, rateLimits: RateLimitSnapshot, rateLimitsByLimitId: {[limitId]: RateLimitSnapshot}, rateLimitResetCredits }`. `RateLimitWindow { usedPercent, windowDurationMins, resetsAt }`. `rateLimitReachedType ∈ rate_limit_reached | workspace_owner_credits_depleted | workspace_member_credits_depleted | workspace_owner_usage_limit_reached | workspace_member_usage_limit_reached`. There is a push notification `account/rateLimits/updated` (sparse; merge it). The schema notes: "`ordinaryUsageAllowed`… Null means unavailable; clients must not infer recovery from percentages or reset times." **This is the best proactive probe for a runner.** It is first-party, uses the CLI's own auth inside the CLI, and spends no model tokens; it reads the backend's `/wham/usage`. App-server schemas are flagged as unstable.
3. **TUI `/status`** shows the same windows interactively. CodexBar and similar tools use the Codex OAuth API, the local CLI, or the RPC (L).

**Windows (L/M, secondary press; cross-check with OpenAI's pricing page).** Historically a 5-hour plus weekly pair. On 2026-07-12 OpenAI removed the 5 h limit for Plus, Pro and Business. It returned for **Plus** on 2026-08-25. **Pro tiers stay weekly-only**, which matches the local data. Credits (`credits.balance`, `has_credits`) let usage continue after the window when purchased. Treat `credits.balance > 0` as "spending money".

### C. Distinguishing exhausted, logged-out and transient

| State | Exec JSON (`message`) | Rollout / RPC structured | Retry? |
|---|---|---|---|
| **Exhausted** | "You’ve hit your usage limit…" or "upgrade to Plus" | `codex_error_info: usage_limit_exceeded`; `rate_limit_reached_type` set; window `used_percent` ≥ 100; `ordinaryUsageAllowed: false` | Never until `resets_at` |
| **Per-request 429** | (retried internally, `RetryPolicy` stream/backoff) | `rate_limit_exceeded` | Yes, short backoff |
| **Overloaded** | "Selected model is at capacity. Please try a different model." / "We’re currently experiencing high demand…" | `server_overloaded`, `internal_server_error` | Yes, server-directed backoff |
| **Stream / network** | "stream disconnected…", connection failed | `response_stream_disconnected`, `http_connection_failed`, `response_too_many_failed_attempts` | Yes |
| **Logged out / expired** | RefreshTokenFailed (reasons `Expired | Exhausted | Revoked | Other`; "refresh token was already used… log out and sign in again"); "Not logged in" | `unauthorized` | No: needs a human `codex login` |
| **API-key quota (not subscription)** | "Quota exceeded. Check your plan and billing details." / "Your API key has reached its spend limit." | `QuotaExceeded` / spend-limit codes | No |

Out-of-band auth check: `codex login status`, which Arbitron's `codex.Readiness` already uses. The rollout file is the reliable classifier. Exec JSON alone forces string matching, and that must handle U+2019.

### D. ToS caveats (Codex)
- ChatGPT plan usage is for the account holder. Sharing one subscription across a team or routing other users through it violates OpenAI's sharing rules (L; check the current Terms of Use and Services Agreement).
- Codex `exec` and app-server automation on your own account is a first-party, documented use. Rotating several accounts to dodge limits is the risk area.
- Business and Enterprise workspaces have admin-set limits (`workspace_*_usage_limit_reached`). Distinguish them, because the fix is "ask your admin", not "wait".

---

## Recommended detection strategy for a runner

1. **Classify from structured fields, never from prose.** Build one classifier per harness that returns `{state: exhausted | auth | transient | other, window, resetsAt (epoch), usedPct?}`.
   - Claude: decode `assistant.error`, `result.is_error`, `result.api_error_status`, `result.api_error_type`, and `rate_limit_event.rate_limit_info` from stream-json (currently dropped by `streamjson.go`). As a fallback, read the session JSONL entry with `isApiErrorMessage` and take `quotaLimits` / `apiErrorParams.rate_limit_info`.
   - Codex: after a failed or non-zero turn, open the rollout file for the `thread.started` id and read the last error `codex_error_info` and the last `token_count.rate_limits`. Fall back to a message regex that accepts both `’` and `'` ( `You[’']ve hit your usage limit` ). Bypass `SanitizeProcessError` for the *class* (an enum is safe to surface; text is not).
2. **Exhausted means park the account until `resetsAt` plus jitter**, then put it back in a "probation" state. The first job must succeed before the account is fully trusted (public reports show stale blocks after reset). Do not burn node retries: the memory notes a quota hit eats the whole retry budget. Route the node to another account and suspend it.
3. **Auth means drain and alert a human**, with no retry. Verify with `claude auth status` (with `ANTHROPIC_API_KEY` unset) or `codex login status`.
4. **Transient (529, overloaded, server throttle, 5xx, stream errors) means exponential backoff on the same account**, possibly with a model downgrade. Claude's "temporarily limiting requests (not your usage limit)" is transient even though it is a 429. Check for `quotaLimits` or `rate_limit_info.status == rejected` before calling anything exhausted.
5. **Proactive load spreading:**
   - Codex: poll `codex app-server` → `account/rateLimits/read` on each runner every few minutes and when idle. It is free, first-party, and gives `usedPercent`, `windowDurationMins`, `resetsAt` and `ordinaryUsageAllowed`. Between polls, update from every rollout `token_count`.
   - Claude: update after each turn from `rate_limit_event` (`status`, `resetsAt`, and opportunistically `utilization` / `unifiedWindows` when present). If percentages are absent, schedule on `status` alone: treat `allowed_warning` as "deprioritize". An optional fallback is a hidden statusline hook on the runner that dumps `rate_limits` to a file. It only fires in interactive sessions, so it is useless for pure `-p` fleets. Do **not** call `/api/oauth/usage` with the CLI's token (ToS).
   - Report per account `{window_minutes, used_pct, resets_at, status, overage/credits in use}` to the server. Key windows by `window_minutes` / `rateLimitType`, not by "primary/secondary". Plans differ: Pro Codex is weekly-only, and Claude may add per-model weekly buckets.
6. **Pin harness versions per runner and keep fixture tests** of the JSON shapes above. Field names have changed (pipe-epoch → prose → `apiError`; `resets_in_seconds` → `resets_at`; new `@internal` fields), and both vendors mark some of these surfaces internal or unstable.

---

## Sources

Local (read-only, 2026-11-19):
- `~/.claude/projects/**/*.jsonl`: 27 `error:"rate_limit"` records (Claude Code 2.1.284–2.1.306, 2026-10-03 → 2026-10-22), 463 `authentication_failed` (mostly 2.1.322–2.1.323, 2026-11-12), 1 `server_error`.
- `~/.codex/sessions/**/rollout-*.jsonl`: 325 files and 5,365 `token_count.rate_limits` snapshots (Codex 0.170.0, 2026-11-10 → 11-19), with no exhaustion events present.
- Strings in the Claude Code 2.1.328 binary (SDK zod schema for `rate_limit_event`, error-kind enum, statusline docs, `/api/oauth/usage`) and the Codex 0.170.0 binary (usage-limit messages, `codex_error_info` values, `x-codex-*` headers).
- Arbitron memory notes: `reference_codex_quota_exhaustion_diagnosis.md` (2026-09-12) and `reference_claudecli_harness_process_failed.md` (2026-11-12).

Web:
- Claude Code statusline docs, `rate_limits` fields (fetched 2026-11-19): https://code.claude.com/docs/en/statusline
- Claude Code legal & compliance, OAuth / Agent SDK rules (fetched 2026-11-19): https://code.claude.com/docs/en/legal-and-compliance
- Use the Claude Agent SDK with your Claude plan (latest dated entry 2026-10-07; June 15 change paused): https://support.claude.com/en/articles/15036540-use-the-claude-agent-sdk-with-your-claude-plan
- anthropics/claude-code #68816, synthetic rate-limit turn with `stop_reason: stop_sequence` (opened 2026-06-16, closed not-planned 2026-07-28): https://github.com/anthropics/claude-code/issues/68816
- anthropics/claude-code #77018, utilization absent from headless `rate_limit_event` (2026-07-13, v2.1.207): https://github.com/anthropics/claude-code/issues/77018
- anthropics/claude-code #78476, `rate_limit_event` sample payload (2026-07-17, v2.1.209): https://github.com/anthropics/claude-code/issues/78476
- Historic `Claude AI usage limit reached|<epoch>` issues, e.g. #1328 (2025-05), #15064 (2025-12): https://github.com/anthropics/claude-code/issues/1328, https://github.com/anthropics/claude-code/issues/15064
- Reports of false or stale limit blocks: #12829 https://github.com/anthropics/claude-code/issues/12829, #54177, #61828, #78617 (claudeissues.com mirrors, 2026)
- Claude Code v2.1.199 release (subagent usage-limit error propagation fix): https://github.com/anthropics/claude-code/releases/tag/v2.1.199
- openai/codex `codex-rs/protocol/src/error.rs` (main, fetched 2026-11-19): https://github.com/openai/codex/blob/main/codex-rs/protocol/src/error.rs
- openai/codex `codex-rs/exec/src/exec_events.rs` (TurnFailed / ThreadErrorEvent message-only): https://github.com/openai/codex/blob/main/codex-rs/exec/src/exec_events.rs
- openai/codex app-server rate-limit schema (`GetAccountRateLimitsResponse`, `RateLimitSnapshot`, `RateLimitWindow`, `RateLimitReachedType`): https://github.com/openai/codex/tree/main/codex-rs/app-server-protocol/schema/typescript/v2
- openai/codex PR #5302, adds `account/rateLimits/read` (2025): https://github.com/openai/codex/pull/5302
- openai/codex discussion #2251, reset-time confusion: https://github.com/openai/codex/discussions/2251
- Codex 5 h limit removed (2026-07-12) and restored for Plus (2026-08-25), Pro still weekly-only (secondary press): https://www.techrepublic.com/article/news-openai-five-hour-codex-limit-chatgpt-plus/, https://enterprisedna.co/resources/news/openai-codex-chatgpt-work-5-hour-limit-restored-plus-august-2026/
- Third-party `/api/oauth/usage` clients (schema inference only): https://docs.rs/crate/cship/1.7.1/source/src/usage_limits.rs, https://docs.rs/crate/claude-usage/0.2.3/source/README.md
- CodexBar provider sources (README, pushed 2026-11-14): https://github.com/steipete/CodexBar
- Anthropic third-party OAuth enforcement (press, 2026-02): https://www.theregister.com/2026/02/20/anthropic_clarifies_ban_third_party_claude_access
