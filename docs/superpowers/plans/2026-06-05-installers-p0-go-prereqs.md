# Installers P0 — Go Prerequisites Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the two load-bearing Go capabilities the installers depend on — a configurable config-file path (`--config` flag + `ARBITRON_CONFIG` env) and direct HTTPS serving (`server.tls` with BYO cert/key or autocert).

**Architecture:** Keep the testable logic in small pure functions — `resolveConfigPath` (path precedence) and `buildTLSConfig` (TLS config + autocert manager) — each unit-tested, with thin cobra/`http.Server` glue around them. Config validation follows the existing `validateAuth` fail-fast precedent in `adapter/config/loader.go`.

**Tech Stack:** Go 1.25, cobra, koanf, testify, `crypto/tls`, `golang.org/x/crypto/acme/autocert` (new dependency).

**Scope:** This is plan P0 of the installers feature (design: `docs/superpowers/specs/2026-06-05-installers-design.md`). The installer scripts (P1–P5) are separate plans and are not covered here. The native Windows-service awareness (design §Required Go changes #3) is **NOT** delivered by P0 — it is owned by **P4 Task 0** (`installers-implementation-phase-4-task-0.md`), the phase where it can be validated on a Windows runner. (P3, the *local* Windows installer, uses a Scheduled Task and needs no SCM awareness; only P4's shared Windows Service does.)

---

## File structure

- `cmd/arbitron/configpath.go` (new) — `resolveConfigPath` (pure) + `configPathFromCmd` (cobra glue).
- `cmd/arbitron/configpath_test.go` (new) — unit tests for `resolveConfigPath`.
- `cmd/arbitron/main.go` (modify) — extract `newRootCmd()`, add persistent `--config` flag, thread the resolved path into `initDomain`, wire TLS into `runServe`.
- `cmd/arbitron/main_test.go` (modify) — assert the `--config` flag is registered.
- `cmd/arbitron/tls.go` (new) — `buildTLSConfig` (pure-ish: builds `*tls.Config` + `*autocert.Manager`).
- `cmd/arbitron/tls_test.go` (new) — unit tests with a self-signed cert helper.
- `domain/config/config.go` (modify) — add `TLSConfig` struct + `TLS` field on `ServerConfig`.
- `adapter/config/loader.go` (modify) — TLS defaults, commented YAML block, `validateTLS`.
- `adapter/config/loader_test.go` (modify) — `validateTLS` table tests.
- `go.mod` / `go.sum` (modify) — add `golang.org/x/crypto`.

---

## Task 1: Configurable config path — pure resolver

**Files:**
- Create: `cmd/arbitron/configpath.go`
- Test: `cmd/arbitron/configpath_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/arbitron/configpath_test.go`:

```go
package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveConfigPath(t *testing.T) {
	home := "/home/u"
	def := filepath.Join(home, ".arbitron", "config.yaml")

	// Flag wins over env and default.
	assert.Equal(t, "/flag/c.yaml", resolveConfigPath("/flag/c.yaml", "/env/c.yaml", home))
	// Env wins over default when no flag.
	assert.Equal(t, "/env/c.yaml", resolveConfigPath("", "/env/c.yaml", home))
	// Default when neither set.
	assert.Equal(t, def, resolveConfigPath("", "", home))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/arbitron/ -run TestResolveConfigPath`
Expected: FAIL — `undefined: resolveConfigPath`.

- [ ] **Step 3: Write minimal implementation**

Create `cmd/arbitron/configpath.go`:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// resolveConfigPath picks the config-file path by precedence:
// --config flag > ARBITRON_CONFIG env > ~/.arbitron/config.yaml default.
func resolveConfigPath(flagVal, envVal, homeDir string) string {
	if flagVal != "" {
		return flagVal
	}
	if envVal != "" {
		return envVal
	}
	return filepath.Join(homeDir, ".arbitron", "config.yaml")
}

// configPathFromCmd resolves the effective config path for a cobra command,
// reading the --config flag and the ARBITRON_CONFIG env var.
func configPathFromCmd(cmd *cobra.Command) (string, error) {
	flagVal, _ := cmd.Flags().GetString("config")
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determining home directory: %w", err)
	}
	return resolveConfigPath(flagVal, os.Getenv("ARBITRON_CONFIG"), home), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/arbitron/ -run TestResolveConfigPath`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/arbitron/configpath.go cmd/arbitron/configpath_test.go
git commit -m "feat(cli): config-path resolver (--config / ARBITRON_CONFIG)"
```

---

## Task 2: Wire `--config` into the root command and `initDomain`

**Files:**
- Modify: `cmd/arbitron/main.go`
- Test: `cmd/arbitron/main_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/arbitron/main_test.go` (the `require` import is already present):

```go
// The root command exposes a persistent --config flag (default empty), used to
// override the config-file path for system/service installs.
func TestRootCommand_HasConfigFlag(t *testing.T) {
	f := newRootCmd().PersistentFlags().Lookup("config")
	require.NotNil(t, f, "--config persistent flag must be registered on root")
	assert.Equal(t, "", f.DefValue, "--config default must be empty")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/arbitron/ -run TestRootCommand_HasConfigFlag`
Expected: FAIL — `undefined: newRootCmd`.

- [ ] **Step 3: Extract `newRootCmd()` and add the flag**

In `cmd/arbitron/main.go`, replace the existing `main()` function:

```go
func main() {
	rootCmd := &cobra.Command{
		Use:   "arbitron",
		Short: "Arbitron workflow orchestration engine",
		Long:  "Arbitron is a Go-based workflow orchestration engine with hexagonal architecture.",
	}

	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
```

with:

```go
func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd builds the root command with its subcommands and the persistent
// --config flag. Factored out of main() so tests can inspect it.
func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "arbitron",
		Short: "Arbitron workflow orchestration engine",
		Long:  "Arbitron is a Go-based workflow orchestration engine with hexagonal architecture.",
	}

	rootCmd.PersistentFlags().String("config", "",
		"path to config.yaml (overrides ARBITRON_CONFIG and the default ~/.arbitron/config.yaml)")

	rootCmd.AddCommand(newServeCmd())
	rootCmd.AddCommand(newMCPCmd())

	return rootCmd
}
```

- [ ] **Step 4: Change `initDomain` to take the config path**

In `cmd/arbitron/main.go`, change the signature and remove the hardcoded home-dir resolution. Replace:

```go
func initDomain(_ context.Context) (*domainStack, error) {
	// 1. Load configuration.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("determining home directory: %w", err)
	}
	configPath := filepath.Join(homeDir, ".arbitron", "config.yaml")

	slog.Info("loading configuration", "path", configPath)
	cfg, err := configadapter.Load(configPath)
```

with:

```go
func initDomain(_ context.Context, configPath string) (*domainStack, error) {
	// 1. Load configuration from the resolved path.
	slog.Info("loading configuration", "path", configPath)
	cfg, err := configadapter.Load(configPath)
```

- [ ] **Step 5: Update both `initDomain` call sites**

In `runServe`, replace:

```go
	ds, err := initDomain(ctx)
	if err != nil {
		return err
	}
```

with:

```go
	configPath, err := configPathFromCmd(cmd)
	if err != nil {
		return err
	}
	ds, err := initDomain(ctx, configPath)
	if err != nil {
		return err
	}
```

In `runMCP`, replace:

```go
	ds, err := initDomain(ctx)
	if err != nil {
		return err
	}
```

with:

```go
	configPath, err := configPathFromCmd(cmd)
	if err != nil {
		return err
	}
	ds, err := initDomain(ctx, configPath)
	if err != nil {
		return err
	}
```

- [ ] **Step 6: Verify build and tests pass**

Run: `go build ./... && go test ./cmd/arbitron/ -run 'TestRootCommand_HasConfigFlag|TestResolveConfigPath'`
Expected: build succeeds; PASS.

Note: if the compiler reports `filepath` imported and not used in `main.go`, confirm whether other code in the file still uses it (it does — e.g. `filepath.Abs`). Do not remove the import unless the compiler flags it.

- [ ] **Step 7: Commit**

```bash
git add cmd/arbitron/main.go cmd/arbitron/main_test.go
git commit -m "feat(cli): thread --config/ARBITRON_CONFIG into initDomain"
```

---

## Task 3: TLS config fields + fail-fast validation

**Files:**
- Modify: `domain/config/config.go`
- Modify: `adapter/config/loader.go`
- Test: `adapter/config/loader_test.go`

- [ ] **Step 1: Write the failing test**

Add to `adapter/config/loader_test.go`:

```go
// writeConfigFile writes YAML to a temp config path and returns it.
func writeConfigFile(t *testing.T, yaml string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(p, []byte(yaml), 0600))
	return p
}

func TestLoad_TLSValidation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "disabled is ok",
			yaml: "server:\n  tls:\n    enabled: false\n",
		},
		{
			name: "cert and key ok",
			yaml: "server:\n  tls:\n    enabled: true\n    certfile: /etc/a/cert.pem\n    keyfile: /etc/a/key.pem\n",
		},
		{
			name:    "only certfile is an error",
			yaml:    "server:\n  tls:\n    enabled: true\n    certfile: /etc/a/cert.pem\n",
			wantErr: "certfile and keyfile",
		},
		{
			name: "autocert with cache dir ok",
			yaml: "server:\n  tls:\n    enabled: true\n    autocertdomain: a.example\n    autocertcachedir: /var/lib/arbitron/acme\n",
		},
		{
			name:    "autocert without cache dir is an error",
			yaml:    "server:\n  tls:\n    enabled: true\n    autocertdomain: a.example\n",
			wantErr: "autocertcachedir",
		},
		{
			name:    "cert and autocert together is an error",
			yaml:    "server:\n  tls:\n    enabled: true\n    certfile: /c\n    keyfile: /k\n    autocertdomain: a.example\n    autocertcachedir: /cache\n",
			wantErr: "either",
		},
		{
			name:    "enabled with nothing is an error",
			yaml:    "server:\n  tls:\n    enabled: true\n",
			wantErr: "requires",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adapterconfig.Load(writeConfigFile(t, tc.yaml))
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./adapter/config/ -run TestLoad_TLSValidation`
Expected: FAIL — the `server.tls` keys don't unmarshal yet (no `TLS` field) and `validateTLS` doesn't exist, so error cases return no error.

- [ ] **Step 3: Add the `TLSConfig` struct**

In `domain/config/config.go`, add the `TLS` field to `ServerConfig` (insert after the `PublicBaseURL` field, before the closing brace):

```go
	// TLS configures direct HTTPS serving. When Enabled, exactly one of
	// {CertFile+KeyFile} or {AutocertDomain} must be set. Disabled (default) =
	// plain HTTP, intended for deployments behind a TLS-terminating tunnel/proxy.
	TLS TLSConfig
```

Then add the struct definition immediately after the `ServerConfig` struct:

```go
// TLSConfig configures direct HTTPS serving by the arbitron HTTP server.
type TLSConfig struct {
	// Enabled turns on HTTPS. When false, the server listens on plain HTTP.
	Enabled bool
	// CertFile / KeyFile are paths to a PEM cert/key pair (BYO or internal CA).
	CertFile string
	KeyFile  string
	// AutocertDomain enables automatic Let's Encrypt for this hostname; mutually
	// exclusive with CertFile/KeyFile. Requires AutocertCacheDir for cert storage.
	AutocertDomain   string
	AutocertCacheDir string
}
```

- [ ] **Step 4: Add TLS defaults and validation in the loader**

In `adapter/config/loader.go`, add these entries to the map returned by `defaults()` (alongside the other `server.*` keys):

```go
		"server.tls.enabled":          false,
		"server.tls.certfile":         "",
		"server.tls.keyfile":          "",
		"server.tls.autocertdomain":   "",
		"server.tls.autocertcachedir": "",
```

In the `Load` function, add the TLS validation right after the existing `validateAuth` call:

```go
	if err := validateTLS(&cfg); err != nil {
		return nil, err
	}
```

Add the `validateTLS` function near `validateAuth`:

```go
// validateTLS enforces the direct-HTTPS contract fail-fast at load: when TLS is
// enabled, exactly one of {certfile+keyfile} or {autocertdomain} must be set,
// and autocert requires a cache directory. Mirrors the validateAuth precedent.
func validateTLS(cfg *domainconfig.Config) error {
	t := cfg.Server.TLS
	if !t.Enabled {
		return nil
	}
	hasCert := t.CertFile != ""
	hasKey := t.KeyFile != ""
	hasAutocert := t.AutocertDomain != ""
	if hasCert != hasKey {
		return fmt.Errorf("server.tls.enabled requires both certfile and keyfile")
	}
	if hasCert && hasAutocert {
		return fmt.Errorf("server.tls: set either cert/key or autocertdomain, not both")
	}
	if !hasCert && !hasAutocert {
		return fmt.Errorf("server.tls.enabled requires cert/key or autocertdomain")
	}
	if hasAutocert && t.AutocertCacheDir == "" {
		return fmt.Errorf("server.tls autocertdomain requires autocertcachedir")
	}
	return nil
}
```

- [ ] **Step 5: Document TLS in the default YAML**

In `adapter/config/loader.go`, in `defaultYAMLContent()`, extend the `server:` block by inserting this commented documentation directly after the `publicbaseurl` comment lines (before the `database:` line):

```go
  # tls:
  #   enabled: false
  #     Serve HTTPS directly. Leave false when behind a TLS-terminating
  #     tunnel/proxy (e.g. cloudflared); set true to serve HTTPS from arbitron.
  #   certfile: ""   # PEM cert path (BYO / internal CA). Requires keyfile.
  #   keyfile:  ""   # PEM key path.
  #   autocertdomain: ""    # automatic Let's Encrypt for this host (mutually
  #                         # exclusive with certfile/keyfile). Needs public DNS
  #                         # and reachable :80/:443.
  #   autocertcachedir: ""  # directory to cache autocert certificates.
```

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./adapter/config/ -run TestLoad_TLSValidation`
Expected: PASS (all subtests).

- [ ] **Step 7: Run the full config + existing suites to confirm no regression**

Run: `go test ./adapter/config/ ./domain/config/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add domain/config/config.go adapter/config/loader.go adapter/config/loader_test.go
git commit -m "feat(config): server.tls block with fail-fast validation"
```

---

## Task 4: `buildTLSConfig` helper + autocert dependency

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `cmd/arbitron/tls.go`
- Test: `cmd/arbitron/tls_test.go`

- [ ] **Step 1: Add the autocert dependency**

Run: `go get golang.org/x/crypto/acme/autocert@latest`
Expected: `go.mod` now requires `golang.org/x/crypto`.

- [ ] **Step 2: Write the failing test**

Create `cmd/arbitron/tls_test.go`:

```go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/arbitron/arbitron/domain/config"
)

