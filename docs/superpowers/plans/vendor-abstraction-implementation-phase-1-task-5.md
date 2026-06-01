# Phase 1, Task 5: Capability Validation in `workflow_validate`

## Goal

Make capability validation real from Phase 1: `llm_call` must reference a `model_profile` whose profile `kind == chat`, and `agent_call` must reference one whose `kind == agent`. A kind mismatch, a missing `model_profile`, or an unknown alias must fail `workflow_validate` **before any run** (acceptance #6). The profile registry is threaded into validation the same way the existing file `checker` is, so the domain stays adapter-free.

Source of truth: `docs/superpowers/specs/vendor-abstraction-design-phase-1.md` §4 (capability validation) and §9 (acceptance #6).

## Dependencies

- **Task 3** (`domain/provider.Profile`/`ProfileKind`/`ProfileResolver`/`ErrUnknownProfile`).
- **Task 4** (`NodeTypeAgentCall`, `NodeConfig.ModelProfile`).

## Background (current state)

- `domain/workflow/validation.go`:
  - `FileChecker` is a minimal domain-level interface (`FileExists(workflowName, relativePath) bool`) injected into `Validate(wf *Workflow, checker FileChecker) []ValidationError`. This is the existing pattern for threading an external capability into validation without importing an adapter.
  - `validateNodeConfigs(wf)` switches on `n.Type` and appends per-kind required-config errors with `Code: "MISSING_REQUIRED_CONFIG"` / node-specific codes.
  - `Validate` is the single entry; it calls `validateNodeConfigs(wf)` (no checker arg today) and `validateFileReferences(wf, checker)` only when `checker != nil`.
- `domain/provider.ProfileResolver.Resolve(alias) (Profile, error)` returns `ErrUnknownProfile` on a miss (Task 3).

## Design

Introduce a domain-level **profile checker** interface mirroring `FileChecker`, so `domain/workflow` does not import `domain/provider` types directly if that coupling is undesirable. Two acceptable approaches — pick the one that matches the file-checker precedent most closely:

- **Preferred:** define a tiny interface in `domain/workflow` that returns the kind for an alias, decoupling workflow from the provider package:
  ```go
  // ProfileChecker resolves a node's model_profile alias to its capability
  // kind for validation. Mirrors FileChecker: an injected domain capability,
  // not an adapter import. Returns ok=false for an unknown alias.
  type ProfileChecker interface {
      ProfileKind(alias string) (kind string, ok bool)
  }
  ```
  The adapter side (or `main.go`, Task 6) supplies an implementation that wraps `provider.ProfileResolver` (`Resolve` → return `string(prof.Kind), true`; `ErrUnknownProfile` → `"", false`). This keeps `domain/workflow` free of a `domain/provider` import, exactly like `FileChecker` avoids importing `engine`.

(If the team prefers, `domain/workflow` may import `domain/provider` and take a `provider.ProfileResolver` directly — both are domain packages so it is hex-legal. The `ProfileChecker` indirection is chosen here to match the established `FileChecker` seam and keep `kind` as a plain string at the validation boundary.)

## Files to Create/Modify

### Modify: `domain/workflow/validation.go`

- Add the `ProfileChecker` interface (above) near `FileChecker`.
- Change the `Validate` signature to thread the profile checker, mirroring `checker`:
  ```go
  func Validate(wf *Workflow, checker FileChecker, profiles ProfileChecker) []ValidationError
  ```
  - When `profiles != nil`, run a new `validateModelProfiles(wf, profiles)` pass; when nil, skip it (same nil-skip convention as `checker`).
  - **Caller impact:** every existing call site of `Validate(wf, checker)` must pass the new arg (use `nil` where no resolver is available, e.g. pure structural tests). Update `domain/workflow/validation_test.go` and any callers in `cmd/`, `adapter/http`, or wherever `workflow_validate` is invoked. Search: `Validate(` across the repo.
- Add `validateModelProfiles`:
  ```go
  func validateModelProfiles(wf *Workflow, profiles ProfileChecker) []ValidationError
  ```
  For each node:
  - If `n.Type == NodeTypeLLMCall` or `n.Type == NodeTypeAgentCall`:
    - If `n.Config.ModelProfile == ""` → append `ValidationError{Code: "MISSING_REQUIRED_CONFIG", Message: "<type> node " + n.ID + " must have a model_profile", NodeID: n.ID}`.
    - Else `kind, ok := profiles.ProfileKind(n.Config.ModelProfile)`:
      - `!ok` → append `ValidationError{Code: "UNKNOWN_PROFILE", Message: "node " + n.ID + " references unknown model_profile: " + n.Config.ModelProfile, NodeID: n.ID}`.
      - `ok` but kind mismatch:
        - `llm_call` requires `kind == "chat"`; otherwise `ValidationError{Code: "PROFILE_KIND_MISMATCH", Message: "llm_call node " + n.ID + " requires a chat profile but " + alias + " is " + kind, NodeID: n.ID}`.
        - `agent_call` requires `kind == "agent"`; otherwise the symmetric `PROFILE_KIND_MISMATCH` error.
  - Other node types: ignored.
- Wire the new pass into `Validate`:
  ```go
  if profiles != nil {
      errs = append(errs, validateModelProfiles(wf, profiles)...)
  }
  ```
- Keep `validateNodeConfigs`'s existing `llm_call` `templateFile` requirement; **add** an `agent_call` case there requiring `templateFile` (mirrors `llm_call`), so a missing template is still caught structurally:
  ```go
  case NodeTypeAgentCall:
      if n.Config.TemplateFile == "" {
          errs = append(errs, ValidationError{
              Code: "MISSING_REQUIRED_CONFIG",
              Message: "agent_call node " + n.ID + " must have a templateFile",
              NodeID: n.ID,
          })
      }
  ```

### Create: `adapter/provider/checker.go` (the `ProfileChecker` adapter)

- A thin wrapper so `main.go`/validation callers can adapt the resolver to the workflow-domain interface:
  ```go
  // ResolverChecker adapts a provider.ProfileResolver to workflow.ProfileChecker.
  type ResolverChecker struct{ r provider.ProfileResolver }

  func NewResolverChecker(r provider.ProfileResolver) ResolverChecker
  func (c ResolverChecker) ProfileKind(alias string) (string, bool) {
      prof, err := c.r.Resolve(alias)
      if err != nil {
          return "", false // includes ErrUnknownProfile
      }
      return string(prof.Kind), true
  }
  ```
  - `var _ workflow.ProfileChecker = ResolverChecker{}`.

## Testing (strict TDD — five scenario categories)

Use an in-memory fake `ProfileChecker` (map alias→kind) in `domain/workflow` tests; no adapter import. Per design §7 (validation bullet).

- **Happy path:** an `agent_call` node referencing an agent-kind profile and an `llm_call` referencing a chat-kind profile produce **no** validation errors from `validateModelProfiles`.
- **Success:** a workflow mixing both kinds correctly validates; the new `agent_call` `templateFile` requirement passes when a template is present.
- **Failure:** `agent_call` → a chat profile yields exactly one `PROFILE_KIND_MISMATCH` error on that node (and symmetrically `llm_call` → agent profile).
- **Error:** a node with an empty `model_profile` yields `MISSING_REQUIRED_CONFIG`; a node referencing an alias the checker reports `ok=false` for yields `UNKNOWN_PROFILE`.
- **Edge case:** `Validate(wf, checker, nil)` (nil profile checker) skips capability validation entirely (structural-only mode for tests/callers without a resolver); a workflow with no `llm_call`/`agent_call` nodes produces no profile errors even with a non-nil checker.

Also: a regression test that `validateModelProfiles` runs as part of `Validate` (not just in isolation) and that all existing structural validations still fire.

Constraint: `domain/workflow` imports only stdlib (+ existing intra-domain). The `ResolverChecker` lives in `adapter/provider` and is the only place the resolver meets the workflow interface. Validation tests use fakes.

## Acceptance Criteria

- `workflow.ProfileChecker` exists and `Validate` accepts it as a third, nil-skippable argument.
- `agent_call` bound to a chat profile (and `llm_call` bound to an agent profile) fails `workflow_validate` with a clear `PROFILE_KIND_MISMATCH` error, before any run (acceptance #6).
- Missing `model_profile` → `MISSING_REQUIRED_CONFIG`; unknown alias → `UNKNOWN_PROFILE`.
- `agent_call` requires a `templateFile` (structural check).
- `adapter/provider.ResolverChecker` adapts the resolver to the workflow interface; `domain/workflow` imports no adapter.
- All existing `Validate` callers updated; `go build ./...` and `go test ./domain/workflow/... ./adapter/provider/...` pass.
