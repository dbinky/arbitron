package filesystem

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// Crash in the actual RollbackTreeCommit rename gap, not a manually assembled
// backup fixture. The already-running reader must settle before scope selection.
func TestCommitProtocol_RollbackSwapGap(t *testing.T) {
	if base := os.Getenv("ARBITRON_ROLLBACK_CRASH"); base != "" {
		runRollbackCrash(t, base, os.Getenv("ARBITRON_ROLLBACK_SCOPE"))
		return
	}
	for _, kind := range []string{"private", "shared"} {
		for _, artifact := range []string{"workflow.json", "layout.json"} {
			t.Run(kind+"/"+artifact, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				base := t.TempDir()
				db, err := adaptsqlite.Open(filepath.Join(base, "db.sqlite"))
				require.NoError(t, err)
				defer db.Close()
				require.NoError(t, adaptsqlite.RunMigrations(db))
				store := adaptsqlite.NewLayoutRepo(db)
				requester := layout.NewRequester(store, 3, nil)
				repo := NewWorkflowRepo(filepath.Join(base, "workflows"))
				journal := NewRevisionJournal(repo.basePath)
				source, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Relocator: requester})
				require.NoError(t, err)
				repo.SetReadSettler(source)
				stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: source})
				require.NoError(t, err)
				scope := rollbackCrashScope(kind)
				require.NoError(t, repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: "target"}}, func(owned context.Context) error {
					wf := &workflow.Workflow{ID: uuid.New(), Name: "target", Nodes: []workflow.Node{{ID: "saved", Type: workflow.NodeTypeTransform, Label: "Original 完整 label"}}}
					req, err := stamper.Stamp(owned, scope, "target", wf, nil, workflow.SaveReasonCreate)
					if err != nil {
						return err
					}
					if err = repo.Create(owned, scope, "target", wf); err != nil {
						return err
					}
					stamper.Committed(owned, req)
					return nil
				}))
				dir, err := repo.resolveWorkflowDir(scope, "target")
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "layout.json"), []byte("prior cache bytes"), 0600))
				require.NoError(t, repo.WriteFile(ctx, scope, "target", "support.txt", []byte("prior supporting bytes")))
				want := map[string][]byte{}
				for _, name := range []string{"workflow.json", "layout.json", "support.txt"} {
					want[name], err = os.ReadFile(filepath.Join(dir, name))
					require.NoError(t, err)
				}
				if kind == "private" {
					require.NoError(t, repo.Create(ctx, workflow.Shared(), "target", &workflow.Workflow{Name: "target", Description: "wrong fallback"}))
					require.NoError(t, os.WriteFile(filepath.Join(repo.basePath, "shared", "target", "layout.json"), []byte("wrong fallback cache"), 0600))
				}
				reader := NewFileReader(repo.basePath)
				reader.SetSettledRepository(repo)
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommitProtocol_RollbackSwapGap$")
				cmd.Env = append(os.Environ(), "ARBITRON_ROLLBACK_CRASH="+base, "ARBITRON_ROLLBACK_SCOPE="+kind)
				stdout, err := cmd.StdoutPipe()
				require.NoError(t, err)
				stdin, err := cmd.StdinPipe()
				require.NoError(t, err)
				defer stdin.Close()
				cmd.Stderr = os.Stderr
				require.NoError(t, cmd.Start())
				t.Cleanup(func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				ready := make(chan bool, 1)
				go func() {
					scanner := bufio.NewScanner(stdout)
					for scanner.Scan() {
						if scanner.Text() == "ROLLBACK-GAP" {
							ready <- true
							return
						}
					}
					ready <- false
				}()
				select {
				case ok := <-ready:
					require.True(t, ok, "actual rollback never reached its rename gap")
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				require.NoDirExists(t, dir, "the private candidate must be absent, not merely have stale bytes")
				require.NoError(t, cmd.Process.Kill())
				require.Error(t, cmd.Wait())
				// No load or recovery poll may precede this reserved-byte read.
				got, err := reader.ReadFile(scope.OwnerID, "target", artifact)
				require.NoError(t, err)
				require.Equal(t, want[artifact], got, "a recoverable private tree must beat same-name shared fallback")
				for name, before := range want {
					after, err := os.ReadFile(filepath.Join(dir, name))
					require.NoError(t, err)
					require.Equal(t, before, after)
				}
				var abandoned workflow.ArrangementRequest
				data, err := os.ReadFile(filepath.Join(base, "captured.json"))
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(data, &abandoned))
				_, err = store.Get(ctx, abandoned.Revision)
				require.ErrorIs(t, err, layout.ErrRecordNotFound)
				var rows int
				require.NoError(t, db.QueryRow("SELECT count(*) FROM layout_revisions").Scan(&rows))
				require.Equal(t, 1, rows, "only the genuinely saved original may be arranged")
				requests, err := source.CommittedRequests(ctx)
				require.NoError(t, err)
				require.Empty(t, requests)
				pending, err := repo.PendingTreeCommits(ctx)
				require.NoError(t, err)
				require.Empty(t, pending)
			})
		}
	}
}

func rollbackCrashScope(kind string) workflow.Scope {
	if kind == "private" {
		return workflow.Private(uuid.MustParse("00000000-0000-0000-0000-000000000001"))
	}
	return workflow.Shared()
}

func runRollbackCrash(t *testing.T, base, kind string) {
	ctx := context.Background()
	db, err := adaptsqlite.Open(filepath.Join(base, "db.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	repo := NewWorkflowRepo(filepath.Join(base, "workflows"))
	journal := NewRevisionJournal(repo.basePath)
	requester := layout.NewRequester(adaptsqlite.NewLayoutRepo(db), 3, nil)
	source, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Relocator: requester})
	require.NoError(t, err)
	repo.SetReadSettler(source)
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: source})
	require.NoError(t, err)
	scope := rollbackCrashScope(kind)
	require.NoError(t, repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: "target"}}, func(owned context.Context) error {
		prior, err := repo.Load(owned, scope, "target")
		if err != nil {
			return err
		}
		next := *prior
		next.Description = "uncommitted replacement"
		req, err := stamper.Stamp(owned, scope, "target", &next, prior, workflow.SaveReasonUpdate)
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
		tree, err := repo.PrepareTreeCommit(owned, workflow.MutationTarget{Scope: scope, Name: "target"}, req.Revision)
		if err != nil {
			return err
		}
		if err = repo.Update(owned, scope, "target", &next); err != nil {
			return err
		}
		dir, err := repo.resolveWorkflowDir(scope, "target")
		if err != nil {
			return err
		}
		repo.files.rename = func(from, to string) error {
			if err := os.Rename(from, to); err != nil {
				return err
			}
			if from == dir {
				if err := syncDir(filepath.Dir(dir)); err != nil {
					return err
				}
				fmt.Println("ROLLBACK-GAP")
				var release [1]byte
				_, _ = os.Stdin.Read(release[:])
				os.Exit(2)
			}
			return nil
		}
		return repo.RollbackTreeCommit(owned, tree.ID)
	}))
	t.Fatal("rollback bypassed the injected rename boundary")
}
