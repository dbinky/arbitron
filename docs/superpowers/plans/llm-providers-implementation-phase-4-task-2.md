# LLM Providers — Phase 4 Task 2 Implementation Plan: env-table extension (incl. Bedrock AWS quartet), provider.go run/parse, version probe, fail-closed Factory

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `adapter/opencode/env_providers.go` (the single-source-of-truth auth table) to the full modern provider set — anthropic, openai, openrouter, azure, ollama, amazon-bedrock — including the **Bedrock AWS credential quartet** (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, optional `AWS_SESSION_TOKEN`, `AWS_REGION`) sourced from `Extras` + the sealed `RevealKey` composite (same payload shape as Phase 3) and the keyless **local-Ollama base-url-only** path; rewrite `provider.go` `RunAgent` to build the credential env FIRST (no-partial-env-launch invariant), add a **minimum-version probe** that fails on an incompatible binary, drain/parse the modern `run` output into `engine.AgentResult`, and add **fail-closed** Factory wiring (open-by-default `AllowAllTools=true`).

**Architecture:** All changes within `adapter/opencode` plus a one-arm extension of `adapter/provider/factory.go`. The credential env stays env-only (never argv/logs), reusing the shared `adapter/claudecli.CommandRunner` + `RunSpec{Bin,Args,Dir,Env,MaxResponseBytes}` seam. The version probe runs once (memoized) on first call via the same runner, so it is hermetically testable with the fake runner. The Bedrock composite is parsed by the **shared `adapter/awssig.ParseCredential`** (the SAME tolerant bare-string-or-JSON parser Phase 3's bedrock chat adapter uses) — not a private copy.

**Tech Stack:** Go, table-driven `testing` + `testify`, the `adapter/opencode` `fakeRunner` (a `claudecli.CommandRunner` that records `RunSpec` argv/env/dir and replays canned stdout) already present in `adapter/opencode/provider_test.go`.

## Shared contracts / alignment (READ FIRST)

The Bedrock sealed-credential payload is parsed by the **shared leaf package
`adapter/awssig`** (created in Phase 3 Task 1) — NOT by a private parser in this package.
`bedrockEnv` MUST call `awssig.ParseCredential(sealed) (awssig.Credential, error)` and read
`cred.SecretAccessKey` / `cred.SessionToken`. Do **NOT** define a local
`parseBedrockSecret`/`bedrockComposite` — that would be a second copy of the parser the
plan-alignment forbids. Add the import `"github.com/arbitron/arbitron/adapter/awssig"` to
`env.go` and list `adapter/awssig` as a dependency.

**Dependencies:**
- **Task 1** (this phase): `mapProviderID`, `buildArgs(mission, binding, posture)`, `buildPosture`, `Posture`, `ErrModelRequired`, the `Config.AllowAllTools` knob.
- **`adapter/awssig` (Phase 3 Task 1):** `awssig.ParseCredential` + `awssig.Credential` — the single shared sealed-credential parser. The sealed `RevealKey` payload is `{"secret":"...","session_token":"..."}` JSON when a session token is present, else the **bare** secret-access-key string. `Extras["region"]` (required) and `Extras["access-key-id"]` (required) are non-secret. This task imports `adapter/awssig` directly (a stdlib-only leaf, no cycle) and does NOT import `adapter/bedrock`.
- `domain/provider.ResolvedBinding` (Provider, BaseURL, Extras, Model, AllowedTools, RevealKey), `domain/engine.AgentMission`/`AgentResult`/`AgentStep`, `adapter/procenv` (`BaseEnv`, `IsBaseName`, `WithTimeout`, `StandardBinDirs`), `adapter/claudecli` (`RunSpec`, `CommandRunner`, `NewExecRunner`).

**Out of scope (Task 2):** chat-path adapters (Phases 1–3), validation CLI (Phase 5), the claudecli agent adapter.

---

## File Structure

- **Modify** `adapter/opencode/env_providers.go` — extend `providerEnvVarTable` to the full set; add `ollama` (key + base-url) and the `amazon-bedrock` marker; add an `aws` flag so `bedrock` routes to the AWS-quartet builder instead of the single-key path.
- **Modify** `adapter/opencode/env.go` — `opencodeEnv` branches: AWS-quartet providers (bedrock) → `bedrockEnv`; keyless local-Ollama (no RevealKey / no key) → base-url-only; everyone else → existing single-key path. Add `bedrockEnv` (which calls the shared `awssig.ParseCredential` — NO local composite parser).
- **Create** `adapter/opencode/version.go` — `minOpencodeVersion` constant + `probeVersion(ctx, runner, bin)` running `opencode --version`, parsing it, and returning `ErrIncompatibleOpencode` when below minimum.
- **Modify** `adapter/opencode/provider.go` — rewrite `RunAgent`: env-first, version-probe (memoized) before launch, modern-output drain/parse; remove the stale single-shot doc comments; set `allowAllTools` from `Config`.
- **Modify** `adapter/opencode/parse.go` — keep tolerant text folding; document the modern `run` output is still line-oriented assistant text (no structured usage in this mode), so the coarse token accounting and step-folding contract is unchanged.
- **Modify** `adapter/provider/factory.go` — set `AllowAllTools: true` in the opencode arm (open-by-default) and add a `Deps.OpencodeAllowAllTools *bool` override (nil = default true). Fail-closed comment.
- **Modify/extend** `adapter/opencode/env_test.go`, `adapter/opencode/provider_test.go` — new env/version/run tests across the five categories.
- **Modify** `adapter/provider/factory_test.go` (or create if absent) — assert the opencode arm passes `AllowAllTools=true` by default and respects the override.

---

### Task 2.1: Extend the env table for the full provider set (`env_providers.go`)

**Files:**
- Modify: `adapter/opencode/env_providers.go`
- Test: `adapter/opencode/env_test.go`

- [ ] **Step 1: Write the failing test**

Append to `adapter/opencode/env_test.go`:

```go
// All non-AWS, keyed providers map to a key var + base-url var.
func TestProviderEnvVars_FullKeyedSet(t *testing.T) {
	cases := []struct{ prov, keyVar, baseURLVar string }{
		{"anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"},
		{"openai", "OPENAI_API_KEY", "OPENAI_BASE_URL"},
		{"openrouter", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL"},
		{"azure", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_ENDPOINT"},
		{"ollama", "OLLAMA_API_KEY", "OLLAMA_BASE_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.prov, func(t *testing.T) {
			k, b, ok := providerEnvVars(tc.prov)
			require.True(t, ok)
			assert.Equal(t, tc.keyVar, k)
			assert.Equal(t, tc.baseURLVar, b)
		})
	}
}

// amazon-bedrock is registered and flagged as an AWS-quartet provider.
func TestProviderEnvVars_BedrockIsAWS(t *testing.T) {
	entry, ok := providerEnvVarTable["amazon-bedrock"]
	require.True(t, ok, "amazon-bedrock must be registered")
	assert.True(t, entry.aws, "bedrock must route to the AWS-quartet env builder")
}

// Ollama base-url var name is exposed for the keyless local path.
func TestProviderEnvVars_OllamaBaseURLVar(t *testing.T) {
	_, baseURLVar, ok := providerEnvVars("ollama")
	require.True(t, ok)
	assert.Equal(t, "OLLAMA_BASE_URL", baseURLVar)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/opencode/ -run 'TestProviderEnvVars' -v`
Expected: FAIL — `ollama`/`amazon-bedrock` not in table; `entry.aws` field undefined.

- [ ] **Step 3: Write minimal implementation**

Rewrite `adapter/opencode/env_providers.go`:

```go
package opencode

import "errors"

// ErrUnknownProvider is returned when a ResolvedBinding names a provider that
// has no opencode env mapping. Adding a provider is a one-line entry below.
var ErrUnknownProvider = errors.New("opencode: no env mapping for provider")

// providerEnvEntry describes a provider's opencode subprocess env contract.
//   - keyVar/baseURLVar: the env var names carrying the API key and optional
//     base-url override (single-key providers).
//   - aws: when true, the provider authenticates via the AWS credential quartet
//     (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY / optional AWS_SESSION_TOKEN /
//     AWS_REGION) instead of a single key, and keyVar/baseURLVar are unused.
//   - keyless: when true, the provider needs no stored key (local Ollama); only
//     the base-url var is set and RevealKey is not required.
type providerEnvEntry struct {
	keyVar     string
	baseURLVar string
	aws        bool
	keyless    bool
}

// providerEnvVarTable is the single source of truth for opencode's per-provider
// subprocess env contract. Keys are opencode provider ids (the output of
// mapProviderID), so the table and the `-m` flag share one vocabulary.
var providerEnvVarTable = map[string]providerEnvEntry{
	"anthropic":      {keyVar: "ANTHROPIC_API_KEY", baseURLVar: "ANTHROPIC_BASE_URL"},
	"openai":         {keyVar: "OPENAI_API_KEY", baseURLVar: "OPENAI_BASE_URL"},
	"openrouter":     {keyVar: "OPENROUTER_API_KEY", baseURLVar: "OPENROUTER_BASE_URL"},
	"azure":          {keyVar: "AZURE_OPENAI_API_KEY", baseURLVar: "AZURE_OPENAI_ENDPOINT"},
	"ollama":         {keyVar: "OLLAMA_API_KEY", baseURLVar: "OLLAMA_BASE_URL"},
	"amazon-bedrock": {aws: true},
}

// providerEnvVars resolves the key and base-url environment variable names for
// a provider. ok is false for an unknown (or empty) provider. AWS providers
// return ok=true with empty key/base-url names (the caller routes them to the
// AWS-quartet builder via providerEnvEntryFor).
func providerEnvVars(provider string) (keyVar, baseURLVar string, ok bool) {
	v, ok := providerEnvVarTable[provider]
	if !ok {
		return "", "", false
	}
	return v.keyVar, v.baseURLVar, true
}

// providerEnvEntryFor returns the full entry (including the aws/keyless flags)
// for a provider; ok is false for an unknown/empty provider.
func providerEnvEntryFor(provider string) (providerEnvEntry, bool) {
	v, ok := providerEnvVarTable[provider]
	return v, ok
}
```

> **Local vs cloud Ollama:** both are the same opencode provider id `ollama`. The cloud (ollama.com) connection carries a key → the single-key path sets `OLLAMA_API_KEY` (+ base-url). The **local** connection is keyless: its binding has a `BaseURL` and a nil/empty `RevealKey`, so `opencodeEnv` sets only `OLLAMA_BASE_URL`. The `keyless` flag is not set on the table entry (it is a per-binding property, detected at reveal time), so the entry stays `{keyVar, baseURLVar}`; Task 2.2 handles the keyless branch by inspecting the binding, not the table.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/opencode/ -run 'TestProviderEnvVars' -v`
Expected: PASS. Note: `opencodeEnv` in `env.go` still references the old `providerEnvVars` signature (unchanged) plus the new branching lands in 2.2 — `go build` stays green because `providerEnvVars` keeps its signature.

- [ ] **Step 5: Commit**

```bash
git add adapter/opencode/env_providers.go adapter/opencode/env_test.go
git commit -m "feat(opencode): extend auth table to full provider set + bedrock AWS flag (Phase 4 Task 2)"
```

---

### Task 2.2: Bedrock AWS-quartet env + keyless Ollama branch (`env.go`)

**Files:**
- Modify: `adapter/opencode/env.go`
- Test: `adapter/opencode/env_test.go`

- [ ] **Step 1: Write the failing test**

Append to `adapter/opencode/env_test.go`:

```go
// bedrockBinding builds a binding whose Extras carry region + access-key-id and
// whose RevealKey returns the sealed payload (bare secret or JSON composite).
func bedrockBinding(region, accessKeyID, sealedPayload string, err error) provider.ResolvedBinding {
	return provider.ResolvedBinding{
		Provider: "amazon-bedrock",
		Extras: map[string]string{
			"region":        region,
			"access-key-id": accessKeyID,
		},
		RevealKey: func(_ context.Context) (string, error) {
			if err != nil {
				return "", err
			}
			return sealedPayload, nil
		},
	}
}

// EDGE-CASE / HAPPY — bedrock bare-secret payload expands to the 3-var quartet
// (no session token): AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION.
func TestOpencodeEnv_BedrockBareSecret(t *testing.T) {
	b := bedrockBinding("us-east-1", "AKIA-TEST", "raw-secret-key", nil)
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	id, ok := envValue(env, "AWS_ACCESS_KEY_ID")
	require.True(t, ok)
	assert.Equal(t, "AKIA-TEST", id)
	secret, ok := envValue(env, "AWS_SECRET_ACCESS_KEY")
	require.True(t, ok)
	assert.Equal(t, "raw-secret-key", secret)
	region, ok := envValue(env, "AWS_REGION")
	require.True(t, ok)
	assert.Equal(t, "us-east-1", region)
	_, ok = envValue(env, "AWS_SESSION_TOKEN")
	assert.False(t, ok, "no session token => AWS_SESSION_TOKEN omitted")
}

// EDGE-CASE — bedrock JSON composite payload expands to the FULL quartet incl.
// AWS_SESSION_TOKEN.
func TestOpencodeEnv_BedrockComposite(t *testing.T) {
	payload := `{"secret":"composite-secret","session_token":"sess-123"}`
	b := bedrockBinding("eu-west-1", "AKIA-2", payload, nil)
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	secret, _ := envValue(env, "AWS_SECRET_ACCESS_KEY")
	assert.Equal(t, "composite-secret", secret)
	tok, ok := envValue(env, "AWS_SESSION_TOKEN")
	require.True(t, ok)
	assert.Equal(t, "sess-123", tok)
	region, _ := envValue(env, "AWS_REGION")
	assert.Equal(t, "eu-west-1", region)
}

// INPUT-VALIDATION — bedrock missing region or access-key-id fails before reveal.
func TestOpencodeEnv_BedrockMissingExtras(t *testing.T) {
	for _, tc := range []struct{ region, id string }{
		{"", "AKIA"},
		{"us-east-1", ""},
	} {
		b := bedrockBinding(tc.region, tc.id, "secret", nil)
		env, err := opencodeEnv(context.Background(), b)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrBedrockConfig)
		assert.Nil(t, env, "no partial env on missing bedrock config")
	}
}

