# Mission: Capture the Branch-Point Test Baseline

Working **unattended** in the worktree at `./work`, record which tests are
**already failing before any implementation begins**, so later stages can tell a
real regression from a pre-existing failure. The user is unavailable.

This runs right after the plans are aligned and **before** the first
implementation task — the code under `./work` is still at the feature branch's
starting point, so whatever fails now is *pre-existing*, not caused by this
feature.

## Absolute rules
- **Do NOT modify, create, or delete any source/test/config file under `./work`,
  and do NOT commit anything.** Your only write is the single JSON file named
  below, in the instance workspace root (the parent of `./work`), never inside
  the repo.
- This is a measurement step, not an implementation step. Do not fix anything.

## Steps
1. **Detect the test suite(s).** The repo may be mixed-language, so there may be
   more than one. Check `./work/CLAUDE.md`, `README`, `Makefile`, and the build
   manifests to find the canonical command(s): e.g. `go test ./...` (go.mod),
   `dotnet test` (*.sln/*.csproj), `npm test`/vitest (package.json),
   `pytest` (pyproject/pytest.ini), `cargo test` (Cargo.toml). Prefer the
   project's documented command when one exists.
2. **Build and run each suite** from inside `./work`. Capture, per suite: whether
   it **built/compiled** at all, the count of passing and failing tests, and the
   identifier of each failing test (fully-qualified name / file > test).
3. **Write the baseline file** to the instance workspace root as
   `{{workspace}}/test-baseline.json` — i.e. the parent directory of `./work`,
   NOT inside the repo. It MUST be valid JSON of exactly this shape:

```json
{
  "suites": [
    {
      "name": "<short suite label>",
      "command": "<exact command you ran>",
      "build_ok": true,
      "failed_count": 0,
      "passed_count": 0,
      "failing_tests": ["<fully-qualified failing test id>", "..."]
    }
  ]
}
```

- One array entry per suite you ran. If a suite **fails to build/compile**, set
  `build_ok: false`, `failed_count: 0`, and list the build error summary as a
  single entry in `failing_tests` (prefix it `BUILD: `).
- If you genuinely cannot find any test suite, still write the file with an empty
  `suites` array — do not invent suites.
- Keep `failing_tests` to stable identifiers only (no timestamps, durations, or
  run-specific noise) so later diffs are reliable.

## Finish
Report a one-paragraph summary: which suites you ran, the per-suite pass/fail
counts, and the total number of pre-existing failures recorded. Do not include
the full failing-test list in the summary — it is already in the file.
