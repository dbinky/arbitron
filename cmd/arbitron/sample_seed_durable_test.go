package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/arbitron/arbitron/adapter/filesystem"
	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/layout"
	"github.com/arbitron/arbitron/domain/samples"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

const durableSeedName = "durable-seed"

// Keep source discovery real, but defer seeding until the root's actual durable
// collaborators have been instrumented at a single failure/crash boundary.
func durableSeedEnv(t *testing.T) *layoutE2EEnv {
	t.Helper()
	e := newLayoutE2EEnv(t, layoutE2EOptions{seedSample: durableSeedName})
	t.Setenv("ARBITRON_SAMPLES_SEED", "false")
	require.NoError(t, os.WriteFile(filepath.Join(e.samplesDir, durableSeedName, "z-last.txt"), []byte("last supporting file"), 0600))
	return e
}

func seedWithRoot(t *testing.T, e *layoutE2EEnv, ds *domainStack, repo workflow.Repository, journal workflow.RevisionJournal, publications workflow.RevisionPublicationStore) error {
	t.Helper()
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: ds.layout.requester, Displayed: ds.layout.store, Settler: ds.layout.commits, Logger: layoutE2EDiscardLogger()})
	require.NoError(t, err)
	return seedSampleWorkflows(context.Background(), e.samplesDir, e.workspaceDir, repo, publications, stamper, ds.layout.mutations, adaptsqlite.NewWorkflowIdentityRepo(ds.db), localDefaultUser, layoutE2EDiscardLogger())
}

func assertDurableSeed(t *testing.T, e *layoutE2EEnv, ds *domainStack, req workflow.ArrangementRequest) {
	t.Helper()
	ctx := context.Background()
	wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
	var captured workflow.Workflow
	require.NoError(t, json.Unmarshal(req.Manifest, &captured))
	require.Equal(t, *wf, captured, "the journal captures the complete final saved value")
	source := layoutE2EManifest(t, filepath.Join(e.samplesDir, durableSeedName, "workflow.json"))
	require.Equal(t, source.Nodes, captured.Nodes)
	require.Equal(t, source.Edges, captured.Edges)
	require.Equal(t, workflow.Shared(), req.Scope)
	require.Equal(t, durableSeedName, req.Name)
	require.Equal(t, workflow.SaveReasonSeed, req.Reason)
	require.Equal(t, wf.ID, req.WorkflowID)
	require.Equal(t, wf.Presentation.SavedAt, req.RequestedAt)
	require.Equal(t, layoutE2EPositions, req.Retained.Positions)
	proof, ok, err := ds.publishedRepo.PublicationOf(ctx, durableSeedName)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, req.Revision, proof.Revision)
	require.Equal(t, req.WorkflowID, proof.WorkflowID)
	require.Equal(t, localDefaultUser, proof.Publisher)
	identity, err := adaptsqlite.NewWorkflowIdentityRepo(ds.db).Resolve(ctx, durableSeedName)
	require.NoError(t, err)
	require.Equal(t, req.WorkflowID, identity.ID)
	require.Equal(t, workflow.ScopeShared, identity.Scope)
	for _, file := range []string{"support.txt", "z-last.txt"} {
		want, err := os.ReadFile(filepath.Join(e.samplesDir, durableSeedName, file))
		require.NoError(t, err)
		got, err := os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath(durableSeedName)), file))
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	rec := layoutE2EWaitTerminal(t, ds, req.Revision)
	layoutE2EAssertOutcome(t, rec)
	require.Equal(t, req.Manifest, rec.Manifest, "recovery must feed immutable input, not reread the catalog")
	require.Equal(t, req.Retained, rec.Retained)
	require.Equal(t, req.WorkflowID, rec.Destination.WorkflowID)
	require.Equal(t, req.RequestedAt, rec.RequestedAt)
	require.Equal(t, 3, rec.AttemptLimit)
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
}

