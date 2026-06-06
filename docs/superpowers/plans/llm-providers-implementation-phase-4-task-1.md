# LLM Providers — Phase 4 Task 1 Implementation Plan: modern opencode run-contract, provider-id mapping, ToolScope threading, open-by-default posture

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite `adapter/opencode/args.go` (plus a new `posture.go` and a new `provider_id.go`) from the legacy opencode v0.0.55 single-shot contract to the **modern** `opencode run <prompt> -m <provider>/<model>` contract, mapping arbitron provider strings to opencode provider ids, threading `mission.ToolScope` / `binding.AllowedTools` into opencode's tool/permission selection, and emitting an **open-by-default** (allow-all) permission posture that two operator knobs can narrow.

**Architecture:** Pure-function arg/posture builders in `adapter/opencode`, all driven by the existing `engine.AgentMission` + `provider.ResolvedBinding` seams. `buildArgs` becomes a function of `(mission, binding, posture)` so model routing and tool scope are no longer dropped. A new `AllowAllTools bool` config knob (default true) plus the already-plumbed `ResolvedBinding.AllowedTools` allowlist are the two restriction knobs. No subprocess is touched in this task — `provider.go` wiring of these builders lands in Task 2.

**Tech Stack:** Go, table-driven `testing` + `testify`, the shared `adapter/claudecli.CommandRunner` fake-runner test seam (used only in Task 2; Task 1 unit-tests the pure builders directly).

