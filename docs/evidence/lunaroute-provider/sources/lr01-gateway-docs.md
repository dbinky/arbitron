# LR-01 gateway documentation source

Accessed **2026-11-11 UTC**, directly from LunaRoute's published documentation:

| Title | URL | Markdown source |
| --- | --- | --- |
| API reference | https://docs.lunaroute.com/api/ | https://docs.lunaroute.com/api/index.md |
| Models API | https://docs.lunaroute.com/api/models/ | https://docs.lunaroute.com/api/models.md |
| Authentication | https://docs.lunaroute.com/getting-started/authentication/ | https://docs.lunaroute.com/getting-started/authentication.md |

The API reference states “The API is served from `https://gw.lunaroute.com`”
and lists `GET /v1/models`, `POST /v1/chat/completions`, `POST /v1/messages`
and `POST /v1/responses`. Arbitron does not use the bare `/responses` alias.

Authentication documents LunaRoute keys with an `lr_` prefix. It accepts
`Authorization: Bearer` for these keys, as well as vendor and `x-api-key`
headers. Non-LunaRoute values in standard auth headers are passed upstream as
BYOK credentials: Arbitron must reject them before forwarding. Arbitron's
LR-01 decision uses **only Authorization: Bearer on every sink**. No project
header is sent (usage attribution is outside this release).

The Models API documents an OpenAI-shaped response without `anthropic-version`;
`anthropic-version` or `X-LunaRoute-Client: claude-desktop` selects a different
shape. Listing must send neither selector. The exact `data[].id` wrapper is the
LR-04 contract and still needs live confirmation; prose saying OpenAI-shaped
is not a captured response.

This is source evidence only. No authenticated gateway experiment was possible
in this pass; no credential was supplied. It does not verify inference,
availability, helper routing, or runtime application in unimplemented adapters.
