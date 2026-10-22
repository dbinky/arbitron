package filesystem_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/layout/layouttest"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The child stops at real protocol boundaries while still holding the OS lock.
// Killing it exercises receipt/proof recovery with entirely fresh collaborators.
type crashJournal struct {
	workflow.RevisionJournal
	stage string
}

func (j crashJournal) MarkCommitted(ctx context.Context, rev string) error {
	err := j.RevisionJournal.MarkCommitted(ctx, rev)
	if err == nil && j.stage == "receipt" {
		crashPause()
	}
	return err
}

type crashRequester struct {
	workflow.ArrangementRequester
	stage string
}

func (r crashRequester) RequestArrangement(ctx context.Context, req workflow.ArrangementRequest) error {
	err := r.ArrangementRequester.RequestArrangement(ctx, req)
	if err == nil && r.stage == "enqueue" {
		crashPause()
	}
	return err
}
func crashPause() {
	fmt.Println("CRASH-READY")
	var release [1]byte
	_, _ = os.Stdin.Read(release[:])
	os.Exit(2)
}

func crashScope(stage string) (workflow.Scope, string) {
	if strings.HasPrefix(stage, "private-") {
		return workflow.Private(uuid.MustParse("00000000-0000-0000-0000-000000000001")), strings.TrimPrefix(stage, "private-")
	}
	return workflow.Shared(), stage
}

