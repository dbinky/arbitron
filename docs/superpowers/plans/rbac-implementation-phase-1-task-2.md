# RBAC Phase 1 — Task 2: RBAC Config Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the `auth.rbac.*` configuration (group claim name, default group, department→leads mapping) to the domain config type and the koanf loader, with YAML + env-override support, and a mapping into the `identity.GroupConfig` resolution shape.

**Architecture:** A new `RBACConfig` struct on `domain/config.AuthConfig` (load-time shape, import-clean). New koanf default keys + env overlays in `adapter/config/loader.go`. A small mapping helper `ToGroupConfig()` that converts the load-time `RBACConfig` into the resolution-time `identity.GroupConfig` (defined in Task 1), normalizing lead identifiers and department keys. The Leads map is normalized at load.

**Tech Stack:** Go, koanf (confmap/yaml/env providers), standard library `strings`.

> Read Task 1's "NOTE on the `GroupConfig` home" before starting. `domain/config` stays import-clean (no `domain/identity` import); the load→resolution mapping lives in `adapter/config` (which already imports both `domain/config` and may import `domain/identity`).

---

### Task 2: RBAC config type, loader keys, env overlay, and mapping

**Files:**
- Modify: `domain/config/config.go` (add `RBACConfig`, add `RBAC` field to `AuthConfig`)
- Modify: `adapter/config/loader.go` (defaults map, YAML comment block, env overlay, leads normalization)
- Create: `adapter/config/rbac.go` (the `ToGroupConfig` mapping + leads normalization helper)
- Test: `adapter/config/rbac_test.go`
- Test: `adapter/config/loader_rbac_test.go`

**Dependencies:** Task 1 (`identity.GroupConfig`, `identity.NormalizeDept`). Must be done after Task 1.

- [ ] **Step 1: Add the `RBACConfig` type to the domain config**

In `domain/config/config.go`, add the `RBAC` field to `AuthConfig` (insert after the `BootstrapAdmins` field, before `OIDC`):

```go
	// RBAC holds the role-based-access-control settings (auth mode only). With
	// an empty GroupClaim and no Leads, every authenticated user is a member of
	// the single DefaultGroup — the deployment behaves as one shared workspace
	// with administrators. CONSUMED by the group-resolution helper and the
	// provisioner/middleware (RBAC Phase 1).
	RBAC RBACConfig
```

Then add the new type after `AuthConfig` (before `OIDCConfig`):

```go
// RBACConfig configures department-based RBAC (auth mode only). It is the
// load-time shape; adapter/config maps it into the resolution-time
// identity.GroupConfig (kept separate so domain/config imports nothing from
// domain/identity).
type RBACConfig struct {
	// GroupClaim is the token claim name carrying a user's group membership.
	// Empty ("") means no claim is read and every user falls back to the default
	// group only.
	GroupClaim string
	// DefaultGroup is the sentinel department for claimless users. Defaults to
	// "default".
	DefaultGroup string
	// Leads maps a department name to the identifiers (subjects and/or emails) of
	// its lead users. Department keys and identifiers are normalized to lower-case
	// at load. A lead of a department always sees and can publish-to-public that
	// department even if their token omits it.
	Leads map[string][]string
}
```

- [ ] **Step 2: Run the config package build to verify it compiles**

Run: `go build ./domain/config/`
Expected: success (no output).

- [ ] **Step 3: Write the failing test for `ToGroupConfig` mapping + normalization**

Create `adapter/config/rbac_test.go`:

```go
package config

import (
	"reflect"
	"testing"

	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
)

func TestToGroupConfig(t *testing.T) {
	in := domainconfig.RBACConfig{
		GroupClaim:   "groups",
		DefaultGroup: "Default",
		Leads: map[string][]string{
			"Engineering": {"Alice@Corp.Example", " bob-subject ", ""},
			"  Sales  ":   {"carol@corp.example"},
		},
	}
	got := ToGroupConfig(in)
	want := identity.GroupConfig{
		GroupClaim:   "groups",
		DefaultGroup: "Default", // DefaultGroupOr normalizes at use; mapping passes through
		Leads: map[string][]string{
			"engineering": {"alice@corp.example", "bob-subject"},
			"sales":       {"carol@corp.example"},
		},
	}
	if got.GroupClaim != want.GroupClaim {
		t.Errorf("GroupClaim = %q, want %q", got.GroupClaim, want.GroupClaim)
	}
	if got.DefaultGroup != want.DefaultGroup {
		t.Errorf("DefaultGroup = %q, want %q", got.DefaultGroup, want.DefaultGroup)
	}
	if !reflect.DeepEqual(got.Leads, want.Leads) {
		t.Errorf("Leads = %#v, want %#v", got.Leads, want.Leads)
	}
}

func TestToGroupConfig_EmptyLeads(t *testing.T) {
	got := ToGroupConfig(domainconfig.RBACConfig{})
	if len(got.Leads) != 0 {
		t.Errorf("Leads = %#v, want empty", got.Leads)
	}
}
```

