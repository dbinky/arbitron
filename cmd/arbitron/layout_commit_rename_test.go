package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// These decorators pause only at a named boundary of the actual Service.Rename
// protocol. They forward every load-bearing argument to the root's real ports.
type renameCrashProbe struct {
	layoutCrashBarrier
	armed      bool
	sourceName string
}

func (p *renameCrashProbe) at(stage string) {
	if p.armed && p.stage == stage {
		p.pause()
	}
}
func (p *renameCrashProbe) identity(commit func() error) error {
	p.at("before-identity")
	if p.armed && p.stage == "rollback-restored" {
		return errors.New("injected identity failure")
	}
	if err := commit(); err != nil {
		return err
	}
	p.at("after-identity")
	return nil
}

type renameCrashRegistry struct {
	workflow.Registry
	probe *renameCrashProbe
}

func (r renameCrashRegistry) Rename(ctx context.Context, id uuid.UUID, name string) error {
	return r.probe.identity(func() error { return r.Registry.Rename(ctx, id, name) })
}

type renameCrashPublications struct {
	workflow.RevisionPublicationStore
	probe *renameCrashProbe
}

func (p renameCrashPublications) RenamePublication(ctx context.Context, id uuid.UUID, from, to string, pub workflow.Publication) error {
	return p.probe.identity(func() error { return p.RevisionPublicationStore.RenamePublication(ctx, id, from, to, pub) })
}

type renameCrashJournal struct {
	workflow.RevisionJournal
	probe *renameCrashProbe
}

func (j renameCrashJournal) Record(ctx context.Context, req workflow.ArrangementRequest) error {
	if err := j.RevisionJournal.Record(ctx, req); err != nil {
		return err
	}
	if j.probe.armed {
		data, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(j.probe.root, "captured.json"), data, 0600); err != nil {
			return err
		}
		j.probe.at("prepare")
	}
	return nil
}
func (j renameCrashJournal) MarkCommitted(ctx context.Context, revision string) error {
	if err := j.RevisionJournal.MarkCommitted(ctx, revision); err != nil {
		return err
	}
	j.probe.at("receipt")
	return nil
}
func (j renameCrashJournal) Remove(context.Context, string) error {
	// Exercise an actual acknowledgement-cleanup outage, preserving history for
	// the later journal->Store partial-relocation crash, not a fabricated receipt.
	return errors.New("injected receipt cleanup outage")
}
func (j renameCrashJournal) Relocate(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string) error {
	if err := j.RevisionJournal.Relocate(ctx, from, fn, to, tn); err != nil {
		return err
	}
	j.probe.at("journal-relocated")
	return nil
}

type renameCrashRequester struct {
	workflow.ArrangementRequester
	probe *renameCrashProbe
}

