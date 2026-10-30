# Mutation tooling spike (2026-10-23)

Plan task 1, spec §5.6.1. All six tools installed and were run against 2-file toy projects, so there are no smoke-test blockers. gremlins and dotnet-stryker, the two required tools, both work.

Environment: macOS arm64, Go 1.26.10, .NET SDK 10.0.101, Node with npm, rustc/cargo 1.87.0, Python 3.15.0, Homebrew openjdk 27 (`JAVA_HOME=/opt/homebrew/opt/openjdk`; the macOS `java` stub is not a real JRE). Toy projects were built in the session scratchpad. Each had two source files, one changed in the last commit, and tests that cover both.

Cross-cutting findings:

- Native diff scoping: gremlins, Stryker.NET, cargo-mutants. Target-list only: StrykerJS, mutmut, PIT.
- Only two tools accept an arbitrary test command: StrykerJS (`testRunner: command`) and, loosely, cargo-mutants (extra cargo test args or nextest). The rest run their own runner (`go test`, `dotnet test`, pytest, the Maven/Gradle test phase). So the baseline test command (R6) can only be honored by adapters for js and rust; for the others it can only gate the pre-run.
- Only gremlins omits the replacement text from its report. The mutant key (file, line, start column, mutator, replacement) therefore degrades to (file, line, column, type) for Go. Every adapter now reports the start column (`col`; 0 for mutmut and PIT, which report none) and the key carries it.
- Outputs land in the cwd by default for Stryker.NET (`StrykerOutput/`), mutmut (`mutants/`), StrykerJS (`reports/`, `.stryker-tmp/`), PIT (`target/`) and cargo-mutants (`mutants.out/`). Adapters must redirect them (flags below) or run in a copy.

## gremlins (Go)

- Install: `go install github.com/go-gremlins/gremlins/cmd/gremlins@latest` succeeded. Module version v0.6.0 (`go version -m`). `gremlins --version` prints `gremlins version dev darwin/arm64`, so the probe is presence only.
- Scoping (native): `gremlins unleash --diff HEAD~1 -o diff.json`. Without it: 6 mutants across a.go and b.go. With it, a.go's two mutants became `SKIPPED` and only b.go was exercised.

```
     SKIPPED CONDITIONALS_NEGATION at a.go:4:7
     SKIPPED CONDITIONALS_BOUNDARY at a.go:4:7
      KILLED INVERT_NEGATIVES at b.go:4:11
      KILLED ARITHMETIC_BASE at b.go:4:15
```
- Report: `-o <file>` writes JSON with `files[].mutations[] = {type,status,line,column}` plus totals (`test_efficacy`, `mutants_killed`, `mutants_lived`, ...). Status values seen: KILLED, LIVED, SKIPPED, TIMED OUT. There is no replacement text.
- Test command: none. It always runs `go test`.
- Caveats: skipped mutants still appear in the report, so filter them. A mutant on a changed line in an unchanged-looking file was TIMED OUT once on the tiny toy (parallel coverage run), so retry timeouts. Run from the module root.

## Stryker.NET (C#)

- Install: `dotnet tool install -g dotnet-stryker` gave 5.0.0. `dotnet-stryker --version` does not work (`-v` means project version and demands a value), so the probe is `dotnet tool list -g`.
- Scoping (native): `dotnet-stryker --since:<ref>`. `HEAD~1` was rejected: "No branch or tag or commit found with given target HEAD~1". A sha (`git rev-parse HEAD~1`) works.

```
1 files changed
Changed file .../Lib/B.cs
4 mutants got status Ignored. Reason: Removed by since filter
2 total mutants will be tested because: Mutant changed compared to target commit
The final mutation score is 50.00 %
```
- Report: `--reporter json` writes `StrykerOutput/<ts>/reports/mutation-report.json` (mutation-testing-elements schema 2). Each mutant has `mutatorName`, `replacement`, `status` and `location`. `--output <dir>` moves the logs and report root out of the worktree. Ignored entries are the since-filtered ones.
- Test command: none; `dotnet test` only. A baseline that already fails aborts with "Initial testrun has more than 50% failing tests".
- Caveats: run from the test project directory (or pass `--solution` and `--test-project`). Mutants are located by file path keys that are absolute.

## StrykerJS

- Install: `npm --prefix $TMPDIR/strykerjs i @stryker-mutator/core` gave 10.1.1, run through `node_modules/.bin/stryker`. `npx --no-install stryker` only resolves inside a project that has the dependency, so the probe is project-relative.
- Scoping (target-list): no diff mode. `--mutate b.js` works; the file supports `file:startLine[:col]-endLine[:col]` ranges, which allows line-level scoping from a diff. A config file passed positionally keeps everything outside the worktree:

```
stryker run /scratch/cfg.json   # {"testRunner":"command","commandRunner":{"command":"npm test"},"mutate":["b.js"],
                                #  "reporters":["json"],"jsonReporter":{"fileName":"/scratch/out/mutation.json"},
                                #  "coverageAnalysis":"off","tempDirName":"/scratch/tmp"}
Instrumented 1 source file(s) with 2 mutant(s)
```
`git status` stayed clean.
- Report: mutation-testing-elements JSON with `replacement`. `jsonReporter.fileName` is config-only (the CLI rejected `--jsonReporter.fileName`).
- Test command: `testRunner: command` plus `commandRunner.command` (verified with `npm test` driving node:test). With the command runner, `coverageAnalysis` must be `off`.

## mutmut (Python)

- Install: `pipx install mutmut` gave 3.8.0 (needed `brew install pipx` first). `mutmut --version` crashes outside a configured project (import-time `FileNotFoundError: Could not figure out where the code to mutate is`), so the probe is `pipx list --short`.
- Scoping (target-list): mutmut only reads `./pyproject.toml` or `./setup.cfg` from its cwd, so there is no external config path. Working recipe: `git archive HEAD | tar -x -C <harden dir>`, write `setup.cfg` there with `[mutmut] source_paths=toypkg` and `only_mutate=toypkg/b.py`, run `mutmut run` in that copy. 3 mutants generated for b.py only.
- A layout of `src/<module>.py` imported as `src.x` fails with "Failed trampoline hit. Module name starts with `src.`". The package must be importable as a top-level name (`toypkg.b`).
- Report: `mutants/<pkg>/<module>.py.meta` holds `exit_code_by_key` (0 survived, 1 killed). `mutmut results --all true` lists `toypkg.b.x_add__mutmut_1: survived`; `mutmut show <key>` prints the diff (the replacement). `export-cicd-stats` has counts only.
- Test command: pytest only (`pytest_add_cli_args`, `pytest_add_cli_args_test_selection`). `paths_to_mutate` and `tests_dir` are deprecated.
- Caveat: pytest must be importable from the Python that runs mutmut (installed it in a venv for the spike; the pipx venv lacks pytest).

## PIT (Java)

- Install: `brew install pipx maven gradle` succeeded (maven 3.10.0, gradle 9.8.1). `mvn org.pitest:pitest-maven:help` succeeded, resolving 1.30.1. The Homebrew openjdk 27 was used.
- Scoping (target-list): `-DtargetClasses=toy.B` produced 2 mutants for B only. Native SCM scoping is the paid arcmutate extension.

```
mvn -q org.pitest:pitest-maven:mutationCoverage -DtargetClasses=toy.B \
  -DoutputFormats=XML -DreportsDirectory=<dir> -DtimestampedReports=false
>> Generated 2 mutations Killed 2 (100%)
```
- Report: `<dir>/mutations.xml`, one `<mutation status=...>` per mutant with `sourceFile`, `mutatedClass`, `mutatedMethod`, `lineNumber`, `mutator`, `description`. `-DreportsDirectory` keeps it out of `target/` (verified).
- Caveat that matters for adapters: with JUnit 5 the run fails with "Please check you have correctly installed the pitest plugin for your project's test library" unless `pitest-junit5-plugin` is declared as a dependency of the pitest-maven plugin in the pom. The by-coordinates invocation does not avoid that. Edits to the pom contradict "no edits in the worktree", so the adapter must either run in a copy or use `-f` with an overlay pom (not tried).
- Gradle: gradle installed, but PIT on Gradle needs `info.solidsoft.pitest`; not exercised.
- Test command: none; PIT uses the build's own test framework.

## cargo-mutants (Rust)

- Install: `cargo install cargo-mutants` failed on the first try: "cannot install package `cargo-mutants 27.1.0`, it requires rustc 1.88 or newer, while the currently active rustc version is 1.87.0". `cargo install cargo-mutants --version 26.0.0 --locked` succeeded, so the registry records 26.0.0. The install_hint mentions the pin.
- Scoping (native): `git diff HEAD~1 > d.diff; cargo mutants --in-diff d.diff --output <dir>`. `--list` went from 13 to 7 mutants; the run reported "7 mutants tested in 4s: 1 missed, 6 caught". `git status` stayed clean when `--output` pointed outside the tree.
- Report: `<output>/mutants.out/outcomes.json`. `outcomes[0]` is the `Baseline` scenario. Mutant outcomes carry `scenario.Mutant{file,function,span,replacement,genre}` and `summary` of `CaughtMutant`, `MissedMutant`, `Unviable` or `Timeout`. `missed.txt` is a human list.
- Test command: `--cargo-test-arg` (repeatable) and `--test-tool cargo|nextest`.
