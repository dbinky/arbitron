# Profile-Editing Phase 3 — Runtime Emission Across Backends — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `ResolvedBinding.ContextWindow`/`Effort` actually change what the claude-code and codex subprocesses request, via a deterministic `CapabilityRules.Emit` mapping (env for claude, `-c` for codex). opencode carries the fields but emits nothing.

**Architecture:** Add `Emit` to the Phase-1 `CapabilityRules` (runtime counterpart of `Validate`). claude-cli appends env vars in `env.go`; codex appends `-c model_reasoning_effort` in `args.go`. Adapters gain a `rules` field injected at the composition root.

**Tech Stack:** Go. Seams: `adapter/claudecli/env.go` (`agentEnv`), `adapter/claudecli/chat_adapter.go`, `adapter/codex/args.go` (`buildExecArgs`).

**Design ref:** `docs/superpowers/specs/profile-editing-design-phase-3.md`. **Depends on:** Phases 1, 2.

---

### Task 1: CapabilityRules.Emit + Emission type

**Files:**
- Modify: `domain/provider/capability.go`
- Test: `domain/provider/capability_emit_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestEmit_ClaudeCode200K(t *testing.T) {
	e, err := rules().Emit("claude-code", "opus-4.8", Context200K, EffortDefault)
	if err != nil { t.Fatal(err) }
	if e.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] != "1" || e.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] != "200000" {
		t.Fatalf("200k env missing: %+v", e.Env)
	}
}

func TestEmit_ClaudeCodeEffortAndFloor(t *testing.T) {
	e, _ := rules().Emit("claude-code", "sonnet-4.6", ContextDefault, EffortXHigh)
	if e.Env["CLAUDE_CODE_EFFORT_LEVEL"] != "high" { // floored
		t.Fatalf("expected floored high, got %q", e.Env["CLAUDE_CODE_EFFORT_LEVEL"])
	}
}

func TestEmit_ClaudeCodeEffortNoneDisables(t *testing.T) {
	e, _ := rules().Emit("claude-code", "opus-4.8", ContextDefault, EffortNone)
	if e.Env["MAX_THINKING_TOKENS"] != "0" { t.Fatalf("none should disable: %+v", e.Env) }
}

func TestEmit_CodexEffort(t *testing.T) {
	e, _ := rules().Emit("codex", "gpt-5.5", ContextDefault, EffortMedium)
	if !contains(e.Args, "-c") || !contains(e.Args, "model_reasoning_effort=medium") {
		t.Fatalf("codex effort arg missing: %+v", e.Args)
	}
	none, _ := rules().Emit("codex", "gpt-5.5", ContextDefault, EffortNone)
	if !contains(none.Args, "model_reasoning_effort=minimal") {
		t.Fatalf("codex none→minimal missing: %+v", none.Args)
	}
}

func TestEmit_DefaultsEmitNothing(t *testing.T) {
	e, _ := rules().Emit("claude-code", "opus-4.8", ContextDefault, EffortDefault)
	if len(e.Env) != 0 || len(e.Args) != 0 { t.Fatalf("default should emit nothing: %+v", e) }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement `Emit`** (appended to `capability.go`):

```go
type Emission struct {
	Env  map[string]string
	Args []string
}

// Emit turns a (validated) requested tier+effort into native env/argv for the
// backend. It floors effort via the same rules as Validate, so write-time and
// runtime never disagree. Defaults/unknown axes emit nothing for that axis.
func (r CapabilityRules) Emit(backend, model string, tier ContextWindow, effort Effort) (Emission, error) {
	// Reuse Validate to floor effort + reject inapplicable (defensive; writes are
	// pre-validated). default/none for effort and default for tier never error.
	effTier, effEffort, err := r.Validate(backend, model, tier, effort)
	if err != nil { return Emission{}, err }
	out := Emission{Env: map[string]string{}}
	switch backend {
	case "claude-code", "claude-cli":
		emitClaudeContext(&out, effTier)
		emitClaudeEffort(&out, effEffort)
	case "codex":
		emitCodexEffort(&out, effEffort)
	default:
		// opencode and others: deferred — emit nothing.
	}
	if len(out.Env) == 0 { out.Env = nil }
	return out, nil
}