- [ ] **Step 4: Run the test to verify it fails**

Run: `go test ./adapter/config/ -run TestToGroupConfig -v`
Expected: FAIL — `undefined: ToGroupConfig`.

- [ ] **Step 5: Implement `ToGroupConfig`**

Create `adapter/config/rbac.go`:

```go
package config

import (
	"strings"

	domainconfig "github.com/arbitron/arbitron/domain/config"
	"github.com/arbitron/arbitron/domain/identity"
)

// ToGroupConfig maps the load-time domainconfig.RBACConfig into the
// resolution-time identity.GroupConfig consumed by the group-resolution helpers.
// Department keys and lead identifiers are normalized to lower-case + trimmed;
// blank identifiers are dropped. The DefaultGroup string is passed through
// verbatim — identity.GroupConfig.DefaultGroupOr normalizes it (and applies the
// "default" sentinel) at use.
func ToGroupConfig(in domainconfig.RBACConfig) identity.GroupConfig {
	var leads map[string][]string
	if len(in.Leads) > 0 {
		leads = make(map[string][]string, len(in.Leads))
		for dept, ids := range in.Leads {
			ndept := identity.NormalizeDept(dept)
			if ndept == "" {
				continue
			}
			normIDs := make([]string, 0, len(ids))
			for _, id := range ids {
				nid := strings.ToLower(strings.TrimSpace(id))
				if nid != "" {
					normIDs = append(normIDs, nid)
				}
			}
			if len(normIDs) > 0 {
				leads[ndept] = normIDs
			}
		}
	}
	return identity.GroupConfig{
		GroupClaim:   in.GroupClaim,
		DefaultGroup: in.DefaultGroup,
		Leads:        leads,
	}
}
```

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./adapter/config/ -run TestToGroupConfig -v`
Expected: PASS.

- [ ] **Step 7: Add loader default keys**

In `adapter/config/loader.go`, inside the `defaults()` map, after the existing `"auth.session.cookiesecure": true,` line, add:

```go
		// RBAC (auth mode only). With an empty groupclaim and no leads, every
		// authenticated user is a member of the single default group.
		"auth.rbac.groupclaim":   "",
		"auth.rbac.defaultgroup": "default",
		// auth.rbac.leads is a map[string][]string; it has no flat default (an
		// absent key unmarshals to a nil map). Configured via YAML or the
		// ARBITRON_AUTH_RBAC_LEADS_<DEPT> env overlay below.
```

- [ ] **Step 8: Add the env overlay for `defaultgroup` and the leads map**

In `adapter/config/loader.go`, after the existing `ARBITRON_AUTH_OIDC_SCOPES` overlay block (the one ending its `if raw := ...` block), add:

```go
	// ARBITRON_AUTH_RBAC_LEADS_<DEPT>=id1,id2 overlays one department's lead list
	// as a real slice (the env provider would otherwise leave it a comma string).
	// Department names with non-alphanumeric characters are not expressible via
	// env and must use the YAML file. Example:
	//   ARBITRON_AUTH_RBAC_LEADS_ENGINEERING=alice@corp.example,bob-subject
	const leadsEnvPrefix = "ARBITRON_AUTH_RBAC_LEADS_"
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		name, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(name, leadsEnvPrefix) || val == "" {
			continue
		}
		dept := strings.ToLower(strings.TrimPrefix(name, leadsEnvPrefix))
		if dept == "" {
			continue
		}
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"auth.rbac.leads." + dept: splitCSV(val),
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading auth.rbac.leads.%s: %w", dept, err)
		}
	}
```

- [ ] **Step 9: Add the YAML comment block**

In `adapter/config/loader.go`, inside `defaultYAMLContent()`, extend the commented `auth:` block. Replace the existing `#     cookiesecure: true     # set false ONLY for local http dev` line with:

```go
#     cookiesecure: true     # set false ONLY for local http dev
#   rbac:
#     groupclaim: ""           # token claim carrying group membership; "" => default group only
#                              #   Override via ARBITRON_AUTH_RBAC_GROUPCLAIM=groups
#     defaultgroup: "default"  # sentinel department for claimless users
#                              #   Override via ARBITRON_AUTH_RBAC_DEFAULTGROUP=default
#     leads:                   # department -> list of lead subjects/emails (case-insensitive)
#       engineering: ["alice@corp.example", "bob-subject-id"]
#                              #   Per-dept env overlay: ARBITRON_AUTH_RBAC_LEADS_ENGINEERING=alice@corp.example,bob-subject-id
```

- [ ] **Step 10: Write the failing loader integration test**

