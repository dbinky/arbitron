# Script/Tool-Call Env Hardening + CommandRunner Extraction — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the `script`/`tool_call` process executors from leaking the engine's full environment into subprocesses, replacing it with a three-layer controlled environment, and extract the duplicated process lifecycle into a shared runner.

**Architecture:** Extract the claude-cli env allowlist into a shared `adapter/procenv` package; add a `buildEnv` helper and a shared `run` command-runner in `adapter/process`; refactor both existing executors onto them; add `ScriptConfig.EnvPassthrough` config and a per-node `Config.Env`.

**Tech Stack:** Go, koanf (config), testify (tests), os/exec.

**Spec:** `docs/superpowers/specs/2026-05-28-script-node-executor-design.md`

**Refinement vs spec:** the per-node-env collision rule is enforced at **execution time inside `buildEnv`** (returns an error → node fails red), not as a static `validateNodeConfigs` error. Reason: the passthrough list is operator runtime config that static workflow validation cannot see, and the domain layer must not import the adapter that owns the allowlist. Behavior (a colliding per-node env is rejected) is preserved.

---

## File Structure

- Create `adapter/procenv/procenv.go` — base env allowlist, `BaseEnv()`, `StandardBinDirs()`, `IsBaseName()`. Single source of truth shared by claude-cli and process adapters.
- Create `adapter/procenv/procenv_test.go`.
- Modify `adapter/claudecli/claude.go` — delete local `envAllowlist`/`augmentedEnv`/`standardBinDirs`; consume `procenv`.
- Modify `adapter/claudecli/provider.go:103` — `Env: procenv.BaseEnv()`.
- Modify `domain/workflow/workflow.go` — add `Env map[string]string` to `NodeConfig`.
- Modify `domain/config/config.go` — add `ScriptConfig` + `Config.Script`.
- Modify `adapter/config/loader.go` — default + env split for `script.envpassthrough`.
- Modify `adapter/config/loader_test.go` — env passthrough tests.
- Create `adapter/process/env.go` — `buildEnv(passthrough, nodeEnv, vars)`.
- Create `adapter/process/env_test.go` (package `process`, internal — tests unexported `buildEnv`).
- Create `adapter/process/runner.go` — `commandSpec`, `commandResult`, `run()`.
- Create `adapter/process/runner_test.go` (package `process`, internal).
- Modify `adapter/process/executor_script.go` — refactor onto `run`+`buildEnv`; new constructor arg.
- Modify `adapter/process/executor_toolcall.go` — same.
- Modify `adapter/process/executor_script_test.go` / `executor_toolcall_test.go` — constructor call sites + env assertions.
- Modify `cmd/arbitron/main.go:228-229` — pass `cfg.Script.EnvPassthrough` to both constructors.

---

## Task 1: Shared `adapter/procenv` package

**Files:**
- Create: `adapter/procenv/procenv.go`
- Test: `adapter/procenv/procenv_test.go`

- [ ] **Step 1: Write the failing test**

```go
package procenv_test

import (
	"os"
	"strings"
	"testing"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseEnv_KeepsAllowlistedDropsOthers(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("ARBITRON_SECRET", "leak-me")
	t.Setenv("ANTHROPIC_API_KEY", "leak-me-too")

	env := procenv.BaseEnv()

	joined := strings.Join(env, "\n")
	assert.Contains(t, joined, "HOME=/home/test")
	assert.NotContains(t, joined, "ARBITRON_SECRET")
	assert.NotContains(t, joined, "ANTHROPIC_API_KEY")
}

func TestBaseEnv_AugmentsPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	env := procenv.BaseEnv()

	var path string
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			path = strings.TrimPrefix(e, "PATH=")
		}
	}
	require.NotEmpty(t, path)
	assert.Contains(t, path, "/usr/bin")
	assert.Contains(t, path, "/usr/local/bin")
}

func TestBaseEnv_KeepsLCPrefix(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	env := procenv.BaseEnv()
	assert.Contains(t, strings.Join(env, "\n"), "LC_ALL=en_US.UTF-8")
}

func TestIsBaseName(t *testing.T) {
	assert.True(t, procenv.IsBaseName("PATH"))
	assert.True(t, procenv.IsBaseName("HOME"))
	assert.True(t, procenv.IsBaseName("LC_CTYPE"))
	assert.False(t, procenv.IsBaseName("ANTHROPIC_API_KEY"))
	assert.False(t, procenv.IsBaseName("LLM_API_KEY"))
}

func TestStandardBinDirs(t *testing.T) {
	dirs := procenv.StandardBinDirs()
	assert.Contains(t, dirs, "/usr/local/bin")
}

func TestBaseEnv_DoesNotMutateProcessEnv(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	_ = procenv.BaseEnv()
	assert.Equal(t, "/home/test", os.Getenv("HOME"))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/procenv/...`
