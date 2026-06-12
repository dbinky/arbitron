# Profile-Editing Phase 1 — Capability Ruleset — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `ContextWindow` and `Effort` profile axes as domain enums plus a `CapabilityRules` engine that validates a requested (tier, effort) against a backend/model and reports the effective (floored) values — loaded from an embedded default YAML with optional file override.

**Architecture:** Pure domain types + logic in `domain/provider`; a startup loader in `adapter/config`. No DB, MCP, or runtime wiring. Everything is unit-testable in isolation. Follows the existing `domain/provider` package conventions (see `connection.go`, `provider.go`).

**Tech Stack:** Go, `//go:embed`, koanf/YAML (the repo already uses koanf — see `adapter/config/loader.go`), testify.

**Design ref:** `docs/superpowers/specs/profile-editing-design-phase-1.md`

---

### Task 1: ContextWindow + Effort enums

**Files:**
- Create: `domain/provider/axes.go`
- Test: `domain/provider/axes_test.go`

- [ ] **Step 1: Write failing tests**

```go
package provider

import "testing"

func TestContextWindow_Valid(t *testing.T) {
	for _, v := range []ContextWindow{ContextDefault, Context200K, Context1M} {
		if !v.Valid() {
			t.Fatalf("expected %q valid", v)
		}
	}
	if ContextWindow("4m").Valid() {
		t.Fatal("expected 4m invalid")
	}
}

func TestParseContextWindow(t *testing.T) {
	got, err := ParseContextWindow("200k")
	if err != nil || got != Context200K {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := ParseContextWindow("nope"); err == nil {
		t.Fatal("want ErrInvalidContextWindow")
	}
	// empty string normalizes to default (unset)
	if got, _ := ParseContextWindow(""); got != ContextDefault {
		t.Fatalf("empty should be default, got %q", got)
	}
}

func TestEffort_OrderAndParse(t *testing.T) {
	if !EffortHigh.GreaterThan(EffortMedium) {
		t.Fatal("high > medium")
	}
	if EffortDefault.GreaterThan(EffortLow) {
		t.Fatal("default is unordered, never greater")
	}
	if _, err := ParseEffort("ultra"); err == nil {
		t.Fatal("want ErrInvalidEffort")
	}
}
```

- [ ] **Step 2: Run, verify fail** — `go test ./domain/provider/ -run 'ContextWindow|Effort' -v` → FAIL (undefined).

- [ ] **Step 3: Implement**

```go
package provider

import "errors"

type ContextWindow string

const (
	ContextDefault ContextWindow = "default"
	Context200K    ContextWindow = "200k"
	Context1M      ContextWindow = "1m"
)

var ErrInvalidContextWindow = errors.New("provider: invalid context window")

func (c ContextWindow) Valid() bool {
	switch c {
	case ContextDefault, Context200K, Context1M:
		return true
	}
	return false
}

func ParseContextWindow(s string) (ContextWindow, error) {
	if s == "" {
		return ContextDefault, nil
	}
	c := ContextWindow(s)
	if !c.Valid() {
		return "", ErrInvalidContextWindow
	}
	return c, nil
}

type Effort string

const (
	EffortDefault Effort = "default"
	EffortNone    Effort = "none"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

var ErrInvalidEffort = errors.New("provider: invalid effort")

// effortRank orders the *graded* levels; default/none are unranked (0).
var effortRank = map[Effort]int{
	EffortNone: 1, EffortLow: 2, EffortMedium: 3, EffortHigh: 4, EffortXHigh: 5, EffortMax: 6,
}

func (e Effort) Valid() bool {
	switch e {
	case EffortDefault, EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
		return true
	}
	return false
}

// GreaterThan compares graded levels; default is never greater than anything.
func (e Effort) GreaterThan(o Effort) bool {
	if e == EffortDefault || o == EffortDefault {
		return false
	}
	return effortRank[e] > effortRank[o]
}

func ParseEffort(s string) (Effort, error) {
	if s == "" {
		return EffortDefault, nil
	}
	e := Effort(s)
	if !e.Valid() {
		return "", ErrInvalidEffort
	}
	return e, nil
}
```

- [ ] **Step 4: Run, verify pass** — `go test ./domain/provider/ -run 'ContextWindow|Effort' -v` → PASS.

- [ ] **Step 5: Commit** — `git add domain/provider/axes.go domain/provider/axes_test.go && git commit -m "feat(provider): ContextWindow and Effort axis enums"`

---

### Task 2: CapabilityRules.Validate

