# Phase 1, Task 1: AuthConfig + Mode + koanf Loading & Validation

## Goal

Add an `AuthConfig` to the domain config (`domain/config/config.go`) carrying the operating `Mode` (`local` / `org`), OIDC sub-config, session sub-config, and a bootstrap-admins list. Load it through the existing koanf loader (`adapter/config/loader.go`) with defaults, a documented YAML block, and env-var overrides, and add fail-fast validation: in `org` mode the OIDC issuer/clientID/clientSecret/redirectURL must be non-empty. Local mode (the default) requires no auth config and must load exactly as it does today.

Source of truth: `docs/superpowers/specs/arbitron-phase6-design-phase-1.md` §1.

## Dependencies

None. This is the first task in the phase. Tasks 2–8 read `cfg.Auth.Mode`, `cfg.Auth.OIDC`, and `cfg.Auth.Session`, so this must land first.

## Background (current state)

- `domain/config/config.go` defines the top-level `Config` struct (`Server`, `Database`, `Workspace`, `LLM`, `Logging`, `Tracing`, `Queue`, `Script`, `Notifications`, `Profiles`). There is no auth field today.
- `adapter/config/loader.go` is the koanf loader. It builds a flat `defaults()` map, writes a `defaultYAMLContent()` file on first run, layers env vars (prefix `ARBITRON_`, `_`→`.`), unmarshals into `domainconfig.Config`, then calls `normalize(&cfg)`. The fail-fast precedent is the `llm.timeoutseconds` floor in `normalize` plus `DefaultTimeoutSeconds`.
- `adapter/config/loader_test.go` contains the existing table of load tests (`TestLoad_Defaults`, `TestLoad_YAMLOverride`, `TestLoad_EnvVarOverride`, `TestLoad_TimeoutSecondsFloor`, …). New tests are added here.
- `Load` returns `(*domainconfig.Config, error)`. Today the only error paths are I/O / parse / unmarshal. Validation that returns an error from `Load` is new for this task (the timeout floor only *clamps*; org-mode validation must *reject*).

## Files to Create/Modify

### Modify: `domain/config/config.go`

Add `Auth AuthConfig` to `Config` and define the new types:

```go
type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	Workspace     WorkspaceConfig
	LLM           LLMConfig
	Logging       LoggingConfig
	Tracing       TracingConfig
	Queue         QueueConfig
	Script        ScriptConfig
	Notifications NotificationsConfig
	Profiles      []ProfileConfig
	Auth          AuthConfig
}

// AuthConfig selects the operating mode and holds the org-mode auth settings.
// Local mode (the default) requires none of the sub-configs.
type AuthConfig struct {
	// Mode is "local" (default) or "org". It drives every downstream auth branch:
	// local mode injects a fixed admin identity; org mode validates a session.
	Mode string
	// BootstrapAdmins lists identity subjects/emails provisioned as admins on
	// first login. Declared here so the config shape is complete; CONSUMED in
	// Phase 2 (JIT provisioning). Phase 1 reads but does not enforce it.
	BootstrapAdmins []string
	OIDC            OIDCConfig
	Session         SessionConfig
}

// OIDCConfig configures the self-hosted OIDC authorization-code login flow.
// All fields are required when Mode == "org".
type OIDCConfig struct {
	IssuerURL    string   // e.g. "https://login.example.com" (OIDC discovery root)
	ClientID     string
	ClientSecret string   // SECURITY: sensitive; never echoed on a read surface
	RedirectURL  string   // must equal PublicBaseURL + "/auth/callback"
	Scopes       []string // default openid,profile,email
}

// SessionConfig configures server-side cookie sessions (the session is the
// Arbitron credential; OIDC is only the login mechanism).
type SessionConfig struct {
	TTLSeconds   int    // session lifetime; default 43200 (12h)
	CookieName   string // default "arbitron_session"
	CookieSecure bool   // default true; Secure attribute on the session cookie
}

// Mode constants kept as strings here to avoid a domain/identity import cycle
// (domain/identity defines the typed Mode and converts from this string).
const (
	AuthModeLocal = "local"
	AuthModeOrg   = "org"
)
```

### Modify: `adapter/config/loader.go`

1. Add the auth defaults to the `defaults()` map (koanf flat keys, all lowercase):

