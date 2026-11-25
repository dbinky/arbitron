package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	adaptcrypto "github.com/arbitron/arbitron/adapter/crypto"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicationWiringRequiresDependencies(t *testing.T) {
	ds := bootSinkRoot(t, "", func(string) {})
	for _, missing := range []string{"publisher", "gate"} {
		t.Run(missing, func(t *testing.T) {
			deps := ds.refreshDeps
			if missing == "publisher" {
				deps.Publisher = nil
			} else {
				deps.Gate = nil
			}
			refresh, err := newModelRefresh(deps)
			if refresh != nil {
				defer refresh.stop()
			}
			assert.ErrorIs(t, err, provider.ErrAdvancementPortsMissing)
			assert.Nil(t, refresh, "startup must reject incomplete publication wiring")
		})
	}
}

// A direct commit has no refresh report. Repair must publish the authoritative
// state, including later admin deletion/replacement, without repeating its CAS.
func TestDirectAdvancePublicationRecoveryWithoutReport(t *testing.T) {
	const initial = "deepseek/deepseek-v4-flash"
	const advanced = "deepseek/deepseek-v4.1-flash"
	const replacement = "deepseek/deepseek-v4.2-flash"
	for _, repair := range []string{"retry", "startup"} {
		for _, action := range []string{"unchanged", "deleted", "superseded", "recreated"} {
			t.Run(repair+"/"+action, func(t *testing.T) {
				var mu sync.Mutex
				wantModel := initial
				var models []string
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model    string `json:"model"`
						Messages []struct {
							Content string `json:"content"`
						} `json:"messages"`
						Stream bool `json:"stream"`
					}
					err := json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					want := wantModel
					models = append(models, body.Model)
					mu.Unlock()
					if err != nil || r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer publication-key" || body.Model != want || body.Stream || len(body.Messages) == 0 || body.Messages[len(body.Messages)-1].Content != "hello" {
						http.Error(w, "invalid model/auth/route/prompt", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"model":%q,"choices":[{"message":{"role":"assistant","content":"publication-ok"},"finish_reason":"stop"}]}`, want)
				}))
				defer upstream.Close()
				ds, closeStack := bootSinkRootWithClose(t, "", func(workspace string) {
					writeWorkflow(t, workspace, "publication-exec", `{"name":"publication-exec","nodes":[{"id":"ask","type":"llm_call","entrypoint":true,"config":{"model_profile":"worker","templateFile":"templates/p.md"}}],"edges":[]}`)
					writePinTemplate(t, workspace, "publication-exec")
				})
				ctx := adminCtxForLocalUser(t, ds)
				crypto, err := adaptcrypto.NewAESGCM(bytes.Repeat([]byte{0x5d}, 32))
				require.NoError(t, err)
				conns := newSyncingConnStore(adaptsqlite.NewConnectionRepoWithPolicy(ds.db, crypto, provider.AllowAnyURLPolicy), ds.runners.refreshInventory)
				require.NoError(t, conns.Create(ctx, provider.Connection{Name: "publication-sink", Provider: "openrouter", BaseURL: upstream.URL}, "publication-key"))
				// Existing proved following rows are the input to direct advancement;
				// cold candidate initialization is a separate producer contract.
				storeProfileUnchecked(t, ctx, ds, provider.Profile{Alias: "worker", Kind: provider.ProfileKindChat, Backend: "claude-code", ConnectionRef: "publication-sink", Model: initial, SelectionMode: provider.SelectionFollowing, FollowTarget: &provider.FollowTarget{Provider: "openrouter", Tier: "flash"}})
				_, _, err = ds.profileSvc.Create(ctx, provider.Profile{Alias: "publication-guard", Kind: provider.ProfileKindChat, ConnectionRef: "publication-sink", Model: initial})
				require.NoError(t, err)
				execute := func(stack *domainStack, want string) *instance.Instance {
					t.Helper()
					id, _, err := stack.eng.Trigger(ctx, "publication-exec", nil, "normal")
					require.NoError(t, err)
					run := waitTerminal(t, stack, id)
					assert.Equal(t, instance.StateCompleted, run.State)
					assert.Equal(t, want, run.ModelPins["worker"])
					var output string
					require.NoError(t, stack.db.QueryRow(`SELECT output FROM node_executions WHERE instance_id=? AND node_id='ask'`, id.String()).Scan(&output))
					assert.Contains(t, output, "publication-ok")
					return run
				}
				old := execute(ds, initial)
				prior, err := ds.profileSvc.Get(ctx, "worker")
				require.NoError(t, err)
				snap, err := ds.pathInventory.Snapshot(ctx)
				require.NoError(t, err)
				req := provider.AdvanceRequest{ID: "publication-direct", Expected: prior, Model: advanced, CanonicalID: advanced, PathRevision: snap.Revision, ExecutionRevision: snap.ExecutionRevision}
				// A database-leaf fault breaks List only AFTER the durable commit,
				// leaving the real cache at X and the original journal row pending.
				_, err = ds.db.Exec(`CREATE TRIGGER fail_publication AFTER INSERT ON profile_advancement_journal BEGIN UPDATE profiles SET allowed_tools='invalid-json' WHERE alias='publication-guard'; END`)
				require.NoError(t, err)
				adv, err := ds.profileSvc.AdvanceModel(ctx, req)
				require.ErrorIs(t, err, provider.ErrPublicationPending)
				require.Equal(t, advanced, adv.ToModel)
				binding, err := ds.profileResolver.Resolve(ctx, "worker")
				require.NoError(t, err)
				assert.Equal(t, initial, binding.Model, "failed reload must retain the working snapshot")
				replayed, err := ds.profileSvc.AdvanceModel(ctx, req)
				require.ErrorIs(t, err, provider.ErrPublicationPending)
				assert.Equal(t, adv, replayed)
				pending, err := ds.catalogStore.Unpublished(ctx)
				require.NoError(t, err)
				require.Equal(t, []provider.Advancement{adv}, pending)
				var reportsBeforeRepair int
				// Scoped to this advancement: the stack's shipped following defaults
				// seed in the background and write reports of their own.
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_refresh_reports WHERE id IN (SELECT refresh_id FROM profile_advancement_journal WHERE id='publication-direct')`).Scan(&reportsBeforeRepair))
				assert.Zero(t, reportsBeforeRepair, "the direct commit has no refresh report to recover")
				_, err = ds.db.Exec(`DROP TRIGGER fail_publication; UPDATE profiles SET allowed_tools='[]' WHERE alias='publication-guard'`)
				require.NoError(t, err)

				want := advanced
				if action == "deleted" || action == "recreated" {
					err = ds.profileSvc.Delete(ctx, "worker")
					require.NoError(t, err)
				}
				if action == "superseded" || action == "recreated" {
					p := prior.Clone()
					p.Model, p.SelectionMode, p.FollowTarget = replacement, provider.SelectionPinned, nil
					if action == "recreated" {
						_, _, err = ds.profileSvc.Create(ctx, p)
					} else {
						_, _, err = ds.profileSvc.Update(ctx, p)
					}
					require.NoError(t, err)
					want = replacement
				}
				if repair == "retry" {
					replayed, err = ds.profileSvc.AdvanceModel(ctx, req)
					assert.NoError(t, err)
					assert.Equal(t, adv, replayed)
				} else {
					// Keep the actual guarded startup fetch local and fail-closed;
					// publication recovery must not depend on successful discovery.
					_, err = ds.db.Exec(`UPDATE system_settings SET value='http://127.0.0.1/catalog' WHERE key='model_catalog.url'`)
					require.NoError(t, err)
					configPath := filepath.Join(filepath.Dir(ds.cfg.Database.Path), "config.yaml")
					closeStack()
					ds, err = initRecoveryTestDomain(context.Background(), configPath)
					require.NoError(t, err)
					t.Cleanup(func() { ds.Close(context.Background()) })
				}
				pending, err = ds.catalogStore.Unpublished(ctx)
				require.NoError(t, err)
				assert.Empty(t, pending, "repair must acknowledge original changes, even after deletion")
				var journals, acks, reports int
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM profile_advancement_journal WHERE id='publication-direct' AND to_model=? AND revision=?`, advanced, adv.Revision).Scan(&journals))
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_publication_acks WHERE advancement_id='publication-direct'`).Scan(&acks))
				require.NoError(t, ds.db.QueryRow(`SELECT COUNT(*) FROM model_refresh_reports WHERE id IN (SELECT refresh_id FROM profile_advancement_journal WHERE id='publication-direct')`).Scan(&reports))
				assert.Equal(t, 1, journals)
				assert.Equal(t, 1, acks)
				assert.Zero(t, reports, "repair must not fabricate a report for the original direct advancement")
				if action == "deleted" {
					_, err = ds.profileSvc.Get(ctx, "worker")
					assert.ErrorIs(t, err, provider.ErrProfileNotFound)
					_, err = ds.profileResolver.Resolve(ctx, "worker")
					assert.ErrorIs(t, err, provider.ErrUnknownProfile)
					_, _, err = ds.eng.Trigger(ctx, "publication-exec", nil, "normal")
					assert.Error(t, err, "recovery must not resurrect the deleted alias")
				} else {
					stored, err := ds.profileSvc.Get(ctx, "worker")
					require.NoError(t, err)
					assert.Equal(t, want, stored.Model)
					if action != "unchanged" {
						assert.Greater(t, stored.Revision, adv.Revision)
					}
					mu.Lock()
					wantModel = want
					mu.Unlock()
					execute(ds, want)
				}
				oldStored, err := ds.instanceRepo.Get(ctx, old.ID)
				require.NoError(t, err)
				assert.Equal(t, initial, oldStored.ModelPins["worker"])
				mu.Lock()
				if action == "deleted" {
					assert.Equal(t, []string{initial}, models)
				} else {
					assert.Equal(t, []string{initial, want}, models)
				}
				mu.Unlock()
			})
		}
	}
}