**Dependencies:**
- `domain/engine.AgentMission{Prompt, WorkDir, ToolScope, SessionID, Model, Timeout}` and `AgentResult` (read-only; unchanged).
- `domain/provider.ResolvedBinding{Kind, Provider, BaseURL, Extras, Model, AllowedTools, RevealKey}` (read-only; unchanged). `AllowedTools` is already populated from the per-profile `Profile.AllowedTools` (PR #18 item 1b).
- Mirrors the claudecli agent posture in `adapter/claudecli/agent_adapter.go` (`SkipPermissions: true` + `AllowedTools: scope` where empty ToolScope falls back to the configured global scope). This task expresses the **same open-by-default contract** for opencode.
- No shared-state dependency on Phase 3 in this task. (The Bedrock credential-composite shape — `{"secret":"...","session_token":"..."}` sealed payload, `region`/`access-key-id` in `Extras` — is consumed by Task 2's env table, not here.)

**Out of scope (Task 1):** `env_providers.go` extension, `provider.go` run/parse, version probe, fail-closed Factory wiring — all in Task 2. Task 1 leaves `provider.go` compiling by updating only its single `buildArgs(...)` call site to the new signature with a default posture.

---

## File Structure

- **Modify** `adapter/opencode/args.go` — rewrite `buildArgs` to the modern `run -m` contract; new signature `buildArgs(mission, binding, posture)`.
- **Create** `adapter/opencode/provider_id.go` — `mapProviderID(arbitronProvider string) (opencodeID string, ok bool)`: arbitron→opencode provider-id mapping (single source of truth, used by both args and Task 2's env table).
- **Create** `adapter/opencode/posture.go` — `Posture` struct + `buildPosture(binding, allowAllDefault)` builder expressing the open-by-default tool/permission stance and the two restriction knobs.
- **Modify** `adapter/opencode/provider.go` — update the one `buildArgs(mission)` call site to `buildArgs(mission, p.binding, p.posture)` and add an `AllowAllTools` field threaded from `Config` (default true). (Full run/parse rewrite is Task 2; this is the minimal call-site fix to keep the package compiling.)
- **Modify** `adapter/opencode/Config` (in `provider.go`) — add `AllowAllTools bool` (the operator knob (a)).
- **Create / extend** `adapter/opencode/args_test.go` — table-driven tests for `buildArgs`, `mapProviderID`, `buildPosture` across the five TDD categories. (The existing `TestBuildArgs` in `provider_test.go` is rewritten/moved here; remove the obsolete single-shot assertions.)

---

## Modern opencode contract (reference for the implementer)

The legacy v0.0.55 contract was `opencode -p "<prompt>" -c <cwd> -q -f text` (no `run`, no `-m`, no per-tool permission). The **modern** (SST) contract this task targets:

```
opencode run "<prompt>" -m <provider>/<model> [permission/tool flags]
```

- The prompt is the **positional** to `run` (not `-p`).
- The working directory is the process **cwd** (`RunSpec.Dir = mission.WorkDir`), still the `{{workspace}}` pin — the empty-WorkDir guard in `provider.go` is retained (Task 2 keeps it; Task 1 does not move it).
- Model routing: `-m <opencode-provider-id>/<model-leaf>` derived from `binding.Provider` + `binding.Model`.
- Permission/tool selection: opencode's modern permission model is config-driven (no single `--dangerously-skip-permissions`). We express it as a small set of flags emitted by `buildPosture` (see Posture section). The **exact flag spelling is localized in one place** (`posture.go`) so it can be re-verified against the installed binary without touching `buildArgs`.

> Note on `-m <provider>/<model>`: `binding.Model` may already carry a `provider/model` form (e.g. `"anthropic/claude-3-5-sonnet"`, see `provider_test.go`). `buildArgs` MUST normalize: if `binding.Model` already contains a `/`, take the leaf after the first `/` as the model and rely on the mapped provider id for the prefix, so the emitted flag is always `-m <mappedID>/<modelLeaf>` and never double-prefixed (e.g. never `-m anthropic/anthropic/claude-...`). Empty model is rejected (input-validation).

---

### Task 1.1: Provider-id mapping (`provider_id.go`)

**Files:**
- Create: `adapter/opencode/provider_id.go`
- Test: `adapter/opencode/args_test.go` (new file; shared with 1.2 and 1.3)

- [ ] **Step 1: Write the failing test**

Create `adapter/opencode/args_test.go` with:

```go
package opencode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

func TestMapProviderID(t *testing.T) {
	cases := []struct {
		arbitron string
		want     string
	}{
		{"anthropic", "anthropic"},
		{"anthropic-api", "anthropic"},
		{"openai", "openai"},
		{"openrouter", "openrouter"},
		{"ollama", "ollama"},
		{"bedrock", "amazon-bedrock"},
		{"amazon-bedrock", "amazon-bedrock"},
		{"azure", "azure"},
		{"azure-openai", "azure"},
		{"azure-foundry", "azure"},
	}
	for _, tc := range cases {
		t.Run(tc.arbitron, func(t *testing.T) {
			got, ok := mapProviderID(tc.arbitron)
			require.True(t, ok, "provider %q must map", tc.arbitron)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMapProviderID_CaseAndWhitespaceInsensitive(t *testing.T) {
	got, ok := mapProviderID("  Anthropic-API ")
	require.True(t, ok)
	assert.Equal(t, "anthropic", got)
}

func TestMapProviderID_UnknownFailsClosed(t *testing.T) {
	for _, p := range []string{"", "mystery", "claude-cli"} {
		_, ok := mapProviderID(p)
		assert.False(t, ok, "provider %q must NOT map (fail-closed)", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/opencode/ -run TestMapProviderID -v`
Expected: FAIL — `undefined: mapProviderID`.

- [ ] **Step 3: Write minimal implementation**

Create `adapter/opencode/provider_id.go`:

```go
package opencode

import "strings"

// providerIDMap maps arbitron provider strings (as they appear on
// ResolvedBinding.Provider) to opencode's provider ids used in `run -m
// <provider>/<model>`. It is the single source of truth shared by args.go
// (the -m flag) and env_providers.go (the auth env table, Task 2).
//
// Note: "claude-cli" is intentionally ABSENT — the Factory never routes a
// claude-cli agent binding to opencode (it uses adapter/claudecli), so a
// claude-cli value here is an unknown provider and must fail closed.
var providerIDMap = map[string]string{
	"anthropic":     "anthropic",
	"anthropic-api": "anthropic",
	"openai":        "openai",
	"openrouter":    "openrouter",
	"ollama":        "ollama",
	"bedrock":       "amazon-bedrock",
	"amazon-bedrock": "amazon-bedrock",
	"azure":         "azure",
	"azure-openai":  "azure",
	"azure-foundry": "azure",
}

// mapProviderID normalizes (trim + lowercase) and maps an arbitron provider
// string to its opencode provider id. ok is false for an unknown or empty
// provider so callers can fail closed.
func mapProviderID(arbitronProvider string) (opencodeID string, ok bool) {
	id, ok := providerIDMap[strings.ToLower(strings.TrimSpace(arbitronProvider))]
	return id, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/opencode/ -run TestMapProviderID -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/opencode/provider_id.go adapter/opencode/args_test.go
git commit -m "feat(opencode): arbitron->opencode provider-id mapping (Phase 4 Task 1)"
```

---

### Task 1.2: Open-by-default permission posture (`posture.go`)

**Files:**
- Create: `adapter/opencode/posture.go`
- Test: `adapter/opencode/args_test.go`

- [ ] **Step 1: Write the failing test**

Append to `adapter/opencode/args_test.go`:

```go
func bindingForArgs(prov, model string, allowed []string) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider:     prov,
		Model:        model,
		AllowedTools: allowed,
	}
}

// Default posture (allowAllDefault=true, no per-profile allowlist) is allow-all.
func TestBuildPosture_DefaultAllowAll(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/claude-3-5-sonnet", nil), true)
	assert.True(t, p.AllowAll, "default posture must be allow-all")
	assert.Empty(t, p.AllowedTools, "no per-profile allowlist => empty narrowing set")
}

// Operator knob (a): config disables allow-all => posture is not allow-all.
func TestBuildPosture_ConfigDisablesAllowAll(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/m", nil), false)
	assert.False(t, p.AllowAll, "config knob must be able to disable allow-all")
}

// Operator knob (b): a per-profile AllowedTools allowlist narrows the tool set
// even when allow-all is the config default.
func TestBuildPosture_ProfileAllowlistNarrows(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/m", []string{"Read", "Write"}), true)
	assert.False(t, p.AllowAll, "a non-empty per-profile allowlist overrides allow-all")
	assert.Equal(t, []string{"Read", "Write"}, p.AllowedTools)
}

// A profile allowlist narrows even when the config knob already disabled allow-all.
func TestBuildPosture_AllowlistWithConfigDisabled(t *testing.T) {
	p := buildPosture(bindingForArgs("anthropic", "anthropic/m", []string{"Read"}), false)
	assert.False(t, p.AllowAll)
	assert.Equal(t, []string{"Read"}, p.AllowedTools)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/opencode/ -run TestBuildPosture -v`
Expected: FAIL — `undefined: buildPosture` / `undefined: Posture`.

- [ ] **Step 3: Write minimal implementation**

Create `adapter/opencode/posture.go`:

```go
package opencode

import "github.com/arbitron/arbitron/domain/provider"

// Posture is the resolved tool/permission stance for one opencode run. It
// mirrors the claudecli agent posture (--dangerously-skip-permissions +
// --allowedTools) so both runners express the SAME open-by-default contract:
//
//   - AllowAll true, AllowedTools nil  => skip-permissions ON, broad tools
//     (the friction-free default for the experimentation harness).
//   - AllowAll false, AllowedTools set => only the named tools are permitted
//     (per-profile narrowing, the operator's knob (b)).
//   - AllowAll false, AllowedTools nil => allow-all disabled by config and no
//     per-profile allowlist (the most restrictive posture the operator can
//     reach without a profile change, knob (a)).
type Posture struct {
	// AllowAll grants every tool/permission for the run when true.
	AllowAll bool
	// AllowedTools, when non-empty, is the explicit tool allowlist that narrows
	// the run regardless of AllowAll.
	AllowedTools []string
}

// buildPosture resolves the run posture from the binding's per-profile
// AllowedTools (knob (b)) and the config-level allow-all default (knob (a)).
//
// A non-empty binding.AllowedTools always wins: it disables allow-all and
// pins the run to the named tools. Otherwise the config default decides
// whether the run is allow-all.
func buildPosture(binding provider.ResolvedBinding, allowAllDefault bool) Posture {
	if len(binding.AllowedTools) > 0 {
		return Posture{AllowAll: false, AllowedTools: binding.AllowedTools}
	}
	return Posture{AllowAll: allowAllDefault}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/opencode/ -run TestBuildPosture -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/opencode/posture.go adapter/opencode/args_test.go
git commit -m "feat(opencode): open-by-default tool/permission posture + two operator knobs (Phase 4 Task 1)"
```

---

### Task 1.3: Modern `run -m` arg builder (`args.go`)

**Files:**
- Modify (rewrite): `adapter/opencode/args.go`
- Test: `adapter/opencode/args_test.go`

- [ ] **Step 1: Write the failing test**

Append to `adapter/opencode/args_test.go`. These tests cover the FIVE TDD categories for `buildArgs`:

```go
// helper: find the value following the first occurrence of flag in args.
func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// HAPPY PATH — correct `run -m <provider>/<model>` argv per provider.
func TestBuildArgs_RunContractPerProvider(t *testing.T) {
	cases := []struct {
		prov     string
		model    string
		wantM    string
	}{
		{"anthropic", "claude-3-5-sonnet", "anthropic/claude-3-5-sonnet"},
		{"anthropic-api", "claude-3-5-sonnet", "anthropic/claude-3-5-sonnet"},
		{"openai", "gpt-4o", "openai/gpt-4o"},
		{"openrouter", "meta-llama/llama-3", "openrouter/meta-llama/llama-3"}, // see note below
		{"ollama", "llama3", "ollama/llama3"},
		{"bedrock", "anthropic.claude-3", "amazon-bedrock/anthropic.claude-3"},
		{"azure-openai", "gpt-4o", "azure/gpt-4o"},
	}
	for _, tc := range cases {
		t.Run(tc.prov, func(t *testing.T) {
			b := bindingForArgs(tc.prov, tc.model, nil)
			args, err := buildArgs(
				engine.AgentMission{Prompt: "do it", WorkDir: "/ws"},
				b,
				Posture{AllowAll: true},
			)
			require.NoError(t, err)
			require.GreaterOrEqual(t, len(args), 1)
			assert.Equal(t, "run", args[0], "first arg must be the run subcommand")
			assert.Contains(t, args, "do it", "prompt is the run positional")
			m, ok := argValue(args, "-m")
			require.True(t, ok, "-m flag must be present")
			assert.Equal(t, tc.wantM, m)
			// Legacy flags are gone.
			joined := strings.Join(args, " ")
			assert.NotContains(t, joined, "-p ")
			assert.NotContains(t, joined, "-c ")
		})
	}
}

// HAPPY PATH — a binding.Model already in provider/model form is not double-prefixed.
func TestBuildArgs_ModelAlreadyPrefixed(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/claude-3-5-sonnet", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	m, _ := argValue(args, "-m")
	assert.Equal(t, "anthropic/claude-3-5-sonnet", m, "must not become anthropic/anthropic/...")
}

// INPUT-VALIDATION — empty model is rejected.
func TestBuildArgs_EmptyModelRejected(t *testing.T) {
	b := bindingForArgs("anthropic", "", nil)
	_, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelRequired)
}

// INPUT-VALIDATION / FAIL-CLOSED — unknown provider yields no argv (fail closed).
func TestBuildArgs_UnknownProviderFailsClosed(t *testing.T) {
	b := bindingForArgs("mystery", "m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownProvider)
	assert.Nil(t, args, "no argv on unknown provider")
}

// SECURITY — default allow-all posture grants broad tools/permissions in the argv.
func TestBuildArgs_AllowAllEmitsBroadPermissions(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: true})
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, permissionAllowAllFlag,
		"allow-all posture must emit the broad-permission flag")
}

// SECURITY — a per-profile AllowedTools allowlist measurably narrows the emitted
// tool scope: only the named tools appear, and the allow-all flag does not.
func TestBuildArgs_AllowlistNarrowsToolScope(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", []string{"Read", "Write"})
	posture := buildPosture(b, true) // config default allow-all, but profile narrows
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, posture)
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, permissionAllowAllFlag, "narrowed run must not be allow-all")
	tools, ok := argValue(args, toolScopeFlag)
	require.True(t, ok, "narrowed run must emit the tool-scope flag")
	assert.Equal(t, "Read,Write", tools)
}

