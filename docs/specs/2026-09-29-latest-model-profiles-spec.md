# Latest Model Profiles — Product Specification

## Purpose

Keep Arbitron's model choices current without requiring workflow owners to edit model names after each release. Workflow nodes retain their intended provider and capability tier.

## Features

- Arbitron refreshes an internal catalog of available models every four hours. It distinguishes cataloged models from those confirmed usable through this installation's configured providers and harnesses, including Claude Code, Codex, Pi, and OpenCode.
- An admin can create a profile for a specific model version or one that follows the latest version in a chosen provider and tier. A following profile advances automatically when a newer eligible model appears.
- New workflow runs use an updated model. Runs already underway keep the model they started with.
- If a refresh fails or no eligible replacement is found, a following profile keeps its last known working model.
- Arbitron has an editable Teams webhook setting that starts empty. When configured, Arbitron announces newly usable models and identifies which following profiles changed, if any.

## Outcomes

- By the next successful four-hour refresh after a newer model becomes usable, eligible following profiles select it for new runs.
- Pinned profiles and active runs remain stable. Refresh failures do not interrupt working profiles.
- Each newly usable model produces at least one announcement when the Teams setting is configured; a rare duplicate is possible after an ambiguous delivery.

## Non-goals

- Updating or installing harnesses.
- Automatically changing a profile's provider or tier.
- Moving pinned profiles or changing models in active runs.
- Supplying the Teams webhook URL during this work.

## Rollout exception (owner decision D1, 2026-10-01)

Runs created before the model-pin migration retain the existing live profile-resolution behavior until they finish; a NULL pin record identifies them. Changing such a profile to following can therefore move a legacy run. No historical pin reconstruction or upgrade drain is required. Every run created after the migration persists a pin record (including an empty map for a workflow with no model aliases) and follows the active-run stability outcome above. Missing/corrupt new pin records are errors, never live fallback. See `../designs/2026-09-29-latest-model-profiles-decisions.md`.

## Provider identity limit (owner decision D8, 2026-10-18)

Native Azure and Bedrock paths are pinned-only. A pinned profile/run preserves its configured deployment/model ID; Arbitron makes no immutable underlying-version claim for these opaque routes. They cannot follow a provider/tier or count as confirmed catalog usability without a new evidenced identity producer. Persist this route exception at run creation so a later live route edit cannot weaken an ordinary exact-model pin. This qualifies the specific-version/active-run guarantee only for these owner-approved paths; ordinary following profiles and exact-model pins keep the original outcome.
