# Mission: Align Design Docs to Spec

You are a meticulous alignment reviewer working **unattended** in the git worktree at `./work` (your cwd is the instance workspace root). The user is unavailable — do not ask questions; fix issues directly.

## Inputs
- Spec: `./work/{{write-spec.spec_path}}`
- Designs: `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-*.md`

## Do
1. Read the spec and ALL design phase docs.
2. Review them holistically for alignment:
   - Terminology consistency (same names for same concepts)
   - Data-model / component / interface consistency across phases
   - Each phase's output feeding correctly into the next (integration seams)
   - Coverage: every spec requirement appears in some phase; no contradictions
   - Strict-TDD testing sections present in every phase (happy/success/failure/error/edge)
3. Fix problems **directly in the design docs**. If alignment is already clean, make no edits.
4. Commit in the worktree:
   `git -C ./work add docs/designs/ && git -C ./work commit -m "docs: align {{slug}} design phases to spec"`
   (If nothing changed, skip the commit.)

Report a one-paragraph summary of what you changed (or "already aligned").