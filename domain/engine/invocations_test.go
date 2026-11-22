package engine_test

import (
	"context"
	"testing"

	"github.com/arbitron/arbitron/domain/engine"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryInvocationStore_OpenVisitReusedUntilCommit(t *testing.T) {
	ctx := context.Background()
	s := engine.NewMemoryInvocationStore()
	run := uuid.New()

	first, err := s.Begin(ctx, run, "a")
	require.NoError(t, err)
	assert.Equal(t, 1, first.Visit)
	assert.Nil(t, first.CommittedAt)

	again, err := s.Begin(ctx, run, "a")
	require.NoError(t, err)
	assert.Equal(t, first.ID, again.ID, "an open visit is reused (restart / wake)")

	require.NoError(t, s.Commit(ctx, first.ID))
	require.NoError(t, s.Commit(ctx, first.ID), "commit is idempotent")
	got, err := s.Get(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, got.CommittedAt)

	next, err := s.Begin(ctx, run, "a")
	require.NoError(t, err)
	assert.NotEqual(t, first.ID, next.ID, "a back-edge revisit after commit is a new invocation")
	assert.Equal(t, 2, next.Visit)

	other, err := s.Begin(ctx, run, "b")
	require.NoError(t, err)
	assert.Equal(t, 1, other.Visit, "visits count per node")
}

func TestMemoryInvocationStore_Errors(t *testing.T) {
	ctx := context.Background()
	s := engine.NewMemoryInvocationStore()
	_, err := s.Get(ctx, uuid.New())
	assert.ErrorIs(t, err, engine.ErrInvocationNotFound)
	assert.ErrorIs(t, s.Commit(ctx, uuid.New()), engine.ErrInvocationNotFound)
	_, err = s.Begin(ctx, uuid.Nil, "a")
	assert.Error(t, err)
	_, err = s.Begin(ctx, uuid.New(), "")
	assert.Error(t, err)
}
