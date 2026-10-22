package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func settledReaderFixture(t *testing.T, scopeDir ...string) (*FileReaderAdapter, *WorkflowRepo, string) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(append([]string{base}, scopeDir...)...)
	dir = filepath.Join(dir, "wf")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "workflow.json"), []byte(`{"name":"wf"}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompt.txt"), []byte("hi"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "layout.json"), []byte("{}"), 0o644))
	repo := NewWorkflowRepo(base)
	reader := NewFileReader(base)
	reader.SetSettledRepository(repo)
	return reader, repo, base
}

func TestFileReader_ReservedReadIsSettledFirst(t *testing.T) {
	owner := uuid.New()
	for name, scopeDir := range map[string][]string{"shared": {"shared"}, "private": {"users", owner.String()}} {
		t.Run(name, func(t *testing.T) {
			reader, repo, _ := settledReaderFixture(t, scopeDir...)
			var settled []workflow.Scope
			repo.SetReadSettler(readSettlerFunc(func(_ context.Context, s workflow.Scope, n string) error {
				require.Equal(t, "wf", n)
				settled = append(settled, s)
				return nil
			}))
			data, err := reader.ReadFile(owner, "wf", "workflow.json")
			require.NoError(t, err)
			require.JSONEq(t, `{"name":"wf"}`, string(data))
			if name == "shared" {
				require.Equal(t, []workflow.Scope{workflow.Private(owner), workflow.Shared()}, settled)
			} else {
				require.Equal(t, []workflow.Scope{workflow.Private(owner)}, settled)
			}
		})
	}
}

func TestFileReader_PendingMutationNeverServesReservedBytes(t *testing.T) {
	reader, repo, _ := settledReaderFixture(t, "shared")
	repo.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error { return workflow.ErrMutationPending }))
	for _, p := range []string{"workflow.json", "layout.json"} {
		data, err := reader.ReadFile(uuid.Nil, "wf", p)
		require.Nil(t, data)
		require.True(t, errors.Is(err, workflow.ErrMutationPending), "%s: %v", p, err)
	}
}

func TestFileReader_SettlementObservesRecoveredBytes(t *testing.T) {
	reader, repo, base := settledReaderFixture(t, "shared")
	repo.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error {
		return os.WriteFile(filepath.Join(base, "shared", "wf", "workflow.json"), []byte(`{"name":"restored"}`), 0o644)
	}))
	data, err := reader.ReadFile(uuid.Nil, "wf", "workflow.json")
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"restored"}`, string(data))
}

func TestFileReader_OrdinaryFilesAndUnwiredReaderSkipSettlement(t *testing.T) {
	reader, repo, _ := settledReaderFixture(t, "shared")
	repo.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error { return workflow.ErrMutationPending }))
	data, err := reader.ReadFile(uuid.Nil, "wf", "prompt.txt")
	require.NoError(t, err)
	require.Equal(t, "hi", string(data))

	plain := NewFileReader(repo.basePath)
	data, err = plain.ReadFile(uuid.Nil, "wf", "workflow.json")
	require.NoError(t, err)
	require.NotEmpty(t, data)
}

func TestFileReader_ResolvePathSettlesReservedFiles(t *testing.T) {
	reader, repo, _ := settledReaderFixture(t, "shared")
	repo.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error { return workflow.ErrMutationPending }))
	_, err := reader.ResolvePath(uuid.Nil, "wf", "workflow.json")
	require.ErrorIs(t, err, workflow.ErrMutationPending)
}

