# LR-04 listing and error contract evidence

Accessed **2026-11-11 UTC**:

- **Errors & rate limits**: https://docs.lunaroute.com/api/errors/;
  Markdown https://docs.lunaroute.com/api/errors.md (HTTP 200).
- **Authentication**: https://docs.lunaroute.com/getting-started/authentication/;
  Markdown https://docs.lunaroute.com/getting-started/authentication.md (HTTP 200).
- **Models API**: https://docs.lunaroute.com/api/models/;
  Markdown https://docs.lunaroute.com/api/models.md (HTTP 200).

## Listing

The docs say `GET /v1/models` is OpenAI-shaped without dialect selectors and
entries have an `id`. LR-04 fixes the parser contract to
`{"data":[{"id":"<model>"}]}`. The docs illustrate an individual entry, not a
complete listing envelope. The operator's 2026-11-11 observation
`observations/lr04-discovery-chat.json` confirms the complete `data` wrapper
and nonempty listing, as does R1's direct gateway observation.
Neither `anthropic-version` nor `X-LunaRoute-Client` is sent when listing.
A missing wrapper or malformed id is a protocol failure, not empty availability.

## Error envelopes

Errors & rate limits publishes `{"error":{"code":"INVALID_API_KEY","message":"…"}}`.
It says most Anthropic errors use that envelope, and some are wrapped as
`{"type":"error","error":{…}}`. Arbitron additionally accepts a bare code
object per the LR-04 decision. Only code/type schema is retained in
observations; messages are never evidence output. Conflicting top/nested codes
are unknown, not permission to choose one.

## Documented code table

| Code | Documented status/context | Arbitron outcome |
| --- | --- | --- |
| INVALID_API_KEY | 401, Authentication | credentials rejected |
| CONCURRENT_REQUEST_LIMIT_EXCEEDED | 429, Chat/Messages/Responses | busy |
| INFERENCE_UNAVAILABLE | 503, no usable in-house target | service failure |
| NO_HEALTHY_PROVIDER | 503, no usable in-house target | service failure |
| PROVIDER_KEY_UNAVAILABLE | 503, BYOK key store | service failure |
| BYOK_SPEND_GATE_UNAVAILABLE | 503, Chat/Messages/Responses BYOK | service failure |
| BYOK_SPEND_CAP_REACHED | 429, Chat/Messages/Responses BYOK | non-retryable request refusal |
| MODEL_UNPRICED | 429, Chat/Messages/Responses BYOK | non-retryable request refusal |

The busy code documents Retry-After 60; the spend-cap codes explicitly have
no Retry-After and plain retry does not help. Body codes refine HTTP status,
including account/pricing refusal on 429. This table agrees with P1-T2's
production table. No MODEL_NOT_FOUND or excluded-modality `*_busy` code is
added. R4's observed `INFERENCE_MODEL_NOT_FOUND` now establishes availability
failure directly, with a fresh successful re-list as corroboration, never
message prose.

## Live confirmation

The operator's 2026-11-11 [R4 ruling](../../designs/2026-11-10-lunaroute-provider-decisions.md#r4--lr-04-codes-observed-live-in-addition-to-the-documented-table)
and committed [discovery/chat observation](observations/lr04-discovery-chat.json)
confirm these additional scoped codes:

| Code | Observed status/context | Arbitron outcome |
| --- | --- | --- |
| MISSING_LUNAROUTE_HEADER | 401, no auth header (R4) | credentials rejected |
| INFERENCE_MODEL_NOT_FOUND | 404, unavailable inference model | model unavailable |

The invalid-key record confirms `INVALID_API_KEY`/401. The unavailable-model
record used `claude-sonnet-5-lr1`, absent from the fresh listing. Its historical
`classified_as: cause_unknown` reflects the old classifier; retain the genuine
artifact unchanged, but apply R4's model-unavailable classification now. That
revoked id is no longer executable and cannot be supplied to the current
collector; future negative runs require an absent **canonical reviewed** id.

The production classifier, probe and chat sinks apply these codes for all
operations, with known codes overriding status. Bare 404 and undocumented
`MODEL_NOT_FOUND` remain cause unknown; the re-list still corroborates current
availability. Unobserved documented 429/503 codes retain their documented
mappings without being labelled live-observed. LR-04 is verified from this
operator evidence plus its runtime application, not from offline fixtures.

R4 also requires at least 1024 output tokens for content-bearing reasoning-model
probes: 64 intermittently consumed the entire budget on reasoning. This is a
smoke-test budget, not a fabricated model capability or production minimum.
