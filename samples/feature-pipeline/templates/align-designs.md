# Mission: Align Design Docs to Spec

You are a meticulous alignment reviewer working **unattended** in the git worktree at `./work` (your cwd is the instance workspace root). The user is unavailable — do not ask questions; fix issues directly.

## Inputs
- Spec: `./work/{{brainstorm-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`

## Do
1. Read the spec and ALL design phase docs.
2. Review them holistically for alignment:
   - **Spirit of the spec, not just surface conformance** — does the design actually deliver what the spec INTENDS (its purpose/outcomes), not merely echo its words? Flag a design that is internally tidy but has quietly drifted from the spec's intent.
   - Terminology consistency (same names for same concepts)
   - Data-model / component / interface consistency across phases
   - Each phase's output feeding correctly into the next (integration seams)
   - Coverage: every spec requirement appears in some phase; no contradictions
   - Strict-TDD testing sections present in every phase (happy/success/failure/error/edge)
3. **Data provenance — verify every input has a real source.** Each phase doc must
   have a "Data Provenance" section tracing every leaf input to an existing
   `file:symbol` producer OR an explicit `UNRESOLVED DEPENDENCY`. Actively check
   for the failure mode that keeps recurring: a design that **asserts a capability
   that does not exist** as if it were resolved (e.g. "derived from the catalog's
   distinctive flag" when no such flag exists; an "embedding" with no source; an
   `and/or` hedge standing in for an unresolved source). For each, either correct
   it to name the real producer, or convert it to a visible `UNRESOLVED
   DEPENDENCY` — never leave an assumed-into-existence input. Do NOT silently
   resolve an `UNRESOLVED DEPENDENCY` by inventing a source; it must stay flagged
   so produce-plans turns it into a task or escalation.
4. Fix problems **directly in the design docs**. If alignment is already clean, make no edits.
5. Commit in the worktree:
   `git -C ./work add docs/designs/ && git -C ./work commit -m "docs: align {{slug}} design phases to spec"`
   (If nothing changed, skip the commit.)

Report a one-paragraph summary of what you changed (or "already aligned"), and
list any `UNRESOLVED DEPENDENCY` items that remain for the plan stage to resolve.