// Leave real durable recovery evidence at the gap between the two swap renames.
// Reopening the reader and repository rules out in-memory recovery state.
func missingTreeReader(t *testing.T, scope workflow.Scope) (*FileReaderAdapter, *WorkflowRepo, *RevisionJournal, map[string][]byte) {
	t.Helper()
	ctx := context.Background()
	base := t.TempDir()
	db, err := adaptsqlite.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, adaptsqlite.RunMigrations(db))
	requester := layout.NewRequester(adaptsqlite.NewLayoutRepo(db), 3, nil)
	repo := NewWorkflowRepo(base)
	journal := NewRevisionJournal(base)
	source, err := workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Relocator: requester})
	require.NoError(t, err)
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: requester, Settler: source})
	require.NoError(t, err)
	prior := &workflow.Workflow{Name: "wf", ID: uuid.New(), Description: "prior tree"}
	require.NoError(t, repo.Create(ctx, scope, "wf", prior))
	dir, err := repo.resolveWorkflowDir(scope, "wf")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "layout.json"), []byte("prior cache bytes"), 0600))
	want := map[string][]byte{}
	for _, name := range []string{"workflow.json", "layout.json"} {
		want[name], err = os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
	}
	// A same-name shared workflow must never win over a recoverable private one.
	if scope.Kind == workflow.ScopePrivate {
		require.NoError(t, repo.Create(ctx, workflow.Shared(), "wf", &workflow.Workflow{Name: "wf", Description: "wrong shared fallback"}))
		require.NoError(t, os.WriteFile(filepath.Join(base, "shared", "wf", "layout.json"), []byte("wrong shared cache"), 0600))
	}
	require.NoError(t, repo.WithMutations(ctx, []workflow.MutationTarget{{Scope: scope, Name: "wf"}}, func(owned context.Context) error {
		next := *prior
		next.Description = "uncommitted replacement"
		req, err := stamper.Stamp(owned, scope, "wf", &next, prior, workflow.SaveReasonUpdate)
		if err != nil {
			return err
		}
		tree, err := repo.PrepareTreeCommit(owned, workflow.MutationTarget{Scope: scope, Name: "wf"}, req.Revision)
		if err != nil {
			return err
		}
		if err = os.Rename(dir, filepath.Join(filepath.Dir(dir), ".wf.old-"+tree.ID)); err != nil {
			return err
		}
		return syncDir(filepath.Dir(dir))
	}))
	require.NoDirExists(t, dir)
	repo = NewWorkflowRepo(base)
	journal = NewRevisionJournal(base)
	source, err = workflow.NewCommitSource(workflow.CommitSourceDeps{Repo: repo, Journal: journal, Mutations: repo, Trees: repo, Relocator: requester})
	require.NoError(t, err)
	repo.SetReadSettler(source)
	reader := NewFileReader(base)
	reader.SetSettledRepository(repo)
	return reader, repo, journal, want
}

func TestFileReader_MissingTreeSettlesBeforeScopeSelection(t *testing.T) {
	for _, scopeName := range []string{"shared", "private"} {
		for _, surface := range []string{"read", "resolve", "exists"} {
			for _, artifact := range []string{"workflow.json", "layout.json"} {
				t.Run(scopeName+"/"+surface+"/"+artifact, func(t *testing.T) {
					scope := workflow.Shared()
					if scopeName == "private" {
						scope = workflow.Private(uuid.New())
					}
					reader, repo, journal, want := missingTreeReader(t, scope)
					dir, err := repo.resolveWorkflowDir(scope, "wf")
					require.NoError(t, err)
					switch surface {
					case "read":
						got, err := reader.ReadFile(scope.OwnerID, "wf", artifact)
						require.NoError(t, err)
						require.Equal(t, want[artifact], got)
					case "resolve":
						path, err := reader.ResolvePath(scope.OwnerID, "wf", artifact)
						require.NoError(t, err)
						require.Equal(t, filepath.Join(dir, artifact), path)
					case "exists":
						require.True(t, reader.FileExists(scope.OwnerID, "wf", artifact))
					}
					for name, original := range want {
						got, err := os.ReadFile(filepath.Join(dir, name))
						require.NoError(t, err)
						require.Equal(t, original, got, "recovery restores every reserved artifact")
					}
					pending, err := repo.PendingTreeCommits(context.Background())
					require.NoError(t, err)
					require.Empty(t, pending)
					entries, err := journal.Entries(context.Background())
					require.NoError(t, err)
					require.Empty(t, entries, "uncommitted replacement must never become arrangement input")
				})
			}
		}
	}
}

func TestFileReader_MissingPrivateTreeFailsClosed(t *testing.T) {
	for _, failure := range []error{workflow.ErrMutationPending, os.ErrNotExist} {
		for _, artifact := range []string{"workflow.json", "layout.json"} {
			t.Run(failure.Error()+"/"+artifact, func(t *testing.T) {
				owner := uuid.New()
				reader, repo, _ := settledReaderFixture(t, "shared")
				repo.SetReadSettler(readSettlerFunc(func(_ context.Context, s workflow.Scope, _ string) error {
					if s == workflow.Private(owner) {
						return failure
					}
					return nil
				}))
				got, err := reader.ReadFile(owner, "wf", artifact)
				require.ErrorIs(t, err, failure, "settlement failure must not fall through to shared bytes")
				require.Nil(t, got)
				path, err := reader.ResolvePath(owner, "wf", artifact)
				require.ErrorIs(t, err, failure)
				require.Empty(t, path)
				require.False(t, reader.FileExists(owner, "wf", artifact))
			})
		}
	}
}