func TestSeedSampleWorkflows_DurableRootWiring(t *testing.T) {
	e := durableSeedEnv(t)
	ds, closeRoot := e.boot(t)
	require.NoError(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo))
	wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
	rec := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
	req := workflow.ArrangementRequest{Revision: rec.Revision, Scope: workflow.Shared(), Name: durableSeedName, WorkflowID: wf.ID, Reason: rec.Reason, Manifest: rec.Manifest, Retained: rec.Retained, RequestedAt: rec.RequestedAt}
	assertDurableSeed(t, e, ds, req)
	before, err := os.ReadFile(e.sharedPath(durableSeedName))
	require.NoError(t, err)
	// A ledgered seed is untouched even when its source later becomes malformed.
	require.NoError(t, os.WriteFile(filepath.Join(e.samplesDir, durableSeedName, "workflow.json"), []byte("not JSON"), 0600))
	require.NoError(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo))
	closeRoot()
	ds, _ = e.boot(t)
	require.NoError(t, seedWithRoot(t, e, ds, ds.workflowRepo, ds.layout.journal, ds.publishedRepo))
	after, err := os.ReadFile(e.sharedPath(durableSeedName))
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
	entries, err := ds.layout.journal.Entries(context.Background())
	require.NoError(t, err)
	require.Empty(t, entries)
	trees, err := ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(context.Background())
	require.NoError(t, err)
	require.Empty(t, trees)
}

// Obstruct the real receipt file rather than replacing journal durability with
// an in-memory error fake. The prepared input remains readable after repair.
type seedReceiptObstruction struct {
	workflow.RevisionJournal
	workspace string
}

func (j seedReceiptObstruction) MarkCommitted(ctx context.Context, revision string) error {
	if err := os.Mkdir(filepath.Join(j.workspace, ".arbitron-layout-journal", revision+".committed"), 0700); err != nil {
		return err
	}
	return j.RevisionJournal.MarkCommitted(ctx, revision)
}

func TestSeedSampleWorkflows_LiveDurableRecovery(t *testing.T) {
	for _, boundary := range []string{"receipt", "enqueue"} {
		t.Run(boundary, func(t *testing.T) {
			e := durableSeedEnv(t)
			bad := filepath.Join(e.samplesDir, "a-malformed")
			require.NoError(t, os.MkdirAll(bad, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(bad, "workflow.json"), []byte("not JSON"), 0600))
			ds, _ := e.boot(t)
			var journal workflow.RevisionJournal = ds.layout.journal
			if boundary == "receipt" {
				journal = seedReceiptObstruction{journal, e.workspaceDir}
			} else {
				_, err := ds.db.Exec(`CREATE TRIGGER seed_enqueue_outage BEFORE INSERT ON layout_revisions BEGIN SELECT RAISE(ABORT, 'seed queue unavailable'); END`)
				require.NoError(t, err)
			}
			require.NoError(t, seedWithRoot(t, e, ds, ds.workflowRepo, journal, ds.publishedRepo), "post-commit outages do not undo a saved seed")
			wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
			rev := wf.SavedRevision()
			raw, err := os.ReadFile(filepath.Join(e.workspaceDir, ".arbitron-layout-journal", rev+".json"))
			require.NoError(t, err)
			var req workflow.ArrangementRequest
			require.NoError(t, json.Unmarshal(raw, &req))
			require.Equal(t, 0, layoutE2ECountRows(t, ds.db, durableSeedName))
			ledger, err := filesystem.NewSeedLedger(e.workspaceDir).Seeded(context.Background())
			require.NoError(t, err)
			require.True(t, ledger[durableSeedName])
			require.NoFileExists(t, e.sharedPath("a-malformed"))
			require.False(t, ledger["a-malformed"])
			trees, err := ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(context.Background())
			require.NoError(t, err)
			require.Len(t, trees, 1)
			require.Equal(t, rev, trees[0].Revision)
			if boundary == "receipt" {
				require.NoError(t, os.Remove(filepath.Join(e.workspaceDir, ".arbitron-layout-journal", rev+".committed")))
			}
			entries, err := ds.layout.journal.Entries(context.Background())
			require.NoError(t, err)
			require.Len(t, entries, 1)
			wantState := workflow.JournalCommitted
			if boundary == "receipt" {
				wantState = workflow.JournalPrepared
			}
			require.Equal(t, wantState, entries[0].State)
			require.Equal(t, req, entries[0].Request)
			if boundary == "enqueue" {
				_, err := ds.db.Exec("DROP TRIGGER seed_enqueue_outage")
				require.NoError(t, err)
			}
			// No explicit Recover, resave, restart or second Seed call: the root's
			// production polling loop must discover and consume the saved input.
			require.Eventually(t, func() bool {
				rec, err := ds.layout.store.Get(context.Background(), rev)
				return err == nil && rec.State.Terminal()
			}, 25*time.Second, 20*time.Millisecond)
			assertDurableSeed(t, e, ds, req)
			require.NoError(t, ds.layout.worker.Recover(context.Background()))
			require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
			entries, err = ds.layout.journal.Entries(context.Background())
			require.NoError(t, err)
			require.Empty(t, entries)
			trees, err = ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(context.Background())
			require.NoError(t, err)
			require.Empty(t, trees)
		})
	}
}