// ERROR — bedrock RevealKey failure aborts with no env.
func TestOpencodeEnv_BedrockRevealError(t *testing.T) {
	b := bedrockBinding("us-east-1", "AKIA", "", errors.New("decrypt boom"))
	env, err := opencodeEnv(context.Background(), b)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decrypt boom")
	assert.Nil(t, env)
}

// SECURITY — neither the bedrock secret nor session token leaks into logs; both
// reach the child env only.
func TestOpencodeEnv_BedrockSecretsNotLogged(t *testing.T) {
	const secret = "super-secret-aws"
	const sess = "super-secret-session"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	payload := `{"secret":"` + secret + `","session_token":"` + sess + `"}`
	b := bedrockBinding("us-east-1", "AKIA", payload, nil)
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	assert.NotContains(t, buf.String(), secret)
	assert.NotContains(t, buf.String(), sess)
	gotSecret, _ := envValue(env, "AWS_SECRET_ACCESS_KEY")
	assert.Equal(t, secret, gotSecret)
}

// EDGE-CASE — keyless local Ollama: a binding with no RevealKey but a BaseURL
// sets ONLY OLLAMA_BASE_URL, no key var, no error.
func TestOpencodeEnv_LocalOllamaKeyless(t *testing.T) {
	b := provider.ResolvedBinding{
		Provider:  "ollama",
		BaseURL:   "http://localhost:11434",
		RevealKey: nil, // keyless local path
	}
	env, err := opencodeEnv(context.Background(), b)
	require.NoError(t, err)

	base, ok := envValue(env, "OLLAMA_BASE_URL")
	require.True(t, ok)
	assert.Equal(t, "http://localhost:11434", base)
	_, ok = envValue(env, "OLLAMA_API_KEY")
	assert.False(t, ok, "keyless local ollama must NOT set a key var")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/opencode/ -run 'TestOpencodeEnv_Bedrock|TestOpencodeEnv_LocalOllama' -v`
Expected: FAIL — `ErrBedrockConfig` undefined; bedrock/keyless branches not implemented.

- [ ] **Step 3: Write minimal implementation**

Rewrite `adapter/opencode/env.go`. Replace the package doc comment's stale "single-shot v0.0.55" preamble with a modern-contract note, and replace `opencodeEnv`:

```go
// Package opencode implements engine.AgentProvider by driving the modern
// `opencode run <prompt> -m <provider>/<model>` contract with per-provider auth
// injected via the subprocess env (never argv/logs) and an open-by-default
// permission posture. See args.go (run contract), posture.go (permissions),
// env_providers.go (auth table), and version.go (binary version probe).
package opencode

import (
	"context"
	"errors"
	"fmt"

	"github.com/arbitron/arbitron/adapter/awssig"
	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/provider"
)

// ErrBedrockConfig is returned when a bedrock binding lacks the required
// non-secret Extras (region, access-key-id). It is a configuration-class error
// raised BEFORE any secret is revealed.
var ErrBedrockConfig = errors.New("opencode: bedrock requires region and access-key-id in Extras")

// opencodeEnv returns procenv.BaseEnv() with the provider-keyed credential
// variables appended for the opencode subprocess. The secret is fetched from
// binding.RevealKey(ctx) at call time and placed ONLY in the returned child env;
// it is never logged or written to a file.
//
// On any error (unknown provider, missing bedrock config, RevealKey failure,
// base-name collision) NO env is produced (nil is returned alongside the error)
// so the caller cannot launch with a partial env.
func opencodeEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	entry, ok := providerEnvEntryFor(binding.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, binding.Provider)
	}

	// AWS-quartet providers (bedrock) take a dedicated builder.
	if entry.aws {
		return bedrockEnv(ctx, binding)
	}

	// SECURITY: reject injected names that would shadow the base allowlist.
	if err := rejectBaseNames(entry.keyVar, entry.baseURLVar); err != nil {
		return nil, err
	}

	env := procenv.BaseEnv()

	// Keyless local path (e.g. local Ollama): no RevealKey => set only base-url.
	if binding.RevealKey == nil {
		if binding.BaseURL == "" {
			return nil, fmt.Errorf("opencode: provider %q has no RevealKey and no BaseURL — a connection is required", binding.Provider)
		}
		if entry.baseURLVar != "" {
			env = append(env, entry.baseURLVar+"="+binding.BaseURL)
		}
		return env, nil
	}

	key, err := binding.RevealKey(ctx)
	if err != nil {
		return nil, err
	}
	// A keyless connection may return an empty key via a non-nil RevealKey;
	// treat empty as base-url-only too.
	if key != "" {
		env = append(env, entry.keyVar+"="+key)
	}
	if binding.BaseURL != "" && entry.baseURLVar != "" {
		env = append(env, entry.baseURLVar+"="+binding.BaseURL)
	}
	if key == "" && binding.BaseURL == "" {
		return nil, fmt.Errorf("opencode: provider %q produced no key and no BaseURL", binding.Provider)
	}
	return env, nil
}