// SECURITY — mission.ToolScope (the per-request scope) narrows when set, even
// without a per-profile allowlist; it takes precedence over the posture allowlist
// the same way claudecli prefers mission.ToolScope over cfg.AllowedTools.
func TestBuildArgs_MissionToolScopeNarrows(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(
		engine.AgentMission{Prompt: "x", WorkDir: "/ws", ToolScope: []string{"Read"}},
		b,
		Posture{AllowAll: true},
	)
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, permissionAllowAllFlag)
	tools, ok := argValue(args, toolScopeFlag)
	require.True(t, ok)
	assert.Equal(t, "Read", tools)
}

// EDGE-CASE — config-disabled allow-all with no allowlist emits neither the
// allow-all flag nor a tool-scope flag (opencode's own default permissioning applies).
func TestBuildArgs_NoAllowAllNoAllowlist(t *testing.T) {
	b := bindingForArgs("anthropic", "anthropic/m", nil)
	args, err := buildArgs(engine.AgentMission{Prompt: "x", WorkDir: "/ws"}, b, Posture{AllowAll: false})
	require.NoError(t, err)
	joined := strings.Join(args, " ")
	assert.NotContains(t, joined, permissionAllowAllFlag)
	assert.NotContains(t, joined, toolScopeFlag)
}
```

> **Note on the openrouter case** (`meta-llama/llama-3` → `openrouter/meta-llama/llama-3`): the model leaf may itself contain a `/`. The de-prefix rule must only strip a leading segment when that segment **equals the mapped opencode provider id**; an unrelated leading segment (a real OpenRouter org like `meta-llama`) is preserved. The implementation below encodes exactly that.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/opencode/ -run TestBuildArgs -v`
Expected: FAIL — new `buildArgs` signature/symbols (`ErrModelRequired`, `permissionAllowAllFlag`, `toolScopeFlag`) undefined, and the old single-shot `TestBuildArgs` in `provider_test.go` will conflict (it is removed in Step 3).

