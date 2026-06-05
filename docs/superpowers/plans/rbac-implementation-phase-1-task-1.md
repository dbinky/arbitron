# RBAC Phase 1 — Task 1: Group Resolution Domain Helper Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a pure domain helper that turns verified group claims + RBAC config into the effective department / lead-department projection (normalize, de-duplicate, default-group fallback, lead-derived membership).

**Architecture:** A new pure-domain file `domain/identity/groups.go` with no infra imports. It introduces a `GroupConfig` value type (carried by `domain/identity`, not `domain/config`, to avoid an import cycle — see Note below) and four pure functions. Later tasks (config wiring, provisioner, middleware) consume these.

**Tech Stack:** Go, standard library only (`strings`). Tests use the standard `testing` package (table-driven), matching the existing `domain/identity` test style.

> **NOTE on the `GroupConfig` home (read before starting).** The design doc says `GroupConfig` lives in `domain/config`. However `domain/config` must stay import-clean and `domain/identity` is the dependency floor that everything authz-related consumes. To keep the group-resolution helper pure AND avoid a config→identity or identity→config cycle, this plan defines the **resolution-time** `GroupConfig` value type in `domain/identity` (Task 1) and a **load-time** `RBACConfig` struct in `domain/config` (Task 2) that the wiring maps across (exactly like `oidc.Config` mirrors `config.OIDCConfig`). This mirrors the established pattern where `domain/identity.ModeFromString` takes a plain string rather than importing `config`. Do not add a `domain/config` import to `domain/identity`.

---

### Task 1: Group resolution helper (`domain/identity/groups.go`)

**Files:**
- Create: `domain/identity/groups.go`
- Test: `domain/identity/groups_test.go`

**Dependencies:** None. This is the first task of Phase 1 and has no dependency on other tasks.

- [ ] **Step 1: Write the failing test for `NormalizeDept`**

Create `domain/identity/groups_test.go`:

```go
package identity

import (
	"reflect"
	"testing"
)

func TestNormalizeDept(t *testing.T) {
	cases := map[string]string{
		"Engineering":    "engineering",
		"  engineering ": "engineering",
		"ENGINEERING":    "engineering",
		"":               "",
		"   ":            "",
		"Data Science":   "data science",
	}
	for in, want := range cases {
		if got := NormalizeDept(in); got != want {
			t.Errorf("NormalizeDept(%q) = %q, want %q", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./domain/identity/ -run TestNormalizeDept -v`
Expected: FAIL — `undefined: NormalizeDept`.

- [ ] **Step 3: Write the minimal implementation**

Create `domain/identity/groups.go`:

```go
package identity

import "strings"

// GroupConfig is the resolution-time RBAC configuration the group-resolution
// helpers consume. It is defined HERE (the dependency floor) rather than in
// domain/config so the helpers stay pure and no config<->identity import cycle
// forms; adapter wiring maps config.RBACConfig (Task 2) into this shape, exactly
// as adapter/oidc maps config.OIDCConfig into oidc.Config.
type GroupConfig struct {
	// GroupClaim is the token claim name carrying group membership. "" means no
	// claim is read and every user falls back to the default group.
	GroupClaim string
	// DefaultGroup is the sentinel department for claimless users. Callers MUST
	// pass a non-empty, already-normalized value (the loader defaults it to
	// "default"); DefaultGroupOr applies the "default" fallback defensively.
	DefaultGroup string
	// Leads maps a normalized department name to the lead identifiers (subjects
	// and/or emails, lower-cased) that lead it.
	Leads map[string][]string
}

// defaultGroupSentinel is the hard fallback when GroupConfig.DefaultGroup is
// empty, so resolution never yields an empty department set.
const defaultGroupSentinel = "default"

// NormalizeDept canonicalizes a raw claim/department value: trimmed and
// lower-cased. An all-whitespace input normalizes to "".
func NormalizeDept(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// DefaultGroupOr returns cfg.DefaultGroup normalized, or the "default" sentinel
// when it is empty/blank.
func (cfg GroupConfig) DefaultGroupOr() string {
	if d := NormalizeDept(cfg.DefaultGroup); d != "" {
		return d
	}
	return defaultGroupSentinel
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./domain/identity/ -run TestNormalizeDept -v`
Expected: PASS.

- [ ] **Step 5: Write the failing test for `ResolveDepartments`**