// genSelfSigned writes a throwaway self-signed cert/key pair to temp files.
func genSelfSigned(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31-1, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM, err := os.Create(certPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der}))
	require.NoError(t, certPEM.Close())

	keyBytes, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM, err := os.Create(keyPath)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(keyPEM, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}))
	require.NoError(t, keyPEM.Close())

	return certPath, keyPath
}

func TestBuildTLSConfig_Disabled(t *testing.T) {
	tc, mgr, err := buildTLSConfig(config.TLSConfig{Enabled: false})
	require.NoError(t, err)
	assert.Nil(t, tc)
	assert.Nil(t, mgr)
}

func TestBuildTLSConfig_CertKey(t *testing.T) {
	cert, key := genSelfSigned(t)
	tc, mgr, err := buildTLSConfig(config.TLSConfig{Enabled: true, CertFile: cert, KeyFile: key})
	require.NoError(t, err)
	require.NotNil(t, tc)
	assert.Len(t, tc.Certificates, 1)
	assert.Nil(t, mgr)
}

func TestBuildTLSConfig_Autocert(t *testing.T) {
	tc, mgr, err := buildTLSConfig(config.TLSConfig{
		Enabled:          true,
		AutocertDomain:   "a.example",
		AutocertCacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.NotNil(t, tc)
	require.NotNil(t, mgr)
}

func TestBuildTLSConfig_BadKeypair(t *testing.T) {
	tc, mgr, err := buildTLSConfig(config.TLSConfig{
		Enabled:  true,
		CertFile: "/nope/cert.pem",
		KeyFile:  "/nope/key.pem",
	})
	require.Error(t, err)
	assert.Nil(t, tc)
	assert.Nil(t, mgr)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/arbitron/ -run TestBuildTLSConfig`
Expected: FAIL — `undefined: buildTLSConfig`.

- [ ] **Step 4: Write the implementation**

Create `cmd/arbitron/tls.go`:

```go
package main

import (
	"crypto/tls"
	"fmt"

	"golang.org/x/crypto/acme/autocert"

	"github.com/arbitron/arbitron/domain/config"
)

// buildTLSConfig translates the server TLS config into a *tls.Config and, when
// autocert is used, the *autocert.Manager whose HTTP-01 handler must be served
// on :80. Returns (nil, nil, nil) when TLS is disabled (plain HTTP). The config
// loader has already validated that exactly one mode is configured.
func buildTLSConfig(c config.TLSConfig) (*tls.Config, *autocert.Manager, error) {
	if !c.Enabled {
		return nil, nil, nil
	}
	if c.CertFile != "" && c.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("loading TLS keypair: %w", err)
		}
		return &tls.Config{Certificates: []tls.Certificate{cert}}, nil, nil
	}
	if c.AutocertDomain != "" {
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(c.AutocertDomain),
			Cache:      autocert.DirCache(c.AutocertCacheDir),
		}
		return m.TLSConfig(), m, nil
	}
	return nil, nil, fmt.Errorf("tls enabled but neither cert/key nor autocert configured")
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./cmd/arbitron/ -run TestBuildTLSConfig`
Expected: PASS (all subtests).

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/arbitron/tls.go cmd/arbitron/tls_test.go
git commit -m "feat(cli): buildTLSConfig (BYO cert / autocert) helper"
```

---

## Task 5: Wire TLS into `runServe`

**Files:**
- Modify: `cmd/arbitron/main.go`

This task is integration glue around the unit-tested `buildTLSConfig`; it is verified by build + full suite + a manual HTTPS smoke test (a real TLS listener can't be meaningfully unit-tested without binding a port).

- [ ] **Step 1: Build the TLS config before constructing the listener goroutine**

In `cmd/arbitron/main.go` `runServe`, immediately after the `srv := &http.Server{...}` block, add:

```go
	tlsConf, acMgr, err := buildTLSConfig(ds.cfg.Server.TLS)
	if err != nil {
		return fmt.Errorf("configuring TLS: %w", err)
	}
	srv.TLSConfig = tlsConf
```

- [ ] **Step 2: Replace the serve goroutine to branch on TLS**

Replace this block:

```go
	errCh := make(chan error, 1)
	go func() {
		ds.logger.Info("server starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server error: %w", err)
		}
		close(errCh)
	}()
```

with:

```go
	errCh := make(chan error, 1)
	go func() {
		if acMgr != nil {
			// ACME HTTP-01 challenges must be reachable on :80.
			go func() {
				challengeSrv := &http.Server{Addr: ":80", Handler: acMgr.HTTPHandler(nil)}
				if cerr := challengeSrv.ListenAndServe(); cerr != nil && !errors.Is(cerr, http.ErrServerClosed) {
					ds.logger.Error("ACME challenge server error", "error", cerr)
				}
			}()
		}
		ds.logger.Info("server starting", "addr", addr, "tls", tlsConf != nil)
		var serveErr error
		if tlsConf != nil {
			// Cert/key (or autocert) come from srv.TLSConfig, so the file
			// arguments are empty.
			serveErr = srv.ListenAndServeTLS("", "")
		} else {
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("server error: %w", serveErr)
		}
		close(errCh)
	}()
```

- [ ] **Step 3: Build and run the full test suite**

Run: `go build ./... && go test ./...`
Expected: build succeeds; all packages PASS.

- [ ] **Step 4: Manual HTTPS smoke test**

```bash
# Generate a throwaway cert for localhost.
openssl req -x509 -newkey rsa:2048 -nodes -keyout /tmp/arb-key.pem \
  -out /tmp/arb-cert.pem -days 1 -subj "/CN=localhost" >/dev/null 2>&1

# Point a temp config at it and serve.
mkdir -p /tmp/arbtls
cat > /tmp/arbtls/config.yaml <<'YAML'
server:
  host: "127.0.0.1"
  port: 8443
  tls:
    enabled: true
    certfile: /tmp/arb-cert.pem
    keyfile: /tmp/arb-key.pem
database:
  path: /tmp/arbtls/arbitron.db
workspace:
  path: /tmp/arbtls/workspaces
logging:
  file: /tmp/arbtls/arbitron.log
YAML

go run ./cmd/arbitron serve --config /tmp/arbtls/config.yaml &
sleep 3
curl -sk https://127.0.0.1:8443/ -o /dev/null -w "TLS OK: %{http_code}\n"
kill %1
```

Expected: `TLS OK: 200` (or a non-zero HTTP status from the dashboard handler), served over HTTPS. Confirms the cert/key path end-to-end. Then plain HTTP still works when `tls.enabled` is false.

- [ ] **Step 5: Commit**

```bash
git add cmd/arbitron/main.go
git commit -m "feat(cli): serve HTTPS directly when server.tls configured"
```

---

## Self-review

**Spec coverage (design §Required Go changes):**
- #1 Configurable config path → Tasks 1–2. ✓
- #2 Direct TLS serving (config block + validation + cert/key + autocert) → Tasks 3–5. ✓
- #3 Native Windows-service awareness → NOT shipped here; owned by **P4 Task 0** (noted in Scope). ✓ (intentional non-goal for P0; P4 delivers the build-tagged `service_windows.go` + `svc.Run` dispatch)

**Placeholder scan:** No TBD/TODO; every code step has complete code; every command has expected output. ✓

**Type consistency:**
- `resolveConfigPath(flagVal, envVal, homeDir string) string` — defined Task 1, used by `configPathFromCmd` (Task 1) which is called in Task 2. ✓
- `initDomain(ctx, configPath string)` — new signature (Task 2) matches both call sites updated in Task 2. ✓
- `config.TLSConfig{Enabled, CertFile, KeyFile, AutocertDomain, AutocertCacheDir}` — defined Task 3, used identically in Task 4 tests/impl and Task 5 wiring. ✓
- `buildTLSConfig(config.TLSConfig) (*tls.Config, *autocert.Manager, error)` — defined Task 4, called in Task 5 with the same 3-value return. ✓
- `newRootCmd()` — defined Task 2, asserted in the Task 2 test. ✓

**Notes for the implementer:** Tasks are ordered by dependency; do them in sequence. Task 5 has no unit test by design (real TLS listener) — the manual smoke test is the gate, plus the full `go test ./...` for regressions.