- [ ] **Step 3: Write minimal implementation**

Rewrite `adapter/opencode/args.go` entirely:

```go
package opencode

import (
	"errors"
	"fmt"
	"strings"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrModelRequired is returned by buildArgs when the binding carries no model.
// Modern opencode routes via `-m <provider>/<model>`, so a model is mandatory.
var ErrModelRequired = errors.New("opencode: Model is required for run -m routing")

// Flag spellings for the modern (SST) opencode `run` permission/tool surface.
// LOCALIZED HERE so a binary re-verification can adjust spelling in one place
// without touching buildArgs' structure. permissionAllowAllFlag expresses the
// open-by-default (skip-permissions) stance; toolScopeFlag narrows the tool set.
const (
	runSubcommand = "run"
	// permissionAllowAllFlag grants every tool/permission for the run. It is the
	// modern equivalent of claudecli's --dangerously-skip-permissions. Verify the
	// exact spelling against the installed opencode before release.
	permissionAllowAllFlag = "--allow-all-tools"
	// toolScopeFlag carries a comma-joined tool allowlist (the narrowing knob).
	toolScopeFlag = "--allowed-tools"
)

// buildArgs assembles the modern `opencode run` argv for an agent mission.
//
//	opencode run "<prompt>" -m <provider>/<model> [permission/tool flags]
//
// The WorkDir is NOT an arg: it is the process cwd (RunSpec.Dir), the
// {{workspace}} pin; the empty-WorkDir guard lives in provider.go.
//
// Model routing maps binding.Provider -> opencode provider id and pairs it with
// the model leaf. Tool/permission selection follows the resolved Posture, with
// mission.ToolScope (per-request) preferred over posture.AllowedTools
// (per-profile), mirroring claudecli's ToolScope-over-global precedence.
//
// Fail-closed: an unknown provider or empty model returns (nil, err) so the
// caller never launches a mis-routed or tool-less run.
func buildArgs(mission engine.AgentMission, binding provider.ResolvedBinding, posture Posture) ([]string, error) {
	opencodeID, ok := mapProviderID(binding.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, binding.Provider)
	}
	if strings.TrimSpace(binding.Model) == "" {
		return nil, ErrModelRequired
	}
	modelRef := opencodeID + "/" + modelLeaf(binding.Model, opencodeID)

	args := []string{runSubcommand, mission.Prompt, "-m", modelRef}

	// Tool/permission selection. Per-request mission.ToolScope wins over the
	// per-profile posture.AllowedTools (same precedence as claudecli).
	scope := mission.ToolScope
	if len(scope) == 0 {
		scope = posture.AllowedTools
	}
	switch {
	case len(scope) > 0:
		// Narrowed run: emit only the named tools, never allow-all.
		args = append(args, toolScopeFlag, strings.Join(scope, ","))
	case posture.AllowAll:
		// Open-by-default: grant every tool/permission.
		args = append(args, permissionAllowAllFlag)
	default:
		// Config-disabled allow-all with no allowlist: emit nothing and let
		// opencode's own default permissioning apply.
	}
	return args, nil
}

// modelLeaf strips a leading "<opencodeID>/" prefix from a model string so a
// binding.Model already in provider/model form is not double-prefixed. Only a
// leading segment EQUAL to the mapped opencode id is stripped; an unrelated
// leading segment (e.g. an OpenRouter org) is preserved.
func modelLeaf(model, opencodeID string) string {
	prefix := opencodeID + "/"
	if strings.HasPrefix(model, prefix) {
		return strings.TrimPrefix(model, prefix)
	}
	return model
}
```