Expected: FAIL — package `procenv` does not exist.

- [ ] **Step 3: Write minimal implementation**

```go
// Package procenv builds the controlled subprocess environment shared by the
// claude-cli and process adapters. It exposes a base allowlist (no wholesale
// inheritance of the engine's secrets) and PATH augmentation.
package procenv

import (
	"os"
	"path/filepath"
	"strings"
)

// baseAllowlist is the set of parent-process environment variables propagated
// to subprocesses. SECURITY: any var NOT here (and not LC_*) is stripped, so
// secrets the engine started with (ANTHROPIC_API_KEY, DB URLs, ...) never leak.
var baseAllowlist = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
	"LANG": true, "TERM": true, "TMPDIR": true, "SHELL": true,
	"CLAUDE_CONFIG_DIR": true,
}

// IsBaseName reports whether name is part of the base allowlist (including the
// LC_* locale prefix). Used to reject per-node env keys that would shadow it.
func IsBaseName(name string) bool {
	return baseAllowlist[name] || strings.HasPrefix(name, "LC_")
}

// StandardBinDirs are directories appended to PATH so subprocesses can find
// common tools (git, gh, claude). ~/.local/bin is preferred.
func StandardBinDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{"/usr/local/bin", "/opt/homebrew/bin"}
	if home != "" {
		dirs = append([]string{filepath.Join(home, ".local", "bin")}, dirs...)
	}
	return dirs
}

// BaseEnv returns the allowlist-filtered copy of the current process
// environment, with PATH augmented by StandardBinDirs. This is the safe base
// for subprocesses.
func BaseEnv() []string {
	parent := os.Environ()
	filtered := make([]string, 0, len(baseAllowlist))
	var parentPath string
	havePath := false

	for _, e := range parent {
		i := strings.IndexByte(e, '=')
		if i <= 0 {
			continue
		}
		key := e[:i]
		if !baseAllowlist[key] && !strings.HasPrefix(key, "LC_") {
			continue
		}
		if key == "PATH" {
			parentPath = e[i+1:]
			havePath = true
			continue
		}
		filtered = append(filtered, e)
	}

	existing := make(map[string]bool)
	path := parentPath
	if havePath {
		for _, p := range filepath.SplitList(parentPath) {
			existing[p] = true
		}
	}
	for _, d := range StandardBinDirs() {
		if existing[d] {
			continue
		}
		if path == "" {
			path = d
		} else {
			path += string(os.PathListSeparator) + d
		}
		existing[d] = true
	}
	filtered = append(filtered, "PATH="+path)
	return filtered
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./adapter/procenv/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/procenv/
git commit -m "feat(procenv): shared subprocess env allowlist"
```

---

## Task 2: claude-cli consumes `procenv`

**Files:**
- Modify: `adapter/claudecli/claude.go` (delete `envAllowlist`, `augmentedEnv`, `standardBinDirs`; update `resolveBinaryWith` to call `procenv.StandardBinDirs()`)
- Modify: `adapter/claudecli/provider.go:103` (`Env: augmentedEnv()` → `Env: procenv.BaseEnv()`)

- [ ] **Step 1: Confirm what references the symbols being removed**

Run: `grep -rn "augmentedEnv\|envAllowlist\|standardBinDirs" adapter/claudecli/`
Expected: references in `claude.go` (definitions + `resolveBinaryWith`) and `provider.go:103`. Note any `*_test.go` hits — those tests must be updated or deleted in this task.

- [ ] **Step 2: Edit `claude.go`** — delete the `envAllowlist` var, the `augmentedEnv` func, and the `standardBinDirs` func. In `resolveBinaryWith`, replace the `standardBinDirs()` call with `procenv.StandardBinDirs()`. Add `"github.com/arbitron/arbitron/adapter/procenv"` to imports; remove now-unused imports (`strings` may still be used elsewhere — let the compiler tell you).

