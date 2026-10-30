package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	adaptsqlite "github.com/arbitron/arbitron/adapter/sqlite"
	"github.com/arbitron/arbitron/domain/workflow"
	"github.com/stretchr/testify/require"
)

type seedPrepareProbe struct {
	workflow.RevisionJournal
	root string
}

func (j seedPrepareProbe) Record(ctx context.Context, req workflow.ArrangementRequest) error {
	if err := j.RevisionJournal.Record(ctx, req); err != nil {
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(j.root, "prepared-"+req.Revision), data, 0600)
}

// Pause only after each Seeder has read the initially empty ledger and chosen
// this candidate. Both must then contend on the actual filesystem coordinator.
type seedRaceMutations struct {
	workflow.MutationCoordinator
	t          *testing.T
	root, peer string
}

func (m seedRaceMutations) WithMutations(ctx context.Context, targets []workflow.MutationTarget, fn func(context.Context) error) error {
	require.NoError(m.t, os.WriteFile(filepath.Join(m.root, "contending-"+m.peer), nil, 0600))
	require.Eventually(m.t, func() bool { _, err := os.Stat(filepath.Join(m.root, "race-lock-start")); return err == nil }, 10*time.Second, 10*time.Millisecond)
	return m.MutationCoordinator.WithMutations(ctx, targets, fn)
}

func TestSeedSampleWorkflows_RaceProcess(t *testing.T) {
	configPath := os.Getenv("ARBITRON_SEED_RACE_CONFIG")
	if configPath == "" {
		return
	}
	ds, err := initDomain(context.Background(), configPath)
	require.NoError(t, err)
	defer ds.Close(context.Background())
	// Match production's seed-before-worker order. Neither exiting peer may
	// claim this row and leave an interrupted lease for the survivor's probe.
	ds.layout.worker.Stop()
	root := filepath.Dir(configPath)
	require.NoError(t, os.WriteFile(filepath.Join(root, "ready-"+os.Getenv("ARBITRON_SEED_RACE_PEER")), nil, 0600))
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(root, "race-start")); return err == nil }, 10*time.Second, 10*time.Millisecond)
	stamper, err := workflow.NewRevisionStamper(ds.workflowRepo, workflow.StamperDeps{Journal: seedPrepareProbe{ds.layout.journal, root}, Requester: ds.layout.requester, Displayed: ds.layout.store, Settler: ds.layout.commits})
	require.NoError(t, err)
	mutations := seedRaceMutations{ds.layout.mutations, t, root, os.Getenv("ARBITRON_SEED_RACE_PEER")}
	require.NoError(t, seedSampleWorkflows(context.Background(), filepath.Join(root, "samples"), ds.cfg.Workspace.Path, ds.workflowRepo, ds.publishedRepo, stamper, mutations, adaptsqlite.NewWorkflowIdentityRepo(ds.db), nil, nil, localDefaultUser, layoutE2EDiscardLogger()))
}

func TestSeedSampleWorkflows_TwoProcessesCommitOnce(t *testing.T) {
	e := durableSeedEnv(t)
	// Initialize schema/migrations before starting peers; the race under test is
	// two already-booted seeders, not unrelated concurrent database migrations.
	_, closeRoot := e.boot(t)
	closeRoot()
	timeout, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var commands []*exec.Cmd
	for _, peer := range []string{"a", "b"} {
		cmd := exec.CommandContext(timeout, os.Args[0], "-test.run=^TestSeedSampleWorkflows_RaceProcess$")
		cmd.Env = append(os.Environ(), "ARBITRON_SEED_RACE_CONFIG="+e.configPath, "ARBITRON_SEED_RACE_PEER="+peer)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		require.NoError(t, cmd.Start())
		commands = append(commands, cmd)
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "ready-"+peer)); return err == nil }, 10*time.Second, 10*time.Millisecond)
	}
	require.NoError(t, os.WriteFile(filepath.Join(e.root, "race-start"), nil, 0600))
	for _, peer := range []string{"a", "b"} {
		require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(e.root, "contending-"+peer)); return err == nil }, 10*time.Second, 10*time.Millisecond)
	}
	require.NoError(t, os.WriteFile(filepath.Join(e.root, "race-lock-start"), nil, 0600))
	for _, cmd := range commands {
		require.NoError(t, cmd.Wait())
	}
	prepares, err := filepath.Glob(filepath.Join(e.root, "prepared-*"))
	require.NoError(t, err)
	require.Len(t, prepares, 1, "losing peer must reread the ledger under the shared destination lock, before stamping")
	raw, err := os.ReadFile(prepares[0])
	require.NoError(t, err)
	var req workflow.ArrangementRequest
	require.NoError(t, json.Unmarshal(raw, &req))
	ds, _ := e.boot(t)
	assertDurableSeed(t, e, ds, req)
	ledger, err := os.ReadFile(filepath.Join(e.workspaceDir, ".samples_seeded"))
	require.NoError(t, err)
	require.Equal(t, durableSeedName+"\n", string(ledger), "one ledger append across both processes")
}
