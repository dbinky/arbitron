# Mission: Kill the Surviving Mutants (test files only)

You are strengthening the test suite of the feature on branch `{{branch}}`, working **unattended** in the git worktree at `./work`. The user is unavailable — never stop to ask.

Mutation testing changed the source code changed since `{{scope.merge_base}}` one small edit at a time (a flipped comparison, a removed call, a changed constant) and ran the tests against each edit. A **surviving** mutant is an edit no test noticed. Your job this round is to write tests that notice them.

## First: fix what the tamper gate voided

If `{{workspace}}/tamper/must-fix.md` exists, the tamper gate voided the previous round and reset the worktree to that round's start, so its tests are gone. Read it and address every item **before anything else this round**. Do not edit or delete `must-fix.md` itself.

## The rule: test files only

You may change **test files only**: add new test files, or add and strengthen tests in existing ones. Do not change anything else:

- No source file. A round that changes any source file is voided as a whole, and the round still counts against the cap. If a mutant can only be killed by changing the code, leave it.
- No test, mutation or CI configuration (thresholds, exclusions, mutator lists, `[tool.mutmut]`, `stryker.conf`, pitest settings, workflow files).
- No baseline or harden report files under `.arbitron/pipeline/`.
- Nothing under `{{workspace}}/harden/` except `equivalence-claims.json`. The score state is hashed before you start, and a round that edits it is voided as a whole.
- Never delete, rename, skip or loosen an existing test, and never drop an assertion. A deterministic gate checks the round and a separate judge rules on anything it flags.

## The survivors

Read `{{workspace}}/harden/survivors-round.json`:

```json
{"score": 72.41, "threshold": 80, "gate_passed": false,
 "survivors": [{"mutant_id": "pkg/limits/limits.go:41:CONDITIONALS_BOUNDARY:<=", "file": "pkg/limits/limits.go",
                "line": 41, "mutator": "CONDITIONALS_BOUNDARY", "replacement": "<=",
                "status": "survived", "language": "go"}]}
```

`file` is relative to `./work`. `survived` means a test ran the mutated line and still passed; `no_coverage` means no test ran it at all. The languages, tools and test commands for this run are: {{detect.languages}}

For each survivor, read the code at `file`:`line`, work out which behaviour the mutation changes, and write a test that passes on the real code and would fail on the mutant. Assert the observable result (a return value, an error, a boundary), not an implementation detail. Group the tests by the file they cover, and follow the conventions of the neighbouring tests.

Run the tests you wrote with the language's test command and confirm they pass on the unmodified code. A test that fails on the real code is a bug in the test: fix the test, never the code.

## Equivalent mutants

Some mutants cannot be killed because they do not change behaviour (for example, a changed loop bound that the following guard makes unreachable). Do not write a pointless test for one. Claim it instead, in `{{workspace}}/harden/equivalence-claims.json` (outside `./work`; do not commit it):

```json
{"claims": [{"mutant_id": "pkg/limits/limits.go:41:CONDITIONALS_BOUNDARY:<=", "justification": "one or two sentences: why no input can tell the mutant from the original"}]}
```

- `mutant_id` is copied exactly from `survivors-round.json`.
- A separate judge from another model family rules on every claim. An unruled or weak claim is rejected and the mutant keeps counting against the score, so claim only what you can justify from the code.
- Write the file only if you have claims, and write valid JSON and nothing else.

## Commit

Commit the test files: `git -C ./work add <test files> && git -C ./work commit -m "test({{slug}}): kill surviving mutants (harden round {{harden_round}})"`. Commit only; do not push. The workflow checks the round before anything is pushed.

## Closing check: leave the tree clean

Before you finish, run `git -C ./work status --porcelain`. It must print nothing:

- A test file you wrote and did not commit: commit it.
- Anything else (a source edit, a scratch file, generated output): discard it with `git -C ./work checkout -- <path>`, or delete it if it is untracked.

Run the command again until it prints nothing. A round that leaves uncommitted changes is voided as a whole, its committed tests included, and still counts against the cap.

## Report (end of round)

End your response with this block:

```
STATUS: <DONE | DONE_WITH_CONCERNS>
- survivors targeted: <n of total>
- tests added: <n>
- equivalence claims: <n>
- left alive: <one line naming survivors you could not kill without changing source, or "none">
```