- [ ] **Step 3: Edit `provider.go:103`** — change `Env: augmentedEnv(),` to `Env: procenv.BaseEnv(),` and add the `procenv` import.

- [ ] **Step 4: If any claudecli test referenced `augmentedEnv`/`standardBinDirs` directly**, point it at `procenv` equivalents or delete it (the behavior is now covered by `adapter/procenv` tests).

- [ ] **Step 5: Run build + claudecli tests**

Run: `go build ./... && go test ./adapter/claudecli/...`
Expected: PASS (claude-cli behavior unchanged).

- [ ] **Step 6: Commit**

```bash
git add adapter/claudecli/
git commit -m "refactor(claudecli): consume shared procenv allowlist"
```

---

## Task 3: Per-node `Env` field on `NodeConfig`

**Files:**
- Modify: `domain/workflow/workflow.go` (add field to `NodeConfig`)
- Test: `domain/workflow/workflow_test.go` (JSON round-trip) — if no such file, create it.

- [ ] **Step 1: Write the failing test**

```go
func TestNodeConfig_EnvJSONRoundTrip(t *testing.T) {
	in := workflow.NodeConfig{
		ScriptFile: "run.sh",
		Env:        map[string]string{"LLM_MODEL": "{{model}}"},
	}
	b, err := json.Marshal(in)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"env":{"LLM_MODEL":"{{model}}"}`)

	var out workflow.NodeConfig
	require.NoError(t, json.Unmarshal(b, &out))
	assert.Equal(t, "{{model}}", out.Env["LLM_MODEL"])
}
```

(Add imports `encoding/json`, testify, and `workflow` package as needed; follow the existing test file's package + imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./domain/workflow/ -run TestNodeConfig_EnvJSONRoundTrip`
Expected: FAIL — `Env` field undefined.

- [ ] **Step 3: Add the field** in `domain/workflow/workflow.go`, in `NodeConfig`, after `Args`:

```go
	Args            []string          `json:"args,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./domain/workflow/ -run TestNodeConfig_EnvJSONRoundTrip`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/workflow/
git commit -m "feat(workflow): add per-node env map to NodeConfig"
```

---

## Task 4: `ScriptConfig` + loader env pass-through

**Files:**
- Modify: `domain/config/config.go` (add `ScriptConfig`, `Config.Script`)
- Modify: `adapter/config/loader.go` (default + comma-split override)
- Test: `adapter/config/loader_test.go`

- [ ] **Step 1: Write the failing tests**

```go
func TestLoad_ScriptEnvPassthrough_Default(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Empty(t, cfg.Script.EnvPassthrough)
}

func TestLoad_ScriptEnvPassthrough_FromEnv(t *testing.T) {
	t.Setenv("ARBITRON_SCRIPT_ENVPASSTHROUGH", "LLM_API_KEY, LLM_ROUTER_URL ,,OPENAI_API_KEY")
	dir := t.TempDir()
	cfg, err := config.Load(filepath.Join(dir, "c.yaml"))
	require.NoError(t, err)
	assert.Equal(t, []string{"LLM_API_KEY", "LLM_ROUTER_URL", "OPENAI_API_KEY"}, cfg.Script.EnvPassthrough)
}
```

(Match the existing `loader_test.go` package and imports — `config` import path, `filepath`, testify.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./adapter/config/ -run TestLoad_ScriptEnvPassthrough`
Expected: FAIL — `cfg.Script` undefined.

- [ ] **Step 3a: Add the domain type** in `domain/config/config.go`:

In `Config`, add the field:

```go
	Queue     QueueConfig
	Script    ScriptConfig
```

After `LLMConfig` (or near it), add:

```go
// ScriptConfig holds settings for script/tool_call process nodes.
type ScriptConfig struct {
	// EnvPassthrough lists engine-env variable NAMES (not values) that script
	// and tool_call nodes may inherit, in addition to the base allowlist.
	// SECURITY: secrets stay in the deploy environment; only names live here.
	// Without this, a process node sees only the base allowlist.
	EnvPassthrough []string
}
```

- [ ] **Step 3b: Wire the loader** in `adapter/config/loader.go`.

Add to the `defaults()` map:

```go
		"queue.perusercap":      5,
		"script.envpassthrough": []string{},
```

Add a comma-split helper at the bottom of the file:

```go
// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
```