Append to `domain/identity/groups_test.go`:

```go
func TestResolveDepartments(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default"}

	t.Run("mixed case and whitespace collapse to one department", func(t *testing.T) {
		got := ResolveDepartments([]string{"Engineering", " engineering ", "ENGINEERING"}, cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("empty claims fall back to default group", func(t *testing.T) {
		got := ResolveDepartments(nil, cfg)
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default]", got)
		}
	})

	t.Run("all-blank claims fall back to default group", func(t *testing.T) {
		got := ResolveDepartments([]string{"", "   "}, cfg)
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default]", got)
		}
	})

	t.Run("empty DefaultGroup uses the default sentinel", func(t *testing.T) {
		got := ResolveDepartments(nil, GroupConfig{})
		if !reflect.DeepEqual(got, []string{"default"}) {
			t.Errorf("got %v, want [default]", got)
		}
	})

	t.Run("duplicates de-duplicated, order preserved", func(t *testing.T) {
		got := ResolveDepartments([]string{"Sales", "eng", "sales", "ENG"}, cfg)
		if !reflect.DeepEqual(got, []string{"sales", "eng"}) {
			t.Errorf("got %v, want [sales eng]", got)
		}
	})
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./domain/identity/ -run TestResolveDepartments -v`
Expected: FAIL — `undefined: ResolveDepartments`.

- [ ] **Step 7: Implement `ResolveDepartments`**

Append to `domain/identity/groups.go`:

```go
// ResolveDepartments normalizes the raw claim values, drops blanks, and
// de-duplicates while preserving first-seen order. If the result is empty (no
// claim configured, claim absent, or all values blank), it returns the single
// default-group sentinel so a caller is never in zero departments.
func ResolveDepartments(claimValues []string, cfg GroupConfig) []string {
	seen := make(map[string]struct{}, len(claimValues))
	out := make([]string, 0, len(claimValues))
	for _, v := range claimValues {
		d := NormalizeDept(v)
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	if len(out) == 0 {
		return []string{cfg.DefaultGroupOr()}
	}
	return out
}
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./domain/identity/ -run TestResolveDepartments -v`
Expected: PASS.

- [ ] **Step 9: Write the failing test for `LedDepartments`**

Append to `domain/identity/groups_test.go`:

```go
func TestLedDepartments(t *testing.T) {
	cfg := GroupConfig{
		DefaultGroup: "default",
		Leads: map[string][]string{
			"engineering": {"alice@corp.example", "bob-subject-id"},
			"sales":       {"carol@corp.example"},
		},
	}

	t.Run("match by subject", func(t *testing.T) {
		got := LedDepartments("bob-subject-id", "", cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("match by email case-insensitively", func(t *testing.T) {
		got := LedDepartments("some-subject", "ALICE@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("no match yields empty", func(t *testing.T) {
		got := LedDepartments("nobody", "nobody@corp.example", cfg)
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})

	t.Run("nil Leads map yields empty", func(t *testing.T) {
		got := LedDepartments("alice@corp.example", "alice@corp.example", GroupConfig{})
		if len(got) != 0 {
			t.Errorf("got %v, want empty", got)
		}
	})
}
```

- [ ] **Step 10: Run the test to verify it fails**

Run: `go test ./domain/identity/ -run TestLedDepartments -v`
Expected: FAIL — `undefined: LedDepartments`.

- [ ] **Step 11: Implement `LedDepartments`**

Append to `domain/identity/groups.go`:

```go
// LedDepartments returns the normalized departments this identity is a
// configured lead of, matched case-insensitively against the subject OR the
// email (mirroring the bootstrap-admin match rule in
// instance.Provisioner.IsBootstrapAdmin). Result order follows sorted department
// keys for determinism. An empty/blank subject and email yields no matches.
func LedDepartments(subject, email string, cfg GroupConfig) []string {
	if len(cfg.Leads) == 0 {
		return nil
	}
	subj := strings.ToLower(strings.TrimSpace(subject))
	em := strings.ToLower(strings.TrimSpace(email))
	if subj == "" && em == "" {
		return nil
	}
	// Collect matching departments, then sort for deterministic output.
	matched := make([]string, 0, len(cfg.Leads))
	for dept, ids := range cfg.Leads {
		for _, id := range ids {
			lid := strings.ToLower(strings.TrimSpace(id))
			if lid == "" {
				continue
			}
			if (subj != "" && lid == subj) || (em != "" && lid == em) {
				matched = append(matched, NormalizeDept(dept))
				break
			}
		}
	}
	sortStrings(matched)
	return matched
}

// sortStrings is an in-package insertion sort kept here so groups.go stays
// import-light (no sort import alongside strings); the lists are tiny (number of
// led departments), so the simple sort is fine.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
```

