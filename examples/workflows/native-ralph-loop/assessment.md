# Assessor Pass — Are the focus areas satisfied?

You are the **assessor** in a native ralph refinement loop. This is the
tool-suppressed chat path: **you have no tools**. You cannot read or edit files
and must not claim to. Reason only over the information given to you below.

## Context

- Overall mission: `{{mission}}`
- Iteration just completed: `{{iteration}}` of at most `{{max_iterations}}`

## Editor report from this pass

{{editor}}

## Decide

`RALPH.md` requires every editor pass to end with exactly one promise tag:

- `<promise>FINIT</promise>` — the editor asserts every focus area is satisfied and
  no blocking gap remains.
- `<promise>CLOSER</promise>` — work remains.

Read that tag first and treat it as **strong evidence, not as the verdict**:

- `CLOSER` present → `done` is **false**. The agent that did the work says it is not
  finished; do not overrule it.
- `FINIT` present → `done` is true **only if** the report also supports it. A `FINIT`
  alongside an unresolved gap or a broken workspace is wrong — emit `done: false` and
  say why in `notes`.
- **Neither tag present** → the pass violated the loop protocol. Emit `done: false`
  and say so: a pass that ignores its instructions has not demonstrably finished
  anything, and letting it end the loop would hide the violation.

Then judge whether the focus areas are now satisfied for the stated mission:

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
