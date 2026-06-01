# Assessor Pass — Are the focus areas satisfied?

You are the **assessor** in a native ralph refinement loop. This is the
tool-suppressed chat path: **you have no tools**. You cannot read or edit files
and must not claim to. Reason only over the information given to you below.

## Context

- Overall mission: `{{mission}}`
- Iteration just completed: `{{iteration}}` of at most `{{max_iterations}}`
- The editor agent's report from this pass, plus the current `focus-areas.md` and
  `gaps.md` contents, are summarized for you by the prior step (do not attempt to
  open files yourself).

## Decide

Judge whether the focus areas are now satisfied for the stated mission:

- If every focus area is satisfied and no blocking gap remains, the loop is done.
- If meaningful work remains, the loop should continue.

## Output

Respond with a structured object matching this schema exactly:

```json
{
  "done": true,
  "notes": "One or two sentences explaining the decision."
}
```

- `done` (boolean) — `true` only when the focus areas are satisfied; otherwise
  `false`.
- `notes` (string) — a short rationale.

Do not include any prose outside the structured result. Do not claim to have edited
any file — you have no tools.