Also **remove** the now-obsolete single-shot `TestBuildArgs` from `adapter/opencode/provider_test.go` (the one asserting `-p`, `-c`, `--model` absence) — it is replaced by the new `args_test.go` cases. Leave the rest of `provider_test.go` for Task 2 to update.

- [ ] **Step 4: Update the provider.go call site so the package compiles**

In `adapter/opencode/provider.go`:
1. Add `AllowAllTools bool` to `Config` (default semantics: a fresh fresh `Config{}` has `false`; the Factory/composition root sets it `true` for open-by-default — wired in Task 2). Add a `posture` builder usage on the `Provider`.

For Task 1, make the minimal change to keep compilation green: store the resolved posture-inputs on the `Provider` and update the call site.

```go
// in Config:
	// AllowAllTools enables the open-by-default allow-all permission posture
	// (operator knob (a)). Narrowed further by a per-profile AllowedTools
	// allowlist on the binding. Default true is set by the composition root.
	AllowAllTools bool
```

```go
// in Provider struct add:
	allowAllTools bool

// in New(): set p.allowAllTools = cfg.AllowAllTools

// at the buildArgs call site in RunAgent, replace:
//     Args: buildArgs(mission),
// with:
	args, err := buildArgs(mission, p.binding, buildPosture(p.binding, p.allowAllTools))
	if err != nil {
		return nil, fmt.Errorf("opencode: build args: %w", err)
	}
	spec := claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             args,
		Dir:              mission.WorkDir,
		Env:              env,
		MaxResponseBytes: p.maxResponseBytes,
	}
```

