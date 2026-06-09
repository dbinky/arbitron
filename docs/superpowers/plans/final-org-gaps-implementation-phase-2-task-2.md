# Final Org Gaps — Phase 2, Task 2: Mapping + Verbatim Fallback in ResolveDepartments, Provisioner & Config Wiring

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GroupConfig.Mapping` (normalized raw-claim ⇒ friendly department) and apply mapping-with-verbatim-fallback at the single `ResolveDepartments` chokepoint; make `Provisioner.ResolveByEmail` consume the edge groups; and wire `cfg.Auth.Edge.GroupClaim`/`DefaultGroup`/`Mapping` from env/secrets so enabling group mapping is headless.

**Architecture:** `identity.ResolveDepartments` stays the single department-resolution chokepoint. It gains a per-value policy: normalize the raw claim, look it up in `cfg.Mapping`, use the friendly name on a hit or the verbatim normalized value on a miss, dedupe, and fall back to the default group when empty. `Mapping` keys are normalized at load time so lookups are case-insensitive. The provisioner's edge path stops hardcoding `nil` and threads the groups Task 1 delivered. Config wiring extends `EdgeConfig` and the edge `Verifier`/`GroupConfig` construction so the deployment is configured entirely through environment/secrets. **Group claims never set role** — admin stays email-allow-list only (no change to that path).

**Tech Stack:** Go, koanf (config), `coreos/go-oidc/v3` (edge verifier construction), `testify`.

---

## Context the implementer needs

- **Spec:** `docs/specs/final-org-gaps-spec.md` — Capability B: mapping with verbatim fallback; multiple groups ⇒ union of departments; claimless ⇒ default-everything; admin unchanged & never conferred by a group claim; configuration is headless (group claim name + optional value→department map + default-group fallback, all via env/secrets).
- **Design:** `docs/superpowers/specs/final-org-gaps-design-phase-2-sso-group-mapping.md` — sections 2, 3, 5.
- **Chokepoint:** `domain/identity/groups.go` — `GroupConfig{GroupClaim, DefaultGroup, Leads}`; `ResolveDepartments(claimValues, cfg)`; helpers `NormalizeDept`, `DedupeDepts`, `DefaultGroupOr`, `EffectiveMembership`.
- **Provisioner:** `domain/instance/provisioner.go` — `ResolveByEmail` currently (after Task 1) has signature `(ctx, email, groups []string)` but its body still does `identity.ResolveDepartments(nil, p.groups)`. `Resolve` (OIDC path) already does `identity.ResolveDepartments(c.Groups, p.groups)`.
- **Load-time config:** `domain/config/config.go` — `EdgeConfig{TeamDomain, Audiences}` and `RBACConfig{GroupClaim, DefaultGroup, Leads}`.
- **Adapter mapping:** `adapter/config/rbac.go` — `ToGroupConfig(RBACConfig) identity.GroupConfig` (normalizes Leads). This is where `Mapping` normalization will live.
- **Loader:** `adapter/config/loader.go` — koanf defaults (lines ~72–91), env overlays for slice/map fields (lines ~345–398), `normalize` (line ~447), `validateAuth` (line ~501). The `ARBITRON_AUTH_RBAC_LEADS_<DEPT>` overlay (lines 379–398) is the precedent for a per-key map env overlay.
- **Wiring:** `cmd/arbitron/main.go` — `groupCfg := configadapter.ToGroupConfig(cfg.Auth.RBAC)` (line ~332); the edge verifier is built from `adaptedge.Config{TeamDomain, Audiences}` (lines ~363–366). The edge verifier needs `GroupClaim` added here.

> **Decision — where does `Mapping` live in config?** It belongs to RBAC resolution, alongside `Leads`, so it is added to **`RBACConfig.Mapping`** and surfaced under the koanf key `auth.rbac.mapping`. The spec phrases it as "`cfg.Auth.Edge` gains GroupClaim/DefaultGroup/Mapping" — those three are already RBAC-resolution concerns that flow into `identity.GroupConfig`; `GroupClaim`/`DefaultGroup` already live on `RBACConfig`. We keep all three on `RBACConfig` (the single source mapped by `ToGroupConfig`) and additionally feed the **edge verifier's** `GroupClaim` from `cfg.Auth.RBAC.GroupClaim` at wiring time. No new field is duplicated onto `EdgeConfig`.

---

### Task 2a: Add `Mapping` to `GroupConfig` and apply mapping+verbatim in `ResolveDepartments`

**Files:**
- Modify: `domain/identity/groups.go`
- Test: `domain/identity/groups_test.go` (append; create if absent)

- [ ] **Step 1: Write the failing tests** (5 scenario categories)

Append to `domain/identity/groups_test.go`:

```go
// --- Happy path ---