**Files:**
- Create: `domain/provider/capability.go`
- Test: `domain/provider/capability_test.go`

- [ ] **Step 1: Write failing tests** (happy / floor / inapplicable / invalid / unknown)

```go
package provider

import "testing"

func rules() CapabilityRules {
	return NewCapabilityRules(map[string]BackendCapability{
		"claude-code": {
			Backend:      "claude-code",
			ContextTiers: []ContextWindow{ContextDefault, Context200K, Context1M},
			EffortLevels: []Effort{EffortDefault, EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax},
			ModelOverrides: map[string]ModelCapability{
				"sonnet-4.6": {
					EffortLevels: []Effort{EffortDefault, EffortNone, EffortLow, EffortMedium, EffortHigh, EffortMax},
					EffortFloor:  map[Effort]Effort{EffortXHigh: EffortHigh},
				},
			},
		},
		"codex": {
			Backend:      "codex",
			ContextTiers: []ContextWindow{ContextDefault},
			EffortLevels: []Effort{EffortDefault, EffortNone, EffortLow, EffortMedium, EffortHigh, EffortXHigh},
		},
	})
}

func TestValidate_Happy(t *testing.T) {
	tier, eff, err := rules().Validate("claude-code", "opus-4.8", Context1M, EffortHigh)
	if err != nil || tier != Context1M || eff != EffortHigh {
		t.Fatalf("got %q %q %v", tier, eff, err)
	}
}

func TestValidate_FloorEffort(t *testing.T) {
	_, eff, err := rules().Validate("claude-code", "sonnet-4.6", ContextDefault, EffortXHigh)
	if err != nil || eff != EffortHigh {
		t.Fatalf("expected floor to high, got %q %v", eff, err)
	}
}

func TestValidate_InapplicableContext(t *testing.T) {
	_, _, err := rules().Validate("codex", "gpt-5.5", Context200K, EffortMedium)
	if !errorsIs(err, ErrAxisNotApplicable) {
		t.Fatalf("want ErrAxisNotApplicable, got %v", err)
	}
}

func TestValidate_UnknownBackend(t *testing.T) {
	_, _, err := rules().Validate("mystery", "x", ContextDefault, EffortDefault)
	if !errorsIs(err, ErrUnknownBackend) {
		t.Fatalf("want ErrUnknownBackend, got %v", err)
	}
}
```
(Use `errors.Is` directly; `errorsIs` is shorthand — replace with `errors.Is` and import `errors`.)

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Implement** `capability.go` per design §"New domain types":

```go
package provider

import (
	"errors"
	"fmt"
)

var (
	ErrAxisNotApplicable = errors.New("provider: axis not applicable to backend")
	ErrUnknownBackend    = errors.New("provider: unknown backend in capability rules")
)

type ModelCapability struct {
	ContextTiers []ContextWindow
	EffortLevels []Effort
	EffortFloor  map[Effort]Effort
}

type BackendCapability struct {
	Backend        string
	ContextTiers   []ContextWindow
	EffortLevels   []Effort
	ModelOverrides map[string]ModelCapability
}

type CapabilityRules struct {
	byBackend map[string]BackendCapability
}

func NewCapabilityRules(m map[string]BackendCapability) CapabilityRules {
	return CapabilityRules{byBackend: m}
}

func containsCtx(s []ContextWindow, v ContextWindow) bool {
	for _, x := range s { if x == v { return true } }
	return false
}
func containsEff(s []Effort, v Effort) bool {
	for _, x := range s { if x == v { return true } }
	return false
}

// effective tiers/effort for a backend+model (model override wins when present).
func (r CapabilityRules) caps(backend, model string) (tiers []ContextWindow, levels []Effort, floor map[Effort]Effort, ok bool) {
	bc, ok := r.byBackend[backend]
	if !ok {
		return nil, nil, nil, false
	}
	tiers, levels = bc.ContextTiers, bc.EffortLevels
	if mc, has := bc.ModelOverrides[model]; has {
		if mc.ContextTiers != nil { tiers = mc.ContextTiers }
		if mc.EffortLevels != nil { levels = mc.EffortLevels }
		floor = mc.EffortFloor
	}
	return tiers, levels, floor, true
}

// Validate returns the effective (possibly floored) tier+effort, or an error.
func (r CapabilityRules) Validate(backend, model string, tier ContextWindow, effort Effort) (ContextWindow, Effort, error) {
	if !tier.Valid() { return "", "", ErrInvalidContextWindow }
	if !effort.Valid() { return "", "", ErrInvalidEffort }
	tiers, levels, floor, ok := r.caps(backend, model)
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrUnknownBackend, backend)
	}
	// Context: default always ok; non-default must be allowed.
	if tier != ContextDefault && !containsCtx(tiers, tier) {
		return "", "", fmt.Errorf("%w: context %q on backend %q", ErrAxisNotApplicable, tier, backend)
	}
	// Effort: default/none always pass through; graded must be allowed or floored.
	effEffort := effort
	if effort != EffortDefault && !containsEff(levels, effort) {
		if f, has := floor[effort]; has {
			effEffort = f
		} else {
			effEffort = floorToHighest(levels, effort)
			if effEffort == "" {
				return "", "", fmt.Errorf("%w: effort %q on backend %q", ErrAxisNotApplicable, effort, backend)
			}
		}
	}
	return tier, effEffort, nil
}

// floorToHighest returns the highest allowed graded level <= want, or "" if none.
func floorToHighest(levels []Effort, want Effort) Effort {
	best := Effort("")
	for _, l := range levels {
		if l == EffortDefault || l == EffortNone { continue }
		if !l.GreaterThan(want) && (best == "" || l.GreaterThan(best)) {
			best = l
		}
	}
	return best
}
```