In `Load`, after the `ANTHROPIC_API_KEY` special case and before `k.Unmarshal`, normalize the env var into a real slice (the env provider stores it as a raw string, which would not unmarshal into `[]string`):

```go
	// ARBITRON_SCRIPT_ENVPASSTHROUGH arrives as a comma string via the env
	// provider; overlay it as a real slice so it unmarshals into []string.
	if raw := os.Getenv("ARBITRON_SCRIPT_ENVPASSTHROUGH"); raw != "" {
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"script.envpassthrough": splitCSV(raw),
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading script.envpassthrough: %w", err)
		}
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./adapter/config/ ./domain/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add domain/config/ adapter/config/
git commit -m "feat(config): add script.envPassthrough setting"
```

---

## Task 5: `buildEnv` helper

**Files:**
- Create: `adapter/process/env.go`
- Test: `adapter/process/env_test.go` (package `process` — internal, tests unexported `buildEnv`)

- [ ] **Step 1: Write the failing test**

```go
package process

import (
	"strings"
	"testing"

	"github.com/arbitron/arbitron/domain/variable"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i > 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	return m
}

func TestBuildEnv_BasePresent_SecretsAbsent(t *testing.T) {
	t.Setenv("HOME", "/home/test")
	t.Setenv("MY_SECRET", "leak")
	env, err := buildEnv(nil, nil, variable.NewStore())
	require.NoError(t, err)
	m := envMap(env)
	assert.Equal(t, "/home/test", m["HOME"])
	_, leaked := m["MY_SECRET"]
	assert.False(t, leaked, "non-allowlisted engine var must not leak")
}

func TestBuildEnv_PassthroughPresentWhenSet(t *testing.T) {
	t.Setenv("LLM_API_KEY", "sk-123")
	env, err := buildEnv([]string{"LLM_API_KEY"}, nil, variable.NewStore())
	require.NoError(t, err)
	assert.Equal(t, "sk-123", envMap(env)["LLM_API_KEY"])
}

func TestBuildEnv_PassthroughAbsentWhenUnset(t *testing.T) {
	os.Unsetenv("LLM_ROUTER_URL")
	env, err := buildEnv([]string{"LLM_ROUTER_URL"}, nil, variable.NewStore())
	require.NoError(t, err)
	_, ok := envMap(env)["LLM_ROUTER_URL"]
	assert.False(t, ok, "unset passthrough must not inject an empty var")
}

func TestBuildEnv_PerNodeEnvSubstituted(t *testing.T) {
	vars := variable.NewStore()
	vars.SetInput("model", "opus")
	env, err := buildEnv(nil, map[string]string{"LLM_MODEL": "{{model}}"}, vars)
	require.NoError(t, err)
	assert.Equal(t, "opus", envMap(env)["LLM_MODEL"])
}

func TestBuildEnv_RejectsCollisionWithBase(t *testing.T) {
	_, err := buildEnv(nil, map[string]string{"PATH": "/evil"}, variable.NewStore())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}

func TestBuildEnv_RejectsCollisionWithPassthrough(t *testing.T) {
	_, err := buildEnv([]string{"LLM_API_KEY"}, map[string]string{"LLM_API_KEY": "fake"}, variable.NewStore())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}
```

(Add `"os"` to the import list for the unset test.)

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./adapter/process/ -run TestBuildEnv`
Expected: FAIL — `buildEnv` undefined.

- [ ] **Step 3: Implement `adapter/process/env.go`**

```go
package process

import (
	"fmt"
	"os"
	"strings"

	"github.com/arbitron/arbitron/adapter/procenv"
	"github.com/arbitron/arbitron/domain/variable"
)

// buildEnv constructs the environment for a process node: the base allowlist,
// plus operator-approved passthrough vars (by name, from the engine env), plus
// the node's literal env (with {{var}} substitution). A node-env key colliding
// with a base or passthrough name is rejected to prevent secret/base spoofing.
func buildEnv(passthrough []string, nodeEnv map[string]string, vars *variable.VariableStore) ([]string, error) {
	env := procenv.BaseEnv()

	passSet := make(map[string]bool, len(passthrough))
	for _, name := range passthrough {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		passSet[name] = true
		if val, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+val)
		}
	}

	for k, v := range nodeEnv {
		if procenv.IsBaseName(k) || passSet[k] {
			return nil, fmt.Errorf("node env %q collides with a base or passthrough variable name", k)
		}
		env = append(env, k+"="+variable.Resolve(v, vars))
	}
	return env, nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./adapter/process/ -run TestBuildEnv`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/process/env.go adapter/process/env_test.go
git commit -m "feat(process): three-layer subprocess env builder"
```

