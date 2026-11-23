package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/modelidentity"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var regNow = time.Date(2026, 11, 20, 9, 30, 0, 0, time.UTC)

func builtinRunner(limit int) runner.Runner {
	return runner.Runner{ID: runner.BuiltInRunnerID, Name: "builtin", BuiltIn: true, StepLimit: limit,
		Labels: map[string]string{"site": "hq"}, Compat: runner.Compatibility{State: runner.CompatCompatible},
		Liveness: runner.LivenessOnline, OS: "darwin", RunnerVersion: "1.0.0",
		HarnessVersions: map[string]string{"claude": "2.1.3"}, WorkspaceRoot: "/srv/ws", CreatedAt: regNow}
}

func newRegistry(t *testing.T) (*RunnerRegistryRepo, testDB) {
	db := setupTestDB(t)
	repo := NewRunnerRegistryRepo(db.DB)
	repo.now = func() time.Time { return regNow }
	return repo, db
}

func subSource(kind runner.CredentialKind, ref string, fam modelidentity.Family) runner.Source {
	return runner.Source{ID: runner.MakeSourceID(runner.BuiltInRunnerID, kind, ref), RunnerID: runner.BuiltInRunnerID,
		Kind: kind, CredentialRef: ref, Families: []modelidentity.Family{fam}, Backends: []string{"", "claude-code"}}
}

func TestRunnerRegistryRepo_UpsertAndGet(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))

	got, sources, err := repo.GetRunner(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Empty(t, sources)
	assert.Equal(t, builtinRunner(8), got)

	list, err := repo.ListRunners(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)

	_, _, err = repo.GetRunner(ctx, "nope")
	assert.ErrorIs(t, err, runner.ErrRunnerNotFound)
}

func TestRunnerRegistryRepo_UpsertPreservesOperatorConfig(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	four := 4
	_, err := repo.UpdateConfig(ctx, runner.BuiltInRunnerID, runner.RunnerConfigPatch{StepLimit: &four, Labels: map[string]string{"gpu": "none"}})
	require.NoError(t, err)
	require.NoError(t, repo.SetCordoned(ctx, runner.BuiltInRunnerID, true))
	require.NoError(t, repo.SetDraining(ctx, runner.BuiltInRunnerID, true))

	again := builtinRunner(8)
	again.RunnerVersion = "1.1.0"
	again.Liveness = runner.LivenessOnline
	require.NoError(t, repo.UpsertRunner(ctx, again), "boot re-registration")

	got, _, err := repo.GetRunner(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, 4, got.StepLimit, "operator step limit survives re-registration")
	assert.Equal(t, map[string]string{"gpu": "none"}, got.Labels)
	assert.True(t, got.Cordoned)
	assert.True(t, got.Draining)
	assert.Equal(t, "1.1.0", got.RunnerVersion, "machine facts are refreshed")
}

func TestRunnerRegistryRepo_ValidationAndNotFound(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	assert.ErrorIs(t, repo.UpsertRunner(ctx, builtinRunner(0)), runner.ErrInvalidStepLimit)
	assert.ErrorIs(t, repo.UpsertRunner(ctx, builtinRunner(257)), runner.ErrInvalidStepLimit)
	bad := builtinRunner(8)
	bad.ID = ""
	assert.Error(t, repo.UpsertRunner(ctx, bad))

	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	zero := 0
	_, err := repo.UpdateConfig(ctx, runner.BuiltInRunnerID, runner.RunnerConfigPatch{StepLimit: &zero})
	assert.ErrorIs(t, err, runner.ErrInvalidStepLimit)
	_, err = repo.UpdateConfig(ctx, "ghost", runner.RunnerConfigPatch{})
	assert.ErrorIs(t, err, runner.ErrRunnerNotFound)
	assert.ErrorIs(t, repo.SetCordoned(ctx, "ghost", true), runner.ErrRunnerNotFound)
	assert.ErrorIs(t, repo.SetDraining(ctx, "ghost", true), runner.ErrRunnerNotFound)
	assert.ErrorIs(t, repo.SetLiveness(ctx, "ghost", runner.LivenessLost, regNow), runner.ErrRunnerNotFound)
	assert.ErrorIs(t, repo.SetCompat(ctx, "ghost", runner.Compatibility{State: runner.CompatIncompatible}), runner.ErrRunnerNotFound)
	assert.ErrorIs(t, repo.ReplaceSources(ctx, "ghost", nil), runner.ErrRunnerNotFound)
	assert.ErrorIs(t, repo.RecordHealth(ctx, "builtin/none/-", runner.SourceHealth{}), runner.ErrSourceNotFound)
	_, err = repo.GetSource(ctx, "builtin/none/-")
	assert.ErrorIs(t, err, runner.ErrSourceNotFound)
}