func (r renameCrashRequester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error {
	if err := r.ArrangementRequester.RequestArrangement(ctx, req); err != nil {
		return err
	}
	r.probe.at("enqueue")
	return nil
}
func (r renameCrashRequester) RelocateArrangements(ctx context.Context, from workflow.Scope, fn string, to workflow.Scope, tn string) error {
	if err := r.ArrangementRequester.RelocateArrangements(ctx, from, fn, to, tn); err != nil {
		return err
	}
	r.probe.at("store-relocated")
	return nil
}

type renameCrashRepo struct {
	*filesystem.WorkflowRepo
	probe *renameCrashProbe
}

func (r renameCrashRepo) Delete(ctx context.Context, scope workflow.Scope, name string) error {
	if r.probe.armed && name == r.probe.sourceName && r.probe.stage == "cleanup-error" {
		return errors.New("injected source cleanup failure")
	}
	if err := r.WorkflowRepo.Delete(ctx, scope, name); err != nil {
		return err
	}
	if name == r.probe.sourceName {
		r.probe.at("source-cleaned")
	} else if name == "renamed" {
		r.probe.at("rollback-restored")
	}
	return nil
}
func (r renameCrashRepo) RollbackTreeCommit(ctx context.Context, id string) error {
	if err := r.WorkflowRepo.RollbackTreeCommit(ctx, id); err != nil {
		return err
	}
	r.probe.at("rollback-restored")
	return nil
}

func TestCommitProtocol_RenameProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_RENAME_CRASH_CONFIG")
	if configPath == "" {
		return
	}
	kind, stage, _ := strings.Cut(os.Getenv("ARBITRON_RENAME_CRASH_STAGE"), "/")
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	ds.layout.worker.Stop()
	probe := &renameCrashProbe{layoutCrashBarrier: layoutCrashBarrier{root: filepath.Dir(configPath), stage: stage}}
	repo := renameCrashRepo{ds.workflowRepo.(*filesystem.WorkflowRepo), probe}
	journal := renameCrashJournal{ds.layout.journal, probe}
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: renameCrashRequester{ds.layout.requester, probe}, Settler: ds.layout.commits, Displayed: ds.layout.store})
	require.NoError(t, err)
	svc := workflow.NewWorkflowService(repo, renameCrashPublications{ds.publishedRepo, probe})
	svc.SetRegistry(renameCrashRegistry{adaptsqlite.NewWorkflowIdentityRepo(ds.db), probe})
	svc.SetRevisionStamper(stamper)
	svc.SetMutationCoordinator(ds.layout.mutations)
	ctx := adminCtxForLocalUser(t, ds)
	scope, path := workflow.Private(localDefaultUser), filepath.Join(ds.cfg.Workspace.Path, "users", localDefaultUser.String(), "original", "workflow.json")
	require.NoError(t, svc.Create(ctx, "original", layoutE2EWorkflow("original", "Saved Unicode 原始 → text")))
	if kind == "shared" {
		require.NoError(t, svc.Publish(ctx, "original", "published", workflow.VisibilityPublic, ""))
		scope, path = workflow.Shared(), filepath.Join(ds.cfg.Workspace.Path, "shared", "published", "workflow.json")
	}
	name := "original"
	if kind == "shared" {
		name = "published"
	}
	require.NoError(t, repo.WriteFile(ctx, scope, name, "support.txt", []byte("saved supporting bytes")))
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(path), "layout.json"), []byte(`{"saved":"cache bytes"}`), 0600))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(probe.root, "prior-manifest.json"), before, 0600))
	probe.sourceName, probe.armed = name, true
	err = svc.Rename(ctx, name, "renamed")
	if stage == "cleanup-error" {
		require.ErrorContains(t, err, "injected source cleanup failure")
		probe.pause()
	}
	require.NoError(t, err)
	t.Fatal("rename never reached kill barrier")
}

