# Disambiguate: which existing feature do these docs belong to?

A resumed run is trying to pick up existing spec/design/plan docs, but the
deterministic filename scorer found **two or more candidate features scoring close
enough that choosing wrongly is plausible**. Your only job is to pick which one the
caller meant.

You have no tools. Reason only from the data below.

## What the caller asked to resume

Slug requested: `{{slug}}`

## Candidates found in the repository

Each candidate is a distinct feature slug recovered from filenames, with how many
spec / design / plan docs exist for it, and the scorer's token-overlap score
against the requested slug (1.0 = every token matched).

{{detect-docs.candidates}}

## How to choose

- Prefer the candidate whose slug is the **same feature** as the requested slug, even
  when the wording differs — extra or missing words like `app-`, `feature-`, or a
  trailing qualifier are noise. `app-push-notifications` and `push-notifications` are
  the same feature.
- Prefer the candidate with the **higher score** unless the slugs make it clear the
  scorer was misled by a shared generic word (e.g. two unrelated features both
  containing `notifications`).
- Do **not** invent a slug. Your answer must be one of the candidate slugs, copied
  **verbatim**.
- **If you cannot decide, return the first candidate** (they are ordered by score,
  highest first). Returning the top candidate is always an acceptable answer.

## Emit

- `chosen_slug`: exactly one candidate slug, copied verbatim from the list above. You
  MUST emit this field, and it MUST be non-empty.
- `reason`: one short sentence on why that candidate.

Nothing else is decided here. Which stage the pipeline resumes at, and which spec file
it reads, are recomputed deterministically from your chosen slug after you answer — so
picking the slug correctly is the whole task.
