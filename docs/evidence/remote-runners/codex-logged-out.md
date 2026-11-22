# Observed: `codex exec --json` with no credentials (Codex CLI 0.171.0)

Observed 2026-11-22 on macOS (Apple M2), codex-cli **0.171.0**, during plan dependency resolution (RR-03, P1 T12). Run against an **empty temporary `CODEX_HOME`** (`OPENAI_API_KEY`/`CODEX_API_KEY` unset), so the operator's real login was not touched and no tokens were spent. Request IDs, cf-ray values, thread/turn IDs and paths are redacted. This file is the **producer** for the P1 T12 logged-out fixtures (`exec_stdout_logged_out.jsonl`, `rollout_logged_out_401.jsonl`); copy them from here.

Command: `CODEX_HOME=$(mktemp -d) codex exec --json --skip-git-repo-check "Reply OK" </dev/null` in an empty git repo. Exit code **1**. Duration ≈ 15 s (5 WebSocket retries, then HTTPS fallback with 5 more retries).

## stdout (`--json`), abridged to distinct shapes

```jsonl
{"type":"thread.started","thread_id":"<redacted>"}
{"type":"turn.started"}
{"type":"error","message":"Reconnecting... 2/5 (unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: wss://api.openai.com/v1/responses, cf-ray: <redacted>)"}
{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Falling back from WebSockets to HTTPS transport. unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: wss://api.openai.com/v1/responses, cf-ray: <redacted>"}}
{"type":"error","message":"Reconnecting... 1/5 (unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses, cf-ray: <redacted>, request id: <redacted>)"}
{"type":"error","message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses, cf-ray: <redacted>, request id: <redacted>"}
{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header, url: https://api.openai.com/v1/responses, cf-ray: <redacted>, request id: <redacted>"}}
```

stdout carries **prose only** — no structured code. It must not be classified (prose ban); it only marks the turn as failed.

## Rollout (`$CODEX_HOME/sessions/YYYY/MM/DD/rollout-*.jsonl`), structured records

There is **no** `event_msg` of `type: "error"` in the rollout for this failure. The structured cause is on the terminal `task_complete` event:

```json
{"type":"event_msg","payload":{"type":"task_complete","root_turn_id":"<redacted>","turn_id":"<redacted>","last_agent_message":null,"error":{"message":"<prose>","codex_error_info":{"http_connection_failed":{"http_status_code":401}}},"started_at":1795354882,"completed_at":1795354897,"duration_ms":15073,"message":null}}
```

Other rollout records present (not outcome-bearing): `session_meta` (with `cli_version: "0.171.0"`), `task_started`, `response_item` (instructions/tools/user message), `world_state`, `turn_context`, `thread_settings_applied`, `item_completed`, `configuration_update`. No `token_count` record is written when no request succeeds.

## Contract derived (decision 2026-11-22, RR-03)

- The rollout reader takes `codex_error_info` from **both** `event_msg` `error` payloads **and** `task_complete.error`; the last one in the current turn decides.
- Object-form `codex_error_info` whose inner object has `http_status_code` **401 or 403** (under any key, observed `http_connection_failed`) classifies **logged_out**. Other object forms (including `http_connection_failed` with other or null status codes, `response_stream_disconnected`) keep their existing transient mapping.
- The string form `"unauthorized"` (synthetic, from codex-rs source; expired/reused refresh token) remains logged_out; it is not observed here.
