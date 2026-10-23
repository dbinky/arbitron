# Captured models.dev normalization inputs

Retrieved over HTTPS on 2026-10-16 (no harness invocation or usability claim):

- Catalog: `https://models.dev/api.json`
  - Download SHA-256: `51bcb50c9758a0544e0c20177844e59ce7b4e145e267b83d9ba85226a9ee9a4a`
- Schema: `https://raw.githubusercontent.com/anomalyco/models.dev/dev/packages/core/src/schema.ts`
  - Complete captured file: `models.dev-schema.ts`
  - SHA-256: `8184fb02441d4bbd1f01d05257a3a630c81adaf85bcca34498c923b0f98a5682`

`models.dev.json` is a reduced response: provider metadata is unchanged, and
only the following model keys were retained, with every model field unchanged:

- anthropic: `claude-opus-4-6`, `claude-opus-4-7`, `claude-opus-5-5`, `claude-fable-5`
- openrouter: `deepseek/deepseek-v4-pro`, `deepseek/deepseek-v4-flash`

Reduced fixture SHA-256:
`ecb7b1c7a164b01de904df977f38d20680705fa759d33353c64134c570d5730e`.
JSON whitespace/key ordering was rewritten during reduction. These are not
claimed to be the historical September 30 bytes used in decision D2.

The captured schema's `Provider.models` is a record of `Model` objects keyed by
native model ID; `ModelShape` includes optional `canonical_model_id` and
`ModelBase` includes `release_date`. Arbitron parses these identity fields,
retains all other metadata in the raw slice, and treats absent optional identity
metadata as unknown. It does not derive follow tiers from family, price or limits.

The equal-version date test uses the captured opus 4.6/4.7 release dates with an
explicit test policy that captures only the major version. The policy is an
algorithm-test input, not the shipped D2 rule or a claim that these versions are
equivalent under D2. The Unicode/missing-metadata rows and malformed variants
in tests are explicitly generated mutations, not upstream evidence.

Persistence/restart, policy reclassification and final execution acceptance
remain owned by P3T5–P3T7. This fixture proves source metadata ingestion, not
usability, durable membership or installed root wiring.
