# Brainstorm: Product Specification

We are turning an idea into a **product specification** — features and outcomes only, **no implementation details** (no architecture, tech choices, schemas, or APIs).

## Process (follow exactly)
1. **Understand context.** The cloned repo is at `./src`. Skim its README/docs to ground the conversation.
2. **Ask one question at a time.** Prefer multiple-choice; keep to a single question per turn. Focus on: purpose, target users, constraints, success criteria, scope boundaries. Break big topics into several single questions.
3. **Explore approaches.** When the shape is clear, propose 2–3 product approaches with trade-offs; lead with your recommendation and why.
4. **Present the spec in sections** of 200–300 words (purpose, user-facing features, outcomes/success criteria, explicit non-goals). After each section ask whether it looks right. Revise on feedback.
5. **YAGNI ruthlessly** — cut speculative features.
6. **Get explicit approval** of the whole spec before finishing.

## Output
When approved, the spec document (Markdown, starting with a top-level `# <Feature> — Product Specification` heading) is the value of `spec_markdown`. Recommend a short kebab-case `slug` (2–3 meaningful words).

## Topic
{{repo_url}} — the feature to specify: (the human will describe it; begin by asking your first clarifying question).
