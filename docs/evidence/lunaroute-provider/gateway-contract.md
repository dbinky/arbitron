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
complete listing envelope; **the wrapper is pending live confirmation**.
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
added. Availability comes from a successful re-list, never message prose.

The docs also list other auth and modality codes. This is the deliberately
scoped LR-04 table, not an expansion of the release. P2-T8 still owns live
code/envelope confirmation; source review alone does not mark LR-04 verified.