func TestResolveDepartments_MappedGroup_UsesFriendlyName(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"guid-123"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}

func TestResolveDepartments_UnmappedGroup_UsesVerbatimNormalized(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"Marketing"}, cfg) // no mapping entry
	assert.Equal(t, []string{"marketing"}, got, "verbatim fallback is normalized (lower-cased)")
}

func TestResolveDepartments_MultipleGroups_UnionMappedAndVerbatim(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"guid-123", "sales"}, cfg)
	assert.Equal(t, []string{"engineering", "sales"}, got, "first-seen order preserved")
}

func TestResolveDepartments_NoGroups_FallsBackToDefault(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments(nil, cfg)
	assert.Equal(t, []string{"default"}, got)
}

// --- Input validation ---

func TestResolveDepartments_MappingKeyMatchedCaseInsensitively(t *testing.T) {
	// Keys are normalized at load (Task 2b). ResolveDepartments normalizes the
	// incoming claim before lookup, so a differently-cased claim still hits.
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "engineering"}}
	got := ResolveDepartments([]string{"GUID-123"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}

func TestResolveDepartments_BlankAndWhitespaceClaimsDropped(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default"}
	got := ResolveDepartments([]string{"  ", "", "eng"}, cfg)
	assert.Equal(t, []string{"eng"}, got)
}

func TestResolveDepartments_AllBlankClaims_FallsBackToDefault(t *testing.T) {
	cfg := GroupConfig{DefaultGroup: "default"}
	got := ResolveDepartments([]string{"  ", ""}, cfg)
	assert.Equal(t, []string{"default"}, got)
}

func TestResolveDepartments_DuplicatesAfterMappingCollapsed(t *testing.T) {
	// Two distinct raw claims mapping to the SAME friendly name dedupe to one.
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{
		"guid-a": "engineering",
		"guid-b": "engineering",
	}}
	got := ResolveDepartments([]string{"guid-a", "guid-b"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}

func TestResolveDepartments_MappedFriendlyNameNormalized(t *testing.T) {
	// Even if a mapping value somehow reaches here un-normalized, output is normalized.
	cfg := GroupConfig{DefaultGroup: "default", Mapping: map[string]string{"guid-123": "Engineering "}}
	got := ResolveDepartments([]string{"guid-123"}, cfg)
	assert.Equal(t, []string{"engineering"}, got)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./domain/identity/ -run TestResolveDepartments -v`
Expected: FAIL to compile — `GroupConfig` has no `Mapping` field.

- [ ] **Step 3: Add the field and apply the policy**

In `domain/identity/groups.go`, add to `GroupConfig` (after `Leads`):

```go
	// Mapping maps a NORMALIZED raw-claim value to a friendly department name. The
	// loader normalizes both keys and values (NormalizeDept) so lookups here are
	// case-insensitive. A raw claim with a mapping entry resolves to the friendly
	// name; a claim with no entry resolves to its own normalized value (verbatim
	// fallback). Nil/empty => every claim is verbatim.
	Mapping map[string]string
```

Replace `ResolveDepartments` with the mapping-aware version:

```go
// ResolveDepartments is the single department-resolution chokepoint. For each raw
// claim value, in first-seen order: normalize it; drop blanks; if cfg.Mapping has
// an entry, use the normalized friendly name; otherwise use the normalized
// verbatim value. The result is de-duplicated (order-preserving). If the result
// is empty (no claim configured, claim absent, or all values blank), it returns
// the single default-group sentinel so a caller is never in zero departments.
func ResolveDepartments(claimValues []string, cfg GroupConfig) []string {
	resolved := make([]string, 0, len(claimValues))
	for _, v := range claimValues {
		k := NormalizeDept(v)
		if k == "" {
			continue
		}
		if mapped, ok := cfg.Mapping[k]; ok {
			if m := NormalizeDept(mapped); m != "" {
				resolved = append(resolved, m)
				continue
			}
		}
		resolved = append(resolved, k) // verbatim fallback
	}
	out := DedupeDepts(resolved)
	if len(out) == 0 {
		return []string{cfg.DefaultGroupOr()}
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./domain/identity/ -v`
Expected: PASS for all `TestResolveDepartments*`. The pre-existing `ResolveDepartments`/`EffectiveMembership` tests still pass (mapping is nil ⇒ pure verbatim, identical to prior behavior).

- [ ] **Step 5: Commit**

```bash
git add domain/identity/groups.go domain/identity/groups_test.go
git commit -m "feat(identity): mapping+verbatim fallback in ResolveDepartments chokepoint"
```

---

### Task 2b: Map `RBACConfig.Mapping` into `GroupConfig` (normalize keys & values)

**Files:**
- Modify: `domain/config/config.go`
- Modify: `adapter/config/rbac.go`
- Test: `adapter/config/rbac_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `adapter/config/rbac_test.go`:

```go
func TestToGroupConfig_NormalizesMappingKeysAndValues(t *testing.T) {
	in := domainconfig.RBACConfig{
		GroupClaim:   "groups",
		DefaultGroup: "default",
		Mapping: map[string]string{
			"GUID-123":  "Engineering",
			" guid-b ":  " Sales ",
		},
	}
	got := config.ToGroupConfig(in)
	// Keys and values are normalized (trimmed + lower-cased).
	assert.Equal(t, "engineering", got.Mapping["guid-123"])
	assert.Equal(t, "sales", got.Mapping["guid-b"])
}

func TestToGroupConfig_DropsBlankMappingEntries(t *testing.T) {
	in := domainconfig.RBACConfig{Mapping: map[string]string{
		"   ":      "engineering", // blank key dropped
		"guid-123": "   ",         // blank value dropped
		"guid-ok":  "platform",
	}}
	got := config.ToGroupConfig(in)
	_, hasBlankKey := got.Mapping[""]
	assert.False(t, hasBlankKey, "blank key dropped")
	_, hasBlankVal := got.Mapping["guid-123"]
	assert.False(t, hasBlankVal, "entry with blank value dropped")
	assert.Equal(t, "platform", got.Mapping["guid-ok"])
}

func TestToGroupConfig_NilMapping_YieldsNilMapping(t *testing.T) {
	got := config.ToGroupConfig(domainconfig.RBACConfig{})
	assert.Nil(t, got.Mapping)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/config/ -run TestToGroupConfig_ -v`
Expected: FAIL to compile — `RBACConfig` has no `Mapping`, `GroupConfig.Mapping` was added in Task 2a (so it compiles there) but `ToGroupConfig` does not populate it.

- [ ] **Step 3: Add `RBACConfig.Mapping`**

In `domain/config/config.go`, add to `RBACConfig` (after `Leads`):

```go
	// Mapping maps a raw group-claim VALUE to a friendly department NAME, so opaque
	// claim values (e.g. directory GUIDs) become readable department names. Keys
	// and values are normalized to lower-case + trimmed at load. A claim value with
	// no entry falls back to its own normalized value (verbatim). Suppliable via
	// env/secrets (ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE>=friendly) or YAML.
	Mapping map[string]string
```

- [ ] **Step 4: Populate `Mapping` in `ToGroupConfig`**

In `adapter/config/rbac.go`, inside `ToGroupConfig`, build a normalized mapping and set it on the returned `GroupConfig`. Add before the `return`:

```go
	var mapping map[string]string
	if len(in.Mapping) > 0 {
		mapping = make(map[string]string, len(in.Mapping))
		for raw, friendly := range in.Mapping {
			k := identity.NormalizeDept(raw)
			v := identity.NormalizeDept(friendly)
			if k == "" || v == "" {
				continue // drop blank key or blank value
			}
			mapping[k] = v
		}
		if len(mapping) == 0 {
			mapping = nil
		}
	}
```

And change the returned struct literal to include `Mapping: mapping`:

```go
	return identity.GroupConfig{
		GroupClaim:   in.GroupClaim,
		DefaultGroup: in.DefaultGroup,
		Leads:        leads,
		Mapping:      mapping,
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./adapter/config/ ./domain/config/ -run 'TestToGroupConfig|RBAC' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add domain/config/config.go adapter/config/rbac.go adapter/config/rbac_test.go
git commit -m "feat(config): RBACConfig.Mapping normalized into GroupConfig"
```

---

### Task 2c: Provisioner consumes the edge groups

**Files:**
- Modify: `domain/instance/provisioner.go`
- Test: `domain/instance/provisioner_test.go` (append; reuse the existing fake `UserRepository`)

- [ ] **Step 1: Write the failing tests** (happy path + authorization/role)

First inspect the existing provisioner test fakes:

Run: `grep -n "ResolveByEmail\|fakeUser\|UserRepository\|ReplaceDepartments\|ListDepartments" domain/instance/provisioner_test.go`
Expected: a fake `UserRepository` that records `ReplaceDepartments(userID, depts)`. Reuse it; the test asserts on the recorded `depts`.

Append:

```go
func TestResolveByEmail_MappedGroup_SnapshotsFriendlyDepartment(t *testing.T) {
	users := newFakeUserRepo() // adapt to the file's existing constructor
	p := NewProvisioner(users, nil, identity.GroupConfig{
		DefaultGroup: "default",
		Mapping:      map[string]string{"guid-123": "engineering"},
	})
	u, err := p.ResolveByEmail(context.Background(), "alice@corp.example", []string{"guid-123"})
	require.NoError(t, err)
	assert.Equal(t, []string{"engineering"}, users.departmentsFor(u.ID))
}

func TestResolveByEmail_UnmappedGroup_SnapshotsVerbatim(t *testing.T) {
	users := newFakeUserRepo()
	p := NewProvisioner(users, nil, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.ResolveByEmail(context.Background(), "bob@corp.example", []string{"Marketing"})
	require.NoError(t, err)
	assert.Equal(t, []string{"marketing"}, users.departmentsFor(u.ID))
}

func TestResolveByEmail_MultipleGroups_SnapshotsUnion(t *testing.T) {
	users := newFakeUserRepo()
	p := NewProvisioner(users, nil, identity.GroupConfig{
		DefaultGroup: "default",
		Mapping:      map[string]string{"guid-123": "engineering"},
	})
	u, err := p.ResolveByEmail(context.Background(), "carol@corp.example", []string{"guid-123", "sales"})
	require.NoError(t, err)
	assert.Equal(t, []string{"engineering", "sales"}, users.departmentsFor(u.ID))
}

func TestResolveByEmail_NoGroups_StillDefaultGroup(t *testing.T) {
	users := newFakeUserRepo()
	p := NewProvisioner(users, nil, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.ResolveByEmail(context.Background(), "dan@corp.example", nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, users.departmentsFor(u.ID))
}

// Authorization/role: a group claim NEVER confers admin; admin is email-allow-list only.
func TestResolveByEmail_GroupClaimNeverConfersAdmin(t *testing.T) {
	users := newFakeUserRepo()
	// "admins" looks privilege-y but is just a department; bootstrap list is empty.
	p := NewProvisioner(users, nil, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.ResolveByEmail(context.Background(), "eve@corp.example", []string{"admins", "superuser"})
	require.NoError(t, err)
	assert.Equal(t, identity.RoleUser, u.Role, "a group claim must never grant admin")
}

func TestResolveByEmail_BootstrapEmail_IsAdminRegardlessOfGroups(t *testing.T) {
	users := newFakeUserRepo()
	p := NewProvisioner(users, []string{"boss@corp.example"}, identity.GroupConfig{DefaultGroup: "default"})
	u, err := p.ResolveByEmail(context.Background(), "boss@corp.example", []string{"interns"})
	require.NoError(t, err)
	assert.Equal(t, identity.RoleAdmin, u.Role, "email allow-list confers admin even with a non-admin group claim")
}
```

(Adapt `newFakeUserRepo()` / `departmentsFor()` to whatever the existing test file names its fake. If the fake only records into an exported field, assert on that field directly.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./domain/instance/ -run TestResolveByEmail -v`
Expected: FAIL — the mapped/verbatim/union tests fail because the body still resolves `nil` (so every user lands in `default`).

- [ ] **Step 3: Thread groups into the snapshot**

In `domain/instance/provisioner.go`, in `ResolveByEmail`, change:

```go
	depts := identity.ResolveDepartments(nil, p.groups)
```

to:

```go
	depts := identity.ResolveDepartments(groups, p.groups)
```

Also update the method's doc comment: replace the sentence "the user's departments are snapshotted to the default group (Phase 1 consumes no group claim)" with "the user's departments are snapshotted from the edge-forwarded group claim via the same ResolveDepartments chokepoint the OIDC path uses (mapping ▸ verbatim ▸ default); a user with no groups still falls back to the default group."

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./domain/instance/ -v`
Expected: PASS for all `TestResolveByEmail*` and the pre-existing provisioner tests.

- [ ] **Step 5: Commit**

```bash
git add domain/instance/provisioner.go domain/instance/provisioner_test.go
git commit -m "feat(instance): ResolveByEmail snapshots departments from edge groups"
```

---

### Task 2d: Config loader — `auth.rbac.mapping` from YAML + env, and edge verifier `GroupClaim` wiring

**Files:**
- Modify: `adapter/config/loader.go`
- Modify: `cmd/arbitron/main.go`
- Test: `adapter/config/loader_rbac_test.go` (append)

- [ ] **Step 1: Write the failing tests**

Append to `adapter/config/loader_rbac_test.go` (match the file's existing loader-test helpers; loader tests typically write a temp YAML and/or set env vars then call `Load`). Two scenarios — YAML map and env overlay:

```go
func TestLoad_RBACMapping_FromYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
auth:
  mode: edge
  edge:
    teamdomain: "https://acme.cloudflareaccess.com"
    audiences: ["app-aud"]
  rbac:
    groupclaim: "groups"
    defaultgroup: "default"
    mapping:
      guid-123: engineering
      guid-b: sales
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "engineering", cfg.Auth.RBAC.Mapping["guid-123"])
	assert.Equal(t, "sales", cfg.Auth.RBAC.Mapping["guid-b"])
}

func TestLoad_RBACMapping_FromEnvOverlay(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_MAPPING_GUID-123", "engineering")
	t.Setenv("ARBITRON_AUTH_RBAC_MAPPING_SALES-GUID", "sales")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
auth:
  mode: edge
  edge:
    teamdomain: "https://acme.cloudflareaccess.com"
    audiences: ["app-aud"]
  rbac:
    groupclaim: "groups"
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	// Env names are upper-cased by convention; the overlay lower-cases the suffix
	// for the raw-claim key. Values pass through; ToGroupConfig normalizes later.
	assert.Equal(t, "engineering", cfg.Auth.RBAC.Mapping["guid-123"])
	assert.Equal(t, "sales", cfg.Auth.RBAC.Mapping["sales-guid"])
}

func TestLoad_NoGroupClaim_BehavesAsToday(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
auth:
  mode: edge
  edge:
    teamdomain: "https://acme.cloudflareaccess.com"
    audiences: ["app-aud"]
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Auth.RBAC.GroupClaim, "unset GroupClaim => no group extraction (today's behavior)")
}
```

(If `config.Load` takes different arguments in this repo — e.g. `Load(paths ...string)` or a loader struct — match the existing signature used by the other tests in this file. Inspect with `grep -n "config.Load\|func Load" adapter/config/*.go` first.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./adapter/config/ -run 'TestLoad_RBACMapping|TestLoad_NoGroupClaim' -v`
Expected: FAIL — `cfg.Auth.RBAC.Mapping` is always nil (no koanf default, no env overlay).

- [ ] **Step 3: Add a koanf default for the mapping map**

In `adapter/config/loader.go`, in the defaults block near the `auth.rbac.leads` comment (~line 89), the `mapping` map (like `leads`) has no flat default — it is populated by YAML or the env overlay. Add a clarifying comment beside the leads comment so future readers know `mapping` is handled the same way:

```go
		// auth.rbac.mapping is a map[string]string (raw-claim -> friendly dept); like
		// leads it has no flat default and is supplied via YAML or the
		// ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE> env overlay below.
```

(No default value is registered for the map, matching `leads`.)

- [ ] **Step 4: Add the env overlay for per-entry mapping**

In `adapter/config/loader.go`, immediately AFTER the `ARBITRON_AUTH_RBAC_LEADS_` overlay loop (~line 398), add the mapping overlay (single scalar value per entry, so no `splitCSV`):

```go
	// ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE>=friendly overlays one raw-claim ->
	// friendly-department entry. The suffix after the prefix is the raw claim value
	// (lower-cased; raw values with characters not expressible as env names must use
	// the YAML file). Example:
	//   ARBITRON_AUTH_RBAC_MAPPING_GUID-123=engineering
	const mappingEnvPrefix = "ARBITRON_AUTH_RBAC_MAPPING_"
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		name, val := kv[:eq], kv[eq+1:]
		if !strings.HasPrefix(name, mappingEnvPrefix) || val == "" {
			continue
		}
		rawKey := strings.ToLower(strings.TrimPrefix(name, mappingEnvPrefix))
		if rawKey == "" {
			continue
		}
		if err := k.Load(confmap.Provider(map[string]interface{}{
			"auth.rbac.mapping." + rawKey: val,
		}, "."), nil); err != nil {
			return nil, fmt.Errorf("loading auth.rbac.mapping.%s: %w", rawKey, err)
		}
	}
```

- [ ] **Step 5: Wire the edge verifier's `GroupClaim` in main.go**

In `cmd/arbitron/main.go`, in the edge-verifier construction (~line 363), add `GroupClaim` so the verifier extracts the configured claim:

```go
		edgeVerifier, eerr := adaptedge.New(context.Background(), adaptedge.Config{
			TeamDomain: cfg.Auth.Edge.TeamDomain,
			Audiences:  cfg.Auth.Edge.Audiences,
			GroupClaim: cfg.Auth.RBAC.GroupClaim,
		})
```

(`groupCfg := configadapter.ToGroupConfig(cfg.Auth.RBAC)` at line ~332 already carries `GroupClaim`, `DefaultGroup`, `Leads`, and now `Mapping` into the provisioner/middleware; no further change there.)

- [ ] **Step 6: Run tests + full build to verify green**

Run: `go test ./adapter/config/ -v && go build ./...`
Expected: PASS; build green.

- [ ] **Step 7: Commit**

```bash
git add adapter/config/loader.go cmd/arbitron/main.go adapter/config/loader_rbac_test.go
git commit -m "feat(config): load auth.rbac.mapping (yaml+env); wire edge GroupClaim"
```

---

## Self-review checklist (run before handing off)

- [ ] `ResolveDepartments` remains the SINGLE chokepoint; mapping ▸ verbatim ▸ default applied per value, first-seen order preserved, deduped.
- [ ] `Mapping` keys AND values normalized at load (`ToGroupConfig`); blank keys/values dropped; nil-safe.
- [ ] Mapping lookup is case-insensitive (claim normalized before lookup, keys normalized at load).
- [ ] `Provisioner.ResolveByEmail` threads `groups` (no remaining hardcoded `nil`); `Resolve` (OIDC) unchanged.
- [ ] A group claim NEVER sets role — admin remains email-allow-list only (verified by the two role tests).
- [ ] `cfg.Auth.RBAC.Mapping` loads from YAML and `ARBITRON_AUTH_RBAC_MAPPING_<RAWVALUE>` env; unset `GroupClaim` ⇒ behaves as today.
- [ ] Edge verifier built with `GroupClaim: cfg.Auth.RBAC.GroupClaim`.
- [ ] `go build ./...` green.