// All persistence delegates to the root's actual adapters. These wrappers only
// hold the writer at the precise boundary at which the parent kills the process.
type seedCrashRepo struct {
	*filesystem.WorkflowRepo
	barrier layoutCrashBarrier
}

func (r seedCrashRepo) WriteFile(ctx context.Context, scope workflow.Scope, name, rel string, data []byte) error {
	err := r.WorkflowRepo.WriteFile(ctx, scope, name, rel, data)
	if err == nil && r.barrier.stage == "partial-tree" && rel == "support.txt" {
		r.barrier.pause()
	}
	return err
}

type seedCrashJournal struct{ layoutCrashJournal }

type seedCrashRegistry struct {
	workflow.Registry
	barrier layoutCrashBarrier
}

func (r seedCrashRegistry) Register(ctx context.Context, entry workflow.RegistryEntry) error {
	err := r.Registry.Register(ctx, entry)
	if err == nil && r.barrier.stage == "identity" {
		r.barrier.pause()
	}
	return err
}

func (j seedCrashJournal) Remove(ctx context.Context, revision string) error {
	err := j.RevisionJournal.Remove(ctx, revision)
	if err == nil && j.barrier.stage == "before-ledger" {
		j.barrier.pause()
	}
	return err
}

func TestSeedSampleWorkflows_CrashProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_SEED_CRASH_CONFIG")
	if configPath == "" {
		return
	}
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	// Production starts its worker after initial seed capture. Recreate that
	// ordering while instrumenting the already-constructed root collaborators.
	ds.layout.worker.Stop()
	barrier := layoutCrashBarrier{root: filepath.Dir(configPath), stage: os.Getenv("ARBITRON_SEED_CRASH_STAGE")}
	repo := seedCrashRepo{ds.workflowRepo.(*filesystem.WorkflowRepo), barrier}
	journal := seedCrashJournal{layoutCrashJournal{ds.layout.journal, barrier}}
	stamper, err := workflow.NewRevisionStamper(repo, workflow.StamperDeps{Journal: journal, Requester: layoutCrashRequester{ds.layout.requester, barrier}, Displayed: ds.layout.store, Settler: ds.layout.commits})
	require.NoError(t, err)
	source := filepath.Join(barrier.root, "samples")
	registry := seedCrashRegistry{adaptsqlite.NewWorkflowIdentityRepo(ds.db), barrier}
	err = seedSampleWorkflows(context.Background(), source, ds.cfg.Workspace.Path, repo, layoutCrashPublications{ds.publishedRepo, barrier}, stamper, ds.layout.mutations, registry, localDefaultUser, layoutE2EDiscardLogger())
	require.NoError(t, err)
	t.Fatal("seed never reached kill barrier")
}

func killSeedAt(t *testing.T, e *layoutE2EEnv, stage string) workflow.ArrangementRequest {
	t.Helper()
	cmd := startSeedAt(t, e, stage)
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	return capturedSeedRequest(t, e)
}

// startSeedAt leaves a live seeding peer paused, holding its destination lock,
// at stage; the caller kills it.
func startSeedAt(t *testing.T, e *layoutE2EEnv, stage string) *exec.Cmd {
	t.Helper()
	timeout, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestSeedSampleWorkflows_CrashProcess$")
	cmd.Env = append(os.Environ(), "ARBITRON_SEED_CRASH_CONFIG="+e.configPath, "ARBITRON_SEED_CRASH_STAGE="+stage)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "crash-ready")); return err == nil }, 10*time.Second, 10*time.Millisecond)
	return cmd
}