(Task 2 revisits `RunAgent` for the version probe and parse; this is just the compile-keeping call-site change.)

- [ ] **Step 5: Run the full package test to verify it passes**

Run: `go test ./adapter/opencode/ -v`
Expected: PASS for all `TestBuildArgs*`, `TestBuildPosture*`, `TestMapProviderID*`. The existing `provider_test.go` happy-path test (`TestRunAgent_HappyPath`) asserts the **old** `-p`/`-c` argv and will FAIL — that is expected and is rewritten in Task 2. To keep Task 1's commit green in isolation, update `TestRunAgent_HappyPath` and `TestRunAgent_Variants` argv assertions minimally here:
  - replace `-p`/`-c` assertions with `run` + `-m anthropic/claude-3-5-sonnet`;
  - drop the `assert.NotContains(..., "--model")` line (modern contract uses `-m`).

Run again: `go test ./adapter/opencode/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add adapter/opencode/args.go adapter/opencode/args_test.go adapter/opencode/provider.go adapter/opencode/provider_test.go
git commit -m "feat(opencode): modern run -m contract + ToolScope/posture threading (Phase 4 Task 1)"
```

---

## TDD test list (five categories) — Task 1

All Task 1 tests are **pure-function** unit tests (no subprocess); the fake-runner seam (`adapter/claudecli.CommandRunner` fake) is exercised in Task 2.