- [ ] **Step 4: Run, verify pass.** Add a floor-to-highest test (xhigh on a backend allowing only up to high with no explicit floor map → high).

- [ ] **Step 5: Commit** — `git commit -m "feat(provider): CapabilityRules.Validate with effort floor"`

---

### Task 3: Embedded default ruleset + file loader

**Files:**
- Create: `adapter/config/capabilities.default.yaml`
- Create: `adapter/config/capabilities.go`
- Test: `adapter/config/capabilities_test.go`

- [ ] **Step 1: Write failing tests**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arbitron/arbitron/domain/provider"
)

func TestLoadCapabilityRules_EmbeddedDefault(t *testing.T) {
	r, err := LoadCapabilityRules("") // no override path
	if err != nil { t.Fatal(err) }
	_, eff, err := r.Validate("claude-code", "sonnet-4.6", provider.Context200K, provider.EffortXHigh)
	if err != nil || eff != provider.EffortHigh {
		t.Fatalf("embedded sonnet floor failed: %q %v", eff, err)
	}
}

func TestLoadCapabilityRules_OverrideMerges(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "capabilities.yaml")
	os.WriteFile(p, []byte("backends:\n  codex:\n    context_tiers: [default]\n    effort_levels: [default, high]\n"), 0o600)
	r, err := LoadCapabilityRules(p)
	if err != nil { t.Fatal(err) }
	// override narrows codex effort to {default, high}: medium floors to... none allowed below high → high? No: floorToHighest(<=medium) over {high} → "" → inapplicable.
	if _, _, err := r.Validate("codex", "gpt-5.5", provider.ContextDefault, provider.EffortMedium); err == nil {
		t.Fatal("expected medium rejected under narrowed codex rules")
	}
}

func TestLoadCapabilityRules_MalformedFails(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yaml")
	os.WriteFile(p, []byte("backends:\n  codex:\n    effort_levels: [bogus]\n"), 0o600)
	if _, err := LoadCapabilityRules(p); err == nil {
		t.Fatal("expected malformed enum to fail fast")
	}
}
```

- [ ] **Step 2: Run, verify fail.**

- [ ] **Step 3: Author the embedded default YAML** (content from design §"Embedded default content"):

```yaml
backends:
  claude-code:
    context_tiers: [default, 200k, 1m]
    effort_levels: [default, none, low, medium, high, xhigh, max]
    model_overrides:
      sonnet-4.6:
        effort_levels: [default, none, low, medium, high, max]
        effort_floor: { xhigh: high }
  claude-cli:
    context_tiers: [default, 200k, 1m]
    effort_levels: [default, none, low, medium, high, xhigh, max]
  codex:
    context_tiers: [default]
    effort_levels: [default, none, low, medium, high, xhigh]
  opencode:
    context_tiers: [default]
    effort_levels: [default]
```

- [ ] **Step 4: Implement loader** `capabilities.go`:

```go
package config

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/arbitron/arbitron/domain/provider"
	"gopkg.in/yaml.v3"
)

//go:embed capabilities.default.yaml
var defaultCapabilitiesYAML []byte