---

## Task 6: Shared `run` command runner

**Files:**
- Create: `adapter/process/runner.go`
- Test: `adapter/process/runner_test.go` (package `process` — internal)

- [ ] **Step 1: Write the failing test**

```go
package process

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skipWin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell not supported on Windows")
	}
}

func TestRun_SuccessCapturesStdout(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{Name: "/bin/sh", Args: []string{"-c", "echo hi"}})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	assert.Contains(t, res.Stdout, "hi")
}

func TestRun_NonZeroExitIsNotRunError(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{Name: "/bin/sh", Args: []string{"-c", "echo oops >&2; exit 3"}})
	require.NoError(t, err) // completed-but-failed is NOT a run error
	assert.Equal(t, 3, res.ExitCode)
	assert.Contains(t, res.Stderr, "oops")
}

func TestRun_StartFailureIsRunError(t *testing.T) {
	_, err := run(context.Background(), commandSpec{Name: "/nonexistent/binary-xyz"})
	require.Error(t, err)
}

func TestRun_TimeoutIsRunErrorAndFast(t *testing.T) {
	skipWin(t)
	start := time.Now()
	_, err := run(context.Background(), commandSpec{Name: "/bin/sh", Args: []string{"-c", "sleep 30"}, TimeoutSecs: 1})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}

func TestRun_PassesEnv(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{
		Name: "/bin/sh", Args: []string{"-c", "echo $FOO"}, Env: []string{"FOO=bar"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.Stdout, "bar")
}

func TestRun_PassesStdin(t *testing.T) {
	skipWin(t)
	res, err := run(context.Background(), commandSpec{
		Name: "/bin/sh", Args: []string{"-c", "cat"}, Stdin: []byte("piped"),
	})
	require.NoError(t, err)
	assert.Contains(t, res.Stdout, "piped")
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./adapter/process/ -run TestRun_`
Expected: FAIL — `run`/`commandSpec`/`commandResult` undefined.

- [ ] **Step 3: Implement `adapter/process/runner.go`**

```go
package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// commandSpec describes a subprocess to run.
type commandSpec struct {
	Name        string   // interpreter or command
	Args        []string
	Dir         string   // cwd; "" inherits
	Stdin       []byte   // nil = no stdin
	Env         []string // final "k=v" list; nil inherits the parent env
	TimeoutSecs int      // 0 = no timeout
}

// commandResult is the outcome of a completed (or attempted) subprocess.
type commandResult struct {
	Stdout   string
	Stderr   string // bounded tail (maxOutputBytes)
	ExitCode int
}

// run starts the process and waits with an optional timeout, killing the
// process group on deadline. A completed-but-non-zero exit is reported via
// commandResult.ExitCode with a nil error. run returns a non-nil error only for
// execution faults (start failure, timeout/cancel, or a non-exit Wait error).
func run(ctx context.Context, spec commandSpec) (commandResult, error) {
	execCtx := ctx
	if spec.TimeoutSecs > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(spec.TimeoutSecs)*time.Second)
		defer cancel()
	}

	cmd := exec.Command(spec.Name, spec.Args...)
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if spec.Env != nil {
		cmd.Env = spec.Env
	}
	if spec.Stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.Stdin)
	}
	setProcGroup(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &stdout, limit: maxOutputBytes}
	cmd.Stderr = &limitedWriter{buf: &stderr, limit: maxOutputBytes}

	if err := cmd.Start(); err != nil {
		return commandResult{}, fmt.Errorf("start: %w", err)
	}

	doneCh := make(chan error, 1)
	go func() { doneCh <- cmd.Wait() }()

	select {
	case <-execCtx.Done():
		killProcGroup(cmd)
		<-doneCh
		return commandResult{Stdout: stdout.String(), Stderr: stderr.String()}, execCtx.Err()
	case waitErr := <-doneCh:
		res := commandResult{Stdout: stdout.String(), Stderr: stderr.String()}
		if waitErr != nil {
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				res.ExitCode = exitErr.ExitCode()
				return res, nil
			}
			return res, waitErr
		}
		return res, nil
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./adapter/process/ -run TestRun_`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add adapter/process/runner.go adapter/process/runner_test.go
git commit -m "feat(process): shared command runner (honors TODO refactor)"
```

---

## Task 7: Refactor `ScriptExecutor` onto runner + env; wire main.go

**Files:**
- Modify: `adapter/process/executor_script.go`
- Modify: `adapter/process/executor_script_test.go` (constructor call sites + env assertions)
- Modify: `cmd/arbitron/main.go:228`

- [ ] **Step 1: Update existing test call sites + add env tests** in `executor_script_test.go`.

Change every `process.NewScriptExecutor(fr)` to `process.NewScriptExecutor(fr, nil)`. Then add:

```go
func TestScriptExecutor_PerNodeEnvAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	scriptPath := writeTempScript(t, `#!/bin/sh
echo "{\"model\": \"$LLM_MODEL\"}"
`)
	fr := newMockFileReader()
	fr.paths["test-wf/e.sh"] = scriptPath
	vars := variable.NewStore()
	vars.SetInput("m", "opus")
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile: "e.sh", Interpreter: "/bin/sh",
			Env: map[string]string{"LLM_MODEL": "{{m}}"},
		},
	}
	exec := process.NewScriptExecutor(fr, nil)
	result, err := exec.Execute(context.Background(), node, vars, "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"model": "opus"}, result.Output)
}

