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

// ARBITRON_AUTH_RBAC_DEFAULTGROUP sets the sentinel department for claimless
// users via the generic env provider. This pins the env → koanf key mapping so
// a rename or transformation change is caught immediately.
func TestLoad_RBAC_DefaultGroup_EnvOverride(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_DEFAULTGROUP", "everyone")
	cfg, err := Load(writeConfig(t, "auth:\n  mode: \"local\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.RBAC.DefaultGroup != "everyone" {
		t.Errorf("DefaultGroup = %q, want everyone", cfg.Auth.RBAC.DefaultGroup)
	}
}

// Env overlay wins over YAML for the same dept's lead list.
// This pins the precedence order that production operators rely on when fixing
// a misconfigured YAML lead list via environment without editing the file.
func TestLoad_RBAC_EnvOverlay_WinsOverYAML(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_LEADS_ENGINEERING", "env-alice@corp.example")
	body := `auth:
  mode: "local"
  rbac:
    leads:
      engineering: ["yaml-bob@corp.example"]
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	leads := cfg.Auth.RBAC.Leads["engineering"]
	if len(leads) != 1 || leads[0] != "env-alice@corp.example" {
		t.Errorf("Leads[engineering] = %v, want [env-alice@corp.example] (env must win over YAML)", leads)
	}
}

// An empty-value ARBITRON_AUTH_RBAC_LEADS_<DEPT>= env var clobbers YAML leads
// at the raw RBACConfig level (generic env provider wins: the koanf key is set
// to "" which unmarshals as [""]). The critical invariant is downstream:
// ToGroupConfig drops blank lead IDs, so GroupConfig.Leads["engineering"] is
// absent even when RBACConfig.Leads["engineering"] contains only blank strings.
func TestLoad_RBAC_EmptyValueEnvOverlay_DropsInToGroupConfig(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_LEADS_ENGINEERING", "")
	body := `auth:
  mode: "local"
  rbac:
    leads:
      engineering: ["yaml-alice@corp.example"]
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// After normalization, ToGroupConfig must produce no valid leads for engineering.
	gc, err := ToGroupConfig(cfg.Auth.RBAC)
	if err != nil {
		t.Fatalf("ToGroupConfig: %v", err)
	}
	if _, ok := gc.Leads["engineering"]; ok {
		t.Errorf("ToGroupConfig produced Leads[engineering] = %v, want dept absent (all-blank from empty env)", gc.Leads["engineering"])
	}
}

// Multiple per-dept env overlays can coexist; each adds one department's leads.
func TestLoad_RBAC_MultipleLeadsEnvOverlays(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_LEADS_ENGINEERING", "alice@corp.example")
	t.Setenv("ARBITRON_AUTH_RBAC_LEADS_SALES", "bob@corp.example, carol-subj ")
	cfg, err := Load(writeConfig(t, "auth:\n  mode: \"local\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Auth.RBAC.Leads["engineering"], []string{"alice@corp.example"}) {
		t.Errorf("Leads[engineering] = %v, want [alice@corp.example]", cfg.Auth.RBAC.Leads["engineering"])
	}
	if !reflect.DeepEqual(cfg.Auth.RBAC.Leads["sales"], []string{"bob@corp.example", "carol-subj"}) {
		t.Errorf("Leads[sales] = %v, want [bob@corp.example carol-subj]", cfg.Auth.RBAC.Leads["sales"])
	}
}

func TestLoad_RBACMapping_FromYAML(t *testing.T) {
	body := `auth:
  mode: "local"
  rbac:
    groupclaim: "groups"
    defaultgroup: "default"
    mapping:
      guid-123: engineering
      guid-b: sales
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Auth.RBAC.Mapping["guid-123"]; got != "engineering" {
		t.Errorf("Mapping[guid-123] = %q, want engineering", got)
	}
	if got := cfg.Auth.RBAC.Mapping["guid-b"]; got != "sales" {
		t.Errorf("Mapping[guid-b] = %q, want sales", got)
	}
}

func TestLoad_RBACMapping_FromEnvOverlay(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_MAPPING_GUID-123", "engineering")
	t.Setenv("ARBITRON_AUTH_RBAC_MAPPING_SALES-GUID", "sales")
	cfg, err := Load(writeConfig(t, "auth:\n  mode: \"local\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Env names are upper-cased by convention; the overlay lower-cases the suffix
	// for the raw-claim key. Values pass through; ToGroupConfig normalizes later.
	if got := cfg.Auth.RBAC.Mapping["guid-123"]; got != "engineering" {
		t.Errorf("Mapping[guid-123] = %q, want engineering", got)
	}
	if got := cfg.Auth.RBAC.Mapping["sales-guid"]; got != "sales" {
		t.Errorf("Mapping[sales-guid] = %q, want sales", got)
	}
}

// Env overlay wins over YAML for the same mapping key.
// This is the exact analog of TestLoad_RBAC_EnvOverlay_WinsOverYAML for leads:
// an operator can fix a misconfigured YAML mapping entry by setting the env var
// without editing the file. Pins the env-layer-loads-last contract.
func TestLoad_RBACMapping_EnvOverlay_WinsOverYAML(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_MAPPING_GUID-123", "engineering")
	body := `auth:
  mode: "local"
  rbac:
    mapping:
      guid-123: sales
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Auth.RBAC.Mapping["guid-123"]; got != "engineering" {
		t.Errorf("Mapping[guid-123] = %q, want engineering (env must win over YAML)", got)
	}
}

// YAML and env mapping entries with different keys both survive — the overlay
// is additive, not a full replacement. Operators can define base mappings in
// YAML and inject secret/dynamic GUIDs via env without removing YAML entries.
func TestLoad_RBACMapping_EnvAndYAML_DifferentKeys_Coexist(t *testing.T) {
	t.Setenv("ARBITRON_AUTH_RBAC_MAPPING_GUID-ENV", "platform")
	body := `auth:
  mode: "local"
  rbac:
    mapping:
      guid-yaml: engineering
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Auth.RBAC.Mapping["guid-yaml"]; got != "engineering" {
		t.Errorf("Mapping[guid-yaml] = %q, want engineering (YAML entry must survive)", got)
	}
	if got := cfg.Auth.RBAC.Mapping["guid-env"]; got != "platform" {
		t.Errorf("Mapping[guid-env] = %q, want platform (env entry must survive)", got)
	}
}

func TestLoad_NoGroupClaim_BehavesAsToday(t *testing.T) {
	cfg, err := Load(writeConfig(t, "auth:\n  mode: \"local\"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.RBAC.GroupClaim != "" {
		t.Errorf("GroupClaim = %q, want empty (unset GroupClaim => no group extraction)", cfg.Auth.RBAC.GroupClaim)
	}
}

// A whitespace-padded YAML GroupClaim (" groups ") must be trimmed at load so
// both the OIDC provider and the edge verifier receive the clean claim name.
// Without this, a padded value causes both to look for the wrong claim name,
// silently yielding nil groups (default group only) for every login.
func TestLoad_RBAC_GroupClaim_WhitespaceTrimmedAtLoad(t *testing.T) {
	body := `auth:
  mode: "local"
  rbac:
    groupclaim: "  groups  "
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.RBAC.GroupClaim != "groups" {
		t.Errorf("GroupClaim = %q, want groups (whitespace must be trimmed by normalize)", cfg.Auth.RBAC.GroupClaim)
	}
}

// A whitespace-only GroupClaim must be treated as unset (empty string) so that
// a deployment that accidentally sets groupclaim: "   " gets the "no group
// extraction" behavior rather than searching every token for a claim whose name
// is purely whitespace.
func TestLoad_RBAC_GroupClaim_WhitespaceOnlyEqualsUnset(t *testing.T) {
	body := `auth:
  mode: "local"
  rbac:
    groupclaim: "   "
`
	cfg, err := Load(writeConfig(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Auth.RBAC.GroupClaim != "" {
		t.Errorf("GroupClaim = %q, want empty (whitespace-only GroupClaim must normalize to empty)", cfg.Auth.RBAC.GroupClaim)
	}
}
