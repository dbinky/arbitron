package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProfileMutationWiringRejectsStaleAdmin(t *testing.T) {
	for _, operation := range []string{"full", "patch", "delete"} {
		t.Run(operation, func(t *testing.T) {
			const x = "deepseek/deepseek-v4-flash"
			const y = "deepseek/deepseek-v4.1-flash"
			var mu sync.Mutex
			var models []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Stream   bool   `json:"stream"`
					Messages []struct {
						Content string `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || (body.Model != x && body.Model != y) || body.Stream || len(body.Messages) == 0 || body.Messages[0].Content == "" || r.URL.Path != "/chat/completions" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer mutation-secret" {
					http.Error(w, "missing execution inputs", http.StatusBadRequest)
					return
				}
				mu.Lock()
				models = append(models, body.Model)
				mu.Unlock()
				fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"accepted %s"}}]}`, body.Model, body.Model)
			}))
			defer upstream.Close()
			ds := bootSinkRoot(t, "", func(workspace string) {
				writeWorkflow(t, workspace, "mutation", `{"name":"mutation","nodes":[{"id":"work","type":"llm_call","entrypoint":true,"config":{"model_profile":"chat","templateFile":"templates/p.md"}}],"edges":[]}`)
				writePinTemplate(t, workspace, "mutation")
			})
			ctx := adminCtxForLocalUser(t, ds)
			crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
			require.NoError(t, err)
			conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
			require.NoError(t, conns.Create(ctx, provider.Connection{Name: "mutation-api", Provider: "openrouter", BaseURL: upstream.URL}, "mutation-secret"))
			_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "chat", Kind: provider.ProfileKindChat, ConnectionRef: "mutation-api", Model: x})
			require.NoError(t, err)
			run := func(model string) uuid.UUID {
				id, _, err := ds.eng.Trigger(ctx, "mutation", nil, "normal")
				require.NoError(t, err)
				require.Eventually(t, func() bool { p, e := ds.instanceRepo.Get(ctx, id); return e == nil && p.State.IsTerminal() }, 10*time.Second, 10*time.Millisecond)
				saved, err := ds.instanceRepo.Get(ctx, id)
				require.NoError(t, err)
				require.Equal(t, instance.StateCompleted, saved.State)
				assert.Equal(t, map[string]string{"chat": model}, saved.ModelPins)
				trace, err := ds.nodeExecRepo.ListByInstance(ctx, id)
				require.NoError(t, err)
				require.Len(t, trace, 1)
				assert.Equal(t, instance.NodeStatusSucceeded, trace[0].Status)
				assert.Contains(t, fmt.Sprint(trace[0].Output), "accepted "+model)
				return id
			}
			oldRun := run(x)
			prior, err := ds.profileSvc.Get(ctx, "chat")
			require.NoError(t, err)
			held, release, leaseDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				leaseDone <- ds.executionGate.WithRead(ctx, func(context.Context) error { close(held); <-release; return nil })
			}()
			<-held
			released := false
			defer func() {
				if !released {
					close(release)
					<-leaseDone
				}
			}()
			result := make(chan error, 1)
			go func() {
				var err error
				switch operation {
				case "full":
					p := prior.Clone()
					p.AllowedTools = []string{"Glob"}
					_, _, err = ds.profileSvc.Update(ctx, p)
				case "patch":
					tools := []string{"Glob"}
					_, _, err = ds.profileSvc.UpdatePatch(ctx, "chat", provider.ProfilePatch{AllowedTools: &tools})
				case "delete":
					err = ds.profileSvc.Delete(ctx, "chat")
				}
				result <- err
			}()
			// Wait for the actual service to finish its authoritative read and block on
			// the shared gate, rather than assuming a sleep means the read happened.
			require.Eventually(t, func() bool {
				stack := make([]byte, 1<<20)
				n := runtime.Stack(stack, true)
				for _, g := range strings.Split(string(stack[:n]), "\n\n") {
					if strings.Contains(g, "provider.(*ProfileService).") && strings.Contains(g, "provider.(*ExecutionGate).WithMutation") {
						return true
					}
				}
				return false
			}, 5*time.Second, time.Millisecond)
			// A separate DB connection does not share the in-memory gate. Its durable
			// update must survive the admin's already-read edit acquiring the lease.
			var seq int
			var databaseName, databasePath string
			require.NoError(t, ds.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &databaseName, &databasePath))
			require.NotEmpty(t, databasePath)
			externalDB, err := adaptsqlite.Open(databasePath)
			require.NoError(t, err)
			defer externalDB.Close()
			repo := adaptsqlite.NewProfileRepo(externalDB)
			external := prior.Clone()
			external.Model = y
			require.NoError(t, repo.Update(ctx, external))
			current, err := repo.Get(ctx, "chat")
			require.NoError(t, err)
			close(release)
			released = true
			require.NoError(t, <-leaseDone)
			select {
			case err := <-result:
				assert.ErrorIs(t, err, provider.ErrAdvanceConflict)
			case <-time.After(5 * time.Second):
				t.Fatal("admin mutation did not finish")
			}
			saved, err := repo.Get(ctx, "chat")
			require.NoError(t, err)
			assert.Equal(t, current, saved)
			binding, err := ds.profileResolver.Resolve(ctx, "chat")
			require.NoError(t, err)
			assert.Equal(t, x, binding.Model, "conflict must not publish")
			// Retrying from a fresh read preserves Y and publishes it to a new run.
			updated, _, err := ds.profileSvc.UpdatePatch(ctx, "chat", provider.ProfilePatch{})
			require.NoError(t, err)
			assert.Equal(t, y, updated.Model)
			assert.Equal(t, current.Revision+1, updated.Revision)
			run(y)
			old, err := ds.instanceRepo.Get(ctx, oldRun)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"chat": x}, old.ModelPins)
			mu.Lock()
			got := append([]string(nil), models...)
			mu.Unlock()
			assert.Equal(t, []string{x, y}, got)
			require.NoError(t, ds.profileSvc.Delete(ctx, "chat"))
		})
	}
}
