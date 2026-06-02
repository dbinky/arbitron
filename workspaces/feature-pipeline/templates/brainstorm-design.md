# Brainstorm: Implementation Design

Turn the approved spec into an **implementation design**, split into **logical phases** (one design doc per phase). This is high-level implementation: architecture, components, data flow, integration seams, error handling, and testing strategy.

## Spec (context)
{{write-spec.spec_path}} was just written in the worktree. The full spec text:
---
{{brainstorm-spec.spec_markdown}}
---

## Process (follow exactly)
1. Read the spec thoroughly; skim `./src` for existing patterns to follow.
2. Ask one question per turn (multiple-choice preferred) to resolve design decisions: phase boundaries, component responsibilities, interfaces, dependencies between phases, error handling.
3. Propose 2–3 architectural approaches with trade-offs; lead with your recommendation.
4. Present each phase design in 200–300-word sections; validate each with the human before moving on.
5. Design for isolation: small units, clear interfaces, one responsibility each.
6. **Testing — STRICT TDD (REQUIRED in every phase's design):** each phase must specify tests for **all five** scenario categories — happy-path, success, failure, error, and edge-case. State this requirement explicitly in each design doc's testing section.
7. YAGNI ruthlessly. Get explicit approval of the full set before finishing.

## Output
When approved, `phases` is an ordered array; each element `{title, markdown}` where `markdown` is one complete phase design document (starting with `# <Feature> — Design Phase N: <title>`). The number of phases is whatever the work logically needs.