- **Happy-path:**
  - `TestBuildArgs_RunContractPerProvider` — correct `run -m <provider>/<model>` argv for anthropic / anthropic-api / openai / openrouter / ollama / bedrock / azure-openai; prompt is the `run` positional; no legacy `-p`/`-c`.
  - `TestBuildArgs_ModelAlreadyPrefixed` — `binding.Model` already `provider/model` is not double-prefixed.
  - `TestMapProviderID` — all eight arbitron strings map to the right opencode id.
- **Input-validation:**
  - `TestBuildArgs_EmptyModelRejected` — empty model → `ErrModelRequired`.
  - `TestMapProviderID_CaseAndWhitespaceInsensitive` — normalization.
- **Error-handling / fail-closed:**
  - `TestBuildArgs_UnknownProviderFailsClosed` — unknown provider → `ErrUnknownProvider`, nil argv.
  - `TestMapProviderID_UnknownFailsClosed` — `""`, `mystery`, `claude-cli` do not map.
- **Edge-cases:**
  - `TestBuildArgs_NoAllowAllNoAllowlist` — config-disabled allow-all + no allowlist emits neither flag.
  - openrouter org-in-model leaf (`meta-llama/llama-3`) preserved, covered in `TestBuildArgs_RunContractPerProvider`.
- **Security:**
  - `TestBuildArgs_AllowAllEmitsBroadPermissions` — default allow-all grants broad tools/permissions.
  - `TestBuildArgs_AllowlistNarrowsToolScope` — per-profile `AllowedTools` measurably narrows emitted scope; allow-all flag absent.
  - `TestBuildArgs_MissionToolScopeNarrows` — per-request `mission.ToolScope` narrows and takes precedence.
  - `TestBuildPosture_ConfigDisablesAllowAll` — operator knob (a) can disable allow-all.

---

## Acceptance criteria — Task 1

1. `go test ./adapter/opencode/` passes; `go build ./...` is green.
2. `buildArgs` emits `opencode run <prompt> -m <opencode-provider>/<model>` for every supported provider, with the WorkDir carried as cwd (no `-p`/`-c` legacy flags).
3. `mapProviderID` maps anthropic-api/anthropic→anthropic, openai→openai, openrouter→openrouter, ollama→ollama, bedrock/amazon-bedrock→amazon-bedrock, azure-*→azure; unknown/empty/claude-cli fail closed.
4. Open-by-default: a default-config binding with no per-profile allowlist produces an allow-all posture and emits the broad-permission flag.
5. Both restriction knobs work: config `AllowAllTools=false` disables allow-all; a non-empty `binding.AllowedTools` (or `mission.ToolScope`) narrows the emitted tool scope and suppresses the allow-all flag.
6. Unknown provider and empty model fail closed (error, nil argv) — never a mis-routed launch.
7. No secret is referenced in `args.go` (model/provider strings only; secrets are env-only, handled in Task 2).