// bedrockEnv builds the AWS credential quartet for a bedrock binding. region and
// access-key-id come from non-secret Extras; the secret-access-key (and optional
// session token) come from the sealed RevealKey payload, parsed by the SHARED
// awssig.ParseCredential (the SAME parser Phase 3's bedrock chat adapter uses).
func bedrockEnv(ctx context.Context, binding provider.ResolvedBinding) ([]string, error) {
	region := binding.Extras["region"]
	accessKeyID := binding.Extras["access-key-id"]
	if region == "" || accessKeyID == "" {
		return nil, ErrBedrockConfig
	}
	// SECURITY: reject any base-name collision before revealing.
	if err := rejectBaseNames("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION"); err != nil {
		return nil, err
	}
	if binding.RevealKey == nil {
		return nil, fmt.Errorf("opencode: bedrock binding has no RevealKey — a connection is required")
	}
	sealed, err := binding.RevealKey(ctx)
	if err != nil {
		return nil, err
	}
	// SHARED parser: tolerant bare-string-or-JSON composite (adapter/awssig).
	cred, err := awssig.ParseCredential(sealed)
	if err != nil {
		// awssig.ErrMissingSecret (empty/empty-secret) maps to a config-class error.
		return nil, fmt.Errorf("opencode: bedrock secret-access-key: %w", err)
	}

	env := procenv.BaseEnv()
	env = append(env,
		"AWS_ACCESS_KEY_ID="+accessKeyID,
		"AWS_SECRET_ACCESS_KEY="+cred.SecretAccessKey,
		"AWS_REGION="+region,
	)
	if cred.SessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+cred.SessionToken)
	}
	return env, nil
}
```

Keep `rejectBaseNames` as-is in `env.go` (it is already defined there). Do NOT add a local
`parseBedrockSecret`/`bedrockComposite` — `awssig.ParseCredential` is the single shared parser.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/opencode/ -run 'TestOpencodeEnv' -v`
Expected: PASS for the new bedrock/ollama cases AND the existing keyed-provider cases (`TestOpencodeEnv_HappyPath`, `_ProviderVariants`, `_RevealKeyError`, `_UnknownProvider`, `_EmptyBaseURL`, `_NilRevealKey_Errors`).