func emitClaudeContext(out *Emission, tier ContextWindow) {
	switch tier {
	case Context200K:
		out.Env["CLAUDE_CODE_DISABLE_1M_CONTEXT"] = "1"
		out.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = "200000"
	case Context1M:
		out.Env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = "1000000"
	case ContextDefault:
		// emit nothing
	}
}

func emitClaudeEffort(out *Emission, e Effort) {
	switch e {
	case EffortDefault:
		// nothing
	case EffortNone:
		out.Env["MAX_THINKING_TOKENS"] = "0"
	default:
		out.Env["CLAUDE_CODE_EFFORT_LEVEL"] = string(e)
	}
}

func emitCodexEffort(out *Emission, e Effort) {
	switch e {
	case EffortDefault:
		// nothing
	case EffortNone:
		out.Args = append(out.Args, "-c", "model_reasoning_effort=minimal")
	default:
		out.Args = append(out.Args, "-c", "model_reasoning_effort="+string(e))
	}
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./domain/provider/ -run Emit -v`

- [ ] **Step 5: Commit** — `git commit -m "feat(provider): CapabilityRules.Emit native mapping"`

---

### Task 2: claude-cli env emission (agent + chat)

**Files:**
- Modify: `adapter/claudecli/env.go` (`agentEnv`)
- Modify: `adapter/claudecli/chat_adapter.go` (env build path) + the adapter struct holding `rules`
- Modify: `adapter/claudecli/shared.go` (add a shared `applyCapabilityEnv` helper)
- Test: `adapter/claudecli/env_test.go` (extend)

- [ ] **Step 1: Write failing tests** (extend existing `env_test.go`)

```go
func TestAgentEnv_AppliesContextAndEffort(t *testing.T) {
	rules := testCapRules() // claude-code: 200k + effort high
	binding := provider.ResolvedBinding{Backend: "claude-code", Model: "opus-4.8",
		ContextWindow: provider.Context200K, Effort: provider.EffortHigh,
		RevealKey: func(context.Context) (string, error) { return "", nil }}
	env, err := agentEnvWithRules(context.Background(), binding, rules)
	if err != nil { t.Fatal(err) }
	assertEnv(t, env, "CLAUDE_CODE_EFFORT_LEVEL", "high")
	assertEnv(t, env, "CLAUDE_CODE_DISABLE_1M_CONTEXT", "1")
	assertEnv(t, env, "CLAUDE_CODE_AUTO_COMPACT_WINDOW", "200000")
}

func TestAgentEnv_DefaultsAddNothing(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "claude-code", Model: "opus-4.8",
		RevealKey: func(context.Context) (string, error) { return "", nil }}
	base, _ := agentEnv(context.Background(), binding) // existing credential-only path
	withRules, _ := agentEnvWithRules(context.Background(), binding, rules)
	if len(withRules) != len(base) { t.Fatal("defaults must not add env") }
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement** — add `applyCapabilityEnv` to `shared.go`:

```go
// applyCapabilityEnv appends the capability-derived env (context window, effort)
// to an existing env slice, computed from the binding via the capability rules.
// Backend is the EFFECTIVE backend already resolved onto the binding.
func applyCapabilityEnv(env []string, binding provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	em, err := rules.Emit(binding.Backend, binding.Model, binding.ContextWindow, binding.Effort)
	if err != nil { return nil, err }
	for k, v := range em.Env {
		env = append(env, k+"="+v)
	}
	return env, nil
}
```
Then add `agentEnvWithRules(ctx, binding, rules)` that calls the existing `agentEnv` then `applyCapabilityEnv`. The agent adapter and chat adapter store `rules provider.CapabilityRules` (set in their constructors) and call the `WithRules` form. Keep `agentEnv` (credential-only) intact so its existing tests pass; production call sites switch to the `WithRules` form. The chat path (`chat_adapter.go`) builds its env similarly — route it through `applyCapabilityEnv` too so chat-kind 200k profiles work.

- [ ] **Step 4: Run, verify pass** — `go test ./adapter/claudecli/ -v`

- [ ] **Step 5: Commit** — `git commit -m "feat(claudecli): emit context-window + effort env"`

---

### Task 3: codex effort emission

**Files:**
- Modify: `adapter/codex/args.go` (`buildArgs`, `buildExecArgs`)
- Modify: `adapter/codex/stream_adapter.go` (pass effort emission)
- Test: `adapter/codex/args_test.go` (extend)

- [ ] **Step 1: Write failing tests**

```go
func TestBuildArgs_AppendsEffort(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "codex", Model: "gpt-5.5", Effort: provider.EffortMedium}
	args, err := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	if err != nil { t.Fatal(err) }
	assertContainsSeq(t, args, "-c", "model_reasoning_effort=medium")
	assertContainsSeq(t, args, "-m", "gpt-5.5")
}

func TestBuildArgs_NoEffortWhenDefault(t *testing.T) {
	rules := testCapRules()
	binding := provider.ResolvedBinding{Backend: "codex", Model: "gpt-5.5"}
	args, _ := buildArgsWithRules(engine.AgentMission{WorkDir: "/w"}, binding, rules)
	if contains(args, "model_reasoning_effort=default") || contains(args, "-c") {
		t.Fatalf("default effort must not emit -c: %v", args)
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement** — extend `buildExecArgs` to accept extra args and append them after the model flag:

```go
func buildExecArgs(model, workDir string, extra []string) ([]string, error) {
	model = strings.TrimSpace(model)
	if model == "" { return nil, ErrModelRequired }
	args := []string{execSubcommand, bypassFlag, modelFlag, model}
	args = append(args, extra...)            // e.g. -c model_reasoning_effort=medium
	args = append(args, cdFlag, workDir, jsonFlag)
	return args, nil
}

func buildArgsWithRules(mission engine.AgentMission, binding provider.ResolvedBinding, rules provider.CapabilityRules) ([]string, error) {
	em, err := rules.Emit(binding.Backend, binding.Model, binding.ContextWindow, binding.Effort)
	if err != nil { return nil, err }
	return buildExecArgs(binding.Model, mission.WorkDir, em.Args)
}
```
Keep the old `buildArgs(mission, binding)` as a thin wrapper passing `nil` extra (so existing call sites/tests compile), and switch the provider + stream adapter to the `WithRules` form, threading the `rules` field stored on the codex provider struct. The stream path computes `em.Args` from its `ChatRequest`-derived binding the same way.

- [ ] **Step 4: Run, verify pass** — `go test ./adapter/codex/ -v`

- [ ] **Step 5: Commit** — `git commit -m "feat(codex): emit model_reasoning_effort via -c"`

---

### Task 4: Wire rules into adapters at the composition root

**Files:**
- Modify: `cmd/arbitron/main.go` (`initDomain` — pass the `rules` built in Phase 2 Task 5 into the claudecli + codex provider constructors)
- Test: covered by adapter tests + a build/smoke check

- [ ] **Step 1:** Thread the already-loaded `provider.CapabilityRules` into the claudecli agent/chat adapter constructors and the codex provider constructor (add a `rules` parameter/field). Default to an empty `CapabilityRules{}` only in tests; production always passes the loaded rules.
- [ ] **Step 2: Build + full adapter tests** — `go build ./... && go test ./adapter/claudecli/... ./adapter/codex/... ./domain/provider/...`
- [ ] **Step 3: Commit** — `git commit -m "feat(cmd): inject capability rules into claude/codex adapters"`

---

## Self-Review (Phase 3)
- **Spec coverage:** §3/§10.4 "context+effort reach claude-code and codex at runtime" → Tasks 2-3; deterministic mapping → Task 1. ✓
- **Type consistency:** `Emit`/`Emission`/`applyCapabilityEnv`/`buildArgsWithRules` consistent; effective-backend assumption matches design (factory overwrites `binding.Backend` before dispatch). ✓
- **Placeholder scan:** none. opencode intentionally emits nothing (documented).
