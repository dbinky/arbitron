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