- [ ] **Step 12: Run the test to verify it passes**

Run: `go test ./domain/identity/ -run TestLedDepartments -v`
Expected: PASS.

- [ ] **Step 13: Write the failing test for `EffectiveMembership` (claim-derived ∪ led)**

Append to `domain/identity/groups_test.go`:

```go
func TestEffectiveMembership(t *testing.T) {
	cfg := GroupConfig{
		DefaultGroup: "default",
		Leads:        map[string][]string{"platform": {"alice@corp.example"}},
	}

	t.Run("lead of D sees D even when token omits it", func(t *testing.T) {
		// alice's token only carries engineering, but she leads platform.
		got := EffectiveMembership([]string{"engineering"}, "alice-subj", "alice@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"engineering", "platform"}) {
			t.Errorf("got %v, want [engineering platform]", got)
		}
	})

	t.Run("non-lead gets only claim departments", func(t *testing.T) {
		got := EffectiveMembership([]string{"engineering"}, "bob-subj", "bob@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"engineering"}) {
			t.Errorf("got %v, want [engineering]", got)
		}
	})

	t.Run("claimless lead gets default group plus led department", func(t *testing.T) {
		got := EffectiveMembership(nil, "alice-subj", "alice@corp.example", cfg)
		if !reflect.DeepEqual(got, []string{"default", "platform"}) {
			t.Errorf("got %v, want [default platform]", got)
		}
	})
}
```

- [ ] **Step 14: Run the test to verify it fails**

Run: `go test ./domain/identity/ -run TestEffectiveMembership -v`
Expected: FAIL — `undefined: EffectiveMembership`.

- [ ] **Step 15: Implement `EffectiveMembership`**

Append to `domain/identity/groups.go`:

```go
// EffectiveMembership is the caller's full department set: the claim-derived
// departments (ResolveDepartments) unioned with the departments they lead
// (LedDepartments). A configured lead of a department therefore always sees that
// department even if their token omits it. Order: claim/default departments
// first (first-seen), then any newly-added led departments (sorted).
func EffectiveMembership(claimValues []string, subject, email string, cfg GroupConfig) []string {
	out := ResolveDepartments(claimValues, cfg)
	seen := make(map[string]struct{}, len(out))
	for _, d := range out {
		seen[d] = struct{}{}
	}
	for _, d := range LedDepartments(subject, email, cfg) {
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out
}
```

- [ ] **Step 16: Run the full identity package test to verify everything passes**

Run: `go test ./domain/identity/ -v`
Expected: PASS (all `groups_test.go` tests plus existing identity tests).

- [ ] **Step 17: Vet the package**

Run: `go vet ./domain/identity/`
Expected: no output (clean).

- [ ] **Step 18: Commit**

```bash
git add domain/identity/groups.go domain/identity/groups_test.go
git commit -m "feat(rbac): group-resolution domain helper (normalize, default-group, leads)

Pure domain/identity helpers: NormalizeDept, ResolveDepartments (default-group
fallback + de-dup), LedDepartments (subject/email match), EffectiveMembership
(claim ∪ led). GroupConfig value type lives here to keep config import-clean.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Test Scenario Coverage (this task)

| Category | Scenario | Step |
|----------|----------|------|
| Happy | Single normalized department from mixed-case claims | 5 |
| Happy | Lead-derived department unioned into membership | 13 |
| Edge | Whitespace/case collapse to one department | 1, 5 |
| Edge | Duplicate claim values de-duplicated, order preserved | 5 |
| Edge | All-blank values dropped | 5 |
| Edge | Array claim with empty `DefaultGroup` uses sentinel | 5 |
| Error/fallback | Empty/nil claims → default group | 5 |
| Authz | Lead match by subject and by email (case-insensitive) | 9 |
| Authz | Non-lead gets no led departments | 9, 13 |
| Mode | (Local-mode no-op verified in Task 5; helper is mode-agnostic) | — |