> The existing `TestOpencodeEnv_NilRevealKey_Errors` asserts a nil RevealKey for **anthropic** errors. With the new keyless branch, a nil RevealKey + empty BaseURL still errors (anthropic has a base-url var but no BaseURL set in that test) — keep the test but assert the new "no RevealKey and no BaseURL" message, OR give that test binding a BaseURL and assert it now succeeds base-url-only. Choose the former to preserve "anthropic requires a key": update the test to expect an error because anthropic with nil RevealKey and a BaseURL would otherwise silently run unauthenticated — so additionally guard: **only the `ollama` provider may run keyless**. Implement that guard:

Add to `bedrockEnv`/`opencodeEnv` keyless branch a provider-allowlist check:

```go
// keylessProviders may authenticate with no key (local self-hosted endpoints).
var keylessProviders = map[string]bool{"ollama": true}
```

and in the keyless branch:

```go
	if binding.RevealKey == nil {
		if !keylessProviders[binding.Provider] {
			return nil, fmt.Errorf("opencode: provider %q requires a key (nil RevealKey)", binding.Provider)
		}
		if binding.BaseURL == "" {
			return nil, fmt.Errorf("opencode: keyless provider %q requires a BaseURL", binding.Provider)
		}
		env = append(env, entry.baseURLVar+"="+binding.BaseURL)
		return env, nil
	}
```

This keeps `TestOpencodeEnv_NilRevealKey_Errors` (anthropic) red→error and makes `TestOpencodeEnv_LocalOllamaKeyless` pass. Re-run:

Run: `go test ./adapter/opencode/ -run 'TestOpencodeEnv' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/opencode/env.go adapter/opencode/env_test.go
git commit -m "feat(opencode): bedrock AWS-quartet env + keyless local-ollama branch (Phase 4 Task 2)"
```

---

### Task 2.3: Minimum-version probe (`version.go`)

**Files:**
- Create: `adapter/opencode/version.go`
- Test: `adapter/opencode/version_test.go`

- [ ] **Step 1: Write the failing test**

Create `adapter/opencode/version_test.go`:

```go
package opencode

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// versionRunner is a fakeRunner specialization that returns canned --version output.
func versionRunner(line string, startErr error) *fakeRunner {
	return &fakeRunner{lines: []string{line}, startErr: startErr}
}

// HAPPY — a version at/above the minimum probes OK and records `--version` argv.
func TestProbeVersion_Compatible(t *testing.T) {
	fr := versionRunner("opencode "+minOpencodeVersion, nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.NoError(t, err)
	assert.Contains(t, fr.gotSpec.Args, "--version")
}

// ERROR — a version below the minimum fails with ErrIncompatibleOpencode naming
// the found + required versions.
func TestProbeVersion_TooOld(t *testing.T) {
	fr := versionRunner("opencode 0.0.55", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
	assert.Contains(t, err.Error(), "0.0.55")
	assert.Contains(t, err.Error(), minOpencodeVersion)
}

// ERROR — binary missing (start error) surfaces a clear error.
func TestProbeVersion_BinaryMissing(t *testing.T) {
	fr := versionRunner("", errors.New(`exec: "opencode": not found`))
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// EDGE — unparseable version output fails closed (incompatible), not a silent pass.
func TestProbeVersion_Unparseable(t *testing.T) {
	fr := versionRunner("not-a-version", nil)
	err := probeVersion(context.Background(), fr, "opencode")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
}

var _ claudecli.CommandRunner = (*fakeRunner)(nil)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/opencode/ -run TestProbeVersion -v`
Expected: FAIL — `minOpencodeVersion`, `probeVersion`, `ErrIncompatibleOpencode` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `adapter/opencode/version.go`:

```go
package opencode

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/arbitron/arbitron/adapter/claudecli"
)

// minOpencodeVersion is the minimum modern (SST) opencode release with a stable
// `run -m provider/model` contract and config-driven permissions. Earlier
// builds (e.g. the legacy v0.0.55 Go variant) lack `run`/`-m` and are rejected.
//
// NOTE: confirm this against the first SST release exposing stable `run -m` +
// permission config before shipping; it is surfaced in config/docs as the
// runtime prerequisite.
const minOpencodeVersion = "0.4.0"

// ErrIncompatibleOpencode is returned when the resolved opencode binary is older
// than minOpencodeVersion or reports an unparseable version.
var ErrIncompatibleOpencode = errors.New("opencode: incompatible binary version")

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// probeVersion runs `<bin> --version`, parses the first semver-looking token,
// and returns ErrIncompatibleOpencode if it is below minOpencodeVersion or
// unparseable. It uses the shared CommandRunner seam so it is hermetically
// testable with the fake runner.
func probeVersion(ctx context.Context, runner claudecli.CommandRunner, bin string) error {
	lines, wait, err := runner.Run(ctx, claudecli.RunSpec{Bin: bin, Args: []string{"--version"}})
	if err != nil {
		return fmt.Errorf("opencode: version probe: %w", err)
	}
	var out []string
	for l := range lines {
		out = append(out, l)
	}
	if waitErr := wait(); waitErr != nil {
		return fmt.Errorf("opencode: version probe: %w", waitErr)
	}
	found := parseVersion(strings.Join(out, "\n"))
	if found == nil {
		return fmt.Errorf("%w: could not parse version from %q (require >= %s)", ErrIncompatibleOpencode, strings.Join(out, " "), minOpencodeVersion)
	}
	if compareVersion(found, mustParseVersion(minOpencodeVersion)) < 0 {
		return fmt.Errorf("%w: found %d.%d.%d, require >= %s", ErrIncompatibleOpencode, found[0], found[1], found[2], minOpencodeVersion)
	}
	return nil
}

func parseVersion(s string) []int {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	out := make([]int, 3)
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

func mustParseVersion(s string) []int {
	v := parseVersion(s)
	if v == nil {
		panic("opencode: bad minOpencodeVersion constant: " + s)
	}
	return v
}

// compareVersion returns -1, 0, or 1 comparing a to b major.minor.patch.
func compareVersion(a, b []int) int {
	for i := 0; i < 3; i++ {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/opencode/ -run TestProbeVersion -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/opencode/version.go adapter/opencode/version_test.go
git commit -m "feat(opencode): minimum-version probe via shared runner seam (Phase 4 Task 2)"
```