func TestScriptExecutor_PassthroughSecretAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	t.Setenv("LLM_API_KEY", "sk-xyz")
	scriptPath := writeTempScript(t, `#!/bin/sh
echo "{\"k\": \"$LLM_API_KEY\"}"
`)
	fr := newMockFileReader()
	fr.paths["test-wf/k.sh"] = scriptPath
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{ScriptFile: "k.sh", Interpreter: "/bin/sh"},
	}
	exec := process.NewScriptExecutor(fr, []string{"LLM_API_KEY"})
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "sk-xyz"}, result.Output)
}

func TestScriptExecutor_EnvCollisionFailsNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	scriptPath := writeTempScript(t, "#!/bin/sh\necho '{}'\n")
	fr := newMockFileReader()
	fr.paths["test-wf/c.sh"] = scriptPath
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{
			ScriptFile: "c.sh", Interpreter: "/bin/sh",
			Env: map[string]string{"PATH": "/evil"},
		},
	}
	exec := process.NewScriptExecutor(fr, nil)
	_, err := exec.Execute(context.Background(), node, variable.NewStore(), "test-wf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")
}

func TestScriptExecutor_SecretNotLeakedWithoutPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	t.Setenv("MY_SECRET", "leak")
	scriptPath := writeTempScript(t, `#!/bin/sh
echo "{\"s\": \"${MY_SECRET:-absent}\"}"
`)
	fr := newMockFileReader()
	fr.paths["test-wf/leak.sh"] = scriptPath
	node := workflow.Node{
		ID: "s1", Type: workflow.NodeTypeScript,
		Config: workflow.NodeConfig{ScriptFile: "leak.sh", Interpreter: "/bin/sh"},
	}
	exec := process.NewScriptExecutor(fr, nil)
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "test-wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"s": "absent"}, result.Output)
}
```

- [ ] **Step 2: Run to verify the new tests fail to compile/pass**

Run: `go test ./adapter/process/ -run TestScriptExecutor`
Expected: FAIL — `NewScriptExecutor` takes one arg (compile error).

- [ ] **Step 3: Refactor `executor_script.go`**

Replace the struct, constructor, and `Execute` body with:

```go
// ScriptExecutor runs external scripts via os/exec, piping the variable store
// as JSON to stdin and parsing stdout as JSON. The subprocess environment is
// the base allowlist + configured passthrough + per-node env (see buildEnv);
// it does NOT inherit the engine's full environment.
type ScriptExecutor struct {
	fileReader  engine.FileReader
	passthrough []string
}

// NewScriptExecutor creates a ScriptExecutor. envPassthrough names the engine
// env vars (e.g. LLM_API_KEY) that script nodes may inherit beyond the base
// allowlist; pass nil for none.
func NewScriptExecutor(fr engine.FileReader, envPassthrough []string) *ScriptExecutor {
	return &ScriptExecutor{fileReader: fr, passthrough: envPassthrough}
}