func TestRunnerRegistryRepo_LivenessAndCompat(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	at := regNow.Add(time.Minute)
	require.NoError(t, repo.SetLiveness(ctx, runner.BuiltInRunnerID, runner.LivenessSuspect, at))
	require.NoError(t, repo.SetCompat(ctx, runner.BuiltInRunnerID, runner.Compatibility{State: runner.CompatChatOnly, Reason: "claude 2.0 < 2.1"}))
	got, _, err := repo.GetRunner(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	assert.Equal(t, runner.LivenessSuspect, got.Liveness)
	require.NotNil(t, got.LastContact)
	assert.Equal(t, at, *got.LastContact)
	assert.Equal(t, runner.Compatibility{State: runner.CompatChatOnly, Reason: "claude 2.0 < 2.1"}, got.Compat)
}

func TestRunnerRegistryRepo_ReplaceSourcesKeepsHealthForUnchangedIDs(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	anth := subSource(runner.CredAnthropicSubscription, "", "anthropic")
	lr := subSource(runner.CredLunaRoute, "lr-a", "openai")
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{anth, lr}))

	reset := regNow.Add(3 * time.Hour)
	exhausted := runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityExhausted, ResetAt: &reset, LastOutcome: runner.OutcomeExhausted, Detail: "exhausted until reset"}
	require.NoError(t, repo.RecordHealth(ctx, anth.ID, exhausted))

	anth.Families = []modelidentity.Family{"anthropic"}
	anth.Models = []string{"claude-sonnet-4-5"}
	anth.Health = runner.SourceHealth{} // the inventory never resets health
	azure := subSource(runner.CredServerConnection, "azure", "openai")
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{anth, azure}))

	_, sources, err := repo.GetRunner(ctx, runner.BuiltInRunnerID)
	require.NoError(t, err)
	byID := map[runner.SourceID]runner.Source{}
	for _, s := range sources {
		byID[s.ID] = s
	}
	require.Len(t, byID, 2, "lr-a was removed")
	assert.Equal(t, exhausted, byID[anth.ID].Health, "health survives re-inventory")
	assert.Equal(t, []string{"claude-sonnet-4-5"}, byID[anth.ID].Models, "advertised facts are refreshed")
	assert.Equal(t, runner.SourceHealth{Auth: runner.AuthUnknown, Capacity: runner.CapacityAvailable}, byID[azure.ID].Health, "new sources start unknown/available")

	mismatched := azure
	mismatched.ID = "builtin/server_connection/other"
	assert.Error(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{mismatched}), "a source ID must be its identity")
}

func TestRunnerRegistryRepo_TwoConnectionsNeverShareHealth(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	a := subSource(runner.CredServerConnection, "conn-a", "openai")
	b := subSource(runner.CredServerConnection, "conn-b", "openai")
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{a, b}))
	require.NotEqual(t, a.ID, b.ID)

	until := regNow.Add(time.Minute)
	require.NoError(t, repo.RecordHealth(ctx, a.ID, runner.SourceHealth{Auth: runner.AuthLoggedOut, Capacity: runner.CapacityAvailable, Congested: true, CongestedUntil: &until, LastOutcome: runner.OutcomeLoggedOut}))

	gotA, err := repo.GetSource(ctx, a.ID)
	require.NoError(t, err)
	gotB, err := repo.GetSource(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, runner.AuthLoggedOut, gotA.Health.Auth)
	assert.Equal(t, runner.SourceHealth{Auth: runner.AuthUnknown, Capacity: runner.CapacityAvailable}, gotB.Health, "same family, different connection: untouched")
}