func TestCommitProtocol_PeerCrash(t *testing.T) {
	if base := os.Getenv("ARBITRON_COMMIT_CRASH"); base != "" {
		runCommitCrashChild(t, base, os.Getenv("ARBITRON_COMMIT_STAGE"))
		return
	}
	for _, stage := range []string{"prepare", "visible", "proof", "receipt", "enqueue", "private-prepare", "private-visible", "private-receipt", "private-enqueue"} {
		t.Run(stage, func(t *testing.T) {
			scope, boundary := crashScope(stage)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			base := t.TempDir()
			workspace := filepath.Join(base, "workflows")
			db, err := adaptsqlite.Open(filepath.Join(base, "db.sqlite"))
			require.NoError(t, err)
			defer db.Close()
			require.NoError(t, adaptsqlite.RunMigrations(db))
			pubs := adaptsqlite.NewPublishedWorkflowRepo(db)
			registry := adaptsqlite.NewWorkflowIdentityRepo(db)
			store := adaptsqlite.NewLayoutRepo(db)
			requester := layout.NewRequester(store, 3, nil)
			repo := filesystem.NewWorkflowRepo(workspace)
			journal := filesystem.NewRevisionJournal(workspace)
			source, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Publications: pubs, Registry: registry, Relocator: requester})
			require.NoError(t, err)
			repo.SetReadSettler(source)
			id := uuid.New()
			oldRev := uuid.Must(uuid.NewV7()).String()
			old := &workflow.Workflow{ID: id, Name: "demo", Nodes: []workflow.Node{{ID: "a"}}, Presentation: &workflow.Presentation{Revision: oldRev, Scope: workflow.ScopeLabel(scope), OwnerID: scope.OwnerID, Name: "demo"}}
			require.NoError(t, repo.Create(ctx, scope, "demo", old))
			require.NoError(t, repo.WriteFile(ctx, scope, "demo", "support", []byte("old support")))
			require.NoError(t, pubs.CommitPublication(ctx, "demo", workflow.Publication{WorkflowID: id, Revision: oldRev, Publisher: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Visibility: workflow.VisibilityPublic}))
			// This reader is already alive before the writer crashes; it does not wait
			// for a worker sweep before deciding which bytes can be exposed.
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommitProtocol_PeerCrash$")
			cmd.Env = append(os.Environ(), "ARBITRON_COMMIT_CRASH="+base, "ARBITRON_COMMIT_STAGE="+stage)
			stdout, err := cmd.StdoutPipe()
			require.NoError(t, err)
			cmd.Stderr = os.Stderr
			stdin, err := cmd.StdinPipe()
			require.NoError(t, err)
			defer stdin.Close()
			require.NoError(t, cmd.Start())
			defer cmd.Process.Kill()
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == "CRASH-READY" {
						ready <- scanner.Text()
						return
					}
				}
				ready <- "child exited"
			}()
			select {
			case line := <-ready:
				require.Equal(t, "CRASH-READY", line)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cache := filesystem.NewLayoutCache(repo)
			geometry := layouttest.Arranged(t, old)
			entry := layout.CacheEntry{Format: layout.CacheFormatV1, Revision: oldRev, Generation: 1, JobGeneration: 1, Contract: geometry.Contract, Verdict: layout.VerdictNeedsAttention, Geometry: geometry}
			destination := layout.Destination{Scope: scope, Name: "demo", WorkflowID: id}
			waiting, stopWaiting := context.WithTimeout(ctx, 40*time.Millisecond)
			require.ErrorIs(t, cache.PublishCache(waiting, destination, entry, func(context.Context) error { t.Fatal("cache escaped live writer lock"); return nil }), context.DeadlineExceeded)
			stopWaiting()
			require.NoError(t, cmd.Process.Kill())
			require.Error(t, cmd.Wait())
			var req workflow.ArrangementRequest
			input, err := os.ReadFile(filepath.Join(base, "captured.json"))
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(input, &req))
			committed := boundary == "proof" || boundary == "receipt" || boundary == "enqueue" || scope.Kind == workflow.ScopePrivate && boundary == "visible"
			published := cache.PublishCache(ctx, destination, entry, func(context.Context) error { return nil })
			if committed {
				require.ErrorIs(t, published, layout.ErrNotCurrent)
			} else {
				require.NoError(t, published)
				cached, ok, err := cache.ReadCache(ctx, destination.Scope, destination.Name)
				require.NoError(t, err)
				require.True(t, ok)
				require.Equal(t, entry, cached)
			}
			saved, err := repo.Load(ctx, scope, "demo")
			require.NoError(t, err)
			wantRev := oldRev
			if committed {
				wantRev = req.Revision
			}
			require.Equal(t, wantRev, saved.SavedRevision())
			partition := "shared"
			if scope.Kind == workflow.ScopePrivate {
				partition = filepath.Join("users", scope.OwnerID.String())
			}
			support, err := os.ReadFile(filepath.Join(workspace, partition, "demo", "support"))
			require.NoError(t, err)
			wantSupport := "old support"
			if committed {
				wantSupport = "new support"
			}
			require.Equal(t, wantSupport, string(support))
			recovered, err := source.CommittedRequests(ctx)
			require.NoError(t, err)
			if committed {
				require.Len(t, recovered, 1)
				require.Equal(t, req, recovered[0])
				for range 2 {
					require.NoError(t, requester.RequestArrangement(ctx, recovered[0]))
				}
				row, err := store.Get(ctx, req.Revision)
				require.NoError(t, err)
				require.Equal(t, req.Manifest, row.Manifest)
			} else {
				require.Empty(t, recovered)
				_, err := store.Get(ctx, req.Revision)
				require.ErrorIs(t, err, layout.ErrRecordNotFound)
			}
		})
	}
}
func runCommitCrashChild(t *testing.T, base, stage string) {
	scope, stage := crashScope(stage)
	ctx := context.Background()
	db, err := adaptsqlite.Open(filepath.Join(base, "db.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	repo := filesystem.NewWorkflowRepo(filepath.Join(base, "workflows"))
	journal := filesystem.NewRevisionJournal(filepath.Join(base, "workflows"))
	pubs := adaptsqlite.NewPublishedWorkflowRepo(db)
	registry := adaptsqlite.NewWorkflowIdentityRepo(db)
	requester := layout.NewRequester(adaptsqlite.NewLayoutRepo(db), 3, nil)
	source, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Publications: pubs, Registry: registry, Relocator: requester})
	require.NoError(t, err)
	repo.SetReadSettler(source)
	st, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: crashJournal{journal, stage}, Requester: crashRequester{requester, stage}, Settler: source})
	require.NoError(t, err)
	require.NoError(t, repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: "demo"}}, func(owned context.Context) error {
		prior, err := repo.Load(owned, scope, "demo")
		if err != nil {
			return err
		}
		wf := *prior
		wf.Description = "new"
		req, err := st.Stamp(owned, scope, "demo", &wf, prior, workflow.SaveReasonUpdate)
		if err != nil {
			return err
		}
		data, err := json.Marshal(req)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(base, "captured.json"), data, 0600); err != nil {
			return err
		}
		if stage == "prepare" {
			crashPause()
		}
		var tree workflow.TreeCommit
		if scope.Kind == workflow.ScopeShared {
			tree, err = repo.PrepareTreeCommit(owned, workflow.MutationTarget{Scope: scope, Name: "demo"}, req.Revision)
			if err != nil {
				return err
			}
		}
		if err = repo.Update(owned, scope, "demo", &wf); err != nil {
			return err
		}
		if err = repo.WriteFile(owned, scope, "demo", "support", []byte("new support")); err != nil {
			return err
		}
		if stage == "visible" {
			crashPause()
		}
		if scope.Kind == workflow.ScopeShared {
			if err = pubs.CommitPublication(owned, "demo", workflow.Publication{Revision: req.Revision, WorkflowID: req.WorkflowID, Publisher: uuid.MustParse("00000000-0000-0000-0000-000000000001"), Visibility: workflow.VisibilityPublic}); err != nil {
				return err
			}
		}
		if stage == "proof" {
			crashPause()
		}
		st.Committed(owned, req)
		if tree.ID == "" {
			return nil
		}
		return repo.FinishTreeCommit(owned, tree.ID)
	}))
}