func capturedSeedRequest(t *testing.T, e *layoutE2EEnv) workflow.ArrangementRequest {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(e.root, "captured.json"))
	require.NoError(t, err)
	var req workflow.ArrangementRequest
	require.NoError(t, json.Unmarshal(raw, &req))
	return req
}

func TestSeedSampleWorkflows_KillAndRecover(t *testing.T) {
	for _, stage := range []string{"prepare", "identity", "partial-tree", "before-proof", "after-proof", "receipt", "enqueue", "before-ledger"} {
		t.Run(stage, func(t *testing.T) {
			e := durableSeedEnv(t)
			req := killSeedAt(t, e, stage)
			if stage == "prepare" || stage == "identity" {
				require.NoFileExists(t, e.sharedPath(durableSeedName))
			} else {
				require.Equal(t, req.Revision, layoutE2ESavedRevision(t, e.sharedPath(durableSeedName)))
				if stage == "partial-tree" {
					require.FileExists(t, filepath.Join(filepath.Dir(e.sharedPath(durableSeedName)), "support.txt"))
					require.NoFileExists(t, filepath.Join(filepath.Dir(e.sharedPath(durableSeedName)), "z-last.txt"))
				}
			}
			// The restart is the production boot: initDomain settles, then its seed
			// switch resumes or reseeds. Proven commits keep their immutable revision
			// and finish the ledger; an abandoned attempt must release its exact
			// identity so the same boot seeds the real source anew.
			t.Setenv("ARBITRON_SAMPLES_SEED", "true")
			ds, _ := e.boot(t)
			ctx := context.Background()
			committed := stage == "after-proof" || stage == "receipt" || stage == "enqueue" || stage == "before-ledger"
			if committed {
				assertDurableSeed(t, e, ds, req)
				require.Equal(t, req.Revision, layoutE2EWaitTerminal(t, ds, req.Revision).Revision)
			} else {
				wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
				require.NotEqual(t, req.Revision, wf.SavedRevision())
				require.NotEqual(t, req.WorkflowID, wf.ID)
				rec := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
				fresh := workflow.ArrangementRequest{Revision: rec.Revision, Scope: workflow.Shared(), Name: durableSeedName, WorkflowID: wf.ID, Reason: rec.Reason, Manifest: rec.Manifest, Retained: rec.Retained, RequestedAt: rec.RequestedAt}
				assertDurableSeed(t, e, ds, fresh)
				_, err := ds.layout.store.Get(ctx, req.Revision)
				require.ErrorIs(t, err, layout.ErrRecordNotFound, "abandoned attempt never arranges")
			}
			bootLog, err := os.ReadFile(e.logPath)
			require.NoError(t, err)
			require.NotContains(t, string(bootLog), "sample workflow seeding failed", "boot must not continue without the interrupted sample")
			require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
			require.NoError(t, ds.layout.worker.Recover(ctx))
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries)
			trees, err := ds.workflowRepo.(workflow.TreeCommitter).PendingTreeCommits(ctx)
			require.NoError(t, err)
			require.Empty(t, trees)
			ledgerBytes, err := os.ReadFile(filepath.Join(e.workspaceDir, ".samples_seeded"))
			require.NoError(t, err, "boot must resume or reseed the interrupted sample and record it")
			require.Equal(t, durableSeedName+"\n", string(ledgerBytes))
		})
	}
}

// Only Record fails: source, ledger reads, journal cleanup and SQLite acceptance
// remain real, so a second infrastructure failure cannot conceal this window.
type seedLedgerOutage struct {
	samples.Ledger
	err error
}

func (l seedLedgerOutage) Record(context.Context, string) error { return l.err }