func TestRunnerRegistryRepo_HealthRoundTripKeepsUnknownDistinctFromZero(t *testing.T) {
	repo, _ := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	src := subSource(runner.CredOpenAISubscription, "", "openai")
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{src}))

	zero, used := 0.0, 42.5
	reset := regNow.Add(5 * time.Hour)
	h := runner.SourceHealth{Auth: runner.AuthOK, Capacity: runner.CapacityAvailable, Headroom: &zero,
		Windows:     []runner.LimitWindow{{LimitID: "codex", ModelScope: "gpt-5.1", DurationMins: 300, UsedPercent: &used, ResetAt: &reset}},
		LastProbeAt: &regNow, LastOutcome: runner.OutcomeNone}
	require.NoError(t, repo.RecordHealth(ctx, src.ID, h))
	got, err := repo.GetSource(ctx, src.ID)
	require.NoError(t, err)
	assert.Equal(t, h, got.Health)
	require.NotNil(t, got.Health.Headroom)
	assert.Equal(t, 0.0, *got.Health.Headroom, "a known zero stays zero")

	h.Headroom = nil
	require.NoError(t, repo.RecordHealth(ctx, src.ID, h))
	got, err = repo.GetSource(ctx, src.ID)
	require.NoError(t, err)
	assert.Nil(t, got.Health.Headroom, "unknown stays unknown")
}

func insertJobRow(t *testing.T, db testDB, runnerID, sourceID, state string) {
	t.Helper()
	_, err := db.DB.Exec(`INSERT INTO runner_jobs (id, run_id, invocation_id, operation_id, attempt, node_id, op, group_kind, runner_id, source_id, generation, state, created_at, updated_at)
		VALUES (?, ?, ?, 'node', 1, 'n', 'chat', 'anthropic_subscription', NULLIF(?, ''), NULLIF(?, ''), 1, ?, '2026-11-20T00:00:00Z', '2026-11-20T00:00:00Z')`,
		uuid.NewString(), uuid.NewString(), uuid.NewString(), runnerID, sourceID, state)
	require.NoError(t, err)
}

func TestRunnerRegistryRepo_SnapshotCountsOnlyOfferedAndRunning(t *testing.T) {
	repo, db := newRegistry(t)
	ctx := context.Background()
	require.NoError(t, repo.UpsertRunner(ctx, builtinRunner(8)))
	src := subSource(runner.CredAnthropicSubscription, "", "anthropic")
	require.NoError(t, repo.ReplaceSources(ctx, runner.BuiltInRunnerID, []runner.Source{src}))

	insertJobRow(t, db, "", "", "queued") // a waiting intent holds no slot
	insertJobRow(t, db, "builtin", string(src.ID), "offered")
	insertJobRow(t, db, "builtin", string(src.ID), "running")
	insertJobRow(t, db, "builtin", "", "running") // process step: slot, no source
	insertJobRow(t, db, "builtin", string(src.ID), "completed")
	insertJobRow(t, db, "builtin", string(src.ID), "failed")
	insertJobRow(t, db, "builtin", string(src.ID), "fenced")

	snap, err := repo.Snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snap.Runners, 1)
	require.Len(t, snap.Sources, 1)
	assert.Equal(t, 3, snap.SlotsInUse[runner.BuiltInRunnerID])
	assert.Equal(t, 2, snap.SourceInFlight[src.ID])
	assert.NotNil(t, snap.Telemetry, "filled by the caller; never nil")
	assert.NotNil(t, snap.Homes)
	assert.True(t, snap.Now.IsZero(), "the dispatcher stamps Now from its clock")
}
