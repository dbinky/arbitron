package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/arbitron/arbitron/domain/instance"
	"github.com/arbitron/arbitron/domain/provider"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func acceptedIntent() engine.TurnIntent {
	run := uuid.New()
	return engine.TurnIntent{ID: uuid.New(), RequestID: uuid.New(), LeaseKey: engine.RunLeaseKey(run), RunID: run,
		InvocationID: uuid.New(), NodeID: "chat", TurnIndex: 1, Kind: engine.TurnIntentFirst,
		Prompt: "exact rendered prompt ✓", SystemPrompt: "sys", Nonce: "nonce", Model: "accepted-model",
		Tools: []string{"Read"}, SessionGeneration: 3, OperationID: "turn/1",
		Binding: engine.AcceptedTurnBinding{Kind: provider.ProfileKindAgent, Provider: "claude-cli", Model: "accepted-model", Backend: "codex", AllowedTools: []string{"Read"}, Effort: provider.Effort("high"), RequireExecutedModel: true}}
}

func TestTurnIntentRepo_DatabaseRejectsOverlappingPending(t *testing.T) {
	db := setupTestDB(t)
	repo := NewTurnIntentRepo(db.DB)
	in := acceptedIntent()
	require.NoError(t, repo.Persist(context.Background(), in))
	_, err := db.DB.ExecContext(context.Background(), `INSERT INTO turn_intents (id,request_id,lease_key,run_id,invocation_id,node_id,turn_index,kind,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, uuid.NewString(), uuid.NewString(), in.LeaseKey, in.RunID.String(), in.InvocationID.String(), in.NodeID, 2, "message", 1)
	require.Error(t, err)
}

func TestTurnIntentRepo_RequestPayloadCASAndPendingUniqueness(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTurnIntentRepo(db.DB)
	in := acceptedIntent()
	require.NoError(t, repo.Persist(ctx, in))
	require.NoError(t, repo.Persist(ctx, in), "same request is idempotent")
	changed := in
	changed.Message = "different message"
	require.Error(t, repo.Persist(ctx, changed))
	changed = in
	changed.Binding.Backend = "claude-code"
	require.Error(t, repo.Persist(ctx, changed))
	other := in
	other.ID, other.RequestID = uuid.New(), uuid.New()
	require.Error(t, repo.Persist(ctx, other), "one pending request per session")
	// Even direct writers must respect the unique pending constraint.
	_, err := db.DB.ExecContext(ctx, `INSERT INTO turn_intents (id,request_id,lease_key,run_id,invocation_id,node_id,turn_index,kind,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, uuid.NewString(), uuid.NewString(), in.LeaseKey, in.RunID.String(), in.InvocationID.String(), in.NodeID, 2, "message", 1)
	require.Error(t, err)
	require.NoError(t, repo.Complete(ctx, in.ID))
	require.NoError(t, repo.Persist(ctx, in), "retry after completion cannot reopen it")
	pending, err := repo.Pending(ctx)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, repo.Persist(ctx, other))
	got, err := repo.PendingFor(ctx, in.LeaseKey)
	require.NoError(t, err)
	require.Equal(t, other.RequestID, got.RequestID)
}

func TestTurnIntentRepo_InvalidIdentityAndKind(t *testing.T) {
	db := setupTestDB(t)
	repo := NewTurnIntentRepo(db.DB)
	ctx := context.Background()
	for _, mutate := range []func(*engine.TurnIntent){
		func(in *engine.TurnIntent) { in.ID = uuid.Nil },
		func(in *engine.TurnIntent) { in.RequestID = uuid.Nil },
		func(in *engine.TurnIntent) { in.Kind = "invalid" },
		func(in *engine.TurnIntent) { at := time.Now(); in.CompletedAt = &at },
	} {
		in := acceptedIntent()
		mutate(&in)
		require.Error(t, repo.Persist(ctx, in))
	}
	require.ErrorIs(t, repo.Complete(ctx, uuid.New()), instance.ErrNotFound)
}

func TestTurnIntentRepo_PersistPendingComplete(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	repo := NewTurnIntentRepo(db.DB)
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	repo.SetClock(func() time.Time { return at })
	first := acceptedIntent()
	require.NoError(t, repo.Persist(ctx, first))
	got, err := repo.PendingFor(ctx, first.LeaseKey)
	require.NoError(t, err)
	require.NotNil(t, got)
	first.CreatedAt = at
	require.Equal(t, first, *got)
	// The database, not an in-memory cache, is the replay source.
	reopened := NewTurnIntentRepo(db.DB)
	pending, err := reopened.Pending(ctx)
	require.NoError(t, err)
	require.Equal(t, []engine.TurnIntent{first}, pending)
	require.NoError(t, repo.Complete(ctx, first.ID))
	require.NoError(t, repo.Complete(ctx, first.ID))
	got, err = reopened.PendingFor(ctx, first.LeaseKey)
	require.NoError(t, err)
	require.Nil(t, got)
}