func TestSeedSampleWorkflows_LedgerOnlyFailureResumesAfterRecovery(t *testing.T) {
	for _, boundary := range []string{"acknowledged", "worker-recovery"} {
		t.Run(boundary, func(t *testing.T) {
			e := durableSeedEnv(t)
			ds, closeRoot := e.boot(t)
			ctx := context.Background()
			if boundary == "worker-recovery" {
				_, err := ds.db.Exec(`CREATE TRIGGER seed_enqueue_outage BEFORE INSERT ON layout_revisions BEGIN SELECT RAISE(ABORT, 'seed queue unavailable'); END`)
				require.NoError(t, err)
			}
			boom := errors.New("seed ledger append unavailable")
			seeder := samples.NewSeeder(filesystem.NewSampleSource(e.samplesDir), ds.workflowRepo, ds.publishedRepo,
				seedLedgerOutage{filesystem.NewSeedLedger(e.workspaceDir), boom}, localDefaultUser, layoutE2EDiscardLogger())
			seeder.SetRevisionStamper(ds.layout.stamper)
			seeder.SetMutationCoordinator(ds.layout.mutations)
			seeder.SetRegistry(adaptsqlite.NewWorkflowIdentityRepo(ds.db))
			require.ErrorIs(t, seeder.Seed(ctx), boom)
			before, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)
			wf := layoutE2EManifest(t, e.sharedPath(durableSeedName))
			ledger, err := filesystem.NewSeedLedger(e.workspaceDir).Seeded(ctx)
			require.NoError(t, err)
			require.False(t, ledger[durableSeedName])
			if boundary == "worker-recovery" {
				_, err := ds.db.Exec("DROP TRIGGER seed_enqueue_outage")
				require.NoError(t, err)
			}
			require.NoError(t, ds.layout.worker.Recover(ctx))
			original := layoutE2EWaitTerminal(t, ds, wf.SavedRevision())
			entries, err := ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries, "resume must survive both acknowledgement cleanup paths")
			var captured workflow.Workflow
			require.NoError(t, json.Unmarshal(original.Manifest, &captured))
			require.Equal(t, *wf, captured, "the acknowledged input must be the complete committed seed, not a reloaded source")
			require.Equal(t, workflow.SaveReasonSeed, original.Reason)
			require.Equal(t, wf.Presentation.SavedAt, original.RequestedAt)
			require.Equal(t, wf.Presentation.Retained, original.Retained)
			require.Equal(t, layout.Destination{Scope: workflow.Shared(), Name: durableSeedName, WorkflowID: wf.ID}, original.Destination)
			require.Equal(t, 3, original.AttemptLimit)
			closeRoot()
			// Resume must use the original captured input even after source changes.
			require.NoError(t, os.WriteFile(filepath.Join(e.samplesDir, durableSeedName, "workflow.json"), []byte("now malformed"), 0600))
			t.Setenv("ARBITRON_SAMPLES_SEED", "true")
			ds, closeRoot = e.boot(t)
			ledgerBytes, err := os.ReadFile(filepath.Join(e.workspaceDir, ".samples_seeded"))
			require.NoError(t, err, "a committed seed must finish its ledger append on restart")
			require.Equal(t, durableSeedName+"\n", string(ledgerBytes))
			after, err := os.ReadFile(e.sharedPath(durableSeedName))
			require.NoError(t, err)
			require.Equal(t, before, after)
			recovered, err := ds.layout.store.Get(ctx, wf.SavedRevision())
			require.NoError(t, err)
			require.Equal(t, original, recovered, "ledger repair must not rerun or reset arrangement")
			proof, present, err := ds.publishedRepo.PublicationOf(ctx, durableSeedName)
			require.NoError(t, err)
			require.True(t, present)
			require.Equal(t, original.Revision, proof.Revision)
			require.Equal(t, wf.ID, proof.WorkflowID)
			require.Equal(t, localDefaultUser, proof.Publisher)
			require.Equal(t, durableSeedName, proof.SourceName)
			entries, err = ds.layout.journal.Entries(ctx)
			require.NoError(t, err)
			require.Empty(t, entries, "resumed acknowledgement must clean its restored receipt")
			support, err := os.ReadFile(filepath.Join(filepath.Dir(e.sharedPath(durableSeedName)), "support.txt"))
			require.NoError(t, err)
			require.Equal(t, "real sample supporting content", string(support))

			closeRoot()
			ds, _ = e.boot(t)
			ledgerBytes, err = os.ReadFile(filepath.Join(e.workspaceDir, ".samples_seeded"))
			require.NoError(t, err)
			require.Equal(t, durableSeedName+"\n", string(ledgerBytes))
			require.Equal(t, 1, layoutE2ECountRows(t, ds.db, durableSeedName))
		})
	}
}