---

### Task 2.4: Rewrite `RunAgent` — env-first, version-probe, modern parse (`provider.go`)

**Files:**
- Modify: `adapter/opencode/provider.go`
- Test: `adapter/opencode/provider_test.go`

- [ ] **Step 1: Write the failing test**

Replace the stale single-shot assertions in `adapter/opencode/provider_test.go`. Add/rewrite these (the `fakeRunner` and `anthropicBinding` helpers already exist). The version probe runs first, so a single mission produces **two** runner invocations (probe then run); extend `fakeRunner` to script multiple invocations:

```go
// multiRunner replays a sequence of canned invocations (probe, then run...),
// recording each RunSpec. It satisfies claudecli.CommandRunner.
type multiRunner struct {
	scripts []runScript
	calls   []claudecli.RunSpec
	idx     int
}

type runScript struct {
	lines    []string
	waitErr  error
	startErr error
}

func (m *multiRunner) Run(ctx context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	m.calls = append(m.calls, spec)
	var s runScript
	if m.idx < len(m.scripts) {
		s = m.scripts[m.idx]
	}
	m.idx++
	if s.startErr != nil {
		return nil, nil, s.startErr
	}
	ch := make(chan string)
	go func() {
		defer close(ch)
		for _, l := range s.lines {
			ch <- l
		}
	}()
	return ch, func() error { return s.waitErr }, nil
}

func compatibleProbe() runScript { return runScript{lines: []string{"opencode " + minOpencodeVersion}} }

// HAPPY — agent mission: probe passes, then `run -m anthropic/<model>` argv with
// the injected env; canned output folds into an AgentResult.
func TestRunAgent_ModernRunContract(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{"Working.", "Edited hello.txt.", "Done: goodbye."}},
	}}
	p := New(Config{
		Binding:       anthropicBinding("sk-sentinel", nil),
		BinaryPath:    "opencode",
		Runner:        mr,
		AllowAllTools: true,
	})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{
		Prompt:  "edit hello.txt to say goodbye",
		WorkDir: "/tmp/ws",
		Model:   "anthropic/claude-3-5-sonnet",
	})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Contains(t, res.Summary, "goodbye")

	// Second call is the run; assert modern argv.
	require.GreaterOrEqual(t, len(mr.calls), 2)
	runSpec := mr.calls[1]
	assert.Equal(t, "run", runSpec.Args[0])
	m, ok := argValue(runSpec.Args, "-m")
	require.True(t, ok)
	assert.Equal(t, "anthropic/claude-3-5-sonnet", m)
	assert.Equal(t, "/tmp/ws", runSpec.Dir, "WorkDir is the cwd pin")
	assert.Contains(t, runSpec.Args, permissionAllowAllFlag, "open-by-default posture")
	key, ok := envValue(runSpec.Env, "ANTHROPIC_API_KEY")
	require.True(t, ok)
	assert.Equal(t, "sk-sentinel", key)
}

// SECURITY — RevealKey failure aborts BEFORE any subprocess launch (no probe, no run).
func TestRunAgent_RevealKeyAbortsBeforeLaunch(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"ok"}}}}
	p := New(Config{
		Binding:       anthropicBinding("", errors.New("decrypt boom")),
		BinaryPath:    "opencode",
		Runner:        mr,
		AllowAllTools: true,
	})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "decrypt boom")
	assert.Empty(t, mr.calls, "no subprocess (not even the version probe) may launch when env fails")
}

// ERROR — incompatible binary: probe fails => mission fails closed, run never launches.
func TestRunAgent_IncompatibleBinary(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{{lines: []string{"opencode 0.0.55"}}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrIncompatibleOpencode)
	assert.Len(t, mr.calls, 1, "only the probe ran; the run never launched")
}

// INPUT-VALIDATION — empty WorkDir rejected before env build and before any launch.
func TestRunAgent_EmptyWorkDirModern(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{compatibleProbe(), {lines: []string{"ok"}}}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "", Model: "anthropic/m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrWorkDirRequired)
	assert.Empty(t, mr.calls, "no launch on empty WorkDir")
}

// FAIL-CLOSED — unknown provider names provider+node in the error, no run launches.
func TestRunAgent_UnknownProviderFailsClosed(t *testing.T) {
	mr := &multiRunner{}
	binding := provider.ResolvedBinding{
		Provider:  "mystery",
		Model:     "m",
		RevealKey: func(context.Context) (string, error) { return "k", nil },
	}
	p := New(Config{Binding: binding, BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	res, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "m"})
	require.Error(t, err)
	assert.Nil(t, res)
	assert.ErrorIs(t, err, ErrUnknownProvider)
	assert.Contains(t, err.Error(), "mystery", "error must name the provider")
	assert.Empty(t, mr.calls, "fail-closed: never a silent tool-less run")
}

// SECURITY — the version probe is memoized: a second mission does NOT re-probe.
func TestRunAgent_ProbeMemoized(t *testing.T) {
	mr := &multiRunner{scripts: []runScript{
		compatibleProbe(),
		{lines: []string{"first"}},
		{lines: []string{"second"}},
	}}
	p := New(Config{Binding: anthropicBinding("k", nil), BinaryPath: "opencode", Runner: mr, AllowAllTools: true})
	for i := 0; i < 2; i++ {
		_, err := p.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "anthropic/m"})
		require.NoError(t, err)
	}
	// 1 probe + 2 runs = 3 calls; only the first is `--version`.
	require.Len(t, mr.calls, 3)
	assert.Contains(t, mr.calls[0].Args, "--version")
	assert.NotContains(t, mr.calls[1].Args, "--version")
	assert.NotContains(t, mr.calls[2].Args, "--version")
}
```

