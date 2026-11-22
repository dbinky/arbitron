# Observed: `codex app-server` stdio JSON-RPC and `account/rateLimits/read` (Codex CLI 0.171.0)

Observed 2026-11-22 on macOS (Apple M2), codex-cli **0.171.0**, during plan dependency resolution (RR-03, P1 T12 `RateLimitsRead`, consumed by P1 T13 `CodexProber`). The logged-in read is token-free and read-only and was run with the operator's approval. Account ID, credit IDs, host name and installation ID are redacted. This file is the **producer** for the P1 T12 app-server fixtures.

## Request (written to stdin, one JSON object per line, no `jsonrpc` field)

```jsonl
{"id":0,"method":"initialize","params":{"clientInfo":{"name":"probe","title":"probe","version":"1"}}}
{"method":"initialized"}
{"id":1,"method":"account/rateLimits/read"}
```

## Framing and termination

| Question (P1 T12 ESCALATION) | Observed |
|---|---|
| Header-less JSON-RPC? | **Yes.** One JSON object per stdout line; responses carry `id` and `result`/`error`, no `jsonrpc`. stderr empty. |
| Unsolicited messages? | **Yes.** A notification without `id` is interleaved before the id-1 answer, e.g. `{"method":"remoteControl/status/changed","params":{"status":"disabled","serverName":"<redacted>","installationId":"<redacted>","environmentId":null},"emittedAtMs":1795354914585}`. Clients must skip messages without a matching `id`. |
| Answers before exiting on stdin EOF? | **No.** With stdin closed immediately after the three lines, the server answered id 0 (`initialize`), emitted the notification, and **exited 0 without answering id 1**. With stdin held open, id 1 was answered (< 1 s logged out, ~1–2 s logged in). |

## `initialize` result

```json
{"id":0,"result":{"userAgent":"probe/0.171.0 (Mac OS 26.5.2; arm64) unknown (probe; 1)","codexHome":"<redacted>","platformFamily":"unix","platformOs":"macos"}}
```

## `account/rateLimits/read` — logged out (empty `CODEX_HOME`)

```json
{"error":{"code":-32600,"message":"codex account authentication required to read rate limits"},"id":1}
```

## `account/rateLimits/read` — logged in (ChatGPT subscription)

```json
{"id":1,"result":{
  "ordinaryUsageAllowed":true,
  "rateLimits":{"limitId":"codex","limitName":null,"normalModelSlug":null,
    "primary":{"usedPercent":30,"windowDurationMins":10080,"resetsAt":1795565327},
    "secondary":null,
    "credits":{"hasCredits":false,"unlimited":false,"balance":"0"},
    "individualLimit":null,"spendControlReached":false,"planType":"prolite","rateLimitReachedType":null},
  "rateLimitsByLimitId":{"codex":{"limitId":"codex","limitName":null,"normalModelSlug":null,
    "primary":{"usedPercent":30,"windowDurationMins":10080,"resetsAt":1795565327},
    "secondary":null,
    "credits":{"hasCredits":false,"unlimited":false,"balance":"0"},
    "individualLimit":null,"spendControlReached":false,"planType":"prolite","rateLimitReachedType":null}},
  "rateLimitResetCredits":{"availableCount":3,"credits":[{"id":"<redacted>","resetType":"codexRateLimits","status":"available","grantedAt":1793478476,"expiresAt":1796070476,"title":"Full reset","description":"<prose>"}]},
  "accountId":"<redacted>",
  "rateLimitUpsell":null}}
```

`usedPercent` is an **integer** here (`30`); `resetsAt` is epoch **seconds**. Extra keys (`credits`, `planType`, `rateLimitResetCredits`, `accountId`, `rateLimitUpsell`, `normalModelSlug`, `spendControlReached`) are ignored and never stored or published.

## Contract derived (decision 2026-11-22, RR-03)

- `RateLimitsRead` owns a **dedicated stdio session** (not `claudecli.CommandRunner` with a fixed stdin string): spawn `codex app-server`, write the three request lines, **keep stdin open**, read stdout line by line, skip every message whose `id` is not `1`, stop at the id-1 message, then close stdin and kill/reap the process. The whole exchange is bounded by `probeTimeout` and `probeMaxResponseBytes`; exceeding either kills the process.
- id-1 `result` decodes `ordinaryUsageAllowed` (bool or null → tri-state), `rateLimits` and `rateLimitsByLimitId` windows (`usedPercent` as JSON number, integer or float; `windowDurationMins`; `resetsAt` seconds or milliseconds normalized as elsewhere).
- id-1 `error` with **any** code (including the observed `-32600` logged-out answer), EOF before id 1, timeout, or malformed JSON → `ErrRateLimitsUnavailable` → `CodexProber` reports capacity **unknown**. The prober never classifies logged-out from this error's prose; Codex logged-out detection comes from run outcomes (`codex-logged-out.md`).