// Generated swap directories contain either the old tree or an unpublished
// staging tree. Ordinary files bypass settlement, but must never make those
// directories addressable as workflows, even through an unwired engine reader.
func TestFileReader_SwapDirectoriesNeverResolveOrdinaryFiles(t *testing.T) {
	for _, kind := range []string{"shared", "private"} {
		for _, wired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wired=%t", kind, wired), func(t *testing.T) {
				owner := uuid.Nil
				scopeDir := []string{"shared"}
				if kind == "private" {
					owner = uuid.New()
					scopeDir = []string{"users", owner.String()}
				}
				reader, repo, base := settledReaderFixture(t, scopeDir...)
				if !wired {
					reader = NewFileReader(base)
				}
				repo.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error {
					t.Fatal("ordinary script resolution must not invoke settlement")
					return nil
				}))
				root := filepath.Join(append([]string{base}, scopeDir...)...)
				id := uuid.NewString()
				aside, staging := ".wf.old-"+id, ".wf.tmp-"+id
				require.NoError(t, os.Rename(filepath.Join(root, "wf"), filepath.Join(root, aside)))
				require.NoError(t, os.Mkdir(filepath.Join(root, staging), 0700))
				for _, name := range []string{aside, staging} {
					require.NoError(t, os.WriteFile(filepath.Join(root, name, "script.sh"), []byte("unsettled script"), 0600))
					for _, alias := range []string{name, "./" + name, "wf/../" + name} {
						path, err := reader.ResolvePath(owner, alias, "script.sh")
						require.ErrorIs(t, err, workflow.ErrInvalidName)
						require.Empty(t, path)
						data, err := reader.ReadFile(owner, alias, "script.sh")
						require.ErrorIs(t, err, workflow.ErrInvalidName)
						require.Nil(t, data)
						require.False(t, reader.FileExists(owner, alias, "script.sh"))
					}
					path, err := reader.ResolvePath(owner, "wf", "../"+name+"/script.sh")
					require.Error(t, err)
					require.Empty(t, path)
				}
				// Ordinary dot-prefixed workflows are supported; only generated
				// internal names are reserved.
				require.NoError(t, os.Mkdir(filepath.Join(root, ".ordinary"), 0700))
				require.NoError(t, os.WriteFile(filepath.Join(root, ".ordinary", "script.sh"), []byte("committed script"), 0600))
				path, err := reader.ResolvePath(owner, ".ordinary", "script.sh")
				require.NoError(t, err)
				require.Equal(t, filepath.Join(root, ".ordinary", "script.sh"), path)
			})
		}
	}
}

func TestFileReader_HeldDestinationLockTimesOutWithoutBytes(t *testing.T) {
	reader, repo, _ := settledReaderFixture(t, "shared")
	repo.SetReadSettler(readSettlerFunc(func(context.Context, workflow.Scope, string) error {
		t.Error("settlement cannot run before the destination lock is acquired")
		return nil
	}))
	// A separate repository instance exercises the same filesystem lock used
	// by a surviving peer, rather than only the reader's in-memory gate.
	peer := NewWorkflowRepo(repo.basePath)
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- peer.WithMutations(context.Background(), []workflow.MutationTarget{{Scope: workflow.Shared(), Name: "wf"}}, func(context.Context) error {
			close(locked)
			<-release
			return nil
		})
	}()
	t.Cleanup(func() {
		close(release)
		require.NoError(t, <-done)
	})
	select {
	case <-locked:
	case <-time.After(5 * time.Second):
		t.Fatal("peer failed to acquire destination lock")
	}
	started := time.Now()
	data, err := reader.ReadFile(uuid.Nil, "wf", "workflow.json")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, data, "a lock timeout must never fall back to visible bytes")
	require.Less(t, time.Since(started), 35*time.Second, "the raw-read timeout must bound the lock wait")
}