Remove the now-obsolete `TestRunAgent_HappyPath` argv assertions (the `-p`/`-c` ones) if not already updated in Task 1, and delete `TestRunAgent_Variants`' `-q`/`-f` assertions (modern contract has no `-q`/`-f`). Keep `TestRunAgent_NonZeroExit`, `_BinaryNotFound`, `_ContextCancel`, `_Timeout`, `_EmptyOutput`, `_KeyAbsentFromLogs` but adapt them to the `multiRunner` (probe + run) where they previously used `fakeRunner` — or keep `fakeRunner` for them by making the probe memoization injectable (see Step 3: `New` accepts a pre-satisfied probe in tests via a `skipVersionProbe` unexported field set through a test helper, OR simpler: those tests use `multiRunner` with `compatibleProbe()` first). Prefer converting them to `multiRunner`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/opencode/ -run TestRunAgent -v`
Expected: FAIL — `AllowAllTools`/probe/memoization not wired; argv still legacy.

- [ ] **Step 3: Write minimal implementation**

Rewrite the relevant parts of `adapter/opencode/provider.go`:

1. Update the `Provider` struct + `New` to carry `allowAllTools` and a memoized probe:

```go
type Provider struct {
	binding          provider.ResolvedBinding
	binPath          string
	runner           claudecli.CommandRunner
	timeout          time.Duration
	maxResponseBytes int64
	allowAllTools    bool

	probeOnce sync.Once
	probeErr  error
}
```

```go
func New(cfg Config) *Provider {
	runner := cfg.Runner
	if runner == nil {
		runner = claudecli.NewExecRunner()
	}
	return &Provider{
		binding:          cfg.Binding,
		binPath:          resolveBinary(cfg.BinaryPath),
		runner:           runner,
		timeout:          cfg.Timeout,
		maxResponseBytes: cfg.MaxResponseBytes,
		allowAllTools:    cfg.AllowAllTools,
	}
}
```

2. Rewrite `RunAgent` with strict ordering — **WorkDir guard → env (fail-closed, no launch) → version probe (memoized) → run → parse**:

```go
func (p *Provider) RunAgent(ctx context.Context, mission engine.AgentMission) (*engine.AgentResult, error) {
	if mission.WorkDir == "" {
		return nil, ErrWorkDirRequired
	}

	// Build args FIRST so an unknown provider / empty model fails closed with no
	// launch (NEVER a silent tool-less run). buildArgs names the provider in the
	// error; we add the node/model context.
	args, err := buildArgs(mission, p.binding, buildPosture(p.binding, p.allowAllTools))
	if err != nil {
		return nil, fmt.Errorf("opencode agent (provider %q, model %q): %w", p.binding.Provider, mission.Model, err)
	}

	// Build the credential env BEFORE any subprocess so a RevealKey / config
	// failure aborts before launch (no-partial-env, no probe).
	env, err := opencodeEnv(ctx, p.binding)
	if err != nil {
		return nil, fmt.Errorf("opencode: build env: %w", err)
	}

	// Version probe (memoized): an incompatible/missing binary fails closed here,
	// before the run launches.
	if err := p.ensureCompatible(ctx); err != nil {
		return nil, err
	}

	timeout := mission.Timeout
	if timeout <= 0 {
		timeout = p.timeout
	}
	runCtx, cancel := procenv.WithTimeout(ctx, timeout)
	defer cancel()

	spec := claudecli.RunSpec{
		Bin:              p.binPath,
		Args:             args,
		Dir:              mission.WorkDir,
		Env:              env,
		MaxResponseBytes: p.maxResponseBytes,
	}

	lines, wait, err := p.runner.Run(runCtx, spec)
	if err != nil {
		return nil, fmt.Errorf("opencode: run: %w", err)
	}
	var collected []string
	for line := range lines {
		collected = append(collected, line)
	}
	if waitErr := wait(); waitErr != nil {
		if ctxErr := runCtx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("opencode: mission failed: %w", waitErr)
	}

	summary, steps, tokensOut := parseOutput(collected)
	engineSteps := make([]engine.AgentStep, 0, len(steps))
	for _, s := range steps {
		engineSteps = append(engineSteps, engine.AgentStep{Kind: s.Kind, Summary: s.Summary})
	}
	return &engine.AgentResult{
		Summary:   summary,
		Steps:     engineSteps,
		Model:     mission.Model,
		TokensOut: tokensOut,
	}, nil
}

// ensureCompatible runs the version probe at most once per Provider.
func (p *Provider) ensureCompatible(ctx context.Context) error {
	p.probeOnce.Do(func() {
		p.probeErr = probeVersion(ctx, p.runner, p.binPath)
	})
	return p.probeErr
}
```

> **Ordering subtlety for `TestRunAgent_RevealKeyAbortsBeforeLaunch`:** that test expects **zero** runner calls (not even the probe) on a RevealKey failure. The ordering above builds args, then env (which fails on RevealKey) BEFORE `ensureCompatible` — so the probe never runs. Good. For `TestRunAgent_UnknownProviderFailsClosed`, `buildArgs` errors first → also zero calls. For `TestRunAgent_IncompatibleBinary`, args+env succeed, probe runs (1 call) and fails → run never launches. All three match the asserted call counts.

3. Add `import "sync"`; remove the stale single-shot doc comments on `Provider`/`RunAgent` and replace with a modern-contract note.

- [ ] **Step 4: Run the full package test to verify it passes**

Run: `go test ./adapter/opencode/ -v`
Expected: PASS (all `TestRunAgent*`, `TestBuildArgs*`, `TestOpencodeEnv*`, `TestProbeVersion*`, `TestParseOutput`).

- [ ] **Step 5: Commit**

```bash
git add adapter/opencode/provider.go adapter/opencode/provider_test.go adapter/opencode/parse.go
git commit -m "feat(opencode): env-first + memoized version probe + modern run/parse (Phase 4 Task 2)"
```

---

### Task 2.5: Factory wiring — open-by-default + fail-closed (`factory.go`)

> **Shared-file ordering:** `adapter/provider/factory.go` is edited by P1 T1 (openai/ollama `ChatProviderFor` arm), P2 T2 (anthropic-api arm), P3 T2 (bedrock arm), and this task. This task touches a DIFFERENT part: the `AgentProviderFor` opencode arm (`AllowAllTools`) and a new `Deps.OpencodeAllowAllTools` field — it does NOT touch any `ChatProviderFor` arm. No real conflict with P1–P3; apply in phase order (1→4).

**Files:**
- Modify: `adapter/provider/factory.go`
- Test: `adapter/provider/factory_test.go`

- [ ] **Step 1: Write the failing test**

Append to `adapter/provider/factory_test.go` (create the file with package `provider` if it does not exist). The test asserts the opencode arm is constructed with `AllowAllTools=true` by default and honors an override. Because `opencode.Config` is consumed inside `AgentProviderFor`, assert behavior via a constructed binding routed through the factory and a fake runner that records the run argv (open-by-default emits the allow-all flag):