func TestCommitProtocol_RenamePeerCrash(t *testing.T) {
	pinLayoutE2EEnv(t)
	for _, kind := range []string{"private", "shared"} {
		for _, boundary := range []string{"prepare", "before-identity", "after-identity", "receipt", "enqueue", "journal-relocated", "store-relocated", "cleanup-error", "source-cleaned", "rollback-restored"} {
			t.Run(kind+"/"+boundary, func(t *testing.T) {
				t.Parallel()
				e := newLayoutE2EEnv(t, layoutE2EOptions{maxAttempts: "1"})
				ds, _ := e.boot(t)
				ds.layout.worker.Stop()
				ctx := adminCtxForLocalUser(t, ds)
				scope, sourcePath, targetPath, sourceName := workflow.Private(localDefaultUser), e.privatePath("original"), e.privatePath("renamed"), "original"
				if kind == "shared" {
					scope, sourcePath, targetPath, sourceName = workflow.Shared(), e.sharedPath("published"), e.sharedPath("renamed"), "published"
				}
				processCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(processCtx, os.Args[0], "-test.run=^TestCommitProtocol_RenameProcess$")
				cmd.Env = append(os.Environ(), "ARBITRON_RENAME_CRASH_CONFIG="+e.configPath, "ARBITRON_RENAME_CRASH_STAGE="+kind+"/"+boundary)
				cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
				require.NoError(t, cmd.Start())
				t.Cleanup(func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				require.Eventually(t, func() bool {
					_, err := os.Stat(filepath.Join(e.root, "crash-ready"))
					return err == nil
				}, 12*time.Second, 10*time.Millisecond)
				require.NoError(t, cmd.Process.Kill())
				require.Error(t, cmd.Wait())
				data, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
				require.NoError(t, err)
				var req workflow.ArrangementRequest
				require.NoError(t, json.Unmarshal(data, &req))
				prior, err := os.ReadFile(filepath.Join(e.root, "prior-manifest.json"))
				require.NoError(t, err)
				var old workflow.Workflow
				require.NoError(t, json.Unmarshal(prior, &old))
				require.NotEqual(t, old.SavedRevision(), req.Revision)
				require.Equal(t, old.ID, req.WorkflowID)
				committed := boundary != "prepare" && boundary != "before-identity" && boundary != "rollback-restored"
				intents, err := ds.layout.journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Len(t, intents, 1)
				if boundary == "source-cleaned" {
					require.NoDirExists(t, filepath.Dir(sourcePath))
				} else {
					require.FileExists(t, sourcePath, "both trees must survive a cleanup failure or partial relocation")
				}
				if boundary == "journal-relocated" {
					entries, err := ds.layout.journal.Entries(ctx)
					require.NoError(t, err)
					found := false
					for _, entry := range entries {
						if entry.Request.Revision == old.SavedRevision() {
							found = true
							require.Equal(t, "renamed", entry.Request.Name)
						}
					}
					require.True(t, found, "actual source receipt must have been durably relocated")
					row, err := ds.layout.store.Get(ctx, old.SavedRevision())
					require.NoError(t, err)
					require.Equal(t, sourceName, row.Destination.Name, "Store has not moved yet at this partial-relocation boundary")
				}
				if boundary == "prepare" || boundary == "rollback-restored" {
					require.NoDirExists(t, filepath.Dir(targetPath))
				} else {
					require.Equal(t, req.Revision, layoutE2ESavedRevision(t, targetPath))
				}
				registry := adaptsqlite.NewWorkflowIdentityRepo(ds.db)
				resolved, err := registry.ResolveName(ctx, old.ID)
				require.NoError(t, err)
				winner, loser := sourceName, "renamed"
				winnerPath, loserPath := sourcePath, targetPath
				if committed {
					winner, loser, winnerPath, loserPath = "renamed", sourceName, targetPath, sourcePath
				}
				require.Equal(t, winner, resolved, "registry, not tree presence, is the commit point")

				// This live root's FIRST post-kill read is reserved bytes, not a worker
				// sweep or a manifest read which could conceal an unwired FileReader.
				for _, name := range []string{sourceName, "renamed"} {
					for _, artifact := range []string{"workflow.json", "layout.json"} {
						got, err := ds.fileReader.ReadFile(localDefaultUser, name, artifact)
						require.Empty(t, got, "unresolved relocation must not expose either tree's reserved bytes")
						require.ErrorIs(t, err, workflow.ErrMutationPending)
					}
				}
				_, err = ds.workflowRepo.Load(ctx, scope, "renamed")
				require.ErrorIs(t, err, workflow.ErrMutationPending)
				_, _, err = ds.eng.Trigger(ctx, "renamed", nil, "normal")
				require.ErrorIs(t, err, workflow.ErrMutationPending)
				var instances int
				require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM instances").Scan(&instances))
				require.Zero(t, instances)
				router := newLayoutE2ERouter(t, ds)
				status, body := layoutE2EDo(t, router, http.MethodGet, "/api/v1/workflows/renamed/files/workflow.json", "")
				require.Equal(t, http.StatusServiceUnavailable, status)
				require.Equal(t, "UNAVAILABLE", body["code"])
				srv := newLayoutE2EMCPServer(t, ds)
				res := callMCPToolE2E(t, srv, ctx, "workflow_read_file", map[string]any{"name": "renamed", "path": "workflow.json"})
				require.True(t, res.IsError)
				require.Contains(t, mcpE2EText(t, res), "awaiting durable mutation recovery")

				// Fresh real collaborators recover the same persisted evidence. Nothing
				// consults a catalog or guesses the winner from the surviving folders.
				repo := filesystem.NewWorkflowRepo(e.workspaceDir)
				journal := filesystem.NewRevisionJournal(e.workspaceDir)
				store := adaptsqlite.NewLayoutRepo(ds.db)
				requester := layout.NewRequester(store, 1, layoutE2EDiscardLogger())
				recovery, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Registry: registry, Publications: adaptsqlite.NewPublishedWorkflowRepo(ds.db), Relocator: requester, Logger: layoutE2EDiscardLogger()})
				require.NoError(t, err)
				requests, err := recovery.CommittedRequests(ctx)
				require.NoError(t, err)
				for _, request := range requests {
					require.NoError(t, requester.RequestArrangement(ctx, request))
				}
				require.NoFileExists(t, loserPath, "losing endpoint %s must be cleaned", loser)
				got, err := ds.fileReader.ReadFile(localDefaultUser, winner, "workflow.json")
				require.NoError(t, err)
				if committed {
					require.JSONEq(t, string(req.Manifest), string(got))
					row, err := store.Get(ctx, req.Revision)
					require.NoError(t, err)
					require.Equal(t, req.Manifest, row.Manifest)
					require.Equal(t, layout.Destination{Scope: scope, Name: winner, WorkflowID: old.ID}, row.Destination)
				} else {
					require.Equal(t, prior, got, "rollback preserves exact source bytes")
					_, err := store.Get(ctx, req.Revision)
					require.ErrorIs(t, err, layout.ErrRecordNotFound)
				}
				for artifact, want := range map[string]string{"support.txt": "saved supporting bytes", "layout.json": `{"saved":"cache bytes"}`} {
					got, err := os.ReadFile(filepath.Join(filepath.Dir(winnerPath), artifact))
					require.NoError(t, err)
					require.Equal(t, want, string(got))
				}
				oldRow, err := store.Get(ctx, old.SavedRevision())
				require.NoError(t, err)
				immutable, err := json.Marshal(&old)
				require.NoError(t, err)
				require.Equal(t, immutable, oldRow.Manifest, "destination relocation must not rewrite immutable input bytes")
				require.Equal(t, layout.Destination{Scope: scope, Name: winner, WorkflowID: old.ID}, oldRow.Destination)
				intents, err = journal.RelocationIntents(ctx)
				require.NoError(t, err)
				require.Empty(t, intents)
				pending, err := repo.PendingTreeCommits(ctx)
				require.NoError(t, err)
				require.Empty(t, pending)
				if kind == "shared" {
					pub, ok, err := ds.publishedRepo.PublicationOf(ctx, winner)
					require.NoError(t, err)
					require.True(t, ok)
					want := old.SavedRevision()
					if committed {
						want = req.Revision
					}
					require.Equal(t, want, pub.Revision)
					require.Equal(t, old.ID, pub.WorkflowID)
					_, ok, err = ds.publishedRepo.PublicationOf(ctx, loser)
					require.NoError(t, err)
					require.False(t, ok)
				}
				// Repeated recovery/acknowledgement cannot add a token or move history back.
				var count int
				require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&count))
				wantCount := 1
				if kind == "shared" {
					wantCount++
				}
				if committed {
					wantCount++
				}
				require.Equal(t, wantCount, count, "no phantom revision may reach SQLite")
				requests, err = recovery.CommittedRequests(ctx)
				require.NoError(t, err)
				for _, request := range requests {
					require.NoError(t, requester.RequestArrangement(ctx, request))
				}
				var afterCount int
				require.NoError(t, ds.db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&afterCount))
				require.Equal(t, count, afterCount)
			})
		}
	}
}
