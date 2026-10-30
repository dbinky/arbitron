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
- **Run every suite synchronously, to completion, in the foreground.** NEVER
  background a test command, NEVER poll-and-yield, and NEVER end your turn while
  a suite is "still running" or you are "waiting for tests to finish." Block
  until each command has fully exited and you hold its pass/fail results in hand.
  A mixed-language repo (e.g. Go **and** .NET) can take many minutes per suite —
  that is expected; wait for it. Ending early with the tests still running is the
  #1 way this step fails, and it strands the whole pipeline.
- **The `test-baseline.json` file is your ONE required deliverable. You are NOT
  finished until it exists on disk at `{{workspace}}/test-baseline.json`.**
  Before you conclude your turn, confirm the file is written and valid. If you
  ever must stop before every suite completes (time/space pressure), stop running
  suites and write the file *immediately* with whatever you have measured —
  marking any suite you could not finish with `build_ok:false` and a
  `failing_tests` entry prefixed `INCOMPLETE: ` — rather than ending with no file.

## Steps
1. **Detect the test suite(s).** The repo may be mixed-language, so there may be
   more than one. Check `./work/CLAUDE.md`, `README`, `Makefile`, and the build
   manifests to find the canonical command(s): e.g. `go test ./...` (go.mod),
   `dotnet test` (*.sln/*.csproj), `npm test`/vitest (package.json),
   `pytest` (pyproject/pytest.ini), `cargo test` (Cargo.toml). Prefer the
   project's documented command when one exists.
2. **Build and run each suite** from inside `./work`, one at a time, waiting for
   each to finish before starting the next. Capture, per suite: whether it
   **built/compiled** at all, the count of passing and failing tests, and the
   identifier of each failing test (fully-qualified name / file > test). Do not
   background them or run them detached — you need each command's real exit and
   output to record an accurate baseline.
3. **Write the baseline file** to the instance workspace root as
   `{{workspace}}/test-baseline.json` — i.e. the parent directory of `./work`,
   NOT inside the repo. It MUST be valid JSON of exactly this shape:

```json
{
  "suites": [
    {
      "name": "<short suite label>",
      "command": "<exact command you ran>",
      "paths": ["<repo-relative dir or file this suite covers>", "..."],
      "build_ok": true,
      "failed_count": 0,
      "passed_count": 0,
      "failing_tests": ["<fully-qualified failing test id>", "..."]
    }
  ]
}
```

- `paths` lists every repo-relative directory or file whose changes can change
  this suite's outcome: its source and test directories, plus the build files it
  depends on (e.g. `["shared", "go.mod", "go.sum"]` for a Go suite over
  `./shared/...`, `["src/Api"]` for `dotnet test src/Api/App.sln`,
  `["deploy/publish.sh", "deploy/publish_test.sh"]` for a script test). After each refine pass a script re-runs only the suites
  whose `paths` the pass touched; a changed file that no suite claims forces
  every suite to run. So err toward including a path, but do not list the repo
  root: that makes every change run every suite.
- One array entry per suite you ran. If a suite **fails to build/compile**, set
  `build_ok: false`, `failed_count: 0`, and list the build error summary as a
  single entry in `failing_tests` (prefix it `BUILD: `).
- If a suite could not be run to completion (see the time/space escape hatch in
  Absolute rules), set `build_ok: false` and add a `failing_tests` entry prefixed
  `INCOMPLETE: ` describing what did not finish — never silently omit it.
- If you genuinely cannot find any test suite, still write the file with an empty
  `suites` array — do not invent suites.
- Keep `failing_tests` to stable identifiers only (no timestamps, durations, or
  run-specific noise) so later diffs are reliable.

## Finish
First **verify `{{workspace}}/test-baseline.json` exists and is valid JSON** —
this is your definition of done. Then report a one-paragraph summary: which
suites you ran, the per-suite pass/fail counts, and the total number of
pre-existing failures recorded. Do not include the full failing-test list in the
summary — it is already in the file.
