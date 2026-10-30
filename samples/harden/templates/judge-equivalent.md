# Equivalent-mutant judge

You are an independent judge. A test-writing agent could not kill some surviving mutants and claims they are **equivalent**: that no input can tell the mutated code from the original. An approved claim takes the mutant out of the mutation score, so your job is to rule, for every claim, whether it is really equivalent.

## Inputs

- Claims: `{{workspace}}/harden/equivalence-claims.json`, shaped `{"claims":[{"mutant_id": "...", "justification": "..."}]}`
- The mutants: `{{workspace}}/harden/survivors-round.json` lists each survivor's `mutant_id`, `file` (relative to `./work`), `line`, `mutator` and `replacement`
- The code: the worktree at `./work`

For each claim, find its mutant in `survivors-round.json`, read the code around `file`:`line`, apply the `replacement` in your head, and decide whether any input, state or call order could make the mutated code behave differently from the original.

## How to rule

- **Equivalent** only when you can see why no observable behaviour can differ: the mutated branch is unreachable, the change is absorbed by a later guard, or both versions compute the same value for every input.
- **Not equivalent** when you can name an input that tells them apart, or when the justification asserts equivalence without showing it.

When the evidence is thin or you cannot tell, rule `equivalent: false`. A wrong rejection costs one survivor in the score; a wrong approval hides a real gap in the tests.

Do not edit any file except the verdicts file below. Do not commit. Do not write tests.

## Output

Write exactly one file, `{{workspace}}/harden/equivalence-verdicts.json`, in this shape:

```json
{"rulings":[{"mutant_id":"pkg/limits/limits.go:41:CONDITIONALS_BOUNDARY:<=","equivalent":false,"reason":"one sentence naming the evidence"}]}
```

Rules for the file:

- It must be valid JSON and nothing else.
- `mutant_id` is copied exactly from a claim; `equivalent` is a JSON boolean; `reason` is a short string.
- Do not add the rulings to your reply in place of the file; the file is what is read.

## Closing check

Before you finish, open the claims file, list every `mutant_id`, and confirm `equivalence-verdicts.json` holds exactly one ruling for each of them: no claim missing, no id invented. A claim with no ruling is rejected. Fix the file if the counts differ, then reply with one line: `rulings: <n> of <n> claims`.
