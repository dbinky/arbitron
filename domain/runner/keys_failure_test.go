package runner_test

import (
	"context"
	"errors"
	"github.com/arbitron/arbitron/domain/runner"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type failingKeyStore struct {
	runner.KeyStore
	err       error
	attempted runner.RunnerKey
}

func (s *failingKeyStore) Create(_ context.Context, k *runner.RunnerKey) error {
	s.attempted = *k
	return s.err
}

func TestKeyService_StoreFailureDoesNotAdmitOrRevealKey(t *testing.T) {
	f := newKeyFixture(t)
	store := &failingKeyStore{err: errors.New("disk full")}
	svc := runner.NewKeyService(runner.KeyServiceDeps{Store: store, Registry: f.reg, Audit: f.audit, Revoker: f.revoker, Clock: time.Now, DefaultStepLimit: 8})
	secret, k, err := svc.Create(adminCtx(), "mac-a")
	require.ErrorIs(t, err, store.err)
	require.Empty(t, secret)
	require.Empty(t, k)
	require.NotEmpty(t, store.attempted.Hash)
	_, _, err = f.reg.GetRunner(context.Background(), store.attempted.RunnerID)
	require.ErrorIs(t, err, runner.ErrRunnerNotFound)
	require.Empty(t, f.audit.actions())
	require.Empty(t, f.revoker.ids)
}
