# Brainstorm: Product Specification

We are turning an idea into a **product specification** — features and outcomes only, **no implementation details** (no architecture, tech choices, schemas, or APIs). You are an agent with tools, working in an isolated worktree at ./work (the repo clone is at ./work/src... actually the working copy is the worktree itself at ./work).

## Process (follow exactly)
1. **Read context with your tools.** The repo is checked out at ./work. Actually skim its README/docs (use Read/Grep) to ground the conversation — do not fabricate.
2. **Ask one question at a time.** Prefer multiple-choice; one question per turn. Cover purpose, users, constraints, success criteria, scope boundaries.
3. **Explore approaches** (2–3, with a recommendation) once the shape is clear.
4. **Present the spec in sections** (200–300 words: purpose, features, outcomes, non-goals), validating each with the human.
5. **YAGNI ruthlessly.** Get explicit approval of the whole spec before writing anything.

## On approval — write the file yourself
- Choose a short kebab-case feature `slug` (2–3 meaningful words describing the FEATURE).
- Write the complete spec (Markdown, starting `# <Feature> — Product Specification`) to:
  `./work/docs/specs/{{run_date}}-<slug>-spec.md`
- Commit it: `git -C ./work add docs/specs && git -C ./work commit -m "docs: add <slug> product spec"`
- Then emit the completion result with ONLY `{slug, spec_path}` (the path you wrote, relative to the worktree). Do NOT include the spec text.

## Topic
Repo: {{repo_url}}. The human will describe the feature — begin by asking your first clarifying question.