type capFile struct {
	Backends map[string]struct {
		ContextTiers   []string `yaml:"context_tiers"`
		EffortLevels   []string `yaml:"effort_levels"`
		ModelOverrides map[string]struct {
			ContextTiers []string          `yaml:"context_tiers"`
			EffortLevels []string          `yaml:"effort_levels"`
			EffortFloor  map[string]string `yaml:"effort_floor"`
		} `yaml:"model_overrides"`
	} `yaml:"backends"`
}

// LoadCapabilityRules parses the embedded default, then overlays an optional
// override file (per-backend replace). Loaded ONCE at startup.
func LoadCapabilityRules(overridePath string) (provider.CapabilityRules, error) {
	base, err := parseCapFile(defaultCapabilitiesYAML)
	if err != nil {
		return provider.CapabilityRules{}, fmt.Errorf("embedded capabilities: %w", err)
	}
	if overridePath != "" {
		if raw, err := os.ReadFile(overridePath); err == nil {
			ov, err := parseCapFile(raw)
			if err != nil {
				return provider.CapabilityRules{}, fmt.Errorf("override capabilities %q: %w", overridePath, err)
			}
			for k, v := range ov { base[k] = v } // per-backend replace
		} else if !os.IsNotExist(err) {
			return provider.CapabilityRules{}, fmt.Errorf("reading %q: %w", overridePath, err)
		}
	}
	return provider.NewCapabilityRules(base), nil
}

func parseCapFile(raw []byte) (map[string]provider.BackendCapability, error) {
	var f capFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	out := make(map[string]provider.BackendCapability, len(f.Backends))
	for name, b := range f.Backends {
		bc := provider.BackendCapability{Backend: name}
		var err error
		if bc.ContextTiers, err = parseTiers(b.ContextTiers); err != nil { return nil, fmt.Errorf("%s: %w", name, err) }
		if bc.EffortLevels, err = parseEfforts(b.EffortLevels); err != nil { return nil, fmt.Errorf("%s: %w", name, err) }
		if len(b.ModelOverrides) > 0 {
			bc.ModelOverrides = map[string]provider.ModelCapability{}
			for m, mo := range b.ModelOverrides {
				mc := provider.ModelCapability{}
				if mc.ContextTiers, err = parseTiers(mo.ContextTiers); err != nil { return nil, fmt.Errorf("%s/%s: %w", name, m, err) }
				if mc.EffortLevels, err = parseEfforts(mo.EffortLevels); err != nil { return nil, fmt.Errorf("%s/%s: %w", name, m, err) }
				if len(mo.EffortFloor) > 0 {
					mc.EffortFloor = map[provider.Effort]provider.Effort{}
					for from, to := range mo.EffortFloor {
						fe, err := provider.ParseEffort(from); if err != nil { return nil, err }
						te, err := provider.ParseEffort(to); if err != nil { return nil, err }
						mc.EffortFloor[fe] = te
					}
				}
				bc.ModelOverrides[m] = mc
			}
		}
		out[name] = bc
	}
	return out, nil
}

func parseTiers(ss []string) ([]provider.ContextWindow, error) {
	out := make([]provider.ContextWindow, 0, len(ss))
	for _, s := range ss { v, err := provider.ParseContextWindow(s); if err != nil { return nil, err }; out = append(out, v) }
	return out, nil
}
func parseEfforts(ss []string) ([]provider.Effort, error) {
	out := make([]provider.Effort, 0, len(ss))
	for _, s := range ss { v, err := provider.ParseEffort(s); if err != nil { return nil, err }; out = append(out, v) }
	return out, nil
}
```
Note: confirm `gopkg.in/yaml.v3` is already a dependency (koanf uses it transitively; if not, `go get gopkg.in/yaml.v3`). The repo's `adapter/config/loader.go` already parses YAML — reuse its yaml import path.

- [ ] **Step 5: Run, verify pass; then `go test ./domain/provider/... ./adapter/config/... && go vet ./...`**

- [ ] **Step 6: Commit** — `git commit -m "feat(config): embedded + override capability ruleset loader"`

---

## Self-Review (Phase 1)
- **Spec coverage:** §4.1/§4.2 (axes) → Task 1; §5 ruleset + write-time validity → Task 2; §5 "configurable file, startup-loaded, embedded default" → Task 3. ✓
- **Placeholder scan:** none.
- **Type consistency:** `ContextWindow`/`Effort`/`CapabilityRules`/`BackendCapability`/`ModelCapability`/`Validate` names match design Phase 1 and are consumed unchanged in Phases 2-4. ✓