func (e *ScriptExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, workflowName string) (*engine.NodeResult, error) {
	scriptPath, err := e.fileReader.ResolvePath(workflowName, node.Config.ScriptFile)
	if err != nil {
		return nil, fmt.Errorf("script %q: resolve path: %w", node.ID, err)
	}

	args := make([]string, len(node.Config.Args))
	for i, arg := range node.Config.Args {
		args[i] = variable.Resolve(arg, vars)
	}

	interpreter := node.Config.Interpreter
	if interpreter == "" {
		interpreter = "/bin/sh"
	}

	env, err := buildEnv(e.passthrough, node.Config.Env, vars)
	if err != nil {
		return nil, fmt.Errorf("script %q: %w", node.ID, err)
	}

	stdinData, err := json.Marshal(vars.Snapshot())
	if err != nil {
		return nil, fmt.Errorf("script %q: marshal variables: %w", node.ID, err)
	}

	res, runErr := run(ctx, commandSpec{
		Name:        interpreter,
		Args:        append([]string{scriptPath}, args...),
		Dir:         filepath.Dir(scriptPath),
		Stdin:       stdinData,
		Env:         env,
		TimeoutSecs: node.Config.TimeoutSeconds,
	})

	result := &engine.NodeResult{
		ScriptStdout:   res.Stdout,
		ScriptStderr:   res.Stderr,
		ScriptExitCode: res.ExitCode,
	}
	if runErr != nil {
		return result, fmt.Errorf("script %q: %w", node.ID, runErr)
	}
	if res.ExitCode != 0 {
		return result, fmt.Errorf("script %q: exit status %d: %s", node.ID, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	var output any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &output); err != nil {
		return result, fmt.Errorf("script %q: stdout is not valid JSON: %w", node.ID, err)
	}
	result.Output = output
	return result, nil
}
```

Remove now-unused imports (`bytes`, `os/exec`, `time` move into `runner.go`; the executor keeps `context`, `encoding/json`, `fmt`, `path/filepath`, `strings`, and the domain imports). Let the compiler guide removal. Keep the `SECURITY:` comment block but update it to note the env model now applies; the `TODO(refactor)` block can be deleted since the runner extraction it asked for is now done.

- [ ] **Step 4: Wire `main.go`** — change line 228:

```go
		workflow.NodeTypeScript:      process.NewScriptExecutor(fileReader, cfg.Script.EnvPassthrough),
```

- [ ] **Step 5: Run build + the process + engine suites**

Run: `go build ./... && go test ./adapter/process/ ./domain/engine/...`
Expected: PASS (original 7 script tests + 4 new).

- [ ] **Step 6: Commit**

```bash
git add adapter/process/executor_script.go adapter/process/executor_script_test.go cmd/arbitron/main.go
git commit -m "refactor(process): script executor uses shared runner + env model"
```

---

## Task 8: Refactor `ToolCallExecutor`; wire main.go

**Files:**
- Modify: `adapter/process/executor_toolcall.go`
- Modify: `adapter/process/executor_toolcall_test.go`
- Modify: `cmd/arbitron/main.go:229`

- [ ] **Step 1: Update test call sites + add env tests** in `executor_toolcall_test.go`.

Change every `process.NewToolCallExecutor()` to `process.NewToolCallExecutor(nil)`. Add:

```go
func TestToolCallExecutor_PassthroughSecretAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	t.Setenv("LLM_API_KEY", "sk-tc")
	node := workflow.Node{
		ID: "t1", Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: "/bin/sh",
			Args:    []string{"-c", `echo "{\"k\": \"$LLM_API_KEY\"}"`},
		},
	}
	exec := process.NewToolCallExecutor([]string{"LLM_API_KEY"})
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"k": "sk-tc"}, result.Output)
}

func TestToolCallExecutor_SecretNotLeaked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("not supported on Windows")
	}
	t.Setenv("MY_SECRET", "leak")
	node := workflow.Node{
		ID: "t1", Type: workflow.NodeTypeToolCall,
		Config: workflow.NodeConfig{
			Command: "/bin/sh",
			Args:    []string{"-c", `echo "{\"s\": \"${MY_SECRET:-absent}\"}"`},
		},
	}
	exec := process.NewToolCallExecutor(nil)
	result, err := exec.Execute(context.Background(), node, variable.NewStore(), "wf")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"s": "absent"}, result.Output)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./adapter/process/ -run TestToolCallExecutor`
Expected: FAIL — `NewToolCallExecutor` takes no args (compile error).

- [ ] **Step 3: Refactor `executor_toolcall.go`**

```go
// ToolCallExecutor runs an external command with arguments. Unlike
// ScriptExecutor it does NOT pipe variables to stdin. The subprocess
// environment is the base allowlist + configured passthrough + per-node env.
type ToolCallExecutor struct {
	passthrough []string
}