```go
"auth.mode":                "local",
"auth.bootstrapadmins":     []string{},
"auth.oidc.issuerurl":      "",
"auth.oidc.clientid":       "",
"auth.oidc.clientsecret":   "",
"auth.oidc.redirecturl":    "",
"auth.oidc.scopes":         []string{"openid", "profile", "email"},
"auth.session.ttlseconds":  43200, // 12h
"auth.session.cookiename":  "arbitron_session",
"auth.session.cookiesecure": true,
```

2. Add a commented `auth:` block to `defaultYAMLContent()` documenting org-mode setup (mirror the existing commented `notifications.teams` style). Example skeleton:

```yaml
# auth:
#   mode: "local"            # "local" (default, single auto-admin) or "org"
#   bootstrapadmins: []      # subjects/emails provisioned as admin on first login (Phase 2)
#   oidc:
#     issuerurl: ""          # OIDC discovery root; REQUIRED in org mode
#     clientid: ""           # REQUIRED in org mode
#     clientsecret: ""       # REQUIRED in org mode; SECURITY: sensitive, never echoed
#     redirecturl: ""        # REQUIRED in org mode; = publicbaseurl + "/auth/callback"
#     scopes: ["openid", "profile", "email"]
#   session:
#     ttlseconds: 43200      # 12h
#     cookiename: "arbitron_session"
#     cookiesecure: true     # set false ONLY for local http dev
```

3. Add CSV env-var overlays for the two slice fields, mirroring the existing `ARBITRON_SCRIPT_ENVPASSTHROUGH` overlay (the env provider delivers a comma string, not a slice):

```go
if raw := os.Getenv("ARBITRON_AUTH_BOOTSTRAPADMINS"); raw != "" {
	if err := k.Load(confmap.Provider(map[string]interface{}{
		"auth.bootstrapadmins": splitCSV(raw),
	}, "."), nil); err != nil {
		return nil, fmt.Errorf("loading auth.bootstrapadmins: %w", err)
	}
}
if raw := os.Getenv("ARBITRON_AUTH_OIDC_SCOPES"); raw != "" {
	if err := k.Load(confmap.Provider(map[string]interface{}{
		"auth.oidc.scopes": splitCSV(raw),
	}, "."), nil); err != nil {
		return nil, fmt.Errorf("loading auth.oidc.scopes: %w", err)
	}
}
```

4. Extend `normalize` to floor the session TTL (mirror the timeout floor — a zero/negative TTL would mint never-expiring or instantly-expired sessions) and default the cookie name:

```go
const DefaultSessionTTLSeconds = 43200 // 12h

func normalize(cfg *domainconfig.Config) {
	if cfg.LLM.TimeoutSeconds <= 0 {
		cfg.LLM.TimeoutSeconds = DefaultTimeoutSeconds
	}
	cfg.Server.PublicBaseURL = strings.TrimRight(cfg.Server.PublicBaseURL, "/")
	cfg.Auth.Mode = strings.ToLower(strings.TrimSpace(cfg.Auth.Mode))
	if cfg.Auth.Mode == "" {
		cfg.Auth.Mode = domainconfig.AuthModeLocal
	}
	if cfg.Auth.Session.TTLSeconds <= 0 {
		cfg.Auth.Session.TTLSeconds = DefaultSessionTTLSeconds
	}
	if cfg.Auth.Session.CookieName == "" {
		cfg.Auth.Session.CookieName = "arbitron_session"
	}
}
```

5. Add a `validateAuth` call inside `Load` after `normalize`, returning an error (the first non-clamping validation in `Load`):

```go
// after normalize(&cfg)
if err := validateAuth(&cfg); err != nil {
	return nil, err
}
return &cfg, nil
```