```go
package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/adapter/claudecli"
	"github.com/arbitron/arbitron/domain/engine"
	domprovider "github.com/arbitron/arbitron/domain/provider"
)

// recRunner records every RunSpec and replays a compatible version probe then output.
type recRunner struct {
	calls []claudecli.RunSpec
	idx   int
}

func (r *recRunner) Run(_ context.Context, spec claudecli.RunSpec) (<-chan string, func() error, error) {
	r.calls = append(r.calls, spec)
	line := "ok"
	if len(spec.Args) > 0 && spec.Args[len(spec.Args)-1] == "--version" {
		line = "opencode 99.0.0"
	}
	r.idx++
	ch := make(chan string, 1)
	ch <- line
	close(ch)
	return ch, func() error { return nil }, nil
}

func openAgentBinding() domprovider.ResolvedBinding {
	return domprovider.ResolvedBinding{
		Kind:      domprovider.ProfileKindAgent,
		Provider:  "openai",
		Model:     "gpt-4o",
		RevealKey: func(context.Context) (string, error) { return "sk-x", nil },
	}
}

// Default factory => opencode runs open-by-default (allow-all flag emitted).
func TestAgentProviderFor_OpenByDefault(t *testing.T) {
	rr := &recRunner{}
	f := NewFactory(Deps{Runner: rr})
	ap, err := f.AgentProviderFor(openAgentBinding())
	require.NoError(t, err)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-4o"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(rr.calls), 2)
	runArgs := strings.Join(rr.calls[1].Args, " ")
	assert.Contains(t, runArgs, "--allow-all-tools", "factory default must be open-by-default")
}

// Operator knob (a): OpencodeAllowAllTools=false disables allow-all.
func TestAgentProviderFor_AllowAllDisabled(t *testing.T) {
	rr := &recRunner{}
	disabled := false
	f := NewFactory(Deps{Runner: rr, OpencodeAllowAllTools: &disabled})
	ap, err := f.AgentProviderFor(openAgentBinding())
	require.NoError(t, err)

	_, err = ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "gpt-4o"})
	require.NoError(t, err)
	runArgs := strings.Join(rr.calls[1].Args, " ")
	assert.NotContains(t, runArgs, "--allow-all-tools", "config knob must disable allow-all")
}

// Fail-closed: an unknown agent provider routed to opencode fails closed naming the provider.
func TestAgentProviderFor_UnknownProviderFailsClosed(t *testing.T) {
	rr := &recRunner{}
	f := NewFactory(Deps{Runner: rr})
	binding := domprovider.ResolvedBinding{
		Kind:      domprovider.ProfileKindAgent,
		Provider:  "mystery",
		Model:     "m",
		RevealKey: func(context.Context) (string, error) { return "k", nil },
	}
	ap, err := f.AgentProviderFor(binding)
	require.NoError(t, err, "construction defers; the run fails closed")
	_, runErr := ap.RunAgent(context.Background(), engine.AgentMission{Prompt: "go", WorkDir: "/tmp/ws", Model: "m"})
	require.Error(t, runErr)
	assert.Contains(t, runErr.Error(), "mystery")
	assert.Empty(t, rr.calls, "fail-closed: no subprocess for an undrivable provider")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/provider/ -run TestAgentProviderFor -v`
Expected: FAIL — `Deps.OpencodeAllowAllTools` undefined; opencode arm does not set `AllowAllTools`.

- [ ] **Step 3: Write minimal implementation**

In `adapter/provider/factory.go`:

1. Add to `Deps`:

```go
	// OpencodeAllowAllTools is the operator knob (a) for the agent path's
	// open-by-default permission posture. nil => default true (open). A non-nil
	// false disables the allow-all posture for every opencode-driven provider;
	// per-profile ResolvedBinding.AllowedTools (knob (b)) narrows independently.
	OpencodeAllowAllTools *bool
```

2. In `AgentProviderFor`, set `AllowAllTools` from the deps (default true):

```go
	default:
		allowAll := true
		if f.deps.OpencodeAllowAllTools != nil {
			allowAll = *f.deps.OpencodeAllowAllTools
		}
		// Fail-closed: a provider opencode cannot drive (unknown to its provider
		// map) surfaces a clear agent error at RunAgent time naming the provider;
		// it is NEVER a silent tool-less run.
		return opencode.New(opencode.Config{
			Binding:          b,
			BinaryPath:       f.deps.OpencodeBinaryPath,
			Runner:           f.deps.Runner,
			Timeout:          f.deps.OpencodeTimeout,
			MaxResponseBytes: f.deps.MaxResponseBytes,
			AllowAllTools:    allowAll,
		}), nil
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/provider/ -run TestAgentProviderFor -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/provider/factory.go adapter/provider/factory_test.go
git commit -m "feat(provider): opencode open-by-default agent posture + config knob (Phase 4 Task 2)"
```

---

### Task 2.6: Config plumbing + docs for the version prerequisite + allow-all knob

**Files:**
- Modify: `domain/config/config.go` (LLMConfig)
- Modify: `adapter/config/loader.go` (map config → `Deps.OpencodeAllowAllTools`)
- Modify: `cmd/arbitron/main.go` (thread the knob into `provider.Deps`)
- Modify: project docs (e.g. `README.md` / `docs/` config reference) for the minimum opencode version prerequisite

- [ ] **Step 1: Write the failing test**

Add to the config loader test (follow the existing `adapter/config/loader_test.go` patterns) a case asserting that an `llm.agent_allow_all_tools: false` config value lands as a non-nil `*bool(false)` in the threaded `Deps`, and that omission yields nil (→ default true). Reuse the loader's existing table-test harness; assert on the field added below.

```go
func TestLoad_AgentAllowAllToolsDefaultsOpen(t *testing.T) {
	cfg := mustLoadYAML(t, ``) // omitted key
	assert.Nil(t, cfg.LLM.AgentAllowAllTools, "omitted => nil => default open")
}

func TestLoad_AgentAllowAllToolsDisable(t *testing.T) {
	cfg := mustLoadYAML(t, "llm:\n  agent_allow_all_tools: false\n")
	require.NotNil(t, cfg.LLM.AgentAllowAllTools)
	assert.False(t, *cfg.LLM.AgentAllowAllTools)
}
```

