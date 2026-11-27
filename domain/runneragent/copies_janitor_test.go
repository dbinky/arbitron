package runneragent_test

import (
	"context"
	"crypto/ed25519"
	"sync"
	"testing"
	"time"

	"github.com/arbitron/arbitron/domain/runneragent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeJanitor struct {
	mu      sync.Mutex
	held    []runneragent.HeldCopy
	deletes chan runneragent.DeleteCopy
}

func (j *fakeJanitor) HeldCopies(context.Context) ([]runneragent.HeldCopy, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]runneragent.HeldCopy(nil), j.held...), nil
}
func (j *fakeJanitor) DeleteCopy(_ context.Context, runID string, upTo int64) (bool, error) {
	j.deletes <- runneragent.DeleteCopy{RunID: runID, HomeGeneration: upTo}
	return true, nil
}

func TestAgent_HelloReportsCopiesAndAppliesDeleteCopy(t *testing.T) {
	run := uuid.NewString()
	j := &fakeJanitor{held: []runneragent.HeldCopy{{RunID: run, HomeGeneration: 3}}, deletes: make(chan runneragent.DeleteCopy, 4)}
	h := newHarness(t, 16, withCopies(j))
	h.start()
	s := h.session()
	hello := admit(t, s, h.key.Public().(ed25519.PublicKey))
	assert.Equal(t, j.held, hello.Copies)
	s.push(runneragent.Inbound{Kind: runneragent.KindDeleteCopy, DeleteCopy: &runneragent.DeleteCopy{RunID: run, HomeGeneration: 2, Reason: "home_moved"}})
	select {
	case d := <-j.deletes:
		assert.Equal(t, run, d.RunID)
		assert.Equal(t, int64(2), d.HomeGeneration, "the named generation is passed through for the runner-side check")
	case <-time.After(5 * time.Second):
		t.Fatal("delete_copy was not applied")
	}
}
func TestAgent_WithoutJanitorReportsNoCopies(t *testing.T) {
	h := newHarness(t, 16)
	h.start()
	s := h.session()
	hello := admit(t, s, h.key.Public().(ed25519.PublicKey))
	require.Empty(t, hello.Copies)
}