Create `adapter/config/loader_rbac_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return p
}

func TestLoad_RBAC_Defaults(t *testing.T) {
	// A bare local-mode config yields the default-group fallback and no leads.
	cfg, err := Load(writeConfig(t, "auth:\n  mode: \"local\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.RBAC.GroupClaim != "" {
		t.Errorf("GroupClaim = %q, want empty", cfg.Auth.RBAC.GroupClaim)
	}
	if cfg.Auth.RBAC.DefaultGroup != "default" {
		t.Errorf("DefaultGroup = %q, want default", cfg.Auth.RBAC.DefaultGroup)
	}
	if len(cfg.Auth.RBAC.Leads) != 0 {
		t.Errorf("Leads = %#v, want empty", cfg.Auth.RBAC.Leads)
	}
}

func TestLoad_RBAC_FromYAML(t *testing.T) {
	body := `auth:
  mode: "org"
  oidc:
    issuerurl: "https://idp.example"
    clientid: "cid"
    clientsecret: "secret"
    redirecturl: "https://app.example/auth/callback"
  rbac:
    groupclaim: "groups"
    defaultgroup: "everyone"
    leads:
      engineering: ["alice@corp.example", "bob-subject"]
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.RBAC.GroupClaim != "groups" {
		t.Errorf("GroupClaim = %q, want groups", cfg.Auth.RBAC.GroupClaim)
	}
	if cfg.Auth.RBAC.DefaultGroup != "everyone" {
		t.Errorf("DefaultGroup = %q, want everyone", cfg.Auth.RBAC.DefaultGroup)
	}
	want := []string{"alice@corp.example", "bob-subject"}
	if !reflect.DeepEqual(cfg.Auth.RBAC.Leads["engineering"], want) {
		t.Errorf("Leads[engineering] = %#v, want %#v", cfg.Auth.RBAC.Leads["engineering"], want)
	}
}

func TestLoad_RBAC_EnvOverlay(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_GROUPCLAIM", "memberOf")
	t.Setenv("ARBITRON_AUTH_RBAC_LEADS_PLATFORM", "dave@corp.example, eve-subject ")
	cfg, err := Load(writeConfig(t, "auth:\n  mode: \"local\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.RBAC.GroupClaim != "memberOf" {
		t.Errorf("GroupClaim = %q, want memberOf", cfg.Auth.RBAC.GroupClaim)
	}
	want := []string{"dave@corp.example", "eve-subject"}
	if !reflect.DeepEqual(cfg.Auth.RBAC.Leads["platform"], want) {
		t.Errorf("Leads[platform] = %#v, want %#v", cfg.Auth.RBAC.Leads["platform"], want)
	}
}
```

- [ ] **Step 11: Run the loader test to verify it fails (then passes after Steps 7–9 are applied)**

Run: `go test ./adapter/config/ -run TestLoad_RBAC -v`
Expected: PASS (Steps 7–9 already wired the keys; this confirms YAML + env overlay).
If FAIL with `GroupClaim = "", want memberOf`, re-check the env overlay in Step 8.

- [ ] **Step 12: Run the full config package test**

Run: `go test ./adapter/config/ ./domain/config/ -v`
Expected: PASS (new RBAC tests plus all existing config tests still green).

- [ ] **Step 13: Vet**

Run: `go vet ./adapter/config/ ./domain/config/`
Expected: clean.

- [ ] **Step 14: Commit**

```bash
git add domain/config/config.go adapter/config/loader.go adapter/config/rbac.go adapter/config/rbac_test.go adapter/config/loader_rbac_test.go
git commit -m "feat(rbac): auth.rbac config (groupclaim, defaultgroup, leads) + mapping

Adds domainconfig.RBACConfig, koanf defaults/YAML/env overlay (incl. per-dept
ARBITRON_AUTH_RBAC_LEADS_<DEPT>), and ToGroupConfig mapping into the resolution
shape with normalized department keys + lead identifiers.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Test Scenario Coverage (this task)

| Category | Scenario | Step |
|----------|----------|------|
| Happy | RBAC config loaded from YAML | 10 |
| Happy | `ToGroupConfig` maps + lower-cases keys/identifiers | 3 |
| Edge | Blank lead identifiers dropped; blank dept key dropped | 3 |
| Edge | Empty config → nil Leads, no panic | 3 |
| Edge | Default-group default value `"default"` when unset | 10 |
| Error | (Loader still fails fast on org-mode missing OIDC — unchanged) | covered by existing loader tests |
| Authz | Lead identifiers normalized so subject/email match later | 3 |
| Mode | Local-mode config loads RBAC defaults harmlessly (consumed inert) | 10 |
| Env | `ARBITRON_AUTH_RBAC_*` and per-dept leads overlay | 10 |
