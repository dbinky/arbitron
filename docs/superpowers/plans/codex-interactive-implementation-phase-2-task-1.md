# Plan — Phase 2, Task 1: approval policy (`decide` + path-scope classification)

**Slug:** `codex-interactive`
**Design:** `docs/superpowers/specs/codex-interactive-design-phase-2.md` (§3)
**Depends on:** Task 1.1 (`approvalRequest` / `approvalDecision` types)
**Package:** `adapter/codex`

## Goal

The enforcement heart, as a pure, transport-free function so it is exhaustively
TDD-able in isolation: classify each `approvalRequest` into allow/deny under the
invisible auto-policy. Default-deny. Path scope = `docs/` relative to `WorkDir`.

## Files

- `adapter/codex/approval_policy.go` (new) — `approvalPolicy` + `decide`.
- `adapter/codex/approval_policy_test.go` (new) — full five-category coverage.

## Implementation

```
type approvalPolicy struct {
    workDir      string // the node WorkDir
    writableRoot string // default "docs"
}

func (p approvalPolicy) decide(req approvalRequest) approvalDecision
```

Classification (in order):

1. **Patch/write** (`req.WritePaths != nil`): allow **iff every** path, resolved
   relative to `workDir` and `filepath.Clean`ed, is within `workDir/writableRoot`.
   Any path outside (incl. `..`-escapes, absolutes landing outside) → **deny the
   whole patch**.
2. **Command exec** (`req.Command != nil`):
   - the deliverable **git commit** (recognized narrowly: `git` with a
     `commit`/`add` subcommand scoped to the repo) → **allow**;
   - a read-only / informational command (no tree mutation) → **allow**;
   - anything else (build/test/install, arbitrary mutation) → **deny**.
   Keep the "informational vs mutating" test conservative: a small allowlist of
   known-safe verbs is **not** required if the chosen sandbox already blocks
   mutating writes; document the reliance. When in doubt → **deny** (default-deny).
3. **Anything unrecognized** → **deny** with a reason (fail closed; new codex
   approval kinds never open).

Every decision returns a `Reason` string for the trace. The policy never prompts
a human and never returns a "defer to human" — it is fully automatic.

### Path-scope helper

A `withinRoot(workDir, root, target string) bool` helper: clean + make-absolute
both, test `strings.HasPrefix(cleanTarget, cleanRoot+sep)` (or equality). Unit-
test it directly with adversarial inputs.

## Tests (TDD — all five categories)

- **Happy path:** a patch writing only `docs/specs/foo-spec.md` → allow; an
  informational command → allow.
- **Success:** a multi-file patch all under `docs/superpowers/specs/` → allow; the
  deliverable `git commit` → allow.
- **Failure (the guardrail):** a patch touching `domain/.../settings.go` → deny;
  a `make build` / `npm run build` command → deny; the decision carries a reason.
- **Error / adversarial:** a patch path `docs/../domain/x.go` (escape) → deny; an
  absolute `/etc/passwd` → deny; a mixed patch (`docs/a.md` + `cmd/main.go`) →
  deny wholesale.
- **Edge case:** empty `WritePaths` and empty `Command` (neither) → deny
  (unrecognized); a path exactly equal to `docs` (the root dir itself) handled
  deterministically; trailing-slash and `./docs/...` forms normalize correctly.

## Verification

- `go test ./adapter/codex/...` green; `go vet` clean.
- The policy has zero dependency on the transport or `engine` — a pure unit.

## Out of scope

- Wiring `decide` as the `approvalFunc` into a real turn (Task 2.2).
- Trace emission plumbing (the provider records the decision in Task 2.2).