(Adapt `mustLoadYAML` to the loader package's actual test helper name; if none exists, follow the established loader-test construction in `adapter/config/loader_test.go`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/config/ -run AgentAllowAllTools -v`
Expected: FAIL — `LLMConfig.AgentAllowAllTools` undefined.

- [ ] **Step 3: Write minimal implementation**

1. Add to `LLMConfig` in `domain/config/config.go`:

```go
	// AgentAllowAllTools is the operator knob (a) for the opencode agent path's
	// open-by-default permission posture. nil (key omitted) => default ON (open);
	// an explicit false disables the allow-all posture for opencode-driven
	// agent providers. Mirrors claudecli's SkipPermissions stance. Per-profile
	// AllowedTools (knob (b)) narrows independently of this switch.
	AgentAllowAllTools *bool
```

2. In `adapter/config/loader.go`, unmarshal `llm.agent_allow_all_tools` into `*bool` (koanf supports pointer fields; if not, read it as an `Exists`+`Bool` pair into a local `*bool`).

3. In `cmd/arbitron/main.go`, where `provider.Deps` is constructed, set `OpencodeAllowAllTools: cfg.LLM.AgentAllowAllTools`.

- [ ] **Step 4: Document the prerequisite**

Add a short "opencode (agent path) requires opencode >= <minOpencodeVersion>" note plus the `llm.agent_allow_all_tools` switch to the config reference docs and the install docs. Reference the `minOpencodeVersion` constant value (keep the doc and the constant in sync; the version probe enforces it at first agent call with `ErrIncompatibleOpencode`).

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./adapter/config/ ./cmd/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domain/config/config.go adapter/config/loader.go cmd/arbitron/main.go README.md docs/
git commit -m "feat(config): agent_allow_all_tools knob + opencode version prerequisite docs (Phase 4 Task 2)"
```

---

### Task 2.7: Full-suite verification

- [ ] **Step 1: Run the whole hermetic suite**

Run: `make test` (== `go test ./...`)
Expected: PASS, with NO real opencode binary required (every opencode path is driven by the fake/`multiRunner`/`recRunner` seam).

- [ ] **Step 2: Vet/build**

Run: `go vet ./... && go build ./...`
Expected: clean.

- [ ] **Step 3: Commit any cleanup**

```bash
git add -A
git commit -m "chore(opencode): Phase 4 Task 2 cleanup — modern agent path green (no real binary)"
```

---

## TDD test list (five categories) — Task 2

All tests are hermetic (fake `CommandRunner` seam: `fakeRunner` / `multiRunner` / `recRunner`); no real opencode binary.

- **Happy-path:**
  - `TestRunAgent_ModernRunContract` — probe passes, `run -m anthropic/<model>` argv + injected `ANTHROPIC_API_KEY` env + `/tmp/ws` cwd + allow-all flag; output folds to `AgentResult`.
  - `TestProviderEnvVars_FullKeyedSet` — full keyed provider table.
  - `TestOpencodeEnv_BedrockBareSecret` — bedrock bare secret → 3-var quartet.
  - `TestProbeVersion_Compatible` — at/above minimum probes OK; `--version` argv.
  - `TestAgentProviderFor_OpenByDefault` — factory default emits allow-all.
- **Input-validation:**
  - `TestOpencodeEnv_BedrockMissingExtras` — missing region/access-key-id → `ErrBedrockConfig`, no env, before reveal.
  - `TestRunAgent_EmptyWorkDirModern` — empty WorkDir rejected, no launch (`ErrWorkDirRequired`).
  - (Task 1) empty model rejected via `buildArgs` → `ErrModelRequired` (surfaced through `RunAgent`).
- **Error-handling / fail-closed:**
  - `TestRunAgent_IncompatibleBinary` — probe below minimum → `ErrIncompatibleOpencode`, run never launches.
  - `TestRunAgent_UnknownProviderFailsClosed` — unknown provider names provider in error, zero subprocess calls (never a silent tool-less run).
  - `TestAgentProviderFor_UnknownProviderFailsClosed` — fail-closed through the Factory.
  - `TestProbeVersion_TooOld` / `_BinaryMissing` / `_Unparseable`.
  - `TestOpencodeEnv_BedrockRevealError` — RevealKey failure aborts, no env.
- **Edge-cases:**
  - `TestOpencodeEnv_BedrockComposite` — JSON composite expands to the FULL quartet incl. `AWS_SESSION_TOKEN`.
  - `TestOpencodeEnv_LocalOllamaKeyless` — keyless local Ollama sets only `OLLAMA_BASE_URL`, no key var.
  - `TestRunAgent_ProbeMemoized` — version probe runs once across missions.
  - `TestParseOutput` (existing) — tolerant multi-line/empty parsing retained.
- **Security:**
  - `TestRunAgent_RevealKeyAbortsBeforeLaunch` — RevealKey failure → zero subprocess launches (no-partial-env, not even the probe).
  - `TestOpencodeEnv_BedrockSecretsNotLogged` — secret + session token never logged; reach child env only.
  - `TestRunAgent_KeyAbsentFromLogs` (existing, adapted) — key absent from logs/error, present only in `RunSpec.Env`.
  - `TestAgentProviderFor_OpenByDefault` / `_AllowAllDisabled` — open-by-default vs config-disabled allow-all (knob (a)); per-profile narrowing (knob (b)) covered in Task 1's `TestBuildArgs_AllowlistNarrowsToolScope`.

---

## Acceptance criteria — Task 2

1. `make test` (`go test ./...`) passes hermetically — no real opencode binary.
2. `env_providers.go` is the single source of truth covering anthropic, openai, openrouter, azure, ollama, amazon-bedrock; bedrock flagged AWS-quartet.
3. Bedrock composite (`{"secret","session_token"}` JSON) and bare-secret payloads both expand to the AWS env quartet (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, + `AWS_SESSION_TOKEN` when present), with `region`/`access-key-id` from `Extras` — the SAME shape Phase 3 seals.
4. Local Ollama runs keyless: only `OLLAMA_BASE_URL` is set, no key var; non-ollama providers with a nil RevealKey still error.
5. `RunAgent` ordering guarantees: WorkDir guard → args (fail-closed on unknown provider / empty model) → env (fail-closed on RevealKey/bedrock-config) → memoized version probe → run. A RevealKey failure or unknown provider produces ZERO subprocess launches.
6. An incompatible/too-old opencode binary fails closed at first agent call with `ErrIncompatibleOpencode` naming the found + required versions; the minimum version is documented as a runtime prerequisite.
7. The Factory routes all non-claude agent bindings to opencode with `AllowAllTools=true` by default (open-by-default); `Deps.OpencodeAllowAllTools`/`llm.agent_allow_all_tools=false` disables allow-all (knob (a)); per-profile `AllowedTools` narrows (knob (b), Task 1).
8. No secret (key, AWS secret, session token) appears in argv, logs, or error messages — secrets flow only via `RunSpec.Env`.
9. A provider opencode genuinely cannot drive yields a clear, actionable agent error naming the provider — never a silent tool-less run.
