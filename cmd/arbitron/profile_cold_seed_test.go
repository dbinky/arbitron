package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/modelcatalog"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No prior initDomain, catalog refresh, candidate injection or seed retry: the
// configured profiles must be ready for execution on the very first boot.
// Only TLS socket I/O is replaced, as in the native pinned execution tests.
func TestColdFollowingSeedFirstBoot(t *testing.T) {
	const selected = "deepseek/deepseek-v4.1-flash"
	for _, fault := range []string{"confirmed", "empty catalog", "wrong model", "fetch failure", "inventory failure", "commit failure"} {
		t.Run(fault, func(t *testing.T) {
			ctx := context.Background()
			key := bytes.Repeat([]byte{0x5d}, 32)
			t.Setenv("ARBITRON_MASTER_KEY", base64.StdEncoding.EncodeToString(key))
			root := t.TempDir()
			dbPath, workspace := filepath.Join(root, "arbitron.db"), filepath.Join(root, "workspaces")
			require.NoError(t, os.MkdirAll(workspace, 0o700))
			writeWorkflow(t, workspace, "cold-seed", `{"name":"cold-seed","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"cold","templateFile":"templates/p.md"}}],"edges":[]}`)
			writePinTemplate(t, workspace, "cold-seed")
			// Connections are configured independently of profile initialization. Start
			// with no profile, catalog, proof or seed-journal rows.
			db, err := adaptsqlite.Open(dbPath)
			require.NoError(t, err)
			require.NoError(t, adaptsqlite.RunMigrations(db))
			crypto, err := adaptcrypto.NewAESGCM(key)
			require.NoError(t, err)
			if fault != "inventory failure" {
				require.NoError(t, adaptsqlite.NewConnectionRepo(db, crypto).Create(ctx, provider.Connection{Name: "cold-route", Provider: "openrouter", BaseURL: "https://cold-provider.example.com"}, "cold-key"))
			}
			if fault == "commit failure" {
				_, err = db.Exec(`CREATE TRIGGER cold_commit_fault BEFORE INSERT ON model_usability_evidence BEGIN SELECT RAISE(ABORT,'cold disk fault'); END`)
				require.NoError(t, err)
			}
			require.NoError(t, db.Close())
			configPath := filepath.Join(root, "config.yaml")
			config := fmt.Sprintf(`auth:
  mode: local
database:
  path: %s
workspace:
  path: %s
samples:
  seed: false
logging:
  level: error
  file: %s
profiles:
  - alias: cold
    kind: chat
    connectionref: cold-route
    selection_mode: following
    follow_target: {provider: openrouter, tier: flash}
  - alias: explicit
    kind: chat
    connectionref: cold-route
    model: deepseek/deepseek-v4-flash
    selection_mode: following
    follow_target: {provider: openrouter, tier: flash}
`, dbPath, workspace, filepath.Join(root, "log"))
			require.NoError(t, os.WriteFile(configPath, []byte(config), 0o600))
			var mu sync.Mutex
			var models []string
			fetches := 0
			offline := false
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Host {
				case "models.dev":
					fetches++
					if offline || fault == "fetch failure" {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					if r.Method != http.MethodGet || r.URL.Path != "/api.json" || r.Header.Get("Accept") != "application/json" {
						http.Error(w, "wrong source request", 400)
						return
					}
					if fault == "empty catalog" {
						fmt.Fprint(w, `{}`)
						return
					}
					fmt.Fprint(w, refreshCatalogFixture)
				case "cold-provider.example.com":
					var body struct {
						Model    string
						Messages []struct{ Content string }
						Tools    []any
						Stream   bool
					}
					if offline || r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer cold-key" || json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" || len(body.Messages) == 0 || len(body.Tools) != 0 || body.Stream {
						http.Error(w, "wrong route/auth/model/prompt/tools", 400)
						return
					}
					models = append(models, body.Model)
					actual := body.Model
					if fault == "wrong model" {
						actual = "fallback"
					}
					fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"cold selected output"},"finish_reason":"stop"}]}`, actual)
				default:
					http.Error(w, "unexpected host", 400)
				}
			}))
			defer srv.Close()
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.Proxy = nil
			transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != "models.dev:443" && address != "cold-provider.example.com:443" {
					return nil, fmt.Errorf("unexpected TLS destination %s", address)
				}
				dialer := tls.Dialer{Config: srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
				return dialer.DialContext(ctx, network, srv.Listener.Addr().String())
			}
			defer transport.CloseIdleConnections()
			original := http.DefaultTransport
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = original }()
			ds, err := initDomain(ctx, configPath)
			require.NoError(t, err)
			seedBuiltStack(t, ds)
			var closeOnce sync.Once
			closeStack := func() { closeOnce.Do(func() { ds.Close(ctx) }) }
			defer closeStack()
			repo := adaptsqlite.NewProfileRepo(ds.db)
			if fault != "confirmed" {
				for _, alias := range []string{"cold", "explicit"} {
					_, err := repo.Get(ctx, alias)
					assert.ErrorIs(t, err, provider.ErrProfileNotFound)
					_, err = ds.profileResolver.Resolve(ctx, alias)
					assert.Error(t, err)
				}
				var events int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_announcement_events`).Scan(&events))
				assert.Zero(t, events)
				pending, err := repo.PendingProfileSeed(ctx, nil)
				require.NoError(t, err)
				assert.Len(t, pending, 2, "failed initialization retains the original pending aliases")
				if fault != "inventory failure" {
					mu.Lock()
					assert.Positive(t, fetches, "refusal must actually consume the failing source")
					if fault == "wrong model" || fault == "commit failure" {
						assert.NotEmpty(t, models, "refusal must reach the exact-route proof or commit boundary")
					}
					mu.Unlock()
				}
				return
			}
			stored, err := repo.Get(ctx, "cold")
			require.NoError(t, err, "cold initialization must not defer configured aliases to a later boot")
			assert.Equal(t, selected, stored.Model)
			assert.Equal(t, int64(1), stored.Revision)
			assert.Equal(t, &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}, stored.FollowTarget)
			explicit, err := repo.Get(ctx, "explicit")
			require.NoError(t, err)
			assert.Equal(t, "deepseek/deepseek-v4-flash", explicit.Model)
			view, err := ds.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			require.NotEmpty(t, view.Evidence)
			events, err := ds.db.Query(`SELECT payload,state FROM model_announcement_events`)
			require.NoError(t, err)
			defer events.Close()
			var canonicals []string
			for events.Next() {
				var raw, state string
				require.NoError(t, events.Scan(&raw, &state))
				var payload modelcatalog.AnnouncementPayload
				require.NoError(t, json.Unmarshal([]byte(raw), &payload))
				canonicals = append(canonicals, payload.CanonicalID)
				assert.NotEmpty(t, payload.EventID)
				assert.NotEmpty(t, payload.Paths)
				assert.Empty(t, payload.Changes, "bootstrap cannot advance profiles")
				assert.Equal(t, modelcatalog.EventSuppressed, state, "the webhook defaults empty")
			}
			require.NoError(t, events.Err())
			require.NoError(t, events.Close())
			assert.ElementsMatch(t, []string{"deepseek/deepseek-v4-flash", selected}, canonicals)
			mu.Lock()
			assert.Positive(t, fetches)
			models = nil
			mu.Unlock()
			run, _, err := ds.eng.Trigger(adminCtxForLocalUser(t, ds), "cold-seed", nil, "normal")
			require.NoError(t, err)
			completed := waitTerminal(t, ds, run)
			require.Equal(t, instance.StateCompleted, completed.State)
			assert.Equal(t, selected, completed.ModelPins["cold"])
			var output string
			require.NoError(t, ds.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id='ask'`, run.String()).Scan(&output))
			assert.Contains(t, output, "cold selected output")
			mu.Lock()
			assert.Equal(t, []string{selected}, models)
			before := fetches
			offline = true
			mu.Unlock()
			closeStack()
			reopened, err := initDomain(ctx, configPath)
			require.NoError(t, err)
			defer reopened.Close(ctx)
			after, err := adaptsqlite.NewProfileRepo(reopened.db).Get(ctx, "cold")
			require.NoError(t, err)
			assert.Equal(t, stored, after, "restart during outage must not reseed or revalidate working rows")
			reopenedView, err := reopened.catalogStore.Snapshot(ctx)
			require.NoError(t, err)
			assert.Equal(t, view.Evidence, reopenedView.Evidence)
			binding, err := reopened.profileResolver.Resolve(ctx, "cold")
			require.NoError(t, err)
			assert.Equal(t, selected, binding.Model)
			mu.Lock()
			assert.Equal(t, before, fetches, "completed seed snapshot must not rediscover on restart")
			mu.Unlock()
		})
	}
}
