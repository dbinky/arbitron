# Operator manual agent tool tasks — 2026-11-12

Credential-free record of manual operator runs against the hosted gateway
(`https://gw.lunaroute.com`) with an `lr_` key held only by the operator. Each
run used the launch shape the production adapter emits. Task instructions are
omitted; only configuration and outcome metadata are retained below.

| Backend | Binary | Model | Launch shape | Result |
|---|---|---|---|---|
| codex | codex-cli 0.167.2 | `glm-5.3` | Private `CODEX_HOME` (0700, empty); `codex exec -s workspace-write -m glm-5.3` with the LR-07 `-c` overrides (`model_provider`, `model_providers.lunaroute.{name,base_url=https://gw.lunaroute.com/v1,env_key=LUNAROUTE_API_KEY,wire_api=responses}`, `review_model`) | **Pass.** Exit 0, about 75s. Created `hello.txt` (`DONE`) through `apply_patch` (`file_change` item), final message `DONE`, `turn.completed` with usage. No login was needed. Codex warned: "Model metadata for `glm-5.3` not found. Defaulting to fallback metadata" (a non-fatal degradation warning). |
| pi | pi 1.5.2 | `lunaroute/glm-5.3` | Private `PI_CODING_AGENT_DIR` (0700) containing the LR-08 `models.json` (`openai-completions`, `apiKey:"$LUNAROUTE_API_KEY"`, `authHeader:true`, `reasoning:true`, context 524288, max 131072); `pi --print --mode json --no-session --model lunaroute/glm-5.3` | **Pass.** About 45s. Created `hello.txt` (`DONE`) through the `write` tool call; `agent_settled` with `aborted:false`. |
| claude-code | (2026-11-12, earlier) | `glm-5.3` | `ANTHROPIC_BASE_URL` pointed at the gateway, with `ANTHROPIC_AUTH_TOKEN` | Worked manually (Write tool, about 30s), but **not released**: R7 leaves claude-code off for LunaRoute. Recorded for context only. |

I grepped the captured output for the key and found none. No prompts, auth
material or workspace paths beyond the scratch directories were persisted.