// NewToolCallExecutor creates a ToolCallExecutor. envPassthrough names the
// engine env vars a tool_call node may inherit beyond the base allowlist.
func NewToolCallExecutor(envPassthrough []string) *ToolCallExecutor {
	return &ToolCallExecutor{passthrough: envPassthrough}
}

func (e *ToolCallExecutor) Execute(ctx context.Context, node workflow.Node, vars *variable.VariableStore, _ string) (*engine.NodeResult, error) {
	args := make([]string, len(node.Config.Args))
	for i, arg := range node.Config.Args {
		args[i] = variable.Resolve(arg, vars)
	}

	env, err := buildEnv(e.passthrough, node.Config.Env, vars)
	if err != nil {
		return nil, fmt.Errorf("tool_call %q: %w", node.ID, err)
	}

	res, runErr := run(ctx, commandSpec{
		Name:        node.Config.Command,
		Args:        args,
		Env:         env,
		TimeoutSecs: node.Config.TimeoutSeconds,
	})

	result := &engine.NodeResult{
		ScriptStdout:   res.Stdout,
		ScriptStderr:   res.Stderr,
		ScriptExitCode: res.ExitCode,
	}
	if runErr != nil {
		return result, fmt.Errorf("tool_call %q: %w", node.ID, runErr)
	}
	if res.ExitCode != 0 {
		return result, fmt.Errorf("tool_call %q: exit status %d: %s", node.ID, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	var output any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &output); err != nil {
		return result, fmt.Errorf("tool_call %q: stdout is not valid JSON: %w", node.ID, err)
	}
	result.Output = output
	return result, nil
}
```

Remove now-unused imports (`bytes`, `os/exec`, `time`). Delete the `TODO(refactor)` block (done). Keep/refresh the `SECURITY:` note.

- [ ] **Step 4: Wire `main.go`** — change line 229:

```go
		workflow.NodeTypeToolCall:    process.NewToolCallExecutor(cfg.Script.EnvPassthrough),
```

- [ ] **Step 5: Run build + process suite**

Run: `go build ./... && go test ./adapter/process/...`
Expected: PASS (original 8 tool tests + 2 new).

- [ ] **Step 6: Commit**

```bash
git add adapter/process/executor_toolcall.go adapter/process/executor_toolcall_test.go cmd/arbitron/main.go
git commit -m "refactor(process): tool_call executor uses shared runner + env model"
```

---

## Task 9: Full verification

- [ ] **Step 1: Build, vet, full test suite**

Run: `go build ./... && go vet ./... && go test -count=1 ./...`
Expected: PASS across the repo. The leak-prevention assertions (`*_SecretNotLeaked*`) and the 15 pre-existing process tests all green.

- [ ] **Step 2: Confirm no `os.Environ()` inheritance remains in process nodes**

Run: `grep -rn "os.Environ\|cmd.Env" adapter/process/`
Expected: `cmd.Env = spec.Env` only inside `runner.go`; no implicit full-env inheritance.

- [ ] **Step 3: Final commit (if vet/format produced changes)**

```bash
git add -A && git commit -m "chore: gofmt/vet for script env hardening" || echo "nothing to commit"
```

---

## Self-Review (completed by plan author)

- **Spec coverage:** procenv extraction (T1–2), per-node env (T3), config passthrough (T4), three-layer buildEnv (T5), CommandRunner extraction (T6), both executors refactored + hardened (T7–8), tests across all 5 categories (T1,5,6,7,8), verification (T9). The spec's "validation error" for collisions is implemented as a runtime `buildEnv` error — documented at the top as the one deliberate refinement.
- **Type consistency:** `buildEnv(passthrough []string, nodeEnv map[string]string, vars *variable.VariableStore) ([]string, error)`, `commandSpec`/`commandResult`, `NewScriptExecutor(fr, []string)`, `NewToolCallExecutor([]string)`, `cfg.Script.EnvPassthrough []string` used consistently in every task and in main.go.
- **No placeholders:** every code/test step contains complete code and exact run commands.
