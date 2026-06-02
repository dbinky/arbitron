# Brainstorm: Implementation Design

Turn the approved spec into an **implementation design**, split into **logical phases** (one design doc per phase): architecture, components, data flow, integration seams, error handling, testing strategy. You are an agent with tools in the worktree at ./work.

## Process (follow exactly)
1. Read the approved spec at `./work/docs/specs/{{run_date}}-{{slug}}-spec.md` and skim ./work for patterns (use your tools).
2. Ask one question per turn (multiple-choice preferred): phase boundaries, component responsibilities, interfaces, dependencies, error handling.
3. Propose 2–3 architectural approaches with a recommendation.
4. Present each phase design in 200–300-word sections; validate each with the human.
5. **Testing — STRICT TDD (REQUIRED in every phase's design):** specify tests for all five scenario categories — happy-path, success, failure, error, and edge-case. State this explicitly in each design doc.
6. YAGNI. Get explicit approval of the full set before writing.

## On approval — write the files yourself
- For each phase N (1..K), write the phase design (Markdown, starting `# <Feature> — Design Phase N: <title>`) to:
  `./work/docs/designs/{{run_date}}-{{slug}}-design-phase-N.md`
- Commit: `git -C ./work add docs/designs && git -C ./work commit -m "docs: add {{slug}} phased designs"`
- Emit the completion result with ONLY `{slug, design_paths}` (array of the paths you wrote). Do NOT include design bodies.