```go
// validateAuth enforces the org-mode contract: a deployment that turns on org
// mode without a complete OIDC config cannot perform a single login, so we
// fail fast at load rather than 500 on the first /auth/login. Mirrors the
// timeout-floor fail-fast precedent. Local mode (and any unknown mode, which
// normalize cannot produce) requires nothing.
func validateAuth(cfg *domainconfig.Config) error {
	switch cfg.Auth.Mode {
	case domainconfig.AuthModeLocal:
		return nil
	case domainconfig.AuthModeOrg:
		var missing []string
		if strings.TrimSpace(cfg.Auth.OIDC.IssuerURL) == "" {
			missing = append(missing, "auth.oidc.issuerurl")
		}
		if strings.TrimSpace(cfg.Auth.OIDC.ClientID) == "" {
			missing = append(missing, "auth.oidc.clientid")
		}
		if strings.TrimSpace(cfg.Auth.OIDC.ClientSecret) == "" {
			missing = append(missing, "auth.oidc.clientsecret")
		}
		if strings.TrimSpace(cfg.Auth.OIDC.RedirectURL) == "" {
			missing = append(missing, "auth.oidc.redirecturl")
		}
		if len(missing) > 0 {
			return fmt.Errorf("org mode requires OIDC config; missing: %s", strings.Join(missing, ", "))
		}
		return nil
	default:
		return fmt.Errorf("invalid auth.mode %q (want %q or %q)", cfg.Auth.Mode, domainconfig.AuthModeLocal, domainconfig.AuthModeOrg)
	}
}
```

### Modify (optional, scope-noted): `domain/config/redact.go`

Add the auth mode to `RedactedConfig` so `GET /system/config` surfaces the mode without leaking the client secret. SECURITY: expose ONLY `Auth.Mode` and a boolean `OIDCConfigured`; NEVER `ClientSecret`. Add:

```go
type RedactedAuth struct {
	Mode           string `json:"mode"`
	OIDCConfigured bool   `json:"oidcConfigured"`
}
```

and populate it in `Redact` (`OIDCConfigured: cfg.Auth.OIDC.ClientSecret != ""`). The allow-list shape guarantees the secret can never be serialized.

## Testing (strict TDD — five scenario categories)

All tests in `adapter/config/loader_test.go` (+ a `domain/config` test for the secret-redaction guard). Write first, then implement.

- **Happy path** — `TestLoad_AuthDefaults`: a fresh load (no env) yields `cfg.Auth.Mode == "local"`, `cfg.Auth.Session.CookieName == "arbitron_session"`, `cfg.Auth.Session.TTLSeconds == 43200`, `cfg.Auth.Session.CookieSecure == true`, `cfg.Auth.OIDC.Scopes == ["openid","profile","email"]`, and `Load` returns no error.
- **Happy path (org)** — `TestLoad_AuthOrgMode_Valid`: a YAML file with `mode: org` and all four OIDC fields set loads without error and the fields round-trip onto `cfg.Auth.OIDC`.
- **Input validation** — `TestLoad_AuthMode_Normalized`: `mode: "ORG"` / `" org "` normalize to `"org"`; an unknown `mode: "cloud"` returns an error from `Load` mentioning `invalid auth.mode`.
- **Error/failure** — `TestLoad_AuthOrgMode_MissingOIDC`: `mode: org` with empty `clientsecret` (and another with empty `issuerurl`) makes `Load` return a non-nil error naming the missing key(s); the returned `*Config` is nil.
- **Boundary** — `TestLoad_AuthSessionTTLFloor`: `ttlseconds: 0` and `ttlseconds: -1` both floor to `DefaultSessionTTLSeconds`; `ttlseconds: 1` is preserved (the floor only clamps non-positive).
- **Isolation/security** — `TestLoad_AuthBootstrapAdmins_FromEnv`: `ARBITRON_AUTH_BOOTSTRAPADMINS="a@x.com, b@x.com"` overlays as a two-element slice (CSV split, empties dropped); and `TestRedact_NeverLeaksClientSecret` (in `domain/config`): a `Config` with a non-empty `OIDC.ClientSecret` projected via `Redact` produces JSON containing no substring of the secret and `oidcConfigured: true`.

## Acceptance Criteria

- `Config.Auth` exists with `Mode`, `BootstrapAdmins`, `OIDC`, `Session`; `AuthModeLocal`/`AuthModeOrg` constants defined.
- `Load` returns a non-nil error for org mode with any missing OIDC field, and for an unknown mode; local mode loads unchanged.
- Session TTL floors and cookie-name default applied in `normalize`.
- The OIDC client secret never appears in `RedactedConfig` output.
- `go build ./...` and `go test ./adapter/config/... ./domain/config/...` pass.
