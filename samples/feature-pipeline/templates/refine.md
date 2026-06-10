# Mission: Ralph Refinement Pass

Working **unattended** in the worktree at `./work`, make the **smallest correct improvement** toward "done". The user is unavailable.

## Each pass
1. Read `./work/RALPH.md`, `./work/focus-areas.md`, `./work/gaps-identified.md`.
2. Run the project's test suite (and build) to see current status.
3. Pick the **single highest-priority** item from `focus-areas.md` (failing tests first). Make the smallest correct change that advances it — following strict TDD where adding behavior (failing test for happy/success/failure/error/edge → minimal code → green).
4. Re-run the affected tests.
5. **Update the tracking files in place:** strike completed focus areas, add newly discovered gaps, reprioritize.
6. Commit: `git -C ./work add -A && git -C ./work commit -m "refine({{slug}}): <what you changed>"`.

Make progress every pass; never leave the tree uncommitted.